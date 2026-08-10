package cli

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/mib/search"
	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// These tests cover the CLI's half of server-side narrowing: it sends the
// flags, and it reports the counts the server sends back rather than counting a
// page that is missing most of the list.

// TestServerPageAdoptsTheServerCounts checks a page block is believed. Counting
// the rows instead would report "50 of 50" for the first page of a 10,539
// object module.
func TestServerPageAdoptsTheServerCounts(t *testing.T) {
	items := []string{"a", "b", "c"}
	page := serverPage(items, &client.Page{
		Total: 10539, Matched: 293, Count: 3, Offset: 50, Limit: 3,
	}, "objects", "cev", 3)

	if page.Total != 10539 {
		t.Errorf("total = %d, want the module's 10539", page.Total)
	}
	if page.Matched != 293 {
		t.Errorf("matched = %d, want the 293 the search found", page.Matched)
	}
	if page.Shown != 3 || len(page.Items) != 3 {
		t.Errorf("shown = %d with %d items, want 3", page.Shown, len(page.Items))
	}
	if page.Offset != 50 {
		t.Errorf("offset = %d, want 50", page.Offset)
	}
	if !page.Truncated {
		t.Error("rows were left beyond this page; the document must say so")
	}
	if page.Search != "cev" || page.Limit != 3 {
		t.Errorf("page must echo the flags that produced it, got %+v", page.listCounts)
	}
}

// TestServerPageWithoutAPageBlock checks the unnarrowed path: no page block
// means the whole list arrived, so counting it is exactly right.
func TestServerPageWithoutAPageBlock(t *testing.T) {
	page := serverPage([]string{"a", "b"}, nil, "objects", "", 0)

	if page.Total != 2 || page.Matched != 2 || page.Shown != 2 {
		t.Errorf("counts = %+v, want 2 everywhere", page.listCounts)
	}
	if page.Truncated || page.Offset != 0 {
		t.Errorf("an unnarrowed page cannot be truncated or offset: %+v", page.listCounts)
	}

	// A nil list must still render, and must encode as [] rather than null.
	empty := serverPage[string](nil, nil, "objects", "", 0)
	if empty.Items == nil {
		t.Error("items = nil; an empty listing must be an empty array")
	}
}

// TestServerPageLastPageIsNotTruncated checks the final page does not claim
// rows were held back.
func TestServerPageLastPageIsNotTruncated(t *testing.T) {
	page := serverPage([]string{"y", "z"}, &client.Page{
		Total: 10, Matched: 10, Count: 2, Offset: 8, Limit: 5,
	}, "objects", "", 5)

	if page.Truncated {
		t.Error("the last page must not report rows beyond it")
	}
}

// TestPageNoteReportsARange checks the notice a paged view prints: where the
// rows sit in the whole, not just how many there are.
func TestPageNoteReportsARange(t *testing.T) {
	var out bytes.Buffer

	page := serverPage(make([]string, 50), &client.Page{
		Total: 293, Matched: 293, Count: 50, Offset: 50, Limit: 50,
	}, "objects", "", 50)
	page.note(&out, "")

	if got := out.String(); !strings.Contains(got, "showing 51-100 of 293 objects") {
		t.Errorf("note = %q, want a 51-100 of 293 range", got)
	}

	// The first page keeps the wording it has always had.
	out.Reset()
	first := serverPage(make([]string, 50), &client.Page{
		Total: 293, Matched: 293, Count: 50, Offset: 0, Limit: 50,
	}, "objects", "", 50)
	first.note(&out, "")
	if got := out.String(); !strings.Contains(got, "showing 50 of 293 objects; use --limit 0 for all") {
		t.Errorf("note = %q, want the unpaged wording", got)
	}
}

