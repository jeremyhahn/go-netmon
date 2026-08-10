package storage

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble"
)

// dumpStore returns every key and value in the store, which is how a test
// asserts that an operation changed nothing at all rather than that it changed
// nothing it happened to look at.
func dumpStore(t *testing.T, store *Storage) map[string]string {
	t.Helper()

	iter, err := store.db.NewIter(&pebble.IterOptions{})
	if err != nil {
		t.Fatalf("NewIter: %v", err)
	}
	defer func() { _ = iter.Close() }()

	contents := make(map[string]string)
	for iter.First(); iter.Valid(); iter.Next() {
		contents[string(iter.Key())] = string(iter.Value())
	}
	if err := iter.Error(); err != nil {
		t.Fatalf("iterating store: %v", err)
	}
	return contents
}

// seedConfig builds a configuration with every shape the document has to carry:
// a profile with a filtered assignment, a device that inherits it, a device with
// its own direct assignment and a pinned poll interval, and a module that
// nothing references.
func seedConfig(t *testing.T, store *Storage) {
	t.Helper()
	ctx := context.Background()

	for _, name := range []string{"IF-MIB", "HOST-RESOURCES-MIB", "UNUSED-MIB"} {
		mib := &MIB{Name: name, FilePath: "/mibs/" + name + ".txt", ObjectCount: 3}
		if err := store.CreateMIB(ctx, mib); err != nil {
			t.Fatalf("CreateMIB(%s): %v", name, err)
		}
	}

	profile := &DeviceProfile{Name: "core", Description: "core routers"}
	if err := store.CreateDeviceProfile(ctx, profile); err != nil {
		t.Fatalf("CreateDeviceProfile: %v", err)
	}
	ifMIB := mustMIBID(t, store, "IF-MIB")
	if err := store.AssignMIBToProfile(ctx, profile.ID, ifMIB); err != nil {
		t.Fatalf("AssignMIBToProfile: %v", err)
	}
	filtered := MIBAssignment{
		Enabled:      true,
		IncludeNames: []string{"^ifIn.*"},
		ExcludeOIDs:  []string{"^1\\.3\\.6\\.1\\.2\\.1\\.2\\.2\\.1\\.1$"},
		MaxOIDs:      50,
	}
	if err := store.SetProfileMIBAssignment(ctx, profile.ID, ifMIB, filtered); err != nil {
		t.Fatalf("SetProfileMIBAssignment: %v", err)
	}

	interval := 45
	router := &Device{
		Name: "core-01", Address: "192.0.2.252", SNMPVersion: "2c",
		Community: "public", Port: 161, PollInterval: &interval,
	}
	if err := store.CreateDevice(ctx, router); err != nil {
		t.Fatalf("CreateDevice(core-01): %v", err)
	}
	if err := store.AssignProfileToDevice(ctx, router.ID, profile.ID); err != nil {
		t.Fatalf("AssignProfileToDevice: %v", err)
	}
	hostMIB := mustMIBID(t, store, "HOST-RESOURCES-MIB")
	if err := store.AssignMIBToDevice(ctx, router.ID, hostMIB); err != nil {
		t.Fatalf("AssignMIBToDevice: %v", err)
	}

	bastion := &Device{Name: "bastion", Address: "192.0.2.14", SNMPVersion: "v2c", Port: 1161}
	if err := store.CreateDevice(ctx, bastion); err != nil {
		t.Fatalf("CreateDevice(bastion): %v", err)
	}
}

func mustMIBID(t *testing.T, store *Storage, name string) int64 {
	t.Helper()
	mib, err := store.GetMIBByName(context.Background(), name)
	if err != nil {
		t.Fatalf("GetMIBByName(%s): %v", name, err)
	}
	return mib.ID
}

// exportConfig exports and clears the timestamp, which is provenance rather than
// configuration and would otherwise defeat every comparison.
func exportConfig(t *testing.T, store *Storage, opts ConfigExportOptions) *Config {
	t.Helper()
	doc, err := store.ExportConfig(context.Background(), opts)
	if err != nil {
		t.Fatalf("ExportConfig: %v", err)
	}
	doc.ExportedAt = doc.ExportedAt.UTC().Truncate(0)
	return doc
}

