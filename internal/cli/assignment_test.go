package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// assignedMIB builds a listing entry: a module together with how it is polled.
func assignedMIB(id int64, name string, objectCount int, a client.MIBAssignment) client.AssignedMIB {
	return client.AssignedMIB{
		MIB:        client.MIB{ID: id, Name: name, ObjectCount: objectCount},
		Assignment: a,
	}
}

// enabledMIB is the ordinary listing entry: polled, unfiltered.
func enabledMIB(id int64, name string, objectCount int) client.AssignedMIB {
	return assignedMIB(id, name, objectCount, client.MIBAssignment{Enabled: true})
}

// fakeAssignmentAPI serves the routes the assignment commands use, keeping the
// stored assignments in memory so a read-modify-write round trip is exercised
// end to end rather than asserted one request at a time.
//
// It answers for both devices and profiles, since the two subtrees share an
// implementation and should be provably interchangeable.
type fakeAssignmentAPI struct {
	t *testing.T

	// assignments and estimates are keyed by MIB ID.
	assignments map[int64]client.MIBAssignment
	estimates   map[int64]client.MIBSeriesEstimate

	// estimateStatus, when non-zero, makes the estimate route fail with it.
	estimateStatus int

	// requests records every call, so a test can prove one was never made.
	requests []string
}

func newFakeAssignmentAPI(t *testing.T) *fakeAssignmentAPI {
	return &fakeAssignmentAPI{
		t: t,
		assignments: map[int64]client.MIBAssignment{
			1: {Enabled: true, IncludeNames: []string{"^ifHC"}},
			2: {Enabled: false},
		},
		estimates: map[int64]client.MIBSeriesEstimate{
			1: {
				MIB: "IF-MIB", Device: "core-01", Series: 960,
				Scalars: 0, Columns: 40, Instances: 24,
				Explanation: "estimated from 24 interface instances observed on this device",
			},
			2: {
				MIB: "CISCO-PROCESS-MIB", Device: "core-01", Series: 120,
				Scalars: 4, Columns: 8, Instances: 14, Exact: true,
				Explanation: "exact count from Prometheus",
			},
		},
	}
}

// handler builds the routes and records what is called.
func (f *fakeAssignmentAPI) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/devices", deviceListHandler(f.t, testDeviceFixture()))
	mux.HandleFunc("GET /api/profiles", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(f.t, w, map[string]any{"profiles": []client.Profile{testProfile()}})
	})
	mux.HandleFunc("GET /api/mibs", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(f.t, w, map[string]any{"mibs": []client.MIB{
			{ID: 1, Name: "IF-MIB", ObjectCount: 91},
			{ID: 2, Name: "CISCO-PROCESS-MIB", ObjectCount: 190},
		}})
	})

	for _, owner := range []string{"/api/devices/7", "/api/profiles/1"} {
		mux.HandleFunc("GET "+owner+"/mibs/{mibID}/assignment", f.getAssignment)
		mux.HandleFunc("PUT "+owner+"/mibs/{mibID}/assignment", f.setAssignment)
	}
	mux.HandleFunc("GET /api/devices/7/mibs/{mibID}/estimate", f.getEstimate)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		mux.ServeHTTP(w, r)
	})
}

// mibID reads the MIB the request addresses, answering 404 for one the fake
// does not hold — the same shape the server uses for an unassigned MIB.
func (f *fakeAssignmentAPI) mibID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("mibID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid MIB ID", http.StatusBadRequest)
		return 0, false
	}
	if _, ok := f.assignments[id]; !ok {
		http.Error(w, "MIB is not assigned", http.StatusNotFound)
		return 0, false
	}
	return id, true
}

func (f *fakeAssignmentAPI) getAssignment(w http.ResponseWriter, r *http.Request) {
	id, ok := f.mibID(w, r)
	if !ok {
		return
	}
	respondJSON(f.t, w, f.assignments[id])
}

func (f *fakeAssignmentAPI) setAssignment(w http.ResponseWriter, r *http.Request) {
	id, ok := f.mibID(w, r)
	if !ok {
		return
	}

	var a client.MIBAssignment
	if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
		f.t.Fatalf("decode assignment: %v", err)
	}
	f.assignments[id] = a
	respondJSON(f.t, w, a)
}

