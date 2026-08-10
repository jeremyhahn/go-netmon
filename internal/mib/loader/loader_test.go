package loader

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/mib/storage"
	"github.com/jeremyhahn/go-netmon/internal/testsupport"
)

// The loader is the only thing that turns a MIB file into something pollable,
// and it is shared by the poller and the web UI. Two properties matter most and
// are what these tests are built around:
//
//   - a module already in the store is not parsed again, because a device can
//     carry seventeen of them and each parse costs tens of milliseconds; and
//   - a module that fails to parse costs nothing that was already stored.
//
// gosmi, which the parser wraps, keeps its loaded modules in package-global
// state. Only one loader may therefore be open at a time in this binary, so
// every test closes its loader before opening another.

// fixtureMIB is a minimal but genuinely well-formed module. It is written to a
// temporary directory so a test can corrupt it afterwards, which is not
// something that can be done to the repository's own MIB files.
const fixtureMIB = `FIXTURE-MIB DEFINITIONS ::= BEGIN

IMPORTS
    MODULE-IDENTITY, OBJECT-TYPE, Integer32, enterprises
        FROM SNMPv2-SMI;

fixtureMIB MODULE-IDENTITY
    LAST-UPDATED "202601010000Z"
    ORGANIZATION "go-netmon tests"
    CONTACT-INFO  "none"
    DESCRIPTION   "A minimal module used by the loader tests."
    ::= { enterprises 999999 }

fixtureScalar OBJECT-TYPE
    SYNTAX      Integer32
    MAX-ACCESS  read-only
    STATUS      current
    DESCRIPTION "A single readable scalar."
    ::= { fixtureMIB 1 }

END
`

// newLoader opens a loader over a fresh store, searching the repository's
// standard MIB directory plus any extra directories given.
//
// The store is in the test's temporary directory, so nothing outside it is read
// or written. The returned close function is idempotent, so a test that has to
// release gosmi early can call it and still be cleaned up.
func newLoader(t *testing.T, autoLoad []string, extraPaths ...string) (*Loader, func()) {
	t.Helper()

	paths := append(testsupport.MIBDirs("standard"), extraPaths...)
	l, err := New(&config.MIBConfig{
		Paths:       paths,
		StoragePath: filepath.Join(t.TempDir(), "mib-storage"),
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

// writeMIB writes content to dir under the file name gosmi searches for.
func writeMIB(t *testing.T, dir, module, content string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(dir, module+".txt"), []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", module, err)
	}
}

// storeOf returns the loader's MIB metadata store.
func storeOf(t *testing.T, l *Loader) *storage.Storage {
	t.Helper()

	store, ok := l.GetStorage().(*storage.Storage)
	if !ok || store == nil {
		t.Fatal("loader returned no usable storage")
	}
	return store
}

func TestNewOpensTheStoreAndTheParser(t *testing.T) {
	l, _ := newLoader(t, nil)

	if storeOf(t, l) == nil {
		t.Error("no MIB store")
	}
	if l.GetParser() == nil {
		t.Error("no parser")
	}
	if l.GetProfileManager() == nil {
		t.Error("no profile manager")
	}
}

// TestNewRejectsAnUnusableStorePath is the bad-input case for construction: the
// store path names something that cannot hold a database.
func TestNewRejectsAnUnusableStorePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, []byte("occupied"), 0o600); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}

	l, err := New(&config.MIBConfig{Paths: testsupport.MIBDirs("standard"), StoragePath: path})
	if err == nil {
		_ = l.Close()
		t.Fatal("opening a store over a regular file succeeded")
	}

	var openErr *StoreOpenError
	if !errors.As(err, &openErr) {
		t.Fatalf("error is %T (%v), want *StoreOpenError", err, err)
	}
	if openErr.Path != path {
		t.Errorf("error names path %q, want %q", openErr.Path, path)
	}
}