func TestExportConfigCarriesEveryConfiguredThing(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	seedConfig(t, store)

	doc := exportConfig(t, store, ConfigExportOptions{})

	if doc.SchemaVersion != ConfigSchemaVersion {
		t.Errorf("schema_version = %d, want %d", doc.SchemaVersion, ConfigSchemaVersion)
	}
	if doc.ExportedAt.IsZero() {
		t.Error("exported_at is not set")
	}

	wantMIBs := []ConfigMIB{{Name: "HOST-RESOURCES-MIB"}, {Name: "IF-MIB"}, {Name: "UNUSED-MIB"}}
	if !reflect.DeepEqual(doc.MIBs, wantMIBs) {
		t.Errorf("mibs = %+v, want %+v", doc.MIBs, wantMIBs)
	}

	interval := 45
	wantDevices := []ConfigDevice{
		{Name: "bastion", Address: "192.0.2.14", Port: 1161, SNMPVersion: "v2c"},
		{
			Name: "core-01", Address: "192.0.2.252", Port: 161, SNMPVersion: "2c",
			Community: "public", PollInterval: &interval,
			Profiles: []string{"core"},
			MIBs: []ConfigAssignment{{
				Module: "HOST-RESOURCES-MIB", MIBAssignment: MIBAssignment{Enabled: true},
			}},
		},
	}
	if !reflect.DeepEqual(doc.Devices, wantDevices) {
		t.Errorf("devices = %+v, want %+v", doc.Devices, wantDevices)
	}

	wantProfiles := []ConfigProfile{{
		Name: "core", Description: "core routers",
		MIBs: []ConfigAssignment{{
			Module: "IF-MIB",
			MIBAssignment: MIBAssignment{
				Enabled:      true,
				ExcludeOIDs:  []string{"^1\\.3\\.6\\.1\\.2\\.1\\.2\\.2\\.1\\.1$"},
				IncludeNames: []string{"^ifIn.*"},
				MaxOIDs:      50,
			},
		}},
	}}
	if !reflect.DeepEqual(doc.Profiles, wantProfiles) {
		t.Errorf("profiles = %+v, want %+v", doc.Profiles, wantProfiles)
	}
}

func TestExportConfigOnEmptyInstanceIsEmpty(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	doc := exportConfig(t, store, ConfigExportOptions{})

	if len(doc.Devices) != 0 || len(doc.Profiles) != 0 || len(doc.MIBs) != 0 {
		t.Fatalf("empty instance exported %d devices, %d profiles, %d mibs",
			len(doc.Devices), len(doc.Profiles), len(doc.MIBs))
	}
}

func TestExportConfigReferencedMIBsOnly(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	seedConfig(t, store)

	doc := exportConfig(t, store, ConfigExportOptions{ReferencedMIBsOnly: true})

	want := []ConfigMIB{{Name: "HOST-RESOURCES-MIB"}, {Name: "IF-MIB"}}
	if !reflect.DeepEqual(doc.MIBs, want) {
		t.Errorf("mibs = %+v, want %+v (UNUSED-MIB is referenced by nothing)", doc.MIBs, want)
	}
}

func TestImportConfigRoundTripReproducesTheConfiguration(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	seedConfig(t, store)

	original := exportConfig(t, store, ConfigExportOptions{})
	ctx := context.Background()

	// Wipe: an empty document is a configuration with nothing in it, and
	// replace semantics mean importing it removes everything.
	empty := &Config{SchemaVersion: ConfigSchemaVersion}
	if _, err := store.ImportConfig(ctx, empty); err != nil {
		t.Fatalf("ImportConfig(empty): %v", err)
	}
	wiped := exportConfig(t, store, ConfigExportOptions{})
	if len(wiped.Devices) != 0 || len(wiped.Profiles) != 0 {
		t.Fatalf("after wiping, %d devices and %d profiles remain",
			len(wiped.Devices), len(wiped.Profiles))
	}
	if len(wiped.MIBs) != 3 {
		t.Fatalf("wiping the configuration removed MIB modules: %d left, want 3", len(wiped.MIBs))
	}

	if _, err := store.ImportConfig(ctx, original); err != nil {
		t.Fatalf("ImportConfig(original): %v", err)
	}

	restored := exportConfig(t, store, ConfigExportOptions{})
	restored.ExportedAt = original.ExportedAt
	if !reflect.DeepEqual(restored, original) {
		t.Errorf("restored configuration differs from the exported one\n got: %+v\nwant: %+v",
			restored, original)
	}
}

func TestImportConfigDeletesRecordsTheDocumentOmits(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	seedConfig(t, store)
	ctx := context.Background()

	doc := exportConfig(t, store, ConfigExportOptions{})

	// Drop the second device from the document. Replace semantics must delete
	// it from the instance, which is the destructive behavior this feature was
	// built around.
	kept := make([]ConfigDevice, 0, 1)
	for _, d := range doc.Devices {
		if d.Name != "bastion" {
			kept = append(kept, d)
		}
	}
	doc.Devices = kept

	plan, err := store.ImportConfig(ctx, doc)
	if err != nil {
		t.Fatalf("ImportConfig: %v", err)
	}
	if plan.Counts.DevicesDeleted != 1 {
		t.Errorf("plan deleted %d devices, want 1", plan.Counts.DevicesDeleted)
	}

	if _, lookupErr := store.GetDeviceByName(ctx, "bastion"); lookupErr == nil {
		t.Fatal("device bastion survived an import that omitted it")
	}
	devices, err := store.ListDevices(ctx)
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if len(devices) != 1 || devices[0].Name != "core-01" {
		t.Errorf("devices after import = %+v, want only core-01", devices)
	}
}

