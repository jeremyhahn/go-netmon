package snmp

import (
	"errors"
	"fmt"
	"testing"

	"github.com/gosnmp/gosnmp"
)

// fakeAgent answers GETs the way a real one does: it refuses any request with
// more than maxVarbinds by returning an EMPTY response carrying the tooBig
// status, which gosnmp reports as a successful call.
type fakeAgent struct {
	maxVarbinds int
	requests    [][]string
	err         error
}

func (a *fakeAgent) get(oids []string) (*gosnmp.SnmpPacket, error) {
	a.requests = append(a.requests, append([]string(nil), oids...))

	if a.err != nil {
		return nil, a.err
	}
	if len(oids) > a.maxVarbinds {
		return &gosnmp.SnmpPacket{Error: gosnmp.TooBig}, nil
	}

	vars := make([]gosnmp.SnmpPDU, 0, len(oids))
	for i, oid := range oids {
		vars = append(vars, gosnmp.SnmpPDU{
			Name: oid, Type: gosnmp.Integer, Value: i,
		})
	}
	return &gosnmp.SnmpPacket{Error: gosnmp.NoError, Variables: vars}, nil
}

// testOIDs builds n distinct OIDs.
func testOIDs(n int) []string {
	oids := make([]string, n)
	for i := range oids {
		oids[i] = fmt.Sprintf("1.3.6.1.2.1.1.%d.0", i+1)
	}
	return oids
}

// TestGetWithSplitRecoversFromTooBig is the regression this exists for. A Cisco
// ASR1000 answers tooBig past roughly 30 varbinds. Because gosnmp reports that
// as a successful call with no variables, a caller that only checks the Go
// error silently collects nothing — which is exactly how two routers reported
// healthy polls while contributing zero scalar metrics.
func TestGetWithSplitRecoversFromTooBig(t *testing.T) {
	agent := &fakeAgent{maxVarbinds: 30}
	oids := testOIDs(78)

	results, err := getWithSplit(agent.get, oids)
	if err != nil {
		t.Fatalf("getWithSplit: %v", err)
	}

	if len(results) != len(oids) {
		t.Fatalf("got %d results, want %d — every OID must survive the split", len(results), len(oids))
	}

	// Every OID must appear exactly once, in order.
	for i, r := range results {
		if r.OID != oids[i] {
			t.Errorf("result %d has OID %s, want %s", i, r.OID, oids[i])
		}
	}

	// The agent must never have been asked for more than it can answer on the
	// requests that actually succeeded.
	if len(agent.requests) < 2 {
		t.Errorf("expected the request to be split, got %d requests", len(agent.requests))
	}
}

func TestGetWithSplitPassesThroughWhenItFits(t *testing.T) {
	agent := &fakeAgent{maxVarbinds: 100}
	oids := testOIDs(25)

	results, err := getWithSplit(agent.get, oids)
	if err != nil {
		t.Fatalf("getWithSplit: %v", err)
	}
	if len(results) != 25 {
		t.Errorf("got %d results, want 25", len(results))
	}
	// A request that fits must cost exactly one round trip.
	if len(agent.requests) != 1 {
		t.Errorf("made %d requests, want 1 when the batch already fits", len(agent.requests))
	}
}

func TestGetWithSplitVeryRestrictiveAgent(t *testing.T) {
	// An agent that will only answer one varbind at a time still has to yield
	// every value, just at the cost of one request each.
	agent := &fakeAgent{maxVarbinds: 1}
	oids := testOIDs(8)

	results, err := getWithSplit(agent.get, oids)
	if err != nil {
		t.Fatalf("getWithSplit: %v", err)
	}
	if len(results) != 8 {
		t.Errorf("got %d results, want 8", len(results))
	}
}

func TestGetWithSplitSingleOIDStillTooBig(t *testing.T) {
	// Splitting cannot help an agent that rejects even one varbind, so this
	// must surface as an error rather than as an empty success.
	agent := &fakeAgent{maxVarbinds: 0}

	_, err := getWithSplit(agent.get, []string{"1.3.6.1.2.1.1.1.0"})
	if err == nil {
		t.Fatal("expected an error when a single OID is still too big")
	}

	var tooBig *TooBigError
	if !errors.As(err, &tooBig) {
		t.Fatalf("got %T, want *TooBigError", err)
	}
	if tooBig.OID != "1.3.6.1.2.1.1.1.0" {
		t.Errorf("OID = %q, want the offending OID", tooBig.OID)
	}
}

func TestGetWithSplitTransportError(t *testing.T) {
	agent := &fakeAgent{maxVarbinds: 100, err: errors.New("request timeout")}

	_, err := getWithSplit(agent.get, testOIDs(3))
	if err == nil {
		t.Fatal("expected an error")
	}

	var getErr *GetError
	if !errors.As(err, &getErr) {
		t.Fatalf("got %T, want *GetError", err)
	}
	if !errors.Is(err, agent.err) {
		t.Error("the transport error should be unwrappable")
	}
}

// TestGetWithSplitOtherErrorStatuses verifies that any non-zero error status is
// reported rather than silently returning an empty result set.
func TestGetWithSplitOtherErrorStatuses(t *testing.T) {
	statuses := []gosnmp.SNMPError{
		gosnmp.NoSuchName,
		gosnmp.GenErr,
		gosnmp.ReadOnly,
	}

	for _, status := range statuses {
		t.Run(status.String(), func(t *testing.T) {
			get := func(oids []string) (*gosnmp.SnmpPacket, error) {
				return &gosnmp.SnmpPacket{Error: status}, nil
			}

			_, err := getWithSplit(get, testOIDs(4))
			if err == nil {
				t.Fatalf("status %v was reported as success", status)
			}

			var respErr *ResponseError
			if !errors.As(err, &respErr) {
				t.Fatalf("got %T, want *ResponseError", err)
			}
			if respErr.Status != status {
				t.Errorf("Status = %v, want %v", respErr.Status, status)
			}
		})
	}
}

func TestGetMultipleRejectsEmptyInput(t *testing.T) {
	c := &Client{}

	_, err := c.GetMultiple(nil)
	if err == nil {
		t.Fatal("expected an error for an empty OID list")
	}

	var noOIDs *NoOIDsError
	if !errors.As(err, &noOIDs) {
		t.Errorf("got %T, want *NoOIDsError", err)
	}
}
