package snmp

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
)

// walkerOver returns a walker that hands each pdu to the caller in order,
// pausing between them so that the timestamps a walk produces are
// distinguishable. It stops early and propagates the callback's error, the way
// gosnmp's own walk does.
func walkerOver(pdus []gosnmp.SnmpPDU, gap time.Duration) walker {
	return func(_ string, walkFn gosnmp.WalkFunc) error {
		for _, pdu := range pdus {
			if err := walkFn(pdu); err != nil {
				return err
			}
			time.Sleep(gap)
		}
		return nil
	}
}

func walkPDUs() []gosnmp.SnmpPDU {
	return []gosnmp.SnmpPDU{
		{Name: "1.3.6.1.2.1.2.2.1.10.1", Type: gosnmp.Counter32, Value: uint(10)},
		{Name: "1.3.6.1.2.1.2.2.1.10.2", Type: gosnmp.Counter32, Value: uint(20)},
		{Name: "1.3.6.1.2.1.2.2.1.10.3", Type: gosnmp.Counter32, Value: uint(30)},
	}
}

// TestStreamWalkStampsEachPDUOnArrival guards the deliberate difference between
// the streaming and collecting walks. A streaming caller consumes values as
// they arrive and wants each one stamped with when it arrived, because a walk
// over a large table spans a meaningful amount of time.
func TestStreamWalkStampsEachPDUOnArrival(t *testing.T) {
	c := &Client{}

	var stamps []time.Time
	err := c.streamWalk(walkerOver(walkPDUs(), 2*time.Millisecond), "WALK", "1.3.6.1.2.1.2.2.1.10",
		func(r *Result) error {
			stamps = append(stamps, r.Timestamp)
			return nil
		})
	if err != nil {
		t.Fatalf("streamWalk: %v", err)
	}

	if len(stamps) != len(walkPDUs()) {
		t.Fatalf("got %d results, want %d", len(stamps), len(walkPDUs()))
	}
	for i := 1; i < len(stamps); i++ {
		if !stamps[i].After(stamps[i-1]) {
			t.Errorf("result %d is stamped %v, not after result %d at %v — a streaming walk must stamp each PDU as it arrives",
				i, stamps[i], i-1, stamps[i-1])
		}
	}
}

// TestCollectWalkStampsOneTimeForTheWholeWalk is the other half of that
// contract. A caller that takes the whole table at once is taking one sample,
// so every value in it must share a single stamp or the rate calculations
// downstream compare values from different instants.
func TestCollectWalkStampsOneTimeForTheWholeWalk(t *testing.T) {
	c := &Client{}

	results, err := c.collectWalk(walkerOver(walkPDUs(), 2*time.Millisecond), "WALK", "1.3.6.1.2.1.2.2.1.10")
	if err != nil {
		t.Fatalf("collectWalk: %v", err)
	}

	if len(results) != len(walkPDUs()) {
		t.Fatalf("got %d results, want %d", len(results), len(walkPDUs()))
	}
	for i, r := range results[1:] {
		if !r.Timestamp.Equal(results[0].Timestamp) {
			t.Errorf("result %d is stamped %v, want the single walk time %v",
				i+1, r.Timestamp, results[0].Timestamp)
		}
	}
}

func TestCollectWalkCopiesEveryField(t *testing.T) {
	c := &Client{}

	results, err := c.collectWalk(walkerOver(walkPDUs(), 0), "WALK", "1.3.6.1.2.1.2.2.1.10")
	if err != nil {
		t.Fatalf("collectWalk: %v", err)
	}

	for i, want := range walkPDUs() {
		got := results[i]
		if got.OID != want.Name {
			t.Errorf("result %d OID = %q, want %q", i, got.OID, want.Name)
		}
		if got.Type != want.Type {
			t.Errorf("result %d Type = %v, want %v", i, got.Type, want.Type)
		}
		if got.Value != want.Value {
			t.Errorf("result %d Value = %v, want %v", i, got.Value, want.Value)
		}
	}
}

