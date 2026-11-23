package loader

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/testsupport"
)

// These tests are about the cost of a RESTART, so they are written as restarts:
// a loader is opened over a store path, closed, and another opened over the
// same path. Closing calls gosmi.Exit, which discards every parsed module, so
// the second loader starts with the same empty parser cache a fresh process
// would have. Only the Pebble store carries over — which is exactly the
// asymmetry the fix exploits.

// editedFixtureMIB is fixtureMIB with a second scalar, so a test can tell which
// version of the file the store holds.
const editedFixtureMIB = `FIXTURE-MIB DEFINITIONS ::= BEGIN

IMPORTS
    MODULE-IDENTITY, OBJECT-TYPE, Integer32, enterprises
        FROM SNMPv2-SMI;

fixtureMIB MODULE-IDENTITY
    LAST-UPDATED "202601020000Z"
    ORGANIZATION "go-netmon tests"
    CONTACT-INFO  "none"
    DESCRIPTION   "A minimal module used by the loader tests, edited."
    ::= { enterprises 999999 }

fixtureScalar OBJECT-TYPE
    SYNTAX      Integer32
    MAX-ACCESS  read-only
    STATUS      current
    DESCRIPTION "A single readable scalar."
    ::= { fixtureMIB 1 }

fixtureSecondScalar OBJECT-TYPE
    SYNTAX      Integer32
    MAX-ACCESS  read-only
    STATUS      current
    DESCRIPTION "Added by an edit to the file on disk."
    ::= { fixtureMIB 2 }

END
`

// restartLoader opens a loader over an explicit store path, so a test can open
// a second one over the same store and get what a restarted process gets.
func restartLoader(t *testing.T, storePath string, autoLoad []string, extraPaths ...string) (*Loader, func()) {
	t.Helper()

	l, err := New(&config.MIBConfig{
		Paths:       append(testsupport.MIBDirs("standard"), extraPaths...),
		StoragePath: storePath,
		AutoLoad:    autoLoad,
	})
	if err != nil {
		t.Fatalf("open loader: %v", err)
	}

	var once sync.Once
	closeLoader := func() { once.Do(func() { _ = l.Close() }) }
	t.Cleanup(closeLoader)

	return l, closeLoader
}

// TestRestartDoesNotReparseAnUnchangedCatalog is the defect in #58 stated as a
// test: the second start must not repeat the first start's work.
func TestRestartDoesNotReparseAnUnchangedCatalog(t *testing.T) {
	store := filepath.Join(t.TempDir(), "mib-storage")
	mibs := t.TempDir()
	writeMIB(t, mibs, "FIXTURE-MIB", fixtureMIB)

	cold, closeCold := restartLoader(t, store, []string{"FIXTURE-MIB"}, mibs)
	if err := cold.Load(); err != nil {
		t.Fatalf("cold Load: %v", err)
	}
	if cold.Revision() == 0 {
		t.Fatal("the cold start parsed nothing")
	}
	closeCold()

	warm, closeWarm := restartLoader(t, store, []string{"FIXTURE-MIB"}, mibs)
	if err := warm.Load(); err != nil {
		t.Fatalf("warm Load: %v", err)
	}
	if got := warm.Revision(); got != 0 {
		t.Errorf("the warm start changed the store %d times, want 0 — it reparsed a catalog it already had", got)
	}
	if !warm.HasModule("FIXTURE-MIB") {
		t.Error("the warm start left the module out of the store")
	}
	closeWarm()
}

// A file named differently from the module it declares (iDRAC-SMIv2.mib
// declaring IDRAC-MIB-SMIv2) must load under the declared name, with its
// objects resolved, and a restart must find it cached.
func TestFileNameDifferingFromModuleNameLoadsAndStaysCached(t *testing.T) {
	store := filepath.Join(t.TempDir(), "mib-storage")
	mibs := t.TempDir()
	writeMIB(t, mibs, "renamed-fixture-file", fixtureMIB)

	cold, closeCold := restartLoader(t, store, []string{"renamed-fixture-file"}, mibs)
	if err := cold.Load(); err != nil {
		t.Fatalf("cold Load: %v", err)
	}
	if cold.Revision() == 0 {
		t.Fatal("the cold start parsed nothing")
	}
	if !cold.HasModule("FIXTURE-MIB") {
		t.Fatal("the module is not stored under the name it declares")
	}
	oids, err := storeOf(t, cold).ListModuleOIDs("FIXTURE-MIB")
	if err != nil || len(oids) == 0 {
		t.Fatalf("objects of the declared module not resolved: %d, %v", len(oids), err)
	}
	closeCold()

	warm, closeWarm := restartLoader(t, store, []string{"renamed-fixture-file"}, mibs)
	if err := warm.Load(); err != nil {
		t.Fatalf("warm Load: %v", err)
	}
	if got := warm.Revision(); got != 0 {
		t.Errorf("the warm start changed the store %d times, want 0 — it reparsed a cached module", got)
	}
	closeWarm()
}