func TestImportConfigKeepsIdentityOfSurvivingRecords(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	seedConfig(t, store)
	ctx := context.Background()

	before, err := store.GetDeviceByName(ctx, "core-01")
	if err != nil {
		t.Fatalf("GetDeviceByName: %v", err)
	}

	doc := exportConfig(t, store, ConfigExportOptions{})
	for i := range doc.Devices {
		if doc.Devices[i].Name == "core-01" {
			doc.Devices[i].Address = "192.0.2.253"
		}
	}
	if _, importErr := store.ImportConfig(ctx, doc); importErr != nil {
		t.Fatalf("ImportConfig: %v", importErr)
	}

	after, err := store.GetDeviceByName(ctx, "core-01")
	if err != nil {
		t.Fatalf("GetDeviceByName after import: %v", err)
	}
	if after.ID != before.ID {
		t.Errorf("device ID changed from %d to %d across an update", before.ID, after.ID)
	}
	if !after.CreatedAt.Equal(before.CreatedAt) {
		t.Errorf("created_at changed from %s to %s", before.CreatedAt, after.CreatedAt)
	}
	if after.Address != "192.0.2.253" {
		t.Errorf("address = %q, want the imported value", after.Address)
	}
}

func TestImportConfigOfItsOwnExportChangesNothing(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	seedConfig(t, store)
	ctx := context.Background()

	doc := exportConfig(t, store, ConfigExportOptions{})
	before := dumpStore(t, store)

	plan, err := store.ImportConfig(ctx, doc)
	if err != nil {
		t.Fatalf("ImportConfig: %v", err)
	}
	if plan.Changed() {
		t.Errorf("re-importing an instance's own export reported changes: %+v", plan)
	}
	if got := dumpStore(t, store); !reflect.DeepEqual(got, before) {
		t.Error("re-importing an instance's own export modified the store")
	}
}

func TestPlanImportConfigChangesNothing(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	seedConfig(t, store)
	ctx := context.Background()

	doc := exportConfig(t, store, ConfigExportOptions{})
	doc.Devices = nil
	doc.Profiles = nil

	before := dumpStore(t, store)
	plan, err := store.PlanImportConfig(ctx, doc, nil)
	if err != nil {
		t.Fatalf("PlanImportConfig: %v", err)
	}
	if plan.Counts.DevicesDeleted != 2 || plan.Counts.ProfilesDeleted != 1 {
		t.Errorf("plan counts = %+v, want 2 devices and 1 profile deleted", plan.Counts)
	}

	if got := dumpStore(t, store); !reflect.DeepEqual(got, before) {
		t.Error("a dry run modified the store")
	}
}

func TestPlanImportConfigReportsAddUpdateAndDelete(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	seedConfig(t, store)

	doc := exportConfig(t, store, ConfigExportOptions{})
	doc.Devices = editDevices(doc.Devices)
	doc.Devices = append(doc.Devices, ConfigDevice{
		Name: "newswitch", Address: "192.0.2.9", SNMPVersion: "2c", Port: 161,
	})

	plan, err := store.PlanImportConfig(context.Background(), doc, nil)
	if err != nil {
		t.Fatalf("PlanImportConfig: %v", err)
	}

	byName := changesByName(plan.Devices)
	deleted, ok := byName["bastion"]
	if !ok || deleted.Action != ConfigActionDelete {
		t.Fatalf("bastion change = %+v, want a delete", deleted)
	}
	if deleted.ID == 0 {
		t.Error("a delete does not name the stored record's ID")
	}
	created, ok := byName["newswitch"]
	if !ok || created.Action != ConfigActionCreate {
		t.Fatalf("newswitch change = %+v, want a create", created)
	}
	updated, ok := byName["core-01"]
	if !ok || updated.Action != ConfigActionUpdate {
		t.Fatalf("core-01 change = %+v, want an update", updated)
	}

	want := map[string][2]string{
		"address":                          {"192.0.2.252", "192.0.2.253"},
		"poll_interval":                    {"45s", "inherit"},
		"mibs[HOST-RESOURCES-MIB].enabled": {"true", "false"},
	}
	assertFieldChanges(t, updated.Fields, want)
}

