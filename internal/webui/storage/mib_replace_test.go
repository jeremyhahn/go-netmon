package storage

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble"
)

// TestReplaceMIBContentRefreshesParsedFields is the point of the whole
// operation: a module re-parsed from a fixed parser must pick up the new
// metadata without becoming a different record.
func TestReplaceMIBContentRefreshesParsedFields(t *testing.T) {
	ctx := t.Context()
	s := newCatalogStore(t)

	created := mustCreateMIB(t, s, "ENTITY-SENSOR-MIB")
	if err := s.UpdateMIBTags(ctx, created.ID, []string{"cisco", "asr1001"}); err != nil {
		t.Fatalf("UpdateMIBTags: %v", err)
	}

	refreshed, err := s.ReplaceMIBContent(ctx, "ENTITY-SENSOR-MIB", MIBContent{
		FilePath:    "/mibs/uploaded/ENTITY-SENSOR-MIB.my",
		Description: "The MIB module to describe and store sensor readings.",
		ObjectCount: 12,
	})
	if err != nil {
		t.Fatalf("ReplaceMIBContent: %v", err)
	}

	// Parsed metadata is refreshed.
	if refreshed.ObjectCount != 12 {
		t.Errorf("ObjectCount = %d, want the newly parsed 12", refreshed.ObjectCount)
	}
	if !strings.Contains(refreshed.Description, "sensor readings") {
		t.Errorf("Description = %q, want the newly parsed text", refreshed.Description)
	}
	if refreshed.FilePath != "/mibs/uploaded/ENTITY-SENSOR-MIB.my" {
		t.Errorf("FilePath = %q, want the re-uploaded file", refreshed.FilePath)
	}

	// Identity and operator-owned fields are not.
	if refreshed.ID != created.ID {
		t.Errorf("ID = %d, want the original %d; assignments are keyed by it",
			refreshed.ID, created.ID)
	}
	if refreshed.Name != created.Name {
		t.Errorf("Name = %q, want %q", refreshed.Name, created.Name)
	}
	if got := strings.Join(refreshed.Tags, ","); got != "cisco,asr1001" {
		t.Errorf("Tags = %q, want the tags the operator set", got)
	}
	if !refreshed.ImportedAt.Equal(created.ImportedAt) {
		t.Errorf("ImportedAt = %v, want the original %v", refreshed.ImportedAt, created.ImportedAt)
	}
	if refreshed.RefreshedAt == nil {
		t.Fatal("RefreshedAt is unset; a refresh has to be visible afterwards")
	}
	if time.Since(*refreshed.RefreshedAt) > time.Minute {
		t.Errorf("RefreshedAt = %v, want the time of this refresh", refreshed.RefreshedAt)
	}

	// The refresh is what is stored, not just what was returned.
	stored, err := s.GetMIB(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetMIB: %v", err)
	}
	if stored.ObjectCount != 12 || stored.RefreshedAt == nil {
		t.Errorf("stored record = %+v, want the refreshed one", stored)
	}

	// And the module is still resolvable under its name.
	byName, err := s.GetMIBByName(ctx, "ENTITY-SENSOR-MIB")
	if err != nil {
		t.Fatalf("GetMIBByName after refresh: %v", err)
	}
	if byName.ID != created.ID {
		t.Errorf("name index points at %d, want %d", byName.ID, created.ID)
	}
}

