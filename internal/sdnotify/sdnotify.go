// Package sdnotify reports service state to systemd over the socket named by
// NOTIFY_SOCKET.
//
// It exists because a Type=simple unit is "active" the instant the process
// forks. The web UI parses its MIB catalog before it binds :8080, so for
// minutes after a restart systemd reported a healthy service that refused
// every connection — a state indistinguishable, from the outside, from a
// process wedged and never going to serve. A deploy was read as hung and
// rolled back on that signal.
//
// With Type=notify the unit is not active until the process says so, and the
// process says so after the listener is bound. There is no separate health
// check to keep in step: the readiness signal is emitted from the one place
// that knows the socket is accepting.
//
// systemd's protocol is a datagram of newline-separated VAR=value pairs sent
// to a unixgram socket. That is small enough to implement here rather than
// take a dependency for, and doing so keeps the notification path free of any
// code the service does not otherwise run.
package sdnotify

import (
	"net"
	"os"
	"strings"
)

// SocketEnv is the variable systemd sets to the notification socket's address.
// It is unset for a process that is not supervised, which is the normal case
// under `go run`, in a container and in every test.
const SocketEnv = "NOTIFY_SOCKET"

// Notifier sends state messages to the service manager.
//
// A Notifier with no socket — the process is not running under systemd — is
// usable and does nothing, and so is a nil *Notifier. Callers therefore never
// branch on whether they are supervised: a service that reports readiness does
// it the same way in both cases, and the one that is not listened to is the
// one that costs nothing.
type Notifier struct {
	conn *net.UnixConn
}

// New returns a Notifier for the socket named by NOTIFY_SOCKET.
//
// An unset variable yields a Notifier that discards what it is told. A variable
// that is set but unusable is an error: systemd set it, so something is
// listening, and silently dropping READY=1 in that case is exactly the failure
// this package exists to prevent — the service would hang in "activating" until
// TimeoutStartSec killed it.
func New() (*Notifier, error) {
	addr := os.Getenv(SocketEnv)
	if addr == "" {
		return &Notifier{}, nil
	}
	return Dial(addr)
}

// Dial opens a notifier on an explicit socket address, in the two forms
// systemd uses: an absolute filesystem path, or a name with a leading '@' in
// the abstract namespace. Go's unixgram dialer understands both spellings.
func Dial(addr string) (*Notifier, error) {
	switch {
	case addr == "":
		return nil, &AddressError{Addr: addr, Reason: "empty"}
	case strings.HasPrefix(addr, "@"), strings.HasPrefix(addr, "/"):
		// The two shapes systemd documents.
	default:
		return nil, &AddressError{Addr: addr, Reason: "not an absolute path or an abstract-namespace name"}
	}

	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: addr, Net: "unixgram"})
	if err != nil {
		return nil, &DialError{Addr: addr, Err: err}
	}

	return &Notifier{conn: conn}, nil
}

// Supervised reports whether this Notifier has a service manager to talk to.
// It is what a caller logs; it is not a condition to guard a call on.
func (n *Notifier) Supervised() bool {
	return n != nil && n.conn != nil
}

// Ready tells the service manager the service has finished starting and can
// serve. Send it after the listener is bound, never before: the whole point is
// that "active" and "answering" become the same moment.
func (n *Notifier) Ready() error {
	return n.send("READY=1")
}

// Stopping tells the service manager the service has begun shutting down, so
// that a slow graceful shutdown reads as deactivating rather than as a service
// that has stopped responding for no stated reason.
func (n *Notifier) Stopping() error {
	return n.send("STOPPING=1")
}

// Close releases the notification socket. A closed Notifier keeps working as
// the unsupervised one does, so a shutdown path that reports state after Close
// is harmless rather than a panic.
func (n *Notifier) Close() error {
	if !n.Supervised() {
		return nil
	}
	conn := n.conn
	n.conn = nil
	if err := conn.Close(); err != nil {
		return &CloseError{Err: err}
	}
	return nil
}

// send writes one state datagram. systemd ignores states it does not know, so
// a message that never arrives is the only failure worth reporting.
func (n *Notifier) send(state string) error {
	if !n.Supervised() {
		return nil
	}
	if _, err := n.conn.Write([]byte(state)); err != nil {
		return &SendError{State: state, Err: err}
	}
	return nil
}