func TestPlanImportConfigReportsAPerAssignmentFilterChange(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	seedConfig(t, store)

	doc := exportConfig(t, store, ConfigExportOptions{})
	doc.Profiles[0].MIBs[0].IncludeNames = []string{"^ifIn.*", "^ifOut.*"}
	doc.Profiles[0].MIBs[0].MaxOIDs = 0

	plan, err := store.PlanImportConfig(context.Background(), doc, nil)
	if err != nil {
		t.Fatalf("PlanImportConfig: %v", err)
	}
	if len(plan.Profiles) != 1 || plan.Profiles[0].Action != ConfigActionUpdate {
		t.Fatalf("profile changes = %+v, want one update", plan.Profiles)
	}

	assertFieldChanges(t, plan.Profiles[0].Fields, map[string][2]string{
		"mibs[IF-MIB].include_names": {"[^ifIn.*]", "[^ifIn.* ^ifOut.*]"},
		"mibs[IF-MIB].max_oids":      {"50", "0"},
	})
}

func TestPlanImportConfigReportsAssignmentsArrivingAndLeaving(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	seedConfig(t, store)

	doc := exportConfig(t, store, ConfigExportOptions{})
	for i := range doc.Devices {
		if doc.Devices[i].Name != "core-01" {
			continue
		}
		doc.Devices[i].MIBs = []ConfigAssignment{{
			Module: "IF-MIB", MIBAssignment: MIBAssignment{Enabled: true},
		}}
	}

	plan, err := store.PlanImportConfig(context.Background(), doc, nil)
	if err != nil {
		t.Fatalf("PlanImportConfig: %v", err)
	}
	assertFieldChanges(t, changesByName(plan.Devices)["core-01"].Fields, map[string][2]string{
		"mibs[HOST-RESOURCES-MIB]": {"enabled", ""},
		"mibs[IF-MIB]":             {"", "enabled"},
	})
}

func TestImportConfigRejectsAModuleThisInstanceDoesNotHave(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	seedConfig(t, store)
	ctx := context.Background()

	doc := exportConfig(t, store, ConfigExportOptions{})
	doc.Devices[0].MIBs = []ConfigAssignment{
		{Module: "CISCO-MEMORY-POOL-MIB", MIBAssignment: MIBAssignment{Enabled: true}},
		{Module: "ENTITY-MIB", MIBAssignment: MIBAssignment{Enabled: true}},
	}

	before := dumpStore(t, store)
	_, err := store.ImportConfig(ctx, doc)

	var missing *ConfigMIBMissingError
	if !errors.As(err, &missing) {
		t.Fatalf("ImportConfig error = %v, want *ConfigMIBMissingError", err)
	}
	want := []string{"CISCO-MEMORY-POOL-MIB", "ENTITY-MIB"}
	if !reflect.DeepEqual(missing.Modules, want) {
		t.Errorf("missing modules = %v, want %v", missing.Modules, want)
	}
	for _, module := range want {
		if !strings.Contains(err.Error(), module) {
			t.Errorf("error message does not name %s: %s", module, err)
		}
	}
	if got := dumpStore(t, store); !reflect.DeepEqual(got, before) {
		t.Error("a rejected import modified the store")
	}
}

func TestImportConfigRejectsAnUnreadableSchemaVersion(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	seedConfig(t, store)

	before := dumpStore(t, store)
	doc := exportConfig(t, store, ConfigExportOptions{})
	doc.SchemaVersion = ConfigSchemaVersion + 1

	_, err := store.ImportConfig(context.Background(), doc)

	var schemaErr *ConfigSchemaError
	if !errors.As(err, &schemaErr) {
		t.Fatalf("ImportConfig error = %v, want *ConfigSchemaError", err)
	}
	if schemaErr.Found != ConfigSchemaVersion+1 || schemaErr.Supported != ConfigSchemaVersion {
		t.Errorf("schema error = %+v", schemaErr)
	}
	if got := dumpStore(t, store); !reflect.DeepEqual(got, before) {
		t.Error("a document with an unreadable schema version modified the store")
	}
}