// TestReplaceMIBContentKeepsAssignments is the guarantee that makes a refresh
// usable on a live server: nothing that was polling the module stops polling it,
// and no per-assignment filter is lost.
func TestReplaceMIBContentKeepsAssignments(t *testing.T) {
	ctx := t.Context()
	s := newCatalogStore(t)

	mib := mustCreateMIB(t, s, "ENTITY-SENSOR-MIB")
	device := mustCreateDevice(t, s, "core-01")
	profile := mustCreateProfile(t, s, "Core Routers")

	if err := s.AssignMIBToDevice(ctx, device.ID, mib.ID); err != nil {
		t.Fatalf("AssignMIBToDevice: %v", err)
	}
	if err := s.AssignMIBToProfile(ctx, profile.ID, mib.ID); err != nil {
		t.Fatalf("AssignMIBToProfile: %v", err)
	}

	deviceAssignment := MIBAssignment{
		Enabled:      true,
		IncludeOIDs:  []string{"^1\\.3\\.6\\.1\\.2\\.1\\.99\\.1\\.1\\.1\\.4"},
		ExcludeOIDs:  []string{"\\.9$"},
		IncludeNames: []string{"entPhySensorValue"},
		ExcludeNames: []string{"Rate$"},
		MaxOIDs:      64,
	}
	if err := s.SetDeviceMIBAssignment(ctx, device.ID, mib.ID, deviceAssignment); err != nil {
		t.Fatalf("SetDeviceMIBAssignment: %v", err)
	}

	// A disabled profile assignment is the harder case: a refresh that dropped
	// and recreated the row would quietly start polling it again.
	profileAssignment := MIBAssignment{
		Enabled:     false,
		ExcludeOIDs: []string{"^1\\.3\\.6\\.1\\.2\\.1\\.99\\.1\\.1\\.1\\.1"},
		MaxOIDs:     8,
	}
	if err := s.SetProfileMIBAssignment(ctx, profile.ID, mib.ID, profileAssignment); err != nil {
		t.Fatalf("SetProfileMIBAssignment: %v", err)
	}

	if _, err := s.ReplaceMIBContent(ctx, mib.Name, MIBContent{
		FilePath:    "/mibs/uploaded/ENTITY-SENSOR-MIB.my",
		Description: "refreshed",
		ObjectCount: 12,
	}); err != nil {
		t.Fatalf("ReplaceMIBContent: %v", err)
	}

	deviceMIBs, err := s.GetDeviceMIBs(ctx, device.ID)
	if err != nil {
		t.Fatalf("GetDeviceMIBs: %v", err)
	}
	if len(deviceMIBs) != 1 || deviceMIBs[0].ID != mib.ID {
		t.Fatalf("device MIBs = %+v, want the refreshed module still assigned", deviceMIBs)
	}
	if deviceMIBs[0].ObjectCount != 12 {
		t.Errorf("device sees ObjectCount %d, want the refreshed 12", deviceMIBs[0].ObjectCount)
	}

	profileMIBs, err := s.GetProfileMIBs(ctx, profile.ID)
	if err != nil {
		t.Fatalf("GetProfileMIBs: %v", err)
	}
	if len(profileMIBs) != 1 || profileMIBs[0].ID != mib.ID {
		t.Fatalf("profile MIBs = %+v, want the refreshed module still in the profile", profileMIBs)
	}

	gotDevice, err := s.DeviceMIBAssignment(ctx, device.ID, mib.ID)
	if err != nil {
		t.Fatalf("DeviceMIBAssignment: %v", err)
	}
	assertAssignmentEqual(t, "device", gotDevice, deviceAssignment)

	gotProfile, err := s.ProfileMIBAssignment(ctx, profile.ID, mib.ID)
	if err != nil {
		t.Fatalf("ProfileMIBAssignment: %v", err)
	}
	assertAssignmentEqual(t, "profile", gotProfile, profileAssignment)
}

// assertAssignmentEqual compares every field an operator can set, so a refresh
// that silently reset one of them fails here rather than in production.
func assertAssignmentEqual(t *testing.T, kind string, got, want MIBAssignment) {
	t.Helper()

	if got.Enabled != want.Enabled {
		t.Errorf("%s Enabled = %v, want %v", kind, got.Enabled, want.Enabled)
	}
	if got.MaxOIDs != want.MaxOIDs {
		t.Errorf("%s MaxOIDs = %d, want %d", kind, got.MaxOIDs, want.MaxOIDs)
	}
	for _, field := range []struct {
		name      string
		got, want []string
	}{
		{"IncludeOIDs", got.IncludeOIDs, want.IncludeOIDs},
		{"ExcludeOIDs", got.ExcludeOIDs, want.ExcludeOIDs},
		{"IncludeNames", got.IncludeNames, want.IncludeNames},
		{"ExcludeNames", got.ExcludeNames, want.ExcludeNames},
	} {
		if strings.Join(field.got, "|") != strings.Join(field.want, "|") {
			t.Errorf("%s %s = %v, want %v", kind, field.name, field.got, field.want)
		}
	}
}

// TestReplaceMIBContentUnknownModule checks the caller can tell "never
// imported" apart from a store failure, which is what lets an import fall back
// to creating the record.
func TestReplaceMIBContentUnknownModule(t *testing.T) {
	ctx := t.Context()
	s := newCatalogStore(t)

	_, err := s.ReplaceMIBContent(ctx, "NO-SUCH-MIB", MIBContent{ObjectCount: 1})

	var notFound *MIBNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("error = %v, want a *MIBNotFoundError", err)
	}
	if notFound.Name != "NO-SUCH-MIB" {
		t.Errorf("error names %q, want the module that was asked for", notFound.Name)
	}
}

