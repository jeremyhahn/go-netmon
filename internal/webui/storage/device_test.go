package storage

import (
	"context"
	"testing"
)

func TestCreateDevice(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	tests := []struct {
		name    string
		device  *Device
		wantErr bool
	}{
		{
			name: "valid device",
			device: &Device{
				Name:        "test-device",
				Address:     "203.0.113.1",
				SNMPVersion: "2c",
				Community:   "public",
				Port:        161,
			},
			wantErr: false,
		},
		{
			name: "device with default port",
			device: &Device{
				Name:        "test-device-2",
				Address:     "203.0.113.2",
				SNMPVersion: "2c",
				Community:   "public",
			},
			wantErr: false,
		},
		{
			name: "duplicate device name",
			device: &Device{
				Name:        "test-device",
				Address:     "203.0.113.3",
				SNMPVersion: "2c",
				Community:   "public",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := store.CreateDevice(ctx, tt.device)
			if (err != nil) != tt.wantErr {
				t.Errorf("CreateDevice() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if tt.device.ID == 0 {
					t.Error("CreateDevice() should set device ID")
				}
				if tt.device.Port == 0 {
					t.Error("CreateDevice() should set default port")
				}
			}
		})
	}
}

func TestGetDevice(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create a device
	device := &Device{
		Name:        "test-device",
		Address:     "203.0.113.1",
		SNMPVersion: "2c",
		Community:   "public",
		Port:        161,
	}
	if err := store.CreateDevice(ctx, device); err != nil {
		t.Fatalf("CreateDevice() error = %v", err)
	}

	tests := []struct {
		name    string
		id      int64
		wantErr bool
	}{
		{
			name:    "existing device",
			id:      device.ID,
			wantErr: false,
		},
		{
			name:    "non-existent device",
			id:      99999,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := store.GetDevice(ctx, tt.id)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetDevice() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if got.ID != device.ID {
					t.Errorf("GetDevice() ID = %v, want %v", got.ID, device.ID)
				}
				if got.Name != device.Name {
					t.Errorf("GetDevice() Name = %v, want %v", got.Name, device.Name)
				}
			}
		})
	}
}

func TestGetDeviceByName(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	device := &Device{
		Name:        "test-device",
		Address:     "203.0.113.1",
		SNMPVersion: "2c",
		Community:   "public",
		Port:        161,
	}
	if err := store.CreateDevice(ctx, device); err != nil {
		t.Fatalf("CreateDevice() error = %v", err)
	}

	tests := []struct {
		name    string
		devName string
		wantErr bool
	}{
		{
			name:    "existing device",
			devName: "test-device",
			wantErr: false,
		},
		{
			name:    "non-existent device",
			devName: "non-existent",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := store.GetDeviceByName(ctx, tt.devName)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetDeviceByName() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && got.Name != device.Name {
				t.Errorf("GetDeviceByName() Name = %v, want %v", got.Name, device.Name)
			}
		})
	}
}

func TestListDevices(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create multiple devices
	devices := []*Device{
		{Name: "device-1", Address: "203.0.113.1", SNMPVersion: "2c", Community: "public"},
		{Name: "device-2", Address: "203.0.113.2", SNMPVersion: "2c", Community: "public"},
		{Name: "device-3", Address: "203.0.113.3", SNMPVersion: "2c", Community: "public"},
	}

	for _, d := range devices {
		if err := store.CreateDevice(ctx, d); err != nil {
			t.Fatalf("CreateDevice() error = %v", err)
		}
	}

	got, err := store.ListDevices(ctx)
	if err != nil {
		t.Fatalf("ListDevices() error = %v", err)
	}

	if len(got) != len(devices) {
		t.Errorf("ListDevices() count = %v, want %v", len(got), len(devices))
	}
}

