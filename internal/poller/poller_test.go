package poller

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/testsupport"
)

// The system objects the poller falls back to when no MIB loader has supplied
// it a selection (see worker.poll). An agent serving these is enough to
// exercise the whole polling path end to end. sysUpTime is not among them
// because the poller names it itself, as the object it probes a silent device
// with.
const (
	sysDescrOID = "1.3.6.1.2.1.1.1.0"
	sysNameOID  = "1.3.6.1.2.1.1.5.0"
)

// getTestConfig returns a poller configuration whose devices point at closed
// loopback ports. Nothing in this file may send a packet off the machine, so
// the addresses are reserved-and-released local ports (see
// unreachableSNMPAddress) rather than plausible-looking LAN addresses, and the
// SNMP budget is short enough that a worker which does reach for one of them
// gives up immediately instead of holding the suite open for its full retry
// budget.
func getTestConfig(t testing.TB) *config.Config {
	t.Helper()

	return &config.Config{
		Global: config.GlobalConfig{
			DefaultPollInterval: 60,
			MaxWorkers:          10,
			SNMPTimeout:         1,
			SNMPRetries:         0,
		},
		Prometheus: config.PrometheusConfig{
			RemoteWriteURL: "http://test:9090/api/v1/write",
			BatchSize:      1000,
			FlushInterval:  10,
		},
		API: config.APIConfig{
			ListenAddress: "0.0.0.0:8080",
		},
		Metrics: config.MetricsConfig{
			ListenAddress: "0.0.0.0:9090",
		},
		Devices: []config.Device{
			{
				Name:        "device1",
				Address:     unreachableSNMPAddress(t),
				SNMPVersion: "2c",
				Community:   "public",
				Enabled:     true,
			},
			{
				Name:        "device2",
				Address:     unreachableSNMPAddress(t),
				SNMPVersion: "2c",
				Community:   "public",
				Enabled:     true,
			},
		},
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
			Output: "stdout",
		},
	}
}

