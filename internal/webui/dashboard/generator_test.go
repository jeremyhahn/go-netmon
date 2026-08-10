package dashboard

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/mib/parser"
	mibstorage "github.com/jeremyhahn/go-netmon/internal/mib/storage"
	"github.com/jeremyhahn/go-netmon/internal/testsupport"
)

func setupTestStorage(t *testing.T) (*mibstorage.Storage, func()) {
	t.Helper()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	storage, err := mibstorage.New(dbPath)
	require.NoError(t, err)

	cleanup := func() {
		storage.Close()
	}

	return storage, cleanup
}

// loadTestMIB parses moduleName with dirs on the search path and stores what it
// finds.
//
// The search path is a parameter rather than a constant because a module's
// imports have to be on it: parsing with an incomplete path is not an error, it
// just yields fewer objects, most of them with an empty OID and no type. The
// parser is closed before this returns because gosmi keeps loaded modules in
// package state, so a later call with a different path would otherwise reuse
// this one's cache.
func loadTestMIB(t *testing.T, storage *mibstorage.Storage, moduleName string, dirs ...string) {
	t.Helper()

	require.NotEmpty(t, dirs, "a MIB search path is required")

	p := parser.New(dirs...)
	err := p.Init()
	require.NoError(t, err, "Failed to init parser")
	defer p.Close()

	moduleInfo, err := p.LoadModule(moduleName)
	require.NoError(t, err, "Failed to load module %s", moduleName)

	metadata, err := p.ParseModule(moduleName)
	require.NoError(t, err, "Failed to parse module %s", moduleName)

	for _, meta := range metadata {
		storeErr := storage.StoreOID(meta)
		require.NoError(t, storeErr, "Failed to store OID %s", meta.Name)
	}

	if moduleInfo != nil {
		err = storage.StoreModule(moduleInfo)
		require.NoError(t, err, "Failed to store module info")
	}
}

func TestGenerateDashboard_IFMIBWithTableWidget(t *testing.T) {
	storage, cleanup := setupTestStorage(t)
	defer cleanup()

	loadTestMIB(t, storage, "IF-MIB", testsupport.MIBDir("standard"))

	spec, err := GenerateDashboard(storage, "IF-MIB")
	require.NoError(t, err)
	assert.NotNil(t, spec)

	// Verify basic structure
	assert.Equal(t, "IF-MIB", spec.MIBName)

	// IF-MIB should have scalar widgets (e.g., ifNumber)
	assert.NotEmpty(t, spec.ScalarWidgets, "IF-MIB should have scalar widgets")

	// IF-MIB should have table widgets (ifTable, ifXTable)
	assert.NotEmpty(t, spec.TableWidgets, "IF-MIB should have table widgets")

	// Find ifTable widget
	var ifTableWidget *TableWidget
	for i := range spec.TableWidgets {
		if spec.TableWidgets[i].TableName == "ifTable" {
			ifTableWidget = &spec.TableWidgets[i]
			break
		}
	}
	require.NotNil(t, ifTableWidget, "Should have ifTable widget")

	// Verify instance selector
	assert.NotEmpty(t, ifTableWidget.InstanceSelector.LabelName, "Should have label name")
	assert.NotEmpty(t, ifTableWidget.InstanceSelector.QueryTemplate, "Should have query template")

	// Verify status fields (should not include index columns)
	assert.NotEmpty(t, ifTableWidget.StatusFields, "Should have status fields")
	for _, field := range ifTableWidget.StatusFields {
		assert.NotEmpty(t, field.Name)
		assert.NotEmpty(t, field.OID)
	}

	// Verify metric charts (should have counter/gauge columns)
	assert.NotEmpty(t, ifTableWidget.MetricCharts, "Should have metric charts")
	for _, chart := range ifTableWidget.MetricCharts {
		assert.NotEmpty(t, chart.Name)
		assert.NotEmpty(t, chart.OID)
		assert.NotEmpty(t, chart.ChartType)
	}

	// Verify ifIndex is NOT in metric charts (it's an index)
	for _, chart := range ifTableWidget.MetricCharts {
		assert.NotContains(t, chart.Name, "ifIndex", "Index column should not be charted")
	}
}

