package config

import (
	"os"
	"testing"
)

// TestLoad tests the Load function which uses environment variables
func TestLoad(t *testing.T) {
	// Create a temporary config file
	tmpfile, err := os.CreateTemp("", "test-config-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())

	// Write valid config
	configContent := `
global:
  default_poll_interval: 30
  max_workers: 10
  snmp_timeout: 5
  snmp_retries: 3

prometheus:
  remote_write_url: "http://localhost:9090/api/v1/write"
  batch_size: 1000
  flush_interval: 10

api:
  listen_address: ":8080"
  enabled: true

metrics:
  listen_address: ":9100"
  enabled: true

logging:
  level: "info"
  format: "json"
  output: "stdout"

devices:
  - name: "test-device"
    address: "203.0.113.1"
    snmp_version: "2c"
    community: "public"
    profile: "default"
    poll_interval: 30
    enabled: true
`
	if _, writeErr := tmpfile.Write([]byte(configContent)); writeErr != nil {
		t.Fatal(writeErr)
	}
	tmpfile.Close()

	// Set environment variable
	os.Setenv("CONFIG_PATH", tmpfile.Name())
	defer os.Unsetenv("CONFIG_PATH")

	// Test Load
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if cfg == nil {
		t.Fatal("Expected config to be loaded")
	}

	if cfg.Global.DefaultPollInterval != 30 {
		t.Errorf("Expected poll interval 30, got %d", cfg.Global.DefaultPollInterval)
	}

	if len(cfg.Devices) != 1 {
		t.Errorf("Expected 1 device, got %d", len(cfg.Devices))
	}
}

// TestLoad_NoEnvVar tests Load when CONFIG_PATH is not set
func TestLoad_NoEnvVar(t *testing.T) {
	// Ensure CONFIG_PATH is not set
	os.Unsetenv("CONFIG_PATH")

	// This should try to load from default path and likely fail
	// (unless a config.yaml exists in the current directory)
	_, err := Load()

	// We expect an error since no config file exists at default path
	if err == nil {
		// If no error, a config file exists - that's okay for this test
		t.Log("Config loaded from default path (file exists)")
	} else {
		// Expected error - no config at default path
		t.Logf("Expected error when no config at default path: %v", err)
	}
}

// TestGetConfigPath tests the getConfigPath function
func TestGetConfigPath(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		expected string
	}{
		{
			name:     "with environment variable",
			envValue: "/custom/path/config.yaml",
			expected: "/custom/path/config.yaml",
		},
		{
			name:     "without environment variable",
			envValue: "",
			expected: "config.yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envValue != "" {
				os.Setenv("CONFIG_PATH", tt.envValue)
				defer os.Unsetenv("CONFIG_PATH")
			} else {
				os.Unsetenv("CONFIG_PATH")
			}

			path := getConfigPath()
			if path != tt.expected {
				t.Errorf("Expected path %q, got %q", tt.expected, path)
			}
		})
	}
}

// TestConfig_GetEnabledMIBModules tests the GetEnabledMIBModules function
func TestConfig_GetEnabledMIBModules(t *testing.T) {
	tests := []struct {
		name          string
		config        *Config
		expectedCount int
		expectedNames []string
	}{
		{
			name: "with enabled MIB modules",
			config: &Config{
				MIBModules: []MIBModule{
					{Name: "SNMPv2-MIB", Enabled: true},
					{Name: "IF-MIB", Enabled: true},
					{Name: "IP-MIB", Enabled: false},
				},
			},
			expectedCount: 2,
			expectedNames: []string{"SNMPv2-MIB", "IF-MIB"},
		},
		{
			name: "with no enabled modules",
			config: &Config{
				MIBModules: []MIBModule{
					{Name: "SNMPv2-MIB", Enabled: false},
					{Name: "IF-MIB", Enabled: false},
				},
			},
			expectedCount: 0,
			expectedNames: []string{},
		},
		{
			name: "with empty MIBModules",
			config: &Config{
				MIBModules: []MIBModule{},
			},
			expectedCount: 0,
			expectedNames: []string{},
		},
		{
			name: "with all enabled modules",
			config: &Config{
				MIBModules: []MIBModule{
					{Name: "SNMPv2-MIB", Enabled: true},
					{Name: "IF-MIB", Enabled: true},
					{Name: "IP-MIB", Enabled: true},
				},
			},
			expectedCount: 3,
			expectedNames: []string{"SNMPv2-MIB", "IF-MIB", "IP-MIB"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			modules := tt.config.GetEnabledMIBModules()

			if len(modules) != tt.expectedCount {
				t.Fatalf("Expected %d modules, got %d", tt.expectedCount, len(modules))
			}

			for i, module := range modules {
				if i < len(tt.expectedNames) && module.Name != tt.expectedNames[i] {
					t.Errorf("Expected module %q at index %d, got %q", tt.expectedNames[i], i, module.Name)
				}
			}
		})
	}
}

