package snmp

import (
	"errors"
	"strings"
	"testing"

	"github.com/gosnmp/gosnmp"

	"github.com/jeremyhahn/go-netmon/internal/config"
)

// SNMPv1 is what a good deal of embedded equipment offers and some of it offers
// nothing else — the KVM this was written for answers v1 and v3 and not v2c.
// Until this, a v1 device was refused at connect time with "unsupported SNMP
// version", so it could be configured in the web UI and never polled.

// TestV1DeviceConnectsAndGets is the plain case: a v1 device answers a GET.
func TestV1DeviceConnectsAndGets(t *testing.T) {
	agent := newTestAgent(t,
		gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.1.1.0", Type: gosnmp.OctetString, Value: []byte("ATEN KVM")},
	)

	client := connectedClient(t, agent, "1")

	result, err := client.Get("1.3.6.1.2.1.1.1.0")
	if err != nil {
		t.Fatalf("GET over v1: %v", err)
	}
	value, ok := result.Value.([]byte)
	if !ok {
		t.Fatalf("sysDescr came back as %T, want an octet string", result.Value)
	}
	if string(value) != "ATEN KVM" {
		t.Errorf("sysDescr = %q, want %q", value, "ATEN KVM")
	}
}

// TestV1BulkWalkFallsBackToWalk is the reason v1 needs more than a case in a
// switch.
//
// There is no GETBULK in SNMPv1 — gosnmp refuses it outright with "GETBULK not
// supported in SNMPv1" — and the poller walks every table with BulkWalkAll. A
// v1 device would therefore have failed every table with an error about a
// protocol feature rather than about anything the operator could act on.
func TestV1BulkWalkFallsBackToWalk(t *testing.T) {
	agent := newTestAgent(t,
		gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.2.2.1.2.1", Type: gosnmp.OctetString, Value: []byte("eth0")},
		gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.2.2.1.2.2", Type: gosnmp.OctetString, Value: []byte("eth1")},
		gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.2.2.1.2.3", Type: gosnmp.OctetString, Value: []byte("eth2")},
	)

	client := connectedClient(t, agent, "1")

	results, err := client.BulkWalkAll("1.3.6.1.2.1.2.2.1.2")
	if err != nil {
		t.Fatalf("BulkWalkAll over v1: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("walked %d rows, want 3", len(results))
	}

	// The streaming form has to fall back too; they are separate call sites.
	var streamed int
	if err := client.BulkWalk("1.3.6.1.2.1.2.2.1.2", func(*Result) error {
		streamed++
		return nil
	}); err != nil {
		t.Fatalf("BulkWalk over v1: %v", err)
	}
	if streamed != 3 {
		t.Errorf("streamed %d rows, want 3", streamed)
	}
}

// TestV2cStillUsesGetBulk guards the fallback from becoming the default. The
// bulk path exists because walking a large table one variable at a time is slow,
// and a v2c device must keep using it.
func TestV2cStillUsesGetBulk(t *testing.T) {
	agent := newTestAgent(t,
		gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.2.2.1.2.1", Type: gosnmp.OctetString, Value: []byte("eth0")},
		gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.2.2.1.2.2", Type: gosnmp.OctetString, Value: []byte("eth1")},
	)

	client := connectedClient(t, agent, "2c")

	if _, op := client.bulkWalker(); op != "BULKWALK" {
		t.Errorf("a v2c client walks with %q, want BULKWALK", op)
	}
	if _, err := client.BulkWalkAll("1.3.6.1.2.1.2.2.1.2"); err != nil {
		t.Fatalf("BulkWalkAll over v2c: %v", err)
	}
}

// TestV1ChoosesTheWalkOperation pins the choice itself, so a refactor that
// stops consulting the version fails here rather than in production against
// the one device that cannot answer GETBULK.
func TestV1ChoosesTheWalkOperation(t *testing.T) {
	agent := newTestAgent(t,
		gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.1.1.0", Type: gosnmp.OctetString, Value: []byte("x")},
	)

	client := connectedClient(t, agent, "1")

	if _, op := client.bulkWalker(); op != "WALK" {
		t.Errorf("a v1 client walks with %q, want WALK: there is no GETBULK in v1", op)
	}
}

// TestUnsupportedVersionIsRefused covers the bad-input case.
//
// The version is checked twice: config validation refuses it before a socket is
// opened, and the client's own switch has a default that refuses it again. The
// second is unreachable while the two agree on the set, which is the point of
// having it — they are in different packages and can drift.
//
// A caller sees the first, so that is what is asserted here, including that the
// message names the set. "unsupported SNMP version" without saying what is
// supported is the message that sends someone to the source.
func TestUnsupportedVersionIsRefused(t *testing.T) {
	for _, version := range []string{"", "2", "v1", "4", "nonsense"} {
		_, err := NewClient(&config.Device{
			Name: "bad", Address: "127.0.0.1:161", SNMPVersion: version, Community: "public",
		}, agentTimeout, 0)

		if err == nil {
			t.Errorf("version %q was accepted", version)
			continue
		}

		var invalid *config.ValidationError
		if !errors.As(err, &invalid) {
			t.Errorf("version %q gave %T (%v), want a *config.ValidationError", version, err, err)
			continue
		}
		if !strings.Contains(err.Error(), "'1'") {
			t.Errorf("the refusal of %q does not say v1 is supported: %v", version, err)
		}
	}
}

// TestUnsupportedVersionErrorReads pins the defensive error's own wording, since
// nothing else reaches it.
func TestUnsupportedVersionErrorReads(t *testing.T) {
	err := &UnsupportedVersionError{Version: "4"}

	for _, want := range []string{`"4"`, `"1"`, `"2c"`, `"3"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

// connectedClient is newAgentClient for a stated SNMP version.
func connectedClient(t *testing.T, a *testAgent, version string) *Client {
	t.Helper()

	client, err := NewClient(agentDeviceVersion(t, a, version), agentTimeout, 0)
	if err != nil {
		t.Fatalf("NewClient at v%s: %v", version, err)
	}
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect at v%s: %v", version, err)
	}
	t.Cleanup(func() { _ = client.Close() })

	return client
}
