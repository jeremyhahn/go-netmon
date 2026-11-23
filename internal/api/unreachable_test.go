package api

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/poller"
)

// A device's status has two sources that answer different questions, and this
// file is about the join between them: the status tracker knows what the last
// poll produced, and only the poller knows whether the device is answering at
// all and when it will next be tried.

// deadAddress returns a loopback address with nothing listening on it. The
// kernel answers a datagram sent there with ICMP port-unreachable, so a poll
// fails immediately and deterministically without a packet leaving the host and
// without waiting out an SNMP timeout.
func deadAddress(t *testing.T) string {
	t.Helper()

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("reserving a loopback port: %v", err)
	}
	addr := conn.LocalAddr().String()
	if err := conn.Close(); err != nil {
		t.Fatalf("releasing the reserved port: %v", err)
	}

	return addr
}

// fastFailConfig polls on a budget short enough that a test never waits for a
// production timeout.
func fastFailConfig() *config.Config {
	cfg := getTestConfig()
	cfg.Global.DefaultPollInterval = 60
	cfg.Global.SNMPTimeout = 1
	cfg.Global.SNMPRetries = 0
	return cfg
}

func unreachableDevice(t *testing.T, name string) *config.Device {
	t.Helper()

	return &config.Device{
		Name:         name,
		Address:      deadAddress(t),
		SNMPVersion:  "2c",
		Community:    "public",
		PollInterval: 60,
		Enabled:      true,
	}
}

// TestDeviceStatusReportsAnUnreachableDevice is the operator-facing half of the
// fix: a device that answers nothing has to be visibly unreachable rather than
// merely quiet, and has to say when it will be tried again.
func TestDeviceStatusReportsAnUnreachableDevice(t *testing.T) {
	t.Parallel()

	cfg := fastFailConfig()
	p := poller.New(cfg, nil)
	t.Cleanup(func() { _ = p.Stop() })

	handler := NewPollerControlHandler(p, cfg)
	device := unreachableDevice(t, "bastion")

	registered := time.Now()
	if err := p.AddDevice(t.Context(), device); err != nil {
		t.Fatalf("AddDevice: %v", err)
	}

	status := awaitUnreachable(t, handler, device.Name)

	if status.ConsecutiveFailures == 0 {
		t.Error("consecutive_failures = 0 for a device that has never answered")
	}

	// The retry is bracketed against the registration rather than compared to
	// time.Now(), which asserts something about the runner instead: the worker
	// schedules it one poll interval past the cycle that failed, so a test
	// stalled longer than that between the status read and the comparison would
	// report a bug that is not there. Bracketing pins the value exactly and
	// cannot fail for a stall.
	interval := time.Duration(device.PollInterval) * time.Second
	if status.NextPollTime.Before(registered) || status.NextPollTime.After(registered.Add(2*interval)) {
		t.Errorf("next_poll_time = %s, want the retry the device is scheduled for, within [%s, %s]",
			status.NextPollTime, registered, registered.Add(2*interval))
	}
	if !status.LastPollTime.IsZero() {
		t.Errorf("last_poll_time = %s, want the zero time: no poll result has been recorded",
			status.LastPollTime)
	}
}

// TestDeviceStatusOverHTTPCarriesReachability pins the wire format, which is
// what the web UI and the CLI read.
func TestDeviceStatusOverHTTPCarriesReachability(t *testing.T) {
	t.Parallel()

	cfg := fastFailConfig()
	p := poller.New(cfg, nil)
	t.Cleanup(func() { _ = p.Stop() })

	handler := NewPollerControlHandler(p, cfg)
	device := unreachableDevice(t, "bastion")
	if err := p.AddDevice(t.Context(), device); err != nil {
		t.Fatalf("AddDevice: %v", err)
	}
	awaitUnreachable(t, handler, device.Name)

	req := httptest.NewRequest(http.MethodGet, "/api/poller/devices/bastion/status", nil)
	w := httptest.NewRecorder()
	handler.HandleGetDeviceStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. Body: %s", w.Code, w.Body.String())
	}

	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decoding the status response: %v", err)
	}
	if unreachable, _ := body["unreachable"].(bool); !unreachable {
		t.Errorf(`response has unreachable=%v, want true. Body: %v`, body["unreachable"], body)
	}
	if _, ok := body["next_poll_time"]; !ok {
		t.Error("the response does not say when the device will next be tried")
	}
}

// TestDeviceStatusSeparatesNeverPolledFromPolledAndFailed is the distinction the
// UI could not make: both devices below have never produced a metric, and only
// one of them has ever been asked.
func TestDeviceStatusSeparatesNeverPolledFromPolledAndFailed(t *testing.T) {
	t.Parallel()

	cfg := fastFailConfig()
	p := poller.New(cfg, nil)
	t.Cleanup(func() { _ = p.Stop() })

	handler := NewPollerControlHandler(p, cfg)
	tracker := handler.GetStatusTracker()

	fresh := unreachableDevice(t, "fresh")
	failed := unreachableDevice(t, "failed")
	for _, device := range []*config.Device{fresh, failed} {
		if err := p.AddDevice(t.Context(), device); err != nil {
			t.Fatalf("AddDevice %s: %v", device.Name, err)
		}
	}

	// What the poller's result loop does with a failed poll.
	tracker.UpdatePollError(failed.Name, &poller.UnreachableError{Device: failed.Name, Attempts: 4})

	neverPolledStatus := handler.deviceStatus(fresh.Name)
	if neverPolledStatus == nil {
		t.Fatal("a registered device has no status at all")
	}
	if !neverPolledStatus.LastPollTime.IsZero() {
		t.Errorf("a device with no recorded poll has last_poll_time %s, want the zero time",
			neverPolledStatus.LastPollTime)
	}

	failedStatus := handler.deviceStatus(failed.Name)
	if failedStatus == nil {
		t.Fatal("a device whose poll failed has no status at all")
	}
	if failedStatus.LastPollTime.IsZero() {
		t.Error("a device whose poll failed reads as never having been polled")
	}
	if failedStatus.LastPollSuccess {
		t.Error("a failed poll was recorded as a success")
	}
	if failedStatus.LastError == "" {
		t.Error("a failed poll recorded no error to show the operator")
	}
}