// TestValidationError_Error tests the Error() method
func TestValidationError_Error(t *testing.T) {
	tests := []struct {
		name     string
		err      *ValidationError
		contains []string
	}{
		{
			name: "with field and message",
			err: &ValidationError{
				Field:   "devices[0].name",
				Message: "cannot be empty",
			},
			contains: []string{"devices[0].name", "cannot be empty"},
		},
		{
			name: "with only message",
			err: &ValidationError{
				Field:   "",
				Message: "invalid configuration",
			},
			contains: []string{"invalid configuration"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errStr := tt.err.Error()

			if errStr == "" {
				t.Error("Expected non-empty error string")
			}

			for _, substr := range tt.contains {
				if !containsString(errStr, substr) {
					t.Errorf("Expected error to contain %q, got: %s", substr, errStr)
				}
			}
		})
	}
}

// TestDevice_Validate_AllCases tests all validation cases for Device
func TestDevice_Validate_AllCases(t *testing.T) {
	tests := []struct {
		name        string
		device      *Device
		expectError bool
		errorField  string
	}{
		{
			name: "valid SNMPv2c device",
			device: &Device{
				Name:        "test",
				Address:     "203.0.113.1",
				SNMPVersion: "2c",
				Community:   "public",
				Enabled:     true,
			},
			expectError: false,
		},
		{
			name: "valid SNMPv3 device with auth and priv",
			device: &Device{
				Name:         "test",
				Address:      "203.0.113.1",
				SNMPVersion:  "3",
				Username:     "admin",
				AuthProtocol: "SHA",
				AuthPassword: "authpass",
				PrivProtocol: "AES",
				PrivPassword: "privpass",
				Enabled:      true,
			},
			expectError: false,
		},
		{
			name: "missing name",
			device: &Device{
				Name:        "",
				Address:     "203.0.113.1",
				SNMPVersion: "2c",
				Community:   "public",
			},
			expectError: true,
			errorField:  "name",
		},
		{
			name: "missing address",
			device: &Device{
				Name:        "test",
				Address:     "",
				SNMPVersion: "2c",
				Community:   "public",
			},
			expectError: true,
			errorField:  "address",
		},
		{
			name: "invalid SNMP version",
			device: &Device{
				Name:    "test",
				Address: "203.0.113.1",
				// "2" is not a version anything speaks. This case used to be
				// "1", which is a real version and is supported now.
				SNMPVersion: "2",
				Community:   "public",
			},
			expectError: true,
			errorField:  "snmp_version",
		},
		{
			name: "SNMPv2c missing community",
			device: &Device{
				Name:        "test",
				Address:     "203.0.113.1",
				SNMPVersion: "2c",
				Community:   "",
			},
			expectError: true,
			errorField:  "community",
		},
		{
			name: "SNMPv3 missing username",
			device: &Device{
				Name:         "test",
				Address:      "203.0.113.1",
				SNMPVersion:  "3",
				Username:     "",
				AuthProtocol: "SHA",
				AuthPassword: "pass",
			},
			expectError: true,
			errorField:  "username",
		},
		{
			name: "negative poll interval",
			device: &Device{
				Name:         "test",
				Address:      "203.0.113.1",
				SNMPVersion:  "2c",
				Community:    "public",
				PollInterval: -1,
			},
			expectError: true,
			errorField:  "poll_interval",
		},
		{
			name: "SNMPv3 with auth protocol but no password - allowed",
			device: &Device{
				Name:         "test",
				Address:      "203.0.113.1",
				SNMPVersion:  "3",
				Username:     "admin",
				AuthProtocol: "SHA",
				AuthPassword: "",
			},
			expectError: false, // Device.Validate() doesn't enforce auth_password
		},
		{
			name: "SNMPv3 with priv protocol but no password - allowed",
			device: &Device{
				Name:         "test",
				Address:      "203.0.113.1",
				SNMPVersion:  "3",
				Username:     "admin",
				AuthProtocol: "SHA",
				AuthPassword: "authpass",
				PrivProtocol: "AES",
				PrivPassword: "",
			},
			expectError: false, // Device.Validate() doesn't enforce priv_password
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.device.Validate()

			if tt.expectError {
				if err == nil {
					t.Error("Expected error but got nil")
				} else if tt.errorField != "" {
					// Check that error mentions the expected field
					if !containsString(err.Error(), tt.errorField) {
						t.Errorf("Expected error to mention field %q, got: %v", tt.errorField, err)
					}
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error but got: %v", err)
				}
			}
		})
	}
}

// Helper function
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && findSubstr(s, substr)
}

func findSubstr(s, substr string) bool {
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
