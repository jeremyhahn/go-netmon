package storage

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// newCatalogStore opens a store in a temporary directory.
func newCatalogStore(t *testing.T) *Storage {
	t.Helper()

	store, err := New(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	return store
}

// mustCreateMIB stores a MIB and returns it.
func mustCreateMIB(t *testing.T, s *Storage, name string) *MIB {
	t.Helper()

	m := &MIB{Name: name, FilePath: "/mibs/" + name + ".mib", ObjectCount: 3}
	if err := s.CreateMIB(context.Background(), m); err != nil {
		t.Fatalf("CreateMIB(%s): %v", name, err)
	}
	return m
}

// mustCreateProfile stores a profile and returns it.
func mustCreateProfile(t *testing.T, s *Storage, name string) *DeviceProfile {
	t.Helper()

	p := &DeviceProfile{Name: name, Description: name + " profile"}
	if err := s.CreateDeviceProfile(context.Background(), p); err != nil {
		t.Fatalf("CreateDeviceProfile(%s): %v", name, err)
	}
	return p
}

// mustCreateDevice stores a device and returns it.
func mustCreateDevice(t *testing.T, s *Storage, name string) *Device {
	t.Helper()

	d := &Device{Name: name, Address: "192.0.2.1", SNMPVersion: "v2c", Community: "public"}
	if err := s.CreateDevice(context.Background(), d); err != nil {
		t.Fatalf("CreateDevice(%s): %v", name, err)
	}
	return d
}

func TestGetMIBByName(t *testing.T) {
	ctx := t.Context()
	s := newCatalogStore(t)
	created := mustCreateMIB(t, s, "IF-MIB")

	got, err := s.GetMIBByName(ctx, "IF-MIB")
	if err != nil {
		t.Fatalf("GetMIBByName: %v", err)
	}
	if got.ID != created.ID || got.Name != "IF-MIB" {
		t.Errorf("got %+v, want the created MIB %+v", got, created)
	}
}

func TestGetMIBByNameRejectsUnknownName(t *testing.T) {
	s := newCatalogStore(t)

	got, err := s.GetMIBByName(t.Context(), "NO-SUCH-MIB")
	if err == nil {
		t.Fatal("GetMIBByName found a MIB that was never stored")
	}
	if got != nil {
		t.Errorf("got %+v alongside an error", got)
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want it to say the MIB was not found", err)
	}
}

func TestListMIBs(t *testing.T) {
	ctx := t.Context()
	s := newCatalogStore(t)

	if mibs, err := s.ListMIBs(ctx); err != nil || len(mibs) != 0 {
		t.Fatalf("ListMIBs on an empty store = %v, %v; want no MIBs and no error", mibs, err)
	}

	for _, name := range []string{"IF-MIB", "TCP-MIB", "UDP-MIB"} {
		mustCreateMIB(t, s, name)
	}

	mibs, err := s.ListMIBs(ctx)
	if err != nil {
		t.Fatalf("ListMIBs: %v", err)
	}
	if len(mibs) != 3 {
		t.Fatalf("ListMIBs returned %d MIBs, want 3", len(mibs))
	}

	seen := make(map[string]bool, len(mibs))
	for _, m := range mibs {
		seen[m.Name] = true
	}
	for _, name := range []string{"IF-MIB", "TCP-MIB", "UDP-MIB"} {
		if !seen[name] {
			t.Errorf("ListMIBs is missing %s", name)
		}
	}
}

func TestUpdateMIB(t *testing.T) {
	ctx := t.Context()
	s := newCatalogStore(t)
	m := mustCreateMIB(t, s, "IF-MIB")
	imported := m.ImportedAt

	m.Name = "IF-MIB-V2"
	m.Description = "renamed"
	m.ObjectCount = 99
	if err := s.UpdateMIB(ctx, m); err != nil {
		t.Fatalf("UpdateMIB: %v", err)
	}

	got, err := s.GetMIB(ctx, m.ID)
	if err != nil {
		t.Fatalf("GetMIB: %v", err)
	}
	if got.Name != "IF-MIB-V2" || got.Description != "renamed" || got.ObjectCount != 99 {
		t.Errorf("got %+v, want the updated fields", got)
	}
	if !got.ImportedAt.Equal(imported) {
		t.Errorf("ImportedAt = %v, want the original %v: a rename is not a re-import", got.ImportedAt, imported)
	}

	// The name index must follow the rename in both directions.
	if _, err := s.GetMIBByName(ctx, "IF-MIB-V2"); err != nil {
		t.Errorf("the new name does not resolve: %v", err)
	}
	if _, err := s.GetMIBByName(ctx, "IF-MIB"); err == nil {
		t.Error("the old name still resolves after a rename")
	}
}

func TestUpdateMIBRejectsBadInput(t *testing.T) {
	ctx := t.Context()
	s := newCatalogStore(t)
	existing := mustCreateMIB(t, s, "IF-MIB")
	other := mustCreateMIB(t, s, "TCP-MIB")

	t.Run("unknown ID", func(t *testing.T) {
		err := s.UpdateMIB(ctx, &MIB{ID: 4242, Name: "GHOST-MIB"})
		if err == nil {
			t.Fatal("UpdateMIB accepted a MIB that was never stored")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("error = %q, want it to say the MIB was not found", err)
		}
	})

	t.Run("renaming onto a name in use", func(t *testing.T) {
		clash := *other
		clash.Name = existing.Name
		err := s.UpdateMIB(ctx, &clash)
		if err == nil {
			t.Fatal("UpdateMIB renamed a MIB onto a name already in use")
		}
		if !strings.Contains(err.Error(), existing.Name) {
			t.Errorf("error = %q, want it to name the conflict", err)
		}

		// The clash must leave both records as they were.
		if got, getErr := s.GetMIBByName(ctx, other.Name); getErr != nil || got.ID != other.ID {
			t.Errorf("the rejected rename disturbed %s: %+v, %v", other.Name, got, getErr)
		}
	})
}

func TestUpdateMIBTags(t *testing.T) {
	ctx := t.Context()
	s := newCatalogStore(t)
	m := mustCreateMIB(t, s, "IF-MIB")

	if err := s.UpdateMIBTags(ctx, m.ID, []string{"interfaces", "standard"}); err != nil {
		t.Fatalf("UpdateMIBTags: %v", err)
	}

	got, err := s.GetMIB(ctx, m.ID)
	if err != nil {
		t.Fatalf("GetMIB: %v", err)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "interfaces" || got.Tags[1] != "standard" {
		t.Errorf("Tags = %v, want [interfaces standard]", got.Tags)
	}

	// Clearing tags must be possible, not just adding them.
	if clearErr := s.UpdateMIBTags(ctx, m.ID, nil); clearErr != nil {
		t.Fatalf("UpdateMIBTags clearing: %v", clearErr)
	}
	if got, err = s.GetMIB(ctx, m.ID); err != nil || len(got.Tags) != 0 {
		t.Errorf("Tags = %v (%v), want none", got.Tags, err)
	}
}

func TestUpdateMIBTagsRejectsUnknownMIB(t *testing.T) {
	s := newCatalogStore(t)

	err := s.UpdateMIBTags(t.Context(), 4242, []string{"x"})
	if err == nil {
		t.Fatal("UpdateMIBTags accepted a MIB that was never stored")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want it to say the MIB was not found", err)
	}
}

// TestDeleteMIBRemovesItsAssociations keeps a deleted MIB from leaving join
// rows that would later resolve to nothing.
func TestDeleteMIBRemovesItsAssociations(t *testing.T) {
	ctx := t.Context()
	s := newCatalogStore(t)

	m := mustCreateMIB(t, s, "IF-MIB")
	keep := mustCreateMIB(t, s, "TCP-MIB")
	device := mustCreateDevice(t, s, "bastion")
	profile := mustCreateProfile(t, s, "switches")

	for _, mib := range []*MIB{m, keep} {
		if err := s.AssignMIBToDevice(ctx, device.ID, mib.ID); err != nil {
			t.Fatalf("AssignMIBToDevice: %v", err)
		}
		if err := s.AssignMIBToProfile(ctx, profile.ID, mib.ID); err != nil {
			t.Fatalf("AssignMIBToProfile: %v", err)
		}
	}

	if err := s.DeleteMIB(ctx, m.ID); err != nil {
		t.Fatalf("DeleteMIB: %v", err)
	}

	if _, err := s.GetMIB(ctx, m.ID); err == nil {
		t.Error("the deleted MIB still resolves by ID")
	}
	if _, err := s.GetMIBByName(ctx, "IF-MIB"); err == nil {
		t.Error("the deleted MIB still resolves by name")
	}

	deviceMIBs, err := s.GetDeviceMIBs(ctx, device.ID)
	if err != nil {
		t.Fatalf("GetDeviceMIBs: %v", err)
	}
	if len(deviceMIBs) != 1 || deviceMIBs[0].ID != keep.ID {
		t.Errorf("device MIBs = %+v, want only %s", deviceMIBs, keep.Name)
	}

	profileMIBs, err := s.GetProfileMIBs(ctx, profile.ID)
	if err != nil {
		t.Fatalf("GetProfileMIBs: %v", err)
	}
	if len(profileMIBs) != 1 || profileMIBs[0].ID != keep.ID {
		t.Errorf("profile MIBs = %+v, want only %s", profileMIBs, keep.Name)
	}
}

func TestDeleteMIBRejectsUnknownMIB(t *testing.T) {
	s := newCatalogStore(t)

	err := s.DeleteMIB(t.Context(), 4242)
	if err == nil {
		t.Fatal("DeleteMIB accepted a MIB that was never stored")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want it to say the MIB was not found", err)
	}
}

func TestDeviceProfileLifecycle(t *testing.T) {
	ctx := t.Context()
	s := newCatalogStore(t)

	created := mustCreateProfile(t, s, "switches")

	got, err := s.GetDeviceProfile(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDeviceProfile: %v", err)
	}
	if got.Name != "switches" {
		t.Errorf("Name = %q, want switches", got.Name)
	}

	byName, err := s.GetDeviceProfileByName(ctx, "switches")
	if err != nil {
		t.Fatalf("GetDeviceProfileByName: %v", err)
	}
	if byName.ID != created.ID {
		t.Errorf("ID = %d, want %d", byName.ID, created.ID)
	}

	mustCreateProfile(t, s, "routers")
	profiles, err := s.ListDeviceProfiles(ctx)
	if err != nil {
		t.Fatalf("ListDeviceProfiles: %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("ListDeviceProfiles returned %d, want 2", len(profiles))
	}

	created.Name = "access-switches"
	created.Description = "renamed"
	if err := s.UpdateDeviceProfile(ctx, created); err != nil {
		t.Fatalf("UpdateDeviceProfile: %v", err)
	}
	if _, err := s.GetDeviceProfileByName(ctx, "access-switches"); err != nil {
		t.Errorf("the new profile name does not resolve: %v", err)
	}
	if _, err := s.GetDeviceProfileByName(ctx, "switches"); err == nil {
		t.Error("the old profile name still resolves after a rename")
	}
}

func TestDeviceProfileRejectsBadInput(t *testing.T) {
	ctx := t.Context()
	s := newCatalogStore(t)
	existing := mustCreateProfile(t, s, "switches")
	other := mustCreateProfile(t, s, "routers")

	tests := []struct {
		name string
		call func() error
	}{
		{"get by unknown ID", func() error { _, err := s.GetDeviceProfile(ctx, 4242); return err }},
		{"get by unknown name", func() error { _, err := s.GetDeviceProfileByName(ctx, "ghosts"); return err }},
		{"update an unknown ID", func() error {
			return s.UpdateDeviceProfile(ctx, &DeviceProfile{ID: 4242, Name: "ghosts"})
		}},
		{"delete an unknown ID", func() error { return s.DeleteDeviceProfile(ctx, 4242) }},
		{"rename onto a name in use", func() error {
			clash := *other
			clash.Name = existing.Name
			return s.UpdateDeviceProfile(ctx, &clash)
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); err == nil {
				t.Fatal("the call succeeded on input it should have rejected")
			}
		})
	}
}

// TestDeleteDeviceProfileRemovesItsAssociations keeps a deleted profile from
// leaving join rows behind on either side.
func TestDeleteDeviceProfileRemovesItsAssociations(t *testing.T) {
	ctx := t.Context()
	s := newCatalogStore(t)

	profile := mustCreateProfile(t, s, "switches")
	keep := mustCreateProfile(t, s, "routers")
	device := mustCreateDevice(t, s, "bastion")
	mib := mustCreateMIB(t, s, "IF-MIB")

	for _, p := range []*DeviceProfile{profile, keep} {
		if err := s.AssignProfileToDevice(ctx, device.ID, p.ID); err != nil {
			t.Fatalf("AssignProfileToDevice: %v", err)
		}
	}
	if err := s.AssignMIBToProfile(ctx, profile.ID, mib.ID); err != nil {
		t.Fatalf("AssignMIBToProfile: %v", err)
	}

	if err := s.DeleteDeviceProfile(ctx, profile.ID); err != nil {
		t.Fatalf("DeleteDeviceProfile: %v", err)
	}

	profiles, err := s.GetDeviceProfiles(ctx, device.ID)
	if err != nil {
		t.Fatalf("GetDeviceProfiles: %v", err)
	}
	if len(profiles) != 1 || profiles[0].ID != keep.ID {
		t.Errorf("device profiles = %+v, want only %s", profiles, keep.Name)
	}

	mibs, err := s.GetProfileMIBs(ctx, profile.ID)
	if err != nil {
		t.Fatalf("GetProfileMIBs: %v", err)
	}
	if len(mibs) != 0 {
		t.Errorf("the deleted profile still has MIBs: %+v", mibs)
	}
}

func TestGetDevicesByProfile(t *testing.T) {
	ctx := t.Context()
	s := newCatalogStore(t)

	profile := mustCreateProfile(t, s, "switches")
	other := mustCreateProfile(t, s, "routers")
	assigned := mustCreateDevice(t, s, "bastion")
	unassigned := mustCreateDevice(t, s, "core-01")

	if err := s.AssignProfileToDevice(ctx, assigned.ID, profile.ID); err != nil {
		t.Fatalf("AssignProfileToDevice: %v", err)
	}
	if err := s.AssignProfileToDevice(ctx, unassigned.ID, other.ID); err != nil {
		t.Fatalf("AssignProfileToDevice: %v", err)
	}

	devices, err := s.GetDevicesByProfile(ctx, profile.ID)
	if err != nil {
		t.Fatalf("GetDevicesByProfile: %v", err)
	}
	if len(devices) != 1 || devices[0].ID != assigned.ID {
		t.Errorf("devices = %+v, want only %s", devices, assigned.Name)
	}
}

func TestGetDevicesByProfileWithNoMembers(t *testing.T) {
	s := newCatalogStore(t)
	profile := mustCreateProfile(t, s, "switches")

	devices, err := s.GetDevicesByProfile(t.Context(), profile.ID)
	if err != nil {
		t.Fatalf("GetDevicesByProfile: %v", err)
	}
	if len(devices) != 0 {
		t.Errorf("devices = %+v, want none", devices)
	}
}

func TestRemoveMIBFromProfile(t *testing.T) {
	ctx := t.Context()
	s := newCatalogStore(t)

	profile := mustCreateProfile(t, s, "switches")
	mib := mustCreateMIB(t, s, "IF-MIB")

	if err := s.AssignMIBToProfile(ctx, profile.ID, mib.ID); err != nil {
		t.Fatalf("AssignMIBToProfile: %v", err)
	}
	if err := s.RemoveMIBFromProfile(ctx, profile.ID, mib.ID); err != nil {
		t.Fatalf("RemoveMIBFromProfile: %v", err)
	}

	mibs, err := s.GetProfileMIBs(ctx, profile.ID)
	if err != nil {
		t.Fatalf("GetProfileMIBs: %v", err)
	}
	if len(mibs) != 0 {
		t.Errorf("profile MIBs = %+v, want none", mibs)
	}

	// A second removal is reported rather than silently accepted, so a client
	// that removed the wrong thing hears about it.
	err = s.RemoveMIBFromProfile(ctx, profile.ID, mib.ID)
	if err == nil {
		t.Fatal("RemoveMIBFromProfile accepted an assignment that is not there")
	}
	if !strings.Contains(err.Error(), "not assigned") {
		t.Errorf("error = %q, want it to say the MIB is not assigned", err)
	}
}

// TestGetDeviceMIBsSeparated distinguishes what a device is assigned directly
// from what it effectively polls, which is what the API reports separately.
func TestGetDeviceMIBsSeparated(t *testing.T) {
	ctx := t.Context()
	s := newCatalogStore(t)

	device := mustCreateDevice(t, s, "bastion")
	profile := mustCreateProfile(t, s, "switches")
	direct := mustCreateMIB(t, s, "IF-MIB")
	viaProfile := mustCreateMIB(t, s, "TCP-MIB")

	if err := s.AssignMIBToDevice(ctx, device.ID, direct.ID); err != nil {
		t.Fatalf("AssignMIBToDevice: %v", err)
	}
	if err := s.AssignMIBToProfile(ctx, profile.ID, viaProfile.ID); err != nil {
		t.Fatalf("AssignMIBToProfile: %v", err)
	}
	if err := s.AssignProfileToDevice(ctx, device.ID, profile.ID); err != nil {
		t.Fatalf("AssignProfileToDevice: %v", err)
	}

	directMIBs, allMIBs, err := s.GetDeviceMIBsSeparated(ctx, device.ID)
	if err != nil {
		t.Fatalf("GetDeviceMIBsSeparated: %v", err)
	}

	if len(directMIBs) != 1 || directMIBs[0].ID != direct.ID {
		t.Errorf("direct MIBs = %+v, want only %s", directMIBs, direct.Name)
	}
	if len(allMIBs) != 2 {
		t.Fatalf("effective MIBs = %+v, want both the direct and the profile's", allMIBs)
	}

	seen := make(map[int64]bool, len(allMIBs))
	for _, m := range allMIBs {
		seen[m.ID] = true
	}
	if !seen[direct.ID] || !seen[viaProfile.ID] {
		t.Errorf("effective MIBs = %+v, want %s and %s", allMIBs, direct.Name, viaProfile.Name)
	}
}

func TestGetDeviceMIBsSeparatedForADeviceWithNothing(t *testing.T) {
	s := newCatalogStore(t)
	device := mustCreateDevice(t, s, "bastion")

	directMIBs, allMIBs, err := s.GetDeviceMIBsSeparated(t.Context(), device.ID)
	if err != nil {
		t.Fatalf("GetDeviceMIBsSeparated: %v", err)
	}
	if len(directMIBs) != 0 || len(allMIBs) != 0 {
		t.Errorf("got %+v / %+v, want no MIBs on either list", directMIBs, allMIBs)
	}
}

func TestSetProfileMIBAssignment(t *testing.T) {
	ctx := t.Context()
	s := newCatalogStore(t)

	profile := mustCreateProfile(t, s, "switches")
	mib := mustCreateMIB(t, s, "IF-MIB")
	if err := s.AssignMIBToProfile(ctx, profile.ID, mib.ID); err != nil {
		t.Fatalf("AssignMIBToProfile: %v", err)
	}

	// A fresh assignment polls everything.
	got, err := s.ProfileMIBAssignment(ctx, profile.ID, mib.ID)
	if err != nil {
		t.Fatalf("ProfileMIBAssignment: %v", err)
	}
	if !got.Enabled || got.HasFilters() {
		t.Errorf("assignment = %+v, want enabled and unfiltered", got)
	}

	want := MIBAssignment{Enabled: true, IncludeNames: []string{"^if"}, MaxOIDs: 25}
	if setErr := s.SetProfileMIBAssignment(ctx, profile.ID, mib.ID, want); setErr != nil {
		t.Fatalf("SetProfileMIBAssignment: %v", setErr)
	}

	got, err = s.ProfileMIBAssignment(ctx, profile.ID, mib.ID)
	if err != nil {
		t.Fatalf("ProfileMIBAssignment: %v", err)
	}
	if !got.Enabled || got.MaxOIDs != 25 || len(got.IncludeNames) != 1 || got.IncludeNames[0] != "^if" {
		t.Errorf("assignment = %+v, want %+v", got, want)
	}
	if !got.HasFilters() {
		t.Error("HasFilters reported an unfiltered assignment")
	}
}

func TestSetProfileMIBAssignmentRejectsUnassignedMIB(t *testing.T) {
	ctx := t.Context()
	s := newCatalogStore(t)

	profile := mustCreateProfile(t, s, "switches")
	mib := mustCreateMIB(t, s, "IF-MIB")

	err := s.SetProfileMIBAssignment(ctx, profile.ID, mib.ID, MIBAssignment{Enabled: false})
	if err == nil {
		t.Fatal("SetProfileMIBAssignment configured a MIB that is not assigned to the profile")
	}

	var notFound *AssignmentNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("error = %v (%T), want *AssignmentNotFoundError", err, err)
	}
	if notFound.Kind != "profile" || notFound.OwnerID != profile.ID || notFound.MIBID != mib.ID {
		t.Errorf("error = %+v, want it to name the profile and MIB", notFound)
	}
}

func TestStorageErrorMessages(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{
			err:  &AssignmentNotFoundError{Kind: "device", OwnerID: 3, MIBID: 7},
			want: "storage: MIB 7 is not assigned to device 3",
		},
		{
			err:  &AssignmentNotFoundError{Kind: "profile", OwnerID: 1, MIBID: 2},
			want: "storage: MIB 2 is not assigned to profile 1",
		},
		{
			err:  &AssignmentKeyError{Key: "dev-mib:bad"},
			want: `storage: malformed assignment key "dev-mib:bad"`,
		},
	}

	for _, tt := range tests {
		if got := tt.err.Error(); got != tt.want {
			t.Errorf("Error() = %q, want %q", got, tt.want)
		}
	}
}

