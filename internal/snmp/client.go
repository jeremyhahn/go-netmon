package snmp

import (
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/jeremyhahn/go-netmon/internal/config"
)

// Client wraps gosnmp.GoSNMP with additional functionality
type Client struct {
	device *config.Device
	conn   *gosnmp.GoSNMP
	mu     sync.RWMutex
}

// NewClient creates a new SNMP client for a device
func NewClient(device *config.Device, timeout time.Duration, retries int) (*Client, error) {
	if device == nil {
		return nil, fmt.Errorf("device cannot be nil")
	}

	if err := device.Validate(); err != nil {
		return nil, fmt.Errorf("invalid device configuration: %w", err)
	}

	// Parse address to separate host and port
	host, portStr, err := net.SplitHostPort(device.Address)
	if err != nil {
		// No port specified, use address as host and default port
		host = device.Address
		portStr = "161"
	}

	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return nil, fmt.Errorf("invalid port in address %s: %w", device.Address, err)
	}

	conn := &gosnmp.GoSNMP{
		Target:    host,
		Port:      uint16(port),
		Transport: "udp",
		Timeout:   timeout,
		Retries:   retries,
		MaxOids:   100, // Increase from default 60 to handle larger OID sets
	}

	// Configure SNMP version and authentication
	switch device.SNMPVersion {
	case "1":
		// SNMPv1 is still what a lot of embedded equipment offers — KVMs, PDUs,
		// older UPSs — and several of them offer nothing else. It differs from
		// v2c in ways the poller has to respect: there is no GETBULK, and a
		// walk that runs off the end of the tree is reported as the
		// NoSuchName error rather than as an endOfMibView marker.
		conn.Version = gosnmp.Version1
		conn.Community = device.Community
	case "2c":
		conn.Version = gosnmp.Version2c
		conn.Community = device.Community
	case "3":
		if err := ConfigureV3(conn, V3Credentials{
			Username:     device.Username,
			AuthProtocol: device.AuthProtocol,
			AuthPassword: device.AuthPassword,
			PrivProtocol: device.PrivProtocol,
			PrivPassword: device.PrivPassword,
		}); err != nil {
			return nil, err
		}
	default:
		return nil, &UnsupportedVersionError{Version: device.SNMPVersion}
	}

	return &Client{
		device: device,
		conn:   conn,
	}, nil
}

// Connect establishes connection to the SNMP device
func (c *Client) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.conn.Connect(); err != nil {
		return fmt.Errorf("failed to connect to %s: %w", c.device.Address, err)
	}

	return nil
}

// Close closes the SNMP connection.
//
// It takes a read lock, not a write lock. Closing the socket is precisely how a
// request already in flight is interrupted, so waiting for that request to
// finish before closing defeats the purpose — and against a device that never
// answers, that wait is the full timeout × retries budget. Holding the control
// path behind it is what let one dead device stall device registration and
// status lookups for every other device.
//
// Concurrent readers are exactly the in-flight requests being interrupted; the
// read lock is there to exclude Connect from swapping the socket underneath.
func (c *Client) Close() error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.conn != nil && c.conn.Conn != nil {
		return c.conn.Conn.Close()
	}
	return nil
}

// Get performs an SNMP GET for a single OID
func (c *Client) Get(oid string) (*Result, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result, err := c.conn.Get([]string{oid})
	if err != nil {
		return nil, fmt.Errorf("SNMP GET failed for %s: %w", oid, err)
	}

	if len(result.Variables) == 0 {
		return nil, fmt.Errorf("no results returned for OID %s", oid)
	}

	return &Result{
		OID:       result.Variables[0].Name,
		Type:      result.Variables[0].Type,
		Value:     result.Variables[0].Value,
		Timestamp: time.Now(),
	}, nil
}

