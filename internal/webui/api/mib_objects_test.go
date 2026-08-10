package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"

	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/mib/selector"
	mibstorage "github.com/jeremyhahn/go-netmon/internal/mib/storage"
	webuistorage "github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// The object plan is what a device page draws its checkbox list from, so these
// tests are written the way that page reads the answer: by object name, with
// the reason each one carries.

const ifTableOID = "1.3.6.1.2.1.2.2"

// planObjects is the IF-MIB slice the plan tests run against: a table with its
// row node, a not-accessible index column, two readable columns, a deprecated
// column and two scalars.
func planObjects() []*mib.OIDMetadata {
	return []*mib.OIDMetadata{
		{OID: ifTableOID, Name: "ifTable", Module: "IF-MIB",
			Access: "not-accessible", Status: "current", IsTable: true},
		{OID: ifEntryOID, Name: "ifEntry", Module: "IF-MIB",
			Access: "not-accessible", Status: "current", IsTableEntry: true, TableOID: ifTableOID},
		{OID: "1.3.6.1.2.1.2.2.1.1", Name: "ifIndex", Module: "IF-MIB", Type: "InterfaceIndex",
			Access: "not-accessible", Status: "current",
			IsColumn: true, IsIndex: true, TableOID: ifTableOID, EntryOID: ifEntryOID},
		{OID: ifInOctets, Name: "ifInOctets", Module: "IF-MIB", Type: "Counter32",
			Access: "read-only", Status: "current",
			IsColumn: true, Pollable: true, TableOID: ifTableOID, EntryOID: ifEntryOID},
		{OID: ifOutOctets, Name: "ifOutOctets", Module: "IF-MIB", Type: "Counter32",
			Access: "read-only", Status: "current",
			IsColumn: true, Pollable: true, TableOID: ifTableOID, EntryOID: ifEntryOID},
		{OID: "1.3.6.1.2.1.2.2.1.21", Name: "ifSpecific", Module: "IF-MIB", Type: "OBJECT IDENTIFIER",
			Access: "read-only", Status: "deprecated",
			IsColumn: true, TableOID: ifTableOID, EntryOID: ifEntryOID},
		{OID: sysUpTimeOID, Name: "sysUpTime", Module: "IF-MIB", Type: "TimeTicks",
			Access: "read-only", Status: "current", IsScalar: true, Pollable: true},
		{OID: sysDescrOID, Name: "sysDescr", Module: "IF-MIB", Type: "DisplayString",
			Access: "read-only", Status: "current", IsScalar: true, Pollable: true},
	}
}

// planByName indexes a plan by object name.
func planByName(objects []MIBObjectDecision) map[string]MIBObjectDecision {
	byName := make(map[string]MIBObjectDecision, len(objects))
	for _, object := range objects {
		byName[object.Name] = object
	}
	return byName
}

// assertPlanned checks one object's verdict and reason.
func assertPlanned(t *testing.T, byName map[string]MIBObjectDecision, name string, polled bool, reason string) {
	t.Helper()

	object, ok := byName[name]
	if !ok {
		t.Fatalf("no entry for %s; the plan must account for every object of the module", name)
	}
	if object.Polled != polled {
		t.Errorf("%s: polled = %v, want %v (reason %q)", name, object.Polled, polled, object.Reason)
	}
	if object.Reason != reason {
		t.Errorf("%s: reason = %q, want %q", name, object.Reason, reason)
	}
}

