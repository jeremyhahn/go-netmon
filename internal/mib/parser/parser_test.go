package parser

import (
	"testing"

	"github.com/sleepinggenius2/gosmi/types"

	"github.com/jeremyhahn/go-netmon/internal/testsupport"
)

func TestNew(t *testing.T) {
	parser := New("/usr/share/snmp/mibs")
	if parser == nil {
		t.Fatal("Expected parser to be created")
	}
	if len(parser.paths) != 1 {
		t.Errorf("Expected 1 path, got %d", len(parser.paths))
	}
}

func TestParser_getTypeName(t *testing.T) {
	parser := New()
	tests := []struct {
		name     string
		baseType types.BaseType
		expected string
	}{
		{"Integer32", types.BaseTypeInteger32, "Integer32"},
		{"Unsigned32", types.BaseTypeUnsigned32, "Unsigned32"},
		{"OctetString", types.BaseTypeOctetString, "OctetString"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parser.getTypeName(tt.baseType)
			if result != tt.expected {
				t.Errorf("Expected %s, got %s", tt.expected, result)
			}
		})
	}
}

func TestParser_getAccessString(t *testing.T) {
	parser := New()
	result := parser.getAccessString(types.AccessReadOnly)
	if result != "read-only" {
		t.Errorf("Expected 'read-only', got '%s'", result)
	}
}

func TestParser_getStatusString(t *testing.T) {
	parser := New()
	result := parser.getStatusString(types.StatusCurrent)
	if result != "current" {
		t.Errorf("Expected 'current', got '%s'", result)
	}
}

func TestParser_Init(t *testing.T) {
	parser := New("./mibs")
	if err := parser.Init(); err != nil {
		t.Errorf("Init() failed: %v", err)
	}
	defer parser.Close()
}

func TestParser_LoadModule_SNMPv2(t *testing.T) {
	parser := New(testsupport.MIBDir("standard"))
	if err := parser.Init(); err != nil {
		t.Fatalf("Init() failed: %v", err)
	}
	defer parser.Close()

	info, err := parser.LoadModule("SNMPv2-MIB")
	if err != nil {
		t.Fatalf("LoadModule() failed: %v", err)
	}

	if info == nil {
		t.Fatal("Expected module info, got nil")
	}

	if info.Name != "SNMPv2-MIB" {
		t.Errorf("Expected module name 'SNMPv2-MIB', got '%s'", info.Name)
	}

	if info.OIDCount == 0 {
		t.Error("Expected OID count > 0")
	}

	t.Logf("Loaded %s: %d OIDs", info.Name, info.OIDCount)
}

func TestParser_ParseModule_SNMPv2(t *testing.T) {
	parser := New(testsupport.MIBDir("standard"))
	if err := parser.Init(); err != nil {
		t.Fatalf("Init() failed: %v", err)
	}
	defer parser.Close()

	oids, err := parser.ParseModule("SNMPv2-MIB")
	if err != nil {
		t.Fatalf("ParseModule() failed: %v", err)
	}

	if len(oids) == 0 {
		t.Fatal("Expected OIDs, got none")
	}

	t.Logf("Parsed %d OIDs from SNMPv2-MIB", len(oids))

	// Check for well-known OIDs
	found := false
	for _, oid := range oids {
		if oid.Name == "sysDescr" {
			found = true
			if oid.OID != "1.3.6.1.2.1.1.1" {
				t.Errorf("Expected sysDescr OID to be 1.3.6.1.2.1.1.1, got %s", oid.OID)
			}
			if oid.Module != "SNMPv2-MIB" {
				t.Errorf("Expected module SNMPv2-MIB, got %s", oid.Module)
			}
			t.Logf("Found sysDescr: OID=%s, Type=%s, Access=%s", oid.OID, oid.Type, oid.Access)
			break
		}
	}

	if !found {
		t.Error("Expected to find sysDescr OID")
	}
}

func TestParser_LoadModule_IFMIB(t *testing.T) {
	parser := New(testsupport.MIBDir("standard"))
	if err := parser.Init(); err != nil {
		t.Fatalf("Init() failed: %v", err)
	}
	defer parser.Close()

	info, err := parser.LoadModule("IF-MIB")
	if err != nil {
		t.Fatalf("LoadModule() failed: %v", err)
	}

	if info == nil {
		t.Fatal("Expected module info, got nil")
	}

	if info.Name != "IF-MIB" {
		t.Errorf("Expected module name 'IF-MIB', got '%s'", info.Name)
	}

	if info.OIDCount == 0 {
		t.Error("Expected OID count > 0")
	}

	t.Logf("Loaded %s: %d OIDs", info.Name, info.OIDCount)
}

func TestParser_GetTableInfo(t *testing.T) {
	parser := New(testsupport.MIBDir("standard"))
	if err := parser.Init(); err != nil {
		t.Fatalf("Init() failed: %v", err)
	}
	defer parser.Close()

	_, err := parser.LoadModule("IF-MIB")
	if err != nil {
		t.Fatalf("LoadModule() failed: %v", err)
	}

	tables, err := parser.GetTableInfo("IF-MIB")
	if err != nil {
		t.Fatalf("GetTableInfo() failed: %v", err)
	}

	if len(tables) == 0 {
		t.Fatal("Expected tables, got none")
	}

	t.Logf("Found %d tables in IF-MIB", len(tables))

	// Check for ifTable
	found := false
	for _, table := range tables {
		if table.Name == "ifTable" {
			found = true
			if len(table.Columns) == 0 {
				t.Error("Expected ifTable to have columns")
			}
			if len(table.Indexes) == 0 {
				t.Error("Expected ifTable to have indexes")
			}
			t.Logf("ifTable: %d columns, %d indexes", len(table.Columns), len(table.Indexes))
			break
		}
	}

	if !found {
		t.Error("Expected to find ifTable")
	}
}

func TestParser_ParseModule_PollableOIDs(t *testing.T) {
	parser := New(testsupport.MIBDir("standard"))
	if err := parser.Init(); err != nil {
		t.Fatalf("Init() failed: %v", err)
	}
	defer parser.Close()

	oids, err := parser.ParseModule("IF-MIB")
	if err != nil {
		t.Fatalf("ParseModule() failed: %v", err)
	}

	pollable := 0
	for _, oid := range oids {
		if oid.Pollable {
			pollable++
		}
	}

	if pollable == 0 {
		t.Error("Expected some pollable OIDs")
	}

	t.Logf("Found %d pollable OIDs out of %d total", pollable, len(oids))
}
