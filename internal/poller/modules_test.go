package poller

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/mib/loader"
	"github.com/jeremyhahn/go-netmon/internal/mib/storage"
	"github.com/jeremyhahn/go-netmon/internal/testsupport"
)

// This file covers the step that makes an operator's MIB assignment take
// effect. The web UI and the poller keep separate MIB metadata stores, and the
// web UI tells the poller only the *names* of a device's modules, so a name
// whose objects the poller has never parsed selects nothing. That is not a
// theoretical gap: a live install had seventeen modules assigned to a router
// and polled 38 objects instead of 155, because its store held only the three
// modules named in config.yaml's mib.auto_load.
//
// selection_parity_test.go pins those two numbers against a synthetic catalog.
// The tests here reproduce them against the repository's real MIB files, which
// is what makes them evidence about this bug rather than about the fixture.

// coreRouterModules is the seventeen-module "Core Routers" profile, as assigned
// to core-01 and core-02 on the install where the shortfall was measured.
func coreRouterModules() []string {
	return []string{
		"CISCO-BGP4-MIB",
		"CISCO-ENVMON-MIB",
		"CISCO-IF-EXTENSION-MIB",
		"CISCO-MEMORY-POOL-MIB",
		"CISCO-PROCESS-MIB",
		"ENTITY-MIB",
		"HOST-RESOURCES-MIB",
		"IANAifType-MIB",
		"IF-INVERTED-STACK-MIB",
		"IF-MIB",
		"IP-MIB",
		"SNMPv2-CONF",
		"SNMPv2-MIB",
		"SNMPv2-SMI",
		"SNMPv2-TC",
		"TCP-MIB",
		"UDP-MIB",
	}
}

// autoLoadedModules is config.yaml's mib.auto_load: everything the poller's
// store holds before it has been told which devices exist.
func autoLoadedModules() []string {
	return []string{"SNMPv2-MIB", "IF-MIB", "CISCO-BGP4-MIB"}
}

// newHydrationLoader builds a MIB loader whose store starts out holding only
// the auto-loaded modules, over the repository's real MIB files. extraPaths are
// searched after the repository's own directories.
//
// The store lives in the test's temporary directory, so nothing on the host is
// written and each test starts from the same state.
func newHydrationLoader(t *testing.T, autoLoad []string, extraPaths ...string) *loader.Loader {
	t.Helper()

	paths := testsupport.MIBDirs("standard", "cisco", "asr1001", "vendor")
	paths = append(paths, extraPaths...)

	mibLoader, err := loader.New(&config.MIBConfig{
		Paths:       paths,
		StoragePath: filepath.Join(t.TempDir(), "mib-storage"),
		AutoLoad:    autoLoad,
	})
	if err != nil {
		t.Fatalf("create MIB loader: %v", err)
	}
	// gosmi holds its loaded modules in package state, so only one loader may
	// be open at a time in a test binary.
	t.Cleanup(func() { _ = mibLoader.Close() })

	if err := mibLoader.Load(); err != nil {
		t.Fatalf("load MIBs: %v", err)
	}

	return mibLoader
}

// storedSelection is what the poller would poll for a device given what its
// store holds right now. It is the same read and the same rule startWorker
// uses, so it answers the question the operator is really asking: how many
// objects is this device fetching?
func storedSelection(t *testing.T, mibLoader *loader.Loader, device *config.Device) []*mib.OIDMetadata {
	t.Helper()

	store, ok := mibLoader.GetStorage().(*storage.Storage)
	if !ok || store == nil {
		t.Fatal("MIB loader returned no usable storage")
	}

	var objects []*mib.OIDMetadata
	for _, module := range device.Modules {
		found, err := store.ListModuleOIDs(module)
		if err != nil {
			t.Fatalf("list OIDs of %s: %v", module, err)
		}
		objects = append(objects, found...)
	}

	return selectModuleOIDs(objects, device)
}

// newHydrationPoller builds a poller over mibLoader with no devices of its own.
func newHydrationPoller(t *testing.T, mibLoader *loader.Loader) *Poller {
	t.Helper()

	cfg := getTestConfig(t)
	cfg.Devices = nil

	p := New(cfg, mibLoader)
	t.Cleanup(func() { _ = p.Stop() })
	return p
}

