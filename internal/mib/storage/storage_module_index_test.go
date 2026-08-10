package storage

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/cockroachdb/pebble"

	"github.com/jeremyhahn/go-netmon/internal/mib"
)

// moduleIndexFixture stores objects across two modules, including one object
// with no numeric OID (a group), which is keyed by module and name rather than
// by OID and so exercises the second form of index key.
func moduleIndexFixture(t *testing.T) (*Storage, func()) {
	t.Helper()

	store, cleanup := getTestStorage(t)

	oids := []*mib.OIDMetadata{
		{OID: "1.3.6.1.2.1.2.2.1.10", Name: "ifInOctets", Module: "IF-MIB", Type: "Counter32", Access: "read-only", Status: "current", IsColumn: true, Pollable: true},
		{OID: "1.3.6.1.2.1.2.2.1.16", Name: "ifOutOctets", Module: "IF-MIB", Type: "Counter32", Access: "read-only", Status: "current", IsColumn: true, Pollable: true},
		{OID: "1.3.6.1.2.1.2.2", Name: "ifTable", Module: "IF-MIB", Access: "not-accessible", Status: "current", IsTable: true},
		{Name: "ifGeneralGroup", Module: "IF-MIB", Status: "current"},
		{OID: "1.3.6.1.2.1.1.1.0", Name: "sysDescr", Module: "SNMPv2-MIB", Type: "DisplayString", Access: "read-only", Status: "current", IsScalar: true, Pollable: true},
		{OID: "1.3.6.1.2.1.1.3.0", Name: "sysUpTime", Module: "SNMPv2-MIB", Type: "TimeTicks", Access: "read-only", Status: "current", IsScalar: true, Pollable: true},
	}
	for _, oid := range oids {
		if err := store.StoreOID(oid); err != nil {
			cleanup()
			t.Fatalf("store %s: %v", oid.Name, err)
		}
	}

	return store, cleanup
}

// names collects the object names of a result set for order-independent
// comparison.
func names(oids []*mib.OIDMetadata) map[string]bool {
	set := make(map[string]bool, len(oids))
	for _, oid := range oids {
		set[oid.Name] = true
	}
	return set
}

// countKeys returns the number of keys stored under a prefix.
func countKeys(t *testing.T, store *Storage, prefix string) int {
	t.Helper()

	iter, err := store.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(prefix),
		UpperBound: []byte(prefixSuccessor(prefix)),
	})
	if err != nil {
		t.Fatalf("iterate %s: %v", prefix, err)
	}
	defer iter.Close()

	count := 0
	for iter.First(); iter.Valid(); iter.Next() {
		count++
	}
	return count
}

// keyExists reports whether a raw key is present in the store.
func keyExists(t *testing.T, store *Storage, key []byte) bool {
	t.Helper()

	_, closer, err := store.db.Get(key)
	if err != nil {
		if err == pebble.ErrNotFound {
			return false
		}
		t.Fatalf("get %s: %v", key, err)
	}
	closer.Close()

	return true
}

// makeLegacyStore removes every index key, reproducing a store written by a
// build that predates the module index.
func makeLegacyStore(t *testing.T, store *Storage) {
	t.Helper()

	for _, prefix := range []string{PrefixModuleOID, PrefixModuleIndexed} {
		if err := store.db.DeleteRange([]byte(prefix), []byte(prefixSuccessor(prefix)), pebble.Sync); err != nil {
			t.Fatalf("strip %s: %v", prefix, err)
		}
	}

	if got := countKeys(t, store, PrefixModuleOID); got != 0 {
		t.Fatalf("legacy store still has %d index entries", got)
	}
}

// storeUnindexedOID writes an OID record without its index entry, the way a
// build without the index would. A lookup that returns it has scanned the whole
// store rather than consulting the index.
func storeUnindexedOID(t *testing.T, store *Storage, meta *mib.OIDMetadata) {
	t.Helper()

	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal %s: %v", meta.Name, err)
	}
	if err := store.db.Set([]byte(PrefixOID+oidKeyValue(meta)), data, pebble.Sync); err != nil {
		t.Fatalf("write unindexed %s: %v", meta.Name, err)
	}
}

