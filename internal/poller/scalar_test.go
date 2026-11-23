package poller

import (
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/mib"
)

func TestIsScalarOID(t *testing.T) {
	tests := []struct {
		name     string
		oid      *mib.OIDMetadata
		expected bool
	}{
		{
			name: "Scalar OID with IsScalar=true",
			oid: &mib.OIDMetadata{
				OID:      "1.3.6.1.2.1.1.1",
				Name:     "sysDescr",
				IsScalar: true,
			},
			expected: true,
		},
		{
			name: "Scalar OID with IsScalar=false (should still be scalar)",
			oid: &mib.OIDMetadata{
				OID:      "1.3.6.1.2.1.1.3",
				Name:     "sysUpTime",
				IsScalar: false,
			},
			expected: true,
		},
		{
			name: "Table OID",
			oid: &mib.OIDMetadata{
				OID:     "1.3.6.1.2.1.2.2",
				Name:    "ifTable",
				IsTable: true,
			},
			expected: false,
		},
		{
			name: "Column OID",
			oid: &mib.OIDMetadata{
				OID:      "1.3.6.1.2.1.2.2.1.2",
				Name:     "ifDescr",
				IsColumn: true,
			},
			expected: false,
		},
		{
			name: "Table Entry OID",
			oid: &mib.OIDMetadata{
				OID:          "1.3.6.1.2.1.2.2.1",
				Name:         "ifEntry",
				IsTableEntry: true,
			},
			expected: false,
		},
		{
			name: "Node OID (not marked as scalar but should be treated as scalar)",
			oid: &mib.OIDMetadata{
				OID:          "1.3.6.1.2.1.1.5",
				Name:         "sysName",
				IsScalar:     false,
				IsTable:      false,
				IsColumn:     false,
				IsTableEntry: false,
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isScalarOID(tt.oid)
			if result != tt.expected {
				t.Errorf("isScalarOID() = %v, want %v for OID %s (%s)",
					result, tt.expected, tt.oid.OID, tt.oid.Name)
			}
		})
	}
}

func TestEnsureScalarSuffix(t *testing.T) {
	tests := []struct {
		name     string
		oidStr   string
		expected string
	}{
		{
			name:     "OID without .0 suffix",
			oidStr:   "1.3.6.1.2.1.1.1",
			expected: "1.3.6.1.2.1.1.1.0",
		},
		{
			name:     "OID with .0 suffix",
			oidStr:   "1.3.6.1.2.1.1.1.0",
			expected: "1.3.6.1.2.1.1.1.0",
		},
		{
			name:     "OID ending with .10 (not .0)",
			oidStr:   "1.3.6.1.2.1.1.10",
			expected: "1.3.6.1.2.1.1.10.0",
		},
		{
			name:     "Short OID",
			oidStr:   "1",
			expected: "1.0",
		},
		{
			name:     "Empty OID",
			oidStr:   "",
			expected: ".0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ensureScalarSuffix(tt.oidStr)
			if result != tt.expected {
				t.Errorf("ensureScalarSuffix(%q) = %q, want %q",
					tt.oidStr, result, tt.expected)
			}
		})
	}
}

func TestScalarOIDDetectionInPoll(t *testing.T) {
	// Test that scalar OIDs are correctly identified during poll
	oids := []*mib.OIDMetadata{
		{
			OID:      "1.3.6.1.2.1.1.1",
			Name:     "sysDescr",
			IsScalar: true,
		},
		{
			OID:      "1.3.6.1.2.1.1.3",
			Name:     "sysUpTime",
			IsScalar: false, // Parser might set this to false
		},
		{
			OID:      "1.3.6.1.2.1.1.5",
			Name:     "sysName",
			IsScalar: false,
		},
		{
			OID:     "1.3.6.1.2.1.2.2",
			Name:    "ifTable",
			IsTable: true,
		},
		{
			OID:      "1.3.6.1.2.1.2.2.1.2",
			Name:     "ifDescr",
			IsColumn: true,
		},
	}

	var scalarOIDs []string
	var tableOIDs []*mib.OIDMetadata

	for _, oid := range oids {
		if oid.IsTable || oid.IsColumn || oid.IsTableEntry {
			if !oid.IsTableEntry {
				tableOIDs = append(tableOIDs, oid)
			}
		} else {
			oidStr := ensureScalarSuffix(oid.OID)
			scalarOIDs = append(scalarOIDs, oidStr)
		}
	}

	// Should have 3 scalar OIDs (sysDescr, sysUpTime, sysName)
	if len(scalarOIDs) != 3 {
		t.Errorf("Expected 3 scalar OIDs, got %d", len(scalarOIDs))
	}

	// Should have 2 table/column OIDs (ifTable, ifDescr)
	if len(tableOIDs) != 2 {
		t.Errorf("Expected 2 table OIDs, got %d", len(tableOIDs))
	}

	// Verify all scalars have .0 suffix
	expectedScalars := []string{
		"1.3.6.1.2.1.1.1.0",
		"1.3.6.1.2.1.1.3.0",
		"1.3.6.1.2.1.1.5.0",
	}

	for i, expected := range expectedScalars {
		if scalarOIDs[i] != expected {
			t.Errorf("Scalar OID %d: got %q, want %q", i, scalarOIDs[i], expected)
		}
	}
}
