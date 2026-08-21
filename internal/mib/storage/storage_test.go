package storage

import (
	"fmt"
	"testing"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/mib"
)

func getTestStorage(t *testing.T) (*Storage, func()) {
	// t.TempDir is removed by the framework even if the test panics or fails
	// early, which a manual RemoveAll is not.
	tmpDir := t.TempDir()

	storage, err := New(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	cleanup := func() {
		_ = storage.Close()
	}

	return storage, cleanup
}

func TestNew(t *testing.T) {
	tmpDir := t.TempDir()

	storage, err := New(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer storage.Close()

	if storage == nil {
		t.Fatal("Expected storage to be created")
	}

	if storage.db == nil {
		t.Error("Expected db to be initialized")
	}
}

func TestStorage_StoreAndGetOID(t *testing.T) {
	storage, cleanup := getTestStorage(t)
	defer cleanup()

	meta := &mib.OIDMetadata{
		OID:         "1.3.6.1.2.1.1.1.0",
		Name:        "sysDescr",
		Module:      "SNMPv2-MIB",
		Type:        "OctetString",
		Access:      "read-only",
		Status:      "current",
		Description: "System description",
		IsScalar:    true,
		Pollable:    true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	// Store OID
	if err := storage.StoreOID(meta); err != nil {
		t.Fatalf("Failed to store OID: %v", err)
	}

	// Retrieve OID
	retrieved, err := storage.GetOID("1.3.6.1.2.1.1.1.0")
	if err != nil {
		t.Fatalf("Failed to get OID: %v", err)
	}

	if retrieved.Name != "sysDescr" {
		t.Errorf("Expected name 'sysDescr', got '%s'", retrieved.Name)
	}

	if retrieved.Module != "SNMPv2-MIB" {
		t.Errorf("Expected module 'SNMPv2-MIB', got '%s'", retrieved.Module)
	}

	if retrieved.Type != "OctetString" {
		t.Errorf("Expected type 'OctetString', got '%s'", retrieved.Type)
	}
}

func TestStorage_GetOIDByName(t *testing.T) {
	storage, cleanup := getTestStorage(t)
	defer cleanup()

	meta := &mib.OIDMetadata{
		OID:    "1.3.6.1.2.1.1.1.0",
		Name:   "sysDescr",
		Module: "SNMPv2-MIB",
		Type:   "OctetString",
		Access: "read-only",
		Status: "current",
	}

	if err := storage.StoreOID(meta); err != nil {
		t.Fatalf("Failed to store OID: %v", err)
	}

	// Retrieve by name
	retrieved, err := storage.GetOIDByName("SNMPv2-MIB", "sysDescr")
	if err != nil {
		t.Fatalf("Failed to get OID by name: %v", err)
	}

	if retrieved.OID != "1.3.6.1.2.1.1.1.0" {
		t.Errorf("Expected OID '1.3.6.1.2.1.1.1.0', got '%s'", retrieved.OID)
	}
}

func TestStorage_GetOID_NotFound(t *testing.T) {
	storage, cleanup := getTestStorage(t)
	defer cleanup()

	_, err := storage.GetOID("1.2.3.4.5")
	if err == nil {
		t.Error("Expected error for non-existent OID")
	}
}

func TestStorage_ListOIDs(t *testing.T) {
	storage, cleanup := getTestStorage(t)
	defer cleanup()

	// Store multiple OIDs
	oids := []*mib.OIDMetadata{
		{OID: "1.3.6.1.2.1.1.1.0", Name: "sysDescr", Module: "SNMPv2-MIB", Type: "OctetString", Access: "read-only", Status: "current"},
		{OID: "1.3.6.1.2.1.1.3.0", Name: "sysUpTime", Module: "SNMPv2-MIB", Type: "TimeTicks", Access: "read-only", Status: "current"},
		{OID: "1.3.6.1.2.1.2.1.0", Name: "ifNumber", Module: "IF-MIB", Type: "Integer", Access: "read-only", Status: "current"},
	}

	for _, oid := range oids {
		if err := storage.StoreOID(oid); err != nil {
			t.Fatalf("Failed to store OID: %v", err)
		}
	}

	// List all OIDs
	all, err := storage.ListOIDs("")
	if err != nil {
		t.Fatalf("Failed to list OIDs: %v", err)
	}

	if len(all) != 3 {
		t.Errorf("Expected 3 OIDs, got %d", len(all))
	}

	// List with prefix
	sysOIDs, err := storage.ListOIDs("1.3.6.1.2.1.1")
	if err != nil {
		t.Fatalf("Failed to list OIDs with prefix: %v", err)
	}

	if len(sysOIDs) != 2 {
		t.Errorf("Expected 2 OIDs with prefix 1.3.6.1.2.1.1, got %d", len(sysOIDs))
	}
}

func TestStorage_DeleteOID(t *testing.T) {
	storage, cleanup := getTestStorage(t)
	defer cleanup()

	meta := &mib.OIDMetadata{
		OID:    "1.3.6.1.2.1.1.1.0",
		Name:   "sysDescr",
		Module: "SNMPv2-MIB",
		Type:   "OctetString",
		Access: "read-only",
		Status: "current",
	}

	if err := storage.StoreOID(meta); err != nil {
		t.Fatalf("Failed to store OID: %v", err)
	}

	// Delete OID
	if err := storage.DeleteOID("1.3.6.1.2.1.1.1.0"); err != nil {
		t.Fatalf("Failed to delete OID: %v", err)
	}

	// Verify it's gone
	_, err := storage.GetOID("1.3.6.1.2.1.1.1.0")
	if err == nil {
		t.Error("Expected error after deleting OID")
	}

	// Verify name mapping is gone too
	_, err = storage.GetOIDByName("SNMPv2-MIB", "sysDescr")
	if err == nil {
		t.Error("Expected error after deleting OID (name mapping should be gone)")
	}
}

func TestStorage_StoreAndGetModule(t *testing.T) {
	storage, cleanup := getTestStorage(t)
	defer cleanup()

	module := &mib.ModuleInfo{
		Name:         "SNMPv2-MIB",
		Path:         "/usr/share/snmp/mibs/SNMPv2-MIB.txt",
		Organization: "IETF",
		Description:  "SNMPv2 management information base",
		OIDCount:     42,
		LoadedAt:     time.Now(),
	}

	if err := storage.StoreModule(module); err != nil {
		t.Fatalf("Failed to store module: %v", err)
	}

	retrieved, err := storage.GetModule("SNMPv2-MIB")
	if err != nil {
		t.Fatalf("Failed to get module: %v", err)
	}

	if retrieved.Name != "SNMPv2-MIB" {
		t.Errorf("Expected name 'SNMPv2-MIB', got '%s'", retrieved.Name)
	}

	if retrieved.OIDCount != 42 {
		t.Errorf("Expected OID count 42, got %d", retrieved.OIDCount)
	}
}

func TestStorage_ListModules(t *testing.T) {
	storage, cleanup := getTestStorage(t)
	defer cleanup()

	modules := []*mib.ModuleInfo{
		{Name: "SNMPv2-MIB", OIDCount: 42, LoadedAt: time.Now()},
		{Name: "IF-MIB", OIDCount: 28, LoadedAt: time.Now()},
		{Name: "IP-MIB", OIDCount: 35, LoadedAt: time.Now()},
	}

	for _, mod := range modules {
		if err := storage.StoreModule(mod); err != nil {
			t.Fatalf("Failed to store module: %v", err)
		}
	}

	list, err := storage.ListModules()
	if err != nil {
		t.Fatalf("Failed to list modules: %v", err)
	}

	if len(list) != 3 {
		t.Errorf("Expected 3 modules, got %d", len(list))
	}
}

func TestStorage_DeleteModule(t *testing.T) {
	storage, cleanup := getTestStorage(t)
	defer cleanup()

	module := &mib.ModuleInfo{
		Name:     "TEST-MIB",
		OIDCount: 10,
		LoadedAt: time.Now(),
	}

	if err := storage.StoreModule(module); err != nil {
		t.Fatalf("Failed to store module: %v", err)
	}

	if err := storage.DeleteModule("TEST-MIB"); err != nil {
		t.Fatalf("Failed to delete module: %v", err)
	}

	_, err := storage.GetModule("TEST-MIB")
	if err == nil {
		t.Error("Expected error after deleting module")
	}
}

func TestStorage_StoreAndGetTable(t *testing.T) {
	storage, cleanup := getTestStorage(t)
	defer cleanup()

	table := &mib.TableInfo{
		OID:         "1.3.6.1.2.1.2.2",
		Name:        "ifTable",
		Module:      "IF-MIB",
		Description: "Interface table",
		Indexes:     []string{"ifIndex"},
		Columns:     []string{"1.3.6.1.2.1.2.2.1.1", "1.3.6.1.2.1.2.2.1.2"},
		EntryOID:    "1.3.6.1.2.1.2.2.1",
	}

	if err := storage.StoreTable(table); err != nil {
		t.Fatalf("Failed to store table: %v", err)
	}

	retrieved, err := storage.GetTable("IF-MIB", "ifTable")
	if err != nil {
		t.Fatalf("Failed to get table: %v", err)
	}

	if retrieved.Name != "ifTable" {
		t.Errorf("Expected name 'ifTable', got '%s'", retrieved.Name)
	}

	if len(retrieved.Columns) != 2 {
		t.Errorf("Expected 2 columns, got %d", len(retrieved.Columns))
	}
}

func TestStorage_ListTables(t *testing.T) {
	storage, cleanup := getTestStorage(t)
	defer cleanup()

	tables := []*mib.TableInfo{
		{OID: "1.3.6.1.2.1.2.2", Name: "ifTable", Module: "IF-MIB"},
		{OID: "1.3.6.1.2.1.31.1.1", Name: "ifXTable", Module: "IF-MIB"},
	}

	for _, tbl := range tables {
		if err := storage.StoreTable(tbl); err != nil {
			t.Fatalf("Failed to store table: %v", err)
		}
	}

	list, err := storage.ListTables("IF-MIB")
	if err != nil {
		t.Fatalf("Failed to list tables: %v", err)
	}

	if len(list) != 2 {
		t.Errorf("Expected 2 tables, got %d", len(list))
	}
}

func TestStorage_GetPollableOIDs(t *testing.T) {
	storage, cleanup := getTestStorage(t)
	defer cleanup()

	oids := []*mib.OIDMetadata{
		{OID: "1.3.6.1.2.1.1.1.0", Name: "sysDescr", Module: "SNMPv2-MIB", Access: "read-only", Status: "current", IsScalar: true},
		{OID: "1.3.6.1.2.1.1.2.0", Name: "sysObjectID", Module: "SNMPv2-MIB", Access: "not-accessible", Status: "current", IsScalar: true},
		{OID: "1.3.6.1.2.1.1.3.0", Name: "sysUpTime", Module: "SNMPv2-MIB", Access: "read-only", Status: "current", IsScalar: true},
		{OID: "1.3.6.1.2.1.2.1.0", Name: "ifNumber", Module: "IF-MIB", Access: "read-only", Status: "current", IsScalar: true},
	}

	for _, oid := range oids {
		oid.Pollable = oid.IsPollable()
		if err := storage.StoreOID(oid); err != nil {
			t.Fatalf("Failed to store OID: %v", err)
		}
	}

	pollable, err := storage.GetPollableOIDs("SNMPv2-MIB")
	if err != nil {
		t.Fatalf("Failed to get pollable OIDs: %v", err)
	}

	// Should only get sysDescr and sysUpTime (not sysObjectID which is not-accessible)
	if len(pollable) != 2 {
		t.Errorf("Expected 2 pollable OIDs, got %d", len(pollable))
	}
}

func TestStorage_Count(t *testing.T) {
	storage, cleanup := getTestStorage(t)
	defer cleanup()

	// Store some data
	oid := &mib.OIDMetadata{OID: "1.2.3.4", Name: "test", Module: "TEST", Type: "Integer", Access: "read-only", Status: "current"}
	storage.StoreOID(oid)

	module := &mib.ModuleInfo{Name: "TEST-MIB", OIDCount: 1, LoadedAt: time.Now()}
	storage.StoreModule(module)

	table := &mib.TableInfo{OID: "1.2.3", Name: "testTable", Module: "TEST"}
	storage.StoreTable(table)

	oidCount, modCount, tblCount, err := storage.Count()
	if err != nil {
		t.Fatalf("Failed to count: %v", err)
	}

	if oidCount != 1 {
		t.Errorf("Expected 1 OID, got %d", oidCount)
	}

	if modCount != 1 {
		t.Errorf("Expected 1 module, got %d", modCount)
	}

	if tblCount != 1 {
		t.Errorf("Expected 1 table, got %d", tblCount)
	}
}

func TestStorage_Close(t *testing.T) {
	storage, cleanup := getTestStorage(t)
	defer cleanup()

	if err := storage.Close(); err != nil {
		t.Errorf("Failed to close storage: %v", err)
	}

	// Closing again should be safe
	if err := storage.Close(); err != nil {
		t.Errorf("Second close failed: %v", err)
	}
}

func TestOIDMetadata_IsPollable(t *testing.T) {
	tests := []struct {
		name     string
		meta     mib.OIDMetadata
		expected bool
	}{
		{
			name: "pollable scalar",
			meta: mib.OIDMetadata{
				OID: "1.3.6.1.2.1.1.1", Access: "read-only", Status: "current", IsScalar: true,
			},
			expected: true,
		},
		{
			name:     "not accessible",
			meta:     mib.OIDMetadata{Access: "not-accessible", Status: "current", IsScalar: true},
			expected: false,
		},
		{
			name:     "deprecated",
			meta:     mib.OIDMetadata{Access: "read-only", Status: "deprecated", IsScalar: true},
			expected: false,
		},
		{
			name:     "table",
			meta:     mib.OIDMetadata{Access: "read-only", Status: "current", IsTable: true},
			expected: false,
		},
		{
			name:     "table entry",
			meta:     mib.OIDMetadata{Access: "read-only", Status: "current", IsTableEntry: true},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.meta.IsPollable()
			if result != tt.expected {
				t.Errorf("Expected IsPollable() = %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestStorage_GetMIBColumns(t *testing.T) {
	storage, cleanup := getTestStorage(t)
	defer cleanup()

	// Store test data with various object types
	oids := []*mib.OIDMetadata{
		{
			OID:         "1.3.6.1.2.1.2.2.1.1",
			Name:        "ifIndex",
			Module:      "IF-MIB",
			Type:        "Integer32",
			Access:      "read-only",
			Status:      "current",
			IsColumn:    true,
			ObjectClass: mib.ObjectClassIndexColumn,
			TableOID:    "1.3.6.1.2.1.2.2",
		},
		{
			OID:         "1.3.6.1.2.1.2.2.1.2",
			Name:        "ifDescr",
			Module:      "IF-MIB",
			Type:        "DisplayString",
			Access:      "read-only",
			Status:      "current",
			IsColumn:    true,
			ObjectClass: mib.ObjectClassStatusColumn,
			TableOID:    "1.3.6.1.2.1.2.2",
		},
		{
			OID:         "1.3.6.1.2.1.2.2.1.10",
			Name:        "ifInOctets",
			Module:      "IF-MIB",
			Type:        "Counter32",
			Access:      "read-only",
			Status:      "current",
			IsColumn:    true,
			ObjectClass: mib.ObjectClassMetricColumn,
			TableOID:    "1.3.6.1.2.1.2.2",
		},
		{
			OID:         "1.3.6.1.2.1.1.1.0",
			Name:        "sysDescr",
			Module:      "SNMPv2-MIB",
			Type:        "DisplayString",
			Access:      "read-only",
			Status:      "current",
			IsScalar:    true,
			ObjectClass: mib.ObjectClassScalar,
		},
		{
			OID:         "1.3.6.1.2.1.4.20.1.1",
			Name:        "ipAdEntAddr",
			Module:      "IP-MIB",
			Type:        "IpAddress",
			Access:      "read-only",
			Status:      "current",
			IsColumn:    true,
			ObjectClass: mib.ObjectClassIndexColumn,
			TableOID:    "1.3.6.1.2.1.4.20",
		},
	}

	for _, oid := range oids {
		if err := storage.StoreOID(oid); err != nil {
			t.Fatalf("Failed to store OID: %v", err)
		}
	}

	// Test GetMIBColumns - should return all columns for IF-MIB
	columns, err := storage.GetMIBColumns("IF-MIB")
	if err != nil {
		t.Fatalf("GetMIBColumns failed: %v", err)
	}

	if len(columns) != 3 {
		t.Errorf("Expected 3 columns for IF-MIB, got %d", len(columns))
	}

	// Verify all returned items are columns
	for _, col := range columns {
		if !col.IsColumn {
			t.Errorf("Expected all results to be columns, got %s with IsColumn=%v", col.Name, col.IsColumn)
		}
		if col.Module != "IF-MIB" {
			t.Errorf("Expected module IF-MIB, got %s", col.Module)
		}
	}
}

func TestStorage_GetMIBColumns_EmptyResult(t *testing.T) {
	storage, cleanup := getTestStorage(t)
	defer cleanup()

	// Test with non-existent module
	columns, err := storage.GetMIBColumns("NON-EXISTENT-MIB")
	if err != nil {
		t.Fatalf("GetMIBColumns failed: %v", err)
	}

	if len(columns) != 0 {
		t.Errorf("Expected 0 columns for non-existent MIB, got %d", len(columns))
	}
}

func TestStorage_GetScalars(t *testing.T) {
	storage, cleanup := getTestStorage(t)
	defer cleanup()

	// Store test data
	oids := []*mib.OIDMetadata{
		{
			OID:         "1.3.6.1.2.1.1.1.0",
			Name:        "sysDescr",
			Module:      "SNMPv2-MIB",
			Type:        "DisplayString",
			Access:      "read-only",
			Status:      "current",
			IsScalar:    true,
			ObjectClass: mib.ObjectClassScalar,
		},
		{
			OID:         "1.3.6.1.2.1.1.3.0",
			Name:        "sysUpTime",
			Module:      "SNMPv2-MIB",
			Type:        "TimeTicks",
			Access:      "read-only",
			Status:      "current",
			IsScalar:    true,
			ObjectClass: mib.ObjectClassScalar,
		},
		{
			OID:         "1.3.6.1.2.1.2.2.1.1",
			Name:        "ifIndex",
			Module:      "IF-MIB",
			Type:        "Integer32",
			Access:      "read-only",
			Status:      "current",
			IsColumn:    true,
			ObjectClass: mib.ObjectClassIndexColumn,
		},
	}

	for _, oid := range oids {
		if err := storage.StoreOID(oid); err != nil {
			t.Fatalf("Failed to store OID: %v", err)
		}
	}

	// Test GetScalars
	scalars, err := storage.GetScalars("SNMPv2-MIB")
	if err != nil {
		t.Fatalf("GetScalars failed: %v", err)
	}

	if len(scalars) != 2 {
		t.Errorf("Expected 2 scalars for SNMPv2-MIB, got %d", len(scalars))
	}

	// Verify all returned items are scalars
	for _, scalar := range scalars {
		if scalar.ObjectClass != mib.ObjectClassScalar {
			t.Errorf("Expected ObjectClass to be scalar, got %s for %s", scalar.ObjectClass, scalar.Name)
		}
		if scalar.Module != "SNMPv2-MIB" {
			t.Errorf("Expected module SNMPv2-MIB, got %s", scalar.Module)
		}
	}
}

func TestStorage_GetScalars_EmptyResult(t *testing.T) {
	storage, cleanup := getTestStorage(t)
	defer cleanup()

	// Test with module that has no scalars
	scalars, err := storage.GetScalars("EMPTY-MIB")
	if err != nil {
		t.Fatalf("GetScalars failed: %v", err)
	}

	if len(scalars) != 0 {
		t.Errorf("Expected 0 scalars for empty MIB, got %d", len(scalars))
	}
}

func TestStorage_GetMetricColumns(t *testing.T) {
	storage, cleanup := getTestStorage(t)
	defer cleanup()

	// Store test data
	oids := []*mib.OIDMetadata{
		{
			OID:         "1.3.6.1.2.1.2.2.1.10",
			Name:        "ifInOctets",
			Module:      "IF-MIB",
			Type:        "Counter32",
			Access:      "read-only",
			Status:      "current",
			IsColumn:    true,
			ObjectClass: mib.ObjectClassMetricColumn,
			IsChartable: true,
			ChartType:   "line",
		},
		{
			OID:         "1.3.6.1.2.1.2.2.1.16",
			Name:        "ifOutOctets",
			Module:      "IF-MIB",
			Type:        "Counter32",
			Access:      "read-only",
			Status:      "current",
			IsColumn:    true,
			ObjectClass: mib.ObjectClassMetricColumn,
			IsChartable: true,
			ChartType:   "line",
		},
		{
			OID:         "1.3.6.1.2.1.2.2.1.2",
			Name:        "ifDescr",
			Module:      "IF-MIB",
			Type:        "DisplayString",
			Access:      "read-only",
			Status:      "current",
			IsColumn:    true,
			ObjectClass: mib.ObjectClassStatusColumn,
		},
	}

	for _, oid := range oids {
		if err := storage.StoreOID(oid); err != nil {
			t.Fatalf("Failed to store OID: %v", err)
		}
	}

	// Test GetMetricColumns
	metrics, err := storage.GetMetricColumns("IF-MIB")
	if err != nil {
		t.Fatalf("GetMetricColumns failed: %v", err)
	}

	if len(metrics) != 2 {
		t.Errorf("Expected 2 metric columns for IF-MIB, got %d", len(metrics))
	}

	// Verify all returned items are metric columns
	for _, metric := range metrics {
		if metric.ObjectClass != mib.ObjectClassMetricColumn {
			t.Errorf("Expected ObjectClass to be metric_column, got %s for %s", metric.ObjectClass, metric.Name)
		}
		if metric.Module != "IF-MIB" {
			t.Errorf("Expected module IF-MIB, got %s", metric.Module)
		}
		if !metric.IsChartable {
			t.Errorf("Expected metric column %s to be chartable", metric.Name)
		}
	}
}

func TestStorage_GetMetricColumns_EmptyResult(t *testing.T) {
	storage, cleanup := getTestStorage(t)
	defer cleanup()

	// Test with module that has no metrics
	metrics, err := storage.GetMetricColumns("NO-METRICS-MIB")
	if err != nil {
		t.Fatalf("GetMetricColumns failed: %v", err)
	}

	if len(metrics) != 0 {
		t.Errorf("Expected 0 metric columns, got %d", len(metrics))
	}
}

func TestStorage_GetStatusColumns(t *testing.T) {
	storage, cleanup := getTestStorage(t)
	defer cleanup()

	// Store test data
	oids := []*mib.OIDMetadata{
		{
			OID:         "1.3.6.1.2.1.2.2.1.2",
			Name:        "ifDescr",
			Module:      "IF-MIB",
			Type:        "DisplayString",
			Access:      "read-only",
			Status:      "current",
			IsColumn:    true,
			ObjectClass: mib.ObjectClassStatusColumn,
		},
		{
			OID:           "1.3.6.1.2.1.2.2.1.8",
			Name:          "ifOperStatus",
			Module:        "IF-MIB",
			Type:          "INTEGER",
			Access:        "read-only",
			Status:        "current",
			IsColumn:      true,
			ObjectClass:   mib.ObjectClassStatusColumn,
			EnumValues:    map[int64]string{1: "up", 2: "down", 3: "testing"},
			IsDisplayable: true,
		},
		{
			OID:         "1.3.6.1.2.1.2.2.1.10",
			Name:        "ifInOctets",
			Module:      "IF-MIB",
			Type:        "Counter32",
			Access:      "read-only",
			Status:      "current",
			IsColumn:    true,
			ObjectClass: mib.ObjectClassMetricColumn,
		},
	}

	for _, oid := range oids {
		if err := storage.StoreOID(oid); err != nil {
			t.Fatalf("Failed to store OID: %v", err)
		}
	}

	// Test GetStatusColumns
	statuses, err := storage.GetStatusColumns("IF-MIB")
	if err != nil {
		t.Fatalf("GetStatusColumns failed: %v", err)
	}

	if len(statuses) != 2 {
		t.Errorf("Expected 2 status columns for IF-MIB, got %d", len(statuses))
	}

	// Verify all returned items are status columns
	for _, status := range statuses {
		if status.ObjectClass != mib.ObjectClassStatusColumn {
			t.Errorf("Expected ObjectClass to be status_column, got %s for %s", status.ObjectClass, status.Name)
		}
		if status.Module != "IF-MIB" {
			t.Errorf("Expected module IF-MIB, got %s", status.Module)
		}
	}

	// Verify enum values are preserved
	found := false
	for _, status := range statuses {
		if status.Name == "ifOperStatus" {
			found = true
			if len(status.EnumValues) != 3 {
				t.Errorf("Expected 3 enum values for ifOperStatus, got %d", len(status.EnumValues))
			}
			if status.EnumValues[1] != "up" {
				t.Errorf("Expected enum value 1 to be 'up', got '%s'", status.EnumValues[1])
			}
		}
	}
	if !found {
		t.Error("Expected to find ifOperStatus in results")
	}
}

func TestStorage_GetStatusColumns_EmptyResult(t *testing.T) {
	storage, cleanup := getTestStorage(t)
	defer cleanup()

	// Test with module that has no status columns
	statuses, err := storage.GetStatusColumns("NO-STATUS-MIB")
	if err != nil {
		t.Fatalf("GetStatusColumns failed: %v", err)
	}

	if len(statuses) != 0 {
		t.Errorf("Expected 0 status columns, got %d", len(statuses))
	}
}

func TestStorage_GetTableColumns(t *testing.T) {
	storage, cleanup := getTestStorage(t)
	defer cleanup()

	// Store test data for two different tables
	oids := []*mib.OIDMetadata{
		{
			OID:         "1.3.6.1.2.1.2.2.1.1",
			Name:        "ifIndex",
			Module:      "IF-MIB",
			Type:        "Integer32",
			Access:      "read-only",
			Status:      "current",
			IsColumn:    true,
			ObjectClass: mib.ObjectClassIndexColumn,
			TableOID:    "1.3.6.1.2.1.2.2",
			EntryOID:    "1.3.6.1.2.1.2.2.1",
		},
		{
			OID:         "1.3.6.1.2.1.2.2.1.2",
			Name:        "ifDescr",
			Module:      "IF-MIB",
			Type:        "DisplayString",
			Access:      "read-only",
			Status:      "current",
			IsColumn:    true,
			ObjectClass: mib.ObjectClassStatusColumn,
			TableOID:    "1.3.6.1.2.1.2.2",
			EntryOID:    "1.3.6.1.2.1.2.2.1",
		},
		{
			OID:         "1.3.6.1.2.1.2.2.1.10",
			Name:        "ifInOctets",
			Module:      "IF-MIB",
			Type:        "Counter32",
			Access:      "read-only",
			Status:      "current",
			IsColumn:    true,
			ObjectClass: mib.ObjectClassMetricColumn,
			TableOID:    "1.3.6.1.2.1.2.2",
			EntryOID:    "1.3.6.1.2.1.2.2.1",
		},
		{
			OID:         "1.3.6.1.2.1.4.20.1.1",
			Name:        "ipAdEntAddr",
			Module:      "IP-MIB",
			Type:        "IpAddress",
			Access:      "read-only",
			Status:      "current",
			IsColumn:    true,
			ObjectClass: mib.ObjectClassIndexColumn,
			TableOID:    "1.3.6.1.2.1.4.20",
			EntryOID:    "1.3.6.1.2.1.4.20.1",
		},
	}

	for _, oid := range oids {
		if err := storage.StoreOID(oid); err != nil {
			t.Fatalf("Failed to store OID: %v", err)
		}
	}

	// Test GetTableColumns for ifTable
	columns, err := storage.GetTableColumns("1.3.6.1.2.1.2.2")
	if err != nil {
		t.Fatalf("GetTableColumns failed: %v", err)
	}

	if len(columns) != 3 {
		t.Errorf("Expected 3 columns for ifTable, got %d", len(columns))
	}

	// Verify all returned items belong to the correct table
	for _, col := range columns {
		if col.TableOID != "1.3.6.1.2.1.2.2" {
			t.Errorf("Expected TableOID 1.3.6.1.2.1.2.2, got %s for %s", col.TableOID, col.Name)
		}
	}

	// Test GetTableColumns for ipAddrTable
	columns, err = storage.GetTableColumns("1.3.6.1.2.1.4.20")
	if err != nil {
		t.Fatalf("GetTableColumns failed: %v", err)
	}

	if len(columns) != 1 {
		t.Errorf("Expected 1 column for ipAddrTable, got %d", len(columns))
	}

	if columns[0].Name != "ipAdEntAddr" {
		t.Errorf("Expected column name ipAdEntAddr, got %s", columns[0].Name)
	}
}

func TestStorage_GetTableColumns_EmptyResult(t *testing.T) {
	storage, cleanup := getTestStorage(t)
	defer cleanup()

	// Test with non-existent table OID
	columns, err := storage.GetTableColumns("1.2.3.4.5")
	if err != nil {
		t.Fatalf("GetTableColumns failed: %v", err)
	}

	if len(columns) != 0 {
		t.Errorf("Expected 0 columns for non-existent table, got %d", len(columns))
	}
}

// TestStorage_GetMetricColumns_ScopedToModuleAtScale fills the store with two
// same-sized modules and checks that a lookup returns one module's metric
// columns and nothing else.
//
// What it verifies is the SCOPE of the result: drop either filter and the
// other module's objects or the status columns come back with it. What it
// deliberately does not verify is HOW the scope was reached — a full store
// scan that then filtered by module would satisfy it too. That is a cost
// question, and it belongs to BenchmarkGetMetricColumns and
// BenchmarkModuleLookup in storage_bench_test.go, which measure the index
// against the scan it replaced.
func TestStorage_GetMetricColumns_ScopedToModuleAtScale(t *testing.T) {
	storage, cleanup := getTestStorage(t)
	defer cleanup()

	const perModule = 100

	store := func(module, name string, class mib.ObjectClass) {
		t.Helper()
		oid := &mib.OIDMetadata{
			OID:         fmt.Sprintf("1.3.6.1.2.1.2.2.1.%s", name),
			Name:        name,
			Module:      module,
			Type:        "Counter32",
			Access:      "read-only",
			Status:      "current",
			IsColumn:    true,
			ObjectClass: class,
			TableOID:    "1.3.6.1.2.1.2.2",
		}
		if err := storage.StoreOID(oid); err != nil {
			t.Fatalf("store %s/%s: %v", module, name, err)
		}
	}

	want := make(map[string]bool, perModule)
	for i := range perModule {
		name := fmt.Sprintf("testColumn%d", i)
		store("TEST-MIB", name, mib.ObjectClassMetricColumn)
		want[name] = true

		// Same scale in a second module, and status columns in the module
		// under test, so neither the module nor the class filter can pass by
		// there being nothing to exclude.
		store("OTHER-MIB", fmt.Sprintf("otherColumn%d", i), mib.ObjectClassMetricColumn)
		store("TEST-MIB", fmt.Sprintf("testStatus%d", i), mib.ObjectClassStatusColumn)
	}

	metrics, err := storage.GetMetricColumns("TEST-MIB")
	if err != nil {
		t.Fatalf("GetMetricColumns: %v", err)
	}

	if len(metrics) != perModule {
		t.Fatalf("got %d metric columns, want %d", len(metrics), perModule)
	}

	for _, metric := range metrics {
		if !want[metric.Name] {
			t.Errorf("unexpected object %q (module %s, class %s)", metric.Name, metric.Module, metric.ObjectClass)
		}
		delete(want, metric.Name)
	}

	if len(want) != 0 {
		t.Errorf("%d stored metric columns missing from the result", len(want))
	}
}
