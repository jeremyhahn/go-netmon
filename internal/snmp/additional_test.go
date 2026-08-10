package snmp

import (
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
)

// TestResult_GetFloat64Value tests the critical GetFloat64Value function used by metrics
func TestResult_GetFloat64Value(t *testing.T) {
	tests := []struct {
		name        string
		value       interface{}
		expected    float64
		expectError bool
	}{
		// Integer types
		{"int", int(42), 42.0, false},
		{"int8", int8(42), 42.0, false},
		{"int16", int16(42), 42.0, false},
		{"int32", int32(42), 42.0, false},
		{"int64", int64(42), 42.0, false},

		// Unsigned integer types
		{"uint", uint(42), 42.0, false},
		{"uint8", uint8(42), 42.0, false},
		{"uint16", uint16(42), 42.0, false},
		{"uint32", uint32(42), 42.0, false},
		{"uint64", uint64(42), 42.0, false},

		// Error cases (SNMP doesn't use float types, so these should error)
		{"float32", float32(42.5), 0, true},
		{"float64", float64(42.5), 0, true},
		{"nil", nil, 0, true},
		{"string", "invalid", 0, true},
		{"[]byte", []byte("test"), 0, true},
		{"bool", true, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := &Result{
				OID:   "1.3.6.1.2.1.1.3.0",
				Value: tt.value,
			}

			val, err := result.GetFloat64Value()

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error for %v (%T) but got nil", tt.value, tt.value)
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error for %v (%T) but got: %v", tt.value, tt.value, err)
				}
				if val != tt.expected {
					t.Errorf("Expected value %.2f, got %.2f for %v (%T)", tt.expected, val, tt.value, tt.value)
				}
			}
		})
	}
}

// TestResult_GetUint64Value tests the GetUint64Value function
func TestResult_GetUint64Value(t *testing.T) {
	tests := []struct {
		name        string
		value       interface{}
		expected    uint64
		expectError bool
	}{
		// Integer types (positive)
		{"int", int(42), 42, false},
		{"int8", int8(42), 42, false},
		{"int16", int16(42), 42, false},
		{"int32", int32(42), 42, false},
		{"int64", int64(42), 42, false},

		// Unsigned integer types
		{"uint", uint(42), 42, false},
		{"uint8", uint8(42), 42, false},
		{"uint16", uint16(42), 42, false},
		{"uint32", uint32(42), 42, false},
		{"uint64", uint64(42), 42, false},

		// Large values
		{"uint64 max", uint64(18446744073709551615), 18446744073709551615, false},

		// Negative values should error
		{"int negative", int(-42), 0, true},
		{"int64 negative", int64(-42), 0, true},

		// Error cases
		{"nil", nil, 0, true},
		{"string", "invalid", 0, true},
		{"[]byte", []byte("test"), 0, true},
		{"float64", float64(42.5), 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := &Result{
				OID:   "1.3.6.1.2.1.1.3.0",
				Value: tt.value,
			}

			val, err := result.GetUint64Value()

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error for %v (%T) but got nil", tt.value, tt.value)
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error for %v (%T) but got: %v", tt.value, tt.value, err)
				}
				if val != tt.expected {
					t.Errorf("Expected value %d, got %d for %v (%T)", tt.expected, val, tt.value, tt.value)
				}
			}
		})
	}
}

// TestResult_IsString tests the IsString method
func TestResult_IsString(t *testing.T) {
	tests := []struct {
		name     string
		asnType  gosnmp.Asn1BER
		expected bool
	}{
		{"OctetString", gosnmp.OctetString, true},
		{"Counter32", gosnmp.Counter32, false},
		{"Counter64", gosnmp.Counter64, false},
		{"Gauge32", gosnmp.Gauge32, false},
		{"Integer", gosnmp.Integer, false},
		{"TimeTicks", gosnmp.TimeTicks, false},
		{"IPAddress", gosnmp.IPAddress, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := &Result{
				OID:  "1.3.6.1.2.1.1.1.0",
				Type: tt.asnType,
			}

			if result.IsString() != tt.expected {
				t.Errorf("Expected IsString() = %v for %s (type %v)", tt.expected, tt.name, tt.asnType)
			}
		})
	}
}

// TestResult_String tests the String method
func TestResult_String(t *testing.T) {
	tests := []struct {
		name     string
		result   *Result
		contains []string // Substrings that should be present
	}{
		{
			name: "basic result",
			result: &Result{
				OID:       "1.3.6.1.2.1.1.1.0",
				Type:      gosnmp.OctetString,
				Value:     "test value",
				Timestamp: time.Now(),
			},
			contains: []string{"1.3.6.1.2.1.1.1.0", "OctetString"},
		},
		{
			name: "counter result",
			result: &Result{
				OID:       "1.3.6.1.2.1.2.2.1.10.1",
				Type:      gosnmp.Counter32,
				Value:     uint32(12345),
				Timestamp: time.Now(),
			},
			contains: []string{"1.3.6.1.2.1.2.2.1.10.1", "Counter32"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			str := tt.result.String()

			if str == "" {
				t.Error("Expected non-empty string")
			}

			for _, substr := range tt.contains {
				if !containsSubstring(str, substr) {
					t.Errorf("Expected string to contain %q, got: %s", substr, str)
				}
			}
		})
	}
}

// TestResult_TypeString_AllTypes tests all SNMP type conversions
func TestResult_TypeString_AllTypes(t *testing.T) {
	tests := []struct {
		name     string
		asnType  gosnmp.Asn1BER
		expected string
	}{
		// Existing types
		{"Integer", gosnmp.Integer, "Integer"},
		{"Counter32", gosnmp.Counter32, "Counter32"},
		{"Counter64", gosnmp.Counter64, "Counter64"},
		{"Gauge32", gosnmp.Gauge32, "Gauge32"},
		{"OctetString", gosnmp.OctetString, "OctetString"},
		{"TimeTicks", gosnmp.TimeTicks, "TimeTicks"},
		{"IPAddress", gosnmp.IPAddress, "IPAddress"},

		// Additional types
		{"Boolean", gosnmp.Boolean, "Boolean"},
		{"BitString", gosnmp.BitString, "BitString"},
		{"Null", gosnmp.Null, "Null"},
		{"ObjectIdentifier", gosnmp.ObjectIdentifier, "ObjectIdentifier"},
		{"ObjectDescription", gosnmp.ObjectDescription, "ObjectDescription"},
		{"Opaque", gosnmp.Opaque, "Opaque"},
		{"NsapAddress", gosnmp.NsapAddress, "NsapAddress"},
		{"Uinteger32", gosnmp.Uinteger32, "Uinteger32"},

		// Error types
		{"NoSuchObject", gosnmp.NoSuchObject, "NoSuchObject"},
		{"NoSuchInstance", gosnmp.NoSuchInstance, "NoSuchInstance"},
		{"EndOfMibView", gosnmp.EndOfMibView, "EndOfMibView"},

		// Unknown type
		{"Unknown", gosnmp.Asn1BER(255), "Unknown(255)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := &Result{
				OID:  "1.3.6.1.2.1.1.1.0",
				Type: tt.asnType,
			}

			typeStr := result.TypeString()
			if typeStr != tt.expected {
				t.Errorf("Expected type string '%s', got '%s' for type %v",
					tt.expected, typeStr, tt.asnType)
			}
		})
	}
}

// Helper function to check if a string contains a substring
func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && findSubstring(s, substr)
}

func findSubstring(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	if len(s) < len(substr) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
