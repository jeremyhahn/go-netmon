package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/mib"
	mibStorage "github.com/jeremyhahn/go-netmon/internal/mib/storage"
	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// testMIBLoader implements the MIBLoader interface for testing with storage support
type testMIBLoader struct {
	mibStorage *mibStorage.Storage
}

func (m *testMIBLoader) LoadModule(moduleName string) error {
	return nil
}

func (m *testMIBLoader) GetParser() interface{} {
	return nil
}

func (m *testMIBLoader) GetStorage() interface{} {
	return m.mibStorage
}

func TestHandleGetPollerConfig(t *testing.T) {
	tests := []struct {
		name           string
		setupFunc      func(*testing.T, *storage.Storage, *mibStorage.Storage)
		expectedStatus int
		validateFunc   func(*testing.T, *PollerConfigResponse)
	}{
		{
			name: "empty configuration",
			setupFunc: func(t *testing.T, store *storage.Storage, mibStore *mibStorage.Storage) {
				// No devices configured
			},
			expectedStatus: http.StatusOK,
			validateFunc: func(t *testing.T, resp *PollerConfigResponse) {
				if len(resp.Devices) != 0 {
					t.Errorf("expected 0 devices, got %d", len(resp.Devices))
				}
				if resp.Version == 0 {
					t.Errorf("expected non-zero version")
				}
			},
		},
		{
			name: "single device with no MIBs",
			setupFunc: func(t *testing.T, store *storage.Storage, mibStore *mibStorage.Storage) {
				ctx := context.Background()
				device := &storage.Device{
					Name:        "test-device",
					Address:     "203.0.113.1",
					SNMPVersion: "v2c",
					Community:   "public",
					Port:        161,
				}
				if err := store.CreateDevice(ctx, device); err != nil {
					t.Fatalf("failed to create device: %v", err)
				}
			},
			expectedStatus: http.StatusOK,
			validateFunc: func(t *testing.T, resp *PollerConfigResponse) {
				if len(resp.Devices) != 1 {
					t.Errorf("expected 1 device, got %d", len(resp.Devices))
				}
				if resp.Devices[0].Name != "test-device" {
					t.Errorf("expected device name 'test-device', got '%s'", resp.Devices[0].Name)
				}
				if resp.Devices[0].Address != "203.0.113.1" {
					t.Errorf("expected address '203.0.113.1', got '%s'", resp.Devices[0].Address)
				}
				if resp.Devices[0].SNMPVersion != "v2c" {
					t.Errorf("expected SNMP version 'v2c', got '%s'", resp.Devices[0].SNMPVersion)
				}
				if resp.Devices[0].Community != "public" {
					t.Errorf("expected community 'public', got '%s'", resp.Devices[0].Community)
				}
				if !resp.Devices[0].Enabled {
					t.Errorf("expected device to be enabled")
				}
				// The device was stored without an interval of its own, so
				// it is reported as inheriting: 0 is what the poller reads
				// as "use global.default_poll_interval".
				if resp.Devices[0].PollInterval != 0 {
					t.Errorf("expected poll interval 0 (inherit), got %d", resp.Devices[0].PollInterval)
				}
				if len(resp.Devices[0].Modules) != 0 {
					t.Errorf("expected 0 modules, got %d", len(resp.Devices[0].Modules))
				}
			},
		},
		{
			name: "single device with MIB assigned",
			setupFunc: func(t *testing.T, store *storage.Storage, mibStore *mibStorage.Storage) {
				ctx := context.Background()

				// Create device
				device := &storage.Device{
					Name:        "router-01",
					Address:     "198.51.100.11",
					SNMPVersion: "v2c",
					Community:   "public",
					Port:        161,
				}
				if err := store.CreateDevice(ctx, device); err != nil {
					t.Fatalf("failed to create device: %v", err)
				}

				// Create MIB
				mibRecord := &storage.MIB{
					Name:        "SNMPv2-MIB",
					Description: "SNMPv2 MIB",
					FilePath:    "/test/snmpv2-mib.txt",
					ObjectCount: 2,
				}
				if err := store.CreateMIB(ctx, mibRecord); err != nil {
					t.Fatalf("failed to create MIB: %v", err)
				}

				// Assign MIB to device
				if err := store.AssignMIBToDevice(ctx, device.ID, mibRecord.ID); err != nil {
					t.Fatalf("failed to assign MIB to device: %v", err)
				}

				// Create pollable OID metadata
				oid1 := &mib.OIDMetadata{
					OID:         ".1.3.6.1.2.1.1.3.0",
					Name:        "sysUpTime",
					Module:      "SNMPv2-MIB",
					Type:        "TimeTicks",
					Access:      "read-only",
					Status:      "current",
					Description: "System uptime",
					IsScalar:    true,
					Pollable:    true,
					CreatedAt:   time.Now(),
					UpdatedAt:   time.Now(),
				}
				if err := mibStore.StoreOID(oid1); err != nil {
					t.Fatalf("failed to store OID: %v", err)
				}
			},
			expectedStatus: http.StatusOK,
			validateFunc: func(t *testing.T, resp *PollerConfigResponse) {
				if len(resp.Devices) != 1 {
					t.Fatalf("expected 1 device, got %d", len(resp.Devices))
				}

				device := resp.Devices[0]
				if device.Name != "router-01" {
					t.Errorf("expected device name 'router-01', got '%s'", device.Name)
				}
				if len(device.Modules) != 1 {
					t.Fatalf("expected 1 module, got %d", len(device.Modules))
				}
				if device.Modules[0] != "SNMPv2-MIB" {
					t.Errorf("expected module 'SNMPv2-MIB', got '%s'", device.Modules[0])
				}

				// Verify MIB modules are listed
				if len(resp.MIBModules) != 1 {
					t.Fatalf("expected 1 MIB module, got %d", len(resp.MIBModules))
				}
				if resp.MIBModules[0].Name != "SNMPv2-MIB" {
					t.Errorf("expected MIB module 'SNMPv2-MIB', got '%s'", resp.MIBModules[0].Name)
				}
			},
		},
		{
			name: "multiple devices with different MIB configurations",
			setupFunc: func(t *testing.T, store *storage.Storage, mibStore *mibStorage.Storage) {
				ctx := context.Background()

				// Create first device
				device1 := &storage.Device{
					Name:        "router-01",
					Address:     "203.0.113.1",
					SNMPVersion: "v2c",
					Community:   "public",
					Port:        161,
				}
				if err := store.CreateDevice(ctx, device1); err != nil {
					t.Fatalf("failed to create device1: %v", err)
				}

				// Create second device
				device2 := &storage.Device{
					Name:        "switch-01",
					Address:     "203.0.113.2",
					SNMPVersion: "v3",
					Port:        1161,
				}
				if err := store.CreateDevice(ctx, device2); err != nil {
					t.Fatalf("failed to create device2: %v", err)
				}

				// Create MIBs
				mib1 := &storage.MIB{
					Name:        "SNMPv2-MIB",
					Description: "SNMPv2 MIB",
					FilePath:    "/test/snmpv2-mib.txt",
				}
				if err := store.CreateMIB(ctx, mib1); err != nil {
					t.Fatalf("failed to create MIB1: %v", err)
				}

				mib2 := &storage.MIB{
					Name:        "IF-MIB",
					Description: "Interface MIB",
					FilePath:    "/test/if-mib.txt",
				}
				if err := store.CreateMIB(ctx, mib2); err != nil {
					t.Fatalf("failed to create MIB2: %v", err)
				}

				// Assign MIBs to devices
				if err := store.AssignMIBToDevice(ctx, device1.ID, mib1.ID); err != nil {
					t.Fatalf("failed to assign MIB1 to device1: %v", err)
				}
				if err := store.AssignMIBToDevice(ctx, device2.ID, mib2.ID); err != nil {
					t.Fatalf("failed to assign MIB2 to device2: %v", err)
				}
			},
			expectedStatus: http.StatusOK,
			validateFunc: func(t *testing.T, resp *PollerConfigResponse) {
				if len(resp.Devices) != 2 {
					t.Fatalf("expected 2 devices, got %d", len(resp.Devices))
				}

				// Verify devices and their modules
				device1Found := false
				device2Found := false

				for _, device := range resp.Devices {
					switch device.Name {
					case "router-01":
						device1Found = true
						if len(device.Modules) != 1 {
							t.Errorf("expected 1 module for router-01, got %d", len(device.Modules))
						}
						if device.Modules[0] != "SNMPv2-MIB" {
							t.Errorf("expected SNMPv2-MIB for router-01, got %s", device.Modules[0])
						}
						if device.Address != "203.0.113.1" {
							t.Errorf("expected address '203.0.113.1' for router-01, got '%s'", device.Address)
						}
					case "switch-01":
						device2Found = true
						if len(device.Modules) != 1 {
							t.Errorf("expected 1 module for switch-01, got %d", len(device.Modules))
						}
						if device.Modules[0] != "IF-MIB" {
							t.Errorf("expected IF-MIB for switch-01, got %s", device.Modules[0])
						}
						// Port 1161 should be included in address
						if device.Address != "203.0.113.2:1161" {
							t.Errorf("expected address '203.0.113.2:1161' for switch-01, got '%s'", device.Address)
						}
					}
				}

				if !device1Found {
					t.Error("router-01 not found in response")
				}
				if !device2Found {
					t.Error("switch-01 not found in response")
				}

				// Verify MIB modules
				if len(resp.MIBModules) != 2 {
					t.Fatalf("expected 2 MIB modules, got %d", len(resp.MIBModules))
				}
			},
		},
		{
			name: "device with profile",
			setupFunc: func(t *testing.T, store *storage.Storage, mibStore *mibStorage.Storage) {
				ctx := context.Background()

				// Create device
				device := &storage.Device{
					Name:        "router-01",
					Address:     "203.0.113.1",
					SNMPVersion: "v2c",
					Community:   "public",
					Port:        161,
				}
				if err := store.CreateDevice(ctx, device); err != nil {
					t.Fatalf("failed to create device: %v", err)
				}

				// Create profile
				profile := &storage.DeviceProfile{
					Name:        "router-profile",
					Description: "Router profile",
				}
				if err := store.CreateDeviceProfile(ctx, profile); err != nil {
					t.Fatalf("failed to create profile: %v", err)
				}

				// Create MIB
				mibRecord := &storage.MIB{
					Name:        "IF-MIB",
					Description: "Interface MIB",
					FilePath:    "/test/if-mib.txt",
				}
				if err := store.CreateMIB(ctx, mibRecord); err != nil {
					t.Fatalf("failed to create MIB: %v", err)
				}

				// Assign MIB to profile
				if err := store.AssignMIBToProfile(ctx, profile.ID, mibRecord.ID); err != nil {
					t.Fatalf("failed to assign MIB to profile: %v", err)
				}

				// Assign profile to device
				if err := store.AssignProfileToDevice(ctx, device.ID, profile.ID); err != nil {
					t.Fatalf("failed to assign profile to device: %v", err)
				}
			},
			expectedStatus: http.StatusOK,
			validateFunc: func(t *testing.T, resp *PollerConfigResponse) {
				if len(resp.Devices) != 1 {
					t.Fatalf("expected 1 device, got %d", len(resp.Devices))
				}

				device := resp.Devices[0]
				if device.Profile != "router-profile" {
					t.Errorf("expected profile 'router-profile', got '%s'", device.Profile)
				}
				if len(device.Modules) != 1 {
					t.Fatalf("expected 1 module (from profile), got %d", len(device.Modules))
				}
				if device.Modules[0] != "IF-MIB" {
					t.Errorf("expected module 'IF-MIB', got '%s'", device.Modules[0])
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temporary storage
			store, err := storage.New(t.TempDir() + "/webui.db")
			if err != nil {
				t.Fatalf("failed to create storage: %v", err)
			}
			defer store.Close()

			// Create temporary MIB storage
			mibStore, err := mibStorage.New(t.TempDir() + "/mib.db")
			if err != nil {
				t.Fatalf("failed to create MIB storage: %v", err)
			}
			defer mibStore.Close()

			// Setup test data
			if tt.setupFunc != nil {
				tt.setupFunc(t, store, mibStore)
			}

			// Create server with mock dependencies
			server := &Server{
				storage:   store,
				mibLoader: &testMIBLoader{mibStorage: mibStore},
			}

			// Create request
			req := httptest.NewRequest(http.MethodGet, "/api/poller/config", nil)
			rec := httptest.NewRecorder()

			// Execute handler
			server.handleGetPollerConfig(rec, req)

			// Verify status code
			if rec.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, rec.Code)
			}

			// Verify response
			if tt.expectedStatus == http.StatusOK {
				var resp PollerConfigResponse
				if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
					t.Fatalf("failed to decode response: %v", err)
				}

				if tt.validateFunc != nil {
					tt.validateFunc(t, &resp)
				}
			}
		})
	}
}

