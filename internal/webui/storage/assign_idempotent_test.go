package storage

import "testing"

// Assigning a MIB that is already assigned used to overwrite the join row with
// the legacy sentinel byte, which decodes as "enabled, no filters" — so a
// second assign silently discarded a configured assignment's disable state and
// its include/exclude filters. Assignment is idempotent, so re-assigning must
// leave the existing row untouched.

func TestAssignMIBToDeviceKeepsExistingAssignment(t *testing.T) {
	ctx := t.Context()
	s := newCatalogStore(t)

	device := mustCreateDevice(t, s, "core-01")
	mib := mustCreateMIB(t, s, "IF-MIB")
	if err := s.AssignMIBToDevice(ctx, device.ID, mib.ID); err != nil {
		t.Fatalf("AssignMIBToDevice: %v", err)
	}

	want := MIBAssignment{
		Enabled:      false,
		IncludeNames: []string{"^ifIn"},
		ExcludeOIDs:  []string{`\.99$`},
		MaxOIDs:      25,
	}
	if err := s.SetDeviceMIBAssignment(ctx, device.ID, mib.ID, want); err != nil {
		t.Fatalf("SetDeviceMIBAssignment: %v", err)
	}

	// The second assign is the regression: it must not reset anything.
	if err := s.AssignMIBToDevice(ctx, device.ID, mib.ID); err != nil {
		t.Fatalf("re-assign: %v", err)
	}

	got, err := s.DeviceMIBAssignment(ctx, device.ID, mib.ID)
	if err != nil {
		t.Fatalf("DeviceMIBAssignment: %v", err)
	}
	if got.Enabled {
		t.Error("re-assigning re-enabled a deliberately disabled MIB")
	}
	if got.MaxOIDs != want.MaxOIDs {
		t.Errorf("MaxOIDs = %d, want %d", got.MaxOIDs, want.MaxOIDs)
	}
	if len(got.IncludeNames) != 1 || got.IncludeNames[0] != "^ifIn" {
		t.Errorf("IncludeNames = %v, want %v", got.IncludeNames, want.IncludeNames)
	}
	if len(got.ExcludeOIDs) != 1 || got.ExcludeOIDs[0] != `\.99$` {
		t.Errorf("ExcludeOIDs = %v, want %v", got.ExcludeOIDs, want.ExcludeOIDs)
	}
}

func TestAssignMIBToProfileKeepsExistingAssignment(t *testing.T) {
	ctx := t.Context()
	s := newCatalogStore(t)

	profile := mustCreateProfile(t, s, "core-routers")
	mib := mustCreateMIB(t, s, "IF-MIB")
	if err := s.AssignMIBToProfile(ctx, profile.ID, mib.ID); err != nil {
		t.Fatalf("AssignMIBToProfile: %v", err)
	}

	want := MIBAssignment{Enabled: false, IncludeNames: []string{"^ifOut"}, MaxOIDs: 10}
	if err := s.SetProfileMIBAssignment(ctx, profile.ID, mib.ID, want); err != nil {
		t.Fatalf("SetProfileMIBAssignment: %v", err)
	}

	if err := s.AssignMIBToProfile(ctx, profile.ID, mib.ID); err != nil {
		t.Fatalf("re-assign: %v", err)
	}

	got, err := s.ProfileMIBAssignment(ctx, profile.ID, mib.ID)
	if err != nil {
		t.Fatalf("ProfileMIBAssignment: %v", err)
	}
	if got.Enabled {
		t.Error("re-assigning re-enabled a deliberately disabled MIB")
	}
	if got.MaxOIDs != want.MaxOIDs {
		t.Errorf("MaxOIDs = %d, want %d", got.MaxOIDs, want.MaxOIDs)
	}
	if len(got.IncludeNames) != 1 || got.IncludeNames[0] != "^ifOut" {
		t.Errorf("IncludeNames = %v, want %v", got.IncludeNames, want.IncludeNames)
	}
}

// A first assignment must still start from the permissive default, so the fix
// above cannot be mistaken for "never write the row".
func TestAssignMIBToDeviceFirstAssignmentIsEnabledAndUnfiltered(t *testing.T) {
	ctx := t.Context()
	s := newCatalogStore(t)

	device := mustCreateDevice(t, s, "core-02")
	mib := mustCreateMIB(t, s, "TCP-MIB")
	if err := s.AssignMIBToDevice(ctx, device.ID, mib.ID); err != nil {
		t.Fatalf("AssignMIBToDevice: %v", err)
	}

	got, err := s.DeviceMIBAssignment(ctx, device.ID, mib.ID)
	if err != nil {
		t.Fatalf("DeviceMIBAssignment: %v", err)
	}
	if !got.Enabled || got.HasFilters() {
		t.Errorf("assignment = %+v, want enabled and unfiltered", got)
	}
}