// TestOffsetValidation checks a negative --offset is refused before any
// request, with a typed error, the way a negative --limit already was.
func TestOffsetValidation(t *testing.T) {
	filter := newObjectFilter()
	filter.offset = -1

	err := filter.validate()

	var offsetErr *OffsetError
	if !errors.As(err, &offsetErr) {
		t.Fatalf("err = %v, want an *OffsetError", err)
	}
	if offsetErr.Offset != -1 || !strings.Contains(offsetErr.Error(), "--offset") {
		t.Errorf("error = %q, want it to name the flag and the value", offsetErr.Error())
	}

	filter.offset = 0
	if err := filter.validate(); err != nil {
		t.Errorf("a zero offset must be accepted, got %v", err)
	}
}

// TestObjectListingSendsItsFlagsToTheServer is the whole point of the change:
// the flags have to reach the server, or the CLI is still downloading a module
// to print three rows of it.
func TestObjectListingSendsItsFlagsToTheServer(t *testing.T) {
	var query string

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/mibs/7", mibDetailHandler(t, testMIBDetail(120), &query))

	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newMIBCommand(env), "objects", "7",
		"--search", "ifObject1", "--limit", "3", "--offset", "6", "--pollable-only")
	if err != nil {
		t.Fatalf("objects: %v", err)
	}

	for _, want := range []string{"search=ifObject1", "limit=3", "offset=6", "pollable=true"} {
		if !strings.Contains(query, want) {
			t.Errorf("query %q is missing %q", query, want)
		}
	}
}

// TestObjectListingPagesInOrder checks two pages of the same listing are
// disjoint and consecutive, which is what --offset is for.
func TestObjectListingPagesInOrder(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/mibs/7", mibDetailHandler(t, testMIBDetail(120), nil))

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newMIBCommand(env), "objects", "7", "--limit", "5"); err != nil {
		t.Fatalf("page one: %v", err)
	}
	first := out.String()

	out.Reset()
	if err := runCommand(t, newMIBCommand(env), "objects", "7",
		"--limit", "5", "--offset", "5"); err != nil {
		t.Fatalf("page two: %v", err)
	}
	second := out.String()

	if !strings.Contains(first, "ifObject4 ") || strings.Contains(first, "ifObject5 ") {
		t.Errorf("page one is not the first five objects:\n%s", first)
	}
	if !strings.Contains(second, "ifObject5 ") || strings.Contains(second, "ifObject4 ") {
		t.Errorf("page two is not the second five objects:\n%s", second)
	}
	if !strings.Contains(second, "showing 6-10 of 120 objects") {
		t.Errorf("page two must say where it sits:\n%s", second)
	}
}

// TestObjectListingRejectsAPageThatDoesNotExist checks the server's refusal
// reaches the operator as an error rather than as an empty table that reads
// like an empty module.
func TestObjectListingRejectsAPageThatDoesNotExist(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/mibs/7", mibDetailHandler(t, testMIBDetail(10), nil))

	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newMIBCommand(env), "objects", "7", "--limit", "5", "--offset", "500")

	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("err = %v, want a 400 APIError", err)
	}
}

// TestObjectPlanSendsItsFlagsToTheServer checks the per-device plan narrows on
// the server too, including the flag that is specific to it.
func TestObjectPlanSendsItsFlagsToTheServer(t *testing.T) {
	var query string

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/devices", deviceListHandler(t, testDeviceFixture()))
	mux.HandleFunc("GET /api/mibs", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"mibs": []client.MIB{{ID: 1, Name: "IF-MIB"}}})
	})
	mux.HandleFunc("GET /api/devices/7/mibs/1/objects", planHandler(t, objectPlanFixture(), &query))

	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newDeviceCommand(env), "mib", "objects", "core-01", "IF-MIB",
		"--limit", "1", "--offset", "1", "--polled-only")
	if err != nil {
		t.Fatalf("device mib objects: %v", err)
	}

	for _, want := range []string{"limit=1", "offset=1", "polled=true"} {
		if !strings.Contains(query, want) {
			t.Errorf("query %q is missing %q", query, want)
		}
	}

	if err := runCommand(t, newDeviceCommand(env),
		"mib", "objects", "core-01", "IF-MIB", "--search", "if"); err != nil {
		t.Fatalf("device mib objects --search: %v", err)
	}
	if !strings.Contains(query, "search=if") {
		t.Errorf("query %q is missing the search term", query)
	}
}