// GetMultiple performs an SNMP GET for multiple OIDs.
//
// An agent that cannot fit the whole response in one PDU answers tooBig: an
// empty response carrying an error status, not a transport error. When that
// happens the request is split in half and each half retried, which converges
// on whatever the device can actually answer without needing to know its limit
// in advance. Treating tooBig as success is how a device silently returns no
// scalars at all while every poll still reports success.
func (c *Client) GetMultiple(oids []string) ([]*Result, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if len(oids) == 0 {
		return nil, &NoOIDsError{}
	}

	return getWithSplit(c.conn.Get, oids)
}

// snmpGetter issues a single SNMP GET. Keeping it as a function lets the
// tooBig splitting policy be exercised without a network or a live agent.
type snmpGetter func(oids []string) (*gosnmp.SnmpPacket, error)

// getWithSplit performs a GET, halving the request and retrying whenever the
// agent answers tooBig, until each part fits.
func getWithSplit(get snmpGetter, oids []string) ([]*Result, error) {
	response, err := get(oids)
	if err != nil {
		return nil, &GetError{Count: len(oids), Err: err}
	}

	if response.Error == gosnmp.TooBig {
		if len(oids) == 1 {
			// A single varbind the agent still calls too big is not something
			// splitting can fix.
			return nil, &TooBigError{Count: 1, OID: oids[0]}
		}

		mid := len(oids) / 2
		first, err := getWithSplit(get, oids[:mid])
		if err != nil {
			return nil, err
		}
		second, err := getWithSplit(get, oids[mid:])
		if err != nil {
			return nil, err
		}
		return append(first, second...), nil
	}

	if response.Error != gosnmp.NoError {
		return nil, &ResponseError{Status: response.Error, Index: response.ErrorIndex, Count: len(oids)}
	}

	timestamp := time.Now()
	results := make([]*Result, 0, len(response.Variables))

	for _, variable := range response.Variables {
		results = append(results, &Result{
			OID:       variable.Name,
			Type:      variable.Type,
			Value:     variable.Value,
			Timestamp: timestamp,
		})
	}

	return results, nil
}

// GetBulk performs an SNMP GETBULK operation
func (c *Client) GetBulk(oids []string, maxRepetitions uint32) ([]*Result, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if len(oids) == 0 {
		return nil, fmt.Errorf("no OIDs provided")
	}

	result, err := c.conn.GetBulk(oids, 0, maxRepetitions)
	if err != nil {
		return nil, fmt.Errorf("SNMP GETBULK failed: %w", err)
	}

	timestamp := time.Now()
	results := make([]*Result, 0, len(result.Variables))

	for _, variable := range result.Variables {
		results = append(results, &Result{
			OID:       variable.Name,
			Type:      variable.Type,
			Value:     variable.Value,
			Timestamp: timestamp,
		})
	}

	return results, nil
}

// walker is one of gosnmp's walk variants: Walk or BulkWalk.
type walker func(rootOID string, walkFn gosnmp.WalkFunc) error

// resultFromPDU adapts a gosnmp PDU to a Result stamped at ts.
func resultFromPDU(variable gosnmp.SnmpPDU, ts time.Time) *Result {
	return &Result{
		OID:       variable.Name,
		Type:      variable.Type,
		Value:     variable.Value,
		Timestamp: ts,
	}
}

// streamWalk runs walk and hands each PDU to fn as it arrives. op names the
// operation in any error. The caller holds c.mu.
func (c *Client) streamWalk(walk walker, op, rootOID string, fn func(*Result) error) error {
	received := 0
	if err := walk(rootOID, func(variable gosnmp.SnmpPDU) error {
		received++
		return fn(resultFromPDU(variable, time.Now()))
	}); err != nil {
		return &WalkError{Op: op, OID: rootOID, Received: received, Err: err}
	}
	return nil
}

