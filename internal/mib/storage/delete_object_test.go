package storage

import (
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/mib"
)

// Deleting an object that has no numeric OID.
//
// Objects are keyed by their OID where they have one and by module and name
// where they do not -- groups, notifications, and anything a module defines that
// resolved to no identifier. Deletion only ever took an OID, so for the second
// kind it looked up a key that was never written, removed nothing, and reported
// failure. Reloading a module accumulated one orphan per such object every time,
// and no re-import could clear them because the clear had the same blind spot.
//
// A live store had 79 orphans for one module: the listing showed 159 records for
// the 80 objects the module declares.

// TestAnObjectWithNoOIDCanBeDeleted is the case that leaked.
func TestAnObjectWithNoOIDCanBeDeleted(t *testing.T) {
	store, cleanup := getTestStorage(t)
	defer cleanup()

	group := &mib.OIDMetadata{
		Module: "KNGeneric-MIB", Name: "kvmGroup",
		Access: "not-accessible", Status: "current",
	}
	if err := store.StoreOID(group); err != nil {
		t.Fatalf("StoreOID: %v", err)
	}

	if objects, err := store.ListModuleOIDs("KNGeneric-MIB"); err != nil || len(objects) != 1 {
		t.Fatalf("stored object not listed: %d objects, %v", len(objects), err)
	}

	if err := store.DeleteObject(group); err != nil {
		t.Fatalf("DeleteObject on an object with no OID: %v", err)
	}

	objects, err := store.ListModuleOIDs("KNGeneric-MIB")
	if err != nil {
		t.Fatalf("ListModuleOIDs: %v", err)
	}
	if len(objects) != 0 {
		t.Errorf("%d records survived the delete: %s", len(objects), objects[0].Name)
	}
}

// TestDeletingByOIDCannotReachIt is the bug stated directly, so the reason
// DeleteObject exists cannot be optimized away.
func TestDeletingByOIDCannotReachIt(t *testing.T) {
	store, cleanup := getTestStorage(t)
	defer cleanup()

	group := &mib.OIDMetadata{Module: "TEST-MIB", Name: "testGroup", Status: "current"}
	if err := store.StoreOID(group); err != nil {
		t.Fatalf("StoreOID: %v", err)
	}

	// This is what the module clear used to do.
	if err := store.DeleteOID(group.OID); err == nil {
		t.Error("deleting by an empty OID reported success; it cannot have removed anything")
	}

	objects, _ := store.ListModuleOIDs("TEST-MIB")
	if len(objects) != 1 {
		t.Fatalf("expected the record to survive deletion by OID, found %d", len(objects))
	}

	// And the record is reachable by the key it was actually stored under.
	if err := store.DeleteObject(group); err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}
	if objects, _ := store.ListModuleOIDs("TEST-MIB"); len(objects) != 0 {
		t.Errorf("%d records survived DeleteObject", len(objects))
	}
}

// TestAnObjectWithAnOIDStillDeletesByIt keeps the ordinary path working: the
// overwhelming majority of objects have an OID and are keyed by it.
func TestAnObjectWithAnOIDStillDeletesByIt(t *testing.T) {
	store, cleanup := getTestStorage(t)
	defer cleanup()

	scalar := &mib.OIDMetadata{
		OID: "1.3.6.1.2.1.1.1", Module: "SNMPv2-MIB", Name: "sysDescr",
		Type: "DisplayString", Access: "read-only", Status: "current", IsScalar: true,
	}
	if err := store.StoreOID(scalar); err != nil {
		t.Fatalf("StoreOID: %v", err)
	}

	if err := store.DeleteObject(scalar); err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}
	if objects, _ := store.ListModuleOIDs("SNMPv2-MIB"); len(objects) != 0 {
		t.Errorf("%d records survived", len(objects))
	}
	// And it is gone from the OID lookup too, not merely from the module index.
	if found, err := store.GetOID("1.3.6.1.2.1.1.1"); err == nil && found != nil {
		t.Error("the object is still reachable by OID after deletion")
	}
}

// TestDeleteObjectToleratesNothing covers the empty case, since the loader
// iterates whatever the store returned.
func TestDeleteObjectToleratesNothing(t *testing.T) {
	store, cleanup := getTestStorage(t)
	defer cleanup()
	if err := store.DeleteObject(nil); err != nil {
		t.Errorf("deleting nothing reported %v, want no error", err)
	}
}
