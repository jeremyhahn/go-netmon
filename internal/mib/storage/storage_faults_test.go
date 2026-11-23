package storage

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/vfs"
	"github.com/cockroachdb/pebble/vfs/errorfs"

	"github.com/jeremyhahn/go-netmon/internal/mib"
)

// The store keeps a MIB import durable and is read on every poll, every CLI
// lookup and every web UI page. When the disk underneath it starts failing —
// a bad sector, a full or disconnected volume — each read path has to report
// that rather than answer as though the store were merely empty. An empty
// answer is indistinguishable from "this module has no objects", which is how
// a device silently stops being polled with nothing in the logs.
//
// These tests run the store on an in-memory filesystem whose reads can be
// turned off part-way through. Nothing touches the host filesystem, and the
// records are flushed to a table first so that reads genuinely go through the
// filesystem rather than being served from the memtable.

// faultyStore returns a store on an in-memory filesystem holding the fixture
// below, together with a switch that makes every subsequent read fail.
//
// Only reads are injected. Pebble treats a failed write as unrecoverable and
// aborts the process rather than returning an error, so the write-side error
// branches in this package cannot be reached from a test.
func faultyStore(t *testing.T) (store *Storage, failReads func()) {
	t.Helper()

	var failing atomic.Bool
	fs := errorfs.Wrap(vfs.NewMem(), errorfs.InjectorFunc(func(op errorfs.Op, _ string) error {
		if failing.Load() && op.OpKind() == errorfs.OpKindRead {
			return errorfs.ErrInjected
		}
		return nil
	}))

	db, err := pebble.Open("mib", &pebble.Options{FS: fs})
	if err != nil {
		t.Fatalf("open in-memory store: %v", err)
	}
	t.Cleanup(func() {
		failing.Store(false) // disarm so the close can complete
		_ = db.Close()
	})

	store = &Storage{db: db, path: "mib"}

	if err := store.StoreOID(&mib.OIDMetadata{
		OID: "1.3.6.1.2.1.1.1.0", Name: "sysDescr", Module: "SNMPv2-MIB",
		Type: "DisplayString", Access: "read-only", Status: "current", IsScalar: true,
		TableOID: "",
	}); err != nil {
		t.Fatalf("StoreOID: %v", err)
	}
	if err := store.StoreModule(&mib.ModuleInfo{Name: "SNMPv2-MIB", OIDCount: 1}); err != nil {
		t.Fatalf("StoreModule: %v", err)
	}
	if err := store.StoreTable(&mib.TableInfo{
		OID: "1.3.6.1.2.1.2.2", Name: "ifTable", Module: "IF-MIB",
	}); err != nil {
		t.Fatalf("StoreTable: %v", err)
	}

	// Flush so the records live in a table on the filesystem; anything still
	// in the memtable would be served without a read the injector can see.
	if err := db.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	return store, func() { failing.Store(true) }
}

// injected reports whether err came from the fault injector rather than from
// the store deciding a record was absent.
func injected(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("a failing read was reported as success")
	}
	if !strings.Contains(err.Error(), errorfs.ErrInjected.Error()) {
		t.Errorf("error %q does not carry the underlying I/O failure", err)
	}
}

func TestGetOIDReportsReadFailure(t *testing.T) {
	store, failReads := faultyStore(t)
	failReads()

	meta, err := store.GetOID("1.3.6.1.2.1.1.1.0")
	injected(t, err)
	if meta != nil {
		t.Error("GetOID returned metadata alongside an error")
	}
}

// TestGetOIDDistinguishesAbsentFromUnreadable is the point of the whole file:
// "not found" and "cannot be read" must not look the same to a caller, because
// the first is a normal answer and the second needs an operator.
func TestGetOIDDistinguishesAbsentFromUnreadable(t *testing.T) {
	// Two independent stores: reading a record on a healthy one warms Pebble's
	// block cache, and a warmed read would never reach the injector.
	healthy, _ := faultyStore(t)
	_, absent := healthy.GetOID("1.3.6.1.4.1.9999")
	if absent == nil {
		t.Fatal("GetOID succeeded for an OID that was never stored")
	}
	if strings.Contains(absent.Error(), errorfs.ErrInjected.Error()) {
		t.Fatal("the fixture is wrong: reads are already failing")
	}

	broken, failReads := faultyStore(t)
	failReads()
	_, unreadable := broken.GetOID("1.3.6.1.2.1.1.1.0")
	injected(t, unreadable)

	if absent.Error() == unreadable.Error() {
		t.Error("an absent record and an unreadable one produce the same error")
	}
}

