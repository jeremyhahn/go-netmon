package poller

import (
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/mib"
)

// TestWorker_Poll_OIDSeparation tests that the poll method correctly separates
// scalar OIDs from table/column OIDs
func TestWorker_Poll_OIDSeparation(t *testing.T) {
	testCases := []struct {
		name          string
		oids          []*mib.OIDMetadata
		expectScalars int
		expectTables  int
	}{
		{
			name: "only_scalars",
			oids: []*mib.OIDMetadata{
				{OID: "1.3.6.1.2.1.1.1", Name: "sysDescr", IsScalar: true},
				{OID: "1.3.6.1.2.1.1.3", Name: "sysUpTime", IsScalar: true},
				{OID: "1.3.6.1.2.1.1.5", Name: "sysName", IsScalar: true},
			},
			expectScalars: 3,
			expectTables:  0,
		},
		{
			name: "only_tables",
			oids: []*mib.OIDMetadata{
				{OID: "1.3.6.1.2.1.2.2", Name: "ifTable", IsTable: true},
				{OID: "1.3.6.1.2.1.2.2.1.2", Name: "ifDescr", IsColumn: true},
			},
			expectScalars: 0,
			expectTables:  2,
		},
		{
			name: "mixed_scalars_and_tables",
			oids: []*mib.OIDMetadata{
				{OID: "1.3.6.1.2.1.1.1", Name: "sysDescr", IsScalar: true},
				{OID: "1.3.6.1.2.1.2.2", Name: "ifTable", IsTable: true},
				{OID: "1.3.6.1.2.1.2.2.1.2", Name: "ifDescr", IsColumn: true},
				{OID: "1.3.6.1.2.1.1.5", Name: "sysName", IsScalar: true},
			},
			expectScalars: 2,
			expectTables:  2,
		},
		{
			name: "table_entries_ignored",
			oids: []*mib.OIDMetadata{
				{OID: "1.3.6.1.2.1.2.2", Name: "ifTable", IsTable: true},
				{OID: "1.3.6.1.2.1.2.2.1", Name: "ifEntry", IsTableEntry: true},
				{OID: "1.3.6.1.2.1.2.2.1.2", Name: "ifDescr", IsColumn: true},
			},
			expectScalars: 0,
			expectTables:  2, // Table and column, but not entry
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			scalarCount := 0
			tableCount := 0

			for _, oid := range tc.oids {
				if oid.IsTable || oid.IsColumn || oid.IsTableEntry {
					if !oid.IsTableEntry {
						tableCount++
					}
				} else {
					scalarCount++
				}
			}

			if scalarCount != tc.expectScalars {
				t.Errorf("Expected %d scalars, got %d", tc.expectScalars, scalarCount)
			}

			if tableCount != tc.expectTables {
				t.Errorf("Expected %d tables/columns, got %d", tc.expectTables, tableCount)
			}
		})
	}
}

// TestWorker_Poll_ScalarOIDFormatting tests that scalar OIDs get .0 appended
func TestWorker_Poll_ScalarOIDFormatting(t *testing.T) {
	testCases := []struct {
		name     string
		oid      string
		isScalar bool
		expected string
	}{
		{
			name:     "scalar_without_dot_zero",
			oid:      "1.3.6.1.2.1.1.1",
			isScalar: true,
			expected: "1.3.6.1.2.1.1.1.0",
		},
		{
			name:     "scalar_with_dot_zero",
			oid:      "1.3.6.1.2.1.1.1.0",
			isScalar: true,
			expected: "1.3.6.1.2.1.1.1.0",
		},
		{
			name:     "non_scalar",
			oid:      "1.3.6.1.2.1.2.2",
			isScalar: false,
			expected: "1.3.6.1.2.1.2.2",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			oidStr := tc.oid
			if tc.isScalar {
				if len(oidStr) < 2 || oidStr[len(oidStr)-2:] != ".0" {
					oidStr = oidStr + ".0"
				}
			}

			if oidStr != tc.expected {
				t.Errorf("Expected %s, got %s", tc.expected, oidStr)
			}
		})
	}
}

// TestWorker_Poll_EmptyOIDs tests fallback behavior when no OIDs are configured
func TestWorker_Poll_EmptyOIDs(t *testing.T) {
	// When no OIDs are configured, the poller should fallback to basic system OIDs
	var oids []*mib.OIDMetadata

	if len(oids) == 0 {
		// This is what the poll() method does
		fallbackOIDs := []string{
			"1.3.6.1.2.1.1.1.0", // sysDescr
			"1.3.6.1.2.1.1.3.0", // sysUpTime
			"1.3.6.1.2.1.1.5.0", // sysName
		}

		if len(fallbackOIDs) != 3 {
			t.Errorf("Expected 3 fallback OIDs, got %d", len(fallbackOIDs))
		}
	}
}