// TestHydrateModulesLoadsTheModulesADeviceWasAssigned is the regression test
// for the live shortfall, measured end to end against the real MIB files: a
// device assigned the seventeen-module profile polls 38 objects while the store
// holds only the three auto-loaded modules, and 155 once the rest are parsed.
func TestHydrateModulesLoadsTheModulesADeviceWasAssigned(t *testing.T) {
	mibLoader := newHydrationLoader(t, autoLoadedModules())
	device := &config.Device{Name: "core-01", Modules: coreRouterModules()}

	before := storedSelection(t, mibLoader, device)
	if len(before) != 38 {
		t.Fatalf("with only %v in the store the device selects %d objects, want 38",
			autoLoadedModules(), len(before))
	}

	newHydrationPoller(t, mibLoader).hydrateModules(context.Background(), device)

	after := storedSelection(t, mibLoader, device)
	if len(after) != 155 {
		t.Errorf("after loading the assigned modules the device selects %d objects, want 155",
			len(after))
	}

	for _, module := range device.Modules {
		if !mibLoader.HasModule(module) {
			t.Errorf("module %s is assigned to the device but was not loaded", module)
		}
	}
}

// TestHydrateModulesSkipsAModuleThatCannotBeParsed states the degradation rule:
// one unreadable module costs that module and nothing else.
//
// Failing the device instead would take a router that polls sixteen of its
// seventeen modules perfectly well and stop it polling at all, which is a worse
// outcome than the one being fixed.
func TestHydrateModulesSkipsAModuleThatCannotBeParsed(t *testing.T) {
	brokenDir := t.TempDir()
	writeFixtureMIB(t, brokenDir, "BROKEN-MIB", "BROKEN-MIB DEFINITIONS ::= BEGIN\nnot a MIB at all\n")

	mibLoader := newHydrationLoader(t, nil, brokenDir)
	device := &config.Device{
		Name:    "device-with-a-broken-mib",
		Modules: []string{"SNMPv2-MIB", "BROKEN-MIB", "NO-SUCH-MIB", "IF-MIB"},
	}

	newHydrationPoller(t, mibLoader).hydrateModules(context.Background(), device)

	for _, module := range []string{"SNMPv2-MIB", "IF-MIB"} {
		if !mibLoader.HasModule(module) {
			t.Errorf("module %s was skipped along with the broken one", module)
		}
	}
	for _, module := range []string{"BROKEN-MIB", "NO-SUCH-MIB"} {
		if mibLoader.HasModule(module) {
			t.Errorf("module %s does not parse but was stored anyway", module)
		}
		failures := testutil.ToFloat64(moduleLoadFailures.WithLabelValues(device.Name, module))
		if failures != 1 {
			t.Errorf("gonetmon_mib_module_load_failures_total{device=%q,module=%q} = %v, want 1",
				device.Name, module, failures)
		}
	}

	// The point of skipping rather than failing: the device still polls.
	if got := len(storedSelection(t, mibLoader, device)); got == 0 {
		t.Error("the device selects nothing; one broken module stopped the other three")
	}
}

// TestHydrateModulesLeavesALoadedModuleAlone guards the cost of the fix. Every
// worker start would otherwise reparse every module the device carries, which
// on the seventeen-module profile is seconds of work to reach the state the
// store was already in.
func TestHydrateModulesLeavesALoadedModuleAlone(t *testing.T) {
	mibLoader := newHydrationLoader(t, []string{"SNMPv2-MIB"})
	device := &config.Device{Name: "core-01", Modules: []string{"SNMPv2-MIB"}}

	first, err := mibLoader.GetModuleStatistics("SNMPv2-MIB")
	if err != nil {
		t.Fatalf("read module statistics: %v", err)
	}

	newHydrationPoller(t, mibLoader).hydrateModules(context.Background(), device)

	second, err := mibLoader.GetModuleStatistics("SNMPv2-MIB")
	if err != nil {
		t.Fatalf("read module statistics: %v", err)
	}
	if !second.LoadedAt.Equal(first.LoadedAt) {
		t.Errorf("SNMPv2-MIB was reloaded (%s, was %s); a module already in the store must be left alone",
			second.LoadedAt, first.LoadedAt)
	}
}