// TestPlanMIBObjectsUnfiltered covers the default assignment: one walk of the
// table covers its columns, and each scalar is fetched on its own.
func TestPlanMIBObjectsUnfiltered(t *testing.T) {
	planned := PlanMIBObjects(planObjects(), "IF-MIB", webuistorage.DefaultMIBAssignment(), 24)
	byName := planByName(planned)

	assertPlanned(t, byName, "ifTable", true, string(selector.ReasonPolled))
	assertPlanned(t, byName, "ifEntry", false, string(selector.ReasonCoveredByTable))
	assertPlanned(t, byName, "ifIndex", false, string(selector.ReasonCoveredByTable))
	assertPlanned(t, byName, "ifInOctets", false, string(selector.ReasonCoveredByTable))
	assertPlanned(t, byName, "ifSpecific", false, string(selector.ReasonCoveredByTable))
	assertPlanned(t, byName, "sysUpTime", true, string(selector.ReasonPolled))
	assertPlanned(t, byName, "sysDescr", true, string(selector.ReasonPolled))

	// The classification a client shows has to be the one the rules used.
	if byName["ifTable"].Kind != string(selector.KindTable) {
		t.Errorf("ifTable kind = %q, want table", byName["ifTable"].Kind)
	}
	if byName["ifEntry"].Kind != string(selector.KindEntry) {
		t.Errorf("ifEntry kind = %q, want entry", byName["ifEntry"].Kind)
	}
	if byName["ifInOctets"].Kind != string(selector.KindColumn) {
		t.Errorf("ifInOctets kind = %q, want column", byName["ifInOctets"].Kind)
	}
	if byName["sysUpTime"].Kind != string(selector.KindScalar) {
		t.Errorf("sysUpTime kind = %q, want scalar", byName["sysUpTime"].Kind)
	}

	// Cost: the walk collects two pollable columns over 24 rows; each scalar
	// costs one. Covered columns cost nothing, so nothing is counted twice.
	if got := byName["ifTable"].Series; got != 48 {
		t.Errorf("ifTable series = %d, want 2 columns x 24 rows", got)
	}
	if got := byName["ifInOctets"].Series; got != 0 {
		t.Errorf("a covered column costs %d series, want 0", got)
	}
	if got := byName["sysUpTime"].Series; got != 1 {
		t.Errorf("scalar series = %d, want 1", got)
	}
	if got := byName["ifEntry"].Series; got != 0 {
		t.Errorf("a row node costs %d series, want 0", got)
	}
}

// TestPlanMIBObjectsIncludeNames covers the "advanced" regex mode narrowing a
// module: the objects an include list leaves out must say so, rather than
// looking unreadable.
func TestPlanMIBObjectsIncludeNames(t *testing.T) {
	assignment := webuistorage.MIBAssignment{
		Enabled:      true,
		IncludeNames: []string{"^sysUpTime$"},
	}

	byName := planByName(PlanMIBObjects(planObjects(), "IF-MIB", assignment, 24))

	assertPlanned(t, byName, "sysUpTime", true, string(selector.ReasonPolled))
	assertPlanned(t, byName, "sysDescr", false, string(selector.ReasonNotIncluded))
	assertPlanned(t, byName, "ifTable", false, string(selector.ReasonNotIncluded))
	// With the table no longer walked, its columns stand on their own access.
	assertPlanned(t, byName, "ifInOctets", false, string(selector.ReasonNotIncluded))
	assertPlanned(t, byName, "ifIndex", false, string(selector.ReasonNotAccessible))
	assertPlanned(t, byName, "ifSpecific", false, string(selector.ReasonNotCurrent))
}

// TestPlanMIBObjectsExcludeNames covers the checkbox case: unticking an object
// writes an exclude, and the object must come back saying the filter removed it.
func TestPlanMIBObjectsExcludeNames(t *testing.T) {
	assignment := webuistorage.MIBAssignment{
		Enabled:      true,
		ExcludeNames: []string{"^sysDescr$", "^ifTable$"},
	}

	byName := planByName(PlanMIBObjects(planObjects(), "IF-MIB", assignment, 24))

	assertPlanned(t, byName, "sysDescr", false, string(selector.ReasonExcluded))
	assertPlanned(t, byName, "sysUpTime", true, string(selector.ReasonPolled))
	assertPlanned(t, byName, "ifTable", false, string(selector.ReasonExcluded))
	// Excluding the table stops the walk, so its readable columns are fetched.
	assertPlanned(t, byName, "ifInOctets", true, string(selector.ReasonPolled))
	if got := byName["ifInOctets"].Series; got != 24 {
		t.Errorf("a column walked on its own costs %d series, want one per row", got)
	}
}

// TestPlanMIBObjectsMaxOIDs covers the cap: objects past it must name the cap,
// not some other reason a reader would act on differently.
func TestPlanMIBObjectsMaxOIDs(t *testing.T) {
	assignment := webuistorage.MIBAssignment{
		Enabled:      true,
		ExcludeNames: []string{"^ifTable$"},
		MaxOIDs:      2,
	}

	byName := planByName(PlanMIBObjects(planObjects(), "IF-MIB", assignment, 24))

	// The cap is spent in store order: ifInOctets then ifOutOctets.
	assertPlanned(t, byName, "ifInOctets", true, string(selector.ReasonPolled))
	assertPlanned(t, byName, "ifOutOctets", true, string(selector.ReasonPolled))
	assertPlanned(t, byName, "sysUpTime", false, string(selector.ReasonMaxOIDs))
	assertPlanned(t, byName, "sysDescr", false, string(selector.ReasonMaxOIDs))
	// A cap does not relabel an object that was never fetchable.
	assertPlanned(t, byName, "ifIndex", false, string(selector.ReasonNotAccessible))
}