// TestUniqueNamesAreEnforcedOnCreate keeps a second record from shadowing the
// first in the name index, which is what name lookups resolve through.
func TestUniqueNamesAreEnforcedOnCreate(t *testing.T) {
	ctx := t.Context()
	s := newCatalogStore(t)

	mustCreateMIB(t, s, "IF-MIB")
	mustCreateProfile(t, s, "switches")
	mustCreateDevice(t, s, "bastion")

	tests := []struct {
		name string
		call func() error
	}{
		{"MIB", func() error { return s.CreateMIB(ctx, &MIB{Name: "IF-MIB"}) }},
		{"profile", func() error { return s.CreateDeviceProfile(ctx, &DeviceProfile{Name: "switches"}) }},
		{"device", func() error {
			return s.CreateDevice(ctx, &Device{Name: "bastion", Address: "192.0.2.2", SNMPVersion: "v2c"})
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call()
			if err == nil {
				t.Fatal("a second record was created under a name already in use")
			}
			if !strings.Contains(err.Error(), "already exists") {
				t.Errorf("error = %q, want it to report the name is taken", err)
			}
		})
	}
}

// TestUpdateDeviceRenamesTheNameIndex keeps a renamed device resolvable under
// its new name and no longer under the old one.
func TestUpdateDeviceRenamesTheNameIndex(t *testing.T) {
	ctx := t.Context()
	s := newCatalogStore(t)

	device := mustCreateDevice(t, s, "bastion")
	other := mustCreateDevice(t, s, "core-01")

	device.Name = "bastion-pdu"
	device.Address = "192.0.2.9"
	if err := s.UpdateDevice(ctx, device); err != nil {
		t.Fatalf("UpdateDevice: %v", err)
	}

	got, err := s.GetDeviceByName(ctx, "bastion-pdu")
	if err != nil {
		t.Fatalf("the new name does not resolve: %v", err)
	}
	if got.ID != device.ID || got.Address != "192.0.2.9" {
		t.Errorf("got %+v, want the renamed device", got)
	}
	if _, err := s.GetDeviceByName(ctx, "bastion"); err == nil {
		t.Error("the old device name still resolves after a rename")
	}

	// Renaming onto a name in use must be refused, leaving both records intact.
	clash := *other
	clash.Name = "bastion-pdu"
	if err := s.UpdateDevice(ctx, &clash); err == nil {
		t.Fatal("UpdateDevice renamed a device onto a name already in use")
	}
	if got, err := s.GetDeviceByName(ctx, other.Name); err != nil || got.ID != other.ID {
		t.Errorf("the rejected rename disturbed %s: %+v, %v", other.Name, got, err)
	}
}

