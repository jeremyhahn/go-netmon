package parser

import (
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/testsupport"
)

// TestEnumExtraction tests extraction of enum values from INTEGER types
func TestEnumExtraction(t *testing.T) {
	parser := New(testsupport.MIBDir("standard"))
	if err := parser.Init(); err != nil {
		t.Fatalf("Failed to init parser: %v", err)
	}
	defer parser.Close()

	// Load IF-MIB
	if _, err := parser.LoadModule("IF-MIB"); err != nil {
		t.Fatalf("Failed to load IF-MIB: %v", err)
	}

	// Parse the module
	oids, err := parser.ParseModule("IF-MIB")
	if err != nil {
		t.Fatalf("Failed to parse IF-MIB: %v", err)
	}

	// Find ifOperStatus (should have enum values)
	var ifOperStatus *mib.OIDMetadata
	for _, oid := range oids {
		if oid.Name == "ifOperStatus" {
			ifOperStatus = oid
			break
		}
	}

	if ifOperStatus == nil {
		t.Fatal("ifOperStatus not found in IF-MIB")
	}

	// Test that enum values were extracted
	if len(ifOperStatus.EnumValues) == 0 {
		t.Error("Expected enum values for ifOperStatus, got none")
	}

	// Check for specific enum values
	expectedEnums := map[int64]string{
		1: "up",
		2: "down",
		3: "testing",
		4: "unknown",
		5: "dormant",
		6: "notPresent",
		7: "lowerLayerDown",
	}

	for value, name := range expectedEnums {
		actualName, ok := ifOperStatus.EnumValues[value]
		if !ok {
			t.Errorf("Expected enum value %d to be present", value)
		} else if actualName != name {
			t.Errorf("Expected enum %d to be '%s', got '%s'", value, name, actualName)
		}
	}
}

// TestTableRelationshipDetection tests detection of table hierarchy
func TestTableRelationshipDetection(t *testing.T) {
	parser := New(testsupport.MIBDir("standard"))
	if err := parser.Init(); err != nil {
		t.Fatalf("Failed to init parser: %v", err)
	}
	defer parser.Close()

	// Load IF-MIB
	if _, err := parser.LoadModule("IF-MIB"); err != nil {
		t.Fatalf("Failed to load IF-MIB: %v", err)
	}

	// Parse the module
	oids, err := parser.ParseModule("IF-MIB")
	if err != nil {
		t.Fatalf("Failed to parse IF-MIB: %v", err)
	}

	// Find a table column (ifDescr)
	var ifDescr *mib.OIDMetadata
	for _, oid := range oids {
		if oid.Name == "ifDescr" {
			ifDescr = oid
			break
		}
	}

	if ifDescr == nil {
		t.Fatal("ifDescr not found in IF-MIB")
	}

	// Test that hierarchy is properly set
	if ifDescr.TableOID == "" {
		t.Error("Expected TableOID to be set for ifDescr")
	}

	if ifDescr.EntryOID == "" {
		t.Error("Expected EntryOID to be set for ifDescr")
	}

	// Verify the table OID is correct (ifTable = 1.3.6.1.2.1.2.2)
	expectedTableOID := "1.3.6.1.2.1.2.2"
	if ifDescr.TableOID != expectedTableOID {
		t.Errorf("Expected TableOID to be %s, got %s", expectedTableOID, ifDescr.TableOID)
	}

	// Verify the entry OID is correct (ifEntry = 1.3.6.1.2.1.2.2.1)
	expectedEntryOID := "1.3.6.1.2.1.2.2.1"
	if ifDescr.EntryOID != expectedEntryOID {
		t.Errorf("Expected EntryOID to be %s, got %s", expectedEntryOID, ifDescr.EntryOID)
	}
}

