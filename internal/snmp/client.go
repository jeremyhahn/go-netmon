package snmp

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/credential"
)

// Client wraps gosnmp.GoSNMP with additional functionality
type Client struct {
	// device is the client's own record of the device, without its
	// credentials: those are sealed in creds, which the client borrows.
	device *config.Device
	creds  *Credentials
	conn   *gosnmp.GoSNMP
	// mu guards the connection's lifetime: Connect holds it exclusively,
	// requests and Close share it, so Close can interrupt a request that
	// is blocked on the socket.
	mu sync.RWMutex
	// credMu admits one request at a time to conn while its credentials
	// are installed on it.
	credMu sync.Mutex
}

// NewClient creates a new SNMP client for a device
func NewClient(device *config.Device, timeout time.Duration, retries int) (*Client, error) {
	if device == nil {
		return nil, fmt.Errorf("device cannot be nil")
	}

	if err := device.Validate(); err != nil {
		return nil, fmt.Errorf("invalid device configuration: %w", err)
	}

	// The client keeps its own copy of the device, with the credentials sealed
	// out of it; the caller's is left as it was. The sealed copy holds no
	// locked memory and goes with the client.
	owned := *device
	creds, err := SealCredentials(context.Background(), credential.Default(), &owned)
	if err != nil {
		return nil, err
	}
	client, err := NewClientWithCredentials(&owned, creds, timeout, retries)
	if err != nil {
		_ = creds.Destroy()
		return nil, err
	}
	return client, nil
}

// NewClientWithCredentials creates an SNMP client for a device whose
// credentials have already been sealed out of it by SealCredentials.
//
// The client borrows creds: it opens them for each request and never destroys
// them. Their owner destroys them once the client is no longer used.
func NewClientWithCredentials(
	device *config.Device, creds *Credentials, timeout time.Duration, retries int,
) (*Client, error) {
	if device == nil {
		return nil, fmt.Errorf("device cannot be nil")
	}

	if err := device.ValidateSealed(creds.hasCommunity()); err != nil {
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

	// Configure SNMP version and authentication. The connection is given no
	// secret here: call installs them for each request and removes them after.
	switch device.SNMPVersion {
	case "1":
		// SNMPv1 is still what a lot of embedded equipment offers — KVMs, PDUs,
		// older UPSs — and several of them offer nothing else. It differs from
		// v2c in ways the poller has to respect: there is no GETBULK, and a
		// walk that runs off the end of the tree is reported as the
		// NoSuchName error rather than as an endOfMibView marker.
		conn.Version = gosnmp.Version1
	case "2c":
		conn.Version = gosnmp.Version2c
	case "3":
		if err := configureV3(conn, V3Credentials{
			Username:     device.Username,
			AuthProtocol: device.AuthProtocol,
			PrivProtocol: device.PrivProtocol,
		}, creds.hasAuth(), creds.hasPriv()); err != nil {
			return nil, err
		}
	default:
		return nil, &UnsupportedVersionError{Version: device.SNMPVersion}
	}

	return &Client{
		device: device,
		creds:  creds,
		conn:   conn,
	}, nil
}

// call runs op with the device's credentials installed on the connection and
// removes them before it returns.
//
// It holds the client exclusively: the connection carries the secrets for the
// length of op, and no other request may see them or remove them under it.
// The secrets are views of locked enclaves that are destroyed when call
// returns, so nothing gosnmp keeps may still refer to them by then; scrub
// sees to the connection, and gosnmp's own passphrase cache is disabled (see
// init).
//
// Opening the credentials may wait for locked memory other requests hold;
// ctx bounds that wait, and its end is returned as a CredentialError.
func (c *Client) call(ctx context.Context, op func() error) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.withCredentials(ctx, op)
}

// withCredentials runs op with the device's credentials installed on conn
// and scrubs them off again before returning. The caller holds c.mu.
func (c *Client) withCredentials(ctx context.Context, op func() error) error {
	c.credMu.Lock()
	defer c.credMu.Unlock()

	opened := false
	err := c.creds.use(ctx, func(s openSecrets) error {
		opened = true
		if err := c.install(s); err != nil {
			c.scrub()
			return err
		}
		defer c.scrub()
		return op()
	})
	// Whatever stopped the credentials from being opened -- an open that
	// failed, a limit they can never fit, or ctx ending while they waited for
	// locked memory -- is a credential failure, not the request's.
	if err != nil && !opened {
		return &CredentialError{Device: c.device.Name, Err: err}
	}
	return err
}

