package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadFromFile_ValidConfig(t *testing.T) {
	// Create a temporary config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	validConfig := `
global:
  default_poll_interval: 60
  max_workers: 10
  snmp_timeout: 10
  snmp_retries: 3

prometheus:
  remote_write_url: "http://prometheus:9090/api/v1/write"
  batch_size: 1000
  flush_interval: 10

api:
  listen_address: "0.0.0.0:8080"
  enable_cors: true
  jwt_secret: "test-secret"

metrics:
  listen_address: "0.0.0.0:9090"

devices:
  - name: "test-device"
    address: "203.0.113.1"
    snmp_version: "2c"
    community: "public"
    poll_interval: 60
    enabled: true

logging:
  level: "info"
  format: "json"
  output: "stdout"
`

	if err := os.WriteFile(configPath, []byte(validConfig), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	config, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	// Verify global config
	if config.Global.DefaultPollInterval != 60 {
		t.Errorf("Expected DefaultPollInterval 60, got %d", config.Global.DefaultPollInterval)
	}
	if config.Global.MaxWorkers != 10 {
		t.Errorf("Expected MaxWorkers 10, got %d", config.Global.MaxWorkers)
	}

	// Verify Prometheus config
	if config.Prometheus.RemoteWriteURL != "http://prometheus:9090/api/v1/write" {
		t.Errorf("Expected RemoteWriteURL, got %s", config.Prometheus.RemoteWriteURL)
	}

	// Verify devices
	if len(config.Devices) != 1 {
		t.Fatalf("Expected 1 device, got %d", len(config.Devices))
	}

	device := config.Devices[0]
	if device.Name != "test-device" {
		t.Errorf("Expected device name 'test-device', got %s", device.Name)
	}
	if device.Address != "203.0.113.1" {
		t.Errorf("Expected device address '203.0.113.1', got %s", device.Address)
	}
}

func TestLoadFromFile_WithDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	minimalConfig := `
prometheus:
  remote_write_url: "http://prometheus:9090/api/v1/write"

api:
  listen_address: "0.0.0.0:8080"

metrics:
  listen_address: "0.0.0.0:9090"

devices:
  - name: "test-device"
    address: "203.0.113.1"
    snmp_version: "2c"
    community: "public"
    enabled: true
`

	if err := os.WriteFile(configPath, []byte(minimalConfig), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	config, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	// Verify defaults were set
	if config.Global.DefaultPollInterval != 60 {
		t.Errorf("Expected default DefaultPollInterval 60, got %d", config.Global.DefaultPollInterval)
	}
	if config.Global.MaxWorkers != 10 {
		t.Errorf("Expected default MaxWorkers 10, got %d", config.Global.MaxWorkers)
	}
	if config.Logging.Level != "info" {
		t.Errorf("Expected default log level 'info', got %s", config.Logging.Level)
	}
	if config.Logging.Format != "json" {
		t.Errorf("Expected default log format 'json', got %s", config.Logging.Format)
	}
}

func TestLoadFromFile_FileNotFound(t *testing.T) {
	_, err := LoadFromFile("/nonexistent/config.yaml")
	if err == nil {
		t.Fatal("Expected error for nonexistent file, got nil")
	}
}

func TestLoadFromFile_InvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	invalidYAML := `
invalid: yaml: content:
  - this is not valid
`

	if err := os.WriteFile(configPath, []byte(invalidYAML), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	_, err := LoadFromFile(configPath)
	if err == nil {
		t.Fatal("Expected error for invalid YAML, got nil")
	}
}

func TestConfig_Validate_MissingRequiredFields(t *testing.T) {
	tests := []struct {
		name        string
		config      Config
		expectError bool
		errorField  string
	}{
		{
			name: "missing prometheus URL",
			config: Config{
				Global: GlobalConfig{DefaultPollInterval: 60, MaxWorkers: 10, SNMPTimeout: 10},
				Prometheus: PrometheusConfig{
					RemoteWriteURL: "",
					BatchSize:      1000,
				},
				API:     APIConfig{ListenAddress: "0.0.0.0:8080"},
				Metrics: MetricsConfig{ListenAddress: "0.0.0.0:9090"},
				Devices: []Device{{Name: "test", Address: "1.1.1.1", SNMPVersion: "2c", Community: "public", Enabled: true}},
				Logging: LoggingConfig{Level: "info", Format: "json", Output: "stdout"},
			},
			expectError: true,
			errorField:  "prometheus.remote_write_url",
		},
		{
			// An empty device list is valid: devices can be registered
			// dynamically after startup via the poller control API or
			// WebUI config sync (see docs/CONFIG_SYNC.md), so Validate
			// must not reject it.
			name: "no devices configured",
			config: Config{
				Global:     GlobalConfig{DefaultPollInterval: 60, MaxWorkers: 10, SNMPTimeout: 10},
				Prometheus: PrometheusConfig{RemoteWriteURL: "http://test", BatchSize: 1000, FlushInterval: 10},
				API:        APIConfig{ListenAddress: "0.0.0.0:8080"},
				Metrics:    MetricsConfig{ListenAddress: "0.0.0.0:9090"},
				Devices:    []Device{},
				Logging:    LoggingConfig{Level: "info", Format: "json", Output: "stdout"},
			},
			expectError: false,
		},
		{
			name: "invalid poll interval",
			config: Config{
				Global:     GlobalConfig{DefaultPollInterval: -1, MaxWorkers: 10, SNMPTimeout: 10},
				Prometheus: PrometheusConfig{RemoteWriteURL: "http://test", BatchSize: 1000, FlushInterval: 10},
				API:        APIConfig{ListenAddress: "0.0.0.0:8080"},
				Metrics:    MetricsConfig{ListenAddress: "0.0.0.0:9090"},
				Devices:    []Device{{Name: "test", Address: "1.1.1.1", SNMPVersion: "2c", Community: "public", Enabled: true}},
				Logging:    LoggingConfig{Level: "info", Format: "json", Output: "stdout"},
			},
			expectError: true,
			errorField:  "global.default_poll_interval",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.expectError && err == nil {
				t.Errorf("Expected validation error, got nil")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Expected no error, got: %v", err)
			}
			if tt.expectError && err != nil {
				if verr, ok := err.(*ValidationError); ok {
					if verr.Field != tt.errorField {
						t.Errorf("Expected error field %s, got %s", tt.errorField, verr.Field)
					}
				}
			}
		})
	}
}

func TestDevice_Validate(t *testing.T) {
	tests := []struct {
		name        string
		device      Device
		expectError bool
	}{
		{
			name: "valid SNMPv2c device",
			device: Device{
				Name:        "test",
				Address:     "203.0.113.1",
				SNMPVersion: "2c",
				Community:   "public",
			},
			expectError: false,
		},
		{
			name: "valid SNMPv3 device",
			device: Device{
				Name:        "test",
				Address:     "203.0.113.1",
				SNMPVersion: "3",
				Username:    "admin",
			},
			expectError: false,
		},
		{
			name: "missing name",
			device: Device{
				Address:     "203.0.113.1",
				SNMPVersion: "2c",
				Community:   "public",
			},
			expectError: true,
		},
		{
			name: "missing address",
			device: Device{
				Name:        "test",
				SNMPVersion: "2c",
				Community:   "public",
			},
			expectError: true,
		},
		{
			name: "invalid SNMP version",
			device: Device{
				Name:        "test",
				Address:     "203.0.113.1",
				SNMPVersion: "1",
				Community:   "public",
			},
			expectError: true,
		},
		{
			name: "SNMPv2c missing community",
			device: Device{
				Name:        "test",
				Address:     "203.0.113.1",
				SNMPVersion: "2c",
			},
			expectError: true,
		},
		{
			name: "SNMPv3 missing username",
			device: Device{
				Name:        "test",
				Address:     "203.0.113.1",
				SNMPVersion: "3",
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.device.Validate()
			if tt.expectError && err == nil {
				t.Errorf("Expected validation error, got nil")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Expected no error, got: %v", err)
			}
		})
	}
}

func TestDevice_GetPollInterval(t *testing.T) {
	tests := []struct {
		name            string
		device          Device
		defaultInterval int
		expected        time.Duration
	}{
		{
			name:            "use device interval",
			device:          Device{PollInterval: 30},
			defaultInterval: 60,
			expected:        30 * time.Second,
		},
		{
			name:            "use default interval",
			device:          Device{PollInterval: 0},
			defaultInterval: 60,
			expected:        60 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.device.GetPollInterval(tt.defaultInterval)
			if result != tt.expected {
				t.Errorf("Expected interval %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestConfig_GetEnabledDevices(t *testing.T) {
	config := Config{
		Devices: []Device{
			{Name: "device1", Address: "1.1.1.1", SNMPVersion: "2c", Community: "public", Enabled: true},
			{Name: "device2", Address: "2.2.2.2", SNMPVersion: "2c", Community: "public", Enabled: false},
			{Name: "device3", Address: "3.3.3.3", SNMPVersion: "2c", Community: "public", Enabled: true},
		},
	}

	enabled := config.GetEnabledDevices()
	if len(enabled) != 2 {
		t.Errorf("Expected 2 enabled devices, got %d", len(enabled))
	}

	for _, device := range enabled {
		if !device.Enabled {
			t.Errorf("Device %s should be enabled", device.Name)
		}
	}
}

func TestConfig_GetDeviceByName(t *testing.T) {
	config := Config{
		Devices: []Device{
			{Name: "device1", Address: "1.1.1.1", SNMPVersion: "2c", Community: "public", Enabled: true},
			{Name: "device2", Address: "2.2.2.2", SNMPVersion: "2c", Community: "public", Enabled: true},
		},
	}

	// Test existing device
	device, found := config.GetDeviceByName("device1")
	if !found {
		t.Error("Expected to find device1")
	}
	if device.Address != "1.1.1.1" {
		t.Errorf("Expected address 1.1.1.1, got %s", device.Address)
	}

	// Test non-existent device
	_, found = config.GetDeviceByName("nonexistent")
	if found {
		t.Error("Expected not to find nonexistent device")
	}
}

func TestConfig_Validate_DuplicateDeviceNames(t *testing.T) {
	config := Config{
		Global:     GlobalConfig{DefaultPollInterval: 60, MaxWorkers: 10, SNMPTimeout: 10},
		Prometheus: PrometheusConfig{RemoteWriteURL: "http://test", BatchSize: 1000, FlushInterval: 10},
		API:        APIConfig{ListenAddress: "0.0.0.0:8080"},
		Metrics:    MetricsConfig{ListenAddress: "0.0.0.0:9090"},
		Devices: []Device{
			{Name: "device1", Address: "1.1.1.1", SNMPVersion: "2c", Community: "public", Enabled: true},
			{Name: "device1", Address: "2.2.2.2", SNMPVersion: "2c", Community: "public", Enabled: true},
		},
		Logging: LoggingConfig{Level: "info", Format: "json", Output: "stdout"},
	}

	err := config.Validate()
	if err == nil {
		t.Error("Expected validation error for duplicate device names")
	}
}
