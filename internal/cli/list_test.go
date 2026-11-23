package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// decodePage decodes a list command's --json envelope, failing the test if the
// document is not the shape every listing emits.
func decodePage[T any](t *testing.T, data []byte) listPage[T] {
	t.Helper()

	var page listPage[T]
	if err := json.Unmarshal(data, &page); err != nil {
		t.Fatalf("output was not a list document: %v (%q)", err, data)
	}
	return page
}

// namedFilter is the filter the listFilter unit tests exercise: a plain
// name-and-note pair standing in for any of the CLI's row types.
type namedRow struct {
	Name string `json:"name"`
	Note string `json:"note"`
}

func newNamedFilter(limit int) *listFilter[namedRow] {
	return &listFilter[namedRow]{
		noun:         "rows",
		fields:       "name or note",
		defaultLimit: limit,
		matches: func(r namedRow, needle string) bool {
			return containsFold(needle, r.Name, r.Note)
		},
	}
}

func namedRows(names ...string) []namedRow {
	rows := make([]namedRow, 0, len(names))
	for _, n := range names {
		rows = append(rows, namedRow{Name: n})
	}
	return rows
}

// TestListFilterSearchesEveryDocumentedField checks the promise the --search
// help makes: every field it names is searched, and case never matters.
func TestListFilterSearchesEveryDocumentedField(t *testing.T) {
	rows := []namedRow{
		{Name: "ifInOctets", Note: "interface counter"},
		{Name: "sysDescr", Note: "system description"},
	}

	cases := []struct {
		name   string
		search string
		want   string
	}{
		{name: "name", search: "ifInOctets", want: "ifInOctets"},
		{name: "name lowercased", search: "ifinoctets", want: "ifInOctets"},
		{name: "name uppercased", search: "IFINOCTETS", want: "ifInOctets"},
		{name: "name substring", search: "octet", want: "ifInOctets"},
		{name: "note", search: "system description", want: "sysDescr"},
		{name: "note mixed case", search: "SyStEm", want: "sysDescr"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			filter := newNamedFilter(0)
			filter.search = tc.search

			page := filter.apply(rows)
			if len(page.Items) != 1 || page.Items[0].Name != tc.want {
				t.Fatalf("search %q selected %+v, want only %s", tc.search, page.Items, tc.want)
			}
			if page.Total != 2 || page.Matched != 1 {
				t.Errorf("counts = %+v, want 1 matched of 2", page.listCounts)
			}
		})
	}
}

// TestListFilterSearchNoMatchesReportsAnEmptySet checks that a search matching
// nothing is reported as an empty set rather than silently falling back to the
// unsearched list.
func TestListFilterSearchNoMatchesReportsAnEmptySet(t *testing.T) {
	filter := newNamedFilter(0)
	filter.search = "nothing-matches-this"

	page := filter.apply(namedRows("alpha", "beta"))
	if len(page.Items) != 0 {
		t.Fatalf("items = %+v, want none", page.Items)
	}
	if page.Total != 2 || page.Matched != 0 || page.Shown != 0 {
		t.Errorf("counts = %+v, want 0 matched of 2", page.listCounts)
	}
	if page.Truncated {
		t.Error("an empty result is not a truncated one")
	}

	// An empty page must still encode as [], so a consumer can range over it.
	encoded, err := json.Marshal(page)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(encoded), `"items":[]`) {
		t.Errorf("empty listing must encode items as []: %s", encoded)
	}
}

// TestListFilterAppliesSearchBeforeLimit checks the order the two flags compose
// in. The cap has to act on what the search kept, and the counts have to
// describe the searched set: "showing 2 of 3" must mean 3 matches, not 5 rows.
func TestListFilterAppliesSearchBeforeLimit(t *testing.T) {
	filter := newNamedFilter(0)
	filter.search = "if"
	filter.limit = 2

	page := filter.apply(namedRows("ifInOctets", "ifOutOctets", "ifSpeed", "sysDescr", "sysUpTime"))

	if page.Total != 5 {
		t.Errorf("Total = %d, want the 5 rows the server returned", page.Total)
	}
	if page.Matched != 3 {
		t.Errorf("Matched = %d, want the 3 rows --search kept", page.Matched)
	}
	if page.Shown != 2 || len(page.Items) != 2 {
		t.Fatalf("Shown = %d, want the 2 rows --limit allowed", page.Shown)
	}
	if !page.Truncated {
		t.Error("Truncated must be set when --limit held rows back")
	}
	if page.Items[0].Name != "ifInOctets" || page.Items[1].Name != "ifOutOctets" {
		t.Errorf("items = %+v, want the first two matches in order", page.Items)
	}

	out := &bytes.Buffer{}
	page.note(out, "")
	// The notice counts the searched total, not the module total: telling the
	// reader "2 of 5" here would send them looking for rows the search removed.
	if got := out.String(); got != "showing 2 of 3 rows; use --limit 0 for all\n" {
		t.Errorf("note = %q, want the searched total", got)
	}
}

// TestListFilterZeroLimitPrintsEverything checks that --limit 0 is the escape
// hatch the truncation notice advertises, and that it prints no notice itself.
func TestListFilterZeroLimitPrintsEverything(t *testing.T) {
	filter := newNamedFilter(50)
	filter.limit = 0

	page := filter.apply(namedRows("a", "b", "c"))
	if page.Shown != 3 || page.Truncated {
		t.Fatalf("counts = %+v, want all 3 rows and no truncation", page.listCounts)
	}

	out := &bytes.Buffer{}
	page.note(out, "")
	if out.Len() != 0 {
		t.Errorf("an untruncated listing must print no notice, got %q", out.String())
	}
}

// TestListFilterValidateRejectsNegativeLimit checks that a nonsensical --limit
// is a typed error rather than a silently ignored value.
func TestListFilterValidateRejectsNegativeLimit(t *testing.T) {
	for _, limit := range []int{-1, -50} {
		filter := newNamedFilter(0)
		filter.limit = limit

		err := filter.validate()

		var limitErr *LimitError
		if !errors.As(err, &limitErr) {
			t.Fatalf("validate(%d) = %v, want a *LimitError", limit, err)
		}
		if limitErr.Limit != limit {
			t.Errorf("LimitError.Limit = %d, want %d", limitErr.Limit, limit)
		}
		// The message has to name the flag and the way out of the error.
		if !strings.Contains(limitErr.Error(), "--limit") {
			t.Errorf("error %q must name the flag", limitErr)
		}
	}
}

// TestListFilterValidateAcceptsUsableLimits is the success half: zero and any
// positive cap are legal and must not be rejected.
func TestListFilterValidateAcceptsUsableLimits(t *testing.T) {
	for _, limit := range []int{0, 1, 50, 100000} {
		filter := newNamedFilter(0)
		filter.limit = limit

		if err := filter.validate(); err != nil {
			t.Errorf("validate(%d) = %v, want nil", limit, err)
		}
	}
}

