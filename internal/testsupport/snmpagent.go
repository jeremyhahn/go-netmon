package testsupport

import (
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/gosnmp/gosnmp"
)

// This file provides a real SNMPv2c agent that runs inside the test process on
// a loopback UDP socket. Tests built on it exercise the whole client path — BER
// encoding, the socket, decoding and gosnmp's own walk state machine — instead
// of substituting a stand-in for the parts that actually break in production.
// Nothing leaves the machine and nothing on the host is touched.
//
// It lives here rather than beside one package's tests because more than one
// package needs it: the SNMP client, and the poller that drives the client.

// agentMaxDatagram bounds a single SNMP message. Real agents advertise a
// max-message-size in the same range.
const agentMaxDatagram = 8192

// SNMPAgent serves a fixed MIB over SNMPv2c on a loopback UDP socket.
//
// The zero value is not usable; construct one with NewSNMPAgent.
type SNMPAgent struct {
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

	// silentSubtree, when set, makes the agent consume without answering only
	// those requests that reach into one subtree, answering everything else
	// normally. That is the shape of a device which is up and talking but has
	// one branch of its MIB wedged -- the case that must never be mistaken for
	// an unreachable device.
	silentSubtree atomic.Pointer[string]

	// hollow makes the agent answer every request with a successful but empty
	// response. Some agents do this for objects they half-implement, and it is
	// the shape that turns an unguarded index into the varbind list into a
	// panic.
	hollow atomic.Bool

	// requests counts the datagrams the agent has accepted.
	requests atomic.Int64

	wg        sync.WaitGroup
	closeOnce sync.Once
}

// NewSNMPAgent starts an agent serving objects on a loopback UDP socket. The
// caller closes it, which is what stops the serving goroutine.
func NewSNMPAgent(objects ...gosnmp.SnmpPDU) (*SNMPAgent, error) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return nil, &SNMPAgentListenError{Err: err}
	}

	served := append([]gosnmp.SnmpPDU(nil), objects...)
	sort.Slice(served, func(i, j int) bool {
		return oidLess(served[i].Name, served[j].Name)
	})

	a := &SNMPAgent{conn: conn, mib: served}
	a.wg.Add(1)
	go a.serve()

	return a, nil
}

// Addr returns the host:port the agent is listening on, in the form a
// config.Device address takes.
func (a *SNMPAgent) Addr() string {
	return a.conn.LocalAddr().String()
}

// Close shuts the agent down and waits for its goroutine to exit, so the race
// detector sees a clean handoff rather than a leaked reader. It is safe to call
// more than once.
func (a *SNMPAgent) Close() error {
	var err error
	a.closeOnce.Do(func() {
		err = a.conn.Close()
		a.wg.Wait()
	})
	return err
}

// Requests reports how many datagrams the agent has accepted. A test asserting
// that polling stopped watches this stop moving.
func (a *SNMPAgent) Requests() int64 {
	return a.requests.Load()
}

// LimitResponseTo makes the agent answer tooBig for any request naming more
// than n varbinds. Zero removes the limit.
func (a *SNMPAgent) LimitResponseTo(n int) {
	a.maxVarbinds.Store(int64(n))
}

// AnswerEmpty makes the agent reply to every request with no varbinds.
func (a *SNMPAgent) AnswerEmpty() {
	a.hollow.Store(true)
}

// SetSilent controls whether the agent consumes requests without answering,
// which is how an unreachable device behaves from the client's point of view.
func (a *SNMPAgent) SetSilent(silent bool) {
	a.silent.Store(silent)
}

// SilenceSubtree makes the agent stop answering requests that reach into the
// subtree rooted at oid, while answering everything else as usual. Passing an
// empty OID restores the agent to answering everything.
//
// This is a partially answering device: reachable, talking, and timing out on
// one branch of its MIB.
func (a *SNMPAgent) SilenceSubtree(oid string) {
	if oid == "" {
		a.silentSubtree.Store(nil)
		return
	}
	root := normalizeOID(oid)
	a.silentSubtree.Store(&root)
}

// silencedBy reports whether a request reaches into the silenced subtree.
func (a *SNMPAgent) silencedBy(request *gosnmp.SnmpPacket) bool {
	root := a.silentSubtree.Load()
	if root == nil {
		return false
	}
	for _, variable := range request.Variables {
		name := normalizeOID(variable.Name)
		if name == *root || strings.HasPrefix(name, *root+".") {
			return true
		}
	}
	return false
}

func (a *SNMPAgent) serve() {
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

		// Decoding uses gosnmp itself, so the agent reads exactly the bytes the
		// client wrote rather than a test-only approximation of them.
		decoder := &gosnmp.GoSNMP{Version: gosnmp.Version2c}
		request, err := decoder.SnmpDecodePacket(buf[:n])
		if err != nil {
			continue // a malformed datagram gets no reply, as on a real agent
		}

		if a.silencedBy(request) {
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
func (a *SNMPAgent) respond(request *gosnmp.SnmpPacket) *gosnmp.SnmpPacket {
	// The reply speaks whatever the request spoke. A client checks the version
	// on the response, so answering a v1 request in v2c is not a detail — it
	// makes the agent unusable for testing v1 at all.
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
func (a *SNMPAgent) get(requested []gosnmp.SnmpPDU) []gosnmp.SnmpPDU {
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
func (a *SNMPAgent) getNext(requested []gosnmp.SnmpPDU) []gosnmp.SnmpPDU {
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
func (a *SNMPAgent) getBulk(request *gosnmp.SnmpPacket) []gosnmp.SnmpPDU {
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
func (a *SNMPAgent) successor(oid string) gosnmp.SnmpPDU {
	target := normalizeOID(oid)
	for _, entry := range a.mib {
		if oidLess(target, entry.Name) {
			return entry
		}
	}
	return gosnmp.SnmpPDU{Name: target, Type: gosnmp.EndOfMibView}
}

// lookup finds the object named oid exactly.
func (a *SNMPAgent) lookup(oid string) (gosnmp.SnmpPDU, bool) {
	target := normalizeOID(oid)
	for _, entry := range a.mib {
		if normalizeOID(entry.Name) == target {
			return entry, true
		}
	}
	return gosnmp.SnmpPDU{}, false
}

// OctetString builds an OctetString object, the shape of sysDescr and friends.
func OctetString(oid, value string) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Name: oid, Type: gosnmp.OctetString, Value: []byte(value)}
}

// Counter32 builds a Counter32 object, the shape of the ifTable columns.
func Counter32(oid string, value uint) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Name: oid, Type: gosnmp.Counter32, Value: value}
}

// Integer builds an Integer32 object, the shape of ifIndex and ifNumber.
func Integer(oid string, value int) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Name: oid, Type: gosnmp.Integer, Value: value}
}

// TimeTicks builds a TimeTicks object, the shape of sysUpTime.
func TimeTicks(oid string, value uint32) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Name: oid, Type: gosnmp.TimeTicks, Value: value}
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