func TestBuildPollerDeviceConfig(t *testing.T) {
	// Create temporary storage
	store, err := storage.New(t.TempDir() + "/webui.db")
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Create device
	device := &storage.Device{
		Name:        "test-device",
		Address:     "203.0.113.1",
		SNMPVersion: "v2c",
		Community:   "private",
		Port:        161,
	}
	if createErr := store.CreateDevice(ctx, device); createErr != nil {
		t.Fatalf("failed to create device: %v", createErr)
	}

	// Create MIB
	mibRecord := &storage.MIB{
		Name:        "TEST-MIB",
		Description: "Test MIB",
		FilePath:    "/test/test-mib.txt",
	}
	if createErr := store.CreateMIB(ctx, mibRecord); createErr != nil {
		t.Fatalf("failed to create MIB: %v", createErr)
	}

	// Assign MIB to device
	if assignErr := store.AssignMIBToDevice(ctx, device.ID, mibRecord.ID); assignErr != nil {
		t.Fatalf("failed to assign MIB: %v", assignErr)
	}

	// Create server
	server := &Server{
		storage: store,
	}

	// Test buildPollerDeviceConfig
	config, err := server.buildPollerDeviceConfig(ctx, device)
	if err != nil {
		t.Fatalf("buildPollerDeviceConfig failed: %v", err)
	}

	// Verify device config
	if config.Name != "test-device" {
		t.Errorf("expected name 'test-device', got '%s'", config.Name)
	}
	if config.Address != "203.0.113.1" {
		t.Errorf("expected address '203.0.113.1', got '%s'", config.Address)
	}
	if config.Community != "private" {
		t.Errorf("expected community 'private', got '%s'", config.Community)
	}
	if len(config.Modules) != 1 {
		t.Fatalf("expected 1 module, got %d", len(config.Modules))
	}
	if config.Modules[0] != "TEST-MIB" {
		t.Errorf("expected module 'TEST-MIB', got '%s'", config.Modules[0])
	}
	if config.PollInterval != 0 {
		t.Errorf("expected poll interval 0 (inherit), got %d", config.PollInterval)
	}
	if !config.Enabled {
		t.Errorf("expected device to be enabled")
	}
	if config.Tags["source"] != "webui" {
		t.Errorf("expected tag source='webui', got '%s'", config.Tags["source"])
	}
}

