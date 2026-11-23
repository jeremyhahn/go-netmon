package poller

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/logging"
	"github.com/jeremyhahn/go-netmon/internal/mib/loader"
	"github.com/jeremyhahn/go-netmon/internal/testsupport"
)

// profileMIBDirs is the search path these tests parse with.
//
// The standard directory is complete for the modules loaded below: IF-MIB's and
// SNMPv2-MIB's imports all resolve within it. That matters because an
// incomplete search path is not an error — it silently yields fewer objects,
// most of them with no OID and no type (see
// internal/mib/parser/parser_searchpath_test.go). The assertions on OID and
// type below are what would catch it if this path ever stopped being complete.
func profileMIBDirs() []string {
	return testsupport.MIBDirs("standard")
}

// newProfileLoader builds a MIB loader over a temporary store, loaded from the
// repository's own MIB fixtures. The store is per-test, so nothing on the host
// is read or written outside the test's temporary directory.
func newProfileLoader(t testing.TB) *loader.Loader {
	t.Helper()

	mibCfg := &config.MIBConfig{
		Paths:       profileMIBDirs(),
		StoragePath: filepath.Join(t.TempDir(), "mib-storage"),
		AutoLoad: []string{
			"SNMPv2-MIB",
			"IF-MIB",
		},
	}

	mibLoader, err := loader.New(mibCfg)
	if err != nil {
		t.Fatalf("create MIB loader: %v", err)
	}
	t.Cleanup(func() { _ = mibLoader.Close() })

	if err := mibLoader.Load(); err != nil {
		t.Fatalf("load MIBs: %v", err)
	}

	return mibLoader
}

// TestPoller_WithMIBProfiles is the end-to-end check that a device's profile
// reaches the wire: the OIDs the profile selects are the OIDs the worker asks
// its device for.
//
// The devices poll in-process SNMPv2c agents (see testsupport's SNMPAgent), so
// what is verified is the whole path — profile selection, the scalar/table
// split, chunked GETs and the walk of every table the profile named — rather
// than only that Start returned no error.
func TestPoller_WithMIBProfiles(t *testing.T) {
	mibLoader := newProfileLoader(t)

	// Verify profiles loaded
	profiles := mibLoader.GetProfileManager().ListProfiles()
	if len(profiles) == 0 {
		t.Fatal("Expected built-in profiles to be loaded")
	}
	t.Logf("Loaded %d profiles", len(profiles))

	ciscoAgent := pollingAgent(t, "cisco-test")
	genericAgent := pollingAgent(t, "generic-test")

	cfg := getTestConfig(t)
	cfg.Devices = []config.Device{
		agentDevice("cisco-test", ciscoAgent),
		agentDevice("generic-test", genericAgent),
	}
	cfg.Devices[0].Profile = "cisco-ios"
	cfg.Devices[1].Profile = "default"

	p := New(cfg, mibLoader)
	if p.mibLoader == nil {
		t.Fatal("Expected poller to have MIB loader")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = p.Stop() }()

	results := awaitPollResults(t, p, []string{"cisco-test", "generic-test"}, 15*time.Second)

	// The fallback path polls exactly three system objects when no selection
	// was made. A profiled device must ask for more than that, which is what
	// distinguishes "the profile drove the poll" from "the profile was ignored
	// and the fallback ran".
	const fallbackOIDCount = 3

	for name, result := range results {
		if result.Error != nil {
			t.Errorf("device %s reported an error against a live agent: %v", name, result.Error)
		}
		if len(result.Results) <= fallbackOIDCount {
			t.Errorf("device %s read %d objects, which is no more than the %d-object fallback: its profile was not applied",
				name, len(result.Results), fallbackOIDCount)
		}
		// The device still reads the system objects its agent serves, decoded
		// from the wire rather than assumed.
		if got := sysNameFrom(t, result); got != name {
			t.Errorf("device %s read sysName %q, want %q", name, got, name)
		}
	}

	// One chunked GET plus one walk per table in the profile is many more than
	// a single request, so a worker that quietly polled nothing is visible.
	if ciscoAgent.Requests() < 2 || genericAgent.Requests() < 2 {
		t.Errorf("agent request counts = %d and %d; the profiles' table OIDs were never walked",
			ciscoAgent.Requests(), genericAgent.Requests())
	}
}

