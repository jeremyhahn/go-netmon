package poller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/config"
)

func TestNewConfigSyncClient(t *testing.T) {
	tests := []struct {
		name        string
		baseURL     string
		timeout     time.Duration
		wantTimeout time.Duration
	}{
		{
			name:        "with custom timeout",
			baseURL:     "http://localhost:8080",
			timeout:     5 * time.Second,
			wantTimeout: 5 * time.Second,
		},
		{
			name:        "with zero timeout uses default",
			baseURL:     "http://localhost:8080",
			timeout:     0,
			wantTimeout: 30 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewConfigSyncClient(tt.baseURL, tt.timeout)
			if client == nil {
				t.Fatal("expected non-nil client")
			}
			if client.baseURL != tt.baseURL {
				t.Errorf("baseURL = %v, want %v", client.baseURL, tt.baseURL)
			}
			if client.timeout != tt.wantTimeout {
				t.Errorf("timeout = %v, want %v", client.timeout, tt.wantTimeout)
			}
			if client.maxRetries != 5 {
				t.Errorf("maxRetries = %v, want 5", client.maxRetries)
			}
		})
	}
}

func TestSyncConfiguration_Success(t *testing.T) {
	// Create test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/poller/config" {
			t.Errorf("unexpected path: %v", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method: %v", r.Method)
		}

		resp := ConfigResponse{
			Devices: []DeviceConfig{
				{
					Name:         "test-device",
					Address:      "203.0.113.1:161",
					SNMPVersion:  "2c",
					Community:    "public",
					PollInterval: 30,
					Enabled:      true,
					Profile:      "default",
					Modules:      []string{"IF-MIB", "SNMPv2-MIB"},
					Tags: map[string]string{
						"location": "datacenter1",
					},
				},
			},
			MIBModules: []MIBConfig{
				{
					Name:    "IF-MIB",
					Enabled: true,
					OIDs:    []string{"1.3.6.1.2.1.2"},
				},
			},
			Version: 12345,
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewConfigSyncClient(server.URL, 5*time.Second)
	ctx := context.Background()

	result, err := client.SyncConfiguration(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Devices) != 1 {
		t.Errorf("devices count = %d, want 1", len(result.Devices))
	}

	if len(result.MIBModules) != 1 {
		t.Errorf("mib_modules count = %d, want 1", len(result.MIBModules))
	}

	device := result.Devices[0]
	if device.Name != "test-device" {
		t.Errorf("device name = %v, want test-device", device.Name)
	}
	if device.Address != "203.0.113.1:161" {
		t.Errorf("device address = %v, want 203.0.113.1:161", device.Address)
	}
	if device.SNMPVersion != "2c" {
		t.Errorf("device snmp_version = %v, want 2c", device.SNMPVersion)
	}
	if len(device.Modules) != 2 {
		t.Errorf("device modules count = %d, want 2", len(device.Modules))
	}
}

func TestSyncConfiguration_RetryOnError(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			// Fail first 2 attempts
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		// Succeed on 3rd attempt
		resp := ConfigResponse{
			Devices:    []DeviceConfig{},
			MIBModules: []MIBConfig{},
			Version:    12345,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewConfigSyncClient(server.URL, 5*time.Second)
	ctx := context.Background()

	result, err := client.SyncConfiguration(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}

	if result.Version != 12345 {
		t.Errorf("version = %d, want 12345", result.Version)
	}
}

func TestSyncConfiguration_ContextCancelled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Delay to allow context cancellation
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewConfigSyncClient(server.URL, 5*time.Second)
	ctx, cancel := context.WithCancel(context.Background())

	// Cancel context immediately
	cancel()

	_, err := client.SyncConfiguration(ctx)
	if err == nil {
		t.Fatal("expected error for canceled context")
	}

	// Check that the error is or wraps context.Canceled
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want error wrapping context.Canceled", err)
	}
}

func TestSyncConfiguration_MaxRetriesExceeded(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewConfigSyncClient(server.URL, 1*time.Second)
	client.maxRetries = 3 // Reduce retries for faster test
	ctx := context.Background()

	_, err := client.SyncConfiguration(ctx)
	if err == nil {
		t.Fatal("expected error after max retries")
	}

	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
}