// install puts the opened secrets on the connection.
func (c *Client) install(s openSecrets) error {
	if c.conn.Version != gosnmp.Version3 {
		c.conn.Community = s.community
		return nil
	}
	usm, ok := c.conn.SecurityParameters.(*gosnmp.UsmSecurityParameters)
	if !ok {
		return nil
	}
	usm.AuthenticationPassphrase = s.authPassword
	usm.PrivacyPassphrase = s.privPassword
	// Once the engine is known the localized keys can be derived now; before
	// that, gosnmp derives them itself when discovery finds the engine.
	if usm.AuthoritativeEngineID != "" {
		return usm.InitSecurityKeys()
	}
	return nil
}

// scrub removes every secret, and every key derived from one, from the
// connection. The derived keys are ordinary heap memory and are wiped; the
// passphrases are views of enclaves and are dropped.
func (c *Client) scrub() {
	c.conn.Community = ""
	usm, ok := c.conn.SecurityParameters.(*gosnmp.UsmSecurityParameters)
	if !ok || usm == nil {
		return
	}
	usm.AuthenticationPassphrase = ""
	usm.PrivacyPassphrase = ""
	clear(usm.SecretKey)
	clear(usm.PrivacyKey)
	usm.SecretKey = nil
	usm.PrivacyKey = nil
}

// Connect establishes connection to the SNMP device
func (c *Client) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.withCredentials(context.Background(), func() error {
		if err := c.conn.Connect(); err != nil {
			return fmt.Errorf("failed to connect to %s: %w", c.device.Address, err)
		}
		return nil
	})
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
	var result *gosnmp.SnmpPacket
	if err := c.call(context.Background(), func() error {
		var err error
		result, err = c.conn.Get([]string{oid})
		return err
	}); err != nil {
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
	return c.GetMultipleContext(context.Background(), oids)
}

// GetMultipleContext is GetMultiple with ctx bounding the wait for the
// device's credentials to be opened (see call).
func (c *Client) GetMultipleContext(ctx context.Context, oids []string) ([]*Result, error) {
	if len(oids) == 0 {
		return nil, &NoOIDsError{}
	}

	var results []*Result
	err := c.call(ctx, func() error {
		var err error
		results, err = getWithSplit(c.conn.Get, oids)
		return err
	})
	return results, err
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
	if len(oids) == 0 {
		return nil, fmt.Errorf("no OIDs provided")
	}

	var result *gosnmp.SnmpPacket
	if err := c.call(context.Background(), func() error {
		var err error
		result, err = c.conn.GetBulk(oids, 0, maxRepetitions)
		return err
	}); err != nil {
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
	return c.call(context.Background(), func() error {
		return c.streamWalk(c.conn.Walk, "WALK", rootOID, walkFn)
	})
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
	return c.call(context.Background(), func() error {
		walk, op := c.bulkWalker()
		return c.streamWalk(walk, op, rootOID, walkFn)
	})
}

// WalkAll performs an SNMP WALK and returns all results
func (c *Client) WalkAll(rootOID string) ([]*Result, error) {
	var results []*Result
	err := c.call(context.Background(), func() error {
		var err error
		results, err = c.collectWalk(c.conn.Walk, "WALK", rootOID)
		return err
	})
	return results, err
}

// BulkWalkAll performs an SNMP BULKWALK and returns all results, falling back
// to a plain walk on SNMPv1.
func (c *Client) BulkWalkAll(rootOID string) ([]*Result, error) {
	return c.BulkWalkAllContext(context.Background(), rootOID)
}

// BulkWalkAllContext is BulkWalkAll with ctx bounding the wait for the
// device's credentials to be opened (see call).
func (c *Client) BulkWalkAllContext(ctx context.Context, rootOID string) ([]*Result, error) {
	var results []*Result
	err := c.call(ctx, func() error {
		var err error
		walk, op := c.bulkWalker()
		results, err = c.collectWalk(walk, op, rootOID)
		return err
	})
	return results, err
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