// TestMIBListSendsItsFlagsToTheServer checks the catalog listing narrows on the
// server, tag included.
func TestMIBListSendsItsFlagsToTheServer(t *testing.T) {
	var query string

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/mibs", catalogHandler(t, testMIBs(), &query))

	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newMIBCommand(env), "list",
		"--search", "cisco", "--tag", "cisco", "--limit", "5", "--offset", "0", "--available")
	if err != nil {
		t.Fatalf("mib list: %v", err)
	}

	for _, want := range []string{"search=cisco", "tag=cisco", "limit=5", "filter=available"} {
		if !strings.Contains(query, want) {
			t.Errorf("query %q is missing %q", query, want)
		}
	}
}

// TestCLISearchMatchesTheDocumentedObjectFields pins the CLI's promise about
// --search to the definition the server applies.
//
// The flag help says "name, OID or description". If that ever means something
// else on one side of the wire, an operator's search finds different objects
// depending on which tool they ran, which is exactly the failure the shared
// definition exists to prevent.
func TestCLISearchMatchesTheDocumentedObjectFields(t *testing.T) {
	filter := newObjectFilter()
	if filter.fields != "name, OID or description" {
		t.Fatalf("flag help promises %q; this test pins the other wording", filter.fields)
	}

	object := client.MIBObject{
		OID:         "1.3.6.1.2.1.2.2.1.10",
		Name:        "ifInOctets",
		Description: "octets received on the interface",
		Type:        "Counter32",
		Unit:        "octets",
		Access:      "read-only",
	}

	promised := map[string]bool{
		"ifinoctets":   true, // name
		"2.2.1.10":     true, // OID
		"received on":  true, // description
		"counter32":    false,
		"read-only":    false,
		"not in there": false,
	}
	for term, want := range promised {
		got := search.Object(search.Fold(term), object.Name, object.OID, object.Description)
		if got != want {
			t.Errorf("search %q matched = %v, want %v", term, got, want)
		}
	}
}

// TestListingNarrowsLocallyWhenTheServerDidNot covers the other half of the
// protocol: a response with no page block is the whole list, so the flags are
// applied here. That is what a server predating these parameters answers with —
// it ignored the query and returned everything — and --limit is a promise to
// the terminal either way.
func TestListingNarrowsLocallyWhenTheServerDidNot(t *testing.T) {
	// A server that ignores the query string entirely.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/mibs/7", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, testMIBDetail(120))
	})

	env, out := newFakeEnv(t, mux)
	err := runCommand(t, newMIBCommand(env), "objects", "7", "--search", "ifObject1", "--limit", "3")
	if err != nil {
		t.Fatalf("objects: %v", err)
	}

	text := out.String()
	if !strings.Contains(text, "ifObject1 ") || !strings.Contains(text, "ifObject10 ") {
		t.Errorf("the search was not applied:\n%s", text)
	}
	if strings.Contains(text, "ifObject2 ") {
		t.Errorf("a non-matching object was printed:\n%s", text)
	}
	// ifObject1, ifObject10..19 and ifObject100..119 is 31 matches; three are shown.
	if !strings.Contains(text, "showing 3 of 31 objects") {
		t.Errorf("counts must describe the searched set:\n%s", text)
	}

	// --offset pages the same way it would have on the server.
	out.Reset()
	if err := runCommand(t, newMIBCommand(env), "objects", "7",
		"--search", "ifObject1", "--limit", "3", "--offset", "3"); err != nil {
		t.Fatalf("objects --offset: %v", err)
	}
	text = out.String()
	if !strings.Contains(text, "showing 4-6 of 31 objects") {
		t.Errorf("offset was not applied locally:\n%s", text)
	}
	if strings.Contains(text, "ifObject1 ") {
		t.Errorf("page two repeated a row from page one:\n%s", text)
	}
}

