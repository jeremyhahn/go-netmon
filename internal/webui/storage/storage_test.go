package storage

import (
	"context"
	"testing"
	"time"
)

func setupTestDB(t *testing.T) (*Storage, func()) {
	t.Helper()

	// t.TempDir is removed by the framework even if the test panics or fails
	// early, which a manual RemoveAll is not.
	tmpDir := t.TempDir()

	store, err := New(tmpDir)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}

	cleanup := func() {
		_ = store.Close()
	}

	return store, cleanup
}

func TestNew(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	if store == nil {
		t.Fatal("expected storage to be non-nil")
	}

	if store.db == nil {
		t.Fatal("expected database connection to be non-nil")
	}
}

func TestNewInvalidPath(t *testing.T) {
	_, err := New("/invalid/path/that/does/not/exist/db")
	if err == nil {
		t.Fatal("expected error for invalid path")
	}
}

func TestClose(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	err := store.Close()
	if err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	// Double close should not panic
	err = store.Close()
	if err != nil {
		t.Errorf("Double Close() error = %v", err)
	}
}

func TestConcurrentAccess(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create multiple devices concurrently
	done := make(chan bool)
	errors := make(chan error, 10)

	for i := 0; i < 10; i++ {
		go func(id int) {
			device := &Device{
				Name:        "test-device-" + string(rune(id+'0')),
				Address:     "203.0.113." + string(rune(id+'0')),
				SNMPVersion: "2c",
				Community:   "public",
				Port:        161,
			}
			if err := store.CreateDevice(ctx, device); err != nil {
				errors <- err
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}
	close(errors)

	// Check for errors
	for err := range errors {
		t.Errorf("CreateDevice() error = %v", err)
	}

	// Verify all devices were created
	devices, err := store.ListDevices(ctx)
	if err != nil {
		t.Fatalf("ListDevices() error = %v", err)
	}

	if len(devices) != 10 {
		t.Errorf("expected 10 devices, got %d", len(devices))
	}
}

func TestDatabaseIntegrity(t *testing.T) {
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

	// Delete device should cascade and remove association
	if err := store.DeleteDevice(ctx, device.ID); err != nil {
		t.Fatalf("DeleteDevice() error = %v", err)
	}

	// Verify MIB still exists but association is gone
	storedMIB, err := store.GetMIB(ctx, mib.ID)
	if err != nil {
		t.Errorf("GetMIB() after device deletion error = %v", err)
	}
	if storedMIB.ID != mib.ID {
		t.Error("MIB should still exist after device deletion")
	}

	// Verify device-MIB association is gone
	mibs, err := store.GetDeviceMIBs(ctx, device.ID)
	if err != nil {
		t.Errorf("GetDeviceMIBs() error = %v", err)
	}
	if len(mibs) != 0 {
		t.Errorf("expected 0 MIBs for deleted device, got %d", len(mibs))
	}
}

func TestTimestamps(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now()

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

	retrieved, err := store.GetDevice(ctx, device.ID)
	if err != nil {
		t.Fatalf("GetDevice() error = %v", err)
	}

	// Check that CreatedAt is set and reasonable
	if retrieved.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set")
	}

	if retrieved.CreatedAt.Before(now.Add(-time.Minute)) {
		t.Error("CreatedAt is too far in the past")
	}

	if retrieved.CreatedAt.After(now.Add(time.Minute)) {
		t.Error("CreatedAt is in the future")
	}
}

func TestAutoIncrementIDs(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create multiple devices
	var ids []int64
	for i := 0; i < 5; i++ {
		device := &Device{
			Name:        "test-device-" + string(rune(i+'0')),
			Address:     "203.0.113.1",
			SNMPVersion: "2c",
			Community:   "public",
			Port:        161,
		}
		if err := store.CreateDevice(ctx, device); err != nil {
			t.Fatalf("CreateDevice() error = %v", err)
		}
		ids = append(ids, device.ID)
	}

	// Verify IDs are sequential
	for i := 1; i < len(ids); i++ {
		if ids[i] != ids[i-1]+1 {
			t.Errorf("IDs are not sequential: %d followed by %d", ids[i-1], ids[i])
		}
	}
}

func TestNameUniqueness(t *testing.T) {
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

	// Try to create device with same name
	device2 := &Device{
		Name:        "test-device",
		Address:     "203.0.113.2",
		SNMPVersion: "2c",
		Community:   "public",
		Port:        161,
	}
	err := store.CreateDevice(ctx, device2)
	if err == nil {
		t.Error("expected error when creating device with duplicate name")
	}
}
