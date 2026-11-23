package loader

import (
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/testsupport"
)

// TestLoaderIgnoresSMIPATH checks that the loader searches exactly its
// configured paths. A module that only the host's SMIPATH holds must not load:
// otherwise what the daemon monitors depends on the environment it was
// started from, not on its configuration.
func TestLoaderIgnoresSMIPATH(t *testing.T) {
	t.Setenv("SMIPATH", testsupport.MIBDir("standard"))

	mibLoader, err := New(&config.MIBConfig{
		Paths:       []string{t.TempDir()},
		StoragePath: filepath.Join(t.TempDir(), "mib-storage"),
	})
	if err != nil {
		t.Fatalf("create loader: %v", err)
	}
	t.Cleanup(func() { _ = mibLoader.Close() })

	if err := mibLoader.LoadModule("SNMPv2-MIB"); err == nil {
		t.Error("SNMPv2-MIB loaded from SMIPATH, which is not among the configured paths")
	}
}