// TestListPageWriteEmptyDistinguishesCauses checks that the two ways a listing
// can be empty read differently. "Nothing is configured" is a state to fix and
// "nothing matched" is a query to widen; the same words for both would tell the
// reader neither.
func TestListPageWriteEmptyDistinguishesCauses(t *testing.T) {
	t.Run("nothing configured", func(t *testing.T) {
		filter := newNamedFilter(0)
		page := filter.apply(nil)

		out := &bytes.Buffer{}
		if !page.writeEmpty(out, "", "No devices configured. Add one with: netmon device add") {
			t.Fatal("writeEmpty must report that it handled an empty listing")
		}
		if !strings.Contains(out.String(), "No devices configured") {
			t.Errorf("output = %q, want the caller's message", out.String())
		}
	})

	t.Run("search matched nothing", func(t *testing.T) {
		filter := newNamedFilter(0)
		filter.search = "zzz"
		page := filter.apply(namedRows("alpha", "beta"))

		out := &bytes.Buffer{}
		if !page.writeEmpty(out, "", "No devices configured.") {
			t.Fatal("writeEmpty must report that it handled an empty listing")
		}
		got := out.String()
		if strings.Contains(got, "No devices configured") {
			t.Errorf("a failed search must not claim nothing is configured: %q", got)
		}
		// The message has to echo the search and say how much was searched, so
		// the reader knows the rows exist and the query missed them.
		for _, want := range []string{"No rows match", `"zzz"`, "searched 2"} {
			if !strings.Contains(got, want) {
				t.Errorf("output %q missing %q", got, want)
			}
		}
	})

	t.Run("rows present", func(t *testing.T) {
		filter := newNamedFilter(0)
		page := filter.apply(namedRows("alpha"))

		out := &bytes.Buffer{}
		if page.writeEmpty(out, "", "No devices configured.") {
			t.Error("writeEmpty must not handle a listing that has rows")
		}
		if out.Len() != 0 {
			t.Errorf("writeEmpty wrote %q for a non-empty listing", out.String())
		}
	})
}

// TestWholeListIsNeverTruncated covers the nested lists a command deliberately
// prints in full, such as a conceptual table's columns.
func TestWholeListIsNeverTruncated(t *testing.T) {
	page := wholeList(namedRows("a", "b"), "columns")
	if page.Total != 2 || page.Matched != 2 || page.Shown != 2 || page.Truncated {
		t.Errorf("counts = %+v, want a complete 2-row listing", page.listCounts)
	}

	empty := wholeList[namedRow](nil, "columns")
	encoded, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(encoded), `"items":[]`) {
		t.Errorf("an empty whole list must encode items as []: %s", encoded)
	}
}

// TestContainsFoldIgnoresCaseAndEmptyValues covers the one place the CLI
// decides what a search means.
func TestContainsFoldIgnoresCaseAndEmptyValues(t *testing.T) {
	if !containsFold("octet", "ifInOCTETS", "") {
		t.Error("containsFold must ignore case")
	}
	if !containsFold("", "anything") {
		t.Error("an empty needle is contained by every value")
	}
	if containsFold("missing", "", "nothing here") {
		t.Error("containsFold matched a value it should not have")
	}
	if containsFold("anything") {
		t.Error("containsFold must not match when there are no values")
	}
}

// mibCatalog builds a module catalog of the given size, with a searchable
// name, description and tag on each entry.
func mibCatalog(count int) []client.MIB {
	mibs := make([]client.MIB, 0, count)
	for i := 0; i < count; i++ {
		mibs = append(mibs, client.MIB{
			ID:          int64(i + 1),
			Name:        "MODULE-" + strconv.Itoa(i) + "-MIB",
			Description: "module number " + strconv.Itoa(i),
			ObjectCount: i,
			Tags:        []string{"vendor" + strconv.Itoa(i%2)},
		})
	}
	return mibs
}

func mibCatalogHandler(t *testing.T, mibs []client.MIB) http.Handler {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/mibs", catalogHandler(t, mibs, nil))
	return mux
}

// TestMIBListSearchesNameDescriptionAndTags checks each field the command's
// help promises --search will look at.
func TestMIBListSearchesNameDescriptionAndTags(t *testing.T) {
	mibs := []client.MIB{
		{ID: 1, Name: "IF-MIB", Description: "Interfaces group", Tags: []string{"standard"}},
		{ID: 2, Name: "CISCO-ENVMON-MIB", Description: "Environmental monitor", Tags: []string{"cisco"}},
	}

	cases := []struct {
		name   string
		search string
		want   string
	}{
		{name: "by name", search: "cisco-env", want: "CISCO-ENVMON-MIB"},
		{name: "by description", search: "INTERFACES", want: "IF-MIB"},
		{name: "by tag", search: "standard", want: "IF-MIB"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, out := newFakeEnv(t, mibCatalogHandler(t, mibs))
			if err := runCommand(t, newMIBCommand(env), "list", "--search", tc.search); err != nil {
				t.Fatalf("mib list --search %q: %v", tc.search, err)
			}

			printed := out.String()
			if !strings.Contains(printed, tc.want) {
				t.Errorf("--search %q lost %s:\n%s", tc.search, tc.want, printed)
			}
			for _, m := range mibs {
				if m.Name != tc.want && strings.Contains(printed, m.Name) {
					t.Errorf("--search %q kept %s:\n%s", tc.search, m.Name, printed)
				}
			}
		})
	}
}

// TestMIBListTruncationNotice checks that a capped catalog says so, with the
// counts an operator needs to trust the output.
func TestMIBListTruncationNotice(t *testing.T) {
	env, out := newFakeEnv(t, mibCatalogHandler(t, mibCatalog(120)))

	if err := runCommand(t, newMIBCommand(env), "list", "--limit", "10"); err != nil {
		t.Fatalf("mib list --limit 10: %v", err)
	}

	printed := out.String()
	if !strings.Contains(printed, "showing 10 of 120 MIBs; use --limit 0 for all") {
		t.Errorf("a truncated catalog must say so:\n%s", printed)
	}
	if !strings.Contains(printed, "MODULE-9-MIB") {
		t.Errorf("the first ten modules should be printed:\n%s", printed)
	}
	if strings.Contains(printed, "MODULE-10-MIB") {
		t.Errorf("--limit was not applied:\n%s", printed)
	}
}

