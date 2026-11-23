package storage

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// seconds returns a pointer to an interval, which is how a device that has one
// carries it.
func seconds(n int) *int { return &n }

// newIntervalStore opens a storage rooted in the test's own temp directory.
func newIntervalStore(t *testing.T) *Storage {
	t.Helper()

	store, err := New(filepath.Join(t.TempDir(), "webui.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestValidatePollIntervalAccepts(t *testing.T) {
	tests := []struct {
		name     string
		interval *int
	}{
		{"absent, meaning inherit the global default", nil},
		{"the shortest schedule there is", seconds(MinPollInterval)},
		{"a schedule shorter than the SNMP budget", seconds(10)},
		{"a schedule longer than the SNMP budget", seconds(3600)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidatePollInterval("core-01", tt.interval); err != nil {
				t.Errorf("ValidatePollInterval(%v) = %v, want nil", tt.interval, err)
			}
		})
	}
}

func TestValidatePollIntervalRejects(t *testing.T) {
	tests := []struct {
		name     string
		device   string
		interval int
	}{
		{"zero, which is not a schedule", "core-01", 0},
		{"a negative interval", "core-01", -30},
		{"an unnamed device still reports its value", "", -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePollInterval(tt.device, &tt.interval)
			if err == nil {
				t.Fatalf("ValidatePollInterval(%d) = nil, want an error", tt.interval)
			}

			var invalid *InvalidPollIntervalError
			if !errors.As(err, &invalid) {
				t.Fatalf("error is %T, want *InvalidPollIntervalError", err)
			}
			if invalid.Interval != tt.interval {
				t.Errorf("Interval = %d, want %d", invalid.Interval, tt.interval)
			}

			// The message has to say how to get the default back, because
			// that is what an operator who typed 0 was asking for.
			if !strings.Contains(err.Error(), "inherit the global default") {
				t.Errorf("message %q does not say how to inherit the default", err)
			}
			if tt.device != "" && !strings.Contains(err.Error(), tt.device) {
				t.Errorf("message %q does not name device %q", err, tt.device)
			}
		})
	}
}