// TestPlanMIBObjectsDisabled covers a paused MIB: nothing is polled and every
// object says why, so the page can grey the whole list with one explanation.
func TestPlanMIBObjectsDisabled(t *testing.T) {
	assignment := webuistorage.MIBAssignment{
		Enabled:      false,
		IncludeNames: []string{"^sysUpTime$"},
	}

	planned := PlanMIBObjects(planObjects(), "IF-MIB", assignment, 24)

	if len(planned) != len(planObjects()) {
		t.Fatalf("a disabled MIB still has %d objects; the plan reported %d",
			len(planObjects()), len(planned))
	}
	for _, object := range planned {
		if object.Polled {
			t.Errorf("%s is polled for a disabled assignment", object.Name)
		}
		if object.Reason != string(selector.ReasonModuleDisabled) {
			t.Errorf("%s: reason = %q, want %q",
				object.Name, object.Reason, selector.ReasonModuleDisabled)
		}
		if object.Series != 0 {
			t.Errorf("%s costs %d series while disabled, want 0", object.Name, object.Series)
		}
	}
}

// TestPlanMIBObjectsEmptyModule covers SNMPv2-TC and SNMPv2-CONF, which are
// legitimately empty: textual conventions and conformance statements define
// nothing to poll. The answer is an empty list, not an error.
func TestPlanMIBObjectsEmptyModule(t *testing.T) {
	planned := PlanMIBObjects(nil, "SNMPv2-TC", webuistorage.DefaultMIBAssignment(), 24)

	if planned == nil {
		t.Fatal("an empty module must produce an empty list, not a null one")
	}
	if len(planned) != 0 {
		t.Errorf("got %d objects for a module that defines none", len(planned))
	}
}

// TestPlanMIBObjectsMatchesTheSelector is the anti-divergence test on the API
// side: the objects reported as polled must be exactly selector.Polled's, which
// is what the poller runs.
func TestPlanMIBObjectsMatchesTheSelector(t *testing.T) {
	assignments := []struct {
		name       string
		assignment webuistorage.MIBAssignment
	}{
		{"default", webuistorage.DefaultMIBAssignment()},
		{"include", webuistorage.MIBAssignment{Enabled: true, IncludeNames: []string{"Octets$"}}},
		{"exclude the table", webuistorage.MIBAssignment{Enabled: true, ExcludeNames: []string{"^ifTable$"}}},
		{"exclude by OID", webuistorage.MIBAssignment{Enabled: true, ExcludeOIDs: []string{`^1\.3\.6\.1\.2\.1\.2\.2$`}}},
		{"capped", webuistorage.MIBAssignment{Enabled: true, MaxOIDs: 1}},
		{"disabled", webuistorage.MIBAssignment{Enabled: false}},
	}

	for _, tt := range assignments {
		t.Run(tt.name, func(t *testing.T) {
			objects := planObjects()

			var want []string
			for _, object := range selector.Polled(objects,
				assignedModules("IF-MIB", tt.assignment),
				assignmentFilters("IF-MIB", tt.assignment)) {
				want = append(want, object.Name)
			}

			var got []string
			for _, object := range PlanMIBObjects(objects, "IF-MIB", tt.assignment, 1) {
				if object.Polled {
					got = append(got, object.Name)
				}
			}

			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("the plan reports %v polled; the poller's selector returns %v", got, want)
			}
		})
	}
}

// TestObjectSeriesClampsInstances checks that a polled object never prices at
// zero, which would read as "this costs nothing" for something being fetched.
func TestObjectSeriesClampsInstances(t *testing.T) {
	column := &mib.OIDMetadata{Name: "ifInOctets", IsColumn: true}
	decision := selector.Decision{Object: column, Kind: selector.KindColumn, Polled: true}

	if got := objectSeries(decision, nil, 0); got != 1 {
		t.Errorf("a polled column with no instance evidence costs %d series, want at least 1", got)
	}

	decision.Polled = false
	if got := objectSeries(decision, nil, 24); got != 0 {
		t.Errorf("an unpolled object costs %d series, want 0", got)
	}
}