// TestReplaceMIBContentDanglingNameIndex covers a name index that outlived its
// record. The refresh must report the module as missing rather than resurrect
// it under a record with no identity.
func TestReplaceMIBContentDanglingNameIndex(t *testing.T) {
	ctx := t.Context()
	s := newCatalogStore(t)

	mib := mustCreateMIB(t, s, "IF-MIB")
	if err := s.db.Delete([]byte(PrefixMIB+formatID(mib.ID)), pebble.Sync); err != nil {
		t.Fatalf("delete record: %v", err)
	}

	_, err := s.ReplaceMIBContent(ctx, "IF-MIB", MIBContent{ObjectCount: 1})

	var notFound *MIBNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("error = %v, want a *MIBNotFoundError", err)
	}
}

// TestGetMIBByNameMalformedIndex covers an index entry that is not an ID. It
// must surface as a store error, not as "the module does not exist", so a
// corrupt index is never mistaken for a module worth re-creating.
func TestGetMIBByNameMalformedIndex(t *testing.T) {
	ctx := t.Context()
	s := newCatalogStore(t)

	if err := s.db.Set([]byte(PrefixNameIndex+"mib:BROKEN-MIB"), []byte("not-an-id"), pebble.Sync); err != nil {
		t.Fatalf("seed index: %v", err)
	}

	_, err := s.GetMIBByName(ctx, "BROKEN-MIB")

	var storeErr *MIBStoreError
	if !errors.As(err, &storeErr) {
		t.Fatalf("error = %v, want a *MIBStoreError", err)
	}
	var notFound *MIBNotFoundError
	if errors.As(err, &notFound) {
		t.Error("a corrupt index must not be reported as a missing module")
	}
}

// TestGetMIBByNameUnknown checks the ordinary miss is typed too.
func TestGetMIBByNameUnknown(t *testing.T) {
	s := newCatalogStore(t)

	_, err := s.GetMIBByName(t.Context(), "NO-SUCH-MIB")

	var notFound *MIBNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("error = %v, want a *MIBNotFoundError", err)
	}
}

// TestCreateMIBDuplicateNamesTheExistingRecord checks the conflict carries the
// ID of the record already holding the name. Without it the API cannot tell an
// operator which module to refresh.
func TestCreateMIBDuplicateNamesTheExistingRecord(t *testing.T) {
	ctx := t.Context()
	s := newCatalogStore(t)

	first := mustCreateMIB(t, s, "IF-MIB")

	err := s.CreateMIB(ctx, &MIB{Name: "IF-MIB", FilePath: "/mibs/other.mib"})

	var exists *MIBExistsError
	if !errors.As(err, &exists) {
		t.Fatalf("error = %v, want a *MIBExistsError", err)
	}
	if exists.ID != first.ID {
		t.Errorf("conflict names id %d, want the stored %d", exists.ID, first.ID)
	}
	if exists.Name != "IF-MIB" {
		t.Errorf("conflict names %q, want IF-MIB", exists.Name)
	}
	if !strings.Contains(exists.Error(), "already exists") {
		t.Errorf("message = %q, want it to say the name is taken", exists)
	}
}

// TestMIBErrorMessages checks each MIB error says which module it is about;
// these strings reach an operator through the API and the CLI.
func TestMIBErrorMessages(t *testing.T) {
	cause := errors.New("disk gone")

	tests := []struct {
		name string
		err  error
		want []string
	}{
		{
			name: "exists",
			err:  &MIBExistsError{Name: "IF-MIB", ID: 7},
			want: []string{"IF-MIB", "already exists", "7"},
		},
		{
			name: "not found",
			err:  &MIBNotFoundError{Name: "IF-MIB"},
			want: []string{"IF-MIB", "MIB not found"},
		},
		{
			name: "store",
			err:  &MIBStoreError{Op: "refresh", Name: "IF-MIB", Err: cause},
			want: []string{"refresh", "IF-MIB", "disk gone"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.err.Error()
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("message %q is missing %q", got, want)
				}
			}
		})
	}

	if !errors.Is(&MIBStoreError{Err: cause}, cause) {
		t.Error("MIBStoreError does not unwrap to its cause")
	}
}
