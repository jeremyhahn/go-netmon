package api

import (
	"strings"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/mib"
)

// Warning an operator that a MIB will produce nothing.
//
// ATEN-PRODUCTS-MIB was assigned to a device and the dashboard stayed empty,
// with nothing anywhere saying why. The module is a registry: six OBJECT
// IDENTIFIER assignments naming products, no OBJECT-TYPE, and — because the
// ATEN-SMI module it IMPORTS is not in the store — no resolved OIDs either. Both
// facts are visible at assignment time.

// readable is an object the poller would ask a device for.
func readable(name, oid string) *mib.OIDMetadata {
	return &mib.OIDMetadata{Name: name, OID: oid, Access: "read-only", Status: "current"}
}

// TestARegistryModuleIsReportedAsProducingNothing is the ATEN-PRODUCTS-MIB case
// with its imports resolved: names in a tree, nothing measurable.
func TestARegistryModuleIsReportedAsProducingNothing(t *testing.T) {
	objects := []*mib.OIDMetadata{
		{Name: "kvmProducts", OID: "1.3.6.1.4.1.21317.1", Access: "not-accessible", Status: "current"},
		{Name: "kn4132", OID: "1.3.6.1.4.1.21317.1.1", Access: "not-accessible", Status: "current"},
	}

	result := assessPollability("ATEN-PRODUCTS-MIB", objects)

	if result.UsableForPolling() {
		t.Fatal("a module of OBJECT IDENTIFIER registrations was reported as pollable")
	}
	if result.Objects != 2 {
		t.Errorf("Objects = %d, want 2", result.Objects)
	}
	if result.Pollable != 0 {
		t.Errorf("Pollable = %d, want 0", result.Pollable)
	}
	if result.Unresolved != 0 {
		t.Errorf("Unresolved = %d, want 0: these objects have OIDs", result.Unresolved)
	}

	// The advice matters as much as the warning: there is nothing to fix here,
	// and telling someone to import a missing module would send them hunting for
	// a file that does not exist.
	for _, want := range []string{"ATEN-PRODUCTS-MIB", "registry", "no metrics"} {
		if !strings.Contains(result.Warning, want) {
			t.Errorf("warning does not mention %q: %s", want, result.Warning)
		}
	}
	if strings.Contains(result.Warning, "IMPORTS") {
		t.Errorf("a registry module was blamed on a missing import: %s", result.Warning)
	}
}

// TestAnUnresolvedImportIsNamedAsTheCause is the fixable case, and the one worth
// separating: every object lost its OID because a module it IMPORTS is absent.
func TestAnUnresolvedImportIsNamedAsTheCause(t *testing.T) {
	objects := []*mib.OIDMetadata{
		{Name: "kvmPortCount", Access: "read-only", Status: "current"},
		{Name: "kvmPortName", Access: "read-only", Status: "current"},
	}

	result := assessPollability("ATEN-PRODUCTS-MIB", objects)

	if result.UsableForPolling() {
		t.Fatal("objects with no OID were reported as pollable; the poller has nothing to ask for")
	}
	if result.Unresolved != 2 {
		t.Errorf("Unresolved = %d, want 2", result.Unresolved)
	}
	if !strings.Contains(result.Warning, "IMPORTS") {
		t.Errorf("the warning does not name the missing import as the cause: %s", result.Warning)
	}
	if !strings.Contains(result.Warning, "re-import") {
		t.Errorf("the warning does not say to re-import after fixing it: %s", result.Warning)
	}
}

// TestAModuleWithReadableObjectsIsNotWarnedAbout guards the warning from firing
// on a working MIB, which would train an operator to ignore it.
func TestAModuleWithReadableObjectsIsNotWarnedAbout(t *testing.T) {
	objects := []*mib.OIDMetadata{
		readable("sysDescr", "1.3.6.1.2.1.1.1"),
		readable("sysUpTime", "1.3.6.1.2.1.1.3"),
		// A table and its entry row are not pollable themselves; their columns
		// are. A module of one table must still count as usable.
		{Name: "ifTable", OID: "1.3.6.1.2.1.2.2", Access: "not-accessible",
			Status: "current", IsTable: true},
	}

	result := assessPollability("SNMPv2-MIB", objects)

	if !result.UsableForPolling() {
		t.Fatal("a module with readable scalars was reported as producing nothing")
	}
	if result.Pollable != 2 {
		t.Errorf("Pollable = %d, want 2", result.Pollable)
	}
	if result.Warning != "" {
		t.Errorf("a working module was warned about: %s", result.Warning)
	}
}

// TestPollabilityHandlesADegenerateModule covers the inputs that are not a
// module at all: nothing stored, and a nil in the slice.
func TestPollabilityHandlesADegenerateModule(t *testing.T) {
	empty := assessPollability("EMPTY-MIB", nil)
	if empty.UsableForPolling() {
		t.Error("a module with no objects was reported as pollable")
	}
	if !strings.Contains(empty.Warning, "no objects") {
		t.Errorf("the warning does not say the module is empty: %s", empty.Warning)
	}

	// A nil entry must not panic and must not be counted as an object. Storage
	// returns pointers, and one absent record should not take the endpoint down
	// after the assignment has already been written.
	holed := assessPollability("HOLED-MIB", []*mib.OIDMetadata{
		nil, readable("sysDescr", "1.3.6.1.2.1.1.1"), nil,
	})
	if holed.Pollable != 1 {
		t.Errorf("Pollable = %d, want 1", holed.Pollable)
	}
	if holed.Warning != "" {
		t.Errorf("a module with one readable object was warned about: %s", holed.Warning)
	}
}

// TestAPartlyResolvedModuleReportsBothCounts covers the mixed case: some objects
// resolved and unreadable, others did not resolve at all. Neither single-cause
// message is honest about that, so it gets its own.
func TestAPartlyResolvedModuleReportsBothCounts(t *testing.T) {
	objects := []*mib.OIDMetadata{
		{Name: "kvmProducts", OID: "1.3.6.1.4.1.21317.1", Access: "not-accessible", Status: "current"},
		{Name: "kvmPortCount", Access: "read-only", Status: "current"},
	}

	result := assessPollability("MIXED-MIB", objects)

	if result.Pollable != 0 {
		t.Errorf("Pollable = %d, want 0", result.Pollable)
	}
	if result.Unresolved != 1 {
		t.Errorf("Unresolved = %d, want 1", result.Unresolved)
	}
	if !strings.Contains(result.Warning, "1 of its 2") {
		t.Errorf("the warning does not report how many failed to resolve: %s", result.Warning)
	}
}

// TestADeprecatedObjectDoesNotCountAsPollable pins the rule the estimator and
// the poller share: a module of obsolete objects produces nothing, and saying
// otherwise would promise data that never arrives.
func TestADeprecatedObjectDoesNotCountAsPollable(t *testing.T) {
	objects := []*mib.OIDMetadata{
		{Name: "ifInNUcastPkts", OID: "1.3.6.1.2.1.2.2.1.12",
			Access: "read-only", Status: "deprecated"},
		{Name: "ipRouteAge", OID: "1.3.6.1.2.1.4.21.1.10",
			Access: "read-write", Status: "obsolete"},
	}

	result := assessPollability("RFC1213-MIB", objects)

	if result.UsableForPolling() {
		t.Fatal("deprecated and obsolete objects were reported as pollable")
	}
	if result.Warning == "" {
		t.Fatal("a module of deprecated objects produced no warning")
	}
}
