package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/poller"
)

func getTestConfig() *config.Config {
	return &config.Config{
		Global: config.GlobalConfig{
			DefaultPollInterval: 60,
			MaxWorkers:          10,
			SNMPTimeout:         10,
			SNMPRetries:         3,
		},
		Prometheus: config.PrometheusConfig{
			RemoteWriteURL: "http://test:9090/api/v1/write",
			BatchSize:      1000,
			FlushInterval:  10,
		},
		API: config.APIConfig{
			ListenAddress: "0.0.0.0:8080",
		},
		Metrics: config.MetricsConfig{
			ListenAddress: "0.0.0.0:9090",
		},
		Devices: []config.Device{},
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
			Output: "stdout",
		},
	}
}

// TestHandleRegisterDevice tests device registration via API
func TestHandleRegisterDevice(t *testing.T) {
	cfg := getTestConfig()
	p := poller.New(cfg, nil)
	handler := NewPollerControlHandler(p, cfg)

	testCases := []struct {
		name           string
		request        RegisterDeviceRequest
		expectedStatus int
		checkResponse  func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name: "Valid SNMPv2c device",
			request: RegisterDeviceRequest{
				Name:         "test-router-01",
				Address:      "198.51.100.252",
				SNMPVersion:  "2c",
				Community:    "public",
				PollInterval: 30,
				Enabled:      true,
				Profile:      "default",
			},
			expectedStatus: http.StatusCreated,
			checkResponse: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp DeviceResponse
				if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
					t.Fatalf("Failed to decode response: %v", err)
				}

				if resp.Name != "test-router-01" {
					t.Errorf("Expected name 'test-router-01', got '%s'", resp.Name)
				}

				if resp.Address != "198.51.100.252" {
					t.Errorf("Expected address '198.51.100.252', got '%s'", resp.Address)
				}

				if resp.SNMPVersion != "2c" {
					t.Errorf("Expected SNMP version '2c', got '%s'", resp.SNMPVersion)
				}

				if resp.PollInterval != 30 {
					t.Errorf("Expected poll interval 30, got %d", resp.PollInterval)
				}

				if resp.Profile != "default" {
					t.Errorf("Expected profile 'default', got '%s'", resp.Profile)
				}
			},
		},
		{
			name: "Invalid device - missing name",
			request: RegisterDeviceRequest{
				Address:     "203.0.113.1",
				SNMPVersion: "2c",
				Community:   "public",
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name: "Invalid device - missing address",
			request: RegisterDeviceRequest{
				Name:        "test-device",
				SNMPVersion: "2c",
				Community:   "public",
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name: "Invalid device - missing community for SNMPv2c",
			request: RegisterDeviceRequest{
				Name:        "test-device",
				Address:     "203.0.113.1",
				SNMPVersion: "2c",
			},
			expectedStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create request
			body, err := json.Marshal(tc.request)
			if err != nil {
				t.Fatalf("Failed to marshal request: %v", err)
			}

			req := httptest.NewRequest(http.MethodPost, "/api/poller/devices", bytes.NewReader(body))
			w := httptest.NewRecorder()

			// Handle request
			handler.HandleRegisterDevice(w, req)

			// Check status code
			if w.Code != tc.expectedStatus {
				t.Errorf("Expected status %d, got %d. Body: %s", tc.expectedStatus, w.Code, w.Body.String())
			}

			// Run additional checks if provided
			if tc.checkResponse != nil && w.Code == http.StatusCreated {
				tc.checkResponse(t, w)
			}

			// Clean up - remove device if it was created
			if w.Code == http.StatusCreated && tc.request.Name != "" {
				_ = p.RemoveDevice(tc.request.Name)
			}
		})
	}
}

