package snmp

import (
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/testsupport"
)

// This file provides a real SNMPv2c agent that runs inside the test process on
// a loopback UDP socket. Tests built on it exercise the whole client path —
// BER encoding, the socket, decoding and gosnmp's own walk state machine —
// instead of substituting a stand-in for the parts that actually broke in
// production. Nothing leaves the machine and nothing on the host is touched.

// agentMaxDatagram bounds a single SNMP message. Real agents advertise a
// max-message-size in the same range.
const agentMaxDatagram = 8192

// testAgent serves a fixed MIB over SNMPv2c.
//
// The zero value is not usable; construct one with newTestAgent.
type testAgent struct {
	conn *net.UDPConn

	// mib is the served subtree, kept sorted by numeric OID so GETNEXT and
	// GETBULK can find a successor the way a real agent does.
	mib []gosnmp.SnmpPDU

	// maxVarbinds is the largest number of varbinds the agent will answer in
	// one response. A request naming more is answered with an empty response
	// carrying the tooBig status, which is what a Cisco ASR1000 does and what
	// gosnmp reports back to the caller as a *successful* call. Zero means no
	// limit. It is atomic because the serving goroutine reads it while the
	// test sets it.
	maxVarbinds atomic.Int64

	// silent makes the agent consume requests without answering, which is how
	// an unreachable device behaves from the client's point of view.
	silent atomic.Bool

	// hollow makes the agent answer every request with a successful but empty
	// response. Some agents do this for objects they half-implement, and it is
	// the shape that turns an unguarded index into the varbind list into a
	// panic.
	hollow atomic.Bool

	// requests counts the datagrams the agent has accepted.
	requests atomic.Int64

	// community, when set, is the only community the agent answers; any
	// other is dropped unanswered, as a real agent does.
	community string

	// usm, when set, makes this an SNMPv3 agent with this one user: it
	// answers engine discovery and only requests that authenticate and
	// decrypt under the user's keys (see agent_v3_test.go).
	usm *testsupport.USMEngine

	// rejected counts the requests dropped for bad credentials.
	rejected atomic.Int64

	wg sync.WaitGroup
}

// newTestAgent starts an agent serving pdus and stops it when the test ends.
func newTestAgent(t *testing.T, pdus ...gosnmp.SnmpPDU) *testAgent {
	t.Helper()
	return newConfiguredAgent(t, func(*testAgent) {}, pdus...)
}

// newConfiguredAgent is newTestAgent with configure applied before the agent
// starts serving, for the settings the serving goroutine reads unguarded.
func newConfiguredAgent(t *testing.T, configure func(*testAgent), pdus ...gosnmp.SnmpPDU) *testAgent {
	t.Helper()

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen on loopback: %v", err)
	}

	mib := append([]gosnmp.SnmpPDU(nil), pdus...)
	sort.Slice(mib, func(i, j int) bool {
		return oidLess(mib[i].Name, mib[j].Name)
	})

	a := &testAgent{conn: conn, mib: mib}
	configure(a)
	a.wg.Add(1)
	go a.serve()

	t.Cleanup(a.stop)
	return a
}

// addr returns the host:port the agent is listening on.
func (a *testAgent) addr() string {
	return a.conn.LocalAddr().String()
}

// stop shuts the agent down and waits for its goroutine to exit, so the race
// detector sees a clean handoff rather than a leaked reader.
func (a *testAgent) stop() {
	_ = a.conn.Close()
	a.wg.Wait()
}

func (a *testAgent) serve() {
	defer a.wg.Done()

	buf := make([]byte, agentMaxDatagram)
	for {
		n, from, err := a.conn.ReadFromUDP(buf)
		if err != nil {
			return // the socket is closed; the agent is done
		}
		a.requests.Add(1)

		if a.silent.Load() {
			continue
		}

		if a.usm != nil {
			if wire, ok := a.serveV3(buf[:n]); ok {
				if _, werr := a.conn.WriteToUDP(wire, from); werr != nil {
					return
				}
			}
			continue
		}

		// Decoding uses gosnmp itself, so the agent reads exactly the bytes the
		// client wrote rather than a test-only approximation of them.
		decoder := &gosnmp.GoSNMP{Version: gosnmp.Version2c}
		request, err := decoder.SnmpDecodePacket(buf[:n])
		if err != nil {
			continue // a malformed datagram gets no reply, as on a real agent
		}
		if a.community != "" && request.Community != a.community {
			a.rejected.Add(1)
			continue
		}

		wire, err := a.respond(request).MarshalMsg()
		if err != nil {
			continue
		}
		if _, err := a.conn.WriteToUDP(wire, from); err != nil {
			return
		}
	}
}

// respond builds the reply to one decoded request.
func (a *testAgent) respond(request *gosnmp.SnmpPacket) *gosnmp.SnmpPacket {
	// The reply speaks whatever the request spoke: a client validates the
	// version it gets back, so answering a v1 request in v2c would make this
	// agent unusable for testing v1.
	response := &gosnmp.SnmpPacket{
		Version:   request.Version,
		Community: request.Community,
		PDUType:   gosnmp.GetResponse,
		RequestID: request.RequestID,
	}

	if limit := a.maxVarbinds.Load(); limit > 0 && int64(len(request.Variables)) > limit {
		// tooBig carries no varbinds. gosnmp hands this to the caller with a
		// nil error, so only the status distinguishes it from an empty table.
		response.Error = gosnmp.TooBig
		return response
	}

	if a.hollow.Load() {
		return response
	}

	switch request.PDUType {
	case gosnmp.GetRequest:
		response.Variables = a.get(request.Variables)
	case gosnmp.GetNextRequest:
		response.Variables = a.getNext(request.Variables)
	case gosnmp.GetBulkRequest:
		response.Variables = a.getBulk(request)
	default:
		response.Error = gosnmp.GenErr
	}

	return response
}