// TestAssignmentSourceNamesWhereARuleLives covers every answer the source
// field can give, since it is what tells an operator whether editing a filter
// changes one device or every device sharing a profile.
func TestAssignmentSourceNamesWhereARuleLives(t *testing.T) {
	assignments := deviceAssignments{
		direct:   map[int64]webuistorage.MIBAssignment{1: {Enabled: true}},
		profiles: map[int64][]webuistorage.MIBAssignment{2: {{Enabled: true}}},
	}

	tests := []struct {
		name  string
		mibID int64
		want  string
	}{
		{"the device's own assignment", 1, "device"},
		{"inherited from a profile", 2, "profile"},
		{"neither, so the built-in default applies", 3, "default"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := assignments.source(tt.mibID); got != tt.want {
				t.Errorf("source = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestObjectSeriesByKind prices one object of each kind, including the row node
// that is never polled and so must never be charged for.
func TestObjectSeriesByKind(t *testing.T) {
	columnsByTable := map[string]int{ifTableOID: 2}

	tests := []struct {
		name string
		kind selector.Kind
		oid  string
		want int
	}{
		{"a walked table costs one series per column per row", selector.KindTable, ifTableOID, 48},
		{"a column walked alone costs one per row", selector.KindColumn, ifInOctets, 24},
		{"a scalar costs exactly one", selector.KindScalar, sysUpTimeOID, 1},
		{"a row node is never fetched", selector.KindEntry, ifEntryOID, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision := selector.Decision{
				Object: &mib.OIDMetadata{OID: tt.oid},
				Kind:   tt.kind,
				Polled: true,
			}
			if got := objectSeries(decision, columnsByTable, 24); got != tt.want {
				t.Errorf("objectSeries = %d, want %d", got, tt.want)
			}
		})
	}
}

// newObjectPlanServer builds a server on real stores holding one device, one
// MIB assigned to it, and that MIB's objects.
func newObjectPlanServer(t *testing.T) *Server {
	t.Helper()

	webuiStore, err := webuistorage.New(t.TempDir())
	if err != nil {
		t.Fatalf("create webui storage: %v", err)
	}
	t.Cleanup(func() { webuiStore.Close() })

	mibStore, err := mibstorage.New(t.TempDir())
	if err != nil {
		t.Fatalf("create MIB storage: %v", err)
	}
	t.Cleanup(func() { mibStore.Close() })

	ctx := context.Background()
	if err := webuiStore.CreateDevice(ctx, &webuistorage.Device{
		Name: "core-01", Address: "192.0.2.1", SNMPVersion: "2c", Community: "public", Port: 161,
	}); err != nil {
		t.Fatalf("create device: %v", err)
	}
	if err := webuiStore.CreateMIB(ctx, &webuistorage.MIB{
		Name: "IF-MIB", FilePath: "/mibs/IF-MIB.txt", ObjectCount: 8,
	}); err != nil {
		t.Fatalf("create MIB: %v", err)
	}
	// A second MIB that is imported but never attached to the device.
	if err := webuiStore.CreateMIB(ctx, &webuistorage.MIB{
		Name: "SNMPv2-TC", FilePath: "/mibs/SNMPv2-TC.txt", ObjectCount: 0,
	}); err != nil {
		t.Fatalf("create unassigned MIB: %v", err)
	}
	if err := webuiStore.AssignMIBToDevice(ctx, 1, 1); err != nil {
		t.Fatalf("assign MIB to device: %v", err)
	}

	for _, object := range planObjects() {
		if err := mibStore.StoreOID(object); err != nil {
			t.Fatalf("store OID %s: %v", object.Name, err)
		}
	}

	return &Server{
		router:    mux.NewRouter(),
		storage:   webuiStore,
		mibLoader: &mockMIBLoaderForDashboard{storage: mibStore},
	}
}

// getObjectPlan runs the handler and returns the decoded response.
func getObjectPlan(t *testing.T, server *Server, vars map[string]string) (*httptest.ResponseRecorder, *MIBObjectPlan) {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/api/devices/1/mibs/1/objects", nil)
	req = mux.SetURLVars(req, vars)
	rec := httptest.NewRecorder()

	server.handleGetDeviceMIBObjects(rec, req)
	if rec.Code != http.StatusOK {
		return rec, nil
	}

	var plan MIBObjectPlan
	if err := json.NewDecoder(rec.Body).Decode(&plan); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return rec, &plan
}

// TestHandleGetDeviceMIBObjects covers the endpoint end to end for a directly
// assigned, unfiltered MIB.
func TestHandleGetDeviceMIBObjects(t *testing.T) {
	server := newObjectPlanServer(t)

	rec, plan := getObjectPlan(t, server, map[string]string{"id": "1", "mibId": "1"})
	if plan == nil {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	if plan.Device != "core-01" || plan.MIB != "IF-MIB" {
		t.Errorf("got device %q MIB %q, want core-01 and IF-MIB", plan.Device, plan.MIB)
	}
	if plan.DeviceID != 1 || plan.MIBID != 1 {
		t.Errorf("got ids %d/%d, want 1/1", plan.DeviceID, plan.MIBID)
	}
	if plan.Source != "device" {
		t.Errorf("source = %q, want device", plan.Source)
	}
	if !plan.Assignment.Enabled {
		t.Error("a freshly assigned MIB is enabled")
	}
	if plan.Total != len(planObjects()) {
		t.Errorf("total = %d, want %d", plan.Total, len(planObjects()))
	}

	byName := planByName(plan.Objects)
	assertPlanned(t, byName, "ifTable", true, string(selector.ReasonPolled))
	assertPlanned(t, byName, "ifInOctets", false, string(selector.ReasonCoveredByTable))
	assertPlanned(t, byName, "sysUpTime", true, string(selector.ReasonPolled))

	// One table walk plus two scalars.
	if plan.Polled != 3 {
		t.Errorf("polled = %d, want 3", plan.Polled)
	}
	if plan.Estimate == nil {
		t.Fatal("the plan must carry the module's cost estimate")
	}
	if plan.Estimate.MIB != "IF-MIB" || plan.Estimate.Explanation == "" {
		t.Errorf("estimate = %+v, want the module's own explained estimate", plan.Estimate)
	}
}

// TestHandleGetDeviceMIBObjectsFollowsTheStoredAssignment checks the endpoint
// reads the assignment rather than assuming the default.
func TestHandleGetDeviceMIBObjectsFollowsTheStoredAssignment(t *testing.T) {
	server := newObjectPlanServer(t)

	err := server.storage.SetDeviceMIBAssignment(context.Background(), 1, 1, webuistorage.MIBAssignment{
		Enabled:      true,
		ExcludeNames: []string{"^ifTable$"},
	})
	if err != nil {
		t.Fatalf("set assignment: %v", err)
	}

	_, plan := getObjectPlan(t, server, map[string]string{"id": "1", "mibId": "1"})
	if plan == nil {
		t.Fatal("expected a plan")
	}

	byName := planByName(plan.Objects)
	assertPlanned(t, byName, "ifTable", false, string(selector.ReasonExcluded))
	assertPlanned(t, byName, "ifInOctets", true, string(selector.ReasonPolled))
	if len(plan.Assignment.ExcludeNames) != 1 {
		t.Errorf("assignment = %+v, want the stored exclude echoed back", plan.Assignment)
	}
}

// TestHandleGetDeviceMIBObjectsDisabled covers the paused MIB through the
// endpoint, which is what a greyed-out list is drawn from.
func TestHandleGetDeviceMIBObjectsDisabled(t *testing.T) {
	server := newObjectPlanServer(t)

	err := server.storage.SetDeviceMIBAssignment(context.Background(), 1, 1,
		webuistorage.MIBAssignment{Enabled: false})
	if err != nil {
		t.Fatalf("set assignment: %v", err)
	}

	_, plan := getObjectPlan(t, server, map[string]string{"id": "1", "mibId": "1"})
	if plan == nil {
		t.Fatal("expected a plan")
	}

	if plan.Polled != 0 {
		t.Errorf("polled = %d for a disabled MIB, want 0", plan.Polled)
	}
	if plan.Total == 0 {
		t.Error("a disabled MIB still has objects to list")
	}
	for _, object := range plan.Objects {
		if object.Reason != string(selector.ReasonModuleDisabled) {
			t.Fatalf("%s: reason = %q, want %q",
				object.Name, object.Reason, selector.ReasonModuleDisabled)
		}
	}
}

// TestHandleGetDeviceMIBObjectsThroughAProfile checks a MIB the device polls
// only because a profile says so: the plan uses the profile's assignment and
// names the profile as its source.
func TestHandleGetDeviceMIBObjectsThroughAProfile(t *testing.T) {
	server := newObjectPlanServer(t)
	ctx := context.Background()

	// A second device that carries the MIB only through a profile.
	if err := server.storage.CreateDevice(ctx, &webuistorage.Device{
		Name: "core-02", Address: "192.0.2.2", SNMPVersion: "2c", Port: 161,
	}); err != nil {
		t.Fatalf("create device: %v", err)
	}
	if err := server.storage.CreateDeviceProfile(ctx, &webuistorage.DeviceProfile{
		Name: "Core Routers",
	}); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	if err := server.storage.AssignMIBToProfile(ctx, 1, 1); err != nil {
		t.Fatalf("assign MIB to profile: %v", err)
	}
	if err := server.storage.AssignProfileToDevice(ctx, 2, 1); err != nil {
		t.Fatalf("assign profile to device: %v", err)
	}
	err := server.storage.SetProfileMIBAssignment(ctx, 1, 1, webuistorage.MIBAssignment{
		Enabled:      true,
		ExcludeNames: []string{"^sysDescr$"},
	})
	if err != nil {
		t.Fatalf("set profile assignment: %v", err)
	}

	_, plan := getObjectPlan(t, server, map[string]string{"id": "2", "mibId": "1"})
	if plan == nil {
		t.Fatal("expected a plan")
	}

	if plan.Source != "profile" {
		t.Errorf("source = %q, want profile", plan.Source)
	}
	byName := planByName(plan.Objects)
	assertPlanned(t, byName, "sysDescr", false, string(selector.ReasonExcluded))
	assertPlanned(t, byName, "sysUpTime", true, string(selector.ReasonPolled))
}

// TestHandleGetDeviceMIBObjectsErrors covers every request that must not
// produce a plan. None of them is a server fault, so none may answer 500.
func TestHandleGetDeviceMIBObjectsErrors(t *testing.T) {
	tests := []struct {
		name     string
		vars     map[string]string
		wantCode int
		wantBody string
	}{
		{"unknown device", map[string]string{"id": "404", "mibId": "1"}, http.StatusNotFound, "device 404 not found"},
		{"unknown MIB", map[string]string{"id": "1", "mibId": "404"}, http.StatusNotFound, "MIB 404 not found"},
		{
			"MIB not assigned to the device",
			map[string]string{"id": "1", "mibId": "2"},
			http.StatusNotFound,
			"not assigned to device core-01",
		},
		{"non-numeric device ID", map[string]string{"id": "abc", "mibId": "1"}, http.StatusBadRequest, "invalid device ID"},
		{"non-numeric MIB ID", map[string]string{"id": "1", "mibId": "abc"}, http.StatusBadRequest, "invalid MIB ID"},
		{"missing IDs", map[string]string{}, http.StatusBadRequest, "invalid device ID"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newObjectPlanServer(t)

			rec, _ := getObjectPlan(t, server, tt.vars)

			if rec.Code != tt.wantCode {
				t.Errorf("status = %d, want %d: %s", rec.Code, tt.wantCode, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("body = %q, want it to mention %q", rec.Body.String(), tt.wantBody)
			}
		})
	}
}

// TestHandleGetDeviceMIBObjectsWithoutMIBStorage verifies that a server with no
// MIB metadata reports a server error rather than an empty, confident list that
// would read as "this device polls nothing".
func TestHandleGetDeviceMIBObjectsWithoutMIBStorage(t *testing.T) {
	server := newObjectPlanServer(t)
	server.mibLoader = nil

	rec, _ := getObjectPlan(t, server, map[string]string{"id": "1", "mibId": "1"})

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "mib storage") {
		t.Errorf("body = %q, want it to name the missing MIB storage", rec.Body.String())
	}
}

// TestMIBNotAssignedErrorNamesBothSides checks the error a client shows names
// the pairing that does not exist, since either half may be the mistake.
func TestMIBNotAssignedErrorNamesBothSides(t *testing.T) {
	err := &MIBNotAssignedError{Device: "core-01", MIB: "IF-MIB"}

	if got := err.Error(); !strings.Contains(got, "IF-MIB") || !strings.Contains(got, "core-01") {
		t.Errorf("Error() = %q, want it to name both the MIB and the device", got)
	}
}

// TestHoldsMIB covers the membership test the endpoint gates on.
func TestHoldsMIB(t *testing.T) {
	mibs := []webuistorage.MIB{{ID: 1, Name: "IF-MIB"}, {ID: 7, Name: "IP-MIB"}}

	if !holdsMIB(mibs, 7) {
		t.Error("a MIB the device carries must be found")
	}
	if holdsMIB(mibs, 2) {
		t.Error("a MIB the device does not carry must not be found")
	}
	if holdsMIB(nil, 1) {
		t.Error("a device carrying no MIBs holds nothing")
	}
}
