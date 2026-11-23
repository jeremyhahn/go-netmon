package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/testsupport"
)

// TestChartability_TextualConventions tests that textual conventions are NOT chartable
func TestChartability_TextualConventions(t *testing.T) {
	parser := New(testsupport.MIBDir("standard"))
	require.NoError(t, parser.Init())
	defer parser.Close()

	oids, err := parser.ParseModule("SNMPv2-MIB")
	require.NoError(t, err)

	tests := []struct {
		name        string
		oidName     string
		shouldChart bool
		reason      string
	}{
		{
			name:        "sysORLastChange - TimeStamp",
			oidName:     "sysORLastChange",
			shouldChart: false,
			reason:      "TimeStamp is a textual convention representing timestamp, not a time-series metric",
		},
		{
			name:        "sysUpTime - TimeTicks (Counter)",
			oidName:     "sysUpTime",
			shouldChart: true,
			reason:      "TimeTicks is Counter-like and measures uptime, should be chartable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var found *mib.OIDMetadata
			for _, oid := range oids {
				if oid.Name == tt.oidName {
					found = oid
					break
				}
			}
			require.NotNil(t, found, "OID %s not found", tt.oidName)

			assert.Equal(t, tt.shouldChart, found.IsChartable,
				"OID %s chartability mismatch: %s (Type=%s, Syntax=%s, Description=%s)",
				tt.oidName, tt.reason, found.Type, found.Syntax, found.Description)
		})
	}
}

// TestChartability_IntegerBitmasks tests that INTEGER bitmasks are NOT chartable
func TestChartability_IntegerBitmasks(t *testing.T) {
	parser := New(testsupport.MIBDir("standard"))
	require.NoError(t, parser.Init())
	defer parser.Close()

	oids, err := parser.ParseModule("SNMPv2-MIB")
	require.NoError(t, err)

	tests := []struct {
		name        string
		oidName     string
		shouldChart bool
		reason      string
	}{
		{
			name:        "sysServices - bitmask",
			oidName:     "sysServices",
			shouldChart: false,
			reason:      "sysServices is an INTEGER bitmask representing OSI layer capabilities, not a time-series metric",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var found *mib.OIDMetadata
			for _, oid := range oids {
				if oid.Name == tt.oidName {
					found = oid
					break
				}
			}
			require.NotNil(t, found, "OID %s not found", tt.oidName)

			assert.Equal(t, tt.shouldChart, found.IsChartable,
				"OID %s chartability mismatch: %s (Type=%s, EnumValues=%v, Description=%s)",
				tt.oidName, tt.reason, found.Type, found.EnumValues, found.Description)
		})
	}
}

// TestChartability_IntegerEnums tests that INTEGER enums are NOT chartable
func TestChartability_IntegerEnums(t *testing.T) {
	parser := New(testsupport.MIBDir("standard"))
	require.NoError(t, parser.Init())
	defer parser.Close()

	oids, err := parser.ParseModule("IF-MIB")
	require.NoError(t, err)

	// Find ifTable columns with enums
	var ifAdminStatus, ifOperStatus *mib.OIDMetadata
	for _, oid := range oids {
		switch oid.Name {
		case "ifAdminStatus":
			ifAdminStatus = oid
		case "ifOperStatus":
			ifOperStatus = oid
		}
	}

	require.NotNil(t, ifAdminStatus, "ifAdminStatus not found")
	require.NotNil(t, ifOperStatus, "ifOperStatus not found")

	// Both should have enum values
	assert.NotEmpty(t, ifAdminStatus.EnumValues, "ifAdminStatus should have enum values")
	assert.NotEmpty(t, ifOperStatus.EnumValues, "ifOperStatus should have enum values")

	// Both should NOT be chartable (they're status enums)
	assert.False(t, ifAdminStatus.IsChartable,
		"ifAdminStatus (enum) should not be chartable (Type=%s, EnumValues=%v)",
		ifAdminStatus.Type, ifAdminStatus.EnumValues)
	assert.False(t, ifOperStatus.IsChartable,
		"ifOperStatus (enum) should not be chartable (Type=%s, EnumValues=%v)",
		ifOperStatus.Type, ifOperStatus.EnumValues)
}

