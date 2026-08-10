package dashboard

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jeremyhahn/go-netmon/internal/mib"
)

// TestBuildLearnedTablesFromEntries verifies that learned tables are built from Entry Index fields
func TestBuildLearnedTablesFromEntries(t *testing.T) {
	oids := []*mib.OIDMetadata{
		{
			Name:         "cbgpPeer2AddrFamilyPrefixEntry",
			OID:          "1.3.6.1.4.1.9.9.187.1.2.8.1",
			IsTableEntry: true,
			Index:        []string{"cbgpPeer2Type", "cbgpPeer2RemoteAddr", "cbgpPeer2AddrFamilyAfi", "cbgpPeer2AddrFamilySafi"},
		},
		{
			Name:         "ifEntry",
			OID:          "1.3.6.1.2.1.2.2.1",
			IsTableEntry: true,
			Index:        []string{"ifIndex"},
		},
		{
			Name:         "notAnEntry",
			OID:          "1.3.6.1.2.1.1.1",
			IsTableEntry: false,
			Index:        nil,
		},
	}

	learned := buildLearnedTablesFromEntries(oids)

	// Should have learned 2 tables
	assert.Len(t, learned, 2)

	// Check cbgpPeer2AddrFamilyPrefixEntry
	cbgpEntry, exists := learned["1.3.6.1.4.1.9.9.187.1.2.8.1"]
	assert.True(t, exists, "Should have cbgpPeer2AddrFamilyPrefixEntry")
	assert.Equal(t, "1.3.6.1.4.1.9.9.187.1.2.8.1", cbgpEntry.EntryOID)
	assert.Equal(t, []string{"cbgpPeer2Type", "cbgpPeer2RemoteAddr", "cbgpPeer2AddrFamilyAfi", "cbgpPeer2AddrFamilySafi"}, cbgpEntry.IndexNames)

	// Check ifEntry
	ifEntry, exists := learned["1.3.6.1.2.1.2.2.1"]
	assert.True(t, exists, "Should have ifEntry")
	assert.Equal(t, []string{"ifIndex"}, ifEntry.IndexNames)

	// Should not include non-entry OIDs
	_, exists = learned["1.3.6.1.2.1.1.1"]
	assert.False(t, exists, "Should not include non-entry OIDs")
}

// TestBuildTableWidgetWithLearnedIndexes verifies that tables without index columns
// can use learned indexes from Entry objects
func TestBuildTableWidgetWithLearnedIndexes(t *testing.T) {
	// Simulate a table with no index columns but with learned indexes from Entry
	group := &tableGroup{
		tableOID:   "1.3.6.1.4.1.9.9.187.1.2.8",
		tableName:  "cbgpPeer2AddrFamilyPrefixTable",
		entryOID:   "1.3.6.1.4.1.9.9.187.1.2.8.1",
		indexCols:  []*mib.OIDMetadata{}, // No index columns
		statusCols: []*mib.OIDMetadata{},
		metricCols: []*mib.OIDMetadata{
			{Name: "cbgpPeer2AcceptedPrefixes"},
		},
	}

	learnedTables := LearnedTablesMap{
		"1.3.6.1.4.1.9.9.187.1.2.8.1": {
			EntryOID:   "1.3.6.1.4.1.9.9.187.1.2.8.1",
			IndexNames: []string{"cbgpPeer2Type", "cbgpPeer2RemoteAddr", "cbgpPeer2AddrFamilyAfi", "cbgpPeer2AddrFamilySafi"},
		},
	}

	widget := buildTableWidgetWithLearnedData(group, learnedTables)

	// Should select cbgpPeer2RemoteAddr as the best index
	assert.Equal(t, "cbgpPeer2RemoteAddr", widget.InstanceSelector.IndexColumn)
	assert.Equal(t, "cbgppeer2remoteaddr", widget.InstanceSelector.LabelName)
	assert.Equal(t, "label_values({device=\"{{device}}\"}, cbgppeer2remoteaddr)", widget.InstanceSelector.QueryTemplate)
}

// TestBuildTableWidgetWithoutLearnedIndexes verifies fallback behavior
func TestBuildTableWidgetWithoutLearnedIndexes(t *testing.T) {
	// Simulate a table with no index columns and no learned indexes
	group := &tableGroup{
		tableOID:   "1.3.6.1.4.1.9.9.999.1.1",
		tableName:  "unknownTable",
		entryOID:   "1.3.6.1.4.1.9.9.999.1.1.1",
		indexCols:  []*mib.OIDMetadata{}, // No index columns
		statusCols: []*mib.OIDMetadata{},
		metricCols: []*mib.OIDMetadata{
			{Name: "someMetric"},
		},
	}

	learnedTables := LearnedTablesMap{} // Empty learned tables

	widget := buildTableWidgetWithLearnedData(group, learnedTables)

	// Should fallback to generic instance selector
	assert.Equal(t, "instance", widget.InstanceSelector.IndexColumn)
	assert.Equal(t, "instance", widget.InstanceSelector.LabelName)
	assert.Equal(t, "", widget.InstanceSelector.QueryTemplate)
}

// TestPrioritySelectionWithLearnedIndexes verifies that the priority logic works with learned indexes
func TestPrioritySelectionWithLearnedIndexes(t *testing.T) {
	tests := []struct {
		name           string
		indexNames     []string
		expectedColumn string
		expectedLabel  string
	}{
		{
			name:           "BGP table - prefer RemoteAddr",
			indexNames:     []string{"cbgpPeer2Type", "cbgpPeer2RemoteAddr", "cbgpPeer2AddrFamilyAfi", "cbgpPeer2AddrFamilySafi"},
			expectedColumn: "cbgpPeer2RemoteAddr",
			expectedLabel:  "cbgppeer2remoteaddr",
		},
		{
			name:           "BGP peer table - prefer RemoteAddr",
			indexNames:     []string{"cbgpPeer2Type", "cbgpPeer2RemoteAddr"},
			expectedColumn: "cbgpPeer2RemoteAddr",
			expectedLabel:  "cbgppeer2remoteaddr",
		},
		{
			name:           "Interface table - use ifIndex",
			indexNames:     []string{"ifIndex"},
			expectedColumn: "ifIndex",
			expectedLabel:  "ifindex",
		},
		{
			name:           "Generic address - prefer addr ending",
			indexNames:     []string{"someIndex", "someAddr", "someName"},
			expectedColumn: "someAddr",
			expectedLabel:  "someaddr",
		},
		{
			name:           "No good match - use first",
			indexNames:     []string{"someField1", "someField2"},
			expectedColumn: "someField1",
			expectedLabel:  "somefield1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Convert string names to OIDMetadata
			indexCols := make([]*mib.OIDMetadata, len(tt.indexNames))
			for i, name := range tt.indexNames {
				indexCols[i] = &mib.OIDMetadata{Name: name}
			}

			selector := buildInstanceSelectorFromMultiple(indexCols)

			assert.Equal(t, tt.expectedColumn, selector.IndexColumn,
				"Unexpected index column selected")
			assert.Equal(t, tt.expectedLabel, selector.LabelName,
				"Unexpected label name")
		})
	}
}
