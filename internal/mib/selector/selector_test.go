package selector

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/mib/storage"
)

func setupTestStorage(t *testing.T) (*storage.Storage, func()) {
	tmpDir := filepath.Join(os.TempDir(), "selector_test")
	os.RemoveAll(tmpDir)

	store, err := storage.New(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	cleanup := func() {
		store.Close()
		os.RemoveAll(tmpDir)
	}

	return store, cleanup
}

func createTestOIDs() []*mib.OIDMetadata {
	return []*mib.OIDMetadata{
		{
			OID:       "1.3.6.1.2.1.1.1.0",
			Name:      "sysDescr",
			Module:    "SNMPv2-MIB",
			Type:      "OctetString",
			Access:    "read-only",
			Status:    "current",
			IsScalar:  true,
			Pollable:  true,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
			Tags:      make(map[string]string),
		},
		{
			OID:       "1.3.6.1.2.1.1.3.0",
			Name:      "sysUpTime",
			Module:    "SNMPv2-MIB",
			Type:      "TimeTicks",
			Access:    "read-only",
			Status:    "current",
			IsScalar:  true,
			Pollable:  true,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
			Tags:      make(map[string]string),
		},
		{
			OID:       "1.3.6.1.2.1.2.1.0",
			Name:      "ifNumber",
			Module:    "IF-MIB",
			Type:      "Integer32",
			Access:    "read-only",
			Status:    "current",
			IsScalar:  true,
			Pollable:  true,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
			Tags:      make(map[string]string),
		},
		{
			OID:       "1.3.6.1.2.1.2.2",
			Name:      "ifTable",
			Module:    "IF-MIB",
			Type:      "Table",
			Access:    "not-accessible",
			Status:    "current",
			IsTable:   true,
			Pollable:  false,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
			Tags:      make(map[string]string),
		},
		{
			OID:       "1.3.6.1.2.1.2.2.1.10",
			Name:      "ifInOctets",
			Module:    "IF-MIB",
			Type:      "Counter32",
			Access:    "read-only",
			Status:    "current",
			IsColumn:  true,
			Pollable:  true,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
			Tags:      make(map[string]string),
		},
	}
}

func TestNew(t *testing.T) {
	store, cleanup := setupTestStorage(t)
	defer cleanup()

	selector := New(store)
	if selector == nil {
		t.Fatal("Expected selector, got nil")
	}
	if selector.storage == nil {
		t.Fatal("Expected storage to be set")
	}
}

func TestSelector_SelectOIDs_NilProfile(t *testing.T) {
	store, cleanup := setupTestStorage(t)
	defer cleanup()

	selector := New(store)
	_, err := selector.SelectOIDs(nil)
	if err == nil {
		t.Error("Expected error for nil profile")
	}
}

func TestSelector_SelectOIDs_DefaultProfile(t *testing.T) {
	store, cleanup := setupTestStorage(t)
	defer cleanup()

	// Store test OIDs
	testOIDs := createTestOIDs()
	for _, oid := range testOIDs {
		if err := store.StoreOID(oid); err != nil {
			t.Fatalf("Failed to store OID: %v", err)
		}
	}

	selector := New(store)
	profile := GetDefaultProfile()

	oids, err := selector.SelectOIDs(profile)
	if err != nil {
		t.Fatalf("SelectOIDs failed: %v", err)
	}

	if len(oids) == 0 {
		t.Error("Expected OIDs, got none")
	}

	// Should not include table OIDs
	for _, oid := range oids {
		if oid.IsTable {
			t.Errorf("Found table OID %s, should be excluded", oid.Name)
		}
	}

	t.Logf("Selected %d OIDs with default profile", len(oids))
}

func TestSelector_SelectOIDs_WithModuleFilter(t *testing.T) {
	store, cleanup := setupTestStorage(t)
	defer cleanup()

	// Store test OIDs
	testOIDs := createTestOIDs()
	for _, oid := range testOIDs {
		if err := store.StoreOID(oid); err != nil {
			t.Fatalf("Failed to store OID: %v", err)
		}
	}

	selector := New(store)
	profile := &DeviceProfile{
		Name:    "test",
		Modules: []string{"SNMPv2-MIB"},
	}

	oids, err := selector.SelectOIDs(profile)
	if err != nil {
		t.Fatalf("SelectOIDs failed: %v", err)
	}

	// Should only include SNMPv2-MIB OIDs
	for _, oid := range oids {
		if oid.Module != "SNMPv2-MIB" {
			t.Errorf("Found OID from wrong module: %s", oid.Module)
		}
	}

	if len(oids) != 2 {
		t.Errorf("Expected 2 OIDs from SNMPv2-MIB, got %d", len(oids))
	}
}

func TestSelector_SelectOIDs_WithIncludePattern(t *testing.T) {
	store, cleanup := setupTestStorage(t)
	defer cleanup()

	// Store test OIDs
	testOIDs := createTestOIDs()
	for _, oid := range testOIDs {
		if err := store.StoreOID(oid); err != nil {
			t.Fatalf("Failed to store OID: %v", err)
		}
	}

	selector := New(store)
	profile := &DeviceProfile{
		Name:         "test",
		Modules:      []string{"SNMPv2-MIB", "IF-MIB"},
		IncludeNames: []string{"^sys.*"}, // Only include OIDs starting with "sys"
	}

	oids, err := selector.SelectOIDs(profile)
	if err != nil {
		t.Fatalf("SelectOIDs failed: %v", err)
	}

	// Should only include sysDescr and sysUpTime
	for _, oid := range oids {
		if oid.Name != "sysDescr" && oid.Name != "sysUpTime" {
			t.Errorf("Found unexpected OID: %s", oid.Name)
		}
	}

	if len(oids) != 2 {
		t.Errorf("Expected 2 OIDs matching pattern, got %d", len(oids))
	}
}

func TestSelector_SelectOIDs_WithExcludePattern(t *testing.T) {
	store, cleanup := setupTestStorage(t)
	defer cleanup()

	// Store test OIDs
	testOIDs := createTestOIDs()
	for _, oid := range testOIDs {
		if err := store.StoreOID(oid); err != nil {
			t.Fatalf("Failed to store OID: %v", err)
		}
	}

	selector := New(store)
	profile := &DeviceProfile{
		Name:         "test",
		Modules:      []string{"SNMPv2-MIB"},
		ExcludeNames: []string{".*UpTime.*"}, // Exclude UpTime OIDs
	}

	oids, err := selector.SelectOIDs(profile)
	if err != nil {
		t.Fatalf("SelectOIDs failed: %v", err)
	}

	// Should not include sysUpTime
	for _, oid := range oids {
		if oid.Name == "sysUpTime" {
			t.Error("Found excluded OID: sysUpTime")
		}
	}

	if len(oids) != 1 {
		t.Errorf("Expected 1 OID after exclusion, got %d", len(oids))
	}
}

func TestSelector_SelectOIDs_WithMaxLimit(t *testing.T) {
	store, cleanup := setupTestStorage(t)
	defer cleanup()

	// Store test OIDs
	testOIDs := createTestOIDs()
	for _, oid := range testOIDs {
		if err := store.StoreOID(oid); err != nil {
			t.Fatalf("Failed to store OID: %v", err)
		}
	}

	selector := New(store)
	profile := &DeviceProfile{
		Name:    "test",
		Modules: []string{"SNMPv2-MIB", "IF-MIB"},
		MaxOIDs: 2,
	}

	oids, err := selector.SelectOIDs(profile)
	if err != nil {
		t.Fatalf("SelectOIDs failed: %v", err)
	}

	if len(oids) > 2 {
		t.Errorf("Expected max 2 OIDs, got %d", len(oids))
	}
}

func TestSelector_SelectTableColumns(t *testing.T) {
	store, cleanup := setupTestStorage(t)
	defer cleanup()

	// Store table info
	table := &mib.TableInfo{
		OID:     "1.3.6.1.2.1.2.2",
		Name:    "ifTable",
		Module:  "IF-MIB",
		Columns: []string{"1.3.6.1.2.1.2.2.1.10", "1.3.6.1.2.1.2.2.1.16"},
		Indexes: []string{"ifIndex"},
	}
	if err := store.StoreTable(table); err != nil {
		t.Fatalf("Failed to store table: %v", err)
	}

	// Store column OIDs
	col1 := &mib.OIDMetadata{
		OID:       "1.3.6.1.2.1.2.2.1.10",
		Name:      "ifInOctets",
		Module:    "IF-MIB",
		Type:      "Counter32",
		Access:    "read-only",
		Status:    "current",
		IsColumn:  true,
		Pollable:  true,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Tags:      make(map[string]string),
	}
	col2 := &mib.OIDMetadata{
		OID:       "1.3.6.1.2.1.2.2.1.16",
		Name:      "ifOutOctets",
		Module:    "IF-MIB",
		Type:      "Counter32",
		Access:    "read-only",
		Status:    "current",
		IsColumn:  true,
		Pollable:  true,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Tags:      make(map[string]string),
	}

	if err := store.StoreOID(col1); err != nil {
		t.Fatalf("Failed to store column: %v", err)
	}
	if err := store.StoreOID(col2); err != nil {
		t.Fatalf("Failed to store column: %v", err)
	}

	selector := New(store)

	// Test selecting all columns
	cols, err := selector.SelectTableColumns("IF-MIB", "ifTable", nil)
	if err != nil {
		t.Fatalf("SelectTableColumns failed: %v", err)
	}

	if len(cols) != 2 {
		t.Errorf("Expected 2 columns, got %d", len(cols))
	}

	// Test selecting specific column
	cols, err = selector.SelectTableColumns("IF-MIB", "ifTable", []string{"ifInOctets"})
	if err != nil {
		t.Fatalf("SelectTableColumns failed: %v", err)
	}

	if len(cols) != 1 {
		t.Errorf("Expected 1 column, got %d", len(cols))
	}
	if cols[0].Name != "ifInOctets" {
		t.Errorf("Expected ifInOctets, got %s", cols[0].Name)
	}
}

func TestGetDefaultProfile(t *testing.T) {
	profile := GetDefaultProfile()
	if profile == nil {
		t.Fatal("Expected profile, got nil")
	}
	if profile.Name != "default" {
		t.Errorf("Expected name 'default', got %s", profile.Name)
	}
	if len(profile.Modules) == 0 {
		t.Error("Expected modules, got none")
	}
}

func TestGetCiscoProfile(t *testing.T) {
	profile := GetCiscoProfile()
	if profile == nil {
		t.Fatal("Expected profile, got nil")
	}
	if profile.Name != "cisco" {
		t.Errorf("Expected name 'cisco', got %s", profile.Name)
	}
}

func TestGetInterfaceProfile(t *testing.T) {
	profile := GetInterfaceProfile()
	if profile == nil {
		t.Fatal("Expected profile, got nil")
	}
	if profile.Name != "interfaces" {
		t.Errorf("Expected name 'interfaces', got %s", profile.Name)
	}
}

func TestValidateProfile(t *testing.T) {
	tests := []struct {
		name    string
		profile *DeviceProfile
		wantErr bool
	}{
		{
			name:    "nil profile",
			profile: nil,
			wantErr: true,
		},
		{
			name: "empty name",
			profile: &DeviceProfile{
				Name: "",
			},
			wantErr: true,
		},
		{
			name: "valid profile",
			profile: &DeviceProfile{
				Name:    "test",
				Modules: []string{"SNMPv2-MIB"},
			},
			wantErr: false,
		},
		{
			name: "invalid include pattern",
			profile: &DeviceProfile{
				Name:        "test",
				IncludeOIDs: []string{"[invalid"},
			},
			wantErr: true,
		},
		{
			name: "negative max OIDs",
			profile: &DeviceProfile{
				Name:    "test",
				MaxOIDs: -1,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateProfile(tt.profile)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateProfile() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSelector_SearchOIDs_ByOIDPrefix(t *testing.T) {
	store, cleanup := setupTestStorage(t)
	defer cleanup()

	// Store test OIDs
	testOIDs := createTestOIDs()
	for _, oid := range testOIDs {
		if err := store.StoreOID(oid); err != nil {
			t.Fatalf("Failed to store OID: %v", err)
		}
	}

	selector := New(store)

	// Search by OID prefix
	oids, err := selector.SearchOIDs("1.3.6.1.2.1.1")
	if err != nil {
		t.Fatalf("SearchOIDs failed: %v", err)
	}

	if len(oids) != 2 {
		t.Errorf("Expected 2 OIDs with prefix 1.3.6.1.2.1.1, got %d", len(oids))
	}
}

func TestSelector_SearchOIDs_ByNamePattern(t *testing.T) {
	store, cleanup := setupTestStorage(t)
	defer cleanup()

	// Store test OIDs
	testOIDs := createTestOIDs()
	for _, oid := range testOIDs {
		if err := store.StoreOID(oid); err != nil {
			t.Fatalf("Failed to store OID: %v", err)
		}
	}

	selector := New(store)

	// Search by name pattern
	oids, err := selector.SearchOIDs("^if.*")
	if err != nil {
		t.Fatalf("SearchOIDs failed: %v", err)
	}

	for _, oid := range oids {
		if oid.Name[0:2] != "if" {
			t.Errorf("Found OID not matching pattern: %s", oid.Name)
		}
	}

	t.Logf("Found %d OIDs matching pattern", len(oids))
}

func TestSelector_GetStatistics(t *testing.T) {
	store, cleanup := setupTestStorage(t)
	defer cleanup()

	// Store test OIDs
	testOIDs := createTestOIDs()
	for _, oid := range testOIDs {
		if err := store.StoreOID(oid); err != nil {
			t.Fatalf("Failed to store OID: %v", err)
		}
	}

	selector := New(store)
	profile := GetDefaultProfile()

	stats, err := selector.GetStatistics(profile)
	if err != nil {
		t.Fatalf("GetStatistics failed: %v", err)
	}

	if stats.TotalOIDs == 0 {
		t.Error("Expected OIDs in statistics")
	}

	if len(stats.ByModule) == 0 {
		t.Error("Expected module statistics")
	}

	t.Logf("Statistics:\n%s", stats.String())
}

func TestIsOIDPrefix(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"valid OID", "1.3.6.1.2.1.1", true},
		{"single digit", "1", true},
		{"with trailing dot", "1.3.6.", true},
		{"empty string", "", false},
		{"starts with letter", "a.1.2", false},
		{"contains letter", "1.a.2", false},
		{"name pattern", "sysDescr", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isOIDPrefix(tt.input)
			if got != tt.want {
				t.Errorf("isOIDPrefix(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestSelector_ListAvailableModules(t *testing.T) {
	store, cleanup := setupTestStorage(t)
	defer cleanup()

	// Store module info
	moduleInfo := &mib.ModuleInfo{
		Name:     "TEST-MIB",
		OIDCount: 10,
		LoadedAt: time.Now(),
		Tags:     make(map[string]string),
	}
	if err := store.StoreModule(moduleInfo); err != nil {
		t.Fatalf("Failed to store module: %v", err)
	}

	selector := New(store)
	modules, err := selector.ListAvailableModules()
	if err != nil {
		t.Fatalf("ListAvailableModules failed: %v", err)
	}

	if len(modules) == 0 {
		t.Error("Expected modules, got none")
	}

	found := false
	for _, mod := range modules {
		if mod.Name == "TEST-MIB" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected to find TEST-MIB")
	}
}