// TestHandleListDevices_ReturnsCompleteDeviceDetails is the critical test for the bug fix
func TestHandleListDevices_ReturnsCompleteDeviceDetails(t *testing.T) {
	cfg := getTestConfig()
	p := poller.New(cfg, nil)
	handler := NewPollerControlHandler(p, cfg)
	ctx := context.Background()

	// Add a test device with all fields populated
	testDevice := &config.Device{
		Name:         "core-01",
		Address:      "198.51.100.252",
		SNMPVersion:  "2c",
		Community:    "public",
		PollInterval: 30,
		Enabled:      true,
		Profile:      "default",
		Tags: map[string]string{
			"site": "datacenter-1",
			"role": "core-router",
		},
		Modules: []string{"IF-MIB", "SNMPv2-MIB"},
	}

	err := p.AddDevice(ctx, testDevice)
	if err != nil {
		t.Fatalf("Failed to add device: %v", err)
	}

	// Create GET request to list devices
	req := httptest.NewRequest(http.MethodGet, "/api/poller/devices", nil)
	w := httptest.NewRecorder()

	// Handle request
	handler.HandleListDevices(w, req)

	// Check status code
	if w.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d. Body: %s", w.Code, w.Body.String())
	}

	// Decode response
	var resp ListDevicesResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Verify we have one device
	if resp.Count != 1 {
		t.Fatalf("Expected 1 device, got %d", resp.Count)
	}

	if len(resp.Devices) != 1 {
		t.Fatalf("Expected 1 device in list, got %d", len(resp.Devices))
	}

	// Get the device
	device := resp.Devices[0]

	// Critical checks - verify all fields are populated (bug fix verification)
	if device.Name != "core-01" {
		t.Errorf("Expected name 'core-01', got '%s'", device.Name)
	}

	if device.Address != "198.51.100.252" {
		t.Errorf("BUG: Expected address '198.51.100.252', got '%s' (address field was empty before fix)", device.Address)
	}

	if device.SNMPVersion != "2c" {
		t.Errorf("BUG: Expected SNMP version '2c', got '%s' (snmp_version field was empty before fix)", device.SNMPVersion)
	}

	if device.PollInterval != 30 {
		t.Errorf("BUG: Expected poll interval 30, got %d (poll_interval was 0 before fix)", device.PollInterval)
	}

	if device.Profile != "default" {
		t.Errorf("Expected profile 'default', got '%s'", device.Profile)
	}

	// Verify tags are preserved
	if len(device.Tags) != 2 {
		t.Errorf("Expected 2 tags, got %d", len(device.Tags))
	}

	if device.Tags["site"] != "datacenter-1" {
		t.Errorf("Expected tag 'site' = 'datacenter-1', got '%s'", device.Tags["site"])
	}

	// Verify modules are preserved
	if len(device.Modules) != 2 {
		t.Errorf("Expected 2 modules, got %d", len(device.Modules))
	}

	// Verify enabled status
	if !device.Enabled {
		t.Error("Expected device to be enabled")
	}
}

// TestHandleListDevices_MultipleDevices tests listing multiple devices
func TestHandleListDevices_MultipleDevices(t *testing.T) {
	cfg := getTestConfig()
	p := poller.New(cfg, nil)
	handler := NewPollerControlHandler(p, cfg)
	ctx := context.Background()

	// Add multiple devices with different configurations
	devices := []*config.Device{
		{
			Name:         "router-01",
			Address:      "198.51.100.101",
			SNMPVersion:  "2c",
			Community:    "public",
			PollInterval: 60,
			Enabled:      true,
			Profile:      "cisco",
		},
		{
			Name:         "switch-01",
			Address:      "198.51.100.102",
			SNMPVersion:  "2c",
			Community:    "public",
			PollInterval: 45,
			Enabled:      true,
			Profile:      "interfaces",
		},
		{
			Name:         "server-01",
			Address:      "198.51.100.103",
			SNMPVersion:  "3",
			Username:     "admin",
			AuthProtocol: "SHA",
			AuthPassword: "authpass",
			PrivProtocol: "AES",
			PrivPassword: "privpass",
			PollInterval: 120,
			Enabled:      true,
			Profile:      "host",
		},
	}

	// Add all devices
	for _, device := range devices {
		err := p.AddDevice(ctx, device)
		if err != nil {
			t.Fatalf("Failed to add device %s: %v", device.Name, err)
		}
	}

	// Create GET request to list devices
	req := httptest.NewRequest(http.MethodGet, "/api/poller/devices", nil)
	w := httptest.NewRecorder()

	// Handle request
	handler.HandleListDevices(w, req)

	// Check status code
	if w.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", w.Code)
	}

	// Decode response
	var resp ListDevicesResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Verify count
	if resp.Count != 3 {
		t.Errorf("Expected 3 devices, got %d", resp.Count)
	}

	if len(resp.Devices) != 3 {
		t.Errorf("Expected 3 devices in list, got %d", len(resp.Devices))
	}

	// Verify all devices have complete information
	deviceMap := make(map[string]DeviceResponse)
	for _, device := range resp.Devices {
		deviceMap[device.Name] = device

		// Verify critical fields are not empty
		if device.Address == "" {
			t.Errorf("Device %s has empty address", device.Name)
		}
		if device.SNMPVersion == "" {
			t.Errorf("Device %s has empty SNMP version", device.Name)
		}
		if device.PollInterval == 0 {
			t.Errorf("Device %s has zero poll interval", device.Name)
		}
	}

	// Verify specific device details
	if router, ok := deviceMap["router-01"]; ok {
		if router.Address != "198.51.100.101" {
			t.Errorf("router-01: expected address '198.51.100.101', got '%s'", router.Address)
		}
		if router.Profile != "cisco" {
			t.Errorf("router-01: expected profile 'cisco', got '%s'", router.Profile)
		}
	} else {
		t.Error("router-01 not found in response")
	}
}