func TestUpdateDevice(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	device := &Device{
		Name:        "test-device",
		Address:     "203.0.113.1",
		SNMPVersion: "2c",
		Community:   "public",
		Port:        161,
	}
	if err := store.CreateDevice(ctx, device); err != nil {
		t.Fatalf("CreateDevice() error = %v", err)
	}

	// Update device
	device.Address = "203.0.113.100"
	device.Community = "private"

	if err := store.UpdateDevice(ctx, device); err != nil {
		t.Fatalf("UpdateDevice() error = %v", err)
	}

	// Verify update
	got, err := store.GetDevice(ctx, device.ID)
	if err != nil {
		t.Fatalf("GetDevice() error = %v", err)
	}

	if got.Address != "203.0.113.100" {
		t.Errorf("UpdateDevice() Address = %v, want %v", got.Address, "203.0.113.100")
	}
	if got.Community != "private" {
		t.Errorf("UpdateDevice() Community = %v, want %v", got.Community, "private")
	}
}

func TestUpdateDeviceNotFound(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	device := &Device{
		ID:          99999,
		Name:        "test-device",
		Address:     "203.0.113.1",
		SNMPVersion: "2c",
		Community:   "public",
		Port:        161,
	}

	err := store.UpdateDevice(ctx, device)
	if err == nil {
		t.Error("UpdateDevice() should fail for non-existent device")
	}
}

func TestDeleteDevice(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	device := &Device{
		Name:        "test-device",
		Address:     "203.0.113.1",
		SNMPVersion: "2c",
		Community:   "public",
		Port:        161,
	}
	if err := store.CreateDevice(ctx, device); err != nil {
		t.Fatalf("CreateDevice() error = %v", err)
	}

	// Delete device
	if err := store.DeleteDevice(ctx, device.ID); err != nil {
		t.Fatalf("DeleteDevice() error = %v", err)
	}

	// Verify deletion
	_, err := store.GetDevice(ctx, device.ID)
	if err == nil {
		t.Error("GetDevice() should fail after deletion")
	}
}

func TestDeleteDeviceNotFound(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	err := store.DeleteDevice(ctx, 99999)
	if err == nil {
		t.Error("DeleteDevice() should fail for non-existent device")
	}
}

func TestDeviceMIBAssociation(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create device
	device := &Device{
		Name:        "test-device",
		Address:     "203.0.113.1",
		SNMPVersion: "2c",
		Community:   "public",
		Port:        161,
	}
	if err := store.CreateDevice(ctx, device); err != nil {
		t.Fatalf("CreateDevice() error = %v", err)
	}

	// Create MIB
	mib := &MIB{
		Name:        "TEST-MIB",
		FilePath:    "/test/path",
		Description: "Test MIB",
		ObjectCount: 10,
	}
	if err := store.CreateMIB(ctx, mib); err != nil {
		t.Fatalf("CreateMIB() error = %v", err)
	}

	// Assign MIB to device
	if err := store.AssignMIBToDevice(ctx, device.ID, mib.ID); err != nil {
		t.Fatalf("AssignMIBToDevice() error = %v", err)
	}

	// Get device MIBs
	mibs, err := store.GetDeviceMIBs(ctx, device.ID)
	if err != nil {
		t.Fatalf("GetDeviceMIBs() error = %v", err)
	}

	if len(mibs) != 1 {
		t.Errorf("GetDeviceMIBs() count = %v, want 1", len(mibs))
	}

	if len(mibs) > 0 && mibs[0].ID != mib.ID {
		t.Errorf("GetDeviceMIBs() MIB ID = %v, want %v", mibs[0].ID, mib.ID)
	}

	// Remove MIB from device
	if removeErr := store.RemoveMIBFromDevice(ctx, device.ID, mib.ID); removeErr != nil {
		t.Fatalf("RemoveMIBFromDevice() error = %v", removeErr)
	}

	// Verify removal
	mibs, err = store.GetDeviceMIBs(ctx, device.ID)
	if err != nil {
		t.Fatalf("GetDeviceMIBs() error = %v", err)
	}

	if len(mibs) != 0 {
		t.Errorf("GetDeviceMIBs() count after removal = %v, want 0", len(mibs))
	}
}