// TestPoller_ProfileSelection tests that correct OIDs are selected for profiles.
//
// The MIB fixtures it needs are in the repository, so it always runs; it used
// to be skipped on a check of a relative path that only held when the test
// binary happened to run from the package directory.
func TestPoller_ProfileSelection(t *testing.T) {
	mibLoader := newProfileLoader(t)

	// Test different profiles
	testCases := []struct {
		profileName string
		expectOIDs  bool
	}{
		{"default", true},
		{"cisco-ios", true},
		{"generic-router", true},
		{"interfaces-only", true},
	}

	profileMgr := mibLoader.GetProfileManager()

	for _, tc := range testCases {
		t.Run(tc.profileName, func(t *testing.T) {
			oids, err := profileMgr.SelectOIDsForProfile(tc.profileName)
			if err != nil {
				t.Errorf("Failed to select OIDs for profile %s: %v", tc.profileName, err)
				return
			}

			if tc.expectOIDs && len(oids) == 0 {
				t.Errorf("Expected OIDs for profile %s, got none", tc.profileName)
			}

			t.Logf("Profile %s selected %d OIDs", tc.profileName, len(oids))

			// Verify OIDs have required fields. An empty OID or a missing type
			// is the signature of a module parsed against an incomplete search
			// path, which reports no error of its own.
			for _, oid := range oids {
				if oid.OID == "" {
					t.Errorf("%s: OID has empty OID string", oid.Name)
				}
				if oid.Name == "" {
					t.Errorf("%s: OID has empty name", oid.OID)
				}
				if oid.Type == "" {
					t.Errorf("%s (%s) has no type: its module resolved only partially", oid.Name, oid.OID)
				}
			}
		})
	}
}

// TestPoller_ProfileSelection_UnknownProfile is the error path: a profile
// nobody defined must be reported, not silently answered with an empty
// selection that would leave a device polling nothing.
func TestPoller_ProfileSelection_UnknownProfile(t *testing.T) {
	mibLoader := newProfileLoader(t)

	oids, err := mibLoader.GetProfileManager().SelectOIDsForProfile("no-such-profile")
	if err == nil {
		t.Fatalf("SelectOIDsForProfile accepted an undefined profile and returned %d OIDs", len(oids))
	}
}

// TestPoller_ProfileStatistics tests profile statistics.
func TestPoller_ProfileStatistics(t *testing.T) {
	mibLoader := newProfileLoader(t)

	profileMgr := mibLoader.GetProfileManager()
	stats, err := profileMgr.GetStatistics("default")
	if err != nil {
		t.Fatalf("Failed to get profile statistics: %v", err)
	}

	if stats.TotalOIDs == 0 {
		t.Error("Expected some OIDs to be available")
	}

	t.Logf("Profile 'default' statistics: %d total OIDs, %d counters, %d gauges",
		stats.TotalOIDs, stats.Counters, stats.Gauges)
}

// TestPoller_ProfileStatistics_UnknownProfile is the error path for statistics.
func TestPoller_ProfileStatistics_UnknownProfile(t *testing.T) {
	mibLoader := newProfileLoader(t)

	stats, err := mibLoader.GetProfileManager().GetStatistics("no-such-profile")
	if err == nil {
		t.Fatalf("GetStatistics accepted an undefined profile and returned %+v", stats)
	}
}