func (f *fakeAssignmentAPI) getEstimate(w http.ResponseWriter, r *http.Request) {
	if f.estimateStatus != 0 {
		http.Error(w, "estimate unavailable", f.estimateStatus)
		return
	}

	id, err := strconv.ParseInt(r.PathValue("mibID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid MIB ID", http.StatusBadRequest)
		return
	}
	estimate, ok := f.estimates[id]
	if !ok {
		http.Error(w, "MIB not found", http.StatusNotFound)
		return
	}
	respondJSON(f.t, w, estimate)
}

// wrote reports whether the fake was asked to change anything.
func (f *fakeAssignmentAPI) wrote() bool {
	for _, request := range f.requests {
		if strings.HasPrefix(request, http.MethodPut) {
			return true
		}
	}
	return false
}

func TestDeviceMIBEnableAndDisableRoundTrip(t *testing.T) {
	api := newFakeAssignmentAPI(t)
	env, out := newFakeEnv(t, api.handler())

	// CISCO-PROCESS-MIB starts disabled; enabling it must not disturb IF-MIB.
	if err := runCommand(t, newDeviceCommand(env), "mib", "enable", "core-01", "CISCO-PROCESS-MIB"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if !api.assignments[2].Enabled {
		t.Error("CISCO-PROCESS-MIB was not enabled")
	}
	if !strings.Contains(out.String(), "Enabled CISCO-PROCESS-MIB") {
		t.Errorf("unexpected output: %q", out.String())
	}

	out.Reset()
	if err := runCommand(t, newDeviceCommand(env), "mib", "disable", "core-01", "IF-MIB"); err != nil {
		t.Fatalf("disable: %v", err)
	}

	stored := api.assignments[1]
	if stored.Enabled {
		t.Error("IF-MIB was not disabled")
	}
	// Pausing a MIB must not forget how it was narrowed.
	if len(stored.IncludeNames) != 1 || stored.IncludeNames[0] != "^ifHC" {
		t.Errorf("disable lost the filters: %+v", stored)
	}
	if !strings.Contains(out.String(), "Disabled IF-MIB") {
		t.Errorf("unexpected output: %q", out.String())
	}
}

func TestProfileMIBEnableAndDisableRoundTrip(t *testing.T) {
	api := newFakeAssignmentAPI(t)
	env, out := newFakeEnv(t, api.handler())

	if err := runCommand(t, newProfileCommand(env), "mib", "disable", "switch", "IF-MIB"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if api.assignments[1].Enabled {
		t.Error("IF-MIB was not disabled in the profile")
	}

	out.Reset()
	if err := runCommand(t, newProfileCommand(env), "mib", "enable", "switch", "IF-MIB"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if !api.assignments[1].Enabled {
		t.Error("IF-MIB was not re-enabled in the profile")
	}
	if !strings.Contains(out.String(), "Enabled IF-MIB") {
		t.Errorf("unexpected output: %q", out.String())
	}
	// A profile has no device to size tables against, so no cost is quoted.
	if strings.Contains(out.String(), "series") {
		t.Errorf("profile enable must not claim a series cost: %q", out.String())
	}
}

func TestDeviceMIBEnableRejectsUnknownReferences(t *testing.T) {
	tests := []struct {
		name string
		args []string
		kind string
	}{
		{name: "unknown device", args: []string{"mib", "enable", "ghost", "IF-MIB"}, kind: "device"},
		{name: "unknown mib", args: []string{"mib", "enable", "core-01", "NO-SUCH-MIB"}, kind: "MIB"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeAssignmentAPI(t)
			env, _ := newFakeEnv(t, api.handler())

			err := runCommand(t, newDeviceCommand(env), tt.args...)

			var notFound *client.NotFoundError
			if !errors.As(err, &notFound) {
				t.Fatalf("expected *client.NotFoundError, got %v", err)
			}
			if notFound.Kind != tt.kind {
				t.Errorf("kind = %q, want %q", notFound.Kind, tt.kind)
			}
			if api.wrote() {
				t.Error("a reference that does not resolve must change nothing")
			}
		})
	}
}

// TestDeviceMIBEnableUnassignedMIB covers a MIB the server does not hold an
// assignment for: enabling it is a missing resource, not a server fault.
func TestDeviceMIBEnableUnassignedMIB(t *testing.T) {
	api := newFakeAssignmentAPI(t)
	env, _ := newFakeEnv(t, api.handler())

	err := runCommand(t, newDeviceCommand(env), "mib", "enable", "core-01", "9")

	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *client.APIError, got %v", err)
	}
	if !apiErr.NotFound() {
		t.Errorf("status = %d, want 404", apiErr.StatusCode)
	}
	if !strings.Contains(err.Error(), "9:") {
		t.Errorf("the error must name the failing MIB: %v", err)
	}
}

func TestDeviceMIBDisableSurfacesServerFailure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/devices/7/mibs/1/assignment", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, client.MIBAssignment{Enabled: true})
	})
	mux.HandleFunc("PUT /api/devices/7/mibs/1/assignment", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "storage unavailable", http.StatusInternalServerError)
	})

	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newDeviceCommand(env), "mib", "disable", "7", "1")

	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected a 500 APIError, got %v", err)
	}
}