func TestLoadLoadsTheAutoLoadModules(t *testing.T) {
	l, _ := newLoader(t, []string{"SNMPv2-MIB", "IF-MIB"})

	if err := l.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	loaded, err := l.ListLoadedModules()
	if err != nil {
		t.Fatalf("list loaded modules: %v", err)
	}
	if len(loaded) != 2 {
		t.Errorf("store holds %v, want the two auto-loaded modules", loaded)
	}
	if len(l.GetProfileManager().ListProfiles()) == 0 {
		t.Error("no built-in profiles were loaded")
	}
}

// TestLoadContinuesPastAModuleThatWillNotLoad states that the auto-load list is
// best-effort: one unusable name in config.yaml must not leave the poller with
// no MIB metadata at all.
func TestLoadContinuesPastAModuleThatWillNotLoad(t *testing.T) {
	l, _ := newLoader(t, []string{"NO-SUCH-MIB", "SNMPv2-MIB"})

	if err := l.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	if !l.HasModule("SNMPv2-MIB") {
		t.Error("SNMPv2-MIB was skipped because an earlier module in the list failed")
	}
	if l.HasModule("NO-SUCH-MIB") {
		t.Error("a module that does not exist was recorded as loaded")
	}
}

func TestHasModuleReportsWhatTheStoreHolds(t *testing.T) {
	l, _ := newLoader(t, nil)

	if l.HasModule("SNMPv2-MIB") {
		t.Error("an empty store reports SNMPv2-MIB as present")
	}
	if err := l.LoadModule("SNMPv2-MIB"); err != nil {
		t.Fatalf("load SNMPv2-MIB: %v", err)
	}
	if !l.HasModule("SNMPv2-MIB") {
		t.Error("a loaded module reports as absent")
	}
}

func TestLoadModuleStoresTheModulesObjects(t *testing.T) {
	l, _ := newLoader(t, nil)

	if err := l.LoadModule("IF-MIB"); err != nil {
		t.Fatalf("load IF-MIB: %v", err)
	}

	objects, err := storeOf(t, l).ListModuleOIDs("IF-MIB")
	if err != nil {
		t.Fatalf("list IF-MIB objects: %v", err)
	}
	if len(objects) == 0 {
		t.Fatal("IF-MIB was recorded as loaded but stored no objects")
	}

	stats, err := l.GetModuleStatistics("IF-MIB")
	if err != nil {
		t.Fatalf("module statistics: %v", err)
	}
	if stats.PollableOIDs == 0 || stats.Tables == 0 {
		t.Errorf("IF-MIB reports %d pollable objects and %d tables; it has both",
			stats.PollableOIDs, stats.Tables)
	}
}

// TestLoadModuleRejectsAnUnknownModule is the bad-input case: a name with no
// file behind it must be reported, not silently recorded as loaded.
func TestLoadModuleRejectsAnUnknownModule(t *testing.T) {
	l, _ := newLoader(t, nil)

	err := l.LoadModule("NO-SUCH-MIB")
	if err == nil {
		t.Fatal("loading a module that does not exist succeeded")
	}

	var loadErr *ModuleLoadError
	if !errors.As(err, &loadErr) {
		t.Fatalf("error is %T (%v), want *ModuleLoadError", err, err)
	}
	if loadErr.Module != "NO-SUCH-MIB" {
		t.Errorf("error names module %q, want NO-SUCH-MIB", loadErr.Module)
	}
	if l.HasModule("NO-SUCH-MIB") {
		t.Error("the failed load left a module record behind")
	}
}

