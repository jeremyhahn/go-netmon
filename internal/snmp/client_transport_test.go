package snmp

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/jeremyhahn/go-netmon/internal/config"
)

// sysDescrOID and friends are the scalars GetDeviceInfo asks for, in the order
// it asks for them.
const (
	sysDescrOID    = "1.3.6.1.2.1.1.1.0"
	sysUpTimeOID   = "1.3.6.1.2.1.1.3.0"
	sysContactOID  = "1.3.6.1.2.1.1.4.0"
	sysNameOID     = "1.3.6.1.2.1.1.5.0"
	sysLocationOID = "1.3.6.1.2.1.1.6.0"
)

// unroutableAddress is a link-local IPv6 literal with no zone. Connecting to it
// fails locally with EINVAL: the kernel rejects it without a DNS lookup and
// without a packet leaving the process, which makes it a deterministic
// connection failure that costs microseconds.
const unroutableAddress = "[fe80::1]:161"

// systemGroup is a small but realistic system group.
func systemGroup() []gosnmp.SnmpPDU {
	return []gosnmp.SnmpPDU{
		scalar(sysDescrOID, "Cisco IOS XE Software, Version 17.9"),
		{Name: sysUpTimeOID, Type: gosnmp.TimeTicks, Value: uint32(360000)},
		scalar(sysContactOID, "noc@example.test"),
		scalar(sysNameOID, "edge-router-1"),
		scalar(sysLocationOID, "rack 4"),
	}
}

// newUnconnectedClient returns a client that has never dialed. Every request
// on it fails inside gosnmp before any socket is used.
func newUnconnectedClient(t *testing.T) *Client {
	t.Helper()

	client, err := NewClient(&config.Device{
		Name:        "unconnected",
		Address:     "127.0.0.1:161",
		SNMPVersion: "2c",
		Community:   "public",
	}, agentTimeout, 0)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func TestClientConnect(t *testing.T) {
	agent := newTestAgent(t, systemGroup()...)

	client, err := NewClient(agentDevice(t, agent), agentTimeout, 0)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if client.IsConnected() {
		t.Fatal("a client reports connected before Connect was called")
	}
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = client.Close() }()

	if !client.IsConnected() {
		t.Error("a client does not report connected after a successful Connect")
	}
}

func TestClientConnectUnroutableAddress(t *testing.T) {
	client, err := NewClient(&config.Device{
		Name:        "unroutable",
		Address:     unroutableAddress,
		SNMPVersion: "2c",
		Community:   "public",
	}, agentTimeout, 0)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	err = client.Connect()
	if err == nil {
		t.Fatal("Connect to an unroutable address reported success")
	}
	if !strings.Contains(err.Error(), unroutableAddress) {
		t.Errorf("error %q does not name the address that failed", err)
	}
	if client.IsConnected() {
		t.Error("a client reports connected after Connect failed")
	}
}