// TestMIBListSearchThenLimitCountsTheSearchedTotal is the combination case: the
// cap acts on the searched set, and the notice reports that set's size rather
// than the catalog's.
func TestMIBListSearchThenLimitCountsTheSearchedTotal(t *testing.T) {
	// 120 modules, 60 of which carry vendor1.
	env, out := newFakeEnv(t, mibCatalogHandler(t, mibCatalog(120)))

	err := runCommand(t, newMIBCommand(env), "list", "--search", "vendor1", "--limit", "5")
	if err != nil {
		t.Fatalf("mib list --search --limit: %v", err)
	}

	printed := out.String()
	if !strings.Contains(printed, "showing 5 of 60 MIBs") {
		t.Errorf("the notice must count the searched set, not the catalog:\n%s", printed)
	}
	if strings.Contains(printed, "of 120") {
		t.Errorf("the notice must not report the unsearched total:\n%s", printed)
	}
}

// TestMIBListUnlimitedPrintsNoNotice checks the escape hatch the notice
// advertises actually works and stays quiet.
func TestMIBListUnlimitedPrintsNoNotice(t *testing.T) {
	env, out := newFakeEnv(t, mibCatalogHandler(t, mibCatalog(120)))

	if err := runCommand(t, newMIBCommand(env), "list", "--limit", "0"); err != nil {
		t.Fatalf("mib list --limit 0: %v", err)
	}

	printed := out.String()
	if strings.Contains(printed, "showing") {
		t.Errorf("--limit 0 must print no truncation notice:\n%s", printed)
	}
	if !strings.Contains(printed, "MODULE-119-MIB") {
		t.Errorf("--limit 0 must print the last module:\n%s", printed)
	}
}

// TestMIBListSearchWithNoMatchesExplainsItself checks that a search matching
// nothing says so instead of printing a bare table header.
func TestMIBListSearchWithNoMatchesExplainsItself(t *testing.T) {
	env, out := newFakeEnv(t, mibCatalogHandler(t, mibCatalog(30)))

	if err := runCommand(t, newMIBCommand(env), "list", "--search", "no-such-module"); err != nil {
		t.Fatalf("mib list --search: %v", err)
	}

	printed := out.String()
	for _, want := range []string{"No MIBs match", "no-such-module", "searched 30"} {
		if !strings.Contains(printed, want) {
			t.Errorf("output missing %q:\n%s", want, printed)
		}
	}
	if strings.Contains(printed, "IMPORTED") {
		t.Errorf("an empty result must not print a bare table header:\n%s", printed)
	}
}

// TestMIBListRejectsNegativeLimitBeforeRequesting checks that a nonsensical cap
// fails as a typed error and, crucially, before the CLI calls the server.
func TestMIBListRejectsNegativeLimitBeforeRequesting(t *testing.T) {
	var requests int
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/mibs", func(w http.ResponseWriter, _ *http.Request) {
		requests++
		respondJSON(t, w, map[string]any{"mibs": mibCatalog(3)})
	})

	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newMIBCommand(env), "list", "--limit", "-1")

	var limitErr *LimitError
	if !errors.As(err, &limitErr) {
		t.Fatalf("err = %v, want a *LimitError", err)
	}
	if requests != 0 {
		t.Errorf("made %d request(s) for a limit that could never be honored", requests)
	}
}

// TestMIBListJSONSignalsTheSubset checks the contract --json owes a program:
// the document stays valid under truncation and says it is a subset.
func TestMIBListJSONSignalsTheSubset(t *testing.T) {
	env, out := newFakeEnv(t, mibCatalogHandler(t, mibCatalog(120)))
	env.Opts.JSON = true

	err := runCommand(t, newMIBCommand(env), "list", "--search", "vendor1", "--limit", "5")
	if err != nil {
		t.Fatalf("mib list --json: %v", err)
	}

	page := decodePage[client.MIB](t, out.Bytes())
	if len(page.Items) != 5 {
		t.Fatalf("items = %d, want the 5 --limit allowed", len(page.Items))
	}
	if page.Total != 120 || page.Matched != 60 || page.Shown != 5 {
		t.Errorf("counts = %+v, want 5 shown of 60 matched of 120", page.listCounts)
	}
	if !page.Truncated {
		t.Error("truncated must be true so a consumer knows it has a subset")
	}
	if page.Limit != 5 || page.Search != "vendor1" {
		t.Errorf("the document must echo the flags that produced it: %+v", page.listCounts)
	}
}