func TestDeviceMIBFilterReplacesNamedFieldsOnly(t *testing.T) {
	api := newFakeAssignmentAPI(t)
	api.assignments[1] = client.MIBAssignment{
		Enabled:      true,
		IncludeNames: []string{"^ifHC"},
		ExcludeOIDs:  []string{`\.16$`},
		MaxOIDs:      50,
	}

	env, out := newFakeEnv(t, api.handler())
	err := runCommand(t, newDeviceCommand(env), "mib", "filter", "core-01", "IF-MIB",
		"--include-names", "^ifIn.*", "--include-names", "^ifOut.*", "--max-oids", "200")
	if err != nil {
		t.Fatalf("filter: %v", err)
	}

	stored := api.assignments[1]
	// A list flag replaces the stored list outright.
	if len(stored.IncludeNames) != 2 || stored.IncludeNames[0] != "^ifIn.*" {
		t.Errorf("include names = %v, want the flag value to replace the stored list", stored.IncludeNames)
	}
	if stored.MaxOIDs != 200 {
		t.Errorf("max oids = %d, want 200", stored.MaxOIDs)
	}
	// Fields the command did not name keep their stored value.
	if len(stored.ExcludeOIDs) != 1 || stored.ExcludeOIDs[0] != `\.16$` {
		t.Errorf("exclude oids = %v, want the stored value to survive", stored.ExcludeOIDs)
	}
	if !stored.Enabled {
		t.Error("filtering a MIB must not change whether it is polled")
	}
	if !strings.Contains(out.String(), "^ifIn.*") {
		t.Errorf("the command should report the new filters: %q", out.String())
	}
}

func TestDeviceMIBFilterClear(t *testing.T) {
	api := newFakeAssignmentAPI(t)
	api.assignments[1] = client.MIBAssignment{
		Enabled:      false,
		IncludeNames: []string{"^ifHC"},
		ExcludeNames: []string{"Errors$"},
		IncludeOIDs:  []string{"^1.3.6"},
		ExcludeOIDs:  []string{`\.16$`},
		MaxOIDs:      50,
	}

	env, _ := newFakeEnv(t, api.handler())
	if err := runCommand(t, newDeviceCommand(env), "mib", "filter", "core-01", "IF-MIB", "--clear"); err != nil {
		t.Fatalf("filter --clear: %v", err)
	}

	stored := api.assignments[1]
	if len(stored.IncludeNames)+len(stored.ExcludeNames)+len(stored.IncludeOIDs)+len(stored.ExcludeOIDs) != 0 {
		t.Errorf("--clear left filters behind: %+v", stored)
	}
	if stored.MaxOIDs != 0 {
		t.Errorf("max oids = %d, want the cap removed", stored.MaxOIDs)
	}
	// --clear is about filters, not about whether the module is polled.
	if stored.Enabled {
		t.Error("--clear must leave the enabled state alone")
	}
}

