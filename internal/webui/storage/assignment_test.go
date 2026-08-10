package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/cockroachdb/pebble"
)

// assignmentFixture creates a device, a profile and two MIBs, and assigns both
// MIBs to each.
func assignmentFixture(t *testing.T) (*Storage, func(), int64, int64, []int64) {
	t.Helper()

	store, cleanup := setupTestDB(t)
	ctx := context.Background()

	device := &Device{Name: "core-01", Address: "198.51.100.252", SNMPVersion: "2c", Port: 161}
	if err := store.CreateDevice(ctx, device); err != nil {
		cleanup()
		t.Fatalf("create device: %v", err)
	}

	profile := &DeviceProfile{Name: "core-routers"}
	if err := store.CreateDeviceProfile(ctx, profile); err != nil {
		cleanup()
		t.Fatalf("create profile: %v", err)
	}

	names := []string{"IF-MIB", "CISCO-PROCESS-MIB"}
	mibIDs := make([]int64, 0, len(names))
	for _, name := range names {
		m := &MIB{Name: name, FilePath: "/tmp/" + name}
		if err := store.CreateMIB(ctx, m); err != nil {
			cleanup()
			t.Fatalf("create MIB %s: %v", name, err)
		}
		mibIDs = append(mibIDs, m.ID)

		if err := store.AssignMIBToDevice(ctx, device.ID, m.ID); err != nil {
			cleanup()
			t.Fatalf("assign %s to device: %v", name, err)
		}
		if err := store.AssignMIBToProfile(ctx, profile.ID, m.ID); err != nil {
			cleanup()
			t.Fatalf("add %s to profile: %v", name, err)
		}
	}
	return store, cleanup, device.ID, profile.ID, mibIDs
}

// TestAssignmentDefaultsToEnabled verifies that a freshly created assignment
// polls the whole module, which is what assignment meant before filters existed.
func TestAssignmentDefaultsToEnabled(t *testing.T) {
	store, cleanup, deviceID, profileID, mibIDs := assignmentFixture(t)
	defer cleanup()

	ctx := context.Background()

	got, err := store.DeviceMIBAssignment(ctx, deviceID, mibIDs[0])
	if err != nil {
		t.Fatalf("DeviceMIBAssignment: %v", err)
	}
	if !got.Enabled {
		t.Error("a new device assignment should be enabled")
	}
	if got.HasFilters() {
		t.Errorf("a new assignment should have no filters, got %+v", got)
	}

	gotProfile, err := store.ProfileMIBAssignment(ctx, profileID, mibIDs[0])
	if err != nil {
		t.Fatalf("ProfileMIBAssignment: %v", err)
	}
	if !gotProfile.Enabled {
		t.Error("a new profile assignment should be enabled")
	}
}

// TestAssignmentLegacyRowDecodesAsEnabled is the compatibility guarantee: rows
// written before assignments carried settings hold a sentinel byte, and must
// keep polling exactly as they did rather than silently switching off.
func TestAssignmentLegacyRowDecodesAsEnabled(t *testing.T) {
	store, cleanup, deviceID, _, mibIDs := assignmentFixture(t)
	defer cleanup()

	// Rewrite the join row in the pre-existing format.
	key := []byte(PrefixDevMIB + formatID(deviceID) + ":" + formatID(mibIDs[0]))
	if err := store.db.Set(key, []byte{1}, pebble.Sync); err != nil {
		t.Fatalf("write legacy row: %v", err)
	}

	got, err := store.DeviceMIBAssignment(context.Background(), deviceID, mibIDs[0])
	if err != nil {
		t.Fatalf("DeviceMIBAssignment: %v", err)
	}
	if !got.Enabled {
		t.Error("a legacy assignment row must decode as enabled")
	}
	if got.HasFilters() {
		t.Errorf("a legacy assignment row must carry no filters, got %+v", got)
	}
}

func TestSetAndReadDeviceAssignment(t *testing.T) {
	store, cleanup, deviceID, _, mibIDs := assignmentFixture(t)
	defer cleanup()

	ctx := context.Background()
	want := MIBAssignment{
		Enabled:      true,
		IncludeNames: []string{"^ifHC.*", "^ifOperStatus$"},
		ExcludeNames: []string{"ifSpecific"},
		MaxOIDs:      50,
	}

	if err := store.SetDeviceMIBAssignment(ctx, deviceID, mibIDs[0], want); err != nil {
		t.Fatalf("SetDeviceMIBAssignment: %v", err)
	}

	got, err := store.DeviceMIBAssignment(ctx, deviceID, mibIDs[0])
	if err != nil {
		t.Fatalf("DeviceMIBAssignment: %v", err)
	}
	if !got.Enabled || got.MaxOIDs != 50 {
		t.Errorf("got %+v, want enabled with MaxOIDs 50", got)
	}
	if len(got.IncludeNames) != 2 || got.IncludeNames[0] != "^ifHC.*" {
		t.Errorf("IncludeNames = %v", got.IncludeNames)
	}
	if len(got.ExcludeNames) != 1 || got.ExcludeNames[0] != "ifSpecific" {
		t.Errorf("ExcludeNames = %v", got.ExcludeNames)
	}
	if !got.HasFilters() {
		t.Error("HasFilters should report true for a filtered assignment")
	}

	// The other MIB must be untouched.
	other, err := store.DeviceMIBAssignment(ctx, deviceID, mibIDs[1])
	if err != nil {
		t.Fatalf("DeviceMIBAssignment: %v", err)
	}
	if other.HasFilters() {
		t.Errorf("filters leaked to a sibling assignment: %+v", other)
	}
}