func TestGetOIDByNameReportsReadFailure(t *testing.T) {
	store, failReads := faultyStore(t)
	failReads()

	_, err := store.GetOIDByName("SNMPv2-MIB", "sysDescr")
	injected(t, err)
}

func TestGetModuleReportsReadFailure(t *testing.T) {
	store, failReads := faultyStore(t)
	failReads()

	_, err := store.GetModule("SNMPv2-MIB")
	injected(t, err)
}

func TestGetTableReportsReadFailure(t *testing.T) {
	store, failReads := faultyStore(t)
	failReads()

	_, err := store.GetTable("IF-MIB", "ifTable")
	injected(t, err)
}

func TestDeleteOIDReportsReadFailure(t *testing.T) {
	store, failReads := faultyStore(t)
	failReads()

	// The record has to be read before it can be deleted, because its module
	// and name determine which name mapping and index entry go with it.
	err := store.DeleteOID("1.3.6.1.2.1.1.1.0")
	injected(t, err)
}

// TestListingsReportReadFailure covers the iterating reads. A listing that
// swallowed an I/O error would return a short list that looks exactly like a
// small module, and the caller would act on it.
func TestListingsReportReadFailure(t *testing.T) {
	tests := map[string]func(*Storage) (int, error){
		"ListOIDs": func(s *Storage) (int, error) {
			out, err := s.ListOIDs("")
			return len(out), err
		},
		"ListModules": func(s *Storage) (int, error) {
			out, err := s.ListModules()
			return len(out), err
		},
		"ListTables": func(s *Storage) (int, error) {
			out, err := s.ListTables("IF-MIB")
			return len(out), err
		},
		"ListModuleOIDs": func(s *Storage) (int, error) {
			out, err := s.ListModuleOIDs("SNMPv2-MIB")
			return len(out), err
		},
		"GetPollableOIDs": func(s *Storage) (int, error) {
			out, err := s.GetPollableOIDs("")
			return len(out), err
		},
		"GetTableColumns": func(s *Storage) (int, error) {
			out, err := s.GetTableColumns("1.3.6.1.2.1.2.2")
			return len(out), err
		},
	}

	for name, list := range tests {
		t.Run(name, func(t *testing.T) {
			store, failReads := faultyStore(t)
			failReads()

			count, err := list(store)
			injected(t, err)
			if count != 0 {
				t.Errorf("%s returned %d records alongside an error", name, count)
			}
		})
	}
}

// TestListModuleOIDsReportsIndexScanFailure checks that the failure is reported
// as an index error rather than as an ordinary empty module, which is what the
// caller would otherwise conclude.
func TestListModuleOIDsReportsIndexScanFailure(t *testing.T) {
	store, failReads := faultyStore(t)
	failReads()

	_, err := store.ListModuleOIDs("SNMPv2-MIB")

	var indexErr *IndexError
	if !errors.As(err, &indexErr) {
		t.Fatalf("got %T, want *IndexError", err)
	}
	if indexErr.Module != "SNMPv2-MIB" {
		t.Errorf("Module = %q, want %q", indexErr.Module, "SNMPv2-MIB")
	}
	if indexErr.Op != "scan" {
		t.Errorf("Op = %q, want %q", indexErr.Op, "scan")
	}
}

// TestStoreStillWorksOnAHealthyFilesystem is the control: with the injector
// disarmed the in-memory store behaves exactly like the on-disk one, so every
// failure above is attributable to the injected fault and nothing else.
func TestStoreStillWorksOnAHealthyFilesystem(t *testing.T) {
	store, _ := faultyStore(t)

	meta, err := store.GetOID("1.3.6.1.2.1.1.1.0")
	if err != nil {
		t.Fatalf("GetOID: %v", err)
	}
	if meta.Name != "sysDescr" {
		t.Errorf("Name = %q, want %q", meta.Name, "sysDescr")
	}

	oids, err := store.ListModuleOIDs("SNMPv2-MIB")
	if err != nil {
		t.Fatalf("ListModuleOIDs: %v", err)
	}
	if len(oids) != 1 {
		t.Errorf("ListModuleOIDs returned %d objects, want 1", len(oids))
	}

	counted, modules, tables, err := store.Count()
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if counted != 1 || modules != 1 || tables != 1 {
		t.Errorf("Count() = %d OIDs, %d modules, %d tables; want 1, 1, 1", counted, modules, tables)
	}
}
