package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// objectPlanFixture is one device's view of IF-MIB: a walked table, the objects
// that walk covers, a not-accessible index column and two scalars, one of which
// an exclude filter removed. It carries one of every reason the command prints.
func objectPlanFixture() client.MIBObjectPlan {
	object := func(oid, name, kind, reason string, polled bool, series int) client.MIBObjectDecision {
		return client.MIBObjectDecision{
			MIBObject: client.MIBObject{
				OID: oid, Name: name, Type: "Counter32", Access: "read-only",
			},
			Kind: kind, Polled: polled, Reason: reason, Series: series,
		}
	}

	return client.MIBObjectPlan{
		DeviceID: 7, Device: "core-01",
		MIBID: 1, MIB: "IF-MIB",
		Assignment: client.MIBAssignment{Enabled: true, ExcludeNames: []string{"^sysDescr$"}},
		Source:     "profile",
		Total:      5, Polled: 2,
		Estimate: &client.MIBSeriesEstimate{
			MIB: "IF-MIB", Device: "core-01", Series: 49, Instances: 24,
			Explanation: "estimated from 24 instances observed on this device",
		},
		Objects: []client.MIBObjectDecision{
			object("1.3.6.1.2.1.2.2", "ifTable", "table", "polled", true, 48),
			object("1.3.6.1.2.1.2.2.1.10", "ifInOctets", "column", "covered-by-table-walk", false, 0),
			object("1.3.6.1.2.1.2.2.1.1", "ifIndex", "column", "not-accessible", false, 0),
			object("1.3.6.1.2.1.1.3", "sysUpTime", "scalar", "polled", true, 1),
			object("1.3.6.1.2.1.1.1", "sysDescr", "scalar", "excluded-by-filter", false, 0),
		},
	}
}

// objectPlanHandler serves the routes "device mib objects" walks: the device
// and MIB lookups it resolves names through, and the plan itself.
func objectPlanHandler(t *testing.T, plan client.MIBObjectPlan, status int) http.Handler {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/devices", deviceListHandler(t, testDeviceFixture()))
	mux.HandleFunc("GET /api/mibs", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"mibs": []client.MIB{{ID: 1, Name: "IF-MIB", ObjectCount: 91}}})
	})
	served := planHandler(t, plan, nil)
	mux.HandleFunc("GET /api/devices/7/mibs/1/objects", func(w http.ResponseWriter, r *http.Request) {
		if status != 0 {
			http.Error(w, "mib IF-MIB is not assigned to device core-01", status)
			return
		}
		served(w, r)
	})
	return mux
}

// TestDeviceMIBObjectsRendersEveryVerdict checks the table an operator reads:
// each object with its kind, whether it is polled, what it costs and why it is
// not polled when it is not.
func TestDeviceMIBObjectsRendersEveryVerdict(t *testing.T) {
	env, out := newFakeEnv(t, objectPlanHandler(t, objectPlanFixture(), 0))

	if err := runCommand(t, newDeviceCommand(env), "mib", "objects", "core-01", "IF-MIB"); err != nil {
		t.Fatalf("objects: %v", err)
	}

	printed := out.String()
	for _, want := range []string{
		"core-01", "IF-MIB",
		// The rule in force and where it comes from.
		"enabled, 1 exclude (from profile)",
		"2 polled of 5",
		"~49",
		// Every reason has to reach the reader; that is the point of the view.
		"ifTable", "polled", "48",
		"ifInOctets", "covered-by-table-walk",
		"ifIndex", "not-accessible",
		"sysDescr", "excluded-by-filter",
	} {
		if !strings.Contains(printed, want) {
			t.Errorf("output missing %q:\n%s", want, printed)
		}
	}
}

// TestDeviceMIBObjectsPolledOnly covers the flag an operator reaches for on a
// module whose objects are mostly covered by a table walk.
func TestDeviceMIBObjectsPolledOnly(t *testing.T) {
	env, out := newFakeEnv(t, objectPlanHandler(t, objectPlanFixture(), 0))

	err := runCommand(t, newDeviceCommand(env),
		"mib", "objects", "core-01", "IF-MIB", "--polled-only")
	if err != nil {
		t.Fatalf("objects: %v", err)
	}

	printed := out.String()
	if !strings.Contains(printed, "ifTable") || !strings.Contains(printed, "sysUpTime") {
		t.Errorf("polled objects missing from output:\n%s", printed)
	}
	if strings.Contains(printed, "ifInOctets") || strings.Contains(printed, "sysDescr") {
		t.Errorf("--polled-only printed objects that are not polled:\n%s", printed)
	}
	// The counts still describe the whole module, so the filter cannot make a
	// module look smaller than it is.
	if !strings.Contains(printed, "2 polled of 5 (2 shown by filter)") {
		t.Errorf("summary must still count the whole module:\n%s", printed)
	}
}

// TestDeviceMIBObjectsSearchAndLimit covers narrowing the list by text and the
// notice that says rows were held back.
func TestDeviceMIBObjectsSearchAndLimit(t *testing.T) {
	env, out := newFakeEnv(t, objectPlanHandler(t, objectPlanFixture(), 0))

	err := runCommand(t, newDeviceCommand(env),
		"mib", "objects", "core-01", "IF-MIB", "--search", "sys")
	if err != nil {
		t.Fatalf("objects: %v", err)
	}
	if printed := out.String(); strings.Contains(printed, "ifTable") {
		t.Errorf("--search kept a non-matching object:\n%s", printed)
	}

	out.Reset()
	err = runCommand(t, newDeviceCommand(env),
		"mib", "objects", "core-01", "IF-MIB", "--limit", "2")
	if err != nil {
		t.Fatalf("objects: %v", err)
	}
	if printed := out.String(); !strings.Contains(printed, "showing 2 of 5 objects; use --limit 0 for all") {
		t.Errorf("a truncated list must say so:\n%s", printed)
	}
}