// TestDeviceListSearchAndLimit covers the device listing, which is uncapped by
// default: an inventory is read to confirm what is there.
func TestDeviceListSearchAndLimit(t *testing.T) {
	devices := []client.Device{
		{ID: 1, Name: "core-01", Address: "198.51.100.252"},
		{ID: 2, Name: "core-02", Address: "198.51.100.253"},
		{ID: 3, Name: "bastion", Address: "198.51.100.109"},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/devices", deviceListHandler(t, devices...))

	t.Run("no flags prints every device", func(t *testing.T) {
		env, out := newFakeEnv(t, mux)
		if err := runCommand(t, newDeviceCommand(env), "list"); err != nil {
			t.Fatalf("device list: %v", err)
		}

		printed := out.String()
		for _, d := range devices {
			if !strings.Contains(printed, d.Name) {
				t.Errorf("a short inventory must never be capped by default, missing %s:\n%s",
					d.Name, printed)
			}
		}
		if strings.Contains(printed, "showing") {
			t.Errorf("an uncapped listing must print no notice:\n%s", printed)
		}
	})

	t.Run("search by name", func(t *testing.T) {
		env, out := newFakeEnv(t, mux)
		if err := runCommand(t, newDeviceCommand(env), "list", "--search", "CORE"); err != nil {
			t.Fatalf("device list --search: %v", err)
		}

		printed := out.String()
		if !strings.Contains(printed, "core-01") || !strings.Contains(printed, "core-02") {
			t.Errorf("--search must match names case-insensitively:\n%s", printed)
		}
		if strings.Contains(printed, "bastion") {
			t.Errorf("--search kept a non-matching device:\n%s", printed)
		}
	})

	t.Run("search by address", func(t *testing.T) {
		env, out := newFakeEnv(t, mux)
		if err := runCommand(t, newDeviceCommand(env), "list", "--search", "198.51.100.10"); err != nil {
			t.Fatalf("device list --search: %v", err)
		}

		printed := out.String()
		if !strings.Contains(printed, "bastion") {
			t.Errorf("--search must match addresses:\n%s", printed)
		}
		if strings.Contains(printed, "core-01") {
			t.Errorf("--search kept a non-matching device:\n%s", printed)
		}
	})

	t.Run("limit reports what it hid", func(t *testing.T) {
		env, out := newFakeEnv(t, mux)
		if err := runCommand(t, newDeviceCommand(env), "list", "--limit", "1"); err != nil {
			t.Fatalf("device list --limit: %v", err)
		}
		if !strings.Contains(out.String(), "showing 1 of 3 devices; use --limit 0 for all") {
			t.Errorf("a truncated inventory must say so:\n%s", out.String())
		}
	})

	t.Run("search with no matches explains itself", func(t *testing.T) {
		env, out := newFakeEnv(t, mux)
		if err := runCommand(t, newDeviceCommand(env), "list", "--search", "nope"); err != nil {
			t.Fatalf("device list --search: %v", err)
		}

		printed := out.String()
		if !strings.Contains(printed, "No devices match") || !strings.Contains(printed, "searched 3") {
			t.Errorf("a failed search must explain itself:\n%s", printed)
		}
		if strings.Contains(printed, "No devices configured") {
			t.Errorf("a failed search must not claim the inventory is empty:\n%s", printed)
		}
	})

	t.Run("negative limit is rejected", func(t *testing.T) {
		env, _ := newFakeEnv(t, mux)
		err := runCommand(t, newDeviceCommand(env), "list", "--limit", "-3")

		var limitErr *LimitError
		if !errors.As(err, &limitErr) {
			t.Fatalf("err = %v, want a *LimitError", err)
		}
	})
}

// TestProfileListSearchAndLimit covers the profile listing, including that a
// search also cuts the per-profile MIB-count requests it costs.
func TestProfileListSearchAndLimit(t *testing.T) {
	profiles := []client.Profile{
		{ID: 1, Name: "switch", Description: "Access switches"},
		{ID: 2, Name: "router", Description: "Core routers"},
	}

	var mibRequests int
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/profiles", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"profiles": profiles})
	})
	mux.HandleFunc("GET /api/profiles/{id}/mibs", func(w http.ResponseWriter, _ *http.Request) {
		mibRequests++
		respondJSON(t, w, map[string]any{"mibs": []client.MIB{{ID: 7, Name: "IF-MIB"}}})
	})

	t.Run("search by description", func(t *testing.T) {
		mibRequests = 0
		env, out := newFakeEnv(t, mux)
		if err := runCommand(t, newProfileCommand(env), "list", "--search", "CORE ROUTERS"); err != nil {
			t.Fatalf("profile list --search: %v", err)
		}

		printed := out.String()
		if !strings.Contains(printed, "router") {
			t.Errorf("--search must match descriptions case-insensitively:\n%s", printed)
		}
		if strings.Contains(printed, "switch") {
			t.Errorf("--search kept a non-matching profile:\n%s", printed)
		}
		// Counting only the profiles being printed is what makes a search on a
		// large profile list cheaper, not just shorter.
		if mibRequests != 1 {
			t.Errorf("made %d MIB-count requests, want one per printed profile", mibRequests)
		}
	})

	t.Run("limit reports what it hid", func(t *testing.T) {
		env, out := newFakeEnv(t, mux)
		if err := runCommand(t, newProfileCommand(env), "list", "--limit", "1"); err != nil {
			t.Fatalf("profile list --limit: %v", err)
		}
		if !strings.Contains(out.String(), "showing 1 of 2 profiles; use --limit 0 for all") {
			t.Errorf("a truncated profile list must say so:\n%s", out.String())
		}
	})

	t.Run("negative limit is rejected", func(t *testing.T) {
		env, _ := newFakeEnv(t, mux)
		err := runCommand(t, newProfileCommand(env), "list", "--limit", "-1")

		var limitErr *LimitError
		if !errors.As(err, &limitErr) {
			t.Fatalf("err = %v, want a *LimitError", err)
		}
	})
}

// assignedMIBSet builds a device's or profile's effective MIB list.
func assignedMIBSet() []client.AssignedMIB {
	return []client.AssignedMIB{
		{MIB: client.MIB{ID: 1, Name: "IF-MIB", Description: "Interfaces group"},
			Assignment: client.MIBAssignment{Enabled: true}},
		{MIB: client.MIB{ID: 2, Name: "CISCO-PROCESS-MIB", Description: "CPU load", Tags: []string{"cisco"}},
			Assignment: client.MIBAssignment{Enabled: true}},
		{MIB: client.MIB{ID: 3, Name: "IP-MIB", Description: "IP group"},
			Assignment: client.MIBAssignment{Enabled: false}},
	}
}

// TestDeviceMIBListSearchAndLimit covers a device's MIB listing, including that
// the SOURCE column keeps telling the truth under a search.
func TestDeviceMIBListSearchAndLimit(t *testing.T) {
	all := assignedMIBSet()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/devices", deviceListHandler(t, testDeviceFixture()))
	mux.HandleFunc("GET /api/devices/7/mibs", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, client.DeviceMIBs{
			// Only the Cisco module is attached to the device itself.
			DirectMIBs: []client.AssignedMIB{all[1]},
			AllMIBs:    all,
		})
	})

	t.Run("search by name", func(t *testing.T) {
		env, out := newFakeEnv(t, mux)
		if err := runCommand(t, newDeviceCommand(env), "mib", "list", "core-01", "--search", "cisco"); err != nil {
			t.Fatalf("device mib list --search: %v", err)
		}

		printed := out.String()
		if !strings.Contains(printed, "CISCO-PROCESS-MIB") {
			t.Errorf("--search lost the matching module:\n%s", printed)
		}
		if strings.Contains(printed, "IF-MIB") {
			t.Errorf("--search kept a non-matching module:\n%s", printed)
		}
		// A search must not relabel an assignment: this module is direct, and
		// filtering the list it is shown in cannot change that.
		if !strings.Contains(printed, "direct") {
			t.Errorf("SOURCE must survive a search:\n%s", printed)
		}
	})

	t.Run("search by description", func(t *testing.T) {
		env, out := newFakeEnv(t, mux)
		if err := runCommand(t, newDeviceCommand(env), "mib", "list", "core-01", "--search", "interfaces group"); err != nil {
			t.Fatalf("device mib list --search: %v", err)
		}
		if !strings.Contains(out.String(), "IF-MIB") {
			t.Errorf("--search must match descriptions:\n%s", out.String())
		}
	})

	t.Run("limit reports what it hid", func(t *testing.T) {
		env, out := newFakeEnv(t, mux)
		if err := runCommand(t, newDeviceCommand(env), "mib", "list", "core-01", "--limit", "2"); err != nil {
			t.Fatalf("device mib list --limit: %v", err)
		}
		if !strings.Contains(out.String(), "showing 2 of 3 MIBs; use --limit 0 for all") {
			t.Errorf("a truncated MIB list must say so:\n%s", out.String())
		}
	})

	t.Run("json keeps the direct set whole", func(t *testing.T) {
		env, out := newFakeEnv(t, mux)
		env.Opts.JSON = true
		err := runCommand(t, newDeviceCommand(env), "mib", "list", "core-01", "--search", "ip-mib")
		if err != nil {
			t.Fatalf("device mib list --json: %v", err)
		}

		var report deviceMIBsReport
		if err := json.Unmarshal(out.Bytes(), &report); err != nil {
			t.Fatalf("output was not a device-MIBs document: %v (%q)", err, out.String())
		}
		if len(report.AllMIBs) != 1 || report.AllMIBs[0].Name != "IP-MIB" {
			t.Errorf("all_mibs = %+v, want only the searched module", report.AllMIBs)
		}
		// direct_mibs is what the SOURCE column is decided against, so a search
		// over the printed list must not narrow it.
		if len(report.DirectMIBs) != 1 || report.DirectMIBs[0].Name != "CISCO-PROCESS-MIB" {
			t.Errorf("direct_mibs = %+v, want the device's own assignment", report.DirectMIBs)
		}
		if report.Listing.Total != 3 || report.Listing.Matched != 1 {
			t.Errorf("listing = %+v, want 1 matched of 3", report.Listing)
		}
	})

	t.Run("negative limit is rejected", func(t *testing.T) {
		env, _ := newFakeEnv(t, mux)
		err := runCommand(t, newDeviceCommand(env), "mib", "list", "core-01", "--limit", "-1")

		var limitErr *LimitError
		if !errors.As(err, &limitErr) {
			t.Fatalf("err = %v, want a *LimitError", err)
		}
	})
}

