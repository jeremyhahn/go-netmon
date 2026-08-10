package parser

import (
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/testsupport"
)

// TestParser_OIDExtractionNotEmpty ensures all parsed OIDs have non-empty OID fields
func TestParser_OIDExtractionNotEmpty(t *testing.T) {
	parser := New(testsupport.MIBDir("standard"))
	if err := parser.Init(); err != nil {
		t.Fatalf("Init() failed: %v", err)
	}
	defer parser.Close()

	modules := []string{"IF-MIB", "SNMPv2-MIB"}

	for _, moduleName := range modules {
		t.Run(moduleName, func(t *testing.T) {
			oids, err := parser.ParseModule(moduleName)
			if err != nil {
				t.Fatalf("ParseModule() failed for %s: %v", moduleName, err)
			}

			if len(oids) == 0 {
				t.Fatalf("Expected OIDs, got none for module %s", moduleName)
			}

			emptyCount := 0
			for _, oid := range oids {
				if oid.OID == "" {
					t.Errorf("FOUND EMPTY OID: Name=%s, Module=%s, IsScalar=%v, IsColumn=%v, IsTable=%v",
						oid.Name, oid.Module, oid.IsScalar, oid.IsColumn, oid.IsTable)
					emptyCount++
				}

				// Also verify OID format is correct
				if oid.OID != "" && len(oid.OID) > 0 {
					// Should start with a digit
					if oid.OID[0] < '0' || oid.OID[0] > '9' {
						t.Errorf("Invalid OID format for %s: %q (should start with digit)", oid.Name, oid.OID)
					}
				}
			}

			if emptyCount > 0 {
				t.Fatalf("Found %d OIDs with empty OID field out of %d total in module %s",
					emptyCount, len(oids), moduleName)
			}

			t.Logf("Successfully verified %d OIDs in %s - all have valid OID fields", len(oids), moduleName)
		})
	}
}

// TestParser_SpecificOIDs verifies known OIDs are extracted correctly
func TestParser_SpecificOIDs(t *testing.T) {
	parser := New(testsupport.MIBDir("standard"))
	if err := parser.Init(); err != nil {
		t.Fatalf("Init() failed: %v", err)
	}
	defer parser.Close()

	tests := []struct {
		module      string
		oidName     string
		expectedOID string
	}{
		{"SNMPv2-MIB", "sysDescr", "1.3.6.1.2.1.1.1"},
		{"SNMPv2-MIB", "sysUpTime", "1.3.6.1.2.1.1.3"},
		{"IF-MIB", "ifName", "1.3.6.1.2.1.31.1.1.1.1"},
		{"IF-MIB", "ifHCInOctets", "1.3.6.1.2.1.31.1.1.1.6"},
		{"IF-MIB", "ifMIBObjects", "1.3.6.1.2.1.31.1"},
	}

	for _, tt := range tests {
		t.Run(tt.module+"/"+tt.oidName, func(t *testing.T) {
			oids, err := parser.ParseModule(tt.module)
			if err != nil {
				t.Fatalf("ParseModule() failed: %v", err)
			}

			found := false
			for _, oid := range oids {
				if oid.Name == tt.oidName {
					found = true
					if oid.OID == "" {
						t.Fatalf("OID field is EMPTY for %s", tt.oidName)
					}
					if oid.OID != tt.expectedOID {
						t.Errorf("Expected OID %q, got %q for %s", tt.expectedOID, oid.OID, tt.oidName)
					}
					t.Logf("✓ %s::%s = %s", tt.module, tt.oidName, oid.OID)
					break
				}
			}

			if !found {
				t.Fatalf("OID %s not found in module %s", tt.oidName, tt.module)
			}
		})
	}
}