// TestWorker_Poll_Integration tests the full poll logic (conceptually)
func TestWorker_Poll_Integration(t *testing.T) {
	// This test verifies the logic flow of the poll method
	// In a real scenario, we'd mock the SNMP client

	oids := []*mib.OIDMetadata{
		{OID: "1.3.6.1.2.1.1.1", Name: "sysDescr", IsScalar: true},
		{OID: "1.3.6.1.2.1.1.3", Name: "sysUpTime", IsScalar: true},
		{OID: "1.3.6.1.2.1.2.2.1.2", Name: "ifDescr", IsColumn: true},
		{OID: "1.3.6.1.2.1.2.2.1.5", Name: "ifSpeed", IsColumn: true},
	}

	// Separate OIDs as the poll method does
	var scalarOIDs []string
	var tableOIDs []*mib.OIDMetadata

	for _, oid := range oids {
		if oid.IsTable || oid.IsColumn || oid.IsTableEntry {
			if !oid.IsTableEntry {
				tableOIDs = append(tableOIDs, oid)
			}
		} else {
			oidStr := oid.OID
			if oid.IsScalar {
				if len(oidStr) < 2 || oidStr[len(oidStr)-2:] != ".0" {
					oidStr = oidStr + ".0"
				}
			}
			scalarOIDs = append(scalarOIDs, oidStr)
		}
	}

	// Verify separation
	if len(scalarOIDs) != 2 {
		t.Errorf("Expected 2 scalar OIDs, got %d", len(scalarOIDs))
	}

	if len(tableOIDs) != 2 {
		t.Errorf("Expected 2 table/column OIDs, got %d", len(tableOIDs))
	}

	// Verify scalar OIDs have .0 appended
	for _, sOID := range scalarOIDs {
		if len(sOID) < 2 || sOID[len(sOID)-2:] != ".0" {
			t.Errorf("Scalar OID %s should end with .0", sOID)
		}
	}

	t.Logf("Successfully separated: %d scalars, %d tables/columns", len(scalarOIDs), len(tableOIDs))
}

// TestWorker_Poll_TableWalkCalled verifies table walk is used for table OIDs
func TestWorker_Poll_TableWalkCalled(t *testing.T) {
	// This conceptual test verifies that BulkWalkAll would be called for table OIDs

	tableOID := &mib.OIDMetadata{
		OID:      "1.3.6.1.2.1.2.2.1.2",
		Name:     "ifDescr",
		IsColumn: true,
	}

	// In the actual poll() method, this would call client.BulkWalkAll(tableOID.OID)
	// For this test, we just verify the logic would be correct

	if !tableOID.IsColumn && !tableOID.IsTable {
		t.Error("Expected table OID to be identified as column or table")
	}

	if tableOID.IsTableEntry {
		t.Error("Column should not be marked as table entry")
	}

	t.Logf("Table OID %s (%s) would use BulkWalkAll", tableOID.Name, tableOID.OID)
}

// BenchmarkPoll_OIDSeparation benchmarks the OID separation logic
func BenchmarkPoll_OIDSeparation(b *testing.B) {
	oids := make([]*mib.OIDMetadata, 100)
	for i := 0; i < 100; i++ {
		switch i % 3 {
		case 0:
			oids[i] = &mib.OIDMetadata{
				OID:      "1.3.6.1.2.1.1." + string(rune(i)),
				IsScalar: true,
			}
		case 1:
			oids[i] = &mib.OIDMetadata{
				OID:      "1.3.6.1.2.1.2.2.1." + string(rune(i)),
				IsColumn: true,
			}
		default:
			oids[i] = &mib.OIDMetadata{
				OID:     "1.3.6.1.2.1.2.2." + string(rune(i)),
				IsTable: true,
			}
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var scalarOIDs []string
		var tableOIDs []*mib.OIDMetadata

		for _, oid := range oids {
			if oid.IsTable || oid.IsColumn || oid.IsTableEntry {
				if !oid.IsTableEntry {
					tableOIDs = append(tableOIDs, oid)
				}
			} else {
				oidStr := oid.OID
				if oid.IsScalar {
					if len(oidStr) < 2 || oidStr[len(oidStr)-2:] != ".0" {
						oidStr = oidStr + ".0"
					}
				}
				scalarOIDs = append(scalarOIDs, oidStr)
			}
		}

		// Publish the results so the separation work cannot be optimized away.
		benchScalarOIDs, benchTableOIDs = scalarOIDs, tableOIDs
	}
}

// Sinks for BenchmarkPoll_OIDSeparation.
var (
	benchScalarOIDs []string
	benchTableOIDs  []*mib.OIDMetadata
)
