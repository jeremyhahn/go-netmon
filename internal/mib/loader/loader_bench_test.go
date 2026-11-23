package loader

import (
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/testsupport"
)

// EnsureModule runs once per assigned module every time a device's worker
// starts, and a device can carry seventeen of them, so the cost of the call
// that finds the module already loaded is what decides whether on-demand
// loading is affordable. These benchmarks separate the two paths: the lookup
// that finds the module present, and the parse that does not.

func BenchmarkEnsureModulePresent(b *testing.B) {
	l := benchLoader(b)
	if err := l.EnsureModule("IF-MIB"); err != nil {
		b.Fatalf("prime the store: %v", err)
	}

	b.ResetTimer()
	for b.Loop() {
		if err := l.EnsureModule("IF-MIB"); err != nil {
			b.Fatalf("ensure: %v", err)
		}
	}
}

func BenchmarkLoadModule(b *testing.B) {
	l := benchLoader(b)

	b.ResetTimer()
	for b.Loop() {
		if err := l.LoadModule("IF-MIB"); err != nil {
			b.Fatalf("load: %v", err)
		}
	}
}

// benchLoader opens a loader over a temporary store.
func benchLoader(b *testing.B) *Loader {
	b.Helper()

	l, err := New(&config.MIBConfig{
		Paths:       testsupport.MIBDirs("standard"),
		StoragePath: filepath.Join(b.TempDir(), "mib-storage"),
	})
	if err != nil {
		b.Fatalf("open loader: %v", err)
	}
	b.Cleanup(func() { _ = l.Close() })

	return l
}
