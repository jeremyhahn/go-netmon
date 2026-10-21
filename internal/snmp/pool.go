package snmp

import (
	"fmt"
	"sync"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/config"
)

// Pool manages a pool of SNMP clients
type Pool struct {
	clients map[string]*Client
	timeout time.Duration
	retries int
	mu      sync.RWMutex
}

// NewPool creates a new SNMP client pool
func NewPool(timeout time.Duration, retries int) *Pool {
	return &Pool{
		clients: make(map[string]*Client),
		timeout: timeout,
		retries: retries,
	}
}

// GetOrCreate returns an existing client or creates a new one
func (p *Pool) GetOrCreate(device *config.Device) (*Client, error) {
	return p.getOrCreate(device, func() (*Client, error) {
		return NewClient(device, p.timeout, p.retries)
	})
}

// GetOrCreateWithCredentials is GetOrCreate for a device whose credentials
// the caller has sealed and keeps: the pooled client borrows creds, and the
// caller destroys them once the device is removed from the pool.
func (p *Pool) GetOrCreateWithCredentials(device *config.Device, creds *Credentials) (*Client, error) {
	return p.getOrCreate(device, func() (*Client, error) {
		return NewClientWithCredentials(device, creds, p.timeout, p.retries)
	})
}

func (p *Pool) getOrCreate(device *config.Device, newClient func() (*Client, error)) (*Client, error) {
	if device == nil {
		return nil, fmt.Errorf("device cannot be nil")
	}

	// Try to get existing client
	p.mu.RLock()
	client, exists := p.clients[device.Name]
	p.mu.RUnlock()

	if exists && client.IsConnected() {
		return client, nil
	}

	// Create new client with write lock
	p.mu.Lock()
	defer p.mu.Unlock()

	// Double-check after acquiring write lock
	if client, exists := p.clients[device.Name]; exists && client.IsConnected() {
		return client, nil
	}

	// Create new client
	created, err := newClient()
	if err != nil {
		return nil, fmt.Errorf("failed to create SNMP client: %w", err)
	}

	// Connect to device
	if err := created.Connect(); err != nil {
		return nil, fmt.Errorf("failed to connect to device: %w", err)
	}

	// Store in pool
	p.clients[device.Name] = created

	return created, nil
}

// Get returns an existing client without creating a new one
func (p *Pool) Get(deviceName string) (*Client, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	client, exists := p.clients[deviceName]
	return client, exists
}

// Remove removes a client from the pool and closes its connection
func (p *Pool) Remove(deviceName string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	client, exists := p.clients[deviceName]
	if !exists {
		return nil
	}

	// Drop the client whether or not the close succeeds. Keeping it would be
	// worse than losing it: Close leaves conn.Conn set, so IsConnected still
	// reports true and GetOrCreate would keep handing out a socket that has
	// already been closed, for as long as the process lives. CloseAll already
	// clears its map unconditionally for the same reason.
	delete(p.clients, deviceName)

	if err := client.Close(); err != nil {
		return fmt.Errorf("failed to close client connection: %w", err)
	}

	return nil
}

// CloseAll closes all clients in the pool
func (p *Pool) CloseAll() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	var lastErr error
	for name, client := range p.clients {
		if err := client.Close(); err != nil {
			lastErr = fmt.Errorf("failed to close client %s: %w", name, err)
		}
	}

	// Clear the map
	p.clients = make(map[string]*Client)

	return lastErr
}

// Size returns the number of clients in the pool
func (p *Pool) Size() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.clients)
}

// GetAll returns all clients in the pool
func (p *Pool) GetAll() []*Client {
	p.mu.RLock()
	defer p.mu.RUnlock()

	clients := make([]*Client, 0, len(p.clients))
	for _, client := range p.clients {
		clients = append(clients, client)
	}

	return clients
}

// HealthCheck performs a health check on all clients
func (p *Pool) HealthCheck() map[string]error {
	p.mu.RLock()
	clients := make(map[string]*Client, len(p.clients))
	for name, client := range p.clients {
		clients[name] = client
	}
	p.mu.RUnlock()

	results := make(map[string]error)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for name, client := range clients {
		wg.Add(1)
		go func(n string, c *Client) {
			defer wg.Done()

			// Try to get device info as a health check
			_, err := c.GetDeviceInfo()

			mu.Lock()
			results[n] = err
			mu.Unlock()
		}(name, client)
	}

	wg.Wait()
	return results
}

// Refresh reconnects all clients that are not connected
func (p *Pool) Refresh() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	var lastErr error
	for name, client := range p.clients {
		if !client.IsConnected() {
			if err := client.Connect(); err != nil {
				lastErr = fmt.Errorf("failed to reconnect client %s: %w", name, err)
			}
		}
	}

	return lastErr
}