// TestProfileMIBListSearchAndLimit covers the profile side of the same listing.
func TestProfileMIBListSearchAndLimit(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/profiles", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"profiles": []client.Profile{testProfile()}})
	})
	mux.HandleFunc("GET /api/profiles/1/mibs", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"mibs": assignedMIBSet()})
	})

	t.Run("search by tag", func(t *testing.T) {
		env, out := newFakeEnv(t, mux)
		if err := runCommand(t, newProfileCommand(env), "mib", "list", "switch", "--search", "CISCO"); err != nil {
			t.Fatalf("profile mib list --search: %v", err)
		}

		printed := out.String()
		if !strings.Contains(printed, "CISCO-PROCESS-MIB") {
			t.Errorf("--search must match tags case-insensitively:\n%s", printed)
		}
		if strings.Contains(printed, "IF-MIB") {
			t.Errorf("--search kept a non-matching module:\n%s", printed)
		}
	})

	t.Run("search with no matches explains itself", func(t *testing.T) {
		env, out := newFakeEnv(t, mux)
		if err := runCommand(t, newProfileCommand(env), "mib", "list", "switch", "--search", "zzz"); err != nil {
			t.Fatalf("profile mib list --search: %v", err)
		}

		printed := out.String()
		if !strings.Contains(printed, "No MIBs match") || !strings.Contains(printed, "searched 3") {
			t.Errorf("a failed search must explain itself:\n%s", printed)
		}
		if strings.Contains(printed, "FILTERS") {
			t.Errorf("an empty result must not print a bare table header:\n%s", printed)
		}
	})

	t.Run("limit reports what it hid", func(t *testing.T) {
		env, out := newFakeEnv(t, mux)
		if err := runCommand(t, newProfileCommand(env), "mib", "list", "switch", "--limit", "1"); err != nil {
			t.Fatalf("profile mib list --limit: %v", err)
		}
		if !strings.Contains(out.String(), "showing 1 of 3 MIBs; use --limit 0 for all") {
			t.Errorf("a truncated MIB list must say so:\n%s", out.String())
		}
	})

	t.Run("negative limit is rejected", func(t *testing.T) {
		env, _ := newFakeEnv(t, mux)
		err := runCommand(t, newProfileCommand(env), "mib", "list", "switch", "--limit", "-2")

		var limitErr *LimitError
		if !errors.As(err, &limitErr) {
			t.Fatalf("err = %v, want a *LimitError", err)
		}
	})
}

// TestMIBObjectsJSONSignalsTruncation checks the standalone object listing's
// --json document under a cap: still valid JSON, and honest about the subset.
func TestMIBObjectsJSONSignalsTruncation(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/mibs/7", mibDetailHandler(t, testMIBDetail(120), nil))

	env, out := newFakeEnv(t, mux)
	env.Opts.JSON = true
	if err := runCommand(t, newMIBCommand(env), "objects", "7", "--limit", "10"); err != nil {
		t.Fatalf("mib objects --json --limit: %v", err)
	}

	page := decodePage[client.MIBObject](t, out.Bytes())
	if len(page.Items) != 10 {
		t.Fatalf("items = %d, want the 10 --limit allowed", len(page.Items))
	}
	if page.Total != 120 || page.Matched != 120 || page.Shown != 10 || !page.Truncated {
		t.Errorf("counts = %+v, want 10 shown of 120, truncated", page.listCounts)
	}
}

// TestMIBShowJSONCarriesListingCounts checks that the module document keeps its
// own shape and gains the counts describing its objects.
func TestMIBShowJSONCarriesListingCounts(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/mibs/7", mibDetailHandler(t, testMIBDetail(120), nil))

	env, out := newFakeEnv(t, mux)
	env.Opts.JSON = true
	if err := runCommand(t, newMIBCommand(env), "show", "7", "--limit", "5"); err != nil {
		t.Fatalf("mib show --json: %v", err)
	}

	var report mibShowReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("output was not a module document: %v (%q)", err, out.String())
	}
	// The module's own fields are untouched, so an existing consumer still works.
	if report.Name == "" || report.ObjectCount != 120 {
		t.Errorf("module fields lost: %+v", report.MIBDetail)
	}
	if len(report.Objects) != 5 {
		t.Errorf("objects = %d, want the 5 --limit allowed", len(report.Objects))
	}
	if report.Listing.Total != 120 || report.Listing.Shown != 5 || !report.Listing.Truncated {
		t.Errorf("listing = %+v, want 5 shown of 120, truncated", report.Listing)
	}
}

// TestDeviceMIBObjectsJSONSignalsTruncation checks the same contract on the
// per-device plan, which is the command the rest of the CLI follows.
func TestDeviceMIBObjectsJSONSignalsTruncation(t *testing.T) {
	env, out := newFakeEnv(t, objectPlanHandler(t, objectPlanFixture(), 0))
	env.Opts.JSON = true

	err := runCommand(t, newDeviceCommand(env),
		"mib", "objects", "core-01", "IF-MIB", "--search", "sys", "--limit", "1")
	if err != nil {
		t.Fatalf("device mib objects --json: %v", err)
	}

	var report objectPlanReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("output was not a plan document: %v (%q)", err, out.String())
	}
	// The plan's own totals still describe the module.
	if report.Total != 5 || report.MIB != "IF-MIB" {
		t.Errorf("plan fields lost: %+v", report.MIBObjectPlan)
	}
	if len(report.Objects) != 1 {
		t.Fatalf("objects = %d, want the 1 --limit allowed", len(report.Objects))
	}
	// The listing block counts the searched set: two objects matched "sys".
	if report.Listing.Matched != 2 || report.Listing.Shown != 1 || !report.Listing.Truncated {
		t.Errorf("listing = %+v, want 1 shown of 2 matched", report.Listing)
	}
}