// TestListModuleOIDs_ReturnsOnlyItsModule verifies the index selects exactly one
// module's objects, including objects with no numeric OID.
func TestListModuleOIDs_ReturnsOnlyItsModule(t *testing.T) {
	store, cleanup := moduleIndexFixture(t)
	defer cleanup()

	got, err := store.ListModuleOIDs("IF-MIB")
	if err != nil {
		t.Fatalf("ListModuleOIDs: %v", err)
	}

	gotNames := names(got)
	for _, want := range []string{"ifInOctets", "ifOutOctets", "ifTable", "ifGeneralGroup"} {
		if !gotNames[want] {
			t.Errorf("missing %s from IF-MIB", want)
		}
	}
	for _, unwanted := range []string{"sysDescr", "sysUpTime"} {
		if gotNames[unwanted] {
			t.Errorf("%s belongs to SNMPv2-MIB and must not be returned", unwanted)
		}
	}
	if len(got) != 4 {
		t.Errorf("got %d objects, want 4", len(got))
	}
}

// TestListModuleOIDs_EmptyModuleRejected verifies that the module-scoped lookup
// refuses an empty module name instead of silently returning nothing or, worse,
// everything.
func TestListModuleOIDs_EmptyModuleRejected(t *testing.T) {
	store, cleanup := moduleIndexFixture(t)
	defer cleanup()

	got, err := store.ListModuleOIDs("")
	if got != nil {
		t.Errorf("expected no results, got %d", len(got))
	}

	var want *ModuleRequiredError
	if !errors.As(err, &want) {
		t.Fatalf("got error %v, want *ModuleRequiredError", err)
	}
}

// TestListModuleOIDs_UnknownModule verifies an unknown module is an empty
// result, not an error: callers list objects for MIBs that may not be loaded.
func TestListModuleOIDs_UnknownModule(t *testing.T) {
	store, cleanup := moduleIndexFixture(t)
	defer cleanup()

	got, err := store.ListModuleOIDs("NO-SUCH-MIB")
	if err != nil {
		t.Fatalf("ListModuleOIDs: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d objects, want 0", len(got))
	}
}

// TestListModuleOIDs_TracksDeletes verifies DeleteOID removes the index entry.
// A stale entry would resurrect a deleted object on every module lookup.
func TestListModuleOIDs_TracksDeletes(t *testing.T) {
	store, cleanup := moduleIndexFixture(t)
	defer cleanup()

	if err := store.DeleteOID("1.3.6.1.2.1.2.2.1.10"); err != nil {
		t.Fatalf("DeleteOID: %v", err)
	}

	got, err := store.ListModuleOIDs("IF-MIB")
	if err != nil {
		t.Fatalf("ListModuleOIDs: %v", err)
	}
	if names(got)["ifInOctets"] {
		t.Error("deleted object is still indexed under its module")
	}
	if len(got) != 3 {
		t.Errorf("got %d objects, want 3", len(got))
	}
}

// TestListModuleOIDs_RewriteDoesNotDuplicate verifies re-storing an object
// leaves exactly one index entry, as MIB reloads rewrite every object.
func TestListModuleOIDs_RewriteDoesNotDuplicate(t *testing.T) {
	store, cleanup := moduleIndexFixture(t)
	defer cleanup()

	updated := &mib.OIDMetadata{
		OID: "1.3.6.1.2.1.2.2.1.10", Name: "ifInOctets", Module: "IF-MIB",
		Type: "Counter64", Access: "read-only", Status: "current", IsColumn: true, Pollable: true,
	}
	if err := store.StoreOID(updated); err != nil {
		t.Fatalf("StoreOID: %v", err)
	}

	got, err := store.ListModuleOIDs("IF-MIB")
	if err != nil {
		t.Fatalf("ListModuleOIDs: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d objects, want 4", len(got))
	}

	for _, oid := range got {
		if oid.Name == "ifInOctets" && oid.Type != "Counter64" {
			t.Errorf("got stale type %s, want Counter64", oid.Type)
		}
	}
}

