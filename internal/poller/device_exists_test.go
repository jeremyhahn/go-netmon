package poller

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/config"
)

// TestAddDeviceReportsADuplicateAsAType pins the error a second AddDevice under
// the same name produces.
//
// It was a formatted string, so a caller could only tell a duplicate from a
// real failure by matching on the message. The control API answered 500 for
// both, and the web UI's client -- which retries a 5xx and not a 4xx -- turned
// each one into three. The type is what lets both stop guessing.
func TestAddDeviceReportsADuplicateAsAType(t *testing.T) {
	p := New(getTestConfig(t), nil)
	defer func() { _ = p.Stop() }()

	address := unreachableSNMPAddress(t)
	device := func() *config.Device {
		return &config.Device{
			Name:         "rtr-01",
			Address:      address,
			SNMPVersion:  "2c",
			Community:    "public",
			PollInterval: 60,
			Profile:      "default",
			Enabled:      true,
		}
	}

	if err := p.AddDevice(context.Background(), device()); err != nil {
		t.Fatalf("first AddDevice: %v", err)
	}

	err := p.AddDevice(context.Background(), device())

	var exists *DeviceExistsError
	if !errors.As(err, &exists) {
		t.Fatalf("err = %v, want a *DeviceExistsError", err)
	}
	if exists.Device != "rtr-01" {
		t.Errorf("Device = %q, want %q", exists.Device, "rtr-01")
	}
	if !strings.Contains(exists.Error(), "rtr-01") {
		t.Errorf("Error() = %q, which does not name the device", exists.Error())
	}

	// The refusal is the point: the live worker is left as it was, not replaced.
	if got := len(p.GetWorkerStatus()); got != 1 {
		t.Errorf("workers = %d, want 1", got)
	}
}

// TestAddDeviceDoesNotReportOtherFailuresAsDuplicates is the bad-input half.
// A failure that is not a name collision must not be mistakable for one, or the
// caller's "already registered, carry on" would swallow a device that never
// got registered at all.
func TestAddDeviceDoesNotReportOtherFailuresAsDuplicates(t *testing.T) {
	tests := []struct {
		name   string
		device *config.Device
		stop   bool
	}{
		{
			name:   "an invalid device",
			device: &config.Device{Name: "rtr-02", SNMPVersion: "2c", Community: "public"},
		},
		{
			name: "a poller that has stopped",
			device: &config.Device{
				Name: "rtr-03", Address: "192.0.2.10:161", SNMPVersion: "2c",
				Community: "public", PollInterval: 60, Profile: "default", Enabled: true,
			},
			stop: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := New(getTestConfig(t), nil)
			defer func() { _ = p.Stop() }()

			if tt.stop {
				if err := p.Stop(); err != nil {
					t.Fatalf("Stop: %v", err)
				}
			}

			err := p.AddDevice(context.Background(), tt.device)
			if err == nil {
				t.Fatal("AddDevice = nil, want an error")
			}

			var exists *DeviceExistsError
			if errors.As(err, &exists) {
				t.Errorf("err = %v, which reads as a duplicate; it is not one", err)
			}
		})
	}
}