func TestProfileMIBFilterClearThenSet(t *testing.T) {
	api := newFakeAssignmentAPI(t)
	api.assignments[1] = client.MIBAssignment{
		Enabled:      true,
		IncludeNames: []string{"^ifHC"},
		MaxOIDs:      50,
	}

	env, _ := newFakeEnv(t, api.handler())
	err := runCommand(t, newProfileCommand(env), "mib", "filter", "switch", "IF-MIB",
		"--clear", "--exclude-names", "Errors$")
	if err != nil {
		t.Fatalf("filter: %v", err)
	}

	stored := api.assignments[1]
	if len(stored.IncludeNames) != 0 || stored.MaxOIDs != 0 {
		t.Errorf("--clear should have wiped the stored filters first: %+v", stored)
	}
	if len(stored.ExcludeNames) != 1 || stored.ExcludeNames[0] != "Errors$" {
		t.Errorf("exclude names = %v, want the flag applied after the clear", stored.ExcludeNames)
	}
}

func TestDeviceMIBFilterSetsEveryField(t *testing.T) {
	api := newFakeAssignmentAPI(t)
	env, out := newFakeEnv(t, api.handler())
	env.Opts.JSON = true

	err := runCommand(t, newDeviceCommand(env), "mib", "filter", "core-01", "IF-MIB",
		"--include-names", "^ifHCIn", "--include-names", "^ifHCOut",
		"--exclude-names", "Errors$",
		"--include-oids", `^1\.3\.6\.1\.2\.1\.2\.2\.1\.`,
		"--exclude-oids", `\.16$`,
		"--max-oids", "200")
	if err != nil {
		t.Fatalf("filter: %v", err)
	}

	stored := api.assignments[1]
	if len(stored.IncludeNames) != 2 || len(stored.ExcludeNames) != 1 {
		t.Errorf("name filters = %+v", stored)
	}
	if len(stored.IncludeOIDs) != 1 || len(stored.ExcludeOIDs) != 1 {
		t.Errorf("OID filters = %+v", stored)
	}
	if stored.MaxOIDs != 200 {
		t.Errorf("max oids = %d, want 200", stored.MaxOIDs)
	}

	var reported client.MIBAssignment
	if err := json.Unmarshal(out.Bytes(), &reported); err != nil {
		t.Fatalf("--json output was not JSON: %v (%q)", err, out.String())
	}
	if len(reported.IncludeNames) != 2 || reported.MaxOIDs != 200 {
		t.Errorf("unexpected JSON payload: %+v", reported)
	}
}

// TestMIBAssignmentCommandsRejectBadReferences walks the references each
// command resolves, so no command can act on a name that does not exist.
func TestMIBAssignmentCommandsRejectBadReferences(t *testing.T) {
	tests := []struct {
		name string
		args []string
		kind string
	}{
		{name: "filter unknown device", args: []string{"mib", "filter", "ghost", "IF-MIB"}, kind: "device"},
		{name: "filter unknown mib", args: []string{"mib", "filter", "core-01", "NO-SUCH-MIB"}, kind: "MIB"},
		{name: "cost unknown device", args: []string{"mib", "cost", "ghost", "IF-MIB"}, kind: "device"},
		{name: "cost unknown mib", args: []string{"mib", "cost", "core-01", "NO-SUCH-MIB"}, kind: "MIB"},
		{name: "disable unknown mib", args: []string{"mib", "disable", "core-01", "NO-SUCH-MIB"}, kind: "MIB"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeAssignmentAPI(t)
			env, _ := newFakeEnv(t, api.handler())

			err := runCommand(t, newDeviceCommand(env), tt.args...)

			var notFound *client.NotFoundError
			if !errors.As(err, &notFound) {
				t.Fatalf("expected *client.NotFoundError, got %v", err)
			}
			if notFound.Kind != tt.kind {
				t.Errorf("kind = %q, want %q", notFound.Kind, tt.kind)
			}
			if api.wrote() {
				t.Error("a reference that does not resolve must change nothing")
			}
		})
	}
}