func TestBuildMIBConfig(t *testing.T) {
	// Create temporary MIB storage
	mibStore, err := mibStorage.New(t.TempDir() + "/mib.db")
	if err != nil {
		t.Fatalf("failed to create MIB storage: %v", err)
	}
	defer mibStore.Close()

	ctx := context.Background()

	// Create test MIB record
	mibRecord := &storage.MIB{
		ID:          1,
		Name:        "TEST-MIB",
		Description: "Test MIB",
	}

	// Store pollable and non-pollable OIDs
	pollableOID := &mib.OIDMetadata{
		OID:         ".1.2.3.4.5.0",
		Name:        "testObject",
		Module:      "TEST-MIB",
		Type:        "Counter32",
		Access:      "read-only",
		Status:      "current",
		Description: "Test object",
		Units:       "packets",
		IsScalar:    true,
		Pollable:    true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if storeErr := mibStore.StoreOID(pollableOID); storeErr != nil {
		t.Fatalf("failed to store pollable OID: %v", storeErr)
	}

	nonPollableOID := &mib.OIDMetadata{
		OID:         ".1.2.3.4",
		Name:        "testGroup",
		Module:      "TEST-MIB",
		Type:        "ObjectIdentifier",
		Access:      "not-accessible",
		Status:      "current",
		Description: "Test group",
		Pollable:    false,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if storeErr := mibStore.StoreOID(nonPollableOID); storeErr != nil {
		t.Fatalf("failed to store non-pollable OID: %v", storeErr)
	}

	// Create server
	server := &Server{
		mibLoader: &testMIBLoader{mibStorage: mibStore},
	}

	// Test buildMIBConfig
	config, err := server.buildMIBConfig(ctx, mibRecord)
	if err != nil {
		t.Fatalf("buildMIBConfig failed: %v", err)
	}

	// Verify MIB config
	if config.Name != "TEST-MIB" {
		t.Errorf("expected name 'TEST-MIB', got '%s'", config.Name)
	}

	// Should only include pollable objects
	if len(config.Objects) != 1 {
		t.Fatalf("expected 1 pollable object, got %d", len(config.Objects))
	}

	obj := config.Objects[0]
	if obj.Name != "testObject" {
		t.Errorf("expected object name 'testObject', got '%s'", obj.Name)
	}
	if obj.OID != ".1.2.3.4.5.0" {
		t.Errorf("expected OID '.1.2.3.4.5.0', got '%s'", obj.OID)
	}
	if obj.Type != "Counter32" {
		t.Errorf("expected type 'Counter32', got '%s'", obj.Type)
	}
	if obj.Unit != "packets" {
		t.Errorf("expected unit 'packets', got '%s'", obj.Unit)
	}
	if !obj.Pollable {
		t.Errorf("expected pollable to be true")
	}
}

func TestFormatDeviceAddress(t *testing.T) {
	tests := []struct {
		address  string
		port     int
		expected string
	}{
		{"203.0.113.1", 0, "203.0.113.1"},
		{"203.0.113.1", 161, "203.0.113.1"},
		{"203.0.113.1", 1161, "203.0.113.1:1161"},
		{"198.51.100.101", 8161, "198.51.100.101:8161"},
	}

	for _, tt := range tests {
		result := formatDeviceAddress(tt.address, tt.port)
		if result != tt.expected {
			t.Errorf("formatDeviceAddress(%s, %d) = %s; expected %s", tt.address, tt.port, result, tt.expected)
		}
	}
}

func TestBuildPollerDeviceConfigCarriesAssignments(t *testing.T) {
	ctx := context.Background()
	server, _ := newAssignmentServer(t)

	deviceID := mustCreateDevice(t, server.storage, "router-01")
	filteredID := mustCreateMIB(t, server.storage, "IF-MIB")
	pausedID := mustCreateMIB(t, server.storage, "UDP-MIB")
	untouchedID := mustCreateMIB(t, server.storage, "TCP-MIB")

	for _, mibID := range []int64{filteredID, pausedID, untouchedID} {
		if err := server.storage.AssignMIBToDevice(ctx, deviceID, mibID); err != nil {
			t.Fatalf("failed to assign MIB: %v", err)
		}
	}
	if err := server.storage.SetDeviceMIBAssignment(ctx, deviceID, filteredID, storage.MIBAssignment{
		Enabled:      true,
		IncludeNames: []string{"^ifHC"},
		MaxOIDs:      12,
	}); err != nil {
		t.Fatalf("failed to set assignment: %v", err)
	}
	if err := server.storage.SetDeviceMIBAssignment(ctx, deviceID, pausedID, storage.MIBAssignment{Enabled: false}); err != nil {
		t.Fatalf("failed to disable MIB: %v", err)
	}

	device, err := server.storage.GetDevice(ctx, deviceID)
	if err != nil {
		t.Fatalf("failed to load device: %v", err)
	}

	deviceConfig, err := server.buildPollerDeviceConfig(ctx, device)
	if err != nil {
		t.Fatalf("buildPollerDeviceConfig() error: %v", err)
	}

	// The disabled module is absent, which is what stops the poller reading it.
	// The untouched legacy assignment is present and unfiltered.
	if want := []string{"IF-MIB", "TCP-MIB"}; !reflect.DeepEqual(deviceConfig.Modules, want) {
		t.Errorf("modules = %v, want %v", deviceConfig.Modules, want)
	}

	want := map[string]PollerModuleFilter{
		"IF-MIB": {IncludeNames: []string{"^ifHC"}, MaxOIDs: 12},
	}
	if !reflect.DeepEqual(deviceConfig.ModuleFilters, want) {
		t.Errorf("module filters = %+v, want %+v", deviceConfig.ModuleFilters, want)
	}
}

func TestBuildPollerDeviceConfigUnknownDevice(t *testing.T) {
	server, _ := newAssignmentServer(t)

	// A device that was never stored has no assignments to resolve.
	deviceConfig, err := server.buildPollerDeviceConfig(context.Background(), &storage.Device{ID: 999, Name: "ghost"})
	if err != nil {
		t.Fatalf("buildPollerDeviceConfig() error: %v", err)
	}
	if len(deviceConfig.Modules) != 0 || len(deviceConfig.ModuleFilters) != 0 {
		t.Errorf("got modules %v and filters %+v, want neither", deviceConfig.Modules, deviceConfig.ModuleFilters)
	}
}

func TestHandleGetPollerConfigSerializesModuleFilters(t *testing.T) {
	ctx := context.Background()
	server, _ := newAssignmentServer(t)

	deviceID := mustCreateDevice(t, server.storage, "router-01")
	mibID := mustCreateMIB(t, server.storage, "IF-MIB")
	if err := server.storage.AssignMIBToDevice(ctx, deviceID, mibID); err != nil {
		t.Fatalf("failed to assign MIB: %v", err)
	}
	if err := server.storage.SetDeviceMIBAssignment(ctx, deviceID, mibID, storage.MIBAssignment{
		Enabled:     true,
		ExcludeOIDs: []string{"^1\\.3\\.6\\.1\\.2\\.1\\.2\\.2\\.1\\.1"},
	}); err != nil {
		t.Fatalf("failed to set assignment: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/poller/config", nil)
	rec := httptest.NewRecorder()
	server.handleGetPollerConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	var resp PollerConfigResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(resp.Devices) != 1 {
		t.Fatalf("devices = %d, want 1", len(resp.Devices))
	}

	filter, ok := resp.Devices[0].ModuleFilters["IF-MIB"]
	if !ok {
		t.Fatalf("module_filters = %+v, want an entry for IF-MIB", resp.Devices[0].ModuleFilters)
	}
	if len(filter.ExcludeOIDs) != 1 {
		t.Errorf("filter = %+v, want the exclude pattern to survive the round trip", filter)
	}
}
