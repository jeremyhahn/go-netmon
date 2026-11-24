package snmp

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"syscall"
)

// The single definition of whether a device answered at all; gosnmp's retry exhaustion is recognized by its text, last.

// TimedOut reports whether err means the operation ran out of time waiting for the device.
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

// Unanswered reports whether the device never answered: all attempts timed out or the port was refused.
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

	// ICMP port-unreachable: nothing serves SNMP on the host.
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}

	return TimedOut(err)
}