// TestHydrateModulesStopsWhenTheCallerGivesUp checks the bad-input path: a
// canceled context means the device is not going to be registered, so parsing
// its modules is work nobody asked for.
func TestHydrateModulesStopsWhenTheCallerGivesUp(t *testing.T) {
	mibLoader := newHydrationLoader(t, nil)
	device := &config.Device{Name: "core-01", Modules: []string{"IF-MIB", "TCP-MIB"}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	newHydrationPoller(t, mibLoader).hydrateModules(ctx, device)

	for _, module := range device.Modules {
		if mibLoader.HasModule(module) {
			t.Errorf("module %s was parsed for a device the caller had already abandoned", module)
		}
	}
}

// TestHydrateModulesWithoutAMIBLoader covers the poller that has no MIB
// metadata at all: it polls a fixed set of system OIDs, and asking it to load
// modules must be a no-op rather than a panic.
func TestHydrateModulesWithoutAMIBLoader(t *testing.T) {
	cfg := getTestConfig(t)
	cfg.Devices = nil

	p := New(cfg, nil)
	defer func() { _ = p.Stop() }()

	p.hydrateModules(context.Background(), &config.Device{
		Name:    "no-mibs",
		Modules: []string{"IF-MIB"},
	})
}

// TestHydrateModulesLoadsASharedModuleOnce covers several workers arriving at
// the same missing module at once, which is what happens when a profile is
// applied to a fleet.
//
// The parser is a facade over gosmi, whose loaded modules are package-global
// state, so this is also where an unsynchronized load would corrupt that state;
// running the package under -race is what turns that into a failure. Equal load
// timestamps across the callers are what shows the module was parsed once
// rather than once per caller.
func TestHydrateModulesLoadsASharedModuleOnce(t *testing.T) {
	mibLoader := newHydrationLoader(t, nil)
	p := newHydrationPoller(t, mibLoader)

	const devices = 8
	modules := []string{"IF-MIB", "TCP-MIB", "UDP-MIB"}

	loadedAt := make([][]string, devices)
	var wg sync.WaitGroup
	for i := range devices {
		wg.Add(1)
		go func() {
			defer wg.Done()

			p.hydrateModules(context.Background(), &config.Device{
				Name:    "core-" + string(rune('a'+i)),
				Modules: modules,
			})

			for _, module := range modules {
				stats, err := mibLoader.GetModuleStatistics(module)
				if err != nil {
					loadedAt[i] = append(loadedAt[i], "missing: "+err.Error())
					continue
				}
				loadedAt[i] = append(loadedAt[i], stats.LoadedAt.String())
			}
		}()
	}
	wg.Wait()

	for i, seen := range loadedAt {
		for j, stamp := range seen {
			if stamp != loadedAt[0][j] {
				t.Errorf("caller %d saw %s loaded at %s; caller 0 saw %s — it was parsed more than once",
					i, modules[j], stamp, loadedAt[0][j])
			}
		}
	}
}

// TestAddDeviceLoadsTheDevicesModules is the same fix seen from the API that
// the web UI actually calls: registering a device is what has to bring in the
// MIBs it was assigned, because that is the only notice the poller gets.
func TestAddDeviceLoadsTheDevicesModules(t *testing.T) {
	mibLoader := newHydrationLoader(t, []string{"SNMPv2-MIB"})
	p := newHydrationPoller(t, mibLoader)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := p.Start(ctx); err != nil {
		t.Fatalf("start poller: %v", err)
	}

	device := &config.Device{
		Name:        "core-01",
		Address:     unreachableSNMPAddress(t),
		SNMPVersion: "2c",
		Community:   "public",
		Enabled:     true,
		Modules:     []string{"SNMPv2-MIB", "IF-MIB"},
	}
	if err := p.AddDevice(ctx, device); err != nil {
		t.Fatalf("add device: %v", err)
	}

	p.mu.RLock()
	w := p.workers[device.Name]
	p.mu.RUnlock()
	if w == nil {
		t.Fatal("no worker was started for the device")
	}

	byModule := make(map[string]int)
	for _, oid := range w.oids {
		byModule[oid.Module]++
	}
	if byModule["IF-MIB"] == 0 {
		t.Errorf("the worker polls %v; IF-MIB was assigned but contributes nothing", byModule)
	}
}

// TestAddDeviceKeepsTheDeviceWhenAModuleWillNotLoad pins the same degradation
// rule at the registration boundary: the web UI's PUT must not fail because one
// of the device's MIBs is unparseable.
func TestAddDeviceKeepsTheDeviceWhenAModuleWillNotLoad(t *testing.T) {
	brokenDir := t.TempDir()
	writeFixtureMIB(t, brokenDir, "ALSO-BROKEN-MIB", "ALSO-BROKEN-MIB DEFINITIONS ::= BEGIN\n???\n")

	mibLoader := newHydrationLoader(t, []string{"SNMPv2-MIB"}, brokenDir)
	p := newHydrationPoller(t, mibLoader)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := p.Start(ctx); err != nil {
		t.Fatalf("start poller: %v", err)
	}

	device := &config.Device{
		Name:        "core-02",
		Address:     unreachableSNMPAddress(t),
		SNMPVersion: "2c",
		Community:   "public",
		Enabled:     true,
		Modules:     []string{"SNMPv2-MIB", "ALSO-BROKEN-MIB"},
	}
	if err := p.AddDevice(ctx, device); err != nil {
		t.Fatalf("add device with an unparseable module: %v", err)
	}

	if _, running := p.GetWorkerStatus()[device.Name]; !running {
		t.Error("the device was dropped because one of its modules would not parse")
	}
}

// writeFixtureMIB writes a MIB file into dir under the name gosmi looks for.
func writeFixtureMIB(t *testing.T, dir, module, content string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(dir, module+".txt"), []byte(content), 0o600); err != nil {
		t.Fatalf("write %s fixture: %v", module, err)
	}
}