// pollingAgent starts an in-process SNMPv2c agent that answers the poller's
// fallback system OIDs, naming itself sysName so a result can be traced back to
// the device it came from. The agent is shared with internal/snmp through
// testsupport rather than reimplemented here.
func pollingAgent(t testing.TB, sysName string) *testsupport.SNMPAgent {
	t.Helper()

	agent, err := testsupport.NewSNMPAgent(
		testsupport.OctetString(sysDescrOID, "go-netmon in-process test agent"),
		testsupport.TimeTicks(sysUpTimeOID, 4242),
		testsupport.OctetString(sysNameOID, sysName),
	)
	if err != nil {
		t.Fatalf("start snmp agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })

	return agent
}

// agentDevice describes a device that polls agent once a second, the shortest
// interval a device configuration can express.
func agentDevice(name string, agent *testsupport.SNMPAgent) config.Device {
	return config.Device{
		Name:         name,
		Address:      agent.Addr(),
		SNMPVersion:  "2c",
		Community:    "public",
		PollInterval: 1,
		Enabled:      true,
	}
}

// sysNameFrom returns the sysName the device reported in result, which
// identifies the agent that answered it.
func sysNameFrom(t testing.TB, result *PollResult) string {
	t.Helper()

	for _, r := range result.Results {
		if strings.TrimPrefix(r.OID, ".") != sysNameOID {
			continue
		}
		value, err := r.GetStringValue()
		if err != nil {
			t.Fatalf("sysName from %s: %v", result.DeviceName, err)
		}
		return value
	}

	t.Fatalf("poll result for %s carried no sysName; it read %d objects", result.DeviceName, len(result.Results))
	return ""
}

// awaitPollResults reads results off the poller until every named device has
// reported once, and fails if any of them does not.
func awaitPollResults(t testing.TB, p *Poller, devices []string, timeout time.Duration) map[string]*PollResult {
	t.Helper()

	got := make(map[string]*PollResult, len(devices))
	wanted := make(map[string]bool, len(devices))
	for _, name := range devices {
		wanted[name] = true
	}

	deadline := time.After(timeout)
	for len(got) < len(wanted) {
		select {
		case result, open := <-p.Results():
			if !open || result == nil {
				t.Fatal("the poller closed its result channel while the test was still reading")
				return nil // unreachable: t.Fatal ends the test
			}
			if !wanted[result.DeviceName] {
				t.Fatalf("a result arrived from %q, which is not one of the configured devices", result.DeviceName)
			}
			if _, seen := got[result.DeviceName]; !seen {
				got[result.DeviceName] = result
			}
		case <-deadline:
			missing := make([]string, 0, len(wanted))
			for name := range wanted {
				if _, seen := got[name]; !seen {
					missing = append(missing, name)
				}
			}
			t.Fatalf("no poll result from %v within %v", missing, timeout)
		}
	}

	return got
}

func TestNew(t *testing.T) {
	cfg := getTestConfig(t)
	poller := New(cfg, nil) // nil mibLoader for testing

	if poller == nil {
		t.Fatal("Expected poller to be created")
	}

	if poller.config != cfg {
		t.Error("Poller config doesn't match input config")
	}

	if poller.pool == nil {
		t.Error("Expected SNMP pool to be initialized")
	}

	if poller.resultChan == nil {
		t.Error("Expected result channel to be initialized")
	}
}

// TestPoller_Start_NoEnabledDevices verifies that starting with no enabled
// devices succeeds rather than erroring: devices can be registered later
// via the poller control API or WebUI config sync (see AddDevice and
// docs/CONFIG_SYNC.md), so Start must accept an empty/fully-disabled
// device list and simply wait for devices to be added.
func TestPoller_Start_NoEnabledDevices(t *testing.T) {
	cfg := getTestConfig(t)
	// Disable all devices
	for i := range cfg.Devices {
		cfg.Devices[i].Enabled = false
	}

	poller := New(cfg, nil)
	ctx := context.Background()

	if err := poller.Start(ctx); err != nil {
		t.Fatalf("Expected no error starting with no enabled devices, got: %v", err)
	}
	defer poller.Stop()

	if status := poller.GetWorkerStatus(); len(status) != 0 {
		t.Errorf("Expected 0 workers, got %d", len(status))
	}
}

// TestPoller_Start_PollsEveryEnabledDevice is the whole-path check on Start:
// every enabled device gets a running worker, and every one of those workers
// reaches its own device and reports what it read back.
//
// It replaces a test that was skipped for want of "an actual SNMP server". The
// server now runs in this process on a loopback socket (see testsupport's
// SNMPAgent), so the SNMP encoding, the socket and the client's response
// handling are all exercised for real without a packet leaving the machine.
// Skipped, the test asserted nothing at all; the worker-per-device count and
// the routing of a result back to the device that produced it were unguarded.
func TestPoller_Start_PollsEveryEnabledDevice(t *testing.T) {
	t.Parallel()

	first := pollingAgent(t, "device1")
	second := pollingAgent(t, "device2")

	cfg := getTestConfig(t)
	cfg.Devices = []config.Device{
		agentDevice("device1", first),
		agentDevice("device2", second),
	}

	p := New(cfg, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	status := p.GetWorkerStatus()
	if len(status) != 2 {
		t.Fatalf("worker count = %d, want one per enabled device (2)", len(status))
	}
	for name, w := range status {
		if !w.Running {
			t.Errorf("worker %s reports Running = false immediately after Start", name)
		}
	}

	results := awaitPollResults(t, p, []string{"device1", "device2"}, 10*time.Second)
	for name, result := range results {
		if result.Error != nil {
			t.Errorf("device %s reported an error against a live agent: %v", name, result.Error)
		}
		if len(result.Results) != 3 {
			t.Errorf("device %s read %d objects, want the 3 fallback system objects", name, len(result.Results))
		}
		// The sysName proves the result came from that device's own agent
		// rather than from whichever client the pool handed out first.
		if got := sysNameFrom(t, result); got != name {
			t.Errorf("device %s read sysName %q: its result came from another device's agent", name, got)
		}
	}

	if first.Requests() == 0 || second.Requests() == 0 {
		t.Errorf("agent request counts = %d and %d; a device was never actually polled",
			first.Requests(), second.Requests())
	}

	if err := p.Stop(); err != nil {
		t.Errorf("Stop: %v", err)
	}
}

func TestPoller_Stop_WithoutStart(t *testing.T) {
	cfg := getTestConfig(t)
	poller := New(cfg, nil)

	err := poller.Stop()
	if err != nil {
		t.Errorf("Expected no error stopping unstarted poller, got: %v", err)
	}
}

func TestPoller_Stop_Multiple(t *testing.T) {
	cfg := getTestConfig(t)
	poller := New(cfg, nil)

	// First stop
	if err := poller.Stop(); err != nil {
		t.Errorf("First stop failed: %v", err)
	}

	// Second stop should be idempotent
	if err := poller.Stop(); err != nil {
		t.Errorf("Second stop failed: %v", err)
	}
}

// TestPoller_Start_AfterStop keeps a stopped poller from being restarted. Its
// stop channel is already closed, so a worker started now would return at once
// and the caller would be left with a poller that reports workers it is not
// running.
func TestPoller_Start_AfterStop(t *testing.T) {
	cfg := getTestConfig(t)
	poller := New(cfg, nil)

	if err := poller.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	err := poller.Start(context.Background())
	if err == nil {
		t.Fatal("Start restarted a poller that had already been stopped")
	}
	if status := poller.GetWorkerStatus(); len(status) != 0 {
		t.Errorf("%d workers were started by a rejected Start", len(status))
	}
}

func TestPoller_Results(t *testing.T) {
	cfg := getTestConfig(t)
	poller := New(cfg, nil)

	resultChan := poller.Results()
	if resultChan == nil {
		t.Error("Expected result channel")
	}
}

func TestPoller_GetWorkerStatus_Empty(t *testing.T) {
	cfg := getTestConfig(t)
	poller := New(cfg, nil)

	status := poller.GetWorkerStatus()
	if len(status) != 0 {
		t.Errorf("Expected 0 workers, got %d", len(status))
	}
}

// TestPoller_GetWorkerStatus_ReturnsDeviceDetails tests the bug fix
// Ensures that GetWorkerStatus returns complete device information
func TestPoller_GetWorkerStatus_ReturnsDeviceDetails(t *testing.T) {
	cfg := getTestConfig(t)
	// Start with no devices
	cfg.Devices = []config.Device{}

	poller := New(cfg, nil)
	ctx := context.Background()

	// Add a device with all fields populated
	address := unreachableSNMPAddress(t)
	testDevice := &config.Device{
		Name:         "test-device-01",
		Address:      address,
		SNMPVersion:  "2c",
		Community:    "public",
		PollInterval: 30,
		Enabled:      true,
		Profile:      "default",
		Tags: map[string]string{
			"site":     "datacenter-1",
			"rack":     "A1",
			"function": "core-router",
		},
		Modules: []string{"IF-MIB", "SNMPv2-MIB"},
	}

	// Add device to poller
	err := poller.AddDevice(ctx, testDevice)
	if err != nil {
		t.Fatalf("Failed to add device: %v", err)
	}
	defer poller.Stop()

	// Get worker status
	workerStatus := poller.GetWorkerStatus()

	// Verify we have one worker
	if len(workerStatus) != 1 {
		t.Fatalf("Expected 1 worker, got %d", len(workerStatus))
	}

	// Get the worker status for our device
	status, exists := workerStatus["test-device-01"]
	if !exists {
		t.Fatal("Expected worker status for test-device-01")
	}

	// Verify DeviceName is set
	if status.DeviceName != "test-device-01" {
		t.Errorf("Expected DeviceName 'test-device-01', got '%s'", status.DeviceName)
	}

	// Verify Device pointer is not nil
	if status.Device == nil {
		t.Fatal("Expected Device to be non-nil (BUG: device details not returned)")
	}

	// Verify all device fields are populated correctly
	if status.Device.Name != "test-device-01" {
		t.Errorf("Expected Device.Name 'test-device-01', got '%s'", status.Device.Name)
	}

	if status.Device.Address != address {
		t.Errorf("Expected Device.Address '%s', got '%s'", address, status.Device.Address)
	}

	if status.Device.SNMPVersion != "2c" {
		t.Errorf("Expected Device.SNMPVersion '2c', got '%s'", status.Device.SNMPVersion)
	}

	if status.Device.PollInterval != 30 {
		t.Errorf("Expected Device.PollInterval 30, got %d", status.Device.PollInterval)
	}

	if status.Device.Profile != "default" {
		t.Errorf("Expected Device.Profile 'default', got '%s'", status.Device.Profile)
	}

	// Verify tags are preserved
	if len(status.Device.Tags) != 3 {
		t.Errorf("Expected 3 tags, got %d", len(status.Device.Tags))
	}

	if status.Device.Tags["site"] != "datacenter-1" {
		t.Errorf("Expected tag 'site' = 'datacenter-1', got '%s'", status.Device.Tags["site"])
	}

	// Verify modules are preserved
	if len(status.Device.Modules) != 2 {
		t.Errorf("Expected 2 modules, got %d", len(status.Device.Modules))
	}

	// Verify interval is set
	if status.Interval != 30*time.Second {
		t.Errorf("Expected Interval 30s, got %v", status.Interval)
	}

	// Verify running status
	if status.Running != true {
		t.Error("Expected Running to be true")
	}
}

// TestPoller_AddDevice_PreservesAllFields tests that device registration preserves all fields
func TestPoller_AddDevice_PreservesAllFields(t *testing.T) {
	cfg := getTestConfig(t)
	cfg.Devices = []config.Device{}

	poller := New(cfg, nil)
	defer poller.Stop()
	ctx := context.Background()

	// Test cases with various device configurations
	testCases := []struct {
		name   string
		device *config.Device
	}{
		{
			name: "SNMPv2c device with all fields",
			device: &config.Device{
				Name:         "router-01",
				Address:      unreachableSNMPAddress(t),
				SNMPVersion:  "2c",
				Community:    "private",
				PollInterval: 60,
				Enabled:      true,
				Profile:      "cisco",
				Tags: map[string]string{
					"type":     "router",
					"location": "building-a",
				},
				Modules: []string{"CISCO-PROCESS-MIB"},
			},
		},
		{
			name: "SNMPv3 device",
			device: &config.Device{
				Name:         "switch-01",
				Address:      unreachableSNMPAddress(t),
				SNMPVersion:  "3",
				Username:     "admin",
				AuthProtocol: "SHA",
				AuthPassword: "authpass",
				PrivProtocol: "AES",
				PrivPassword: "privpass",
				PollInterval: 45,
				Enabled:      true,
				Profile:      "interfaces",
			},
		},
		{
			name: "Device with zero poll interval (uses default)",
			device: &config.Device{
				Name:         "server-01",
				Address:      unreachableSNMPAddress(t),
				SNMPVersion:  "2c",
				Community:    "public",
				PollInterval: 0, // Should use default from config
				Enabled:      true,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Add device
			err := poller.AddDevice(ctx, tc.device)
			if err != nil {
				t.Fatalf("Failed to add device: %v", err)
			}

			// Get worker status
			workerStatus := poller.GetWorkerStatus()

			// Verify device exists
			status, exists := workerStatus[tc.device.Name]
			if !exists {
				t.Fatalf("Worker not found for device %s", tc.device.Name)
			}

			// Verify device details are complete
			if status.Device == nil {
				t.Fatal("Device details are nil")
			}

			if status.Device.Name != tc.device.Name {
				t.Errorf("Name mismatch: expected '%s', got '%s'", tc.device.Name, status.Device.Name)
			}

			if status.Device.Address != tc.device.Address {
				t.Errorf("Address mismatch: expected '%s', got '%s'", tc.device.Address, status.Device.Address)
			}

			if status.Device.SNMPVersion != tc.device.SNMPVersion {
				t.Errorf("SNMPVersion mismatch: expected '%s', got '%s'", tc.device.SNMPVersion, status.Device.SNMPVersion)
			}

			// Remove device for next test case
			_ = poller.RemoveDevice(tc.device.Name)
		})
	}
}

func TestPoller_GetPoolSize(t *testing.T) {
	cfg := getTestConfig(t)
	poller := New(cfg, nil)

	size := poller.GetPoolSize()
	if size != 0 {
		t.Errorf("Expected pool size 0, got %d", size)
	}
}

func TestPoller_AddDevice_WhenStopped(t *testing.T) {
	cfg := getTestConfig(t)
	poller := New(cfg, nil)

	// Stop poller first
	if err := poller.Stop(); err != nil {
		t.Fatalf("Failed to stop poller: %v", err)
	}

	ctx := context.Background()
	device := &config.Device{
		Name:        "new-device",
		Address:     unreachableSNMPAddress(t),
		SNMPVersion: "2c",
		Community:   "public",
		Enabled:     true,
	}

	err := poller.AddDevice(ctx, device)
	if err == nil {
		t.Error("Expected error when adding device to stopped poller")
	}
}

func TestPoller_AddDevice_InvalidDevice(t *testing.T) {
	cfg := getTestConfig(t)
	// Start with no devices
	cfg.Devices = []config.Device{}

	poller := New(cfg, nil)
	defer poller.Stop()
	ctx := context.Background()

	invalidDevice := &config.Device{
		Name:    "", // Invalid: empty name
		Address: unreachableSNMPAddress(t),
	}

	err := poller.AddDevice(ctx, invalidDevice)
	if err == nil {
		t.Error("Expected error when adding invalid device")
	}
}

func TestPoller_AddDevice_Duplicate(t *testing.T) {
	cfg := getTestConfig(t)
	cfg.Devices = []config.Device{}

	poller := New(cfg, nil)
	defer poller.Stop()
	ctx := context.Background()

	device := &config.Device{
		Name:        "duplicate-device",
		Address:     unreachableSNMPAddress(t),
		SNMPVersion: "2c",
		Community:   "public",
		Enabled:     true,
	}

	// Add device first time
	err := poller.AddDevice(ctx, device)
	if err != nil {
		t.Fatalf("Failed to add device first time: %v", err)
	}

	// Try to add same device again
	err = poller.AddDevice(ctx, device)
	if err == nil {
		t.Error("Expected error when adding duplicate device")
	}
}

func TestPoller_RemoveDevice_NotFound(t *testing.T) {
	cfg := getTestConfig(t)
	poller := New(cfg, nil)

	err := poller.RemoveDevice("nonexistent")
	if err == nil {
		t.Error("Expected error when removing nonexistent device")
	}
}

func TestPoller_RemoveDevice_Success(t *testing.T) {
	cfg := getTestConfig(t)
	cfg.Devices = []config.Device{}

	poller := New(cfg, nil)
	defer poller.Stop()
	ctx := context.Background()

	device := &config.Device{
		Name:        "remove-test",
		Address:     unreachableSNMPAddress(t),
		SNMPVersion: "2c",
		Community:   "public",
		Enabled:     true,
	}

	// Add device
	err := poller.AddDevice(ctx, device)
	if err != nil {
		t.Fatalf("Failed to add device: %v", err)
	}

	// Verify device exists
	status := poller.GetWorkerStatus()
	if len(status) != 1 {
		t.Errorf("Expected 1 worker, got %d", len(status))
	}

	// Remove device
	err = poller.RemoveDevice(device.Name)
	if err != nil {
		t.Errorf("Failed to remove device: %v", err)
	}

	// Verify device is gone
	status = poller.GetWorkerStatus()
	if len(status) != 0 {
		t.Errorf("Expected 0 workers after removal, got %d", len(status))
	}
}

func TestPoller_HealthCheck_Empty(t *testing.T) {
	cfg := getTestConfig(t)
	poller := New(cfg, nil)

	results := poller.HealthCheck()
	if len(results) != 0 {
		t.Errorf("Expected 0 health check results, got %d", len(results))
	}
}

func TestPollResult(t *testing.T) {
	result := &PollResult{
		DeviceName: "test-device",
		Timestamp:  time.Now(),
		Results:    nil,
		Error:      nil,
	}

	if result.DeviceName != "test-device" {
		t.Errorf("Expected device name 'test-device', got %s", result.DeviceName)
	}

	if result.Timestamp.IsZero() {
		t.Error("Expected non-zero timestamp")
	}
}

func TestWorkerStatus(t *testing.T) {
	testDevice := &config.Device{
		Name:         "test-device",
		Address:      "203.0.113.1",
		SNMPVersion:  "2c",
		Community:    "public",
		PollInterval: 60,
	}

	status := WorkerStatus{
		DeviceName: "test-device",
		Device:     testDevice,
		Interval:   60 * time.Second,
		Running:    true,
	}

	if status.DeviceName != "test-device" {
		t.Errorf("Expected device name 'test-device', got %s", status.DeviceName)
	}

	if status.Device == nil {
		t.Error("Expected Device to be non-nil")
	}

	if status.Device.Address != "203.0.113.1" {
		t.Errorf("Expected Device.Address '203.0.113.1', got '%s'", status.Device.Address)
	}

	if status.Interval != 60*time.Second {
		t.Errorf("Expected interval 60s, got %v", status.Interval)
	}

	if !status.Running {
		t.Error("Expected running status to be true")
	}
}

// TestPoller_ConcurrentPolling states that the poller keeps its devices apart
// while polling them at the same time: each of three workers talks to its own
// agent, and each result carries the sysName of the agent that answered it.
//
// This is the guarantee a shared client, a shared connection or a mis-keyed
// pool entry would break, and it is what the previous "Requires actual SNMP
// server" skip left completely unchecked.
func TestPoller_ConcurrentPolling(t *testing.T) {
	t.Parallel()

	names := []string{"alpha", "bravo", "charlie"}

	cfg := getTestConfig(t)
	cfg.Devices = nil
	agents := make(map[string]*testsupport.SNMPAgent, len(names))
	for _, name := range names {
		agent := pollingAgent(t, name)
		agents[name] = agent
		cfg.Devices = append(cfg.Devices, agentDevice(name, agent))
	}

	p := New(cfg, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	results := awaitPollResults(t, p, names, 10*time.Second)
	for _, name := range names {
		result := results[name]
		if result.Error != nil {
			t.Errorf("device %s reported an error against a live agent: %v", name, result.Error)
		}
		if got := sysNameFrom(t, result); got != name {
			t.Errorf("device %s read sysName %q: concurrent polls crossed devices", name, got)
		}
		if result.Timestamp.IsZero() {
			t.Errorf("device %s reported a zero timestamp", name)
		}
	}

	if size := p.GetPoolSize(); size != len(names) {
		t.Errorf("pool size = %d, want one client per device (%d)", size, len(names))
	}

	if err := p.Stop(); err != nil {
		t.Errorf("Stop: %v", err)
	}
}

// TestPoller_ContextCancellation is the shutdown guarantee: canceling the
// context Start was given ends every worker, and Stop afterwards is still safe.
//
// Skipped, this test proved nothing — a poller that ignored cancellation
// entirely would have passed it. Here the agents' own request counters are what
// prove the polling stopped: they must not move again once the context is
// canceled.
func TestPoller_ContextCancellation(t *testing.T) {
	t.Parallel()

	first := pollingAgent(t, "device1")
	second := pollingAgent(t, "device2")

	cfg := getTestConfig(t)
	cfg.Devices = []config.Device{
		agentDevice("device1", first),
		agentDevice("device2", second),
	}

	p := New(cfg, nil)
	ctx, cancel := context.WithCancel(context.Background())

	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Both devices must be polling before cancellation can be shown to stop it.
	awaitPollResults(t, p, []string{"device1", "device2"}, 10*time.Second)

	cancel()

	// Let any poll that was already in flight finish before the counters are
	// read, so a request that started before the cancellation is not mistaken
	// for one that started after it.
	time.Sleep(500 * time.Millisecond)
	firstAfterCancel, secondAfterCancel := first.Requests(), second.Requests()

	// Two poll intervals is long enough for a surviving worker to speak again.
	time.Sleep(2500 * time.Millisecond)

	if got := first.Requests(); got != firstAfterCancel {
		t.Errorf("device1 sent %d more requests after its context was canceled", got-firstAfterCancel)
	}
	if got := second.Requests(); got != secondAfterCancel {
		t.Errorf("device2 sent %d more requests after its context was canceled", got-secondAfterCancel)
	}

	// Stop must still be safe, and must not block: its workers have already
	// returned, so there is nothing left to wait for.
	stopped := make(chan error, 1)
	go func() { stopped <- p.Stop() }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Errorf("Stop after context cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop blocked after the context was canceled: a worker never returned")
	}
}

// BenchmarkPoller_Start measures registering and tearing down a single worker
// against a live in-process agent, so the cost measured is the poller's own
// startup path rather than a connection timeout.
func BenchmarkPoller_Start(b *testing.B) {
	agent := pollingAgent(b, "bench-device")

	cfg := getTestConfig(b)
	cfg.Devices = []config.Device{agentDevice("bench-device", agent)}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		poller := New(cfg, nil)
		ctx := context.Background()

		if err := poller.Start(ctx); err != nil {
			b.Fatalf("Start: %v", err)
		}
		if err := poller.Stop(); err != nil {
			b.Fatalf("Stop: %v", err)
		}
	}
}