// TestDeviceMIBFilterUnassignedMIB covers filtering a MIB the owner does not
// carry: the read fails and nothing is written.
func TestDeviceMIBFilterUnassignedMIB(t *testing.T) {
	api := newFakeAssignmentAPI(t)
	env, _ := newFakeEnv(t, api.handler())

	err := runCommand(t, newDeviceCommand(env),
		"mib", "filter", "core-01", "9", "--max-oids", "10")

	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || !apiErr.NotFound() {
		t.Fatalf("expected a 404 APIError, got %v", err)
	}
	if api.wrote() {
		t.Error("a filter on an unassigned MIB must change nothing")
	}
}

// TestMIBFilterRejectsBadPatternBeforeAnyRequest is the point of validating in
// the CLI: a pattern that will not compile costs no round trip at all.
func TestMIBFilterRejectsBadPatternBeforeAnyRequest(t *testing.T) {
	tests := []struct {
		name      string
		flag      string
		value     string
		wantField string
	}{
		{name: "include names", flag: "--include-names", value: "if(", wantField: "include_names"},
		{name: "exclude names", flag: "--exclude-names", value: "*bad", wantField: "exclude_names"},
		{name: "include oids", flag: "--include-oids", value: "[", wantField: "include_oids"},
		{name: "exclude oids", flag: "--exclude-oids", value: "(?P<", wantField: "exclude_oids"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeAssignmentAPI(t)
			env, _ := newFakeEnv(t, api.handler())

			err := runCommand(t, newDeviceCommand(env),
				"mib", "filter", "core-01", "IF-MIB", tt.flag, tt.value)

			var patternErr *client.PatternError
			if !errors.As(err, &patternErr) {
				t.Fatalf("expected *client.PatternError, got %v", err)
			}
			if patternErr.Field != tt.wantField {
				t.Errorf("field = %q, want %q", patternErr.Field, tt.wantField)
			}
			if !strings.Contains(err.Error(), tt.value) {
				t.Errorf("the error must name the bad pattern: %v", err)
			}
			if len(api.requests) != 0 {
				t.Errorf("a bad pattern reached the server: %v", api.requests)
			}
		})
	}
}

func TestMIBFilterRejectsNegativeMaxOIDs(t *testing.T) {
	api := newFakeAssignmentAPI(t)
	env, _ := newFakeEnv(t, api.handler())

	err := runCommand(t, newDeviceCommand(env),
		"mib", "filter", "core-01", "IF-MIB", "--max-oids", "-5")

	var configErr *client.ConfigError
	if !errors.As(err, &configErr) {
		t.Fatalf("expected *client.ConfigError, got %v", err)
	}
	if len(api.requests) != 0 {
		t.Errorf("an impossible cap reached the server: %v", api.requests)
	}
}

func TestDeviceMIBEnablePrintsProjectedCost(t *testing.T) {
	api := newFakeAssignmentAPI(t)
	env, out := newFakeEnv(t, api.handler())

	if err := runCommand(t, newDeviceCommand(env), "mib", "enable", "core-01", "IF-MIB"); err != nil {
		t.Fatalf("enable: %v", err)
	}

	text := out.String()
	for _, want := range []string{
		"IF-MIB: ~960 series",
		"estimated from 24 interface instances observed on this device",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("cost preview missing %q:\n%s", want, text)
		}
	}
	// An exact count is not a guess, so it is not marked as one.
	out.Reset()
	if err := runCommand(t, newDeviceCommand(env), "mib", "enable", "core-01", "CISCO-PROCESS-MIB"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if !strings.Contains(out.String(), "CISCO-PROCESS-MIB: 120 series") {
		t.Errorf("an exact count should not carry a tilde:\n%s", out.String())
	}
}