// TestPoller_NilMIBLoader tests backward compatibility with nil MIB loader:
// without a MIB loader there is no selection to make, so the worker falls back
// to the three system objects and polls those and nothing else.
func TestPoller_NilMIBLoader(t *testing.T) {
	agent := pollingAgent(t, "fallback-device")

	cfg := getTestConfig(t)
	cfg.Devices = []config.Device{agentDevice("fallback-device", agent)}

	// Create poller without MIB loader (backward compatibility)
	p := New(cfg, nil)
	if p == nil {
		t.Fatal("Expected poller to be created with nil MIB loader")
	}
	if p.mibLoader != nil {
		t.Error("Expected mibLoader to be nil")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = p.Stop() }()

	result := awaitPollResults(t, p, []string{"fallback-device"}, 10*time.Second)["fallback-device"]
	if result.Error != nil {
		t.Fatalf("the fallback poll failed: %v", result.Error)
	}

	want := map[string]bool{sysDescrOID: true, sysUpTimeOID: true, sysNameOID: true}
	if len(result.Results) != len(want) {
		t.Fatalf("the fallback poll read %d objects, want exactly %d", len(result.Results), len(want))
	}
	for _, r := range result.Results {
		oid := strings.TrimPrefix(r.OID, ".")
		if !want[oid] {
			t.Errorf("the fallback poll read %s, which is not one of the three system objects", oid)
		}
		delete(want, oid)
	}
	for oid := range want {
		t.Errorf("the fallback poll never read %s", oid)
	}
}