func TestImportConfigRejectsInvalidFieldsBeforeWriting(t *testing.T) {
	badInterval := 0
	tests := []struct {
		name       string
		mutate     func(doc *Config)
		wantField  string
		wantReason string
	}{
		{
			name: "a filter that is not a regular expression",
			mutate: func(doc *Config) {
				doc.Profiles[0].MIBs[0].IncludeNames = []string{"^if(In"}
			},
			wantField:  "mibs[IF-MIB].include_names",
			wantReason: "not a valid regular expression",
		},
		{
			name: "an SNMP version nothing can poll",
			mutate: func(doc *Config) {
				doc.Devices[0].SNMPVersion = "v4"
			},
			wantField:  "snmp_version",
			wantReason: "is not a supported SNMP version",
		},
		{
			name: "a poll interval that is not a schedule",
			mutate: func(doc *Config) {
				doc.Devices[0].PollInterval = &badInterval
			},
			wantField:  "poll_interval",
			wantReason: "is not a schedule",
		},
		{
			name: "a port outside the range a socket has",
			mutate: func(doc *Config) {
				doc.Devices[0].Port = 70000
			},
			wantField:  "port",
			wantReason: "must be between 1 and 65535",
		},
		{
			name: "a negative object cap",
			mutate: func(doc *Config) {
				doc.Profiles[0].MIBs[0].MaxOIDs = -1
			},
			wantField:  "mibs[IF-MIB].max_oids",
			wantReason: "must not be negative",
		},
		{
			name: "two devices sharing a name",
			mutate: func(doc *Config) {
				doc.Devices = append(doc.Devices, doc.Devices[0])
			},
			wantField:  "name",
			wantReason: "used by more than one record",
		},
		{
			name: "a device naming a profile the document does not define",
			mutate: func(doc *Config) {
				doc.Devices[1].Profiles = []string{"nonexistent"}
			},
			wantField:  "profiles",
			wantReason: "does not define",
		},
		{
			name: "a module assigned twice to one device",
			mutate: func(doc *Config) {
				doc.Devices[1].MIBs = append(doc.Devices[1].MIBs, doc.Devices[1].MIBs[0])
			},
			wantField:  "mibs.module",
			wantReason: "assigned more than once",
		},
		{
			name: "a device with no address",
			mutate: func(doc *Config) {
				doc.Devices[0].Address = "  "
			},
			wantField:  "address",
			wantReason: "must not be empty",
		},
		{
			name: "a profile with no name",
			mutate: func(doc *Config) {
				doc.Profiles[0].Name = ""
			},
			wantField:  "name",
			wantReason: "must not be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, cleanup := setupTestDB(t)
			defer cleanup()
			seedConfig(t, store)

			before := dumpStore(t, store)
			doc := exportConfig(t, store, ConfigExportOptions{})
			tt.mutate(doc)

			_, err := store.ImportConfig(context.Background(), doc)

			var invalid *ConfigValidationError
			if !errors.As(err, &invalid) {
				t.Fatalf("ImportConfig error = %v, want *ConfigValidationError", err)
			}
			if !hasProblem(invalid.Problems, tt.wantField, tt.wantReason) {
				t.Errorf("problems = %+v, want one naming field %q because %q",
					invalid.Problems, tt.wantField, tt.wantReason)
			}
			if got := dumpStore(t, store); !reflect.DeepEqual(got, before) {
				t.Error("a rejected import modified the store")
			}
		})
	}
}

func TestValidateConfigRejectsAMissingDocument(t *testing.T) {
	var invalid *ConfigValidationError
	if err := ValidateConfig(nil); !errors.As(err, &invalid) {
		t.Fatalf("ValidateConfig(nil) = %v, want *ConfigValidationError", err)
	}
}

func TestValidateConfigAcceptsEveryStoredSNMPVersionSpelling(t *testing.T) {
	for _, version := range []string{"1", "v1", "2c", "V2C", "3", "v3"} {
		doc := &Config{
			SchemaVersion: ConfigSchemaVersion,
			Devices: []ConfigDevice{
				{Name: "d", Address: "192.0.2.1", Port: 161, SNMPVersion: version},
			},
		}
		if err := ValidateConfig(doc); err != nil {
			t.Errorf("ValidateConfig rejected snmp_version %q: %v", version, err)
		}
	}
}

func TestValidateMIBAssignment(t *testing.T) {
	valid := MIBAssignment{Enabled: true, IncludeOIDs: []string{"^1\\.3\\."}, MaxOIDs: 10}
	if err := ValidateMIBAssignment(valid); err != nil {
		t.Fatalf("ValidateMIBAssignment(valid) = %v, want nil", err)
	}

	tests := []struct {
		name      string
		in        MIBAssignment
		wantField string
	}{
		{"a broken OID pattern", MIBAssignment{IncludeOIDs: []string{"("}}, "include_oids"},
		{"a broken OID exclusion", MIBAssignment{ExcludeOIDs: []string{"["}}, "exclude_oids"},
		{"a broken name pattern", MIBAssignment{IncludeNames: []string{"*"}}, "include_names"},
		{"a broken name exclusion", MIBAssignment{ExcludeNames: []string{"(?P<"}}, "exclude_names"},
		{"a negative cap", MIBAssignment{MaxOIDs: -5}, "max_oids"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var fieldErr *AssignmentFieldError
			if err := ValidateMIBAssignment(tt.in); !errors.As(err, &fieldErr) {
				t.Fatalf("error = %v, want *AssignmentFieldError", err)
			}
			if fieldErr.Field != tt.wantField {
				t.Errorf("field = %q, want %q", fieldErr.Field, tt.wantField)
			}
			if !strings.Contains(fieldErr.Error(), tt.wantField) {
				t.Errorf("message %q does not name the field", fieldErr)
			}
		})
	}
}