func TestDeviceMIBEnableGuardsAgainstRunawayCost(t *testing.T) {
	// One MIB alone exceeds the guard, which is what makes the refusal about
	// the projected cost rather than about the number of arguments.
	overBudget := client.MIBSeriesEstimate{
		MIB: "IF-MIB", Device: "core-01", Series: seriesConfirmThreshold + 1,
		Columns: 624, Instances: 51,
		Explanation: "estimated from a median of 51 instances across 8 tables observed on this device",
	}

	t.Run("refused without --yes", func(t *testing.T) {
		api := newFakeAssignmentAPI(t)
		api.estimates[1] = overBudget
		env, out := newFakeEnv(t, api.handler())

		err := runCommand(t, newDeviceCommand(env), "mib", "enable", "core-01", "IF-MIB")

		var budgetErr *SeriesBudgetError
		if !errors.As(err, &budgetErr) {
			t.Fatalf("expected *SeriesBudgetError, got %v", err)
		}
		if budgetErr.Threshold != seriesConfirmThreshold {
			t.Errorf("threshold = %d, want %d", budgetErr.Threshold, seriesConfirmThreshold)
		}
		if !strings.Contains(err.Error(), "--yes") {
			t.Errorf("the refusal must say how to proceed: %v", err)
		}
		if api.wrote() {
			t.Error("a refused enable must change nothing")
		}
		// The operator still gets to see what the cost would have been.
		if !strings.Contains(out.String(), "IF-MIB: ~5001 series") {
			t.Errorf("cost preview missing from a refusal:\n%s", out.String())
		}
	})

	t.Run("allowed with --yes", func(t *testing.T) {
		api := newFakeAssignmentAPI(t)
		api.estimates[1] = overBudget
		api.assignments[1] = client.MIBAssignment{Enabled: false}
		env, out := newFakeEnv(t, api.handler())

		err := runCommand(t, newDeviceCommand(env), "mib", "enable", "core-01", "IF-MIB", "--yes")
		if err != nil {
			t.Fatalf("enable --yes: %v", err)
		}
		if !api.assignments[1].Enabled {
			t.Error("--yes did not enable the MIB")
		}
		if !strings.Contains(out.String(), "Enabled IF-MIB") {
			t.Errorf("unexpected output: %q", out.String())
		}
	})

	t.Run("totalled across MIBs", func(t *testing.T) {
		// Neither MIB breaches the guard alone; together they do, because the
		// device pays for the whole command.
		api := newFakeAssignmentAPI(t)
		half := seriesConfirmThreshold/2 + 1
		api.estimates[1] = client.MIBSeriesEstimate{MIB: "IF-MIB", Series: half}
		api.estimates[2] = client.MIBSeriesEstimate{MIB: "CISCO-PROCESS-MIB", Series: half}

		env, _ := newFakeEnv(t, api.handler())
		err := runCommand(t, newDeviceCommand(env),
			"mib", "enable", "core-01", "IF-MIB", "CISCO-PROCESS-MIB")

		var budgetErr *SeriesBudgetError
		if !errors.As(err, &budgetErr) {
			t.Fatalf("expected *SeriesBudgetError, got %v", err)
		}
		if budgetErr.Series != 2*half {
			t.Errorf("series = %d, want the command total %d", budgetErr.Series, 2*half)
		}
		if api.wrote() {
			t.Error("a refused enable must change nothing, including the first MIB")
		}
	})
}

// TestDeviceMIBEnableWithoutAnEstimate covers the case that must not block a
// legitimate change: the server cannot price the MIB.
func TestDeviceMIBEnableWithoutAnEstimate(t *testing.T) {
	api := newFakeAssignmentAPI(t)
	api.estimateStatus = http.StatusInternalServerError
	api.assignments[1] = client.MIBAssignment{Enabled: false}

	env, out := newFakeEnv(t, api.handler())
	if err := runCommand(t, newDeviceCommand(env), "mib", "enable", "core-01", "IF-MIB"); err != nil {
		t.Fatalf("a missing estimate must not block the enable: %v", err)
	}

	if !api.assignments[1].Enabled {
		t.Error("the MIB was not enabled")
	}
	if !strings.Contains(out.String(), "series cost unavailable") {
		t.Errorf("the operator should be told the cost is unknown:\n%s", out.String())
	}
}

func TestDeviceMIBCost(t *testing.T) {
	api := newFakeAssignmentAPI(t)
	env, out := newFakeEnv(t, api.handler())

	if err := runCommand(t, newDeviceCommand(env), "mib", "cost", "core-01", "IF-MIB"); err != nil {
		t.Fatalf("cost: %v", err)
	}

	text := out.String()
	for _, want := range []string{
		"IF-MIB", "core-01", "~960", "24",
		"estimated from 24 interface instances observed on this device",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("estimate missing %q:\n%s", want, text)
		}
	}
}

