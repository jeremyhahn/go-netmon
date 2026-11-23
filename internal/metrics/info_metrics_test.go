package metrics

import (
	"testing"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/mib/storage"
	"github.com/jeremyhahn/go-netmon/internal/snmp"
)

func TestConvertStatusColumnsToInfoMetrics(t *testing.T) {
	// Create a test storage with table metadata
	tempStorage, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer tempStorage.Close()

	// Add test table metadata (ifTable from IF-MIB)
	ifTableEntry := &mib.OIDMetadata{
		OID:          "1.3.6.1.2.1.2.2.1",
		Name:         "ifEntry",
		Module:       "IF-MIB",
		Parent:       "1.3.6.1.2.1.2.2",
		IsTableEntry: true,
		Index:        []string{"ifIndex"},
	}
	if err := tempStorage.StoreOID(ifTableEntry); err != nil {
		t.Fatalf("Failed to store ifEntry: %v", err)
	}

	// Add ifIndex column
	ifIndex := &mib.OIDMetadata{
		OID:         "1.3.6.1.2.1.2.2.1.1",
		Name:        "ifIndex",
		Module:      "IF-MIB",
		Parent:      "1.3.6.1.2.1.2.2.1",
		IsColumn:    true,
		Type:        "Integer32",
		ObjectClass: mib.ObjectClassIndexColumn,
	}
	if err := tempStorage.StoreOID(ifIndex); err != nil {
		t.Fatalf("Failed to store ifIndex: %v", err)
	}

	// Add ifDescr column (status column)
	ifDescr := &mib.OIDMetadata{
		OID:         "1.3.6.1.2.1.2.2.1.2",
		Name:        "ifDescr",
		Module:      "IF-MIB",
		Parent:      "1.3.6.1.2.1.2.2.1",
		IsColumn:    true,
		Type:        "DisplayString",
		ObjectClass: mib.ObjectClassStatusColumn,
	}
	if err := tempStorage.StoreOID(ifDescr); err != nil {
		t.Fatalf("Failed to store ifDescr: %v", err)
	}

	// Add ifType column (numeric status)
	ifType := &mib.OIDMetadata{
		OID:         "1.3.6.1.2.1.2.2.1.3",
		Name:        "ifType",
		Module:      "IF-MIB",
		Parent:      "1.3.6.1.2.1.2.2.1",
		IsColumn:    true,
		Type:        "Integer32",
		ObjectClass: mib.ObjectClassStatusColumn,
	}
	if err := tempStorage.StoreOID(ifType); err != nil {
		t.Fatalf("Failed to store ifType: %v", err)
	}

	// Create index cache
	cache := NewTableIndexCache(tempStorage)

	// Create test SNMP results
	timestamp := time.Now()
	results := []*snmp.Result{
		{
			OID:       ".1.3.6.1.2.1.2.2.1.2.3", // ifDescr.3
			Value:     []byte("GigabitEthernet0/0/0"),
			Timestamp: timestamp,
		},
		{
			OID:       ".1.3.6.1.2.1.2.2.1.2.10", // ifDescr.10
			Value:     []byte("GigabitEthernet0"),
			Timestamp: timestamp,
		},
	}

	// Test info metric generation
	infoMetrics := ConvertStatusColumnsToInfoMetrics("router1", results, nil, cache, timestamp)

	// Verify results
	if len(infoMetrics) != 2 {
		t.Errorf("Expected 2 info metrics, got %d", len(infoMetrics))
	}

	// Check first metric
	if len(infoMetrics) > 0 {
		metric := infoMetrics[0]
		if metric.Name != "snmp_ifdescr_info" {
			t.Errorf("Expected metric name 'snmp_ifdescr_info', got '%s'", metric.Name)
		}
		if metric.Value != 1.0 {
			t.Errorf("Expected constant value 1.0, got %f", metric.Value)
		}
		if metric.Labels["device"] != "router1" {
			t.Errorf("Expected device label 'router1', got '%s'", metric.Labels["device"])
		}
		if metric.Labels["ifindex"] != "3" {
			t.Errorf("Expected ifindex label '3', got '%s'", metric.Labels["ifindex"])
		}
		if metric.Labels["value"] != "GigabitEthernet0/0/0" {
			t.Errorf("Expected value label 'GigabitEthernet0/0/0', got '%s'", metric.Labels["value"])
		}
	}

	// Check second metric
	if len(infoMetrics) > 1 {
		metric := infoMetrics[1]
		if metric.Labels["ifindex"] != "10" {
			t.Errorf("Expected ifindex label '10', got '%s'", metric.Labels["ifindex"])
		}
		if metric.Labels["value"] != "GigabitEthernet0" {
			t.Errorf("Expected value label 'GigabitEthernet0', got '%s'", metric.Labels["value"])
		}
	}
}