// TestAssigningTwiceIsIdempotent states what a repeated assignment means: the
// join row is keyed by the pair, so a second assignment cannot duplicate it.
func TestAssigningTwiceIsIdempotent(t *testing.T) {
	ctx := t.Context()
	s := newCatalogStore(t)

	device := mustCreateDevice(t, s, "bastion")
	profile := mustCreateProfile(t, s, "switches")
	mib := mustCreateMIB(t, s, "IF-MIB")

	for range 2 {
		if err := s.AssignMIBToDevice(ctx, device.ID, mib.ID); err != nil {
			t.Fatalf("AssignMIBToDevice: %v", err)
		}
		if err := s.AssignMIBToProfile(ctx, profile.ID, mib.ID); err != nil {
			t.Fatalf("AssignMIBToProfile: %v", err)
		}
		if err := s.AssignProfileToDevice(ctx, device.ID, profile.ID); err != nil {
			t.Fatalf("AssignProfileToDevice: %v", err)
		}
	}

	mibs, err := s.GetDeviceMIBs(ctx, device.ID)
	if err != nil {
		t.Fatalf("GetDeviceMIBs: %v", err)
	}
	if len(mibs) != 1 {
		t.Errorf("device MIBs = %+v, want one row for one pair", mibs)
	}

	profiles, err := s.GetDeviceProfiles(ctx, device.ID)
	if err != nil {
		t.Fatalf("GetDeviceProfiles: %v", err)
	}
	if len(profiles) != 1 {
		t.Errorf("device profiles = %+v, want one row for one pair", profiles)
	}
}