func TestConfigErrorMessagesNameTheProblem(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want []string
	}{
		{
			name: "an unreadable schema version",
			err:  &ConfigSchemaError{Found: 9, Supported: 1},
			want: []string{"schema_version 9", "supports version 1"},
		},
		{
			name: "a store failure",
			err:  &ConfigStoreError{Op: "scan dev:", Err: errors.New("disk gone")},
			want: []string{"scan dev:", "disk gone"},
		},
		{
			name: "a field problem",
			err:  &ConfigProblem{Scope: "device", Subject: "bastion", Field: "port", Value: "0", Reason: "bad"},
			want: []string{"device", "bastion", "port", "bad"},
		},
		{
			name: "a document-level problem",
			err:  &ConfigProblem{Scope: "document", Reason: "nothing supplied"},
			want: []string{"document", "nothing supplied"},
		},
		{
			name: "a set of problems",
			err: &ConfigValidationError{Problems: []ConfigProblem{
				{Scope: "device", Subject: "a", Reason: "one"},
				{Scope: "device", Subject: "b", Reason: "two"},
			}},
			want: []string{"2 problem(s)", "one", "two"},
		},
		{
			name: "missing modules",
			err:  &ConfigMIBMissingError{Modules: []string{"IF-MIB"}},
			want: []string{"IF-MIB", "not imported"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			message := tt.err.Error()
			for _, fragment := range tt.want {
				if !strings.Contains(message, fragment) {
					t.Errorf("message %q does not contain %q", message, fragment)
				}
			}
		})
	}
}

func TestConfigStoreErrorUnwraps(t *testing.T) {
	inner := errors.New("boom")
	if !errors.Is(&ConfigStoreError{Op: "x", Err: inner}, inner) {
		t.Error("ConfigStoreError does not unwrap to its cause")
	}
}

func TestImportConfigRejectsAnUnreadableDocumentOnAClosedStore(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	seedConfig(t, store)

	doc := exportConfig(t, store, ConfigExportOptions{})
	doc.Devices = nil
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// A store that is gone must surface as an error rather than a panic or a
	// silently empty configuration.
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Log("closed store returned an error rather than panicking, which is fine")
		}
	}()
	if _, err := store.ImportConfig(context.Background(), doc); err == nil {
		t.Error("ImportConfig on a closed store returned no error")
	}
}

func TestImportConfigAllocatesIDsAboveEveryIDInUse(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	seedConfig(t, store)
	ctx := context.Background()

	doc := exportConfig(t, store, ConfigExportOptions{})
	doc.Devices = append(doc.Devices, ConfigDevice{
		Name: "newswitch", Address: "192.0.2.9", SNMPVersion: "2c",
	})
	if _, err := store.ImportConfig(ctx, doc); err != nil {
		t.Fatalf("ImportConfig: %v", err)
	}

	// A device created after the import must not be handed an ID the import
	// already used, which is what a counter left behind would cause.
	extra := &Device{Name: "later", Address: "192.0.2.10", SNMPVersion: "2c"}
	if err := store.CreateDevice(ctx, extra); err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}

	devices, err := store.ListDevices(ctx)
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	seen := make(map[int64]string, len(devices))
	for _, d := range devices {
		if other, clash := seen[d.ID]; clash {
			t.Fatalf("devices %s and %s share ID %d", other, d.Name, d.ID)
		}
		seen[d.ID] = d.Name
	}
	if len(devices) != 4 {
		t.Errorf("devices = %d, want 4", len(devices))
	}
}

func TestImportConfigNormalizesTheDefaultPort(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	doc := &Config{
		SchemaVersion: ConfigSchemaVersion,
		Devices: []ConfigDevice{
			{Name: "bastion", Address: "192.0.2.14", SNMPVersion: "2c"},
		},
	}
	if _, err := store.ImportConfig(ctx, doc); err != nil {
		t.Fatalf("ImportConfig: %v", err)
	}

	stored, err := store.GetDeviceByName(ctx, "bastion")
	if err != nil {
		t.Fatalf("GetDeviceByName: %v", err)
	}
	if stored.Port != DefaultDevicePort {
		t.Errorf("port = %d, want the default %d", stored.Port, DefaultDevicePort)
	}
}

// editDevices applies the changes the add/update/delete plan test expects.
func editDevices(devices []ConfigDevice) []ConfigDevice {
	edited := make([]ConfigDevice, 0, len(devices))
	for _, d := range devices {
		if d.Name == "bastion" {
			continue
		}
		if d.Name == "core-01" {
			d.Address = "192.0.2.253"
			d.PollInterval = nil
			d.MIBs = []ConfigAssignment{{
				Module: "HOST-RESOURCES-MIB", MIBAssignment: MIBAssignment{Enabled: false},
			}}
		}
		edited = append(edited, d)
	}
	return edited
}

