package snmp

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"syscall"
)

// Whether a device answered at all is a different question from whether an
// operation succeeded, and it is the question a caller has to answer before it
// decides to stop polling a device. These two predicates are the only place
// that question is answered, so a poller, a probe and a health check cannot
// drift into three different opinions about what "unreachable" means.
//
// The answer has to be recovered from an error value because gosnmp reports an
// exhausted retry sequence as a plain fmt.Errorf whose text is the only thing
// that distinguishes it. Everything this package produces itself is a type, so
// the text is consulted last and only for errors that came from gosnmp.

// TimedOut reports whether err means the operation ran out of time waiting for
// the device to say something.
//
// It is deliberately narrow: a refused connection, a bad community and a
// malformed response are all failures, and none of them are this.
func TimedOut(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	// gosnmp rewrites an exhausted retry sequence as "request timeout (after N
	// retries)", losing the deadline error it came from. Its text is all that
	// is left.
	return strings.Contains(err.Error(), "timeout")
}

// Unanswered reports whether err means the device never answered at all.
//
// This is what separates a device that is not there from one that is slow or
// selective. An agent that replied is reachable, whatever the reply said:
//
//   - a non-zero error status is the agent refusing a request it received;
//   - tooBig is the agent describing its own response limit;
//   - a walk that delivered rows before failing had a conversation.
//
// Only silence — every attempt timed out, or the host actively refused the
// port so nothing is listening on it — counts.
func Unanswered(err error) bool {
	if err == nil {
		return false
	}

	// An agent that answered, in any of the ways it can answer.
	var (
		response *ResponseError
		tooBig   *TooBigError
		walk     *WalkError
	)
	switch {
	case errors.As(err, &response), errors.As(err, &tooBig):
		return false
	case errors.As(err, &walk) && walk.Received > 0:
		return false
	}

	// ICMP port-unreachable: the host is up and nothing is serving SNMP on it.
	// From the poller's side that is as unanswered as silence, and unlike
	// silence it costs nothing to discover.
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}

	return TimedOut(err)
}
