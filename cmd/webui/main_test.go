package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/config"
)

// The web UI's parsed MIB catalog is a Pebble store, and where it lives decides
// whether the operator's imported MIBs survive a redeploy. It used to be a
// literal buried in the "no config file found" branch, which put it on the
// container's writable layer: a catalog of 169 modules was destroyed twice in
// one session by an ordinary `docker compose build`. These tests pin the two
// properties that keep that from recurring — the path is whatever the operator
// passed, and it is never inherited from the poller's config file.

func TestConfigureMIBUsesTheCatalogPathItWasGiven(t *testing.T) {
	mibCfg := &config.MIBConfig{}

	configureMIB(mibCfg, "/app/data/webui/mib.db", "/app/data/mibs")

	if mibCfg.StoragePath != "/app/data/webui/mib.db" {
		t.Errorf("catalog is at %q, want the path that was passed", mibCfg.StoragePath)
	}
}

// TestConfigureMIBOverridesTheConfigFilesStoragePath is the important one. A
// config file mounted into the web UI container is the *poller's*, and its
// mib.storage_path names the poller's own catalog. Pebble admits one writer, so
// adopting it means either failing to start or locking the poller out of its
// own store.
func TestConfigureMIBOverridesTheConfigFilesStoragePath(t *testing.T) {
	mibCfg := &config.MIBConfig{StoragePath: "/app/data/mib-storage"}

	configureMIB(mibCfg, "/app/data/webui/mib.db", "/app/data/mibs")

	if mibCfg.StoragePath == "/app/data/mib-storage" {
		t.Error("the web UI adopted the poller's catalog path; Pebble admits only one writer")
	}
}

func TestConfigureMIBAddsTheSearchPaths(t *testing.T) {
	mibCfg := &config.MIBConfig{}

	configureMIB(mibCfg, "data/mib.db", "/app/data/mibs")

	// The uploaded directory is the one that matters: it is the shared volume
	// through which a MIB imported in the web UI reaches the poller, and since
	// imports are copied into it, it is the whole source.
	for _, want := range []string{
		"/app/data/mibs/uploaded",
		"/usr/share/snmp/mibs",
	} {
		if !slices.Contains(mibCfg.Paths, want) {
			t.Errorf("search paths %v are missing %q", mibCfg.Paths, want)
		}
	}

	// Nothing under /app/mibs may be added back. That directory was a bind
	// mount of the repository working copy: a module resolved from there looked
	// imported while living in a place no backup covered and no other host had,
	// which is precisely the coupling removing the mount was meant to end.
	for _, path := range mibCfg.Paths {
		if strings.HasPrefix(path, "/app/mibs") {
			t.Errorf("search path %q reintroduces the bind-mounted repository tree", path)
		}
	}
}

// TestConfigureMIBKeepsConfiguredPathsAndDoesNotRepeatThem covers the input the
// deployment actually produces: a config file that already names some of the
// directories. A repeated directory is not fatal, but it is handed to gosmi as
// a search path and searched twice for every module that misses.
func TestConfigureMIBKeepsConfiguredPathsAndDoesNotRepeatThem(t *testing.T) {
	mibCfg := &config.MIBConfig{
		Paths: []string{"/etc/mibs/site", "/app/mibs/cisco", "/app/mibs/cisco"},
	}

	configureMIB(mibCfg, "data/mib.db", "/app/data/mibs")

	if !slices.Contains(mibCfg.Paths, "/etc/mibs/site") {
		t.Errorf("search paths %v dropped a directory the config file named", mibCfg.Paths)
	}

	seen := make(map[string]int, len(mibCfg.Paths))
	for _, path := range mibCfg.Paths {
		seen[path]++
	}
	// The duplicate the config file itself carries is left alone; what must not
	// happen is this function adding a third copy.
	if seen["/app/mibs/cisco"] > 2 {
		t.Errorf("search paths %v repeat /app/mibs/cisco %d times", mibCfg.Paths, seen["/app/mibs/cisco"])
	}
}