// TestObjectClassification tests classification of objects into semantic roles
func TestObjectClassification(t *testing.T) {
	parser := New(testsupport.MIBDir("standard"))
	if err := parser.Init(); err != nil {
		t.Fatalf("Failed to init parser: %v", err)
	}
	defer parser.Close()

	// Load IF-MIB
	if _, err := parser.LoadModule("IF-MIB"); err != nil {
		t.Fatalf("Failed to load IF-MIB: %v", err)
	}

	// Parse the module
	oids, err := parser.ParseModule("IF-MIB")
	if err != nil {
		t.Fatalf("Failed to parse IF-MIB: %v", err)
	}

	// Create a map for easy lookup
	oidMap := make(map[string]*mib.OIDMetadata)
	for _, oid := range oids {
		oidMap[oid.Name] = oid
	}

	// Test cases for different object classifications
	tests := []struct {
		name                string
		expectedClass       mib.ObjectClass
		expectedIsIndex     bool
		expectedIsChartable bool
	}{
		// Scalar
		{"ifNumber", mib.ObjectClassScalar, false, true},

		// Table node
		{"ifTable", mib.ObjectClassTableNode, false, false},

		// Entry node
		{"ifEntry", mib.ObjectClassEntryNode, false, false},

		// Index column
		{"ifIndex", mib.ObjectClassIndexColumn, true, false},

		// Status columns (strings and enums)
		{"ifDescr", mib.ObjectClassStatusColumn, false, false},
		{"ifOperStatus", mib.ObjectClassStatusColumn, false, false},
		{"ifAdminStatus", mib.ObjectClassStatusColumn, false, false},

		// Metric columns (counters and gauges)
		{"ifInOctets", mib.ObjectClassMetricColumn, false, true},
		{"ifOutOctets", mib.ObjectClassMetricColumn, false, true},
		{"ifInErrors", mib.ObjectClassMetricColumn, false, true},
		{"ifSpeed", mib.ObjectClassMetricColumn, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oid, ok := oidMap[tt.name]
			if !ok {
				t.Fatalf("OID %s not found in parsed results", tt.name)
			}

			if oid.ObjectClass != tt.expectedClass {
				t.Errorf("Expected ObjectClass to be %s, got %s", tt.expectedClass, oid.ObjectClass)
			}

			if oid.IsIndex != tt.expectedIsIndex {
				t.Errorf("Expected IsIndex to be %v, got %v", tt.expectedIsIndex, oid.IsIndex)
			}

			if oid.IsChartable != tt.expectedIsChartable {
				t.Errorf("Expected IsChartable to be %v, got %v", tt.expectedIsChartable, oid.IsChartable)
			}
		})
	}
}

// TestChartTypeAssignment tests that appropriate chart types are assigned
func TestChartTypeAssignment(t *testing.T) {
	parser := New(testsupport.MIBDir("standard"))
	if err := parser.Init(); err != nil {
		t.Fatalf("Failed to init parser: %v", err)
	}
	defer parser.Close()

	// Load IF-MIB
	if _, err := parser.LoadModule("IF-MIB"); err != nil {
		t.Fatalf("Failed to load IF-MIB: %v", err)
	}

	// Parse the module
	oids, err := parser.ParseModule("IF-MIB")
	if err != nil {
		t.Fatalf("Failed to parse IF-MIB: %v", err)
	}

	// Create a map for easy lookup
	oidMap := make(map[string]*mib.OIDMetadata)
	for _, oid := range oids {
		oidMap[oid.Name] = oid
	}

	// Test cases for chart type assignment
	tests := []struct {
		name              string
		expectedChartType string
	}{
		{"ifInOctets", "line"},  // Counter should be line chart
		{"ifOutOctets", "line"}, // Counter should be line chart
		{"ifSpeed", "gauge"},    // Gauge32 should be gauge chart
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oid, ok := oidMap[tt.name]
			if !ok {
				t.Fatalf("OID %s not found in parsed results", tt.name)
			}

			if !oid.IsChartable {
				t.Error("Expected OID to be chartable")
			}

			if oid.ChartType != tt.expectedChartType {
				t.Errorf("Expected ChartType to be %s, got %s", tt.expectedChartType, oid.ChartType)
			}
		})
	}
}