// TestLoadModuleReloadsAModuleInPlace covers the refresh path a MIB re-import
// takes: the objects are replaced, not accumulated.
func TestLoadModuleReloadsAModuleInPlace(t *testing.T) {
	l, _ := newLoader(t, nil)

	if err := l.LoadModule("TCP-MIB"); err != nil {
		t.Fatalf("load TCP-MIB: %v", err)
	}
	first, err := l.GetModuleStatistics("TCP-MIB")
	if err != nil {
		t.Fatalf("module statistics: %v", err)
	}

	if reloadErr := l.LoadModule("TCP-MIB"); reloadErr != nil {
		t.Fatalf("reload TCP-MIB: %v", reloadErr)
	}
	second, err := l.GetModuleStatistics("TCP-MIB")
	if err != nil {
		t.Fatalf("module statistics: %v", err)
	}

	if second.LoadedAt.Before(first.LoadedAt) {
		t.Errorf("reload recorded an earlier timestamp (%s, was %s)", second.LoadedAt, first.LoadedAt)
	}
	if second.PollableOIDs != first.PollableOIDs {
		t.Errorf("reload changed the pollable object count from %d to %d; the module did not change",
			first.PollableOIDs, second.PollableOIDs)
	}
}

// TestLoadModuleKeepsTheStoredCopyWhenTheParseFails is a regression test.
//
// The loader used to delete a module's stored objects *before* parsing the new
// copy, so an attempt to refresh a module whose file had become unreadable
// erased the good copy the operator already had — the module simply vanished
// from the store and every device polling it silently stopped. Parsing first
// degrades that to "nothing changed".
func TestLoadModuleKeepsTheStoredCopyWhenTheParseFails(t *testing.T) {
	mibDir := t.TempDir()
	writeMIB(t, mibDir, "FIXTURE-MIB", fixtureMIB)

	storePath := filepath.Join(t.TempDir(), "mib-storage")
	good, err := New(&config.MIBConfig{
		Paths:       append(testsupport.MIBDirs("standard"), mibDir),
		StoragePath: storePath,
	})
	if err != nil {
		t.Fatalf("open loader: %v", err)
	}
	if loadErr := good.LoadModule("FIXTURE-MIB"); loadErr != nil {
		t.Fatalf("load the intact module: %v", loadErr)
	}
	objects, err := storeOf(t, good).ListModuleOIDs("FIXTURE-MIB")
	if err != nil {
		t.Fatalf("list stored objects: %v", err)
	}
	// Both the store's lock and gosmi's global state have to be released before
	// the second loader can open over the same store.
	if closeErr := good.Close(); closeErr != nil {
		t.Fatalf("close loader: %v", closeErr)
	}

	// The file becomes unreadable between the two runs: a truncated download, a
	// bad edit, a half-written upload.
	writeMIB(t, mibDir, "FIXTURE-MIB", "FIXTURE-MIB DEFINITIONS ::= BEGIN\ngarbage\n")

	broken, err := New(&config.MIBConfig{
		Paths:       append(testsupport.MIBDirs("standard"), mibDir),
		StoragePath: storePath,
	})
	if err != nil {
		t.Fatalf("reopen loader: %v", err)
	}
	t.Cleanup(func() { _ = broken.Close() })

	if reloadErr := broken.LoadModule("FIXTURE-MIB"); reloadErr == nil {
		t.Fatal("parsing a corrupted module succeeded")
	}

	if !broken.HasModule("FIXTURE-MIB") {
		t.Fatal("the failed reload erased the module that was already stored")
	}
	survived, err := storeOf(t, broken).ListModuleOIDs("FIXTURE-MIB")
	if err != nil {
		t.Fatalf("list surviving objects: %v", err)
	}
	if len(survived) != len(objects) {
		t.Errorf("the store holds %d objects after the failed reload, held %d before",
			len(survived), len(objects))
	}
}

func TestEnsureModuleLoadsAnAbsentModule(t *testing.T) {
	l, _ := newLoader(t, nil)

	if err := l.EnsureModule("UDP-MIB"); err != nil {
		t.Fatalf("ensure UDP-MIB: %v", err)
	}
	if !l.HasModule("UDP-MIB") {
		t.Error("UDP-MIB was not loaded")
	}
}