// TestListModuleOIDs_BackfillsLegacyStore covers the upgrade path: a store
// written before the index exists must answer module lookups correctly and
// repair itself, without a re-import.
func TestListModuleOIDs_BackfillsLegacyStore(t *testing.T) {
	store, cleanup := moduleIndexFixture(t)
	defer cleanup()

	makeLegacyStore(t, store)

	got, err := store.ListModuleOIDs("IF-MIB")
	if err != nil {
		t.Fatalf("ListModuleOIDs: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d objects from legacy store, want 4", len(got))
	}

	// One scan repairs the whole store, not only the module asked for.
	if got := countKeys(t, store, PrefixModuleOID); got != 6 {
		t.Errorf("index holds %d entries after backfill, want 6", got)
	}
	if !keyExists(t, store, moduleIndexedKey("SNMPv2-MIB")) {
		t.Error("SNMPv2-MIB was not marked as indexed")
	}

	// A record written without an index entry proves the second lookup is
	// served from the index: only a full scan could find it.
	storeUnindexedOID(t, store, &mib.OIDMetadata{
		OID: "1.3.6.1.2.1.2.2.1.99", Name: "ifPhantom", Module: "IF-MIB",
	})

	got, err = store.ListModuleOIDs("IF-MIB")
	if err != nil {
		t.Fatalf("ListModuleOIDs after backfill: %v", err)
	}
	if names(got)["ifPhantom"] {
		t.Error("lookup rescanned the store instead of using the repaired index")
	}
}

// TestListModuleOIDs_BackfillMarksEmptyModule verifies a module with no objects
// is marked as indexed, so it does not force a full scan on every lookup.
func TestListModuleOIDs_BackfillMarksEmptyModule(t *testing.T) {
	store, cleanup := moduleIndexFixture(t)
	defer cleanup()

	makeLegacyStore(t, store)

	got, err := store.ListModuleOIDs("EMPTY-MIB")
	if err != nil {
		t.Fatalf("ListModuleOIDs: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d objects, want 0", len(got))
	}

	if !keyExists(t, store, moduleIndexedKey("EMPTY-MIB")) {
		t.Fatal("EMPTY-MIB was not marked as indexed")
	}

	storeUnindexedOID(t, store, &mib.OIDMetadata{
		OID: "1.3.6.1.4.1.99.1", Name: "phantom", Module: "EMPTY-MIB",
	})

	got, err = store.ListModuleOIDs("EMPTY-MIB")
	if err != nil {
		t.Fatalf("ListModuleOIDs after marking: %v", err)
	}
	if len(got) != 0 {
		t.Error("empty module rescanned the store instead of trusting its marker")
	}
}

// TestGetPollableOIDs_BackfillsLegacyStore verifies the poller's entry point
// also survives the upgrade, since it is what an existing deployment hits first.
func TestGetPollableOIDs_BackfillsLegacyStore(t *testing.T) {
	store, cleanup := moduleIndexFixture(t)
	defer cleanup()

	makeLegacyStore(t, store)

	got, err := store.GetPollableOIDs("IF-MIB")
	if err != nil {
		t.Fatalf("GetPollableOIDs: %v", err)
	}

	gotNames := names(got)
	for _, want := range []string{"ifInOctets", "ifOutOctets"} {
		if !gotNames[want] {
			t.Errorf("missing pollable %s", want)
		}
	}
	if len(got) != 2 {
		t.Errorf("got %d pollable objects, want 2", len(got))
	}
}

// TestListModuleOIDs_ConcurrentBackfill verifies that simultaneous lookups on an
// unindexed store all return complete results: the backfill takes the write lock
// and re-checks, so at most one rebuild runs and none of the readers sees a
// half-built index.
func TestListModuleOIDs_ConcurrentBackfill(t *testing.T) {
	store, cleanup := moduleIndexFixture(t)
	defer cleanup()

	makeLegacyStore(t, store)

	const readers = 8
	counts := make([]int, readers)
	errs := make([]error, readers)

	var wg sync.WaitGroup
	for i := range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			oids, err := store.ListModuleOIDs("IF-MIB")
			counts[i], errs[i] = len(oids), err
		}()
	}
	wg.Wait()

	for i := range readers {
		if errs[i] != nil {
			t.Fatalf("reader %d: %v", i, errs[i])
		}
		if counts[i] != 4 {
			t.Errorf("reader %d got %d objects, want 4", i, counts[i])
		}
	}
}

