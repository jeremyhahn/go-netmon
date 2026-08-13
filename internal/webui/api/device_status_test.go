package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/webui/client"
	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// mockPollerClient is a mock implementation of PollerClient for testing
type mockPollerClient struct {
	getDeviceStatusFunc func(ctx context.Context, deviceName string) (*client.DeviceStatus, error)
	listDevicesFunc     func(ctx context.Context) ([]client.DeviceInfo, error)
}

func (m *mockPollerClient) RegisterDevice(
	ctx context.Context, device *storage.Device,
	mibNames []string, filters map[string]config.ModuleFilter,
) error {
	return nil
}

func (m *mockPollerClient) UpdateDevice(
	ctx context.Context, device *storage.Device,
	mibNames []string, filters map[string]config.ModuleFilter,
) error {
	return nil
}

func (m *mockPollerClient) RemoveDevice(ctx context.Context, deviceName string) error {
	return nil
}

func (m *mockPollerClient) GetDeviceStatus(ctx context.Context, deviceName string) (*client.DeviceStatus, error) {
	if m.getDeviceStatusFunc != nil {
		return m.getDeviceStatusFunc(ctx, deviceName)
	}
	return nil, errors.New("not implemented")
}

func (m *mockPollerClient) ListDevices(ctx context.Context) ([]client.DeviceInfo, error) {
	if m.listDevicesFunc != nil {
		return m.listDevicesFunc(ctx)
	}
	return nil, nil
}

func TestHandleGetDeviceStatusByName(t *testing.T) {
	tests := []struct {
		name           string
		deviceName     string
		mockStatus     *client.DeviceStatus
		mockError      error
		expectedStatus int
		checkResponse  bool
	}{
		{
			name:       "successful status retrieval by name",
			deviceName: "router-01",
			mockStatus: &client.DeviceStatus{
				Name:             "router-01",
				Connected:        true,
				LastPollSuccess:  true,
				MetricsCollected: 42,
				PollInterval:     "30s",
			},
			mockError:      nil,
			expectedStatus: http.StatusOK,
			checkResponse:  true,
		},
		{
			name:           "device not found by name",
			deviceName:     "nonexistent",
			mockStatus:     nil,
			mockError:      errors.New("device not found"),
			expectedStatus: http.StatusNotFound,
			checkResponse:  false,
		},
		{
			name:           "poller service error",
			deviceName:     "test-device",
			mockStatus:     nil,
			mockError:      errors.New("connection timeout"),
			expectedStatus: http.StatusServiceUnavailable,
			checkResponse:  false,
		},
		{
			name:           "empty device name",
			deviceName:     "",
			mockStatus:     nil,
			mockError:      nil,
			expectedStatus: http.StatusBadRequest,
			checkResponse:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &mockPollerClient{
				getDeviceStatusFunc: func(ctx context.Context, deviceName string) (*client.DeviceStatus, error) {
					if tt.deviceName != "" && deviceName != tt.deviceName {
						t.Errorf("GetDeviceStatus called with wrong device name: got %s, want %s", deviceName, tt.deviceName)
					}
					return tt.mockStatus, tt.mockError
				},
			}

			server := &Server{
				pollerClient: mockClient,
			}

			req := httptest.NewRequest(http.MethodGet, "/api/devices/status/"+tt.deviceName, nil)
			req = mux.SetURLVars(req, map[string]string{"name": tt.deviceName})

			w := httptest.NewRecorder()

			server.handleGetDeviceStatusByName(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("Status code = %v, want %v", w.Code, tt.expectedStatus)
			}

			// Check response body for successful requests
			if tt.checkResponse && w.Code == http.StatusOK {
				var status client.DeviceStatus
				if err := json.NewDecoder(w.Body).Decode(&status); err != nil {
					t.Fatalf("Failed to decode response: %v", err)
				}

				if status.Name != tt.mockStatus.Name {
					t.Errorf("Status.Name = %v, want %v", status.Name, tt.mockStatus.Name)
				}
				if status.Connected != tt.mockStatus.Connected {
					t.Errorf("Status.Connected = %v, want %v", status.Connected, tt.mockStatus.Connected)
				}
				if status.MetricsCollected != tt.mockStatus.MetricsCollected {
					t.Errorf("Status.MetricsCollected = %v, want %v", status.MetricsCollected, tt.mockStatus.MetricsCollected)
				}
			}
		})
	}
}

func TestHandleGetDeviceStatusNilClient(t *testing.T) {
	server := &Server{
		pollerClient: nil, // No client configured
	}

	req := httptest.NewRequest(http.MethodGet, "/api/devices/status/test", nil)
	req = mux.SetURLVars(req, map[string]string{"name": "test"})

	w := httptest.NewRecorder()
	server.handleGetDeviceStatusByName(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("Expected status %d for nil client, got %d", http.StatusServiceUnavailable, w.Code)
	}
}

func TestDeviceStatusResponseFormat(t *testing.T) {
	now := time.Now()
	mockClient := &mockPollerClient{
		getDeviceStatusFunc: func(ctx context.Context, deviceName string) (*client.DeviceStatus, error) {
			return &client.DeviceStatus{
				Name:             deviceName,
				Connected:        true,
				LastPollSuccess:  true,
				LastPollTime:     now,
				MetricsCollected: 100,
				PollInterval:     "30s",
				Tags: map[string]string{
					"environment": "production",
					"location":    "datacenter-1",
				},
			}, nil
		},
	}

	server := &Server{
		pollerClient: mockClient,
	}

	req := httptest.NewRequest(http.MethodGet, "/api/devices/status/test-device", nil)
	req = mux.SetURLVars(req, map[string]string{"name": "test-device"})

	w := httptest.NewRecorder()
	server.handleGetDeviceStatusByName(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", w.Code)
	}

	var status client.DeviceStatus
	if err := json.NewDecoder(w.Body).Decode(&status); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Verify all fields
	if status.Name != "test-device" {
		t.Errorf("Name = %v, want test-device", status.Name)
	}
	if !status.Connected {
		t.Error("Expected Connected to be true")
	}
	if !status.LastPollSuccess {
		t.Error("Expected LastPollSuccess to be true")
	}
	if status.MetricsCollected != 100 {
		t.Errorf("MetricsCollected = %d, want 100", status.MetricsCollected)
	}
	if status.PollInterval != "30s" {
		t.Errorf("PollInterval = %s, want 30s", status.PollInterval)
	}
	if len(status.Tags) != 2 {
		t.Errorf("Tags length = %d, want 2", len(status.Tags))
	}
	if status.Tags["environment"] != "production" {
		t.Errorf("Tags[environment] = %s, want production", status.Tags["environment"])
	}
}