// The other half of the same decision: skipping unchanged modules must not cost
// the operator an edited one. Editing a MIB file on disk is how a fix reaches a
// running appliance, and it took effect on the next restart before this change.
func TestRestartReparsesAnEditedFile(t *testing.T) {
	store := filepath.Join(t.TempDir(), "mib-storage")
	mibs := t.TempDir()
	writeMIB(t, mibs, "FIXTURE-MIB", fixtureMIB)

	first, closeFirst := restartLoader(t, store, []string{"FIXTURE-MIB"}, mibs)
	if err := first.Load(); err != nil {
		t.Fatalf("first Load: %v", err)
	}
	if _, err := storeOf(t, first).GetOIDByName("FIXTURE-MIB", "fixtureSecondScalar"); err == nil {
		t.Fatal("the unedited module already defines fixtureSecondScalar")
	}
	closeFirst()

	// A file whose mtime is at or before the parse is indistinguishable from
	// one that was never touched, and the parse just happened. Waiting is what
	// makes the edit an edit rather than a race with the clock.
	time.Sleep(20 * time.Millisecond)
	writeMIB(t, mibs, "FIXTURE-MIB", editedFixtureMIB)

	second, closeSecond := restartLoader(t, store, []string{"FIXTURE-MIB"}, mibs)
	if err := second.Load(); err != nil {
		t.Fatalf("second Load: %v", err)
	}
	if second.Revision() == 0 {
		t.Fatal("an edited MIB file was not reparsed on restart")
	}
	if _, err := storeOf(t, second).GetOIDByName("FIXTURE-MIB", "fixtureSecondScalar"); err != nil {
		t.Errorf("the edit did not reach the store: %v", err)
	}
	closeSecond()
}

// A module whose file has been removed keeps the copy the store holds. Parsing
// it could only fail, and failing would leave the store as it is anyway — so
// the alternative is a warning per module on every start and no change in what
// is served.
func TestEnsureModuleCurrentKeepsAStoredModuleWhoseFileIsGone(t *testing.T) {
	store := filepath.Join(t.TempDir(), "mib-storage")
	mibs := t.TempDir()
	writeMIB(t, mibs, "FIXTURE-MIB", fixtureMIB)

	first, closeFirst := restartLoader(t, store, []string{"FIXTURE-MIB"}, mibs)
	if err := first.Load(); err != nil {
		t.Fatalf("first Load: %v", err)
	}
	closeFirst()

	if err := os.Remove(filepath.Join(mibs, "FIXTURE-MIB.txt")); err != nil {
		t.Fatalf("remove the MIB file: %v", err)
	}

	second, closeSecond := restartLoader(t, store, nil, mibs)
	if err := second.EnsureModuleCurrent("FIXTURE-MIB"); err != nil {
		t.Errorf("EnsureModuleCurrent on a module whose file is gone: %v", err)
	}
	if !second.HasModule("FIXTURE-MIB") {
		t.Error("the stored module was dropped because its file is gone")
	}
	if got := second.Revision(); got != 0 {
		t.Errorf("store changed %d times, want 0", got)
	}
	closeSecond()
}

// A module the store has never held is parsed, whatever its file's timestamp.
func TestEnsureModuleCurrentLoadsAModuleTheStoreLacks(t *testing.T) {
	mibs := t.TempDir()
	writeMIB(t, mibs, "FIXTURE-MIB", fixtureMIB)

	l, _ := restartLoader(t, filepath.Join(t.TempDir(), "mib-storage"), nil, mibs)

	if err := l.EnsureModuleCurrent("FIXTURE-MIB"); err != nil {
		t.Fatalf("EnsureModuleCurrent: %v", err)
	}
	if !l.HasModule("FIXTURE-MIB") {
		t.Error("the module was not stored")
	}
	if got := l.Revision(); got != 1 {
		t.Errorf("store changed %d times, want 1", got)
	}
}

// The bad-input case: a module no search path defines is reported, and nothing
// is written.
func TestEnsureModuleCurrentReportsAModuleThatCannotBeParsed(t *testing.T) {
	l, _ := restartLoader(t, filepath.Join(t.TempDir(), "mib-storage"), nil)

	err := l.EnsureModuleCurrent("NO-SUCH-MIB")
	if err == nil {
		t.Fatal("EnsureModuleCurrent accepted a module that does not exist")
	}
	var loadErr *ModuleLoadError
	if !errors.As(err, &loadErr) {
		t.Fatalf("error = %T (%v), want *ModuleLoadError", err, err)
	}
	if loadErr.Module != "NO-SUCH-MIB" {
		t.Errorf("ModuleLoadError.Module = %q, want NO-SUCH-MIB", loadErr.Module)
	}
	if got := l.Revision(); got != 0 {
		t.Errorf("store changed %d times after a failed parse, want 0", got)
	}
}

// moduleFileIsNewer answers on the stored metadata alone, so its edges are
// worth stating directly rather than only through a restart.
func TestModuleFileIsNewer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "FIXTURE-MIB.txt")
	if err := os.WriteFile(path, []byte(fixtureMIB), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	for name, tc := range map[string]struct {
		path     string
		loadedAt time.Time
		want     bool
	}{
		"parsed before the file was written": {path, info.ModTime().Add(-time.Second), true},
		"parsed after the file was written":  {path, info.ModTime().Add(time.Second), false},
		"parsed at the same instant":         {path, info.ModTime(), false},
		"no recorded path":                   {"", time.Now(), false},
		"file no longer there":               {filepath.Join(dir, "gone.txt"), time.Time{}, false},
	} {
		t.Run(name, func(t *testing.T) {
			got := moduleFileIsNewer(&mib.ModuleInfo{Path: tc.path, LoadedAt: tc.loadedAt})
			if got != tc.want {
				t.Errorf("moduleFileIsNewer = %v, want %v", got, tc.want)
			}
		})
	}
}
