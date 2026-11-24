package loader

import (
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/testsupport"
)

// TestLoaderIgnoresSMIPATH checks the loader searches only its configured paths.
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