// TestDeviceMIBObjectsRejectsNegativeLimit checks the precedent command
// validates its cap before asking the server for a plan.
func TestDeviceMIBObjectsRejectsNegativeLimit(t *testing.T) {
	env, _ := newFakeEnv(t, objectPlanHandler(t, objectPlanFixture(), 0))

	err := runCommand(t, newDeviceCommand(env),
		"mib", "objects", "core-01", "IF-MIB", "--limit", "-1")

	var limitErr *LimitError
	if !errors.As(err, &limitErr) {
		t.Fatalf("err = %v, want a *LimitError", err)
	}
}

// TestMIBTablesSearchAndLimit covers the conceptual-table listing: tables are
// searched and capped, while a table's columns are always printed whole.
func TestMIBTablesSearchAndLimit(t *testing.T) {
	tables := []client.Table{
		{
			TableOID: "1.3.6.1.2.1.2.2", TableName: "ifTable",
			EntryName: "ifEntry", IndexColumn: "ifIndex",
			Columns: []client.MIBObject{
				{OID: "1.3.6.1.2.1.2.2.1.10", Name: "ifInOctets"},
				{OID: "1.3.6.1.2.1.2.2.1.16", Name: "ifOutOctets"},
			},
		},
		{TableOID: "1.3.6.1.2.1.4.20", TableName: "ipAddrTable", EntryName: "ipAddrEntry"},
		{TableOID: "1.3.6.1.2.1.4.22", TableName: "ipNetToMediaTable", EntryName: "ipNetToMediaEntry"},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/mibs/7/tables", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"tables": tables})
	})

	t.Run("search by table name", func(t *testing.T) {
		env, out := newFakeEnv(t, mux)
		if err := runCommand(t, newMIBCommand(env), "tables", "7", "--search", "IFTABLE"); err != nil {
			t.Fatalf("mib tables --search: %v", err)
		}

		printed := out.String()
		if !strings.Contains(printed, "ifTable") {
			t.Errorf("--search must match table names case-insensitively:\n%s", printed)
		}
		if strings.Contains(printed, "ipAddrTable") {
			t.Errorf("--search kept a non-matching table:\n%s", printed)
		}
		// The matching table's columns are the reason it is shown, so they are
		// never capped.
		if !strings.Contains(printed, "ifInOctets") || !strings.Contains(printed, "ifOutOctets") {
			t.Errorf("a table's columns must always be printed whole:\n%s", printed)
		}
	})

	t.Run("search by index column", func(t *testing.T) {
		env, out := newFakeEnv(t, mux)
		if err := runCommand(t, newMIBCommand(env), "tables", "7", "--search", "ifIndex"); err != nil {
			t.Fatalf("mib tables --search: %v", err)
		}
		if !strings.Contains(out.String(), "ifTable") {
			t.Errorf("--search must match the index column:\n%s", out.String())
		}
	})

	t.Run("search by table OID", func(t *testing.T) {
		env, out := newFakeEnv(t, mux)
		if err := runCommand(t, newMIBCommand(env), "tables", "7", "--search", "1.3.6.1.2.1.4.22"); err != nil {
			t.Fatalf("mib tables --search: %v", err)
		}

		printed := out.String()
		if !strings.Contains(printed, "ipNetToMediaTable") {
			t.Errorf("--search must match table OIDs:\n%s", printed)
		}
		if strings.Contains(printed, "ifTable") {
			t.Errorf("--search kept a non-matching table:\n%s", printed)
		}
	})

	t.Run("limit reports what it hid", func(t *testing.T) {
		env, out := newFakeEnv(t, mux)
		if err := runCommand(t, newMIBCommand(env), "tables", "7", "--limit", "1"); err != nil {
			t.Fatalf("mib tables --limit: %v", err)
		}
		if !strings.Contains(out.String(), "showing 1 of 3 tables; use --limit 0 for all") {
			t.Errorf("a truncated table listing must say so:\n%s", out.String())
		}
	})

	t.Run("search with no matches explains itself", func(t *testing.T) {
		env, out := newFakeEnv(t, mux)
		if err := runCommand(t, newMIBCommand(env), "tables", "7", "--search", "zzz"); err != nil {
			t.Fatalf("mib tables --search: %v", err)
		}
		if !strings.Contains(out.String(), "No tables match") {
			t.Errorf("a failed search must explain itself:\n%s", out.String())
		}
	})

	t.Run("negative limit is rejected", func(t *testing.T) {
		env, _ := newFakeEnv(t, mux)
		err := runCommand(t, newMIBCommand(env), "tables", "7", "--limit", "-1")

		var limitErr *LimitError
		if !errors.As(err, &limitErr) {
			t.Fatalf("err = %v, want a *LimitError", err)
		}
	})
}

// statusFleetHandler serves the routes the all-devices status listing walks:
// the device list and one status per device.
func statusFleetHandler(t *testing.T, devices ...client.Device) http.Handler {
	t.Helper()

	byID := make(map[int64]client.Device, len(devices))
	for _, d := range devices {
		byID[d.ID] = d
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/devices", deviceListHandler(t, devices...))
	mux.HandleFunc("GET /api/devices/{id}/status", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		device := byID[id]
		status := client.DeviceStatus{
			Name: device.Name, Connected: true, PollInterval: "60s",
		}
		// One device is failing, so --search can be tested against the error.
		if device.Name == "bastion" {
			status.Connected = false
			status.LastError = "snmp request timeout"
		}
		respondJSON(t, w, status)
	})
	return mux
}