// changesByName indexes a plan's changes for lookup by record name.
func changesByName(changes []ConfigChange) map[string]ConfigChange {
	byName := make(map[string]ConfigChange, len(changes))
	for _, c := range changes {
		byName[c.Name] = c
	}
	return byName
}

// assertFieldChanges checks that exactly the expected fields moved, and to the
// expected values.
func assertFieldChanges(t *testing.T, got []ConfigFieldChange, want map[string][2]string) {
	t.Helper()

	seen := make(map[string][2]string, len(got))
	for _, change := range got {
		seen[change.Field] = [2]string{change.From, change.To}
	}
	if !reflect.DeepEqual(seen, want) {
		t.Errorf("field changes = %+v, want %+v", seen, want)
	}
}

// hasProblem reports whether any problem names the field and gives the reason.
func hasProblem(problems []ConfigProblem, field, reason string) bool {
	for _, p := range problems {
		if p.Field == field && strings.Contains(p.Reason, reason) {
			return true
		}
	}
	return false
}

func TestParseJoinKeyRejectsMalformedKeys(t *testing.T) {
	valid := PrefixDevMIB + formatID(3) + ":" + formatID(7)
	ownerID, mibID, ok := parseJoinKey(valid, PrefixDevMIB)
	if !ok || ownerID != 3 || mibID != 7 {
		t.Fatalf("parseJoinKey(%q) = (%d, %d, %v)", valid, ownerID, mibID, ok)
	}

	tests := []struct {
		name string
		key  string
	}{
		{"a key under another prefix", PrefixProfMIB + formatID(1) + ":" + formatID(2)},
		{"a key with only one ID", PrefixDevMIB + formatID(1)},
		{"an owner that is not a number", PrefixDevMIB + "abc:" + formatID(2)},
		{"a MIB that is not a number", PrefixDevMIB + formatID(1) + ":xyz"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, parsed := parseJoinKey(tt.key, PrefixDevMIB); parsed {
				t.Errorf("parseJoinKey(%q) accepted a malformed key", tt.key)
			}
		})
	}
}

func TestExportConfigLeavesOutAnAssignmentWhoseModuleIsGone(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	seedConfig(t, store)
	ctx := context.Background()

	device, err := store.GetDeviceByName(ctx, "bastion")
	if err != nil {
		t.Fatalf("GetDeviceByName: %v", err)
	}

	// A join row pointing at a module the catalog no longer holds. Carrying it
	// into the document would produce a file that fails its own validation, so
	// the export drops it.
	dangling := []byte(PrefixDevMIB + formatID(device.ID) + ":" + formatID(4242))
	if err := store.db.Set(dangling, []byte{1}, pebble.Sync); err != nil {
		t.Fatalf("seed dangling assignment: %v", err)
	}

	doc := exportConfig(t, store, ConfigExportOptions{})
	for _, d := range doc.Devices {
		if d.Name == "bastion" && len(d.MIBs) != 0 {
			t.Errorf("device bastion exported %d assignments, want none", len(d.MIBs))
		}
	}

	// The document it produced must still import cleanly, which is the whole
	// reason the dangling row is dropped rather than reported.
	if _, err := store.PlanImportConfig(ctx, doc, nil); err != nil {
		t.Errorf("the exported document does not validate: %v", err)
	}
}

func TestPlanImportConfigTreatsPendingModulesAsAvailable(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	doc := &Config{
		SchemaVersion: ConfigSchemaVersion,
		MIBs:          []ConfigMIB{{Name: "IF-MIB"}},
		Devices: []ConfigDevice{{
			Name: "core-01", Address: "192.0.2.1", SNMPVersion: "2c",
			MIBs: []ConfigAssignment{{Module: "IF-MIB", MIBAssignment: MIBAssignment{Enabled: true}}},
		}},
	}

	// Without the module, and without being told it is coming, the plan is
	// refused: that is the guarantee that an assignment is never silently lost.
	var missing *ConfigMIBMissingError
	if _, err := store.PlanImportConfig(ctx, doc, nil); !errors.As(err, &missing) {
		t.Fatalf("error = %v, want *ConfigMIBMissingError", err)
	}

	// A caller that is about to import the module — an export bundle carrying
	// its source — gets the plan it will actually apply.
	plan, err := store.PlanImportConfig(ctx, doc, []string{"IF-MIB"})
	if err != nil {
		t.Fatalf("PlanImportConfig with a pending module: %v", err)
	}
	if plan.Counts.DevicesCreated != 1 {
		t.Errorf("plan counts = %+v, want one device created", plan.Counts)
	}
}