// TestModuleIndexKeysStayOutOfModuleScans verifies the index keyspace does not
// overlap the module keyspace. Index entries outnumber module records by
// thousands to one, so any overlap would swamp both ListModules and Count.
func TestModuleIndexKeysStayOutOfModuleScans(t *testing.T) {
	store, cleanup := moduleIndexFixture(t)
	defer cleanup()

	if err := store.StoreModule(&mib.ModuleInfo{Name: "IF-MIB", OIDCount: 4}); err != nil {
		t.Fatalf("StoreModule: %v", err)
	}
	// Force the "indexed" markers to exist as well.
	if _, err := store.ListModuleOIDs("IF-MIB"); err != nil {
		t.Fatalf("ListModuleOIDs: %v", err)
	}

	modules, err := store.ListModules()
	if err != nil {
		t.Fatalf("ListModules: %v", err)
	}
	if len(modules) != 1 {
		t.Errorf("ListModules returned %d modules, want 1", len(modules))
	}

	oidCount, moduleCount, _, err := store.Count()
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if oidCount != 6 {
		t.Errorf("counted %d OIDs, want 6", oidCount)
	}
	if moduleCount != 1 {
		t.Errorf("counted %d modules, want 1", moduleCount)
	}
}

// TestListModuleOIDs_SkipsDanglingIndexEntry verifies an index entry whose
// record is gone does not hide the rest of the module. A crash between the
// record write and a later delete must degrade to one missing object, not to an
// empty MIB.
func TestListModuleOIDs_SkipsDanglingIndexEntry(t *testing.T) {
	store, cleanup := moduleIndexFixture(t)
	defer cleanup()

	dangling := moduleOIDIndexKey("IF-MIB", "1.3.6.1.2.1.2.2.1.99")
	if err := store.db.Set(dangling, []byte("1.3.6.1.2.1.2.2.1.99"), pebble.Sync); err != nil {
		t.Fatalf("write dangling entry: %v", err)
	}

	got, err := store.ListModuleOIDs("IF-MIB")
	if err != nil {
		t.Fatalf("ListModuleOIDs: %v", err)
	}
	if len(got) != 4 {
		t.Errorf("got %d objects, want the 4 that still exist", len(got))
	}
}

// TestDeleteOID_UnknownOID verifies deleting something that was never stored is
// reported rather than silently treated as success, which would mask a caller
// deleting the wrong key.
func TestDeleteOID_UnknownOID(t *testing.T) {
	store, cleanup := moduleIndexFixture(t)
	defer cleanup()

	if err := store.DeleteOID("1.3.6.1.4.1.99999.1"); err == nil {
		t.Error("expected an error deleting an OID that is not stored")
	}

	// The store is untouched.
	got, err := store.ListModuleOIDs("IF-MIB")
	if err != nil {
		t.Fatalf("ListModuleOIDs: %v", err)
	}
	if len(got) != 4 {
		t.Errorf("got %d objects, want 4", len(got))
	}
}

// TestSelectModuleOIDs_RequiresModule verifies the module-scoped queries reject
// an empty module rather than quietly returning nothing, which reads as "this
// MIB has no columns" at the call site.
func TestSelectModuleOIDs_RequiresModule(t *testing.T) {
	store, cleanup := moduleIndexFixture(t)
	defer cleanup()

	queries := map[string]func(string) ([]mib.OIDMetadata, error){
		"GetMIBColumns":    store.GetMIBColumns,
		"GetScalars":       store.GetScalars,
		"GetMetricColumns": store.GetMetricColumns,
		"GetStatusColumns": store.GetStatusColumns,
	}

	for name, query := range queries {
		t.Run(name, func(t *testing.T) {
			if _, err := query(""); err == nil {
				t.Error("expected an error for an empty module name")
			}
			// A module that exists still answers.
			got, err := query("IF-MIB")
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if name == "GetMIBColumns" && len(got) != 2 {
				t.Errorf("got %d columns, want 2", len(got))
			}
		})
	}
}
