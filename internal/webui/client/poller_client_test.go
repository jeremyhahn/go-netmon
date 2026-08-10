package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

func TestNewPollerClient(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		timeout time.Duration
	}{
		{
			name:    "with default timeout",
			baseURL: "http://localhost:8080",
			timeout: 0,
		},
		{
			name:    "with custom timeout",
			baseURL: "http://localhost:8080",
			timeout: 5 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewPollerClient(tt.baseURL, tt.timeout)
			if client == nil {
				t.Fatal("NewPollerClient returned nil")
			}
			if client.baseURL != tt.baseURL {
				t.Errorf("baseURL = %v, want %v", client.baseURL, tt.baseURL)
			}
			if tt.timeout > 0 && client.timeout != tt.timeout {
				t.Errorf("timeout = %v, want %v", client.timeout, tt.timeout)
			}
			if tt.timeout == 0 && client.timeout != 10*time.Second {
				t.Errorf("timeout = %v, want default %v", client.timeout, 10*time.Second)
			}
		})
	}
}

func TestPollerClient_RegisterDevice(t *testing.T) {
	tests := []struct {
		name       string
		device     *storage.Device
		mibNames   []string
		statusCode int
		wantErr    bool
	}{
		{
			name: "successful registration",
			device: &storage.Device{
				Name:        "test-device",
				Address:     "203.0.113.1",
				SNMPVersion: "2c",
				Community:   "public",
				Port:        161,
			},
			mibNames:   []string{"IF-MIB", "SNMPv2-MIB"},
			statusCode: http.StatusCreated,
			wantErr:    false,
		},
		{
			name: "server error",
			device: &storage.Device{
				Name:        "test-device",
				Address:     "203.0.113.1",
				SNMPVersion: "2c",
				Community:   "public",
			},
			mibNames:   nil,
			statusCode: http.StatusInternalServerError,
			wantErr:    true,
		},
		{
			name: "bad request",
			device: &storage.Device{
				Name: "test-device",
			},
			mibNames:   nil,
			statusCode: http.StatusBadRequest,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("Method = %v, want %v", r.Method, http.MethodPost)
				}
				if r.URL.Path != "/api/poller/devices" {
					t.Errorf("Path = %v, want %v", r.URL.Path, "/api/poller/devices")
				}

				var req PollerDeviceRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("Failed to decode request: %v", err)
				}

				if req.Name != tt.device.Name {
					t.Errorf("Name = %v, want %v", req.Name, tt.device.Name)
				}

				w.WriteHeader(tt.statusCode)
				json.NewEncoder(w).Encode(map[string]string{
					"message": "device registered",
				})
			}))
			defer server.Close()

			client := NewPollerClient(server.URL, 5*time.Second)
			err := client.RegisterDevice(context.Background(), tt.device, tt.mibNames)

			if (err != nil) != tt.wantErr {
				t.Errorf("RegisterDevice() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestPollerClient_UpdateDevice(t *testing.T) {
	tests := []struct {
		name       string
		device     *storage.Device
		mibNames   []string
		statusCode int
		wantErr    bool
	}{
		{
			name: "successful update",
			device: &storage.Device{
				Name:        "test-device",
				Address:     "203.0.113.2",
				SNMPVersion: "2c",
				Community:   "private",
			},
			mibNames:   []string{"IF-MIB"},
			statusCode: http.StatusOK,
			wantErr:    false,
		},
		{
			name: "device not found",
			device: &storage.Device{
				Name: "nonexistent",
			},
			mibNames:   nil,
			statusCode: http.StatusNotFound,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPut {
					t.Errorf("Method = %v, want %v", r.Method, http.MethodPut)
				}
				expectedPath := "/api/poller/devices/" + tt.device.Name
				if r.URL.Path != expectedPath {
					t.Errorf("Path = %v, want %v", r.URL.Path, expectedPath)
				}

				w.WriteHeader(tt.statusCode)
				json.NewEncoder(w).Encode(map[string]string{
					"message": "device updated",
				})
			}))
			defer server.Close()

			client := NewPollerClient(server.URL, 5*time.Second)
			err := client.UpdateDevice(context.Background(), tt.device, tt.mibNames)

			if (err != nil) != tt.wantErr {
				t.Errorf("UpdateDevice() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestPollerClient_RemoveDevice(t *testing.T) {
	tests := []struct {
		name       string
		deviceName string
		statusCode int
		wantErr    bool
	}{
		{
			name:       "successful removal",
			deviceName: "test-device",
			statusCode: http.StatusOK,
			wantErr:    false,
		},
		{
			name:       "device not found",
			deviceName: "nonexistent",
			statusCode: http.StatusNotFound,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete {
					t.Errorf("Method = %v, want %v", r.Method, http.MethodDelete)
				}
				expectedPath := "/api/poller/devices/" + tt.deviceName
				if r.URL.Path != expectedPath {
					t.Errorf("Path = %v, want %v", r.URL.Path, expectedPath)
				}

				w.WriteHeader(tt.statusCode)
				json.NewEncoder(w).Encode(map[string]string{
					"message": "device removed",
				})
			}))
			defer server.Close()

			client := NewPollerClient(server.URL, 5*time.Second)
			err := client.RemoveDevice(context.Background(), tt.deviceName)

			if (err != nil) != tt.wantErr {
				t.Errorf("RemoveDevice() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestPollerClient_GetDeviceStatus(t *testing.T) {
	tests := []struct {
		name       string
		deviceName string
		statusCode int
		response   *DeviceStatus
		wantErr    bool
	}{
		{
			name:       "successful status retrieval",
			deviceName: "test-device",
			statusCode: http.StatusOK,
			response: &DeviceStatus{
				Name:            "test-device",
				Connected:       true,
				LastPollSuccess: true,
				LastPollTime:    time.Now(),
				PollInterval:    "30s",
			},
			wantErr: false,
		},
		{
			name:       "device not found",
			deviceName: "nonexistent",
			statusCode: http.StatusNotFound,
			response:   nil,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("Method = %v, want %v", r.Method, http.MethodGet)
				}
				expectedPath := "/api/poller/devices/" + tt.deviceName + "/status"
				if r.URL.Path != expectedPath {
					t.Errorf("Path = %v, want %v", r.URL.Path, expectedPath)
				}

				w.WriteHeader(tt.statusCode)
				if tt.response != nil {
					json.NewEncoder(w).Encode(tt.response)
				}
			}))
			defer server.Close()

			client := NewPollerClient(server.URL, 5*time.Second)
			status, err := client.GetDeviceStatus(context.Background(), tt.deviceName)

			if (err != nil) != tt.wantErr {
				t.Errorf("GetDeviceStatus() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && status == nil {
				t.Error("GetDeviceStatus() returned nil status")
			}

			if !tt.wantErr && status.Name != tt.deviceName {
				t.Errorf("Status.Name = %v, want %v", status.Name, tt.deviceName)
			}
		})
	}
}