// TestIndexColumnDetection tests that index columns are properly identified
func TestIndexColumnDetection(t *testing.T) {
	parser := New(testsupport.MIBDir("standard"))
	if err := parser.Init(); err != nil {
		t.Fatalf("Failed to init parser: %v", err)
	}
	defer parser.Close()

	// Load IF-MIB
	if _, err := parser.LoadModule("IF-MIB"); err != nil {
		t.Fatalf("Failed to load IF-MIB: %v", err)
	}

	// Parse the module
	oids, err := parser.ParseModule("IF-MIB")
	if err != nil {
		t.Fatalf("Failed to parse IF-MIB: %v", err)
	}

	// Find ifIndex
	var ifIndex *mib.OIDMetadata
	for _, oid := range oids {
		if oid.Name == "ifIndex" {
			ifIndex = oid
			break
		}
	}

	if ifIndex == nil {
		t.Fatal("ifIndex not found in IF-MIB")
	}

	// Test that ifIndex is identified as an index
	if !ifIndex.IsIndex {
		t.Error("Expected ifIndex to be marked as IsIndex=true")
	}

	if ifIndex.ObjectClass != mib.ObjectClassIndexColumn {
		t.Errorf("Expected ObjectClass to be IndexColumn, got %s", ifIndex.ObjectClass)
	}

	// Index columns should not be chartable
	if ifIndex.IsChartable {
		t.Error("Expected index column to not be chartable")
	}

	// Verify IndexFor is set correctly
	expectedIndexFor := "1.3.6.1.2.1.2.2.1"
	if ifIndex.IndexFor != expectedIndexFor {
		t.Errorf("Expected IndexFor to be %s, got %s", expectedIndexFor, ifIndex.IndexFor)
	}
}

// TestDisplayability tests that IsDisplayable flag is set correctly
func TestDisplayability(t *testing.T) {
	parser := New(testsupport.MIBDir("standard"))
	if err := parser.Init(); err != nil {
		t.Fatalf("Failed to init parser: %v", err)
	}
	defer parser.Close()

	// Load IF-MIB
	if _, err := parser.LoadModule("IF-MIB"); err != nil {
		t.Fatalf("Failed to load IF-MIB: %v", err)
	}

	// Parse the module
	oids, err := parser.ParseModule("IF-MIB")
	if err != nil {
		t.Fatalf("Failed to parse IF-MIB: %v", err)
	}

	// Create a map for easy lookup
	oidMap := make(map[string]*mib.OIDMetadata)
	for _, oid := range oids {
		oidMap[oid.Name] = oid
	}

	// Test cases for displayability
	tests := []struct {
		name                  string
		expectedIsDisplayable bool
	}{
		{"ifNumber", true},     // Scalar should be displayable
		{"ifDescr", true},      // Status column should be displayable
		{"ifOperStatus", true}, // Status column should be displayable
		{"ifTable", false},     // Table node should not be displayable
		{"ifEntry", false},     // Entry node should not be displayable
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oid, ok := oidMap[tt.name]
			if !ok {
				t.Fatalf("OID %s not found in parsed results", tt.name)
			}

			if oid.IsDisplayable != tt.expectedIsDisplayable {
				t.Errorf("Expected IsDisplayable to be %v, got %v", tt.expectedIsDisplayable, oid.IsDisplayable)
			}
		})
	}
}

// TestBackwardCompatibility ensures existing fields are still populated correctly
func TestBackwardCompatibility(t *testing.T) {
	parser := New(testsupport.MIBDir("standard"))
	if err := parser.Init(); err != nil {
		t.Fatalf("Failed to init parser: %v", err)
	}
	defer parser.Close()

	// Load IF-MIB
	if _, err := parser.LoadModule("IF-MIB"); err != nil {
		t.Fatalf("Failed to load IF-MIB: %v", err)
	}

	// Parse the module
	oids, err := parser.ParseModule("IF-MIB")
	if err != nil {
		t.Fatalf("Failed to parse IF-MIB: %v", err)
	}

	// Find ifDescr to test backward compatibility
	var ifDescr *mib.OIDMetadata
	for _, oid := range oids {
		if oid.Name == "ifDescr" {
			ifDescr = oid
			break
		}
	}

	if ifDescr == nil {
		t.Fatal("ifDescr not found in IF-MIB")
	}

	// Test that existing fields are still populated
	if ifDescr.OID == "" {
		t.Error("Expected OID to be populated")
	}

	if ifDescr.Name != "ifDescr" {
		t.Errorf("Expected Name to be 'ifDescr', got '%s'", ifDescr.Name)
	}

	if ifDescr.Module != "IF-MIB" {
		t.Errorf("Expected Module to be 'IF-MIB', got '%s'", ifDescr.Module)
	}

	if ifDescr.Type == "" {
		t.Error("Expected Type to be populated")
	}

	if !ifDescr.IsColumn {
		t.Error("Expected IsColumn to be true")
	}

	if ifDescr.Pollable != ifDescr.IsPollable() {
		t.Error("Expected Pollable to match IsPollable()")
	}
}