func TestGenerateDashboard_ScalarMIB(t *testing.T) {
	storage, cleanup := setupTestStorage(t)
	defer cleanup()

	// SNMPv2-MIB has many scalars
	loadTestMIB(t, storage, "SNMPv2-MIB", testsupport.MIBDir("standard"))

	spec, err := GenerateDashboard(storage, "SNMPv2-MIB")
	require.NoError(t, err)
	assert.NotNil(t, spec)

	assert.Equal(t, "SNMPv2-MIB", spec.MIBName)

	// Should have scalar widgets
	assert.NotEmpty(t, spec.ScalarWidgets, "SNMPv2-MIB should have scalar widgets")

	// Verify scalar widget structure
	for _, widget := range spec.ScalarWidgets {
		assert.NotEmpty(t, widget.Name, "Scalar widget should have name")
		assert.NotEmpty(t, widget.OID, "Scalar widget should have OID")
		assert.NotEmpty(t, widget.Type, "Scalar widget should have type")
	}
}

func TestGenerateDashboard_EnumValuePreservation(t *testing.T) {
	storage, cleanup := setupTestStorage(t)
	defer cleanup()

	loadTestMIB(t, storage, "IF-MIB", testsupport.MIBDir("standard"))

	spec, err := GenerateDashboard(storage, "IF-MIB")
	require.NoError(t, err)

	// Find ifTable widget
	var ifTableWidget *TableWidget
	for i := range spec.TableWidgets {
		if spec.TableWidgets[i].TableName == "ifTable" {
			ifTableWidget = &spec.TableWidgets[i]
			break
		}
	}
	require.NotNil(t, ifTableWidget, "Should have ifTable widget")

	// Find ifOperStatus field (it has enum values: up(1), down(2), testing(3), unknown(4), dormant(5), notPresent(6), lowerLayerDown(7))
	var operStatusField *StatusField
	for i := range ifTableWidget.StatusFields {
		if ifTableWidget.StatusFields[i].Name == "ifOperStatus" {
			operStatusField = &ifTableWidget.StatusFields[i]
			break
		}
	}

	if operStatusField != nil {
		assert.NotEmpty(t, operStatusField.EnumValues, "ifOperStatus should have enum values")
		// Check for common values
		if len(operStatusField.EnumValues) > 0 {
			assert.Contains(t, operStatusField.EnumValues, int64(1), "Should have up(1)")
			assert.Contains(t, operStatusField.EnumValues, int64(2), "Should have down(2)")
		}
	}
}

func TestGenerateDashboard_NonExistentModule(t *testing.T) {
	storage, cleanup := setupTestStorage(t)
	defer cleanup()

	spec, err := GenerateDashboard(storage, "NON-EXISTENT-MIB")
	assert.Error(t, err)
	assert.Nil(t, spec)
}

func TestGenerateDashboard_EmptyModule(t *testing.T) {
	storage, cleanup := setupTestStorage(t)
	defer cleanup()

	// Store a module with no OIDs
	moduleInfo := &mib.ModuleInfo{
		Name:     "EMPTY-MIB",
		OIDCount: 0,
	}
	err := storage.StoreModule(moduleInfo)
	require.NoError(t, err)

	spec, err := GenerateDashboard(storage, "EMPTY-MIB")
	require.NoError(t, err)
	assert.NotNil(t, spec)
	assert.Equal(t, "EMPTY-MIB", spec.MIBName)
	assert.Empty(t, spec.ScalarWidgets)
	assert.Empty(t, spec.TableWidgets)
}