// TestDisablingKeepsTheAssignment verifies that pausing a MIB does not detach
// it, so its filters survive being switched off and back on.
func TestDisablingKeepsTheAssignment(t *testing.T) {
	store, cleanup, deviceID, _, mibIDs := assignmentFixture(t)
	defer cleanup()

	ctx := context.Background()
	paused := MIBAssignment{Enabled: false, ExcludeNames: []string{"ifSpecific"}}
	if err := store.SetDeviceMIBAssignment(ctx, deviceID, mibIDs[0], paused); err != nil {
		t.Fatalf("SetDeviceMIBAssignment: %v", err)
	}

	got, err := store.DeviceMIBAssignment(ctx, deviceID, mibIDs[0])
	if err != nil {
		t.Fatalf("DeviceMIBAssignment: %v", err)
	}
	if got.Enabled {
		t.Error("assignment should be disabled")
	}
	if len(got.ExcludeNames) != 1 {
		t.Errorf("filters must survive being disabled, got %+v", got)
	}

	// It must still be listed as assigned, so the UI can show it as paused.
	mibs, err := store.GetDeviceMIBs(ctx, deviceID)
	if err != nil {
		t.Fatalf("GetDeviceMIBs: %v", err)
	}
	if len(mibs) != 2 {
		t.Errorf("got %d assigned MIBs, want 2 — disabling must not detach", len(mibs))
	}
}

func TestSetAssignmentForUnassignedMIB(t *testing.T) {
	store, cleanup, deviceID, _, _ := assignmentFixture(t)
	defer cleanup()

	ctx := context.Background()
	// Configuring a MIB that was never assigned must fail rather than quietly
	// attaching it and starting collection.
	err := store.SetDeviceMIBAssignment(ctx, deviceID, 9999, MIBAssignment{Enabled: true})
	if err == nil {
		t.Fatal("expected an error for an unassigned MIB")
	}

	var notFound *AssignmentNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("got %T, want *AssignmentNotFoundError", err)
	}
	if notFound.MIBID != 9999 {
		t.Errorf("MIBID = %d, want 9999", notFound.MIBID)
	}
}

func TestReadAssignmentForUnassignedMIB(t *testing.T) {
	store, cleanup, deviceID, _, _ := assignmentFixture(t)
	defer cleanup()

	_, err := store.DeviceMIBAssignment(context.Background(), deviceID, 9999)
	var notFound *AssignmentNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("got %T, want *AssignmentNotFoundError", err)
	}
}

func TestListAssignments(t *testing.T) {
	store, cleanup, deviceID, profileID, mibIDs := assignmentFixture(t)
	defer cleanup()

	ctx := context.Background()
	if err := store.SetDeviceMIBAssignment(ctx, deviceID, mibIDs[1],
		MIBAssignment{Enabled: false}); err != nil {
		t.Fatalf("SetDeviceMIBAssignment: %v", err)
	}

	got, err := store.DeviceMIBAssignments(ctx, deviceID)
	if err != nil {
		t.Fatalf("DeviceMIBAssignments: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d assignments, want 2", len(got))
	}
	if !got[mibIDs[0]].Enabled {
		t.Error("first MIB should still be enabled")
	}
	if got[mibIDs[1]].Enabled {
		t.Error("second MIB should be disabled")
	}

	profileAssignments, err := store.ProfileMIBAssignments(ctx, profileID)
	if err != nil {
		t.Fatalf("ProfileMIBAssignments: %v", err)
	}
	if len(profileAssignments) != 2 {
		t.Errorf("got %d profile assignments, want 2", len(profileAssignments))
	}
}

// TestListAssignmentsIsScopedToOneOwner guards the key scan: device 1's
// assignments must not bleed into device 2's.
func TestListAssignmentsIsScopedToOneOwner(t *testing.T) {
	store, cleanup, deviceID, _, mibIDs := assignmentFixture(t)
	defer cleanup()

	ctx := context.Background()
	second := &Device{Name: "core-02", Address: "198.51.100.253", SNMPVersion: "2c", Port: 161}
	if err := store.CreateDevice(ctx, second); err != nil {
		t.Fatalf("create device: %v", err)
	}
	if err := store.AssignMIBToDevice(ctx, second.ID, mibIDs[0]); err != nil {
		t.Fatalf("assign: %v", err)
	}

	first, err := store.DeviceMIBAssignments(ctx, deviceID)
	if err != nil {
		t.Fatalf("DeviceMIBAssignments: %v", err)
	}
	if len(first) != 2 {
		t.Errorf("device %d has %d assignments, want 2", deviceID, len(first))
	}

	other, err := store.DeviceMIBAssignments(ctx, second.ID)
	if err != nil {
		t.Fatalf("DeviceMIBAssignments: %v", err)
	}
	if len(other) != 1 {
		t.Errorf("device %d has %d assignments, want 1", second.ID, len(other))
	}
}

func TestDecodeMIBAssignmentBadInput(t *testing.T) {
	tests := []struct {
		name  string
		value []byte
	}{
		{"empty", nil},
		{"legacy sentinel", []byte{1}},
		{"not json", []byte("garbage")},
		{"truncated json", []byte(`{"enabled":`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Every unreadable value must fall back to the safe default rather
			// than silently disabling a MIB that was previously polling.
			got := decodeMIBAssignment(tt.value)
			if !got.Enabled {
				t.Errorf("decodeMIBAssignment(%q) disabled polling; want the enabled default", tt.value)
			}
		})
	}
}
