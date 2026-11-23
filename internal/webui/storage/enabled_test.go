package storage

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cockroachdb/pebble"
)

func flag(v bool) *bool { return &v }

func TestEnabledOf(t *testing.T) {
	cases := []struct {
		name   string
		device *Device
		want   bool
	}{
		{"no device", nil, false},
		{"absent", &Device{}, true},
		{"explicitly enabled", &Device{Enabled: flag(true)}, true},
		{"disabled", &Device{Enabled: flag(false)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EnabledOf(tc.device); got != tc.want {
				t.Errorf("EnabledOf = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestDeviceStoredBeforeEnabledExistedIsEnabled is the compatibility
// guarantee: a record an earlier version wrote, read back from the store
// itself, is a device that is polled.
func TestDeviceStoredBeforeEnabledExistedIsEnabled(t *testing.T) {
	store := newIntervalStore(t)

	// Exactly the JSON the previous version of this type produced.
	legacy := []byte(`{"id":7,"name":"pdu-03","address":"192.0.2.3",` +
		`"snmp_version":"v2c","community":"public","port":161,` +
		`"created_at":"2025-01-01T00:00:00Z"}`)
	if err := store.db.Set([]byte(PrefixDevice+formatID(7)), legacy, pebble.Sync); err != nil {
		t.Fatalf("writing the legacy record: %v", err)
	}

	got, err := store.GetDevice(context.Background(), 7)
	if err != nil {
		t.Fatalf("GetDevice: %v", err)
	}
	if got.Enabled != nil {
		t.Errorf("enabled = %v, want absent", *got.Enabled)
	}
	if !EnabledOf(got) {
		t.Error("a device stored before the field existed reads as disabled")
	}
}

func TestDeviceEnabledRoundTrip(t *testing.T) {
	store := newIntervalStore(t)
	ctx := context.Background()

	device := &Device{
		Name: "pdu-01", Address: "192.0.2.9", SNMPVersion: "v2c",
		Community: "public", Port: 161, Enabled: flag(false),
	}
	if err := store.CreateDevice(ctx, device); err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}

	got, err := store.GetDevice(ctx, device.ID)
	if err != nil {
		t.Fatalf("GetDevice: %v", err)
	}
	if EnabledOf(got) {
		t.Fatal("a device stored disabled reads back enabled")
	}

	got.Enabled = flag(true)
	if err = store.UpdateDevice(ctx, got); err != nil {
		t.Fatalf("UpdateDevice: %v", err)
	}
	listed, err := store.ListDevices(ctx)
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if len(listed) != 1 || !EnabledOf(&listed[0]) {
		t.Errorf("listed = %+v, want the one device, enabled", listed)
	}

	// A device that never said stays silent on the wire.
	encoded, err := json.Marshal(&Device{Name: "x"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var keys map[string]any
	if err := json.Unmarshal(encoded, &keys); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := keys["enabled"]; ok {
		t.Errorf("encoded %s; a device that never said must not carry enabled", encoded)
	}
}

// TestConfigRoundTripKeepsADisabledDevice guards the export/import path: a
// device an operator switched off must not come back from a restore polling.
func TestConfigRoundTripKeepsADisabledDevice(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	for _, d := range []*Device{
		{Name: "pdu-01", Address: "192.0.2.9", SNMPVersion: "v2c", Community: "public", Port: 161, Enabled: flag(false)},
		{Name: "pdu-02", Address: "192.0.2.10", SNMPVersion: "v2c", Community: "public", Port: 161},
	} {
		if err := store.CreateDevice(ctx, d); err != nil {
			t.Fatalf("CreateDevice(%s): %v", d.Name, err)
		}
	}

	original := exportConfig(t, store, ConfigExportOptions{})
	if _, err := store.ImportConfig(ctx, &Config{SchemaVersion: ConfigSchemaVersion}); err != nil {
		t.Fatalf("ImportConfig(empty): %v", err)
	}
	if _, err := store.ImportConfig(ctx, original); err != nil {
		t.Fatalf("ImportConfig(original): %v", err)
	}

	for name, want := range map[string]bool{"pdu-01": false, "pdu-02": true} {
		got, err := store.GetDeviceByName(ctx, name)
		if err != nil {
			t.Fatalf("GetDeviceByName(%s): %v", name, err)
		}
		if EnabledOf(got) != want {
			t.Errorf("%s enabled = %v after a round trip, want %v", name, EnabledOf(got), want)
		}
	}
}