// TestLocalNarrowingSelectsTheSameRowsAsTheServer checks the two paths agree.
// They share one search, so a row's fate must not depend on which side ran it.
func TestLocalNarrowingSelectsTheSameRowsAsTheServer(t *testing.T) {
	detail := testMIBDetail(120)

	narrowing := http.NewServeMux()
	narrowing.HandleFunc("GET /api/mibs/7", mibDetailHandler(t, detail, nil))

	ignoring := http.NewServeMux()
	ignoring.HandleFunc("GET /api/mibs/7", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, detail)
	})

	args := []string{"objects", "7", "--search", "ifObject3", "--limit", "4", "--pollable-only"}

	serverEnv, serverOut := newFakeEnv(t, narrowing)
	if err := runCommand(t, newMIBCommand(serverEnv), args...); err != nil {
		t.Fatalf("against a narrowing server: %v", err)
	}

	clientEnv, clientOut := newFakeEnv(t, ignoring)
	if err := runCommand(t, newMIBCommand(clientEnv), args...); err != nil {
		t.Fatalf("against a server that ignores the query: %v", err)
	}

	if serverOut.String() != clientOut.String() {
		t.Errorf("the two narrowing paths disagree:\nserver:\n%s\nclient:\n%s",
			serverOut.String(), clientOut.String())
	}
}

// TestMIBListNarrowsLocallyWhenTheServerDidNot covers the catalog's local path,
// tag selection included.
func TestMIBListNarrowsLocallyWhenTheServerDidNot(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/mibs", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"mibs": testMIBs()})
	})

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newMIBCommand(env), "list", "--tag", "CISCO"); err != nil {
		t.Fatalf("list --tag: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "CISCO-ENVMON-MIB") || strings.Contains(text, "IF-MIB") {
		t.Errorf("the tag was not applied locally:\n%s", text)
	}

	out.Reset()
	if err := runCommand(t, newMIBCommand(env), "list", "--search", "interfaces"); err != nil {
		t.Fatalf("list --search: %v", err)
	}
	text = out.String()
	if !strings.Contains(text, "IF-MIB") || strings.Contains(text, "CISCO-ENVMON-MIB") {
		t.Errorf("the search was not applied locally:\n%s", text)
	}
}

// TestObjectPlanNarrowsLocallyWhenTheServerDidNot covers the plan's local path,
// including the flag that is specific to it.
func TestObjectPlanNarrowsLocallyWhenTheServerDidNot(t *testing.T) {
	plan := objectPlanFixture()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/devices", deviceListHandler(t, testDeviceFixture()))
	mux.HandleFunc("GET /api/mibs", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"mibs": []client.MIB{{ID: 1, Name: "IF-MIB"}}})
	})
	mux.HandleFunc("GET /api/devices/7/mibs/1/objects", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, plan)
	})

	env, out := newFakeEnv(t, mux)
	err := runCommand(t, newDeviceCommand(env),
		"mib", "objects", "core-01", "IF-MIB", "--polled-only")
	if err != nil {
		t.Fatalf("device mib objects --polled-only: %v", err)
	}

	text := out.String()
	for _, object := range plan.Objects {
		printed := strings.Contains(text, " "+object.Name+" ")
		if printed != object.Polled {
			t.Errorf("%s printed = %v, want %v under --polled-only", object.Name, printed, object.Polled)
		}
	}

	// The summary still describes the whole module, not the filtered view.
	if !strings.Contains(text, "2 polled of 5") {
		t.Errorf("the module summary must survive a local filter:\n%s", text)
	}

	// The search runs locally too, over the same fields the server searches.
	out.Reset()
	if err := runCommand(t, newDeviceCommand(env),
		"mib", "objects", "core-01", "IF-MIB", "--search", plan.Objects[0].Name); err != nil {
		t.Fatalf("device mib objects --search: %v", err)
	}
	text = out.String()
	if !strings.Contains(text, plan.Objects[0].Name) {
		t.Errorf("the searched object is missing:\n%s", text)
	}
	for _, object := range plan.Objects[1:] {
		if strings.Contains(object.Name, plan.Objects[0].Name) {
			continue // a name the search legitimately also matches
		}
		if strings.Contains(text, " "+object.Name+" ") {
			t.Errorf("%s survived a search that does not match it:\n%s", object.Name, text)
		}
	}
}