// TestGenerateDashboard_BGPMIBCorrectSpec pins the dashboard BGP4-MIB produces.
//
// It used to be skipped as "BGP4-MIB not available in standard mibs directory".
// The module is in the repository, under mibs/asr1001; what it is not is
// resolvable from mibs/standard alone. Its imports (SNMPv2-SMI in particular)
// are what the standard directory supplies, so both directories have to be on
// the search path. Parsing it with only one of them produces no error at all --
// it produces 56 objects with empty OIDs and no tables, which is a silently
// empty dashboard.
func TestGenerateDashboard_BGPMIBCorrectSpec(t *testing.T) {
	storage, cleanup := setupTestStorage(t)
	defer cleanup()

	loadTestMIB(t, storage, "BGP4-MIB", testsupport.MIBDirs("standard", "asr1001")...)

	spec, err := GenerateDashboard(storage, "BGP4-MIB")
	require.NoError(t, err)
	require.NotNil(t, spec)
	assert.Equal(t, "BGP4-MIB", spec.MIBName)

	// The module's three scalars, with the OIDs RFC 4273 assigns them.
	wantScalars := map[string]string{
		"bgpVersion":    "1.3.6.1.2.1.15.1",
		"bgpLocalAs":    "1.3.6.1.2.1.15.2",
		"bgpIdentifier": "1.3.6.1.2.1.15.4",
	}
	gotScalars := make(map[string]string, len(spec.ScalarWidgets))
	for _, widget := range spec.ScalarWidgets {
		gotScalars[widget.Name] = widget.OID
		assert.NotEmpty(t, widget.Metric, "%s has no Prometheus metric name", widget.Name)
	}
	assert.Equal(t, wantScalars, gotScalars, "BGP4-MIB's scalar widgets")

	// Every table in the module becomes a widget.
	byTable := make(map[string]*TableWidget, len(spec.TableWidgets))
	for i := range spec.TableWidgets {
		byTable[spec.TableWidgets[i].TableName] = &spec.TableWidgets[i]
	}
	for _, name := range []string{"bgpPeerTable", "bgpRcvdPathAttrTable", "bgp4PathAttrTable"} {
		assert.Contains(t, byTable, name, "BGP4-MIB table %s produced no widget", name)
	}

	peers := byTable["bgpPeerTable"]
	require.NotNil(t, peers, "no bgpPeerTable widget")

	// bgpPeerEntry is INDEXed by bgpPeerRemoteAddr alone, so that is the column
	// a row is selected by and the only index label a query needs.
	assert.Equal(t, "bgpPeerRemoteAddr", peers.InstanceSelector.IndexColumn)
	assert.Equal(t, "bgppeerremoteaddr", peers.InstanceSelector.LabelName)
	assert.Equal(t, []string{"bgppeerremoteaddr"}, peers.InstanceSelector.IndexLabels)
	assert.Contains(t, peers.InstanceSelector.QueryTemplate, "{{device}}",
		"the instance query is not parameterised by device")

	// bgpPeerState is the enumerated column an operator actually watches. Its
	// labels have to survive into the spec, or the UI renders "6" instead of
	// "established".
	var peerState *StatusField
	for i := range peers.StatusFields {
		if peers.StatusFields[i].Name == "bgpPeerState" {
			peerState = &peers.StatusFields[i]
			break
		}
	}
	require.NotNil(t, peerState, "bgpPeerState is not a status field of bgpPeerTable")
	assert.Equal(t, "1.3.6.1.2.1.15.3.1.2", peerState.OID)
	assert.True(t, peerState.IsInfoMetric, "an enumerated column is queried as an info metric")
	assert.Equal(t, map[int64]string{
		1: "idle",
		2: "connect",
		3: "active",
		4: "opensent",
		5: "openconfirm",
		6: "established",
	}, peerState.EnumValues)

	charts := make(map[string]MetricChart, len(peers.MetricCharts))
	for _, chart := range peers.MetricCharts {
		charts[chart.Name] = chart
	}

	// A Counter32 column is charted as a rate; a Gauge32 column is not.
	updates, ok := charts["bgpPeerInUpdates"]
	require.True(t, ok, "bgpPeerInUpdates is not charted")
	assert.Equal(t, "1.3.6.1.2.1.15.3.1.10", updates.OID)
	assert.True(t, updates.IsCounter, "bgpPeerInUpdates is a Counter32")
	assert.True(t, updates.UseRate, "a counter must be charted as a rate, not as a raw total")

	established, ok := charts["bgpPeerFsmEstablishedTime"]
	require.True(t, ok, "bgpPeerFsmEstablishedTime is not charted")
	assert.Equal(t, "gauge", established.ChartType)
	assert.False(t, established.IsCounter, "bgpPeerFsmEstablishedTime is a Gauge32")

	// The index column identifies a row; charting it would plot the row's own
	// name against time.
	assert.NotContains(t, charts, "bgpPeerRemoteAddr", "the index column must not be charted")

	for name, chart := range charts {
		assert.NotEmpty(t, chart.OID, "%s has no OID", name)
		assert.NotEmpty(t, chart.Metric, "%s has no Prometheus metric name", name)
		assert.NotEmpty(t, chart.ChartType, "%s has no chart type", name)
	}
}