// TestSNMPv2MIBScalars tests scalar object classification
func TestSNMPv2MIBScalars(t *testing.T) {
	parser := New(testsupport.MIBDir("standard"))
	if err := parser.Init(); err != nil {
		t.Fatalf("Failed to init parser: %v", err)
	}
	defer parser.Close()

	// Load SNMPv2-MIB
	if _, err := parser.LoadModule("SNMPv2-MIB"); err != nil {
		t.Fatalf("Failed to load SNMPv2-MIB: %v", err)
	}

	// Parse the module
	oids, err := parser.ParseModule("SNMPv2-MIB")
	if err != nil {
		t.Fatalf("Failed to parse SNMPv2-MIB: %v", err)
	}

	// Find sysDescr (a scalar)
	var sysDescr *mib.OIDMetadata
	for _, oid := range oids {
		if oid.Name == "sysDescr" {
			sysDescr = oid
			break
		}
	}

	if sysDescr == nil {
		t.Fatal("sysDescr not found in SNMPv2-MIB")
	}

	// Test scalar classification
	if sysDescr.ObjectClass != mib.ObjectClassScalar {
		t.Errorf("Expected ObjectClass to be Scalar, got %s", sysDescr.ObjectClass)
	}

	if !sysDescr.IsDisplayable {
		t.Error("Expected scalar to be displayable")
	}

	// String scalars should not be chartable
	if sysDescr.IsChartable {
		t.Error("Expected string scalar to not be chartable")
	}

	// Also check sysServices - it should be a scalar but NOT chartable (bitmask)
	var sysServices *mib.OIDMetadata
	for _, oid := range oids {
		if oid.Name == "sysServices" {
			sysServices = oid
			break
		}
	}

	if sysServices == nil {
		t.Fatal("sysServices not found in SNMPv2-MIB")
	}

	// sysServices should be a scalar
	if sysServices.ObjectClass != mib.ObjectClassScalar {
		t.Errorf("Expected sysServices ObjectClass to be Scalar, got %s", sysServices.ObjectClass)
	}

	// sysServices should NOT be chartable (INTEGER bitmask)
	if sysServices.IsChartable {
		t.Error("Expected sysServices to NOT be chartable (bitmask indicator in description)")
	}
}

// TestEnumValuesMap tests that EnumValues map is properly initialized
func TestEnumValuesMap(t *testing.T) {
	parser := New(testsupport.MIBDir("standard"))
	if err := parser.Init(); err != nil {
		t.Fatalf("Failed to init parser: %v", err)
	}
	defer parser.Close()

	// Load IF-MIB
	if _, err := parser.LoadModule("IF-MIB"); err != nil {
		t.Fatalf("Failed to load IF-MIB: %v", err)
	}

	// Parse the module
	oids, err := parser.ParseModule("IF-MIB")
	if err != nil {
		t.Fatalf("Failed to parse IF-MIB: %v", err)
	}

	// Find a non-enum field (ifDescr) - should have nil or empty map
	var ifDescr *mib.OIDMetadata
	for _, oid := range oids {
		if oid.Name == "ifDescr" {
			ifDescr = oid
			break
		}
	}

	if ifDescr == nil {
		t.Fatal("ifDescr not found in IF-MIB")
	}

	// ifDescr is a string, should not have enum values
	if len(ifDescr.EnumValues) > 0 {
		t.Error("Expected ifDescr to not have enum values")
	}
}
