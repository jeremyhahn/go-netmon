package snmp

import (
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/jeremyhahn/go-netmon/internal/config"
)

func TestNewClient_ValidSNMPv2c(t *testing.T) {
	device := &config.Device{
		Name:        "test-device",
		Address:     "203.0.113.1",
		SNMPVersion: "2c",
		Community:   "public",
		Enabled:     true,
	}

	client, err := NewClient(device, 10*time.Second, 3)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if client == nil {
		t.Fatal("Expected client to be created")
	}

	if client.device != device {
		t.Error("Client device doesn't match input device")
	}

	if client.conn.Version != gosnmp.Version2c {
		t.Errorf("Expected Version2c, got %v", client.conn.Version)
	}

	if client.conn.Community != "public" {
		t.Errorf("Expected community 'public', got %s", client.conn.Community)
	}
}

func TestNewClient_ValidSNMPv3(t *testing.T) {
	device := &config.Device{
		Name:         "test-device",
		Address:      "203.0.113.1",
		SNMPVersion:  "3",
		Username:     "admin",
		AuthProtocol: "SHA256",
		AuthPassword: "authpass",
		PrivProtocol: "AES",
		PrivPassword: "privpass",
		Enabled:      true,
	}

	client, err := NewClient(device, 10*time.Second, 3)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if client == nil {
		t.Fatal("Expected client to be created")
	}

	if client.conn.Version != gosnmp.Version3 {
		t.Errorf("Expected Version3, got %v", client.conn.Version)
	}

	secParams, ok := client.conn.SecurityParameters.(*gosnmp.UsmSecurityParameters)
	if !ok {
		t.Fatal("Expected UsmSecurityParameters")
	}

	if secParams.UserName != "admin" {
		t.Errorf("Expected username 'admin', got %s", secParams.UserName)
	}

	if secParams.AuthenticationProtocol != gosnmp.SHA256 {
		t.Errorf("Expected SHA256 auth protocol, got %v", secParams.AuthenticationProtocol)
	}

	if secParams.PrivacyProtocol != gosnmp.AES {
		t.Errorf("Expected AES privacy protocol, got %v", secParams.PrivacyProtocol)
	}
}

func TestNewClient_NilDevice(t *testing.T) {
	_, err := NewClient(nil, 10*time.Second, 3)
	if err == nil {
		t.Error("Expected error for nil device")
	}
}

func TestNewClient_InvalidDevice(t *testing.T) {
	device := &config.Device{
		Name:        "", // Invalid: empty name
		Address:     "203.0.113.1",
		SNMPVersion: "2c",
		Community:   "public",
	}

	_, err := NewClient(device, 10*time.Second, 3)
	if err == nil {
		t.Error("Expected error for invalid device")
	}
}

func TestNewClient_UnsupportedVersion(t *testing.T) {
	device := &config.Device{
		Name:    "test",
		Address: "203.0.113.1",
		// "2" is not a version this speaks. It used to be "1", which is
		// supported now — a v1-only device could be configured and never
		// polled, so the client learned to speak it.
		SNMPVersion: "2",
		Community:   "public",
		Enabled:     true,
	}

	_, err := NewClient(device, 10*time.Second, 3)
	if err == nil {
		t.Error("Expected error for unsupported SNMP version")
	}
}

func TestNewClient_UnsupportedAuthProtocol(t *testing.T) {
	device := &config.Device{
		Name:         "test",
		Address:      "203.0.113.1",
		SNMPVersion:  "3",
		Username:     "admin",
		AuthProtocol: "INVALID",
		AuthPassword: "pass",
		Enabled:      true,
	}

	_, err := NewClient(device, 10*time.Second, 3)
	if err == nil {
		t.Error("Expected error for unsupported auth protocol")
	}
}

func TestNewClient_UnsupportedPrivProtocol(t *testing.T) {
	device := &config.Device{
		Name:         "test",
		Address:      "203.0.113.1",
		SNMPVersion:  "3",
		Username:     "admin",
		PrivProtocol: "INVALID",
		PrivPassword: "pass",
		Enabled:      true,
	}

	_, err := NewClient(device, 10*time.Second, 3)
	if err == nil {
		t.Error("Expected error for unsupported priv protocol")
	}
}

func TestClient_GetDevice(t *testing.T) {
	device := &config.Device{
		Name:        "test-device",
		Address:     "203.0.113.1",
		SNMPVersion: "2c",
		Community:   "public",
		Enabled:     true,
	}

	client, err := NewClient(device, 10*time.Second, 3)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	if client.GetDevice() != device {
		t.Error("GetDevice() returned wrong device")
	}
}