// ifTableAgent starts an agent that serves the system objects plus a two-row
// ifTable, so a device configured with IF-MIB has a table to walk and rows to
// bring back.
func ifTableAgent(t testing.TB, sysName string) *testsupport.SNMPAgent {
	t.Helper()

	agent, err := testsupport.NewSNMPAgent(
		testsupport.OctetString(sysDescrOID, "go-netmon in-process test agent"),
		testsupport.TimeTicks(sysUpTimeOID, 4242),
		testsupport.OctetString(sysNameOID, sysName),

		testsupport.Integer(ifNumberOID, 2),

		testsupport.Integer("1.3.6.1.2.1.2.2.1.1.1", 1),
		testsupport.OctetString(ifDescrOID+".1", "eth0"),
		testsupport.Counter32("1.3.6.1.2.1.2.2.1.10.1", 1_000),

		testsupport.Integer("1.3.6.1.2.1.2.2.1.1.2", 2),
		testsupport.OctetString(ifDescrOID+".2", "eth1"),
		testsupport.Counter32("1.3.6.1.2.1.2.2.1.10.2", 2_000),
	)
	if err != nil {
		t.Fatalf("start snmp agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })

	return agent
}

// Objects of the ifTable the agent above serves.
const (
	ifNumberOID = "1.3.6.1.2.1.2.1.0"
	ifDescrOID  = "1.3.6.1.2.1.2.2.1.2"
)

// TestPoller_ModuleSelection_WalksTheModulesTables covers the other half of OID
// selection: a device that names MIB modules explicitly rather than a profile.
//
// That path reads the module's objects out of the MIB store, splits them into
// scalars to GET and tables to walk, and it is the path the WebUI's device
// editor and the config sync both produce. The assertion is that a row of the
// module's table actually comes back, which is what distinguishes a walk that
// ran from a walk that was never issued.
func TestPoller_ModuleSelection_WalksTheModulesTables(t *testing.T) {
	mibLoader := newProfileLoader(t)
	agent := ifTableAgent(t, "module-device")

	var sink safeBuffer
	logger, err := logging.NewWithWriter(
		config.LoggingConfig{Level: "debug", Format: "text", Output: "stdout"}, &sink)
	if err != nil {
		t.Fatalf("NewWithWriter: %v", err)
	}

	cfg := getTestConfig(t)
	cfg.Devices = []config.Device{agentDevice("module-device", agent)}
	cfg.Devices[0].Modules = []string{"IF-MIB"}

	p := NewWithExporter(cfg, mibLoader, nil, WithLogger(logger))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = p.Stop() }()

	result := awaitPollResults(t, p, []string{"module-device"}, 15*time.Second)["module-device"]
	if result.Error != nil {
		t.Fatalf("the module-selected poll failed: %v", result.Error)
	}

	// Every row of ifDescr the agent serves must come back from the walk.
	wantRows := map[string]string{ifDescrOID + ".1": "eth0", ifDescrOID + ".2": "eth1"}
	for _, r := range result.Results {
		oid := strings.TrimPrefix(r.OID, ".")
		want, ok := wantRows[oid]
		if !ok {
			continue
		}
		got, convErr := r.GetStringValue()
		if convErr != nil {
			t.Errorf("%s: %v", oid, convErr)
		}
		if got != want {
			t.Errorf("%s = %q, want %q", oid, got, want)
		}
		delete(wantRows, oid)
	}
	for oid := range wantRows {
		t.Errorf("the walk of IF-MIB's tables never returned %s: the module's tables were not walked", oid)
	}

	// The classification of the module's objects is traced at debug level, and
	// it must have seen both kinds.
	trace := sink.String()
	if !strings.Contains(trace, "adding table OID") {
		t.Error("no table OID was classified from IF-MIB")
	}
	if !strings.Contains(trace, "adding scalar OID") {
		t.Error("no scalar OID was classified from IF-MIB")
	}
}

// TestPoller_UnknownProfile_FailsStart is the error path of OID selection: a
// device naming a profile nobody defined must fail loudly at registration
// rather than start a worker that silently polls the fallback objects forever.
func TestPoller_UnknownProfile_FailsStart(t *testing.T) {
	mibLoader := newProfileLoader(t)
	agent := pollingAgent(t, "bad-profile-device")

	cfg := getTestConfig(t)
	cfg.Devices = []config.Device{agentDevice("bad-profile-device", agent)}
	cfg.Devices[0].Profile = "no-such-profile"

	p := New(cfg, mibLoader)
	defer func() { _ = p.Stop() }()

	err := p.Start(context.Background())
	if err == nil {
		t.Fatal("Start accepted a device whose profile does not exist")
	}
	if !strings.Contains(err.Error(), "no-such-profile") {
		t.Errorf("error = %q, want it to name the missing profile", err)
	}
	if status := p.GetWorkerStatus(); len(status) != 0 {
		t.Errorf("%d workers were left behind by a failed Start", len(status))
	}
}

// TestPoller_NoProfileUsesDefault pins the documented default: a device that
// names no profile is polled with the "default" one, not with nothing.
func TestPoller_NoProfileUsesDefault(t *testing.T) {
	mibLoader := newProfileLoader(t)
	agent := pollingAgent(t, "unprofiled-device")

	cfg := getTestConfig(t)
	cfg.Devices = []config.Device{agentDevice("unprofiled-device", agent)}
	cfg.Devices[0].Profile = "" // no profile named

	p := New(cfg, mibLoader)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = p.Stop() }()

	result := awaitPollResults(t, p, []string{"unprofiled-device"}, 15*time.Second)["unprofiled-device"]
	if result.Error != nil {
		t.Fatalf("the default-profile poll failed: %v", result.Error)
	}

	wantOIDs, err := mibLoader.GetProfileManager().SelectOIDsForProfile("default")
	if err != nil {
		t.Fatalf("SelectOIDsForProfile(default): %v", err)
	}
	scalars := 0
	for _, oid := range wantOIDs {
		if oid.IsScalar && !oid.IsTable && !oid.IsColumn && !oid.IsTableEntry {
			scalars++
		}
	}
	if len(result.Results) != scalars {
		t.Errorf("the device read %d objects, want the %d scalars of the default profile: a different selection was used",
			len(result.Results), scalars)
	}
}

// BenchmarkPoller_WithProfiles benchmarks poller creation with profiles.
func BenchmarkPoller_WithProfiles(b *testing.B) {
	mibLoader := newProfileLoader(b)

	agent := pollingAgent(b, "bench-device")
	cfg := getTestConfig(b)
	cfg.Devices = []config.Device{agentDevice("bench-device", agent)}
	cfg.Devices[0].Profile = "default"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		poller := New(cfg, mibLoader)
		_ = poller.Stop()
	}
}