func TestDeviceProfileAssociation(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create device
	device := &Device{
		Name:        "test-device",
		Address:     "203.0.113.1",
		SNMPVersion: "2c",
		Community:   "public",
		Port:        161,
	}
	if err := store.CreateDevice(ctx, device); err != nil {
		t.Fatalf("CreateDevice() error = %v", err)
	}

	// Create profile
	profile := &DeviceProfile{
		Name:        "test-profile",
		Description: "Test Profile",
	}
	if err := store.CreateDeviceProfile(ctx, profile); err != nil {
		t.Fatalf("CreateDeviceProfile() error = %v", err)
	}

	// Assign profile to device
	if err := store.AssignProfileToDevice(ctx, device.ID, profile.ID); err != nil {
		t.Fatalf("AssignProfileToDevice() error = %v", err)
	}

	// Get device profiles
	profiles, err := store.GetDeviceProfiles(ctx, device.ID)
	if err != nil {
		t.Fatalf("GetDeviceProfiles() error = %v", err)
	}

	if len(profiles) != 1 {
		t.Errorf("GetDeviceProfiles() count = %v, want 1", len(profiles))
	}

	// Remove profile from device
	if removeErr := store.RemoveProfileFromDevice(ctx, device.ID, profile.ID); removeErr != nil {
		t.Fatalf("RemoveProfileFromDevice() error = %v", removeErr)
	}

	// Verify removal
	profiles, err = store.GetDeviceProfiles(ctx, device.ID)
	if err != nil {
		t.Fatalf("GetDeviceProfiles() error = %v", err)
	}

	if len(profiles) != 0 {
		t.Errorf("GetDeviceProfiles() count after removal = %v, want 0", len(profiles))
	}
}

func TestGetDeviceMIBsViaProfile(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create device
	device := &Device{
		Name:        "test-device",
		Address:     "203.0.113.1",
		SNMPVersion: "2c",
		Community:   "public",
		Port:        161,
	}
	if err := store.CreateDevice(ctx, device); err != nil {
		t.Fatalf("CreateDevice() error = %v", err)
	}

	// Create profile
	profile := &DeviceProfile{
		Name:        "test-profile",
		Description: "Test Profile",
	}
	if err := store.CreateDeviceProfile(ctx, profile); err != nil {
		t.Fatalf("CreateDeviceProfile() error = %v", err)
	}

	// Create MIB
	mib := &MIB{
		Name:        "TEST-MIB",
		FilePath:    "/test/path",
		Description: "Test MIB",
		ObjectCount: 10,
	}
	if err := store.CreateMIB(ctx, mib); err != nil {
		t.Fatalf("CreateMIB() error = %v", err)
	}

	// Assign MIB to profile
	if err := store.AssignMIBToProfile(ctx, profile.ID, mib.ID); err != nil {
		t.Fatalf("AssignMIBToProfile() error = %v", err)
	}

	// Assign profile to device
	if err := store.AssignProfileToDevice(ctx, device.ID, profile.ID); err != nil {
		t.Fatalf("AssignProfileToDevice() error = %v", err)
	}

	// Get device MIBs (should include MIB via profile)
	mibs, err := store.GetDeviceMIBs(ctx, device.ID)
	if err != nil {
		t.Fatalf("GetDeviceMIBs() error = %v", err)
	}

	if len(mibs) != 1 {
		t.Errorf("GetDeviceMIBs() count = %v, want 1", len(mibs))
	}

	if len(mibs) > 0 && mibs[0].ID != mib.ID {
		t.Errorf("GetDeviceMIBs() MIB ID = %v, want %v", mibs[0].ID, mib.ID)
	}
}