func TestDeviceMIBCostJSONAndFailure(t *testing.T) {
	t.Run("json", func(t *testing.T) {
		api := newFakeAssignmentAPI(t)
		env, out := newFakeEnv(t, api.handler())
		env.Opts.JSON = true

		if err := runCommand(t, newDeviceCommand(env), "mib", "cost", "core-01", "IF-MIB"); err != nil {
			t.Fatalf("cost --json: %v", err)
		}

		var estimate client.MIBSeriesEstimate
		if err := json.Unmarshal(out.Bytes(), &estimate); err != nil {
			t.Fatalf("output was not JSON: %v (%q)", err, out.String())
		}
		if estimate.Series != 960 || estimate.MIB != "IF-MIB" {
			t.Errorf("unexpected payload: %+v", estimate)
		}
	})

	t.Run("server error", func(t *testing.T) {
		api := newFakeAssignmentAPI(t)
		api.estimateStatus = http.StatusInternalServerError
		env, _ := newFakeEnv(t, api.handler())

		err := runCommand(t, newDeviceCommand(env), "mib", "cost", "core-01", "IF-MIB")

		var apiErr *client.APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusInternalServerError {
			t.Fatalf("expected a 500 APIError, got %v", err)
		}
	})
}

// TestAssignmentCommandShape records the deliberate asymmetry between the two
// subtrees. Pricing a MIB needs a device's tables, which a profile does not
// have, so profiles get neither "cost" nor the --yes it guards; and only a
// device polls anything, so only a device can be asked which objects it is
// polling right now.
func TestAssignmentCommandShape(t *testing.T) {
	env := &Env{Opts: &Options{}}

	tests := []struct {
		name      string
		scope     assignmentScope
		want      []string
		wantGuard bool
	}{
		{
			name:      "device",
			scope:     deviceAssignmentScope(),
			want:      []string{"enable", "disable", "filter", "cost", "objects"},
			wantGuard: true,
		},
		{
			name:  "profile",
			scope: profileAssignmentScope(),
			want:  []string{"enable", "disable", "filter"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmds := newMIBAssignmentCommands(env, tt.scope)

			got := make([]string, 0, len(cmds))
			for _, cmd := range cmds {
				got = append(got, cmd.Name())
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("commands = %v, want %v", got, tt.want)
			}

			hasGuard := cmds[0].Flags().Lookup("yes") != nil
			if hasGuard != tt.wantGuard {
				t.Errorf("enable --yes present = %v, want %v", hasGuard, tt.wantGuard)
			}
		})
	}
}

func TestDeviceMIBListShowsPollingState(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/devices", deviceListHandler(t, testDeviceFixture()))
	mux.HandleFunc("GET /api/devices/7/mibs", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, client.DeviceMIBs{
			DirectMIBs: []client.AssignedMIB{
				assignedMIB(1, "IF-MIB", 91, client.MIBAssignment{
					Enabled:      true,
					IncludeNames: []string{"^ifHC", "^ifIn"},
					ExcludeOIDs:  []string{`\.16$`},
					MaxOIDs:      50,
				}),
			},
			AllMIBs: []client.AssignedMIB{
				assignedMIB(1, "IF-MIB", 91, client.MIBAssignment{
					Enabled:      true,
					IncludeNames: []string{"^ifHC", "^ifIn"},
					ExcludeOIDs:  []string{`\.16$`},
					MaxOIDs:      50,
				}),
				assignedMIB(5, "SNMPv2-MIB", 70, client.MIBAssignment{Enabled: false}),
			},
		})
	})

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newDeviceCommand(env), "mib", "list", "core-01"); err != nil {
		t.Fatalf("mib list: %v", err)
	}

	text := out.String()
	for _, want := range []string{
		"ENABLED", "FILTERS",
		"2 include, 1 exclude, max 50",
		"yes", "no", "-",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("listing missing %q:\n%s", want, text)
		}
	}
}