// TestAssigningSomethingThatDoesNotExistIsRefused keeps a join row from being
// written against a record that was never stored, which would later resolve to
// nothing.
func TestAssigningSomethingThatDoesNotExistIsRefused(t *testing.T) {
	ctx := t.Context()
	s := newCatalogStore(t)

	device := mustCreateDevice(t, s, "bastion")
	profile := mustCreateProfile(t, s, "switches")
	mib := mustCreateMIB(t, s, "IF-MIB")

	const missing int64 = 4242

	tests := []struct {
		name string
		call func() error
		want string
	}{
		{"unknown device", func() error { return s.AssignMIBToDevice(ctx, missing, mib.ID) }, "device not found"},
		{"unknown MIB for a device", func() error { return s.AssignMIBToDevice(ctx, device.ID, missing) }, "MIB not found"},
		{"unknown profile", func() error { return s.AssignMIBToProfile(ctx, missing, mib.ID) }, "profile not found"},
		{"unknown MIB for a profile", func() error { return s.AssignMIBToProfile(ctx, profile.ID, missing) }, "MIB not found"},
		{"unknown device for a profile", func() error { return s.AssignProfileToDevice(ctx, missing, profile.ID) }, "device not found"},
		{"unknown profile for a device", func() error { return s.AssignProfileToDevice(ctx, device.ID, missing) }, "profile not found"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call()
			if err == nil {
				t.Fatal("an assignment was written against a record that does not exist")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestRemoveProfileFromDeviceRejectsAnAbsentAssignment(t *testing.T) {
	ctx := t.Context()
	s := newCatalogStore(t)

	device := mustCreateDevice(t, s, "bastion")
	profile := mustCreateProfile(t, s, "switches")

	err := s.RemoveProfileFromDevice(ctx, device.ID, profile.ID)
	if err == nil {
		t.Fatal("RemoveProfileFromDevice accepted an assignment that is not there")
	}
	if !strings.Contains(err.Error(), "not assigned") {
		t.Errorf("error = %q, want it to say the profile is not assigned", err)
	}
}

func TestMIBIDFromJoinKey(t *testing.T) {
	const scanPrefix = "dev-mib:0000000003:"

	tests := []struct {
		name    string
		key     string
		want    int64
		wantErr bool
	}{
		{name: "a well-formed join key", key: scanPrefix + "0000000007", want: 7},
		{name: "no MIB half", key: "dev-mib:0000000003:", wantErr: true},
		{name: "a key shorter than its own prefix", key: "dev-mib:", wantErr: true},
		{name: "an unparsable MIB half", key: scanPrefix + "seven", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mibIDFromJoinKey(tt.key, scanPrefix)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("mibIDFromJoinKey(%q) = %d, want an error", tt.key, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("mibIDFromJoinKey(%q): %v", tt.key, err)
			}
			if got != tt.want {
				t.Errorf("mibIDFromJoinKey(%q) = %d, want %d", tt.key, got, tt.want)
			}
		})
	}
}
