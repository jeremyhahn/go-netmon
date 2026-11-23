package snmp

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/gosnmp/gosnmp"
)

// gosnmpTimeout is the error gosnmp produces when a device never answers. It is
// a plain fmt.Errorf with the deadline error thrown away, which is why its text
// has to be consulted at all.
func gosnmpTimeout() error {
	return errors.New("request timeout (after 3 retries)")
}

// refused is the error a UDP socket produces when the host answers ICMP
// port-unreachable: nothing is listening on the SNMP port.
func refused() error {
	return &net.OpError{
		Op:  "write",
		Net: "udp",
		Err: &os.SyscallError{Syscall: "sendto", Err: syscall.ECONNREFUSED},
	}
}

// timeoutError is a net.Error that reports a timeout without being any of the
// standard deadline errors, which is what a transport other than gosnmp's own
// would hand back.
type timeoutError struct{}

func (*timeoutError) Error() string   { return "i/o timed out" }
func (*timeoutError) Timeout() bool   { return true }
func (*timeoutError) Temporary() bool { return true }

func TestTimedOut(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"gosnmp's exhausted retry sequence", gosnmpTimeout(), true},
		{"wrapped in the GET that produced it", &GetError{Count: 25, Err: gosnmpTimeout()}, true},
		{"a walk that never got a varbind", &WalkError{Op: "BULKWALK", OID: "1.3.6.1.2.1.2.2", Err: gosnmpTimeout()}, true},
		{"a context deadline", context.DeadlineExceeded, true},
		{"a socket deadline", os.ErrDeadlineExceeded, true},
		{"a net.Error that reports a timeout", &net.OpError{Op: "read", Err: os.ErrDeadlineExceeded}, true},
		{"a net.Error of some other shape that still says it timed out", &timeoutError{}, true},

		{"no error at all", nil, false},
		{"the agent refused the request", &ResponseError{Status: gosnmp.NoSuchName, Count: 4}, false},
		{"the connection was refused", refused(), false},
		{"a configuration mistake", errors.New("unsupported SNMP version: 4"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TimedOut(tt.err); got != tt.want {
				t.Errorf("TimedOut(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestUnanswered is the distinction the poller stops polling a device on, so
// every case that came *from* the device has to be on the false side of it: a
// refusal, a response limit and a table that broke off part-way are all a
// device that is there and talking.
func TestUnanswered(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"silence", &GetError{Count: 25, Err: gosnmpTimeout()}, true},
		{"a walk that got nothing before it timed out", &WalkError{
			Op: "BULKWALK", OID: "1.3.6.1.2.1.2.2", Received: 0, Err: gosnmpTimeout(),
		}, true},
		{"nothing listening on the port", refused(), true},
		{"a budget that ran out", context.DeadlineExceeded, true},

		{"no error at all", nil, false},
		{"the agent refused the request", &ResponseError{Status: gosnmp.NoSuchName, Count: 4}, false},
		{"the agent cannot fit one varbind in a response", &TooBigError{Count: 1, OID: "1.3.6.1.2.1.1.1.0"}, false},
		{"a table that delivered rows before it stopped", &WalkError{
			Op: "BULKWALK", OID: "1.3.6.1.2.1.2.2", Received: 42, Err: gosnmpTimeout(),
		}, false},
		{"a request that named no OIDs", &NoOIDsError{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Unanswered(tt.err); got != tt.want {
				t.Errorf("Unanswered(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// partialWalker returns a walker that delivers the first n PDUs and then fails,
// which is a device that stopped answering half way through a table.
func partialWalker(pdus []gosnmp.SnmpPDU, n int, err error) walker {
	return func(_ string, walkFn gosnmp.WalkFunc) error {
		for i, pdu := range pdus {
			if i == n {
				break
			}
			if walkErr := walkFn(pdu); walkErr != nil {
				return walkErr
			}
		}
		return err
	}
}

// TestWalkErrorRecordsWhatArrived is what keeps a device with one slow table
// from being mistaken for a device that is gone. gosnmp discards the rows a
// failed walk collected, so their count on the error is the only evidence left
// that the device was answering.
func TestWalkErrorRecordsWhatArrived(t *testing.T) {
	t.Parallel()

	c := &Client{}
	transport := gosnmpTimeout()

	results, err := c.collectWalk(partialWalker(walkPDUs(), 2, transport), "BULKWALK", "1.3.6.1.2.1.2.2")
	if results != nil {
		t.Errorf("a failed walk returned %d partial results; callers would read them as a whole table", len(results))
	}

	var walkErr *WalkError
	if !errors.As(err, &walkErr) {
		t.Fatalf("collectWalk error = %v, want a WalkError", err)
	}
	if walkErr.Received != 2 {
		t.Errorf("WalkError.Received = %d, want the 2 varbinds that arrived", walkErr.Received)
	}
	if Unanswered(err) {
		t.Error("a device that delivered two rows before failing was judged unanswered")
	}
	if !errors.Is(err, transport) {
		t.Error("the transport failure is not reachable with errors.Is")
	}
	if !strings.Contains(err.Error(), "BULKWALK") || !strings.Contains(err.Error(), "1.3.6.1.2.1.2.2") {
		t.Errorf("error %q names neither the operation nor the root OID", err)
	}
}

// TestWalkErrorFromASilentDevice is the other half: a walk that got nothing at
// all is silence, and the count says so.
func TestWalkErrorFromASilentDevice(t *testing.T) {
	t.Parallel()

	c := &Client{}

	_, err := c.collectWalk(partialWalker(walkPDUs(), 0, gosnmpTimeout()), "BULKWALK", "1.3.6.1.2.1.2.2")

	var walkErr *WalkError
	if !errors.As(err, &walkErr) {
		t.Fatalf("collectWalk error = %v, want a WalkError", err)
	}
	if walkErr.Received != 0 {
		t.Errorf("WalkError.Received = %d, want 0", walkErr.Received)
	}
	if !Unanswered(err) {
		t.Error("a walk that received nothing at all was not judged unanswered")
	}
}

// TestStreamWalkErrorRecordsWhatArrived covers the streaming walk, which counts
// what it handed to its caller rather than what it kept.
func TestStreamWalkErrorRecordsWhatArrived(t *testing.T) {
	t.Parallel()

	c := &Client{}
	delivered := 0

	err := c.streamWalk(partialWalker(walkPDUs(), 2, gosnmpTimeout()), "WALK", "1.3.6.1.2.1.2.2",
		func(*Result) error {
			delivered++
			return nil
		})

	var walkErr *WalkError
	if !errors.As(err, &walkErr) {
		t.Fatalf("streamWalk error = %v, want a WalkError", err)
	}
	if walkErr.Received != delivered {
		t.Errorf("WalkError.Received = %d, want the %d varbinds handed to the caller", walkErr.Received, delivered)
	}
	if Unanswered(err) {
		t.Error("a device that streamed two rows before failing was judged unanswered")
	}
}

// TestUnansweredAgainstASilentAgent runs the classification over a real socket,
// because the error it has to recognize is one gosnmp constructs itself and the
// only faithful way to obtain it is to let a device stay silent.
func TestUnansweredAgainstASilentAgent(t *testing.T) {
	t.Parallel()

	agent := newTestAgent(t, systemGroup()...)
	client := newAgentClient(t, agent)

	// It answers first, so the test is about silence rather than about the
	// socket never having worked.
	if _, err := client.GetMultiple([]string{sysDescrOID}); err != nil {
		t.Fatalf("GetMultiple against an answering agent: %v", err)
	}

	agent.silent.Store(true)

	_, getErr := client.GetMultiple([]string{sysDescrOID})
	if !Unanswered(getErr) {
		t.Errorf("Unanswered(%v) = false for a GET a silent device never answered", getErr)
	}

	_, walkErr := client.BulkWalkAll("1.3.6.1.2.1.1")
	if !Unanswered(walkErr) {
		t.Errorf("Unanswered(%v) = false for a walk a silent device never answered", walkErr)
	}
}