func TestPollerClient_ListDevices(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		devices    []DeviceInfo
		wantErr    bool
	}{
		{
			name:       "successful list",
			statusCode: http.StatusOK,
			devices: []DeviceInfo{
				{Name: "device1", Enabled: true},
				{Name: "device2", Enabled: true},
			},
			wantErr: false,
		},
		{
			name:       "empty list",
			statusCode: http.StatusOK,
			devices:    []DeviceInfo{},
			wantErr:    false,
		},
		{
			name:       "server error",
			statusCode: http.StatusInternalServerError,
			devices:    nil,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("Method = %v, want %v", r.Method, http.MethodGet)
				}
				if r.URL.Path != "/api/poller/devices" {
					t.Errorf("Path = %v, want %v", r.URL.Path, "/api/poller/devices")
				}

				w.WriteHeader(tt.statusCode)
				if tt.devices != nil {
					response := ListDevicesResponse{
						Devices: tt.devices,
						Count:   len(tt.devices),
					}
					json.NewEncoder(w).Encode(response)
				}
			}))
			defer server.Close()

			client := NewPollerClient(server.URL, 5*time.Second)
			devices, err := client.ListDevices(context.Background())

			if (err != nil) != tt.wantErr {
				t.Errorf("ListDevices() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && len(devices) != len(tt.devices) {
				t.Errorf("ListDevices() count = %v, want %v", len(devices), len(tt.devices))
			}
		})
	}
}

func TestPollerClient_RetryLogic(t *testing.T) {
	attemptCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attemptCount++
		if attemptCount < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"message": "success"})
	}))
	defer server.Close()

	client := NewPollerClient(server.URL, 5*time.Second)
	device := &storage.Device{
		Name:        "test-device",
		Address:     "203.0.113.1",
		SNMPVersion: "2c",
		Community:   "public",
	}

	err := client.RegisterDevice(context.Background(), device, nil)
	if err != nil {
		t.Errorf("RegisterDevice() should succeed after retries, got error: %v", err)
	}

	if attemptCount != 3 {
		t.Errorf("Expected 3 attempts, got %d", attemptCount)
	}
}

func TestPollerClient_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewPollerClient(server.URL, 5*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	device := &storage.Device{
		Name:        "test-device",
		Address:     "203.0.113.1",
		SNMPVersion: "2c",
		Community:   "public",
	}

	err := client.RegisterDevice(ctx, device, nil)
	if err == nil {
		t.Error("RegisterDevice() should fail with canceled context")
	}
}

func TestPollerClient_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewPollerClient(server.URL, 50*time.Millisecond)
	device := &storage.Device{
		Name:        "test-device",
		Address:     "203.0.113.1",
		SNMPVersion: "2c",
		Community:   "public",
	}

	err := client.RegisterDevice(context.Background(), device, nil)
	if err == nil {
		t.Error("RegisterDevice() should fail with timeout")
	}
}
