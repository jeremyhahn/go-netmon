package snmp

import (
	"testing"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/config"
)

func TestNewPool(t *testing.T) {
	pool := NewPool(10*time.Second, 3)

	if pool == nil {
		t.Fatal("Expected pool to be created")
	}

	if pool.Size() != 0 {
		t.Errorf("Expected empty pool, got size %d", pool.Size())
	}

	if pool.timeout != 10*time.Second {
		t.Errorf("Expected timeout 10s, got %v", pool.timeout)
	}

	if pool.retries != 3 {
		t.Errorf("Expected 3 retries, got %d", pool.retries)
	}
}

func TestPool_GetOrCreate_NilDevice(t *testing.T) {
	pool := NewPool(10*time.Second, 3)

	_, err := pool.GetOrCreate(nil)
	if err == nil {
		t.Error("Expected error for nil device")
	}
}

func TestPool_GetOrCreate_InvalidDevice(t *testing.T) {
	pool := NewPool(10*time.Second, 3)

	invalidDevice := &config.Device{
		Name:    "", // Invalid: empty name
		Address: "203.0.113.1",
	}

	_, err := pool.GetOrCreate(invalidDevice)
	if err == nil {
		t.Error("Expected error for invalid device")
	}
}

func TestPool_Get_NonExistent(t *testing.T) {
	pool := NewPool(10*time.Second, 3)

	_, found := pool.Get("nonexistent")
	if found {
		t.Error("Expected not to find nonexistent device")
	}
}

func TestPool_Remove_NonExistent(t *testing.T) {
	pool := NewPool(10*time.Second, 3)

	err := pool.Remove("nonexistent")
	if err != nil {
		t.Errorf("Expected no error for removing nonexistent device, got: %v", err)
	}
}

func TestPool_Size(t *testing.T) {
	pool := NewPool(10*time.Second, 3)

	if pool.Size() != 0 {
		t.Errorf("Expected size 0, got %d", pool.Size())
	}
}

func TestPool_GetAll_Empty(t *testing.T) {
	pool := NewPool(10*time.Second, 3)

	clients := pool.GetAll()
	if len(clients) != 0 {
		t.Errorf("Expected 0 clients, got %d", len(clients))
	}
}

func TestPool_CloseAll_Empty(t *testing.T) {
	pool := NewPool(10*time.Second, 3)

	err := pool.CloseAll()
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	if pool.Size() != 0 {
		t.Errorf("Expected size 0 after CloseAll, got %d", pool.Size())
	}
}

func TestPool_HealthCheck_Empty(t *testing.T) {
	pool := NewPool(10*time.Second, 3)

	results := pool.HealthCheck()
	if len(results) != 0 {
		t.Errorf("Expected 0 results, got %d", len(results))
	}
}

func TestPool_Refresh_Empty(t *testing.T) {
	pool := NewPool(10*time.Second, 3)

	err := pool.Refresh()
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
}

// The tests below need an SNMP agent to talk to. They run against the
// in-process agent in agent_test.go, which listens on a loopback UDP socket
// for the duration of the test, so they exercise the pool's real connect and
// close paths without a device on the network.

func TestPool_Integration_GetOrCreate(t *testing.T) {
	pool := NewPool(agentTimeout, 0)

	device := &config.Device{
		Name:        "test-device",
		Address:     newTestAgent(t, systemGroup()...).addr(),
		SNMPVersion: "2c",
		Community:   "public",
		Enabled:     true,
	}

	// First call should create
	client1, err := pool.GetOrCreate(device)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	if pool.Size() != 1 {
		t.Errorf("Expected pool size 1, got %d", pool.Size())
	}

	// Second call should return existing
	client2, err := pool.GetOrCreate(device)
	if err != nil {
		t.Fatalf("Failed to get client: %v", err)
	}

	if client1 != client2 {
		t.Error("Expected same client instance")
	}

	if pool.Size() != 1 {
		t.Errorf("Expected pool size still 1, got %d", pool.Size())
	}
}

func TestPool_Integration_RemoveAndGet(t *testing.T) {
	pool := NewPool(agentTimeout, 0)

	device := &config.Device{
		Name:        "test-device",
		Address:     newTestAgent(t, systemGroup()...).addr(),
		SNMPVersion: "2c",
		Community:   "public",
		Enabled:     true,
	}

	// Create client
	_, err := pool.GetOrCreate(device)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	// Verify it exists
	_, found := pool.Get("test-device")
	if !found {
		t.Error("Expected to find device")
	}

	// Remove it
	if err := pool.Remove("test-device"); err != nil {
		t.Errorf("Failed to remove device: %v", err)
	}

	// Verify it's gone
	_, found = pool.Get("test-device")
	if found {
		t.Error("Expected device to be removed")
	}

	if pool.Size() != 0 {
		t.Errorf("Expected pool size 0, got %d", pool.Size())
	}
}

func TestPool_Integration_CloseAll(t *testing.T) {
	pool := NewPool(agentTimeout, 0)

	// Add multiple devices
	for i := 1; i <= 3; i++ {
		device := &config.Device{
			Name:        "test-device-" + string(rune('0'+i)),
			Address:     newTestAgent(t, systemGroup()...).addr(),
			SNMPVersion: "2c",
			Community:   "public",
			Enabled:     true,
		}

		if _, err := pool.GetOrCreate(device); err != nil {
			t.Fatalf("Failed to create client %d: %v", i, err)
		}
	}

	if pool.Size() != 3 {
		t.Errorf("Expected pool size 3, got %d", pool.Size())
	}

	// Close all
	if err := pool.CloseAll(); err != nil {
		t.Errorf("Failed to close all: %v", err)
	}

	if pool.Size() != 0 {
		t.Errorf("Expected pool size 0 after CloseAll, got %d", pool.Size())
	}
}

func TestPool_ConcurrentAccess(t *testing.T) {
	pool := NewPool(10*time.Second, 3)

	// Test concurrent Size() calls
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				pool.Size()
			}
			done <- true
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestPool_ConcurrentGetAll(t *testing.T) {
	pool := NewPool(10*time.Second, 3)

	// Test concurrent GetAll() calls
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				pool.GetAll()
			}
			done <- true
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}
