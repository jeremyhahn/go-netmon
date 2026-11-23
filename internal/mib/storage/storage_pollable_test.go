package storage

import (
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/mib"
)

// pollableFixture stores a mix of pollable and non-pollable OIDs across two
// modules and returns the store.
func pollableFixture(t *testing.T) (*Storage, func()) {
	t.Helper()

	store, cleanup := getTestStorage(t)

	oids := []*mib.OIDMetadata{
		{
			OID: "1.3.6.1.2.1.2.2.1.10", Name: "ifInOctets", Module: "IF-MIB",
			Type: "Counter32", Access: "read-only", Status: "current", IsColumn: true, Pollable: true,
		},
		{
			OID: "1.3.6.1.2.1.2.2.1.16", Name: "ifOutOctets", Module: "IF-MIB",
			Type: "Counter32", Access: "read-only", Status: "current", IsColumn: true, Pollable: true,
		},
		{
			OID: "1.3.6.1.2.1.2.2", Name: "ifTable", Module: "IF-MIB",
			Access: "not-accessible", Status: "current", IsTable: true, Pollable: false,
		},
		{
			OID: "1.3.6.1.2.1.1.1", Name: "sysDescr", Module: "SNMPv2-MIB",
			Type: "DisplayString", Access: "read-only", Status: "current", IsScalar: true, Pollable: true,
		},
	}
	for _, oid := range oids {
		if err := store.StoreOID(oid); err != nil {
			cleanup()
			t.Fatalf("store %s: %v", oid.Name, err)
		}
	}
	return store, cleanup
}

// TestGetPollableOIDs_EmptyModuleReturnsAllModules verifies that an empty
// module argument means "every module", matching ListOIDs("") which means
// "every OID". Filtering on Module == "" instead can never match, since every
// stored OID carries a module name, so callers asking for everything silently
// get nothing.
func TestGetPollableOIDs_EmptyModuleReturnsAllModules(t *testing.T) {
	store, cleanup := pollableFixture(t)
	defer cleanup()

	got, err := store.GetPollableOIDs("")
	if err != nil {
		t.Fatalf("GetPollableOIDs: %v", err)
	}

	names := make(map[string]bool, len(got))
	for _, oid := range got {
		names[oid.Name] = true
	}

	for _, want := range []string{"ifInOctets", "ifOutOctets", "sysDescr"} {
		if !names[want] {
			t.Errorf("expected %s in results", want)
		}
	}
	if names["ifTable"] {
		t.Error("ifTable is not pollable and must be excluded")
	}
	if len(got) != 3 {
		t.Errorf("got %d pollable OIDs, want 3", len(got))
	}
}

// TestGetPollableOIDs_FilteredByModule verifies module scoping still works.
func TestGetPollableOIDs_FilteredByModule(t *testing.T) {
	store, cleanup := pollableFixture(t)
	defer cleanup()

	got, err := store.GetPollableOIDs("IF-MIB")
	if err != nil {
		t.Fatalf("GetPollableOIDs: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("got %d OIDs for IF-MIB, want 2", len(got))
	}
	for _, oid := range got {
		if oid.Module != "IF-MIB" {
			t.Errorf("got OID from module %s, want only IF-MIB", oid.Module)
		}
	}
}

// TestGetPollableOIDs_UnknownModule verifies an unknown module yields an empty
// result rather than an error or the full set.
func TestGetPollableOIDs_UnknownModule(t *testing.T) {
	store, cleanup := pollableFixture(t)
	defer cleanup()

	got, err := store.GetPollableOIDs("NO-SUCH-MIB")
	if err != nil {
		t.Fatalf("GetPollableOIDs: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d OIDs for unknown module, want 0", len(got))
	}
}
