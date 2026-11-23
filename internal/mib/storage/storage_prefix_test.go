package storage

import (
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/mib"
)

// TestListOIDs_PrefixRespectsOIDBoundaries verifies that a prefix filter matches
// whole sub-identifiers rather than raw characters. Without boundary awareness
// the prefix "1.3.6.1.2.1.2" also matches "1.3.6.1.2.1.20.x" (ifTable vs the
// unrelated subtree at .20), silently over-selecting OIDs to poll.
func TestListOIDs_PrefixRespectsOIDBoundaries(t *testing.T) {
	store, cleanup := getTestStorage(t)
	defer cleanup()

	stored := []*mib.OIDMetadata{
		// Under 1.3.6.1.2.1.2 — should match.
		{OID: "1.3.6.1.2.1.2", Name: "interfaces", Module: "IF-MIB"},
		{OID: "1.3.6.1.2.1.2.1.0", Name: "ifNumber", Module: "IF-MIB"},
		{OID: "1.3.6.1.2.1.2.2.1.1", Name: "ifIndex", Module: "IF-MIB"},
		// Under 1.3.6.1.2.1.20 — a different subtree that shares a text prefix.
		{OID: "1.3.6.1.2.1.20", Name: "egp", Module: "EGP-MIB"},
		{OID: "1.3.6.1.2.1.20.1.0", Name: "egpInMsgs", Module: "EGP-MIB"},
		// Under 1.3.6.1.2.1.25 — likewise.
		{OID: "1.3.6.1.2.1.25.1.1.0", Name: "hrSystemUptime", Module: "HOST-RESOURCES-MIB"},
	}

	for _, oid := range stored {
		if err := store.StoreOID(oid); err != nil {
			t.Fatalf("store %s: %v", oid.Name, err)
		}
	}

	got, err := store.ListOIDs("1.3.6.1.2.1.2")
	if err != nil {
		t.Fatalf("ListOIDs: %v", err)
	}

	gotNames := make(map[string]bool, len(got))
	for _, oid := range got {
		gotNames[oid.Name] = true
	}

	wantPresent := []string{"interfaces", "ifNumber", "ifIndex"}
	for _, name := range wantPresent {
		if !gotNames[name] {
			t.Errorf("prefix 1.3.6.1.2.1.2 should match %s, but it was missing", name)
		}
	}

	wantAbsent := []string{"egp", "egpInMsgs", "hrSystemUptime"}
	for _, name := range wantAbsent {
		if gotNames[name] {
			t.Errorf("prefix 1.3.6.1.2.1.2 must not match %s (different subtree)", name)
		}
	}

	if len(got) != len(wantPresent) {
		t.Errorf("got %d OIDs, want %d", len(got), len(wantPresent))
	}
}

// TestListOIDs_EmptyPrefixReturnsEverything verifies the unfiltered case still
// returns all stored OIDs.
func TestListOIDs_EmptyPrefixReturnsEverything(t *testing.T) {
	store, cleanup := getTestStorage(t)
	defer cleanup()

	stored := []*mib.OIDMetadata{
		{OID: "1.3.6.1.2.1.1.1.0", Name: "sysDescr", Module: "SNMPv2-MIB"},
		{OID: "1.3.6.1.2.1.2.1.0", Name: "ifNumber", Module: "IF-MIB"},
		{OID: "1.3.6.1.4.1.9.9.109.1.1.1.1.8", Name: "cpmCPUTotal5minRev", Module: "CISCO-PROCESS-MIB"},
	}
	for _, oid := range stored {
		if err := store.StoreOID(oid); err != nil {
			t.Fatalf("store %s: %v", oid.Name, err)
		}
	}

	got, err := store.ListOIDs("")
	if err != nil {
		t.Fatalf("ListOIDs: %v", err)
	}
	if len(got) != len(stored) {
		t.Errorf("got %d OIDs, want %d", len(got), len(stored))
	}
}

// TestListOIDs_PrefixMatchingNothing verifies a prefix with no members returns
// an empty result rather than an error or a partial match.
func TestListOIDs_PrefixMatchingNothing(t *testing.T) {
	store, cleanup := getTestStorage(t)
	defer cleanup()

	if err := store.StoreOID(&mib.OIDMetadata{
		OID: "1.3.6.1.2.1.1.1.0", Name: "sysDescr", Module: "SNMPv2-MIB",
	}); err != nil {
		t.Fatalf("store: %v", err)
	}

	got, err := store.ListOIDs("1.3.6.1.4.1.9999")
	if err != nil {
		t.Fatalf("ListOIDs: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d OIDs, want 0", len(got))
	}
}