// get answers a GETREQUEST: an exact-match lookup per varbind, reporting
// noSuchObject for names the agent does not serve, as SNMPv2c requires.
func (a *testAgent) get(requested []gosnmp.SnmpPDU) []gosnmp.SnmpPDU {
	out := make([]gosnmp.SnmpPDU, 0, len(requested))
	for _, want := range requested {
		if found, ok := a.lookup(want.Name); ok {
			out = append(out, found)
			continue
		}
		out = append(out, gosnmp.SnmpPDU{
			Name: normalizeOID(want.Name),
			Type: gosnmp.NoSuchObject,
		})
	}
	return out
}

// getNext answers a GETNEXTREQUEST, which is what gosnmp's Walk drives.
func (a *testAgent) getNext(requested []gosnmp.SnmpPDU) []gosnmp.SnmpPDU {
	out := make([]gosnmp.SnmpPDU, 0, len(requested))
	for _, want := range requested {
		out = append(out, a.successor(want.Name))
	}
	return out
}

// getBulk answers a GETBULKREQUEST, which is what gosnmp's BulkWalk drives.
// The first NonRepeaters varbinds get one successor each; every remaining
// varbind gets up to MaxRepetitions successors, interleaved as RFC 3416
// specifies.
func (a *testAgent) getBulk(request *gosnmp.SnmpPacket) []gosnmp.SnmpPDU {
	nonRepeaters := int(request.NonRepeaters)
	if nonRepeaters > len(request.Variables) {
		nonRepeaters = len(request.Variables)
	}

	var out []gosnmp.SnmpPDU

	cursors := make([]string, 0, len(request.Variables))
	for i, want := range request.Variables {
		if i < nonRepeaters {
			out = append(out, a.successor(want.Name))
			continue
		}
		cursors = append(cursors, want.Name)
	}

	for r := uint32(0); r < request.MaxRepetitions; r++ {
		for i, cursor := range cursors {
			next := a.successor(cursor)
			out = append(out, next)
			cursors[i] = next.Name
		}
	}

	return out
}

// successor returns the first served object after oid, or an endOfMibView
// marker once the walk has run off the end of the subtree.
func (a *testAgent) successor(oid string) gosnmp.SnmpPDU {
	target := normalizeOID(oid)
	for _, entry := range a.mib {
		if oidLess(target, entry.Name) {
			return entry
		}
	}
	return gosnmp.SnmpPDU{Name: target, Type: gosnmp.EndOfMibView}
}

// lookup finds the object named oid exactly.
func (a *testAgent) lookup(oid string) (gosnmp.SnmpPDU, bool) {
	target := normalizeOID(oid)
	for _, entry := range a.mib {
		if normalizeOID(entry.Name) == target {
			return entry, true
		}
	}
	return gosnmp.SnmpPDU{}, false
}

// normalizeOID strips the leading dot gosnmp puts on decoded names so that
// dotted and undotted forms of the same OID compare equal.
func normalizeOID(oid string) string {
	return strings.TrimPrefix(oid, ".")
}

// oidLess reports whether a sorts before b in SNMP lexicographic order, which
// compares sub-identifiers numerically rather than as text: 1.3.6.1.2.1.1.10
// follows 1.3.6.1.2.1.1.9, where a string comparison would put it first.
func oidLess(a, b string) bool {
	as := strings.Split(normalizeOID(a), ".")
	bs := strings.Split(normalizeOID(b), ".")

	for i := 0; i < len(as) && i < len(bs); i++ {
		av, aErr := strconv.Atoi(as[i])
		bv, bErr := strconv.Atoi(bs[i])
		if aErr != nil || bErr != nil {
			if as[i] != bs[i] {
				return as[i] < bs[i]
			}
			continue
		}
		if av != bv {
			return av < bv
		}
	}
	return len(as) < len(bs)
}

// agentDevice describes a device pointing at the agent.
func agentDevice(t *testing.T, a *testAgent) *config.Device {
	t.Helper()
	return agentDeviceVersion(t, a, "2c")
}

// agentDeviceVersion is agentDevice for a stated SNMP version.
func agentDeviceVersion(t *testing.T, a *testAgent, version string) *config.Device {
	t.Helper()
	return &config.Device{
		Name:        "agent-" + t.Name(),
		Address:     a.addr(),
		SNMPVersion: version,
		Community:   "public",
		Enabled:     true,
	}
}

// agentTimeout is short on purpose: the agent is one hop away over loopback,
// so a test that has to wait for a timeout waits milliseconds, not seconds.
const agentTimeout = 250 * time.Millisecond

// newAgentClient returns a connected client talking to a.
func newAgentClient(t *testing.T, a *testAgent) *Client {
	t.Helper()

	client, err := NewClient(agentDevice(t, a), agentTimeout, 0)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect to %s: %v", a.addr(), err)
	}
	t.Cleanup(func() { _ = client.Close() })

	return client
}

// limitResponseTo makes the agent answer tooBig for any request naming more
// than n varbinds.
func (a *testAgent) limitResponseTo(n int) {
	a.maxVarbinds.Store(int64(n))
}

// answerEmpty makes the agent reply to every request with no varbinds.
func (a *testAgent) answerEmpty() {
	a.hollow.Store(true)
}

// scalar builds an OctetString object, the shape of sysDescr and friends.
func scalar(oid, value string) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Name: oid, Type: gosnmp.OctetString, Value: []byte(value)}
}

// counter builds a Counter32 object, the shape of the ifTable columns.
func counter(oid string, value uint) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Name: oid, Type: gosnmp.Counter32, Value: value}
}
