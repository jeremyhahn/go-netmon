package api

import (
	"errors"
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/webui/client"
	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// TestFleetStatusADisabledDeviceIsNotAFault is the overview's view of a device
// an operator switched off: the poller does not hold it, by design, so it is
// disabled rather than not_registered and is counted apart from the faults.
func TestFleetStatusADisabledDeviceIsNotAFault(t *testing.T) {
	server := newServerWithPoller(t, tablePoller(map[string]*client.DeviceStatus{}, nil))
	off := false
	if err := server.storage.CreateDevice(t.Context(), &storage.Device{
		Name: "pdu-01", Address: "192.0.2.9", SNMPVersion: "v2c",
		Community: "public", Port: 161, Enabled: &off,
	}); err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}
	mustCreateDevice(t, server.storage, "unregistered-99")

	w, fleet := requestFleetStatus(t.Context(), t, server)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %q)", w.Code, http.StatusOK, w.Body.String())
	}

	disabled := entryFor(t, fleet, "pdu-01")
	if disabled.State != StateDisabled || disabled.Enabled {
		t.Errorf("pdu-01 state = %q, enabled = %v; want %q and false", disabled.State, disabled.Enabled, StateDisabled)
	}
	if disabled.Error != "" {
		t.Errorf("pdu-01 error = %q; a disabled device carries no fault", disabled.Error)
	}
	if missing := entryFor(t, fleet, "unregistered-99"); missing.State != StateNotRegistered || !missing.Enabled {
		t.Errorf("unregistered-99 state = %q, enabled = %v; an enabled device the poller lacks is still a fault",
			missing.State, missing.Enabled)
	}
	if fleet.Summary[StateDisabled] != 1 || fleet.Summary[StateNotRegistered] != 1 {
		t.Errorf("summary = %v, want one disabled and one not_registered", fleet.Summary)
	}
}

func TestNewFleetDeviceStatusDisabled(t *testing.T) {
	off := false
	device := storage.Device{ID: 7, Name: "alpha", Address: "192.0.2.7", Enabled: &off}

	// Disabled needs no answer from the poller: an unreachable poller does not
	// make a switched-off device unknown.
	for name, err := range map[string]error{
		"not held":         &DeviceNotPolledError{Device: "alpha"},
		"poller unreached": &PollerUnavailableError{Device: "alpha", Err: errors.New("connection refused")},
	} {
		entry := newFleetDeviceStatus(device, nil, err, PollingConfig{})
		if entry.State != StateDisabled || entry.Error != "" {
			t.Errorf("%s: state = %q, error = %q; want %q and no error", name, entry.State, entry.Error, StateDisabled)
		}
	}

	// One the poller still reports on is still being polled, and says so.
	status := &client.DeviceStatus{Name: "alpha"}
	if entry := newFleetDeviceStatus(device, status, nil, PollingConfig{}); entry.State != StateNeverPolled {
		t.Errorf("state = %q, want %q for a disabled device the poller still holds", entry.State, StateNeverPolled)
	}
}