func TestPollIntervalOf(t *testing.T) {
	tests := []struct {
		name   string
		device *Device
		want   int
	}{
		{"a device with its own schedule", &Device{PollInterval: seconds(45)}, 45},
		{"a device that inherits", &Device{}, 0},
		{"no device at all", nil, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PollIntervalOf(tt.device); got != tt.want {
				t.Errorf("PollIntervalOf = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestDevicePollIntervalRoundTrip is the storage half of the end-to-end
// promise: an interval an operator set comes back exactly as it was set, and a
// device created without one comes back without one rather than as zero.
func TestDevicePollIntervalRoundTrip(t *testing.T) {
	store := newIntervalStore(t)
	ctx := context.Background()

	explicit := &Device{
		Name: "core-01", Address: "192.0.2.1", SNMPVersion: "v2c",
		Community: "public", PollInterval: seconds(45),
	}
	if err := store.CreateDevice(ctx, explicit); err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}

	inherited := &Device{
		Name: "core-02", Address: "192.0.2.2", SNMPVersion: "v2c", Community: "public",
	}
	if err := store.CreateDevice(ctx, inherited); err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}

	got, err := store.GetDevice(ctx, explicit.ID)
	if err != nil {
		t.Fatalf("GetDevice: %v", err)
	}
	if got.PollInterval == nil {
		t.Fatal("the stored interval came back absent; an explicit schedule must survive storage")
	}
	if *got.PollInterval != 45 {
		t.Errorf("poll interval = %d, want 45", *got.PollInterval)
	}

	got, err = store.GetDevice(ctx, inherited.ID)
	if err != nil {
		t.Fatalf("GetDevice: %v", err)
	}
	if got.PollInterval != nil {
		t.Errorf("poll interval = %d, want absent for a device that never set one", *got.PollInterval)
	}

	// The listing must agree with the individual reads, since a fleet view is
	// built from it.
	devices, err := store.ListDevices(ctx)
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if len(devices) != 2 {
		t.Fatalf("listed %d devices, want 2", len(devices))
	}
	if devices[0].PollInterval == nil || *devices[0].PollInterval != 45 {
		t.Errorf("listed core-01 interval = %v, want 45", devices[0].PollInterval)
	}
	if devices[1].PollInterval != nil {
		t.Errorf("listed core-02 interval = %v, want absent", *devices[1].PollInterval)
	}
}

// TestDeviceStoredBeforePollIntervalExisted is the compatibility case: the
// devices already in a live database were written without the field, and they
// have to keep working and read as inheriting rather than as a zero schedule.
func TestDeviceStoredBeforePollIntervalExisted(t *testing.T) {
	store := newIntervalStore(t)
	ctx := context.Background()

	// Exactly the JSON the previous version of this type produced.
	legacy := []byte(`{"id":1,"name":"pdu-03","address":"192.0.2.3",` +
		`"snmp_version":"v2c","community":"public","port":161,` +
		`"created_at":"2025-01-01T00:00:00Z"}`)

	var device Device
	if err := json.Unmarshal(legacy, &device); err != nil {
		t.Fatalf("decoding a device written before the field existed: %v", err)
	}
	if device.PollInterval != nil {
		t.Fatalf("poll interval = %d, want absent", *device.PollInterval)
	}
	if device.Name != "pdu-03" || device.Port != 161 {
		t.Fatalf("the rest of the record did not survive: %+v", device)
	}

	// It must still be storable and re-readable.
	stored := &Device{
		Name: device.Name, Address: device.Address,
		SNMPVersion: device.SNMPVersion, Community: device.Community, Port: device.Port,
	}
	if err := store.CreateDevice(ctx, stored); err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}

	// And it must re-encode without a poll_interval key at all, so a client
	// can tell "inherits" from "explicitly some number".
	encoded, err := json.Marshal(stored)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(encoded), "poll_interval") {
		t.Errorf("encoded %s; a device that inherits must not carry a poll_interval", encoded)
	}
}

// TestUpdateDeviceChangesOnlyTheInterval is the regression for the class of bug
// where a write meant to change one field quietly resets the others.
func TestUpdateDeviceChangesOnlyTheInterval(t *testing.T) {
	store := newIntervalStore(t)
	ctx := context.Background()

	device := &Device{
		Name: "core-01", Address: "192.0.2.1", SNMPVersion: "v3",
		Community: "secret", Port: 1161,
	}
	if err := store.CreateDevice(ctx, device); err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}
	createdAt := device.CreatedAt

	device.PollInterval = seconds(120)
	if err := store.UpdateDevice(ctx, device); err != nil {
		t.Fatalf("UpdateDevice: %v", err)
	}

	got, err := store.GetDevice(ctx, device.ID)
	if err != nil {
		t.Fatalf("GetDevice: %v", err)
	}
	if got.PollInterval == nil || *got.PollInterval != 120 {
		t.Errorf("poll interval = %v, want 120", got.PollInterval)
	}
	if got.Name != "core-01" || got.Address != "192.0.2.1" ||
		got.SNMPVersion != "v3" || got.Community != "secret" || got.Port != 1161 {
		t.Errorf("a poll interval change disturbed another field: %+v", got)
	}
	if !got.CreatedAt.Equal(createdAt) {
		t.Errorf("created_at = %v, want %v", got.CreatedAt, createdAt)
	}

	// Clearing it must hand the device back to the global default, and must
	// leave everything else alone in the same way.
	got.PollInterval = nil
	if clearErr := store.UpdateDevice(ctx, got); clearErr != nil {
		t.Fatalf("UpdateDevice clearing the interval: %v", clearErr)
	}
	cleared, err := store.GetDevice(ctx, device.ID)
	if err != nil {
		t.Fatalf("GetDevice: %v", err)
	}
	if cleared.PollInterval != nil {
		t.Errorf("poll interval = %d, want absent after being cleared", *cleared.PollInterval)
	}
	if cleared.Community != "secret" || cleared.Port != 1161 {
		t.Errorf("clearing the interval disturbed another field: %+v", cleared)
	}
}

// TestStorageRefusesAnUnschedulableInterval keeps the invariant at the durable
// boundary: whatever a caller believes, nothing that is not a schedule is
// written.
func TestStorageRefusesAnUnschedulableInterval(t *testing.T) {
	store := newIntervalStore(t)
	ctx := context.Background()

	device := &Device{
		Name: "core-01", Address: "192.0.2.1", SNMPVersion: "v2c",
		Community: "public", PollInterval: seconds(0),
	}

	err := store.CreateDevice(ctx, device)
	var invalid *InvalidPollIntervalError
	if !errors.As(err, &invalid) {
		t.Fatalf("CreateDevice error = %v (%T), want *InvalidPollIntervalError", err, err)
	}
	if _, lookupErr := store.GetDeviceByName(ctx, "core-01"); lookupErr == nil {
		t.Error("the rejected device was stored anyway")
	}

	// The same rule on the update path, against a device that is already good.
	device.PollInterval = seconds(30)
	if createErr := store.CreateDevice(ctx, device); createErr != nil {
		t.Fatalf("CreateDevice: %v", createErr)
	}

	device.PollInterval = seconds(-1)
	if updateErr := store.UpdateDevice(ctx, device); !errors.As(updateErr, &invalid) {
		t.Fatalf("UpdateDevice error = %v (%T), want *InvalidPollIntervalError", updateErr, updateErr)
	}

	got, err := store.GetDevice(ctx, device.ID)
	if err != nil {
		t.Fatalf("GetDevice: %v", err)
	}
	if got.PollInterval == nil || *got.PollInterval != 30 {
		t.Errorf("poll interval = %v, want the stored 30 to survive a rejected update", got.PollInterval)
	}
}