func TestClient_IsConnected(t *testing.T) {
	device := &config.Device{
		Name:        "test-device",
		Address:     "203.0.113.1",
		SNMPVersion: "2c",
		Community:   "public",
		Enabled:     true,
	}

	client, err := NewClient(device, 10*time.Second, 3)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	// Client should not be connected initially
	if client.IsConnected() {
		t.Error("Client should not be connected before Connect() is called")
	}
}

func TestResult_TypeString(t *testing.T) {
	tests := []struct {
		name     string
		asnType  gosnmp.Asn1BER
		expected string
	}{
		{"Integer", gosnmp.Integer, "Integer"},
		{"Counter32", gosnmp.Counter32, "Counter32"},
		{"Counter64", gosnmp.Counter64, "Counter64"},
		{"Gauge32", gosnmp.Gauge32, "Gauge32"},
		{"OctetString", gosnmp.OctetString, "OctetString"},
		{"TimeTicks", gosnmp.TimeTicks, "TimeTicks"},
		{"IPAddress", gosnmp.IPAddress, "IPAddress"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := &Result{
				OID:  "1.3.6.1.2.1.1.1.0",
				Type: tt.asnType,
			}

			if result.TypeString() != tt.expected {
				t.Errorf("Expected type string '%s', got '%s'", tt.expected, result.TypeString())
			}
		})
	}
}

func TestResult_IsCounter(t *testing.T) {
	tests := []struct {
		name     string
		asnType  gosnmp.Asn1BER
		expected bool
	}{
		{"Counter32", gosnmp.Counter32, true},
		{"Counter64", gosnmp.Counter64, true},
		{"Gauge32", gosnmp.Gauge32, false},
		{"Integer", gosnmp.Integer, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := &Result{Type: tt.asnType}
			if result.IsCounter() != tt.expected {
				t.Errorf("Expected IsCounter() = %v for %s", tt.expected, tt.name)
			}
		})
	}
}

func TestResult_IsGauge(t *testing.T) {
	tests := []struct {
		name     string
		asnType  gosnmp.Asn1BER
		expected bool
	}{
		{"Gauge32", gosnmp.Gauge32, true},
		{"Integer", gosnmp.Integer, true},
		{"Uinteger32", gosnmp.Uinteger32, true},
		{"Counter32", gosnmp.Counter32, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := &Result{Type: tt.asnType}
			if result.IsGauge() != tt.expected {
				t.Errorf("Expected IsGauge() = %v for %s", tt.expected, tt.name)
			}
		})
	}
}

func TestResult_GetInt64Value(t *testing.T) {
	tests := []struct {
		name        string
		value       interface{}
		expected    int64
		expectError bool
	}{
		{"int", int(42), 42, false},
		{"int32", int32(42), 42, false},
		{"int64", int64(42), 42, false},
		{"uint32", uint32(42), 42, false},
		{"uint64", uint64(42), 42, false},
		{"string", "invalid", 0, true},
		{"uint64 overflow", uint64(9223372036854775808), 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := &Result{Value: tt.value}
			val, err := result.GetInt64Value()

			if tt.expectError && err == nil {
				t.Error("Expected error but got nil")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Expected no error but got: %v", err)
			}
			if !tt.expectError && val != tt.expected {
				t.Errorf("Expected value %d, got %d", tt.expected, val)
			}
		})
	}
}

func TestResult_GetStringValue(t *testing.T) {
	tests := []struct {
		name        string
		value       interface{}
		expected    string
		expectError bool
	}{
		{"string", "test", "test", false},
		{"byte slice", []byte("test"), "test", false},
		{"int", 42, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := &Result{Value: tt.value}
			val, err := result.GetStringValue()

			if tt.expectError && err == nil {
				t.Error("Expected error but got nil")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Expected no error but got: %v", err)
			}
			if !tt.expectError && val != tt.expected {
				t.Errorf("Expected value %s, got %s", tt.expected, val)
			}
		})
	}
}

func TestDeviceInfo_String(t *testing.T) {
	info := &DeviceInfo{
		Address:     "203.0.113.1",
		Name:        "test-router",
		Description: "Cisco Router",
		Uptime:      24 * time.Hour,
	}

	str := info.String()
	if str == "" {
		t.Error("Expected non-empty string")
	}

	// Check that key information is present
	if !contains(str, "test-router") {
		t.Error("Expected device name in string")
	}
	if !contains(str, "203.0.113.1") {
		t.Error("Expected device address in string")
	}
}

// Helper function
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) &&
		(s[:len(substr)] == substr || s[len(s)-len(substr):] == substr ||
			containsMiddle(s, substr)))
}

func containsMiddle(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