func TestPollerConfigResponse_ToConfig(t *testing.T) {
	baseConfig := &config.Config{
		Global: config.GlobalConfig{
			DefaultPollInterval: 60,
			MaxWorkers:          10,
			SNMPTimeout:         5,
			SNMPRetries:         3,
		},
		Prometheus: config.PrometheusConfig{
			RemoteWriteURL: "http://localhost:9091/api/v1/write",
			BatchSize:      1000,
			FlushInterval:  10,
		},
	}

	resp := &ConfigResponse{
		Devices: []DeviceConfig{
			{
				Name:         "router1",
				Address:      "198.51.100.101:161",
				SNMPVersion:  "2c",
				Community:    "public",
				PollInterval: 30,
				Enabled:      true,
				Profile:      "cisco",
				Modules:      []string{"CISCO-IF-MIB"},
				Tags: map[string]string{
					"role": "router",
				},
			},
		},
		MIBModules: []MIBConfig{
			{
				Name:    "CISCO-IF-MIB",
				Enabled: true,
				OIDs:    []string{"1.3.6.1.4.1.9"},
			},
		},
		Version: 12345,
	}

	cfg := resp.ToConfig(baseConfig)

	// Verify base config preserved
	if cfg.Global.DefaultPollInterval != 60 {
		t.Errorf("default_poll_interval = %d, want 60", cfg.Global.DefaultPollInterval)
	}

	// Verify devices converted
	if len(cfg.Devices) != 1 {
		t.Fatalf("devices count = %d, want 1", len(cfg.Devices))
	}

	device := cfg.Devices[0]
	if device.Name != "router1" {
		t.Errorf("device name = %v, want router1", device.Name)
	}
	if device.Address != "198.51.100.101:161" {
		t.Errorf("device address = %v, want 198.51.100.101:161", device.Address)
	}
	if device.Profile != "cisco" {
		t.Errorf("device profile = %v, want cisco", device.Profile)
	}
	if !device.Enabled {
		t.Error("device enabled = false, want true")
	}

	// Verify MIB modules converted
	if len(cfg.MIBModules) != 1 {
		t.Fatalf("mib_modules count = %d, want 1", len(cfg.MIBModules))
	}

	mib := cfg.MIBModules[0]
	if mib.Name != "CISCO-IF-MIB" {
		t.Errorf("mib name = %v, want CISCO-IF-MIB", mib.Name)
	}
	if !mib.Enabled {
		t.Error("mib enabled = false, want true")
	}
}

func TestApplyConfiguration_Success(t *testing.T) {
	// Create test config
	cfg := &config.Config{
		Global: config.GlobalConfig{
			DefaultPollInterval: 60,
			MaxWorkers:          10,
			SNMPTimeout:         5,
			SNMPRetries:         3,
		},
	}

	// Create poller (without MIB loader for this test)
	p := New(cfg, nil)
	ctx := context.Background()

	// Start poller
	if err := p.Start(ctx); err != nil {
		t.Fatalf("failed to start poller: %v", err)
	}
	defer p.Stop()

	// Create sync config. The device points at a closed local UDP port
	// (see unreachableSNMPAddress) so that the worker ApplyConfiguration
	// starts fails its first poll immediately instead of blocking the
	// deferred p.Stop() above on a real network timeout.
	syncConfig := &ConfigResponse{
		Devices: []DeviceConfig{
			{
				Name:         "test-device",
				Address:      unreachableSNMPAddress(t),
				SNMPVersion:  "2c",
				Community:    "public",
				PollInterval: 30,
				Enabled:      true,
				Profile:      "default",
				Modules:      []string{"SNMPv2-MIB"},
			},
		},
		Version: 12345,
	}

	// Apply configuration
	if err := ApplyConfiguration(ctx, p, syncConfig); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify device was added
	status := p.GetWorkerStatus()
	if len(status) != 1 {
		t.Errorf("worker count = %d, want 1", len(status))
	}

	if _, exists := status["test-device"]; !exists {
		t.Error("test-device not found in worker status")
	}
}

func TestApplyConfiguration_SkipsDisabledDevices(t *testing.T) {
	// Create test config
	cfg := &config.Config{
		Global: config.GlobalConfig{
			DefaultPollInterval: 60,
			MaxWorkers:          10,
			SNMPTimeout:         5,
			SNMPRetries:         3,
		},
	}

	// Create poller
	p := New(cfg, nil)
	ctx := context.Background()

	// Start poller
	if err := p.Start(ctx); err != nil {
		t.Fatalf("failed to start poller: %v", err)
	}
	defer p.Stop()

	// Create sync config with disabled device
	syncConfig := &ConfigResponse{
		Devices: []DeviceConfig{
			{
				Name:         "disabled-device",
				Address:      "203.0.113.1:161",
				SNMPVersion:  "2c",
				Community:    "public",
				PollInterval: 30,
				Enabled:      false, // Disabled
				Profile:      "default",
			},
		},
		Version: 12345,
	}

	// Apply configuration
	if err := ApplyConfiguration(ctx, p, syncConfig); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify device was NOT added
	status := p.GetWorkerStatus()
	if len(status) != 0 {
		t.Errorf("worker count = %d, want 0", len(status))
	}
}

func TestApplyConfiguration_InvalidDevice(t *testing.T) {
	// Create test config
	cfg := &config.Config{
		Global: config.GlobalConfig{
			DefaultPollInterval: 60,
			MaxWorkers:          10,
			SNMPTimeout:         5,
			SNMPRetries:         3,
		},
	}

	// Create poller
	p := New(cfg, nil)
	ctx := context.Background()

	// Start poller
	if err := p.Start(ctx); err != nil {
		t.Fatalf("failed to start poller: %v", err)
	}
	defer p.Stop()

	// Create sync config with invalid device (missing name)
	syncConfig := &ConfigResponse{
		Devices: []DeviceConfig{
			{
				Name:         "", // Invalid: empty name
				Address:      "203.0.113.1:161",
				SNMPVersion:  "2c",
				Community:    "public",
				PollInterval: 30,
				Enabled:      true,
				Profile:      "default",
			},
		},
		Version: 12345,
	}

	// Apply configuration - should not error but skip invalid device
	if err := ApplyConfiguration(ctx, p, syncConfig); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify device was NOT added
	status := p.GetWorkerStatus()
	if len(status) != 0 {
		t.Errorf("worker count = %d, want 0 (invalid device should be skipped)", len(status))
	}
}