func TestClientGet(t *testing.T) {
	agent := newTestAgent(t, systemGroup()...)
	client := newAgentClient(t, agent)

	before := time.Now()
	result, err := client.Get(sysDescrOID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if normalizeOID(result.OID) != sysDescrOID {
		t.Errorf("OID = %q, want %q", result.OID, sysDescrOID)
	}
	if result.Type != gosnmp.OctetString {
		t.Errorf("Type = %v, want OctetString", result.Type)
	}
	value, err := result.GetStringValue()
	if err != nil {
		t.Fatalf("GetStringValue: %v", err)
	}
	if !strings.Contains(value, "Cisco IOS XE") {
		t.Errorf("value = %q, want the sysDescr the agent serves", value)
	}
	if result.Timestamp.Before(before) {
		t.Error("the result is stamped before the request was made")
	}
}

// TestClientGetUnknownOIDIsNotAnError pins current behavior: an agent that
// does not implement an object answers noSuchObject inside an otherwise
// successful response, and Get passes that through as a Result rather than an
// error. Callers must inspect Type; nothing upstream may assume a nil error
// means a usable value.
func TestClientGetUnknownOIDIsNotAnError(t *testing.T) {
	agent := newTestAgent(t, systemGroup()...)
	client := newAgentClient(t, agent)

	result, err := client.Get("1.3.6.1.4.1.9999.1.0")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if result.Type != gosnmp.NoSuchObject {
		t.Errorf("Type = %v, want NoSuchObject", result.Type)
	}
	if _, err := result.GetStringValue(); err == nil {
		t.Error("a noSuchObject result yielded a string value")
	}
}

func TestClientGetWithoutConnection(t *testing.T) {
	client := newUnconnectedClient(t)

	_, err := client.Get(sysDescrOID)
	if err == nil {
		t.Fatal("Get on an unconnected client reported success")
	}
	if !strings.Contains(err.Error(), sysDescrOID) {
		t.Errorf("error %q does not name the OID that failed", err)
	}
}

func TestClientGetTimesOutWhenAgentIsSilent(t *testing.T) {
	agent := newTestAgent(t, systemGroup()...)
	agent.silent.Store(true)
	client := newAgentClient(t, agent)

	_, err := client.Get(sysDescrOID)
	if err == nil {
		t.Fatal("Get against a device that never answers reported success")
	}
	if agent.requests.Load() == 0 {
		t.Error("the request never reached the agent")
	}
}

// TestClientGetMultipleSplitsOverTheWire is the end-to-end form of the tooBig
// regression: a real agent, real SNMP messages, and an agent whose response
// limit is smaller than the batch. gosnmp reports tooBig as a successful call
// with no varbinds, so without the splitting policy this returns zero results
// while every layer above reports a healthy poll.
func TestClientGetMultipleSplitsOverTheWire(t *testing.T) {
	agent := newTestAgent(t, systemGroup()...)
	agent.limitResponseTo(2)
	client := newAgentClient(t, agent)

	oids := []string{sysDescrOID, sysUpTimeOID, sysContactOID, sysNameOID, sysLocationOID}

	results, err := client.GetMultiple(oids)
	if err != nil {
		t.Fatalf("GetMultiple: %v", err)
	}
	if len(results) != len(oids) {
		t.Fatalf("got %d results, want %d — the split lost values", len(results), len(oids))
	}
	for i, result := range results {
		if normalizeOID(result.OID) != oids[i] {
			t.Errorf("result %d has OID %s, want %s", i, result.OID, oids[i])
		}
	}
	if agent.requests.Load() < 2 {
		t.Errorf("the agent saw %d requests; a batch over its limit must be split", agent.requests.Load())
	}
}

func TestClientGetMultipleWithoutConnection(t *testing.T) {
	client := newUnconnectedClient(t)

	_, err := client.GetMultiple([]string{sysDescrOID, sysNameOID})
	if err == nil {
		t.Fatal("GetMultiple on an unconnected client reported success")
	}

	var getErr *GetError
	if !errors.As(err, &getErr) {
		t.Fatalf("got %T, want *GetError", err)
	}
	if getErr.Count != 2 {
		t.Errorf("Count = %d, want 2", getErr.Count)
	}
}

func TestClientGetBulk(t *testing.T) {
	agent := newTestAgent(t, systemGroup()...)
	client := newAgentClient(t, agent)

	results, err := client.GetBulk([]string{"1.3.6.1.2.1.1"}, 3)
	if err != nil {
		t.Fatalf("GetBulk: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("got %d results, want the 3 repetitions requested", len(results))
	}
	if normalizeOID(results[0].OID) != sysDescrOID {
		t.Errorf("first result is %s, want the first object under the root", results[0].OID)
	}

	// One GETBULK is one sample, so every varbind carries the same stamp.
	for i, result := range results[1:] {
		if !result.Timestamp.Equal(results[0].Timestamp) {
			t.Errorf("result %d is stamped %v, want the single time %v shared by the response",
				i+1, result.Timestamp, results[0].Timestamp)
		}
	}
}

func TestClientGetBulkRejectsEmptyOIDs(t *testing.T) {
	agent := newTestAgent(t, systemGroup()...)
	client := newAgentClient(t, agent)

	if _, err := client.GetBulk(nil, 10); err == nil {
		t.Fatal("GetBulk with no OIDs reported success")
	}
	if agent.requests.Load() != 0 {
		t.Error("an empty GETBULK was put on the wire instead of being rejected")
	}
}

func TestClientGetBulkWithoutConnection(t *testing.T) {
	client := newUnconnectedClient(t)

	if _, err := client.GetBulk([]string{"1.3.6.1.2.1.1"}, 5); err == nil {
		t.Fatal("GetBulk on an unconnected client reported success")
	}
}

func TestClientWalkAll(t *testing.T) {
	agent := newTestAgent(t, systemGroup()...)
	client := newAgentClient(t, agent)

	results, err := client.WalkAll("1.3.6.1.2.1.1")
	if err != nil {
		t.Fatalf("WalkAll: %v", err)
	}
	if len(results) != len(systemGroup()) {
		t.Fatalf("walked %d objects, want %d", len(results), len(systemGroup()))
	}
	if normalizeOID(results[0].OID) != sysDescrOID {
		t.Errorf("walk started at %s, want %s", results[0].OID, sysDescrOID)
	}
}

func TestClientBulkWalkAll(t *testing.T) {
	agent := newTestAgent(t, systemGroup()...)
	client := newAgentClient(t, agent)

	results, err := client.BulkWalkAll("1.3.6.1.2.1.1")
	if err != nil {
		t.Fatalf("BulkWalkAll: %v", err)
	}
	if len(results) != len(systemGroup()) {
		t.Fatalf("bulk-walked %d objects, want %d", len(results), len(systemGroup()))
	}
}

func TestClientWalkStreamsEveryObject(t *testing.T) {
	agent := newTestAgent(t,
		counter("1.3.6.1.2.1.2.2.1.10.1", 100),
		counter("1.3.6.1.2.1.2.2.1.10.2", 200),
		counter("1.3.6.1.2.1.2.2.1.10.10", 300), // sorts after .2 numerically, before it as text
	)
	client := newAgentClient(t, agent)

	var seen []string
	if err := client.Walk("1.3.6.1.2.1.2.2.1.10", func(r *Result) error {
		seen = append(seen, normalizeOID(r.OID))
		return nil
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}

	want := []string{
		"1.3.6.1.2.1.2.2.1.10.1",
		"1.3.6.1.2.1.2.2.1.10.2",
		"1.3.6.1.2.1.2.2.1.10.10",
	}
	if len(seen) != len(want) {
		t.Fatalf("walked %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Errorf("object %d is %s, want %s", i, seen[i], want[i])
		}
	}
}

func TestClientBulkWalkStreamsEveryObject(t *testing.T) {
	agent := newTestAgent(t,
		counter("1.3.6.1.2.1.2.2.1.16.1", 11),
		counter("1.3.6.1.2.1.2.2.1.16.2", 22),
	)
	client := newAgentClient(t, agent)

	count := 0
	if err := client.BulkWalk("1.3.6.1.2.1.2.2.1.16", func(*Result) error {
		count++
		return nil
	}); err != nil {
		t.Fatalf("BulkWalk: %v", err)
	}
	if count != 2 {
		t.Errorf("bulk walk yielded %d objects, want 2", count)
	}
}

func TestClientWalkPropagatesCallbackError(t *testing.T) {
	agent := newTestAgent(t, systemGroup()...)
	client := newAgentClient(t, agent)

	stop := errors.New("caller gave up")
	err := client.Walk("1.3.6.1.2.1.1", func(*Result) error { return stop })
	if err == nil {
		t.Fatal("a walk whose callback failed reported success")
	}
	if !errors.Is(err, stop) {
		t.Errorf("error %v does not wrap the callback's error", err)
	}
}

func TestClientWalkWithoutConnection(t *testing.T) {
	client := newUnconnectedClient(t)
	noop := func(*Result) error { return nil }

	if err := client.Walk("1.3.6.1.2.1.1", noop); err == nil {
		t.Error("Walk on an unconnected client reported success")
	}
	if err := client.BulkWalk("1.3.6.1.2.1.1", noop); err == nil {
		t.Error("BulkWalk on an unconnected client reported success")
	}
	if _, err := client.WalkAll("1.3.6.1.2.1.1"); err == nil {
		t.Error("WalkAll on an unconnected client reported success")
	}
	if _, err := client.BulkWalkAll("1.3.6.1.2.1.1"); err == nil {
		t.Error("BulkWalkAll on an unconnected client reported success")
	}
}

func TestClientGetDeviceInfo(t *testing.T) {
	agent := newTestAgent(t, systemGroup()...)
	client := newAgentClient(t, agent)

	info, err := client.GetDeviceInfo()
	if err != nil {
		t.Fatalf("GetDeviceInfo: %v", err)
	}

	if info.Address != agent.addr() {
		t.Errorf("Address = %q, want %q", info.Address, agent.addr())
	}
	if !strings.Contains(info.Description, "Cisco IOS XE") {
		t.Errorf("Description = %q, want the agent's sysDescr", info.Description)
	}
	if info.Name != "edge-router-1" {
		t.Errorf("Name = %q, want %q", info.Name, "edge-router-1")
	}
	if info.Location != "rack 4" {
		t.Errorf("Location = %q, want %q", info.Location, "rack 4")
	}
	if info.Contact != "noc@example.test" {
		t.Errorf("Contact = %q, want %q", info.Contact, "noc@example.test")
	}
	// sysUpTime counts hundredths of a second, so 360000 ticks is one hour.
	if info.Uptime != time.Hour {
		t.Errorf("Uptime = %v, want %v", info.Uptime, time.Hour)
	}
}

// TestClientGetDeviceInfoWithMissingScalars covers a device that implements
// only part of the system group: the fields it does not answer must be left at
// their zero value rather than filled with the noSuchObject marker.
func TestClientGetDeviceInfoWithMissingScalars(t *testing.T) {
	agent := newTestAgent(t, scalar(sysDescrOID, "minimal agent"))
	client := newAgentClient(t, agent)

	info, err := client.GetDeviceInfo()
	if err != nil {
		t.Fatalf("GetDeviceInfo: %v", err)
	}
	if info.Description != "minimal agent" {
		t.Errorf("Description = %q, want %q", info.Description, "minimal agent")
	}
	if info.Name != "" || info.Location != "" || info.Contact != "" {
		t.Errorf("unimplemented scalars were populated: %+v", info)
	}
	if info.Uptime != 0 {
		t.Errorf("Uptime = %v, want 0 when the device does not answer sysUpTime", info.Uptime)
	}
}

func TestClientGetDeviceInfoWithoutConnection(t *testing.T) {
	client := newUnconnectedClient(t)

	if _, err := client.GetDeviceInfo(); err == nil {
		t.Fatal("GetDeviceInfo on an unconnected client reported success")
	}
}

func TestClientCloseBeforeConnect(t *testing.T) {
	client := newUnconnectedClient(t)

	if err := client.Close(); err != nil {
		t.Errorf("Close on a client that never connected returned %v, want nil", err)
	}
}

func TestClientCloseThenUse(t *testing.T) {
	agent := newTestAgent(t, systemGroup()...)
	client := newAgentClient(t, agent)

	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := client.Get(sysDescrOID); err == nil {
		t.Error("Get on a closed client reported success")
	}
}

// TestClientCloseDoesNotWaitForInFlightRequest guards the reason Close takes a
// read lock rather than a write lock. Closing the socket is how a request that
// will never be answered is interrupted; if Close waited for that request to
// finish it would block for the whole timeout budget, and one dead device would
// stall every control-path operation behind it.
func TestClientCloseDoesNotWaitForInFlightRequest(t *testing.T) {
	agent := newTestAgent(t, systemGroup()...)
	agent.silent.Store(true)

	const requestTimeout = 5 * time.Second
	client, err := NewClient(agentDevice(t, agent), requestTimeout, 0)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	inFlight := make(chan error, 1)
	go func() {
		_, err := client.Get(sysDescrOID)
		inFlight <- err
	}()

	// Wait until the agent has the request, so Close really does race an
	// operation that is already blocked on the socket.
	deadline := time.Now().Add(2 * time.Second)
	for agent.requests.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the request never reached the agent")
		}
		time.Sleep(time.Millisecond)
	}

	start := time.Now()
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if elapsed := time.Since(start); elapsed > requestTimeout/2 {
		t.Errorf("Close took %v; it waited for the in-flight request instead of interrupting it", elapsed)
	}

	select {
	case err := <-inFlight:
		if err == nil {
			t.Error("the interrupted request reported success")
		}
	case <-time.After(requestTimeout):
		t.Error("closing the socket did not interrupt the in-flight request")
	}
}