// TestCollectWalkReturnsNothingForAnEmptySubtree pins that walking a subtree
// the device does not implement is an empty result, not an error: callers
// distinguish "no such table" from "walk failed" by the error, and conflating
// them hides a broken device behind a plausible-looking empty table.
func TestCollectWalkReturnsNothingForAnEmptySubtree(t *testing.T) {
	c := &Client{}

	results, err := c.collectWalk(walkerOver(nil, 0), "WALK", "1.3.6.1.4.1.9999")
	if err != nil {
		t.Fatalf("collectWalk over an empty subtree: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("got %d results, want none", len(results))
	}
}

func TestStreamWalkWrapsWalkerFailure(t *testing.T) {
	c := &Client{}

	transport := errors.New("request timeout")
	failing := func(string, gosnmp.WalkFunc) error { return transport }

	err := c.streamWalk(failing, "BULKWALK", "1.3.6.1.2.1.2", func(*Result) error { return nil })
	if err == nil {
		t.Fatal("a failed walk reported success")
	}
	if !errors.Is(err, transport) {
		t.Errorf("error %v does not wrap the transport failure", err)
	}
	// The operation and the root OID are what make a walk failure actionable
	// in a log line that names neither the device nor the table otherwise.
	if !strings.Contains(err.Error(), "BULKWALK") || !strings.Contains(err.Error(), "1.3.6.1.2.1.2") {
		t.Errorf("error %q names neither the operation nor the root OID", err)
	}
}

func TestCollectWalkWrapsWalkerFailure(t *testing.T) {
	c := &Client{}

	transport := errors.New("connection refused")
	failing := func(string, gosnmp.WalkFunc) error { return transport }

	results, err := c.collectWalk(failing, "WALK", "1.3.6.1.2.1.2")
	if err == nil {
		t.Fatal("a failed walk reported success")
	}
	if results != nil {
		t.Errorf("a failed walk returned %d partial results; callers would treat them as a complete table", len(results))
	}
	if !errors.Is(err, transport) {
		t.Errorf("error %v does not wrap the transport failure", err)
	}
}

// TestStreamWalkStopsAtTheFirstCallbackError checks that a consumer which
// refuses a value ends the walk instead of being handed the rest of the table.
func TestStreamWalkStopsAtTheFirstCallbackError(t *testing.T) {
	c := &Client{}

	stop := errors.New("caller gave up")
	seen := 0

	err := c.streamWalk(walkerOver(walkPDUs(), 0), "WALK", "1.3.6.1.2.1.2.2.1.10",
		func(*Result) error {
			seen++
			return stop
		})
	if err == nil {
		t.Fatal("a walk whose callback failed reported success")
	}
	if !errors.Is(err, stop) {
		t.Errorf("error %v does not wrap the callback's error", err)
	}
	if seen != 1 {
		t.Errorf("the callback was invoked %d times, want 1 — the walk continued past a refused value", seen)
	}
}

func TestResultFromPDU(t *testing.T) {
	ts := time.Date(2024, time.March, 1, 12, 0, 0, 0, time.UTC)
	pdu := gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.1.5.0", Type: gosnmp.OctetString, Value: []byte("router")}

	got := resultFromPDU(pdu, ts)

	if got.OID != pdu.Name {
		t.Errorf("OID = %q, want %q", got.OID, pdu.Name)
	}
	if got.Type != gosnmp.OctetString {
		t.Errorf("Type = %v, want OctetString", got.Type)
	}
	if !got.Timestamp.Equal(ts) {
		t.Errorf("Timestamp = %v, want %v", got.Timestamp, ts)
	}
	value, err := got.GetStringValue()
	if err != nil || value != "router" {
		t.Errorf("GetStringValue() = %q, %v; want %q, nil", value, err, "router")
	}
}

// TestResultFromPDUCarriesUnsetValues covers the shape a device returns for an
// object it does not implement: the PDU has a type but no value, and the
// conversion must not invent one.
func TestResultFromPDUCarriesUnsetValues(t *testing.T) {
	got := resultFromPDU(gosnmp.SnmpPDU{Name: ".1.3.6.1.4.1.9999.1", Type: gosnmp.NoSuchInstance}, time.Time{})

	if got.Value != nil {
		t.Errorf("Value = %v, want nil", got.Value)
	}
	if got.TypeString() != "NoSuchInstance" {
		t.Errorf("TypeString() = %q, want %q", got.TypeString(), "NoSuchInstance")
	}
	if _, err := got.GetInt64Value(); err == nil {
		t.Error("a noSuchInstance result yielded an integer value")
	}
}