// collectWalk runs walk and returns every PDU, all stamped with the time the
// walk started so one traversal yields one consistent sample set. The caller
// holds c.mu.
func (c *Client) collectWalk(walk walker, op, rootOID string) ([]*Result, error) {
	var results []*Result
	timestamp := time.Now()

	if err := walk(rootOID, func(variable gosnmp.SnmpPDU) error {
		results = append(results, resultFromPDU(variable, timestamp))
		return nil
	}); err != nil {
		// The rows that did arrive are dropped -- a half-walked table is not a
		// table -- but how many there were is kept on the error, because it is
		// what tells a caller the device was answering.
		return nil, &WalkError{Op: op, OID: rootOID, Received: len(results), Err: err}
	}
	return results, nil
}

// Walk performs an SNMP WALK starting from the given OID
func (c *Client) Walk(rootOID string, walkFn func(*Result) error) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.streamWalk(c.conn.Walk, "WALK", rootOID, walkFn)
}

// bulkWalker reports the walk to use for a table and what to call it.
//
// GETBULK does not exist in SNMPv1: gosnmp refuses it outright with "GETBULK
// not supported in SNMPv1", so a v1 device would fail every table walk with an
// error about a protocol feature rather than about anything an operator did.
// The version is known here and nowhere else that matters, so the choice is
// made here rather than pushed onto every caller.
//
// The fallback is a plain WALK, which is GETNEXT one variable at a time. It is
// slower — that is the whole reason GETBULK exists — but it is what v1 has.
func (c *Client) bulkWalker() (walker, string) {
	if c.conn.Version == gosnmp.Version1 {
		return c.conn.Walk, "WALK"
	}
	return c.conn.BulkWalk, "BULKWALK"
}

// BulkWalk performs an SNMP BULKWALK (optimized walk using GETBULK), falling
// back to a plain walk on SNMPv1, which has no GETBULK.
func (c *Client) BulkWalk(rootOID string, walkFn func(*Result) error) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	walk, op := c.bulkWalker()
	return c.streamWalk(walk, op, rootOID, walkFn)
}

// WalkAll performs an SNMP WALK and returns all results
func (c *Client) WalkAll(rootOID string) ([]*Result, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.collectWalk(c.conn.Walk, "WALK", rootOID)
}

// BulkWalkAll performs an SNMP BULKWALK and returns all results, falling back
// to a plain walk on SNMPv1.
func (c *Client) BulkWalkAll(rootOID string) ([]*Result, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	walk, op := c.bulkWalker()
	return c.collectWalk(walk, op, rootOID)
}

// GetDeviceInfo returns basic device information
func (c *Client) GetDeviceInfo() (*DeviceInfo, error) {
	sysOIDs := []string{
		"1.3.6.1.2.1.1.1.0", // sysDescr
		"1.3.6.1.2.1.1.3.0", // sysUpTime
		"1.3.6.1.2.1.1.5.0", // sysName
		"1.3.6.1.2.1.1.6.0", // sysLocation
		"1.3.6.1.2.1.1.4.0", // sysContact
	}

	results, err := c.GetMultiple(sysOIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to get device info: %w", err)
	}

	info := &DeviceInfo{
		Address: c.device.Address,
	}

	for i, result := range results {
		switch i {
		case 0:
			if val, ok := result.Value.([]byte); ok {
				info.Description = string(val)
			}
		case 1:
			if val, ok := result.Value.(uint32); ok {
				info.Uptime = time.Duration(val) * time.Millisecond * 10
			}
		case 2:
			if val, ok := result.Value.([]byte); ok {
				info.Name = string(val)
			}
		case 3:
			if val, ok := result.Value.([]byte); ok {
				info.Location = string(val)
			}
		case 4:
			if val, ok := result.Value.([]byte); ok {
				info.Contact = string(val)
			}
		}
	}

	return info, nil
}

// IsConnected returns true if the client is connected
func (c *Client) IsConnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.conn != nil && c.conn.Conn != nil
}

// GetDevice returns the device configuration
func (c *Client) GetDevice() *config.Device {
	return c.device
}