// TestHandleListDevices_Empty tests listing when no devices are registered
func TestHandleListDevices_Empty(t *testing.T) {
	cfg := getTestConfig()
	p := poller.New(cfg, nil)
	handler := NewPollerControlHandler(p, cfg)

	// Create GET request to list devices
	req := httptest.NewRequest(http.MethodGet, "/api/poller/devices", nil)
	w := httptest.NewRecorder()

	// Handle request
	handler.HandleListDevices(w, req)

	// Check status code
	if w.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", w.Code)
	}

	// Decode response
	var resp ListDevicesResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Verify empty list
	if resp.Count != 0 {
		t.Errorf("Expected 0 devices, got %d", resp.Count)
	}

	if len(resp.Devices) != 0 {
		t.Errorf("Expected 0 devices in list, got %d", len(resp.Devices))
	}
}

// TestHandleRemoveDevice tests device removal
func TestHandleRemoveDevice(t *testing.T) {
	cfg := getTestConfig()
	p := poller.New(cfg, nil)
	handler := NewPollerControlHandler(p, cfg)
	ctx := context.Background()

	// Add a device
	device := &config.Device{
		Name:        "remove-test",
		Address:     "203.0.113.100",
		SNMPVersion: "2c",
		Community:   "public",
		Enabled:     true,
	}

	err := p.AddDevice(ctx, device)
	if err != nil {
		t.Fatalf("Failed to add device: %v", err)
	}

	// Verify device exists
	status := p.GetWorkerStatus()
	if len(status) != 1 {
		t.Errorf("Expected 1 device, got %d", len(status))
	}

	// Create DELETE request
	req := httptest.NewRequest(http.MethodDelete, "/api/poller/devices/remove-test", nil)
	w := httptest.NewRecorder()

	// Handle request
	handler.HandleRemoveDevice(w, req)

	// Check status code
	if w.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d. Body: %s", w.Code, w.Body.String())
	}

	// Verify device is removed
	status = p.GetWorkerStatus()
	if len(status) != 0 {
		t.Errorf("Expected 0 devices after removal, got %d", len(status))
	}
}

// TestHandleRemoveDevice_NotFound tests removing non-existent device
func TestHandleRemoveDevice_NotFound(t *testing.T) {
	cfg := getTestConfig()
	p := poller.New(cfg, nil)
	handler := NewPollerControlHandler(p, cfg)

	// Create DELETE request for non-existent device
	req := httptest.NewRequest(http.MethodDelete, "/api/poller/devices/nonexistent", nil)
	w := httptest.NewRecorder()

	// Handle request
	handler.HandleRemoveDevice(w, req)

	// Check status code
	if w.Code != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", w.Code)
	}
}

// TestDeviceResponseFields verifies DeviceResponse structure
func TestDeviceResponseFields(t *testing.T) {
	device := DeviceResponse{
		Name:         "test-device",
		Address:      "203.0.113.1",
		SNMPVersion:  "2c",
		PollInterval: 30,
		Enabled:      true,
		Profile:      "default",
		Modules:      []string{"IF-MIB"},
		Tags: map[string]string{
			"site": "datacenter-1",
		},
	}

	// Verify all fields are accessible
	if device.Name != "test-device" {
		t.Errorf("Name field mismatch")
	}
	if device.Address != "203.0.113.1" {
		t.Errorf("Address field mismatch")
	}
	if device.SNMPVersion != "2c" {
		t.Errorf("SNMPVersion field mismatch")
	}
	if device.PollInterval != 30 {
		t.Errorf("PollInterval field mismatch")
	}
	if !device.Enabled {
		t.Errorf("Enabled field mismatch")
	}
	if device.Profile != "default" {
		t.Errorf("Profile field mismatch")
	}
	if len(device.Modules) != 1 {
		t.Errorf("Modules field mismatch")
	}
	if len(device.Tags) != 1 {
		t.Errorf("Tags field mismatch")
	}
}