func TestProfileMIBListShowsPollingState(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/profiles/1/mibs", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"mibs": []client.AssignedMIB{
			assignedMIB(1, "IF-MIB", 91, client.MIBAssignment{Enabled: false}),
			assignedMIB(2, "SNMPv2-MIB", 70, client.MIBAssignment{
				Enabled: true, ExcludeNames: []string{"Errors$"},
			}),
		}})
	})

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newProfileCommand(env), "mib", "list", "1"); err != nil {
		t.Fatalf("mib list: %v", err)
	}

	text := out.String()
	for _, want := range []string{"ENABLED", "FILTERS", "1 exclude", "no", "yes"} {
		if !strings.Contains(text, want) {
			t.Errorf("listing missing %q:\n%s", want, text)
		}
	}
}

func TestFilterSummary(t *testing.T) {
	tests := []struct {
		name       string
		assignment client.MIBAssignment
		want       string
	}{
		{
			name:       "unfiltered",
			assignment: client.MIBAssignment{Enabled: true},
			want:       "-",
		},
		{
			name: "includes counted across names and oids",
			assignment: client.MIBAssignment{
				IncludeNames: []string{"a", "b"},
				IncludeOIDs:  []string{"c"},
			},
			want: "3 include",
		},
		{
			name: "everything",
			assignment: client.MIBAssignment{
				IncludeNames: []string{"a"},
				ExcludeOIDs:  []string{"b", "c"},
				MaxOIDs:      20,
			},
			want: "1 include, 2 exclude, max 20",
		},
		{
			name:       "cap alone",
			assignment: client.MIBAssignment{MaxOIDs: 5},
			want:       "max 5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := filterSummary(tt.assignment); got != tt.want {
				t.Errorf("filterSummary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatSeriesCost(t *testing.T) {
	tests := []struct {
		name     string
		ref      string
		estimate client.MIBSeriesEstimate
		want     string
	}{
		{
			name: "estimated count is marked approximate",
			ref:  "IF-MIB",
			estimate: client.MIBSeriesEstimate{
				MIB: "IF-MIB", Series: 960,
				Explanation: "estimated from 24 interface instances observed on this device",
			},
			want: "IF-MIB: ~960 series (estimated from 24 interface instances observed on this device)",
		},
		{
			name: "exact count is not",
			ref:  "IF-MIB",
			estimate: client.MIBSeriesEstimate{
				MIB: "IF-MIB", Series: 960, Exact: true,
				Explanation: "exact count from Prometheus",
			},
			want: "IF-MIB: 960 series (exact count from Prometheus)",
		},
		{
			name:     "falls back to what the user typed",
			ref:      "7",
			estimate: client.MIBSeriesEstimate{Series: 12, Explanation: "no pollable objects"},
			want:     "7: ~12 series (no pollable objects)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatSeriesCost(tt.ref, &tt.estimate); got != tt.want {
				t.Errorf("formatSeriesCost() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestFilterFlagsPreserveRegexpCommas guards the flag type. A regexp
// repetition like "if{2,4}" contains a comma, and a comma-splitting flag would
// silently turn it into two invalid patterns.
func TestFilterFlagsPreserveRegexpCommas(t *testing.T) {
	var sent client.MIBAssignment

	mux := http.NewServeMux()
	mux.HandleFunc("/api/devices", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"devices": []client.Device{testDeviceFixture()}})
	})
	mux.HandleFunc("/api/mibs", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"mibs": []client.MIB{{ID: 1, Name: "IF-MIB"}}})
	})
	mux.HandleFunc("GET /api/devices/7/mibs/1/assignment", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, client.MIBAssignment{Enabled: true})
	})
	mux.HandleFunc("PUT /api/devices/7/mibs/1/assignment", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Fatalf("decode: %v", err)
		}
		respondJSON(t, w, sent)
	})

	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newDeviceCommand(env),
		"mib", "filter", "core-01", "IF-MIB", "--include-names", "^if.{2,4}Octets$")
	if err != nil {
		t.Fatalf("device mib filter: %v", err)
	}

	if len(sent.IncludeNames) != 1 {
		t.Fatalf("got %d patterns %v, want the comma kept inside one pattern",
			len(sent.IncludeNames), sent.IncludeNames)
	}
	if sent.IncludeNames[0] != "^if.{2,4}Octets$" {
		t.Errorf("pattern = %q, want it unmangled", sent.IncludeNames[0])
	}
}