func TestConfigPlanChangedReportsAnEmptyPlan(t *testing.T) {
	if (&ConfigPlan{}).Changed() {
		t.Error("an empty plan reports changes")
	}
	if !(&ConfigPlan{Devices: []ConfigChange{{Action: ConfigActionDelete}}}).Changed() {
		t.Error("a plan with a deletion reports no changes")
	}
}

func TestPlanOrdersDestructiveChangesFirst(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	seedConfig(t, store)

	doc := exportConfig(t, store, ConfigExportOptions{})
	doc.Devices = editDevices(doc.Devices)
	doc.Devices = append(doc.Devices, ConfigDevice{
		Name: "aaa-new", Address: "192.0.2.9", SNMPVersion: "2c",
	})

	plan, err := store.PlanImportConfig(context.Background(), doc, nil)
	if err != nil {
		t.Fatalf("PlanImportConfig: %v", err)
	}

	// The deletion has to be read first even though its name sorts last: it is
	// the part an operator has to agree to.
	actions := make([]string, 0, len(plan.Devices))
	for _, change := range plan.Devices {
		actions = append(actions, change.Action)
	}
	want := []string{ConfigActionDelete, ConfigActionUpdate, ConfigActionCreate}
	if !reflect.DeepEqual(actions, want) {
		t.Errorf("actions = %v, want %v", actions, want)
	}
}

func TestAssignmentTextDescribesEveryState(t *testing.T) {
	tests := []struct {
		name string
		in   MIBAssignment
		want string
	}{
		{"polled in full", MIBAssignment{Enabled: true}, "enabled"},
		{"paused", MIBAssignment{}, "disabled"},
		{
			"polled but narrowed",
			MIBAssignment{Enabled: true, IncludeNames: []string{"^ifIn"}},
			"enabled, filtered",
		},
		{
			"paused with its filters kept",
			MIBAssignment{MaxOIDs: 10},
			"disabled, filtered",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := assignmentText(tt.in); got != tt.want {
				t.Errorf("assignmentText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateConfigRejectsAnEmptyModuleName(t *testing.T) {
	doc := &Config{
		SchemaVersion: ConfigSchemaVersion,
		Profiles: []ConfigProfile{{
			Name: "core",
			MIBs: []ConfigAssignment{{Module: "  ", MIBAssignment: MIBAssignment{Enabled: true}}},
		}},
	}

	var invalid *ConfigValidationError
	if err := ValidateConfig(doc); !errors.As(err, &invalid) {
		t.Fatalf("error = %v, want *ConfigValidationError", err)
	}
	if !hasProblem(invalid.Problems, "mibs.module", "must not be empty") {
		t.Errorf("problems = %+v", invalid.Problems)
	}
}

func TestCounterValueIgnoresAMalformedCounter(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// A counter of the wrong width cannot be trusted as a number, so the import
	// falls back to the highest ID it can see rather than reading garbage.
	if err := store.db.Set([]byte(CounterDevice), []byte{1, 2, 3}, pebble.Sync); err != nil {
		t.Fatalf("seed counter: %v", err)
	}

	value, err := store.counterValue(CounterDevice)
	if err != nil {
		t.Fatalf("counterValue: %v", err)
	}
	if value != 0 {
		t.Errorf("counterValue() = %d, want 0 for an unreadable counter", value)
	}

	missing, err := store.counterValue(CounterProfile)
	if err != nil {
		t.Fatalf("counterValue: %v", err)
	}
	if missing != 0 {
		t.Errorf("counterValue() = %d, want 0 when no counter has been written", missing)
	}
}

func TestImportConfigCreatesAProfileNoDeviceUses(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	mib := &MIB{Name: "IF-MIB", FilePath: "/mibs/IF-MIB.txt"}
	if err := store.CreateMIB(ctx, mib); err != nil {
		t.Fatalf("CreateMIB: %v", err)
	}

	doc := &Config{
		SchemaVersion: ConfigSchemaVersion,
		MIBs:          []ConfigMIB{{Name: "IF-MIB"}},
		Profiles: []ConfigProfile{{
			Name: "unused", Description: "attached to nothing yet",
			MIBs: []ConfigAssignment{{Module: "IF-MIB", MIBAssignment: MIBAssignment{Enabled: true}}},
		}},
	}
	if _, err := store.ImportConfig(ctx, doc); err != nil {
		t.Fatalf("ImportConfig: %v", err)
	}

	restored := exportConfig(t, store, ConfigExportOptions{})
	if len(restored.Profiles) != 1 || restored.Profiles[0].Name != "unused" {
		t.Fatalf("profiles = %+v", restored.Profiles)
	}
	if len(restored.Devices) != 0 {
		t.Errorf("devices = %+v, want none", restored.Devices)
	}
}
