package snmp

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/gosnmp/gosnmp"

	"github.com/jeremyhahn/go-netmon/internal/config"
)

// TestNewClientRejectsUnparseablePort covers the address the port parser
// cannot make sense of. Without this the client would be built with port 0 and
// fail later, at poll time, with an error that names neither the device nor the
// reason.
func TestNewClientRejectsUnparseablePort(t *testing.T) {
	for _, address := range []string{"127.0.0.1:snmp", "127.0.0.1:70000", "127.0.0.1:-1"} {
		t.Run(address, func(t *testing.T) {
			_, err := NewClient(&config.Device{
				Name:        "bad-port",
				Address:     address,
				SNMPVersion: "2c",
				Community:   "public",
			}, agentTimeout, 0)

			if err == nil {
				t.Fatalf("NewClient accepted address %q", address)
			}
			if !strings.Contains(err.Error(), address) {
				t.Errorf("error %q does not name the offending address", err)
			}
		})
	}
}

// TestNewClientDefaultsToPort161 pins the fallback for an address given as a
// bare host, which is how devices are usually configured.
func TestNewClientDefaultsToPort161(t *testing.T) {
	client, err := NewClient(&config.Device{
		Name:        "bare-host",
		Address:     "198.51.100.101",
		SNMPVersion: "2c",
		Community:   "public",
	}, agentTimeout, 0)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if client.conn.Target != "198.51.100.101" {
		t.Errorf("Target = %q, want %q", client.conn.Target, "198.51.100.101")
	}
	if client.conn.Port != 161 {
		t.Errorf("Port = %d, want the SNMP default 161", client.conn.Port)
	}
}

// TestClientGetRejectsEmptyResponse covers an agent that answers a GET with a
// successful but varbind-free response. The result must be an error: indexing
// the first variable unguarded would panic the poller on a device that merely
// half-implements an object.
func TestClientGetRejectsEmptyResponse(t *testing.T) {
	agent := newTestAgent(t, systemGroup()...)
	agent.answerEmpty()
	client := newAgentClient(t, agent)

	_, err := client.Get(sysDescrOID)
	if err == nil {
		t.Fatal("an empty response was reported as a successful Get")
	}
	if !strings.Contains(err.Error(), sysDescrOID) {
		t.Errorf("error %q does not name the OID that produced nothing", err)
	}
}

// TestGetWithSplitPropagatesFailureFromTheSecondHalf covers the arm of the
// recursion that the first-half tests do not reach: the batch splits, the first
// half succeeds, and the second half fails. Returning the first half's values
// with a nil error would report a partial poll as a complete one.
func TestGetWithSplitPropagatesFailureFromTheSecondHalf(t *testing.T) {
	transport := errors.New("request timeout")
	oids := testOIDs(4)

	get := func(requested []string) (*gosnmp.SnmpPacket, error) {
		switch {
		case len(requested) == len(oids):
			return &gosnmp.SnmpPacket{Error: gosnmp.TooBig}, nil
		case requested[0] == oids[0]:
			// The first half answers normally.
			vars := make([]gosnmp.SnmpPDU, 0, len(requested))
			for _, oid := range requested {
				vars = append(vars, gosnmp.SnmpPDU{Name: oid, Type: gosnmp.Integer, Value: 1})
			}
			return &gosnmp.SnmpPacket{Error: gosnmp.NoError, Variables: vars}, nil
		default:
			return nil, transport
		}
	}

	results, err := getWithSplit(get, oids)
	if err == nil {
		t.Fatal("a failure in the second half of a split batch was reported as success")
	}
	if results != nil {
		t.Errorf("got %d partial results; a partial batch must not be returned as a whole one", len(results))
	}
	if !errors.Is(err, transport) {
		t.Errorf("error %v does not wrap the transport failure", err)
	}
}

// TestResultIntegerConversionBoundaries covers the narrow SNMP integer widths
// and the two guards that stop a wrapped value from being reported as a
// plausible metric. gosnmp yields uint for Counter32/Gauge32 and uint64 for
// Counter64, and a Counter64 above 2^63 is a value real interfaces reach.
func TestResultIntegerConversionBoundaries(t *testing.T) {
	t.Run("int64 from narrow widths", func(t *testing.T) {
		cases := map[string]interface{}{
			"int8":   int8(-8),
			"int16":  int16(-16),
			"uint8":  uint8(8),
			"uint16": uint16(16),
		}
		want := map[string]int64{"int8": -8, "int16": -16, "uint8": 8, "uint16": 16}

		for name, value := range cases {
			got, err := (&Result{Value: value}).GetInt64Value()
			if err != nil {
				t.Errorf("%s: GetInt64Value: %v", name, err)
				continue
			}
			if got != want[name] {
				t.Errorf("%s: GetInt64Value() = %d, want %d", name, got, want[name])
			}
		}
	})

	t.Run("uint above int64 max", func(t *testing.T) {
		// uint is 64 bits on every platform this runs on, so a Counter64 past
		// 2^63 arrives here and must not wrap to a negative rate.
		_, err := (&Result{Value: uint(math.MaxInt64) + 1}).GetInt64Value()
		if err == nil {
			t.Fatal("a uint above int64 max was converted instead of rejected")
		}
		if !strings.Contains(err.Error(), "int64") {
			t.Errorf("error %q does not say the value does not fit an int64", err)
		}
	})

	t.Run("uint at int64 max", func(t *testing.T) {
		got, err := (&Result{Value: uint(math.MaxInt64)}).GetInt64Value()
		if err != nil {
			t.Fatalf("GetInt64Value at the boundary: %v", err)
		}
		if got != math.MaxInt64 {
			t.Errorf("GetInt64Value() = %d, want %d", got, int64(math.MaxInt64))
		}
	})

	t.Run("uint64 from negative narrow widths", func(t *testing.T) {
		for name, value := range map[string]interface{}{
			"int8":  int8(-1),
			"int16": int16(-1),
			"int32": int32(-1),
		} {
			if _, err := (&Result{Value: value}).GetUint64Value(); err == nil {
				t.Errorf("%s: a negative value was converted to uint64 instead of rejected", name)
			}
		}
	})

	t.Run("uint64 from narrow widths", func(t *testing.T) {
		for name, value := range map[string]interface{}{
			"int8":  int8(7),
			"int16": int16(7),
			"int32": int32(7),
			"uint8": uint8(7),
		} {
			got, err := (&Result{Value: value}).GetUint64Value()
			if err != nil {
				t.Errorf("%s: GetUint64Value: %v", name, err)
				continue
			}
			if got != 7 {
				t.Errorf("%s: GetUint64Value() = %d, want 7", name, got)
			}
		}
	})
}

// TestResultGetFloat64ValueRejectsNonNumeric checks that the float conversion
// inherits the integer conversion's guards rather than yielding a zero that
// would be graphed as a real reading.
func TestResultGetFloat64ValueRejectsNonNumeric(t *testing.T) {
	if _, err := (&Result{Value: []byte("up")}).GetFloat64Value(); err == nil {
		t.Error("an octet string was converted to a float instead of rejected")
	}
	if _, err := (&Result{Value: nil}).GetFloat64Value(); err == nil {
		t.Error("an absent value was converted to a float instead of rejected")
	}
}
