package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// An unreachable device is one the poller is getting nothing at all from, as
// opposed to one whose last poll failed. The CLI has to show the difference,
// because the two call for different actions: one is a device to go and look
// at, the other is a MIB object to stop asking for.

// unreachableStatus is the poller's view of a device that has stopped
// answering: it has been polled, those polls failed, and the next attempt has
// been pushed out by the backoff.
func unreachableStatus() client.DeviceStatus {
	return client.DeviceStatus{
		Name:                "bastion",
		Address:             "198.51.100.14",
		Connected:           false,
		LastPollTime:        time.Now().Add(-4 * time.Minute),
		LastPollSuccess:     false,
		LastError:           "poller: device bastion answered none of 4 SNMP operations; skipped the remaining 59",
		PollInterval:        "60s",
		Unreachable:         true,
		ConsecutiveFailures: 5,
		NextPollTime:        time.Now().Add(15 * time.Minute),
	}
}

func TestUntilTime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		at   time.Time
		want string
	}{
		{"nothing scheduled", time.Time{}, "unscheduled"},
		{"a retry that is due", time.Now().Add(-time.Second), "now"},
		// A second of headroom so the truncation lands on a whole minute
		// however long the test takes to get here.
		{"a retry in a quarter of an hour", time.Now().Add(15*time.Minute + time.Second), "15m0s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := untilTime(tt.at); got != tt.want {
				t.Errorf("untilTime(%v) = %q, want %q", tt.at, got, tt.want)
			}
		})
	}
}

// TestMonitorHeaderReportsAnUnreachableDevice covers what an operator reads
// when they go looking at one device: how many polls have failed in a row, and
// when the poller will try it next.
func TestMonitorHeaderReportsAnUnreachableDevice(t *testing.T) {
	t.Parallel()

	status := unreachableStatus()
	var out bytes.Buffer

	if err := renderMonitorHeader(&out, &client.Device{Name: "bastion", Address: "198.51.100.14", Port: 161}, &status, nil); err != nil {
		t.Fatalf("renderMonitorHeader: %v", err)
	}

	text := out.String()
	for _, want := range []string{"Unreachable", "5 failed polls", "Next poll"} {
		if !strings.Contains(text, want) {
			t.Errorf("header missing %q:\n%s", want, text)
		}
	}
}

// TestMonitorHeaderSaysNothingAboutAReachableDevice is the other half: a device
// that answers some objects and fails on others has a last error and is
// perfectly healthy, and must not be described as unreachable.
func TestMonitorHeaderSaysNothingAboutAReachableDevice(t *testing.T) {
	t.Parallel()

	status := testDeviceStatus()
	status.LastPollSuccess = false
	status.LastError = "SNMP BULKWALK failed for 1.3.6.1.2.1.2.2: request timeout (after 3 retries)"

	var out bytes.Buffer
	if err := renderMonitorHeader(&out, &client.Device{Name: status.Name}, &status, nil); err != nil {
		t.Fatalf("renderMonitorHeader: %v", err)
	}

	if text := out.String(); strings.Contains(text, "Unreachable") {
		t.Errorf("a device that answers was described as unreachable:\n%s", text)
	}
}

// TestHealthNamesAnUnreachableDevice keeps "last poll 4m ago" from reading like
// a slow poller when it is really a device that stopped answering.
func TestHealthNamesAnUnreachableDevice(t *testing.T) {
	t.Parallel()

	status := unreachableStatus()
	check := pollerCheckFor(t, []client.Device{{ID: 1, Name: "bastion"}}, status)

	if check.State != serviceUp {
		t.Errorf("poller state = %q, want %q: the poller answered, it is the device that is down",
			check.State, serviceUp)
	}
	for _, want := range []string{"bastion", "unreachable", "5 failed polls", "retrying in"} {
		if !strings.Contains(check.Detail, want) {
			t.Errorf("detail %q is missing %q", check.Detail, want)
		}
	}
}

// TestHealthPhrasesADueRetryReadably covers the case a scheduled retry reaches:
// once the next poll time has passed the wait is "now", and a caller that
// hardcoded the preposition rendered "retrying in now".
func TestHealthPhrasesADueRetryReadably(t *testing.T) {
	status := unreachableStatus()
	status.NextPollTime = time.Now().Add(-time.Second)

	check := pollerCheckFor(t, []client.Device{{ID: 1, Name: "bastion"}}, status)

	if strings.Contains(check.Detail, "retrying in now") {
		t.Errorf("detail %q reads as %q", check.Detail, "retrying in now")
	}
	if !strings.Contains(check.Detail, "retrying now") {
		t.Errorf("detail %q does not say the retry is due", check.Detail)
	}
}

// An unscheduled retry must not read as "retrying in unscheduled" either.
func TestHealthPhrasesAnUnscheduledRetryReadably(t *testing.T) {
	status := unreachableStatus()
	status.NextPollTime = time.Time{}

	check := pollerCheckFor(t, []client.Device{{ID: 1, Name: "bastion"}}, status)

	if strings.Contains(check.Detail, "in unscheduled") {
		t.Errorf("detail %q reads as %q", check.Detail, "in unscheduled")
	}
}

// TestHealthSaysNothingAboutAReachableDevice is the regression against reporting
// a healthy device as down.
func TestHealthSaysNothingAboutAReachableDevice(t *testing.T) {
	t.Parallel()

	check := pollerCheckFor(t, oneDevice(), testDeviceStatus())

	if strings.Contains(check.Detail, "unreachable") {
		t.Errorf("detail %q calls an answering device unreachable", check.Detail)
	}
}

// pollerCheckFor runs the poller health probe against a management API serving
// one device and one status.
func pollerCheckFor(t *testing.T, devices []client.Device, status client.DeviceStatus) healthCheck {
	t.Helper()

	server := healthServer{devices: devices, status: &status}
	env, _ := newFakeEnv(t, server.mux(t))

	api, err := env.Client()
	if err != nil {
		t.Fatalf("building the API client: %v", err)
	}

	return checkPoller(t.Context(), api, devices)
}