// TestDeviceStatusSearchAndLimit covers the all-devices status listing, whose
// most useful search is over the error text: "which devices fail this way?".
func TestDeviceStatusSearchAndLimit(t *testing.T) {
	devices := []client.Device{
		{ID: 1, Name: "core-01", Address: "198.51.100.252"},
		{ID: 2, Name: "core-02", Address: "198.51.100.253"},
		{ID: 3, Name: "bastion", Address: "198.51.100.109"},
	}
	handler := statusFleetHandler(t, devices...)

	t.Run("search by last error", func(t *testing.T) {
		env, out := newFakeEnv(t, handler)
		if err := runCommand(t, newDeviceCommand(env), "status", "--search", "TIMEOUT"); err != nil {
			t.Fatalf("device status --search: %v", err)
		}

		printed := out.String()
		if !strings.Contains(printed, "bastion") {
			t.Errorf("--search must match the last error case-insensitively:\n%s", printed)
		}
		if strings.Contains(printed, "core-01") {
			t.Errorf("--search kept a healthy device:\n%s", printed)
		}
	})

	t.Run("search by address", func(t *testing.T) {
		env, out := newFakeEnv(t, handler)
		if err := runCommand(t, newDeviceCommand(env), "status", "--search", "198.51.100.252"); err != nil {
			t.Fatalf("device status --search: %v", err)
		}
		if !strings.Contains(out.String(), "core-01") {
			t.Errorf("--search must match addresses:\n%s", out.String())
		}
	})

	t.Run("limit reports what it hid", func(t *testing.T) {
		env, out := newFakeEnv(t, handler)
		if err := runCommand(t, newDeviceCommand(env), "status", "--limit", "2"); err != nil {
			t.Fatalf("device status --limit: %v", err)
		}
		if !strings.Contains(out.String(), "showing 2 of 3 devices; use --limit 0 for all") {
			t.Errorf("a truncated status listing must say so:\n%s", out.String())
		}
	})

	t.Run("json signals the subset", func(t *testing.T) {
		env, out := newFakeEnv(t, handler)
		env.Opts.JSON = true
		if err := runCommand(t, newDeviceCommand(env), "status", "--limit", "1"); err != nil {
			t.Fatalf("device status --json: %v", err)
		}

		page := decodePage[client.DeviceStatus](t, out.Bytes())
		if len(page.Items) != 1 || page.Total != 3 || !page.Truncated {
			t.Errorf("page = %+v, want 1 of 3 and truncated", page.listCounts)
		}
	})

	t.Run("one named device is not a listing", func(t *testing.T) {
		env, out := newFakeEnv(t, handler)
		env.Opts.JSON = true
		if err := runCommand(t, newDeviceCommand(env), "status", "core-01"); err != nil {
			t.Fatalf("device status <device> --json: %v", err)
		}

		// A single device keeps its existing document shape, so the filter
		// cannot change what a script asking for one device receives.
		var status client.DeviceStatus
		if err := json.Unmarshal(out.Bytes(), &status); err != nil {
			t.Fatalf("output was not a status document: %v (%q)", err, out.String())
		}
		if status.Name != "core-01" {
			t.Errorf("status = %+v, want the named device", status)
		}
	})

	t.Run("negative limit is rejected", func(t *testing.T) {
		env, _ := newFakeEnv(t, handler)
		err := runCommand(t, newDeviceCommand(env), "status", "--limit", "-1")

		var limitErr *LimitError
		if !errors.As(err, &limitErr) {
			t.Fatalf("err = %v, want a *LimitError", err)
		}
	})
}

// TestDeviceProfileListSearchAndLimit covers a device's profile listing.
func TestDeviceProfileListSearchAndLimit(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/devices", deviceListHandler(t, testDeviceFixture()))
	mux.HandleFunc("GET /api/devices/7/profiles", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"profiles": []client.Profile{
			{ID: 1, Name: "switch", Description: "Access switches"},
			{ID: 2, Name: "router", Description: "Core routers"},
		}})
	})

	t.Run("search by name", func(t *testing.T) {
		env, out := newFakeEnv(t, mux)
		if err := runCommand(t, newDeviceCommand(env), "profile", "list", "core-01", "--search", "ROUTER"); err != nil {
			t.Fatalf("device profile list --search: %v", err)
		}

		printed := out.String()
		if !strings.Contains(printed, "router") {
			t.Errorf("--search lost the matching profile:\n%s", printed)
		}
		if strings.Contains(printed, "switch") {
			t.Errorf("--search kept a non-matching profile:\n%s", printed)
		}
	})

	t.Run("limit reports what it hid", func(t *testing.T) {
		env, out := newFakeEnv(t, mux)
		if err := runCommand(t, newDeviceCommand(env), "profile", "list", "core-01", "--limit", "1"); err != nil {
			t.Fatalf("device profile list --limit: %v", err)
		}
		if !strings.Contains(out.String(), "showing 1 of 2 profiles; use --limit 0 for all") {
			t.Errorf("a truncated profile listing must say so:\n%s", out.String())
		}
	})

	t.Run("negative limit is rejected", func(t *testing.T) {
		env, _ := newFakeEnv(t, mux)
		err := runCommand(t, newDeviceCommand(env), "profile", "list", "core-01", "--limit", "-1")

		var limitErr *LimitError
		if !errors.As(err, &limitErr) {
			t.Fatalf("err = %v, want a *LimitError", err)
		}
	})
}

// TestProfileDevicesSearchAndLimit covers the devices-using-a-profile listing.
func TestProfileDevicesSearchAndLimit(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/profiles/1/devices", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"devices": []client.Device{
			{ID: 1, Name: "core-01", Address: "198.51.100.252"},
			{ID: 2, Name: "bastion", Address: "198.51.100.109"},
		}})
	})

	t.Run("search by address", func(t *testing.T) {
		env, out := newFakeEnv(t, mux)
		if err := runCommand(t, newProfileCommand(env), "devices", "1", "--search", "198.51.100.109"); err != nil {
			t.Fatalf("profile devices --search: %v", err)
		}

		printed := out.String()
		if !strings.Contains(printed, "bastion") {
			t.Errorf("--search lost the matching device:\n%s", printed)
		}
		if strings.Contains(printed, "core-01") {
			t.Errorf("--search kept a non-matching device:\n%s", printed)
		}
	})

	t.Run("limit reports what it hid", func(t *testing.T) {
		env, out := newFakeEnv(t, mux)
		if err := runCommand(t, newProfileCommand(env), "devices", "1", "--limit", "1"); err != nil {
			t.Fatalf("profile devices --limit: %v", err)
		}
		if !strings.Contains(out.String(), "showing 1 of 2 devices; use --limit 0 for all") {
			t.Errorf("a truncated device listing must say so:\n%s", out.String())
		}
	})

	t.Run("negative limit is rejected", func(t *testing.T) {
		env, _ := newFakeEnv(t, mux)
		err := runCommand(t, newProfileCommand(env), "devices", "1", "--limit", "-1")

		var limitErr *LimitError
		if !errors.As(err, &limitErr) {
			t.Fatalf("err = %v, want a *LimitError", err)
		}
	})
}