// TestEnsureModuleLeavesAPresentModuleAlone is the property that keeps
// on-demand loading affordable: it runs on every worker start, so a module
// already in the store must cost a lookup rather than a parse.
func TestEnsureModuleLeavesAPresentModuleAlone(t *testing.T) {
	l, _ := newLoader(t, nil)

	if err := l.EnsureModule("SNMPv2-MIB"); err != nil {
		t.Fatalf("ensure SNMPv2-MIB: %v", err)
	}
	first, err := l.GetModuleStatistics("SNMPv2-MIB")
	if err != nil {
		t.Fatalf("module statistics: %v", err)
	}

	if ensureErr := l.EnsureModule("SNMPv2-MIB"); ensureErr != nil {
		t.Fatalf("ensure SNMPv2-MIB again: %v", ensureErr)
	}
	second, err := l.GetModuleStatistics("SNMPv2-MIB")
	if err != nil {
		t.Fatalf("module statistics: %v", err)
	}

	if !second.LoadedAt.Equal(first.LoadedAt) {
		t.Errorf("the module was parsed again (%s, was %s)", second.LoadedAt, first.LoadedAt)
	}
}

// TestEnsureModuleRejectsAnUnknownModule is the bad-input case, and it also
// pins that a failed load is not cached: the caller is told every time, so a
// module that becomes available later is picked up.
func TestEnsureModuleRejectsAnUnknownModule(t *testing.T) {
	l, _ := newLoader(t, nil)

	for attempt := 1; attempt <= 2; attempt++ {
		err := l.EnsureModule("NO-SUCH-MIB")
		var loadErr *ModuleLoadError
		if !errors.As(err, &loadErr) {
			t.Fatalf("attempt %d: error is %T (%v), want *ModuleLoadError", attempt, err, err)
		}
	}
}

// TestEnsureModuleParsesASharedModuleOnce covers the case the poller creates:
// several device workers reaching the same missing module at the same moment.
//
// Equal load timestamps show the module was parsed once. Under -race this is
// also what catches an unsynchronized parse, because gosmi's loaded modules are
// package-global state that two concurrent parses corrupt.
func TestEnsureModuleParsesASharedModuleOnce(t *testing.T) {
	l, _ := newLoader(t, nil)

	const callers = 8
	seen := make([]string, callers)

	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()

			if err := l.EnsureModule("IP-MIB"); err != nil {
				seen[i] = "failed: " + err.Error()
				return
			}
			stats, err := l.GetModuleStatistics("IP-MIB")
			if err != nil {
				seen[i] = "missing: " + err.Error()
				return
			}
			seen[i] = stats.LoadedAt.String()
		}()
	}
	wg.Wait()

	for i, stamp := range seen {
		if stamp != seen[0] {
			t.Errorf("caller %d saw IP-MIB loaded at %s, caller 0 saw %s — it was parsed more than once",
				i, stamp, seen[0])
		}
	}
}

// TestGetModuleStatisticsRejectsAnAbsentModule is the bad-input case for the
// statistics call.
func TestGetModuleStatisticsRejectsAnAbsentModule(t *testing.T) {
	l, _ := newLoader(t, nil)

	stats, err := l.GetModuleStatistics("NO-SUCH-MIB")
	if stats != nil {
		t.Errorf("statistics returned for a module the store does not hold: %+v", stats)
	}

	var notFound *ModuleNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("error is %T (%v), want *ModuleNotFoundError", err, err)
	}
	if notFound.Module != "NO-SUCH-MIB" {
		t.Errorf("error names module %q, want NO-SUCH-MIB", notFound.Module)
	}
}

func TestListLoadedModulesReportsAnEmptyStore(t *testing.T) {
	l, _ := newLoader(t, nil)

	loaded, err := l.ListLoadedModules()
	if err != nil {
		t.Fatalf("list loaded modules: %v", err)
	}
	if len(loaded) != 0 {
		t.Errorf("a store with nothing loaded reports %v", loaded)
	}
}

// TestCloseIsIdempotent covers the shutdown path. Both commands defer a close
// and can also close explicitly on an error path, so closing twice has to be
// harmless rather than a second attempt to release a store that is gone.
func TestCloseIsIdempotent(t *testing.T) {
	l, closeLoader := newLoader(t, nil)
	defer closeLoader()

	if err := l.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Errorf("second close: %v", err)
	}
}
