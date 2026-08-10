package snmp

import (
	"strings"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/config"
)

// poolDevice names a device in the pool and points it at addr.
func poolDevice(name, addr string) *config.Device {
	return &config.Device{
		Name:        name,
		Address:     addr,
		SNMPVersion: "2c",
		Community:   "public",
		Enabled:     true,
	}
}

func TestPoolGetOrCreateReportsConnectFailure(t *testing.T) {
	pool := NewPool(agentTimeout, 0)

	_, err := pool.GetOrCreate(poolDevice("unroutable", unroutableAddress))
	if err == nil {
		t.Fatal("GetOrCreate reported success for a device it could not connect to")
	}
	if pool.Size() != 0 {
		t.Errorf("pool holds %d clients; a device that never connected must not be cached", pool.Size())
	}
}

func TestPoolGetAllReturnsEveryClient(t *testing.T) {
	pool := NewPool(agentTimeout, 0)

	names := []string{"router-a", "router-b"}
	for _, name := range names {
		if _, err := pool.GetOrCreate(poolDevice(name, newTestAgent(t, systemGroup()...).addr())); err != nil {
			t.Fatalf("GetOrCreate(%s): %v", name, err)
		}
	}

	all := pool.GetAll()
	if len(all) != len(names) {
		t.Fatalf("GetAll returned %d clients, want %d", len(all), len(names))
	}

	seen := make(map[string]bool, len(all))
	for _, client := range all {
		seen[client.GetDevice().Name] = true
	}
	for _, name := range names {
		if !seen[name] {
			t.Errorf("GetAll omitted %s", name)
		}
	}
}

// TestPoolRemoveReportsCloseFailure covers the path where the socket is already
// gone — a device whose connection was torn down underneath the pool. Remove
// must surface that rather than swallow it, because the caller is usually
// deleting the device and needs to know the teardown was not clean.
func TestPoolRemoveReportsCloseFailure(t *testing.T) {
	pool := NewPool(agentTimeout, 0)

	client, err := pool.GetOrCreate(poolDevice("router", newTestAgent(t, systemGroup()...).addr()))
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	// Close the socket behind the pool's back, so the pool's own Close fails.
	if closeErr := client.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}

	err = pool.Remove("router")
	if err == nil {
		t.Fatal("Remove reported success for a client whose socket could not be closed")
	}
	if !strings.Contains(err.Error(), "close") {
		t.Errorf("error %q does not say the close failed", err)
	}

	// Reporting the failure is only half of it. A failed close used to leave
	// the client in the pool, and because Close leaves conn.Conn set,
	// IsConnected still reports true — so GetOrCreate would hand the same
	// dead socket back for the life of the process.
	if _, stillPooled := pool.Get("router"); stillPooled {
		t.Error("Remove kept a client whose socket it had already closed")
	}

	replacement, err := pool.GetOrCreate(poolDevice("router", newTestAgent(t, systemGroup()...).addr()))
	if err != nil {
		t.Fatalf("GetOrCreate after a failed Remove: %v", err)
	}
	if replacement == client {
		t.Error("GetOrCreate handed back the dead client instead of dialing a new one")
	}
}

func TestPoolCloseAllReportsCloseFailure(t *testing.T) {
	pool := NewPool(agentTimeout, 0)

	client, err := pool.GetOrCreate(poolDevice("router", newTestAgent(t, systemGroup()...).addr()))
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	if closeErr := client.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}

	err = pool.CloseAll()
	if err == nil {
		t.Fatal("CloseAll reported success although one client failed to close")
	}
	if !strings.Contains(err.Error(), "router") {
		t.Errorf("error %q does not name the client that failed", err)
	}
	// The pool is emptied regardless: a failed close must not leave a client
	// behind for the next caller to be handed.
	if pool.Size() != 0 {
		t.Errorf("pool holds %d clients after CloseAll, want 0", pool.Size())
	}
}

func TestPoolHealthCheck(t *testing.T) {
	pool := NewPool(agentTimeout, 0)

	healthy := newTestAgent(t, systemGroup()...)
	unresponsive := newTestAgent(t, systemGroup()...)
	unresponsive.silent.Store(true)

	if _, err := pool.GetOrCreate(poolDevice("healthy", healthy.addr())); err != nil {
		t.Fatalf("GetOrCreate(healthy): %v", err)
	}
	if _, err := pool.GetOrCreate(poolDevice("unresponsive", unresponsive.addr())); err != nil {
		t.Fatalf("GetOrCreate(unresponsive): %v", err)
	}

	results := pool.HealthCheck()
	if len(results) != 2 {
		t.Fatalf("HealthCheck reported on %d devices, want 2", len(results))
	}
	if err, ok := results["healthy"]; !ok || err != nil {
		t.Errorf("healthy device reported %v, want nil", err)
	}
	if err, ok := results["unresponsive"]; !ok || err == nil {
		t.Error("a device that never answers was reported healthy")
	}
}

// TestPoolRefreshReconnects covers the path Refresh exists for: a client that
// has lost its socket is dialed again, and one still holding a socket is left
// alone.
func TestPoolRefreshReconnects(t *testing.T) {
	pool := NewPool(agentTimeout, 0)
	agent := newTestAgent(t, systemGroup()...)

	disconnected, err := NewClient(poolDevice("disconnected", agent.addr()), agentTimeout, 0)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	pool.clients["disconnected"] = disconnected

	connected, err := pool.GetOrCreate(poolDevice("connected", agent.addr()))
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}

	if disconnected.IsConnected() {
		t.Fatal("the fixture is wrong: the client is already connected")
	}

	if err := pool.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !disconnected.IsConnected() {
		t.Error("Refresh left a disconnected client without a socket")
	}
	if !connected.IsConnected() {
		t.Error("Refresh disturbed a client that was already connected")
	}
}

func TestPoolRefreshReportsReconnectFailure(t *testing.T) {
	pool := NewPool(agentTimeout, 0)

	client, err := NewClient(poolDevice("unroutable", unroutableAddress), agentTimeout, 0)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	pool.clients["unroutable"] = client

	err = pool.Refresh()
	if err == nil {
		t.Fatal("Refresh reported success although a client could not be reconnected")
	}
	if !strings.Contains(err.Error(), "unroutable") {
		t.Errorf("error %q does not name the client that failed", err)
	}
}
