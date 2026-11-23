package loader

import (
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/testsupport"
)

// The revision is how a consumer of the MIB store -- the metrics exporter's
// name and index metadata above all -- tells "nothing has been loaded since I
// last looked" from "seventeen modules have", without polling the store or
// being handed a notification per module. Everything it is used for depends on
// it moving when, and only when, the store's contents change.

// newRevisionLoader builds a loader over the repository's real MIB files with
// an empty store in the test's temporary directory.
func newRevisionLoader(t *testing.T) *Loader {
	t.Helper()

	mibLoader, err := New(&config.MIBConfig{
		Paths:       testsupport.MIBDirs("standard", "cisco"),
		StoragePath: filepath.Join(t.TempDir(), "mib-storage"),
	})
	if err != nil {
		t.Fatalf("create loader: %v", err)
	}
	// gosmi keeps its loaded modules in package state, so only one loader may
	// be open at a time in a test binary.
	t.Cleanup(func() { _ = mibLoader.Close() })

	return mibLoader
}

// TestRevisionAdvancesOnlyWhenTheStoreChanges is the contract in one test: a
// module that is parsed moves the revision, a module that was already present
// does not, and a module that will not parse does not either.
//
// The middle case is the one the coalescing depends on. Seventeen EnsureModule
// calls for a device whose modules are already loaded must leave the revision
// alone, or every device registration would rebuild the consumer's index for no
// new metadata.
func TestRevisionAdvancesOnlyWhenTheStoreChanges(t *testing.T) {
	mibLoader := newRevisionLoader(t)

	if got := mibLoader.Revision(); got != 0 {
		t.Fatalf("a loader that has loaded nothing reports revision %d, want 0", got)
	}

	if err := mibLoader.EnsureModule("IF-MIB"); err != nil {
		t.Fatalf("EnsureModule(IF-MIB): %v", err)
	}
	afterFirst := mibLoader.Revision()
	if afterFirst == 0 {
		t.Fatal("loading a module left the revision at 0")
	}

	if err := mibLoader.EnsureModule("IF-MIB"); err != nil {
		t.Fatalf("EnsureModule(IF-MIB) a second time: %v", err)
	}
	if got := mibLoader.Revision(); got != afterFirst {
		t.Errorf("re-ensuring an already-loaded module moved the revision from %d to %d",
			afterFirst, got)
	}

	if err := mibLoader.EnsureModule("NO-SUCH-MIB"); err == nil {
		t.Error("EnsureModule(NO-SUCH-MIB) returned nil error")
	}
	if got := mibLoader.Revision(); got != afterFirst {
		t.Errorf("a module that failed to load moved the revision from %d to %d",
			afterFirst, got)
	}
}

// TestLoadModuleAdvancesTheRevisionOnEveryReload covers the deliberate
// difference from EnsureModule. LoadModule replaces what the store holds even
// when the module is already there, so its objects may genuinely have changed
// and a consumer's derived state has to be rebuilt.
func TestLoadModuleAdvancesTheRevisionOnEveryReload(t *testing.T) {
	mibLoader := newRevisionLoader(t)

	if err := mibLoader.LoadModule("SNMPv2-MIB"); err != nil {
		t.Fatalf("LoadModule(SNMPv2-MIB): %v", err)
	}
	afterFirst := mibLoader.Revision()

	if err := mibLoader.LoadModule("SNMPv2-MIB"); err != nil {
		t.Fatalf("LoadModule(SNMPv2-MIB) again: %v", err)
	}
	if got := mibLoader.Revision(); got <= afterFirst {
		t.Errorf("reloading a module left the revision at %d, want more than %d", got, afterFirst)
	}
}

// TestRevisionCountsEveryAutoLoadedModule checks the bootstrap is accounted for
// too. A consumer built before Load() ran would otherwise see revision 0, match
// its own bookkeeping, and never index the auto-loaded modules at all.
func TestRevisionCountsEveryAutoLoadedModule(t *testing.T) {
	autoLoad := []string{"SNMPv2-MIB", "IF-MIB"}

	mibLoader, err := New(&config.MIBConfig{
		Paths:       testsupport.MIBDirs("standard", "cisco"),
		StoragePath: filepath.Join(t.TempDir(), "mib-storage"),
		AutoLoad:    autoLoad,
	})
	if err != nil {
		t.Fatalf("create loader: %v", err)
	}
	t.Cleanup(func() { _ = mibLoader.Close() })

	if err := mibLoader.Load(); err != nil {
		t.Fatalf("Load(): %v", err)
	}

	if got := mibLoader.Revision(); got < uint64(len(autoLoad)) {
		t.Errorf("after auto-loading %d modules the revision is %d, want at least %d",
			len(autoLoad), got, len(autoLoad))
	}
}