// TestDeviceStatusForAnUnknownDevice is the bad-input case: a name neither the
// poller nor the tracker has heard of has no status to invent.
func TestDeviceStatusForAnUnknownDevice(t *testing.T) {
	t.Parallel()

	cfg := fastFailConfig()
	p := poller.New(cfg, nil)
	t.Cleanup(func() { _ = p.Stop() })

	handler := NewPollerControlHandler(p, cfg)

	if status := handler.deviceStatus("no-such-device"); status != nil {
		t.Errorf("deviceStatus for an unknown device = %+v, want nil", status)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/poller/devices/no-such-device/status", nil)
	w := httptest.NewRecorder()
	handler.HandleGetDeviceStatus(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d for an unknown device, want 404", w.Code)
	}
}

// TestListDevicesCarriesReachability keeps the fleet view honest: it is where an
// operator looks first, and a device that is down has to be marked there too.
func TestListDevicesCarriesReachability(t *testing.T) {
	t.Parallel()

	cfg := fastFailConfig()
	p := poller.New(cfg, nil)
	t.Cleanup(func() { _ = p.Stop() })

	handler := NewPollerControlHandler(p, cfg)
	device := unreachableDevice(t, "bastion")
	if err := p.AddDevice(t.Context(), device); err != nil {
		t.Fatalf("AddDevice: %v", err)
	}
	awaitUnreachable(t, handler, device.Name)

	req := httptest.NewRequest(http.MethodGet, "/api/poller/devices", nil)
	w := httptest.NewRecorder()
	handler.HandleListDevices(w, req)

	var resp ListDevicesResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding the device list: %v", err)
	}
	if len(resp.Devices) != 1 {
		t.Fatalf("listed %d devices, want 1", len(resp.Devices))
	}
	if resp.Devices[0].Status == nil {
		t.Fatal("the listed device carries no status")
	}
	if !resp.Devices[0].Status.Unreachable {
		t.Error("the fleet listing does not mark an unreachable device as unreachable")
	}
}

// TestApplyReachabilityLeavesAHealthyDeviceAlone is the regression guard on the
// join itself: a reachable device must come back clean no matter what the
// tracker recorded about its last poll, because a failed poll and an
// unreachable device are not the same thing.
func TestApplyReachabilityLeavesAHealthyDeviceAlone(t *testing.T) {
	t.Parallel()

	status := &DeviceStatus{Name: "pdu-03", LastPollSuccess: false, LastError: "one table timed out"}
	applyReachability(status, poller.WorkerStatus{DeviceName: "pdu-03", Interval: time.Minute})

	if status.Unreachable {
		t.Error("a device the poller reports as answering was marked unreachable")
	}
	if status.ConsecutiveFailures != 0 {
		t.Errorf("consecutive_failures = %d for a reachable device, want 0", status.ConsecutiveFailures)
	}
	if status.LastError == "" {
		t.Error("the device's real polling error was overwritten")
	}
}

// TestApplyReachabilityReportsTheIntervalInForce covers a status that came from
// the tracker rather than from neverPolled. The tracker builds its entry from a
// poll result, which carries no interval, so a device that had actually polled
// reported an empty poll_interval while one that never had reported it
// correctly — backwards, and visible on every live device.
func TestApplyReachabilityReportsTheIntervalInForce(t *testing.T) {
	t.Parallel()

	status := &DeviceStatus{Name: "core-01", LastPollSuccess: true}
	applyReachability(status, poller.WorkerStatus{DeviceName: "core-01", Interval: 5 * time.Minute})

	if status.PollInterval != "5m0s" {
		t.Errorf("poll_interval = %q, want the worker's interval %q", status.PollInterval, "5m0s")
	}
}

// A device the poller is not running a worker for keeps whatever the tracker
// knew; applyReachability is only reached when a worker exists, so this pins
// that the interval always comes from the worker rather than being invented.
func TestApplyReachabilityPrefersTheWorkerInterval(t *testing.T) {
	t.Parallel()

	status := &DeviceStatus{Name: "core-01", PollInterval: "30s"}
	applyReachability(status, poller.WorkerStatus{DeviceName: "core-01", Interval: 2 * time.Minute})

	if status.PollInterval != "2m0s" {
		t.Errorf("poll_interval = %q, want the worker's current interval %q", status.PollInterval, "2m0s")
	}
}

// awaitUnreachable waits for the device's first poll to fail. The address it
// polls is refused by the kernel, so this is a handful of milliseconds rather
// than an SNMP timeout.
func awaitUnreachable(t *testing.T, handler *PollerControlHandler, device string) *DeviceStatus {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for {
		status := handler.deviceStatus(device)
		if status != nil && status.Unreachable {
			// The two facts move together, or this helper is a race: a device
			// reported unreachable always says when it will be tried again.
			// They used to be written at two points in the worker's cycle, and
			// a status read between them came back with the zero time here --
			// which is what made TestDeviceStatusReportsAnUnreachableDevice
			// fail once in three runs on a loaded host (#33). Asserted rather
			// than waited out: a regression is a failure, not a slower test.
			if status.NextPollTime.IsZero() {
				t.Fatalf("device %s is reported unreachable with no scheduled retry: %+v", device, status)
			}
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("device %s never reported itself unreachable: %+v", device, status)
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
}
