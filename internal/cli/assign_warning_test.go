package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Reporting an assignment that succeeded without achieving anything.
//
// "netmon device mib add" printed "Assigned <MIB>" whatever happened, because the
// operation could only succeed or fail. A module can do neither: ATEN-PRODUCTS-MIB
// attaches cleanly and produces no metric, ever, because it defines no readable
// objects. The server now says so in the assignment response, and the exit code
// stays zero — nothing failed — so the warning is the only signal there is.

// assignmentServer answers the catalog reads a device/MIB reference needs, plus
// the assignment itself with the given response body.
func assignmentServer(t *testing.T, response map[string]any) *http.ServeMux {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/devices", deviceListHandler(t, testDeviceFixture()))
	mux.HandleFunc("GET /api/mibs", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"mibs": testMIBs()})
	})
	mux.HandleFunc("POST /api/devices/7/mibs", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MIBID int64 `json:"mib_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode assignment request: %v", err)
		}
		respondJSON(t, w, response)
	})
	return mux
}

// TestAssignReportsAMIBThatWillProduceNothing is the ATEN-PRODUCTS-MIB case.
func TestAssignReportsAMIBThatWillProduceNothing(t *testing.T) {
	const warning = "ATEN-PRODUCTS-MIB defines 6 objects and none of them are readable values"

	env, out := newFakeEnv(t, assignmentServer(t, map[string]any{
		"mib": "IF-MIB", "device": "core-01",
		"objects": 6, "pollable": 0, "warning": warning,
	}))

	if err := runCommand(t, newDeviceCommand(env), "mib", "add", "core-01", "IF-MIB"); err != nil {
		t.Fatalf("the assignment succeeded, so the command must not fail: %v", err)
	}

	printed := out.String()
	if !strings.Contains(printed, "Assigned IF-MIB") {
		t.Errorf("the assignment was not reported:\n%s", printed)
	}
	if !strings.Contains(printed, warning) {
		t.Errorf("the server's warning never reached the terminal:\n%s", printed)
	}
}

// TestAssignStaysQuietWhenTheMIBWillProduceData guards the warning from
// appearing on every assignment, which would make it worthless.
func TestAssignStaysQuietWhenTheMIBWillProduceData(t *testing.T) {
	env, out := newFakeEnv(t, assignmentServer(t, map[string]any{
		"mib": "IF-MIB", "device": "core-01",
		"objects": 120, "pollable": 96, "series": 4320,
	}))

	if err := runCommand(t, newDeviceCommand(env), "mib", "add", "core-01", "IF-MIB"); err != nil {
		t.Fatalf("assign: %v", err)
	}

	printed := out.String()
	if !strings.Contains(printed, "Assigned IF-MIB") {
		t.Errorf("the assignment was not reported:\n%s", printed)
	}
	if strings.Contains(printed, "warning") {
		t.Errorf("a MIB that will produce data was warned about:\n%s", printed)
	}
}

// TestAssignCarriesTheWarningIntoJSON covers the scripted path. A script cannot
// read the prose, and the exit code is zero either way, so the note has to be in
// the record or the condition is invisible to automation.
func TestAssignCarriesTheWarningIntoJSON(t *testing.T) {
	const warning = "ATEN-PRODUCTS-MIB is a registry of identifiers rather than data"

	env, out := newFakeEnv(t, assignmentServer(t, map[string]any{
		"mib": "IF-MIB", "device": "core-01", "objects": 6, "pollable": 0, "warning": warning,
	}))
	env.Opts.JSON = true

	if err := runCommand(t, newDeviceCommand(env), "mib", "add", "core-01", "IF-MIB"); err != nil {
		t.Fatalf("assign: %v", err)
	}

	var changes []changeReport
	if err := json.Unmarshal(out.Bytes(), &changes); err != nil {
		t.Fatalf("the --json output is not a change list: %v\n%s", err, out.String())
	}
	if len(changes) != 1 {
		t.Fatalf("got %d records, want 1", len(changes))
	}
	if changes[0].Note != warning {
		t.Errorf("Note = %q, want the server's warning", changes[0].Note)
	}
	if changes[0].Action != "assigned" || changes[0].Ref != "IF-MIB" {
		t.Errorf("record = %+v, want the assignment it describes", changes[0])
	}
}

// TestASilentOperationOmitsTheNote checks the field stays absent for the
// operations that have nothing to say, so --json consumers do not have to treat
// an empty note as meaningful.
func TestASilentOperationOmitsTheNote(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/devices", deviceListHandler(t, testDeviceFixture()))
	mux.HandleFunc("GET /api/mibs", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"mibs": testMIBs()})
	})
	mux.HandleFunc("DELETE /api/devices/7/mibs/7", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	env, out := newFakeEnv(t, mux)
	env.Opts.JSON = true

	if err := runCommand(t, newDeviceCommand(env), "mib", "remove", "core-01", "IF-MIB"); err != nil {
		t.Fatalf("remove: %v", err)
	}

	if strings.Contains(out.String(), "note") {
		t.Errorf("a removal carried a note field:\n%s", out.String())
	}
}