func TestScalarWidget_QueryTemplate(t *testing.T) {
	widget := ScalarWidget{
		OID:  "1.3.6.1.2.1.2.1.0",
		Name: "ifNumber",
		Type: "Integer32",
	}

	// Query template should be generated
	assert.NotEmpty(t, widget.Name)
	assert.NotEmpty(t, widget.OID)
}

func TestTableWidget_InstanceSelectorValidation(t *testing.T) {
	widget := TableWidget{
		TableName: "ifTable",
		InstanceSelector: InstanceSelector{
			LabelName:     "ifindex",
			IndexColumn:   "ifIndex",
			QueryTemplate: "group by (ifindex) (snmp__1_3_6_1_2_1_2_2_1_1{device=\"{{device}}\"})",
		},
	}

	assert.NotEmpty(t, widget.InstanceSelector.LabelName)
	assert.NotEmpty(t, widget.InstanceSelector.IndexColumn)
	assert.NotEmpty(t, widget.InstanceSelector.QueryTemplate)
	assert.Contains(t, widget.InstanceSelector.QueryTemplate, "{{device}}")
}

func TestGenerateDashboard_ChartTypeCorrectness(t *testing.T) {
	storage, cleanup := setupTestStorage(t)
	defer cleanup()

	loadTestMIB(t, storage, "IF-MIB", testsupport.MIBDir("standard"))

	spec, err := GenerateDashboard(storage, "IF-MIB")
	require.NoError(t, err)

	// Find ifTable widget
	var ifTableWidget *TableWidget
	for i := range spec.TableWidgets {
		if spec.TableWidgets[i].TableName == "ifTable" {
			ifTableWidget = &spec.TableWidgets[i]
			break
		}
	}
	require.NotNil(t, ifTableWidget)

	// All metric charts should have chart types
	for _, chart := range ifTableWidget.MetricCharts {
		assert.NotEmpty(t, chart.ChartType, "Chart should have chart type")
		// Chart type should be "line", "gauge", or "bar"
		assert.Contains(t, []string{"line", "gauge", "bar"}, chart.ChartType)
	}
}

// TestWidgetMetricNames pins the metric name every widget hands the browser to
// the exporter's naming rule: the MIB object's name, never its OID. The browser
// queries exactly what it is given here, so a wrong name here is a panel that
// silently matches nothing.
func TestWidgetMetricNames(t *testing.T) {
	tests := []struct {
		name     string
		object   *mib.OIDMetadata
		expected string
	}{
		{
			name:     "scalar keeps the object's case",
			object:   &mib.OIDMetadata{OID: ".1.3.6.1.2.1.2.1", Name: "ifNumber", Type: "Integer32", IsChartable: true},
			expected: "snmp_ifNumber",
		},
		{
			name:     "mixed-case enterprise object",
			object:   &mib.OIDMetadata{OID: ".1.3.6.1.4.1.9.9.109.1.1.1.1.5", Name: "cpmCPUTotal5sec", Type: "Gauge32", IsChartable: true},
			expected: "snmp_cpmCPUTotal5sec",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			widget := buildScalarWidget(tt.object)
			assert.Equal(t, tt.expected, widget.Metric)
			assert.NotContains(t, widget.Metric, "1_3_6", "the row index and OID belong in labels, not the name")
		})
	}
}

