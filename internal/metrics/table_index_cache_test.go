package metrics

import (
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/mib/storage"
	"github.com/jeremyhahn/go-netmon/internal/snmp"
)

// TestCacheFriendlyNames tests the caching of friendly names from status columns
func TestCacheFriendlyNames(t *testing.T) {
	// Create test storage
	tmpDir := t.TempDir()
	store, err := storage.New(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer store.Close()

	// Create test MIB data for ifTable
	// Store ifEntry (table entry)
	ifEntry := &mib.OIDMetadata{
		OID:          "1.3.6.1.2.1.2.2.1",
		Name:         "ifEntry",
		Module:       "IF-MIB",
		Type:         "SEQUENCE",
		IsTableEntry: true,
		Index:        []string{"ifIndex"},
		Parent:       "1.3.6.1.2.1.2.2",
	}
	if err := store.StoreOID(ifEntry); err != nil {
		t.Fatalf("Failed to store ifEntry: %v", err)
	}

	// Store ifIndex (index column)
	ifIndex := &mib.OIDMetadata{
		OID:         "1.3.6.1.2.1.2.2.1.1",
		Name:        "ifIndex",
		Module:      "IF-MIB",
		Type:        "Integer32",
		IsColumn:    true,
		Parent:      "1.3.6.1.2.1.2.2.1",
		ObjectClass: mib.ObjectClassIndexColumn,
	}
	if err := store.StoreOID(ifIndex); err != nil {
		t.Fatalf("Failed to store ifIndex: %v", err)
	}

	// Store ifDescr (status column)
	ifDescr := &mib.OIDMetadata{
		OID:         "1.3.6.1.2.1.2.2.1.2",
		Name:        "ifDescr",
		Module:      "IF-MIB",
		Type:        "DisplayString",
		IsColumn:    true,
		Parent:      "1.3.6.1.2.1.2.2.1",
		ObjectClass: mib.ObjectClassStatusColumn,
		TableOID:    "1.3.6.1.2.1.2.2",
		EntryOID:    "1.3.6.1.2.1.2.2.1",
	}
	if err := store.StoreOID(ifDescr); err != nil {
		t.Fatalf("Failed to store ifDescr: %v", err)
	}

	// Store ifName (status column)
	ifName := &mib.OIDMetadata{
		OID:         "1.3.6.1.2.1.31.1.1.1.1",
		Name:        "ifName",
		Module:      "IF-MIB",
		Type:        "DisplayString",
		IsColumn:    true,
		Parent:      "1.3.6.1.2.1.2.2.1",
		ObjectClass: mib.ObjectClassStatusColumn,
		TableOID:    "1.3.6.1.2.1.2.2",
		EntryOID:    "1.3.6.1.2.1.2.2.1",
	}
	if err := store.StoreOID(ifName); err != nil {
		t.Fatalf("Failed to store ifName: %v", err)
	}

	// Store metric column for testing
	ifInOctets := &mib.OIDMetadata{
		OID:         "1.3.6.1.2.1.2.2.1.10",
		Name:        "ifInOctets",
		Module:      "IF-MIB",
		Type:        "Counter32",
		IsColumn:    true,
		Parent:      "1.3.6.1.2.1.2.2.1",
		ObjectClass: mib.ObjectClassMetricColumn,
		TableOID:    "1.3.6.1.2.1.2.2",
		EntryOID:    "1.3.6.1.2.1.2.2.1",
	}
	if err := store.StoreOID(ifInOctets); err != nil {
		t.Fatalf("Failed to store ifInOctets: %v", err)
	}

	// Create cache
	cache := NewTableIndexCache(store)

	// Create test SNMP results with status column values
	results := []*snmp.Result{
		{
			OID:       "1.3.6.1.2.1.2.2.1.2.1", // ifDescr.1
			Type:      gosnmp.OctetString,
			Value:     []byte("GigabitEthernet0/0"),
			Timestamp: time.Now(),
		},
		{
			OID:       "1.3.6.1.2.1.2.2.1.2.2", // ifDescr.2
			Type:      gosnmp.OctetString,
			Value:     []byte("GigabitEthernet0/1"),
			Timestamp: time.Now(),
		},
	}

	// Cache friendly names
	cache.CacheFriendlyNames("test-device", results)

	// Verify cache contents
	if got, _ := cache.friendlyName("test-device", ".1.3.6.1.2.1.2.2.1", "1", "ifdescr"); got != "GigabitEthernet0/0" {
		t.Errorf("Expected cached value 'GigabitEthernet0/0', got %q", got)
	}
	if got, _ := cache.friendlyName("test-device", ".1.3.6.1.2.1.2.2.1", "2", "ifdescr"); got != "GigabitEthernet0/1" {
		t.Errorf("Expected cached value 'GigabitEthernet0/1', got %q", got)
	}
}

// TestExtractIndexLabelsWithFriendlyNames tests that friendly name labels are added to index labels
func TestExtractIndexLabelsWithFriendlyNames(t *testing.T) {
	// Create test storage
	tmpDir := t.TempDir()
	store, err := storage.New(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer store.Close()

	// Create test MIB data
	ifEntry := &mib.OIDMetadata{
		OID:          "1.3.6.1.2.1.2.2.1",
		Name:         "ifEntry",
		Module:       "IF-MIB",
		Type:         "SEQUENCE",
		IsTableEntry: true,
		Index:        []string{"ifIndex"},
		Parent:       "1.3.6.1.2.1.2.2",
	}
	if err := store.StoreOID(ifEntry); err != nil {
		t.Fatalf("Failed to store ifEntry: %v", err)
	}

	ifIndex := &mib.OIDMetadata{
		OID:         "1.3.6.1.2.1.2.2.1.1",
		Name:        "ifIndex",
		Module:      "IF-MIB",
		Type:        "Integer32",
		IsColumn:    true,
		Parent:      "1.3.6.1.2.1.2.2.1",
		ObjectClass: mib.ObjectClassIndexColumn,
	}
	if err := store.StoreOID(ifIndex); err != nil {
		t.Fatalf("Failed to store ifIndex: %v", err)
	}

	ifDescr := &mib.OIDMetadata{
		OID:         "1.3.6.1.2.1.2.2.1.2",
		Name:        "ifDescr",
		Module:      "IF-MIB",
		Type:        "DisplayString",
		IsColumn:    true,
		Parent:      "1.3.6.1.2.1.2.2.1",
		ObjectClass: mib.ObjectClassStatusColumn,
		TableOID:    "1.3.6.1.2.1.2.2",
		EntryOID:    "1.3.6.1.2.1.2.2.1",
	}
	if err := store.StoreOID(ifDescr); err != nil {
		t.Fatalf("Failed to store ifDescr: %v", err)
	}

	ifInOctets := &mib.OIDMetadata{
		OID:         "1.3.6.1.2.1.2.2.1.10",
		Name:        "ifInOctets",
		Module:      "IF-MIB",
		Type:        "Counter32",
		IsColumn:    true,
		Parent:      "1.3.6.1.2.1.2.2.1",
		ObjectClass: mib.ObjectClassMetricColumn,
		TableOID:    "1.3.6.1.2.1.2.2",
		EntryOID:    "1.3.6.1.2.1.2.2.1",
	}
	if err := store.StoreOID(ifInOctets); err != nil {
		t.Fatalf("Failed to store ifInOctets: %v", err)
	}

	// Create cache
	cache := NewTableIndexCache(store)

	// Manually add friendly name to cache
	cache.publishFriendlyNames("test-device", friendlyNames{
		".1.3.6.1.2.1.2.2.1": {{index: "1", label: "ifdescr"}: "GigabitEthernet0/0"},
	})

	// Extract labels from a metric OID
	labels := cache.ExtractIndexLabels("test-device", "1.3.6.1.2.1.2.2.1.10.1")

	// Should have both ifindex and ifdescr labels
	if labels["ifindex"] != "1" {
		t.Errorf("Expected ifindex='1', got %q", labels["ifindex"])
	}

	if labels["ifdescr"] != "GigabitEthernet0/0" {
		t.Errorf("Expected ifdescr='GigabitEthernet0/0', got %q", labels["ifdescr"])
	}
}

// TestConvertSNMPResultsWithFriendlyNames tests end-to-end conversion with friendly names
func TestConvertSNMPResultsWithFriendlyNames(t *testing.T) {
	// Create test storage
	tmpDir := t.TempDir()
	store, err := storage.New(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer store.Close()

	// Create test MIB data
	ifEntry := &mib.OIDMetadata{
		OID:          "1.3.6.1.2.1.2.2.1",
		Name:         "ifEntry",
		Module:       "IF-MIB",
		Type:         "SEQUENCE",
		IsTableEntry: true,
		Index:        []string{"ifIndex"},
		Parent:       "1.3.6.1.2.1.2.2",
	}
	if err := store.StoreOID(ifEntry); err != nil {
		t.Fatalf("Failed to store ifEntry: %v", err)
	}

	ifIndex := &mib.OIDMetadata{
		OID:         "1.3.6.1.2.1.2.2.1.1",
		Name:        "ifIndex",
		Module:      "IF-MIB",
		Type:        "Integer32",
		IsColumn:    true,
		Parent:      "1.3.6.1.2.1.2.2.1",
		ObjectClass: mib.ObjectClassIndexColumn,
	}
	if err := store.StoreOID(ifIndex); err != nil {
		t.Fatalf("Failed to store ifIndex: %v", err)
	}

	ifDescr := &mib.OIDMetadata{
		OID:         "1.3.6.1.2.1.2.2.1.2",
		Name:        "ifDescr",
		Module:      "IF-MIB",
		Type:        "DisplayString",
		IsColumn:    true,
		Parent:      "1.3.6.1.2.1.2.2.1",
		ObjectClass: mib.ObjectClassStatusColumn,
		TableOID:    "1.3.6.1.2.1.2.2",
		EntryOID:    "1.3.6.1.2.1.2.2.1",
	}
	if err := store.StoreOID(ifDescr); err != nil {
		t.Fatalf("Failed to store ifDescr: %v", err)
	}

	ifInOctets := &mib.OIDMetadata{
		OID:         "1.3.6.1.2.1.2.2.1.10",
		Name:        "ifInOctets",
		Module:      "IF-MIB",
		Type:        "Counter32",
		IsColumn:    true,
		Parent:      "1.3.6.1.2.1.2.2.1",
		ObjectClass: mib.ObjectClassMetricColumn,
		TableOID:    "1.3.6.1.2.1.2.2",
		EntryOID:    "1.3.6.1.2.1.2.2.1",
	}
	if err := store.StoreOID(ifInOctets); err != nil {
		t.Fatalf("Failed to store ifInOctets: %v", err)
	}

	// Create cache
	cache := NewTableIndexCache(store)

	// Create SNMP results with both status and metric columns
	results := []*snmp.Result{
		{
			OID:       "1.3.6.1.2.1.2.2.1.2.1", // ifDescr.1
			Type:      gosnmp.OctetString,
			Value:     []byte("GigabitEthernet0/0"),
			Timestamp: time.Now(),
		},
		{
			OID:       "1.3.6.1.2.1.2.2.1.10.1", // ifInOctets.1
			Type:      gosnmp.Counter32,
			Value:     uint(12345678),
			Timestamp: time.Now(),
		},
	}

	// Convert results
	samples := ConvertSNMPResultsWithTableIndexes("test-device", results, nil, cache, nil)

	// Should have 2 samples: the numeric ifInOctets metric (carrying the
	// friendly ifdescr label) and the snmp_ifdescr_info metric generated
	// from the ifDescr status column itself.
	if len(samples) != 2 {
		t.Fatalf("Expected 2 samples, got %d", len(samples))
	}

	var numericSample, infoSample *Sample
	for _, s := range samples {
		switch s.Name {
		case "snmp_ifdescr_info":
			infoSample = s
		default:
			numericSample = s
		}
	}

	if numericSample == nil {
		t.Fatalf("Expected a numeric metric sample, got samples: %+v", samples)
	}

	// Verify it has the device label
	if numericSample.Labels["device"] != "test-device" {
		t.Errorf("Expected device='test-device', got %q", numericSample.Labels["device"])
	}

	// Verify it has the index label
	if numericSample.Labels["ifindex"] != "1" {
		t.Errorf("Expected ifindex='1', got %q", numericSample.Labels["ifindex"])
	}

	// Verify it has the friendly name label
	if numericSample.Labels["ifdescr"] != "GigabitEthernet0/0" {
		t.Errorf("Expected ifdescr='GigabitEthernet0/0', got %q", numericSample.Labels["ifdescr"])
	}

	// Verify the metric value
	if numericSample.Value != 12345678 {
		t.Errorf("Expected value=12345678, got %f", numericSample.Value)
	}

	if infoSample == nil {
		t.Fatalf("Expected an snmp_ifdescr_info sample, got samples: %+v", samples)
	}

	if infoSample.Labels["value"] != "GigabitEthernet0/0" {
		t.Errorf("Expected info metric value label 'GigabitEthernet0/0', got %q", infoSample.Labels["value"])
	}
}

// TestExtractColumnID tests the extractColumnID helper function
func TestExtractColumnID(t *testing.T) {
	tests := []struct {
		name      string
		columnOID string
		entryOID  string
		want      string
	}{
		{
			name:      "ifDescr column",
			columnOID: "1.3.6.1.2.1.2.2.1.2",
			entryOID:  "1.3.6.1.2.1.2.2.1",
			want:      "2",
		},
		{
			name:      "ifInOctets column",
			columnOID: "1.3.6.1.2.1.2.2.1.10",
			entryOID:  "1.3.6.1.2.1.2.2.1",
			want:      "10",
		},
		{
			name:      "with leading dot",
			columnOID: ".1.3.6.1.2.1.2.2.1.2",
			entryOID:  ".1.3.6.1.2.1.2.2.1",
			want:      "2",
		},
		{
			name:      "mismatched OIDs",
			columnOID: "1.3.6.1.2.1.3.3.1.2",
			entryOID:  "1.3.6.1.2.1.2.2.1",
			want:      "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractColumnID(tt.columnOID, tt.entryOID)
			if got != tt.want {
				t.Errorf("extractColumnID() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestCacheFriendlyNamesWithInvalidResults tests error handling
func TestCacheFriendlyNamesWithInvalidResults(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.New(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer store.Close()

	cache := NewTableIndexCache(store)

	// Create results with numeric values (should be ignored)
	results := []*snmp.Result{
		{
			OID:       "1.3.6.1.2.1.2.2.1.10.1",
			Type:      gosnmp.Counter32,
			Value:     uint(12345),
			Timestamp: time.Now(),
		},
	}

	// Should not crash and should not cache anything
	cache.CacheFriendlyNames("test-device", results)

	cacheSize := 0
	for _, rows := range cache.currentFriendlyNames("test-device") {
		cacheSize += len(rows)
	}

	if cacheSize != 0 {
		t.Errorf("Expected empty cache, got %d entries", cacheSize)
	}
}