// TestDeviceMIBObjectsJSON checks --json emits the plan with the filtered
// object list, which is what a script consuming the command expects.
func TestDeviceMIBObjectsJSON(t *testing.T) {
	env, out := newFakeEnv(t, objectPlanHandler(t, objectPlanFixture(), 0))
	env.Opts.JSON = true

	err := runCommand(t, newDeviceCommand(env),
		"mib", "objects", "core-01", "IF-MIB", "--polled-only")
	if err != nil {
		t.Fatalf("objects: %v", err)
	}

	var plan client.MIBObjectPlan
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
		t.Fatalf("decode --json output: %v\n%s", err, out.String())
	}

	if plan.Device != "core-01" || plan.MIB != "IF-MIB" || plan.Source != "profile" {
		t.Errorf("decoded plan = %+v", plan)
	}
	if len(plan.Objects) != 2 {
		t.Fatalf("got %d objects under --polled-only, want 2", len(plan.Objects))
	}
	for _, object := range plan.Objects {
		if !object.Polled || object.Reason != "polled" {
			t.Errorf("object %+v should not have survived --polled-only", object)
		}
	}
	// The whole-module counts must survive filtering, or a script cannot tell
	// how much of the module it is looking at.
	if plan.Total != 5 || plan.Polled != 2 {
		t.Errorf("total/polled = %d/%d, want 5/2", plan.Total, plan.Polled)
	}
}

// TestDeviceMIBObjectsErrors covers the inputs that must fail cleanly: a MIB
// the device does not carry, and references that resolve to nothing.
func TestDeviceMIBObjectsErrors(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		status  int
		wantErr string
	}{
		{
			"MIB not assigned to the device",
			[]string{"core-01", "IF-MIB"},
			http.StatusNotFound,
			"not assigned",
		},
		{
			"unknown device",
			[]string{"nosuchdevice", "IF-MIB"},
			0,
			"nosuchdevice",
		},
		{
			"unknown MIB",
			[]string{"core-01", "NO-SUCH-MIB"},
			0,
			"NO-SUCH-MIB",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, _ := newFakeEnv(t, objectPlanHandler(t, objectPlanFixture(), tt.status))

			err := runCommand(t, newDeviceCommand(env),
				append([]string{"mib", "objects"}, tt.args...)...)

			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

// TestDeviceMIBObjectsRequiresTwoArguments pins the argument count, since a
// single argument would otherwise silently mean something else.
func TestDeviceMIBObjectsRequiresTwoArguments(t *testing.T) {
	env, _ := newFakeEnv(t, objectPlanHandler(t, objectPlanFixture(), 0))

	if err := runCommand(t, newDeviceCommand(env), "mib", "objects", "core-01"); err == nil {
		t.Error("expected an error for a missing MIB argument")
	}
}

// TestAssignmentState covers how the rule in force is described, including the
// unfiltered and disabled cases the fixture does not reach.
func TestAssignmentState(t *testing.T) {
	tests := []struct {
		name string
		plan client.MIBObjectPlan
		want string
	}{
		{
			"enabled and unfiltered",
			client.MIBObjectPlan{
				Assignment: client.MIBAssignment{Enabled: true},
				Source:     "device",
			},
			"enabled (from device)",
		},
		{
			"disabled",
			client.MIBObjectPlan{
				Assignment: client.MIBAssignment{Enabled: false},
				Source:     "profile",
			},
			"disabled (from profile)",
		},
		{
			"filtered and capped",
			client.MIBObjectPlan{
				Assignment: client.MIBAssignment{
					Enabled: true, IncludeNames: []string{"^ifHC"}, MaxOIDs: 50,
				},
				Source: "device",
			},
			"enabled, 1 include, max 50 (from device)",
		},
		{
			"no source reported",
			client.MIBObjectPlan{Assignment: client.MIBAssignment{Enabled: true}},
			"enabled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := assignmentState(&tt.plan); got != tt.want {
				t.Errorf("assignmentState = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSeriesCount checks a measured count is not dressed up as an estimate.
func TestSeriesCount(t *testing.T) {
	if got := seriesCount(&client.MIBSeriesEstimate{Series: 960}); got != "~960" {
		t.Errorf("seriesCount = %q, want an estimate marked with a tilde", got)
	}
	if got := seriesCount(&client.MIBSeriesEstimate{Series: 960, Exact: true}); got != "960" {
		t.Errorf("seriesCount = %q, want a measured count printed plainly", got)
	}
}

// TestRenderObjectPlanTableEmpty covers a filter that matched nothing, which
// must say so rather than printing a bare header.
func TestRenderObjectPlanTableEmpty(t *testing.T) {
	out := &strings.Builder{}

	if err := renderObjectPlanTable(out, wholeList[client.MIBObjectDecision](nil, "objects")); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(out.String(), "no matching objects") {
		t.Errorf("output = %q, want it to say nothing matched", out.String())
	}
}