// TestMetricsQuerySearchAndLimit covers the series listing, where --search over
// labels is how one interface is picked out of a metric.
func TestMetricsQuerySearchAndLimit(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/metrics/query", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, testSeries())
	})

	t.Run("search by label value", func(t *testing.T) {
		env, out := newFakeEnv(t, mux)
		err := runCommand(t, newMetricsCommand(env), "query", "snmp_ifhcinoctets", "--search", `ifindex="1"`)
		if err != nil {
			t.Fatalf("metrics query --search: %v", err)
		}

		printed := out.String()
		if !strings.Contains(printed, `ifindex="1"`) {
			t.Errorf("--search must match rendered labels:\n%s", printed)
		}
		if strings.Contains(printed, `ifindex="2"`) {
			t.Errorf("--search kept a non-matching series:\n%s", printed)
		}
	})

	t.Run("search by metric name", func(t *testing.T) {
		env, out := newFakeEnv(t, mux)
		err := runCommand(t, newMetricsCommand(env), "query", "snmp_ifhcinoctets", "--search", "SNMP_IFHC")
		if err != nil {
			t.Fatalf("metrics query --search: %v", err)
		}
		if !strings.Contains(out.String(), "snmp_ifhcinoctets") {
			t.Errorf("--search must match the metric name case-insensitively:\n%s", out.String())
		}
	})

	t.Run("limit reports what it hid", func(t *testing.T) {
		env, out := newFakeEnv(t, mux)
		err := runCommand(t, newMetricsCommand(env), "query", "snmp_ifhcinoctets", "--limit", "1")
		if err != nil {
			t.Fatalf("metrics query --limit: %v", err)
		}
		if !strings.Contains(out.String(), "showing 1 of 2 series; use --limit 0 for all") {
			t.Errorf("a truncated series listing must say so:\n%s", out.String())
		}
	})

	t.Run("search with no matches explains itself", func(t *testing.T) {
		env, out := newFakeEnv(t, mux)
		err := runCommand(t, newMetricsCommand(env), "query", "snmp_ifhcinoctets", "--search", "zzz")
		if err != nil {
			t.Fatalf("metrics query --search: %v", err)
		}
		if !strings.Contains(out.String(), "No series match") {
			t.Errorf("a failed search must explain itself:\n%s", out.String())
		}
	})

	t.Run("negative limit is rejected", func(t *testing.T) {
		env, _ := newFakeEnv(t, mux)
		err := runCommand(t, newMetricsCommand(env), "query", "snmp_ifhcinoctets", "--limit", "-1")

		var limitErr *LimitError
		if !errors.As(err, &limitErr) {
			t.Fatalf("err = %v, want a *LimitError", err)
		}
	})
}

// TestMetricsInstantSearchAndLimit covers the instant listing, which narrows
// what came back rather than changing the PromQL that produced it.
func TestMetricsInstantSearchAndLimit(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/metrics/instant", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"metrics": []map[string]any{
			{"metric": map[string]string{"__name__": "snmp_ifInOctets", "ifindex": "1"}, "value": 10.0},
			{"metric": map[string]string{"__name__": "snmp_ifOutOctets", "ifindex": "2"}, "value": 20.0},
		}})
	})

	t.Run("search by rendered series", func(t *testing.T) {
		env, out := newFakeEnv(t, mux)
		err := runCommand(t, newMetricsCommand(env), "instant", `{device="core-01"}`, "--search", "IFINOCTETS")
		if err != nil {
			t.Fatalf("metrics instant --search: %v", err)
		}

		printed := out.String()
		if !strings.Contains(printed, "snmp_ifInOctets") {
			t.Errorf("--search must match the rendered series case-insensitively:\n%s", printed)
		}
		if strings.Contains(printed, "snmp_ifOutOctets") {
			t.Errorf("--search kept a non-matching series:\n%s", printed)
		}
	})

	t.Run("limit reports what it hid", func(t *testing.T) {
		env, out := newFakeEnv(t, mux)
		err := runCommand(t, newMetricsCommand(env), "instant", `{device="core-01"}`, "--limit", "1")
		if err != nil {
			t.Fatalf("metrics instant --limit: %v", err)
		}
		if !strings.Contains(out.String(), "showing 1 of 2 series; use --limit 0 for all") {
			t.Errorf("a truncated series listing must say so:\n%s", out.String())
		}
	})

	t.Run("negative limit is rejected", func(t *testing.T) {
		env, _ := newFakeEnv(t, mux)
		err := runCommand(t, newMetricsCommand(env), "instant", `{device="core-01"}`, "--limit", "-1")

		var limitErr *LimitError
		if !errors.As(err, &limitErr) {
			t.Fatalf("err = %v, want a *LimitError", err)
		}
	})
}

// TestPollerTablesSearchAndLimit covers the learned-table listing, which grows
// without bound as the poller walks devices.
func TestPollerTablesSearchAndLimit(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/learned-tables", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, learnedTablesDocument())
	})

	t.Run("search by table name", func(t *testing.T) {
		env, out := newFakeEnv(t, mux)
		if err := runCommand(t, newPollerCommand(env), "tables", "--search", "IFTABLE"); err != nil {
			t.Fatalf("poller tables --search: %v", err)
		}

		printed := out.String()
		if !strings.Contains(printed, "ifTable") {
			t.Errorf("--search must match table names case-insensitively:\n%s", printed)
		}
		if strings.Contains(printed, "1.3.6.1.4.1.9.9.187") {
			t.Errorf("--search kept a non-matching table:\n%s", printed)
		}
		// Stats describe the learner, not the listing, so a search keeps them.
		if !strings.Contains(printed, "total_tables") {
			t.Errorf("a search must not drop the learner's stats:\n%s", printed)
		}
	})

	t.Run("search by entry OID", func(t *testing.T) {
		env, out := newFakeEnv(t, mux)
		if err := runCommand(t, newPollerCommand(env), "tables", "--search", "1.3.6.1.4.1.9.9.187"); err != nil {
			t.Fatalf("poller tables --search: %v", err)
		}
		if strings.Contains(out.String(), "ifTable") {
			t.Errorf("--search kept a non-matching table:\n%s", out.String())
		}
	})

	t.Run("search by index name", func(t *testing.T) {
		env, out := newFakeEnv(t, mux)
		if err := runCommand(t, newPollerCommand(env), "tables", "--search", "ifIndex"); err != nil {
			t.Fatalf("poller tables --search: %v", err)
		}
		if !strings.Contains(out.String(), "ifTable") {
			t.Errorf("--search must match index names:\n%s", out.String())
		}
	})

	t.Run("limit reports what it hid", func(t *testing.T) {
		env, out := newFakeEnv(t, mux)
		if err := runCommand(t, newPollerCommand(env), "tables", "--limit", "1"); err != nil {
			t.Fatalf("poller tables --limit: %v", err)
		}
		if !strings.Contains(out.String(), "showing 1 of 2 tables; use --limit 0 for all") {
			t.Errorf("a truncated table listing must say so:\n%s", out.String())
		}
	})

	t.Run("search with no matches explains itself", func(t *testing.T) {
		env, out := newFakeEnv(t, mux)
		if err := runCommand(t, newPollerCommand(env), "tables", "--search", "zzz"); err != nil {
			t.Fatalf("poller tables --search: %v", err)
		}
		if !strings.Contains(out.String(), "No tables match") {
			t.Errorf("a failed search must explain itself:\n%s", out.String())
		}
	})

	t.Run("negative limit is rejected", func(t *testing.T) {
		env, _ := newFakeEnv(t, mux)
		err := runCommand(t, newPollerCommand(env), "tables", "--limit", "-1")

		var limitErr *LimitError
		if !errors.As(err, &limitErr) {
			t.Fatalf("err = %v, want a *LimitError", err)
		}
	})
}