// TestStatusFieldMetricNames covers the two shapes a status column reaches
// Prometheus in: a numeric column under its own name, and a textual or enum
// column as an info metric whose value is a label.
func TestStatusFieldMetricNames(t *testing.T) {
	group := &tableGroup{
		tableOID:  ".1.3.6.1.2.1.2.2",
		tableName: "ifTable",
		entryOID:  ".1.3.6.1.2.1.2.2.1",
		indexCols: []*mib.OIDMetadata{{Name: "ifIndex", OID: ".1.3.6.1.2.1.2.2.1.1"}},
		statusCols: []*mib.OIDMetadata{
			{Name: "ifDescr", OID: ".1.3.6.1.2.1.2.2.1.2", Type: "DisplayString"},
			{Name: "ifMtu", OID: ".1.3.6.1.2.1.2.2.1.4", Type: "Integer32"},
		},
	}

	widget := buildTableWidgetWithLearnedData(group, nil)

	metrics := make(map[string]string, len(widget.StatusFields))
	for _, field := range widget.StatusFields {
		metrics[field.Name] = field.Metric
	}

	assert.Equal(t, "snmp_ifdescr_info", metrics["ifDescr"], "a string column is published as an info metric")
	assert.Equal(t, "snmp_ifMtu", metrics["ifMtu"], "a numeric column keeps the object's name")
}

