package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble"

	"github.com/jeremyhahn/go-netmon/internal/mib"
)

// A MIB store is written by an importer and read by the poller, the CLI and the
// web UI. A record that cannot be decoded — a partial write, a store written by
// an older schema, a truncated file — must never be handed to a caller as an
// empty-but-valid record, because an OIDMetadata with no Access and no Status
// silently drops out of every poll without anything reporting why.
//
// These tests write the malformed bytes directly through the underlying store,
// which is the only way to produce a record the marshaller would never emit.

// corrupt writes bytes that are not valid JSON under key.
func corrupt(t *testing.T, s *Storage, key string) {
	t.Helper()
	if err := s.db.Set([]byte(key), []byte("{not json"), pebble.Sync); err != nil {
		t.Fatalf("write corrupt record at %s: %v", key, err)
	}
}

func TestNewRejectsUnusablePath(t *testing.T) {
	// A regular file where the store's directory should be.
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, []byte("occupied"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	store, err := New(path)
	if err == nil {
		_ = store.Close()
		t.Fatal("New succeeded on a path that cannot hold a store")
	}
	if store != nil {
		t.Error("New returned a store alongside an error")
	}
}

func TestGetOIDRejectsCorruptRecord(t *testing.T) {
	store, cleanup := getTestStorage(t)
	defer cleanup()

	corrupt(t, store, PrefixOID+"1.3.6.1.2.1.1.1.0")

	meta, err := store.GetOID("1.3.6.1.2.1.1.1.0")
	if err == nil {
		t.Fatalf("GetOID returned %+v for a corrupt record instead of an error", meta)
	}
	if meta != nil {
		t.Error("GetOID returned metadata alongside an error")
	}
}

// TestListOIDsSkipsCorruptRecord pins the deliberate asymmetry with GetOID: a
// listing must not fail wholesale because one of thousands of records is bad,
// so the bad one is dropped and the rest are returned.
func TestListOIDsSkipsCorruptRecord(t *testing.T) {
	store, cleanup := getTestStorage(t)
	defer cleanup()

	good := &mib.OIDMetadata{
		OID: "1.3.6.1.2.1.1.1.0", Name: "sysDescr", Module: "SNMPv2-MIB",
		Type: "DisplayString", Access: "read-only", Status: "current",
	}
	if err := store.StoreOID(good); err != nil {
		t.Fatalf("StoreOID: %v", err)
	}
	corrupt(t, store, PrefixOID+"1.3.6.1.2.1.1.2.0")

	oids, err := store.ListOIDs("")
	if err != nil {
		t.Fatalf("ListOIDs: %v", err)
	}
	if len(oids) != 1 {
		t.Fatalf("ListOIDs returned %d records, want only the readable one", len(oids))
	}
	if oids[0].Name != "sysDescr" {
		t.Errorf("ListOIDs returned %q, want %q", oids[0].Name, "sysDescr")
	}
}

// TestCountIncludesRecordsListOIDsSkips documents a discrepancy an operator can
// hit: Count is a key count and does not decode records, so a store holding a
// corrupt record reports a higher count than the number of objects any listing
// can return.
func TestCountIncludesRecordsListOIDsSkips(t *testing.T) {
	store, cleanup := getTestStorage(t)
	defer cleanup()

	if err := store.StoreOID(&mib.OIDMetadata{
		OID: "1.3.6.1.2.1.1.1.0", Name: "sysDescr", Module: "SNMPv2-MIB", Status: "current",
	}); err != nil {
		t.Fatalf("StoreOID: %v", err)
	}
	corrupt(t, store, PrefixOID+"1.3.6.1.2.1.1.2.0")

	counted, _, _, err := store.Count()
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	listed, err := store.ListOIDs("")
	if err != nil {
		t.Fatalf("ListOIDs: %v", err)
	}

	if counted != 2 {
		t.Errorf("Count reported %d OID records, want 2", counted)
	}
	if len(listed) != 1 {
		t.Errorf("ListOIDs returned %d records, want 1", len(listed))
	}
}

func TestDeleteOIDRejectsCorruptRecord(t *testing.T) {
	store, cleanup := getTestStorage(t)
	defer cleanup()

	corrupt(t, store, PrefixOID+"1.3.6.1.2.1.1.1.0")

	// The record cannot be decoded, so its module and name — and therefore the
	// name mapping and index entry to remove with it — are unknown. Deleting
	// only the record would leave both behind.
	if err := store.DeleteOID("1.3.6.1.2.1.1.1.0"); err == nil {
		t.Fatal("DeleteOID succeeded on a record it could not decode")
	}
}

// TestGetOIDByNameWithDanglingMapping covers a name index pointing at a record
// that is no longer there. The lookup must report that rather than return a
// zero-valued object.
func TestGetOIDByNameWithDanglingMapping(t *testing.T) {
	store, cleanup := getTestStorage(t)
	defer cleanup()

	if err := store.db.Set(
		[]byte(PrefixName+"IF-MIB:ifInOctets"),
		[]byte("1.3.6.1.2.1.2.2.1.10"),
		pebble.Sync,
	); err != nil {
		t.Fatalf("write name mapping: %v", err)
	}

	meta, err := store.GetOIDByName("IF-MIB", "ifInOctets")
	if err == nil {
		t.Fatalf("GetOIDByName returned %+v for a dangling mapping instead of an error", meta)
	}
}

func TestGetOIDByNameUnknownName(t *testing.T) {
	store, cleanup := getTestStorage(t)
	defer cleanup()

	_, err := store.GetOIDByName("IF-MIB", "nothingLikeThis")
	if err == nil {
		t.Fatal("GetOIDByName succeeded for a name that was never stored")
	}
	if !strings.Contains(err.Error(), "nothingLikeThis") {
		t.Errorf("error %q does not name what was looked up", err)
	}
}

func TestGetModuleRejectsCorruptRecord(t *testing.T) {
	store, cleanup := getTestStorage(t)
	defer cleanup()

	corrupt(t, store, PrefixModule+"IF-MIB")

	if _, err := store.GetModule("IF-MIB"); err == nil {
		t.Fatal("GetModule returned a corrupt record as valid module info")
	}
}

func TestListModulesSkipsCorruptRecord(t *testing.T) {
	store, cleanup := getTestStorage(t)
	defer cleanup()

	if err := store.StoreModule(&mib.ModuleInfo{Name: "IF-MIB", OIDCount: 3}); err != nil {
		t.Fatalf("StoreModule: %v", err)
	}
	corrupt(t, store, PrefixModule+"BROKEN-MIB")

	modules, err := store.ListModules()
	if err != nil {
		t.Fatalf("ListModules: %v", err)
	}
	if len(modules) != 1 || modules[0].Name != "IF-MIB" {
		t.Fatalf("ListModules returned %d modules, want only the readable one", len(modules))
	}
}

func TestGetModuleUnknown(t *testing.T) {
	store, cleanup := getTestStorage(t)
	defer cleanup()

	_, err := store.GetModule("NO-SUCH-MIB")
	if err == nil {
		t.Fatal("GetModule succeeded for a module that was never stored")
	}
	if !strings.Contains(err.Error(), "NO-SUCH-MIB") {
		t.Errorf("error %q does not name the module", err)
	}
}

func TestGetTableRejectsCorruptRecord(t *testing.T) {
	store, cleanup := getTestStorage(t)
	defer cleanup()

	corrupt(t, store, PrefixTable+"IF-MIB:ifTable")

	if _, err := store.GetTable("IF-MIB", "ifTable"); err == nil {
		t.Fatal("GetTable returned a corrupt record as valid table info")
	}
}

func TestGetTableUnknown(t *testing.T) {
	store, cleanup := getTestStorage(t)
	defer cleanup()

	_, err := store.GetTable("IF-MIB", "noSuchTable")
	if err == nil {
		t.Fatal("GetTable succeeded for a table that was never stored")
	}
	if !strings.Contains(err.Error(), "noSuchTable") {
		t.Errorf("error %q does not name the table", err)
	}
}

func TestListTablesSkipsCorruptRecord(t *testing.T) {
	store, cleanup := getTestStorage(t)
	defer cleanup()

	if err := store.StoreTable(&mib.TableInfo{
		OID: "1.3.6.1.2.1.2.2", Name: "ifTable", Module: "IF-MIB",
	}); err != nil {
		t.Fatalf("StoreTable: %v", err)
	}
	corrupt(t, store, PrefixTable+"IF-MIB:brokenTable")

	tables, err := store.ListTables("IF-MIB")
	if err != nil {
		t.Fatalf("ListTables: %v", err)
	}
	if len(tables) != 1 || tables[0].Name != "ifTable" {
		t.Fatalf("ListTables returned %d tables, want only the readable one", len(tables))
	}
}

// TestListTablesOfUnknownModule checks that asking for a module with no tables
// is an empty result and not an error, so a caller can iterate modules without
// special-casing the ones that define no tables.
func TestListTablesOfUnknownModule(t *testing.T) {
	store, cleanup := getTestStorage(t)
	defer cleanup()

	tables, err := store.ListTables("NO-SUCH-MIB")
	if err != nil {
		t.Fatalf("ListTables: %v", err)
	}
	if len(tables) != 0 {
		t.Errorf("ListTables returned %d tables for an unknown module, want none", len(tables))
	}
}

// TestCloseIsIdempotent covers the double-close guard. Shutdown paths call
// Close from more than one place, and Pebble panics on a second close.
func TestCloseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Errorf("second Close returned %v, want nil", err)
	}
}