func TestConvertStatusColumnsToInfoMetrics_NoStatusColumns(t *testing.T) {
	// Create a test storage with only numeric metrics
	tempStorage, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer tempStorage.Close()

	// Add test table metadata (ifTable from IF-MIB)
	ifTableEntry := &mib.OIDMetadata{
		OID:          "1.3.6.1.2.1.2.2.1",
		Name:         "ifEntry",
		Module:       "IF-MIB",
		Parent:       "1.3.6.1.2.1.2.2",
		IsTableEntry: true,
		Index:        []string{"ifIndex"},
	}
	if err := tempStorage.StoreOID(ifTableEntry); err != nil {
		t.Fatalf("Failed to save ifEntry: %v", err)
	}

	// Add ifInOctets column (metric, not status)
	ifInOctets := &mib.OIDMetadata{
		OID:         "1.3.6.1.2.1.2.2.1.10",
		Name:        "ifInOctets",
		Module:      "IF-MIB",
		Parent:      "1.3.6.1.2.1.2.2.1",
		IsColumn:    true,
		Type:        "Counter32",
		ObjectClass: mib.ObjectClassMetricColumn,
	}
	if err := tempStorage.StoreOID(ifInOctets); err != nil {
		t.Fatalf("Failed to save ifInOctets: %v", err)
	}

	// Create index cache
	cache := NewTableIndexCache(tempStorage)

	// Create test SNMP results (numeric values)
	// gosnmp decodes Counter32 into a native Go integer type (not raw BER
	// bytes), so the fixture uses uint here to match what GetInt64Value
	// actually receives in production.
	timestamp := time.Now()
	results := []*snmp.Result{
		{
			OID:       ".1.3.6.1.2.1.2.2.1.10.3", // ifInOctets.3
			Value:     uint(10000),               // Numeric value
			Timestamp: timestamp,
		},
	}

	// Test info metric generation (should produce no metrics)
	infoMetrics := ConvertStatusColumnsToInfoMetrics("router1", results, nil, cache, timestamp)

	// Verify no info metrics generated
	if len(infoMetrics) != 0 {
		t.Errorf("Expected 0 info metrics for numeric columns, got %d", len(infoMetrics))
	}
}

func TestConvertStatusColumnsToInfoMetrics_NilCache(t *testing.T) {
	timestamp := time.Now()
	results := []*snmp.Result{
		{
			OID:       ".1.3.6.1.2.1.2.2.1.2.3",
			Value:     []byte("GigabitEthernet0/0/0"),
			Timestamp: timestamp,
		},
	}

	// Test with nil cache
	infoMetrics := ConvertStatusColumnsToInfoMetrics("router1", results, nil, nil, timestamp)

	// Should return nil/empty
	if len(infoMetrics) > 0 {
		t.Errorf("Expected nil or empty result with nil cache, got %d metrics", len(infoMetrics))
	}
}