func TestDetermineLabelName(t *testing.T) {
	tests := []struct {
		name      string
		indexName string
		expected  string
	}{
		{
			name:      "ifIndex",
			indexName: "ifIndex",
			expected:  "ifindex",
		},
		{
			name:      "ifName",
			indexName: "ifName",
			expected:  "ifname",
		},
		{
			name:      "bgpPeerRemoteAddr",
			indexName: "bgpPeerRemoteAddr",
			expected:  "bgppeerremoteaddr",
		},
		{
			name:      "entPhysicalIndex",
			indexName: "entPhysicalIndex",
			expected:  "entphysicalindex",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := determineBestLabelForInstance(tt.indexName)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestBuildInstanceSelectorFromMultiple_BGPTables tests that BGP tables
// correctly select IP address columns over other address-related fields
func TestBuildInstanceSelectorFromMultiple_BGPTables(t *testing.T) {
	tests := []struct {
		name           string
		indexCols      []*mib.OIDMetadata
		expectedColumn string
		description    string
	}{
		{
			name: "cbgpPeer2Table - should prefer RemoteAddr over Type",
			indexCols: []*mib.OIDMetadata{
				{Name: "cbgpPeer2Type"},
				{Name: "cbgpPeer2RemoteAddr"},
			},
			expectedColumn: "cbgpPeer2RemoteAddr",
			description:    "IP address should win over peer type",
		},
		{
			name: "cbgpPeer2AddrFamilyPrefixTable - should prefer RemoteAddr over AddrFamily fields",
			indexCols: []*mib.OIDMetadata{
				{Name: "cbgpPeer2Type"},
				{Name: "cbgpPeer2RemoteAddr"},
				{Name: "cbgpPeer2AddrFamilyAfi"},
				{Name: "cbgpPeer2AddrFamilySafi"},
			},
			expectedColumn: "cbgpPeer2RemoteAddr",
			description:    "IP address should win over address family indicators",
		},
		{
			name: "cbgpPeer2CapsTable - should prefer RemoteAddr over CapIndex",
			indexCols: []*mib.OIDMetadata{
				{Name: "cbgpPeer2Type"},
				{Name: "cbgpPeer2RemoteAddr"},
				{Name: "cbgpPeer2CapIndex"},
			},
			expectedColumn: "cbgpPeer2RemoteAddr",
			description:    "IP address should win over capability index",
		},
		{
			name: "Order independence - AddrFamily fields first",
			indexCols: []*mib.OIDMetadata{
				{Name: "cbgpPeer2AddrFamilyAfi"},
				{Name: "cbgpPeer2AddrFamilySafi"},
				{Name: "cbgpPeer2Type"},
				{Name: "cbgpPeer2RemoteAddr"},
			},
			expectedColumn: "cbgpPeer2RemoteAddr",
			description:    "Should still select RemoteAddr regardless of order",
		},
		{
			name: "Generic address column - should select addr suffix",
			indexCols: []*mib.OIDMetadata{
				{Name: "someIndex"},
				{Name: "someAddr"},
				{Name: "someName"},
			},
			expectedColumn: "someAddr",
			description:    "Column ending in 'addr' should win",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selector := buildInstanceSelectorFromMultiple(tt.indexCols)
			assert.Equal(t, tt.expectedColumn, selector.IndexColumn,
				"Failed: %s. Got %s, want %s",
				tt.description, selector.IndexColumn, tt.expectedColumn)
		})
	}
}

// TestShouldChartScalar tests the logic for determining which scalar values should be charted
func TestShouldChartScalar(t *testing.T) {
	tests := []struct {
		name        string
		oid         *mib.OIDMetadata
		shouldChart bool
		description string
	}{
		{
			name: "Counter should always be charted",
			oid: &mib.OIDMetadata{
				Name:        "ifInOctets",
				Type:        "Counter32",
				IsChartable: true,
			},
			shouldChart: true,
			description: "Counters are inherently time-series",
		},
		{
			name: "Gauge with 'usage' keyword should be charted",
			oid: &mib.OIDMetadata{
				Name:        "memoryUsage",
				Type:        "Gauge32",
				IsChartable: true,
			},
			shouldChart: true,
			description: "Usage metrics are time-series",
		},
		{
			name: "Gauge with 'utilization' keyword should be charted",
			oid: &mib.OIDMetadata{
				Name:        "cpuUtilization",
				Type:        "Gauge32",
				IsChartable: true,
			},
			shouldChart: true,
			description: "Utilization metrics are time-series",
		},
		{
			name: "Gauge with 'rate' keyword should be charted",
			oid: &mib.OIDMetadata{
				Name:        "errorRate",
				Type:        "Gauge32",
				IsChartable: true,
			},
			shouldChart: true,
			description: "Rate metrics are time-series",
		},
		{
			name: "Gauge with 'current' keyword should be charted",
			oid: &mib.OIDMetadata{
				Name:        "currentConnections",
				Type:        "Gauge32",
				IsChartable: true,
			},
			shouldChart: true,
			description: "Current value metrics are time-series",
		},
		{
			name: "Static gauge with 'max' should not be charted",
			oid: &mib.OIDMetadata{
				Name:        "maxSize",
				Type:        "Gauge32",
				IsChartable: false,
			},
			shouldChart: false,
			description: "Static configuration values shouldn't be charted",
		},
		{
			name: "Static gauge with 'limit' should not be charted",
			oid: &mib.OIDMetadata{
				Name:        "connectionLimit",
				Type:        "Gauge32",
				IsChartable: false,
			},
			shouldChart: false,
			description: "Limit configuration values shouldn't be charted",
		},
		{
			name: "Static gauge with 'timeout' should not be charted",
			oid: &mib.OIDMetadata{
				Name:        "sessionTimeout",
				Type:        "Gauge32",
				IsChartable: false,
			},
			shouldChart: false,
			description: "Timeout configuration values shouldn't be charted",
		},
		{
			name: "Gauge with both 'max' and 'usage' should be charted",
			oid: &mib.OIDMetadata{
				Name:        "maxUsage",
				Type:        "Gauge32",
				IsChartable: true,
			},
			shouldChart: true,
			description: "Exception: max+usage indicates a time-series metric",
		},
		{
			name: "Enum should not be charted",
			oid: &mib.OIDMetadata{
				Name:        "operStatus",
				Type:        "INTEGER",
				IsChartable: true,
				EnumValues:  map[int64]string{1: "up", 2: "down"},
			},
			shouldChart: false,
			description: "Enum values should be status displays",
		},
		{
			name: "String should not be charted",
			oid: &mib.OIDMetadata{
				Name:        "sysDescr",
				Type:        "DisplayString",
				IsChartable: true,
			},
			shouldChart: false,
			description: "Strings should be status displays",
		},
		{
			name: "Not chartable at all",
			oid: &mib.OIDMetadata{
				Name:        "someValue",
				Type:        "Integer32",
				IsChartable: false,
			},
			shouldChart: false,
			description: "Non-chartable OIDs should never be charted",
		},
		{
			name: "Gauge with description containing 'bandwidth'",
			oid: &mib.OIDMetadata{
				Name:        "ifSpeed",
				Type:        "Gauge32",
				Description: "Interface bandwidth in bits per second",
				IsChartable: true,
			},
			shouldChart: true,
			description: "Bandwidth metrics in description should be charted",
		},
		{
			name: "Gauge with 'available' keyword",
			oid: &mib.OIDMetadata{
				Name:        "memoryAvailable",
				Type:        "Gauge32",
				IsChartable: true,
			},
			shouldChart: true,
			description: "Available metrics are time-series",
		},
		{
			name: "Gauge with 'free' keyword",
			oid: &mib.OIDMetadata{
				Name:        "diskFree",
				Type:        "Gauge32",
				IsChartable: true,
			},
			shouldChart: true,
			description: "Free space metrics are time-series",
		},
		{
			name: "Static gauge with 'version'",
			oid: &mib.OIDMetadata{
				Name:        "softwareVersion",
				Type:        "Integer32",
				IsChartable: false,
			},
			shouldChart: false,
			description: "Version numbers shouldn't be charted",
		},
		{
			name: "Generic gauge without keywords",
			oid: &mib.OIDMetadata{
				Name:        "someGauge",
				Type:        "Gauge32",
				IsChartable: true,
			},
			shouldChart: true,
			description: "Conservative: chart gauges by default if no clear indicator",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shouldChartScalar(tt.oid)
			assert.Equal(t, tt.shouldChart, result,
				"Failed: %s. OID: %+v", tt.description, tt.oid)
		})
	}
}

// TestBuildScalarWidget_ChartingLogic tests that buildScalarWidget correctly applies shouldChartScalar
func TestBuildScalarWidget_ChartingLogic(t *testing.T) {
	tests := []struct {
		name         string
		oid          *mib.OIDMetadata
		expectedType string
		description  string
	}{
		{
			name: "Counter becomes chart widget",
			oid: &mib.OIDMetadata{
				Name:        "ifInOctets",
				Type:        "Counter32",
				IsChartable: true,
				OID:         "1.3.6.1.2.1.2.2.1.10",
			},
			expectedType: "chart",
			description:  "Counters should be chart widgets",
		},
		{
			name: "Usage gauge becomes chart widget",
			oid: &mib.OIDMetadata{
				Name:        "cpuUsage",
				Type:        "Gauge32",
				IsChartable: true,
				OID:         "1.3.6.1.4.1.1.1",
			},
			expectedType: "chart",
			description:  "Usage gauges should be chart widgets",
		},
		{
			name: "Static gauge becomes status widget",
			oid: &mib.OIDMetadata{
				Name:        "maxConnections",
				Type:        "Gauge32",
				IsChartable: false,
				OID:         "1.3.6.1.4.1.1.2",
			},
			expectedType: "status",
			description:  "Static configuration gauges should be status widgets",
		},
		{
			name: "Enum becomes status widget",
			oid: &mib.OIDMetadata{
				Name:        "operStatus",
				Type:        "INTEGER",
				IsChartable: true,
				EnumValues:  map[int64]string{1: "up", 2: "down"},
				OID:         "1.3.6.1.4.1.1.3",
			},
			expectedType: "status",
			description:  "Enums should be status widgets",
		},
		{
			name: "String becomes status widget",
			oid: &mib.OIDMetadata{
				Name:        "sysDescr",
				Type:        "DisplayString",
				IsChartable: true,
				OID:         "1.3.6.1.2.1.1.1",
			},
			expectedType: "status",
			description:  "Strings should be status widgets",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			widget := buildScalarWidget(tt.oid)
			assert.Equal(t, tt.expectedType, widget.Type,
				"Failed: %s. Widget: %+v", tt.description, widget)

			// If it's a chart widget, verify chart type is set
			if widget.Type == "chart" {
				assert.NotEmpty(t, widget.ChartType, "Chart widget should have chart type")
			}
		})
	}
}