// TestChartability_CountersAndGauges tests that real counters and gauges ARE chartable
func TestChartability_CountersAndGauges(t *testing.T) {
	parser := New(testsupport.MIBDir("standard"))
	require.NoError(t, parser.Init())
	defer parser.Close()

	oids, err := parser.ParseModule("IF-MIB")
	require.NoError(t, err)

	tests := []struct {
		name         string
		oidName      string
		shouldChart  bool
		expectedType string
		reason       string
	}{
		{
			name:         "ifInOctets - Counter32",
			oidName:      "ifInOctets",
			shouldChart:  true,
			expectedType: "Counter32",
			reason:       "Counter32 metrics should always be chartable",
		},
		{
			name:         "ifOutOctets - Counter32",
			oidName:      "ifOutOctets",
			shouldChart:  true,
			expectedType: "Counter32",
			reason:       "Counter32 metrics should always be chartable",
		},
		{
			name:         "ifSpeed - Gauge32",
			oidName:      "ifSpeed",
			shouldChart:  true,
			expectedType: "Gauge32",
			reason:       "Interface speed is a meaningful time-series gauge",
		},
		{
			name:         "ifMtu - Integer32",
			oidName:      "ifMtu",
			shouldChart:  false,
			expectedType: "Integer32",
			reason:       "MTU is a static configuration value, not a time-series metric",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var found *mib.OIDMetadata
			for _, oid := range oids {
				if oid.Name == tt.oidName {
					found = oid
					break
				}
			}
			require.NotNil(t, found, "OID %s not found", tt.oidName)

			assert.Equal(t, tt.expectedType, found.Type, "Type mismatch for %s", tt.oidName)
			assert.Equal(t, tt.shouldChart, found.IsChartable,
				"OID %s chartability mismatch: %s (Type=%s, Description=%s)",
				tt.oidName, tt.reason, found.Type, found.Description)
		})
	}
}

// TestChartability_DescriptionKeywords tests semantic analysis of descriptions
func TestChartability_DescriptionKeywords(t *testing.T) {
	parser := New(testsupport.MIBDir("standard"))
	require.NoError(t, parser.Init())
	defer parser.Close()

	// Test with SNMPv2-MIB
	oids, err := parser.ParseModule("SNMPv2-MIB")
	require.NoError(t, err)

	// sysServices description contains "bit" and describes layer capabilities
	// This is a bitmask, not a time-series metric
	var sysServices *mib.OIDMetadata
	for _, oid := range oids {
		if oid.Name == "sysServices" {
			sysServices = oid
			break
		}
	}
	require.NotNil(t, sysServices, "sysServices not found")

	// Verify the description mentions "layer" or "service" which are indicators of bitmasks
	t.Logf("sysServices Description: %s", sysServices.Description)

	assert.False(t, sysServices.IsChartable,
		"sysServices should not be chartable (bitmask indicator in description)")
}

// TestChartability_GenericApproach tests that the solution works generically without hardcoding
func TestChartability_GenericApproach(t *testing.T) {
	parser := New(testsupport.MIBDir("standard"))
	require.NoError(t, parser.Init())
	defer parser.Close()

	modules := []string{"SNMPv2-MIB", "IF-MIB"}

	for _, moduleName := range modules {
		t.Run(moduleName, func(t *testing.T) {
			oids, err := parser.ParseModule(moduleName)
			require.NoError(t, err)

			for _, oid := range oids {
				if !oid.IsScalar && !oid.IsColumn {
					continue
				}

				// Apply generic rules
				if len(oid.EnumValues) > 0 {
					assert.False(t, oid.IsChartable,
						"OID with enums should not be chartable: %s (Type=%s, EnumValues=%v)",
						oid.Name, oid.Type, oid.EnumValues)
				}

				if oid.IsCounter() {
					assert.True(t, oid.IsChartable,
						"Counter should be chartable: %s (Type=%s)",
						oid.Name, oid.Type)
				}
			}
		})
	}
}

// TestChartability_EdgeCases tests edge cases and special scenarios
func TestChartability_EdgeCases(t *testing.T) {
	parser := New(testsupport.MIBDir("standard"))
	require.NoError(t, parser.Init())
	defer parser.Close()

	oids, err := parser.ParseModule("SNMPv2-MIB")
	require.NoError(t, err)

	tests := []struct {
		name        string
		oidName     string
		shouldChart bool
		reason      string
	}{
		{
			name:        "sysDescr - DisplayString",
			oidName:     "sysDescr",
			shouldChart: false,
			reason:      "String types should never be chartable",
		},
		{
			name:        "sysObjectID - ObjectIdentifier",
			oidName:     "sysObjectID",
			shouldChart: false,
			reason:      "OID types should never be chartable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var found *mib.OIDMetadata
			for _, oid := range oids {
				if oid.Name == tt.oidName {
					found = oid
					break
				}
			}
			require.NotNil(t, found, "OID %s not found", tt.oidName)

			assert.Equal(t, tt.shouldChart, found.IsChartable,
				"OID %s chartability mismatch: %s (Type=%s)",
				tt.oidName, tt.reason, found.Type)
		})
	}
}