func TestConvertSNMPResultsWithTableIndexes_InfoMetrics(t *testing.T) {
	// Create a test storage with table metadata
	tempStorage, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer tempStorage.Close()

	// Add test table metadata (ifTable from IF-MIB)
	ifTableEntry := &mib.OIDMetadata{
		OID:          "1.3.6.1.2.1.2.2.1",
		Name:         "ifEntry",
		Module:       "IF-MIB",
		Parent:       "1.3.6.1.2.1.2.2",
		IsTableEntry: true,
		Index:        []string{"ifIndex"},
	}
	if err := tempStorage.StoreOID(ifTableEntry); err != nil {
		t.Fatalf("Failed to save ifEntry: %v", err)
	}

	// Add ifIndex column
	ifIndex := &mib.OIDMetadata{
		OID:         "1.3.6.1.2.1.2.2.1.1",
		Name:        "ifIndex",
		Module:      "IF-MIB",
		Parent:      "1.3.6.1.2.1.2.2.1",
		IsColumn:    true,
		Type:        "Integer32",
		ObjectClass: mib.ObjectClassIndexColumn,
	}
	if err := tempStorage.StoreOID(ifIndex); err != nil {
		t.Fatalf("Failed to save ifIndex: %v", err)
	}

	// Add ifDescr column (status column)
	ifDescr := &mib.OIDMetadata{
		OID:         "1.3.6.1.2.1.2.2.1.2",
		Name:        "ifDescr",
		Module:      "IF-MIB",
		Parent:      "1.3.6.1.2.1.2.2.1",
		IsColumn:    true,
		Type:        "DisplayString",
		ObjectClass: mib.ObjectClassStatusColumn,
	}
	if err := tempStorage.StoreOID(ifDescr); err != nil {
		t.Fatalf("Failed to save ifDescr: %v", err)
	}

	// Add ifInOctets column (metric)
	ifInOctets := &mib.OIDMetadata{
		OID:         "1.3.6.1.2.1.2.2.1.10",
		Name:        "ifInOctets",
		Module:      "IF-MIB",
		Parent:      "1.3.6.1.2.1.2.2.1",
		IsColumn:    true,
		Type:        "Counter32",
		ObjectClass: mib.ObjectClassMetricColumn,
	}
	if err := tempStorage.StoreOID(ifInOctets); err != nil {
		t.Fatalf("Failed to save ifInOctets: %v", err)
	}

	// Create index cache
	cache := NewTableIndexCache(tempStorage)

	// Create test SNMP results (both string and numeric).
	// gosnmp decodes Counter32 into a native Go integer type (not raw BER
	// bytes), so the fixture uses uint here to match what GetInt64Value
	// actually receives in production.
	timestamp := time.Now()
	results := []*snmp.Result{
		{
			OID:       ".1.3.6.1.2.1.2.2.1.2.3", // ifDescr.3
			Value:     []byte("GigabitEthernet0/0/0"),
			Timestamp: timestamp,
		},
		{
			OID:       ".1.3.6.1.2.1.2.2.1.10.3", // ifInOctets.3
			Value:     uint(10000),
			Timestamp: timestamp,
		},
	}

	// Convert with index cache
	samples := ConvertSNMPResultsWithTableIndexes("router1", results, nil, cache, nil)

	// Verify we got both numeric metric and info metric
	hasNumericMetric := false
	hasInfoMetric := false

	for _, sample := range samples {
		// The numeric metric is named after the MIB object (ifInOctets);
		// the row index lives in labels, never in the metric name.
		if sample.Name == "snmp_ifInOctets" {
			hasNumericMetric = true
			// Numeric metric should have friendly name label
			if sample.Labels["ifdescr"] != "GigabitEthernet0/0/0" {
				t.Errorf("Expected ifdescr label 'GigabitEthernet0/0/0', got '%s'", sample.Labels["ifdescr"])
			}
		}
		if sample.Name == "snmp_ifdescr_info" {
			hasInfoMetric = true
			// Info metric should have value label
			if sample.Labels["value"] != "GigabitEthernet0/0/0" {
				t.Errorf("Expected value label 'GigabitEthernet0/0/0', got '%s'", sample.Labels["value"])
			}
			if sample.Value != 1.0 {
				t.Errorf("Expected info metric value 1.0, got %f", sample.Value)
			}
		}
	}

	if !hasNumericMetric {
		t.Error("Expected numeric metric to be generated")
	}
	if !hasInfoMetric {
		t.Error("Expected info metric to be generated")
	}
}

func TestInfoMetricNaming(t *testing.T) {
	tests := []struct {
		columnName   string
		expectedName string
	}{
		{"ifDescr", "snmp_ifdescr_info"},
		{"ifAlias", "snmp_ifalias_info"},
		{"ifName", "snmp_ifname_info"},
		{"cbgpPeer2LastError", "snmp_cbgppeer2lasterror_info"},
	}

	for _, tt := range tests {
		t.Run(tt.columnName, func(t *testing.T) {
			// Create test storage
			tempStorage, err := storage.New(t.TempDir())
			if err != nil {
				t.Fatalf("Failed to create storage: %v", err)
			}
			defer tempStorage.Close()

			// Add table entry
			tableEntry := &mib.OIDMetadata{
				OID:          "1.3.6.1.2.1.2.2.1",
				Name:         "testEntry",
				Module:       "TEST-MIB",
				Parent:       "1.3.6.1.2.1.2.2",
				IsTableEntry: true,
				Index:        []string{"testIndex"},
			}
			if err := tempStorage.StoreOID(tableEntry); err != nil {
				t.Fatalf("Failed to save tableEntry: %v", err)
			}

			// Add status column
			column := &mib.OIDMetadata{
				OID:         "1.3.6.1.2.1.2.2.1.2",
				Name:        tt.columnName,
				Module:      "TEST-MIB",
				Parent:      "1.3.6.1.2.1.2.2.1",
				IsColumn:    true,
				Type:        "DisplayString",
				ObjectClass: mib.ObjectClassStatusColumn,
			}
			if err := tempStorage.StoreOID(column); err != nil {
				t.Fatalf("Failed to save column: %v", err)
			}

			// Create cache and test
			cache := NewTableIndexCache(tempStorage)
			timestamp := time.Now()
			results := []*snmp.Result{
				{
					OID:       ".1.3.6.1.2.1.2.2.1.2.1",
					Value:     []byte("test value"),
					Timestamp: timestamp,
				},
			}

			infoMetrics := ConvertStatusColumnsToInfoMetrics("router1", results, nil, cache, timestamp)

			if len(infoMetrics) != 1 {
				t.Fatalf("Expected 1 info metric, got %d", len(infoMetrics))
			}

			if infoMetrics[0].Name != tt.expectedName {
				t.Errorf("Expected metric name '%s', got '%s'", tt.expectedName, infoMetrics[0].Name)
			}
		})
	}
}
