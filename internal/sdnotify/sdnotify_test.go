package sdnotify

import (
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// listener stands in for systemd: a unixgram socket that records what the
// process under test sent it. Nothing here needs systemd to be running, or
// even present — the protocol is a datagram on a socket the test owns.
type listener struct {
	t    *testing.T
	conn *net.UnixConn
	addr string
}

func newListener(t *testing.T) *listener {
	t.Helper()

	// A socket path has a hard length limit (108 bytes on Linux), so the name
	// is kept short rather than derived from the test's name.
	addr := filepath.Join(t.TempDir(), "notify")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: addr, Net: "unixgram"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	return &listener{t: t, conn: conn, addr: addr}
}

// next returns the next datagram, failing the test rather than blocking for
// ever if the process under test sent nothing.
func (l *listener) next() string {
	l.t.Helper()

	require.NoError(l.t, l.conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	buf := make([]byte, 256)
	n, err := l.conn.Read(buf)
	require.NoError(l.t, err)
	return string(buf[:n])
}

func TestDialReadyDeliversTheStateDatagram(t *testing.T) {
	sd := newListener(t)

	notifier, err := Dial(sd.addr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = notifier.Close() })

	assert.True(t, notifier.Supervised())
	require.NoError(t, notifier.Ready())
	assert.Equal(t, "READY=1", sd.next())

	require.NoError(t, notifier.Stopping())
	assert.Equal(t, "STOPPING=1", sd.next())
}

// The status line is what a service that has not finished starting says to
// somebody looking at it, so what it carries has to arrive intact.
func TestStatusDeliversTheText(t *testing.T) {
	sd := newListener(t)

	notifier, err := Dial(sd.addr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = notifier.Close() })

	require.NoError(t, notifier.Status("loading imported MIB modules: 42/176 (IF-MIB)"))
	assert.Equal(t, "STATUS=loading imported MIB modules: 42/176 (IF-MIB)", sd.next())
}

// The protocol is newline-separated VAR=value pairs, so a newline inside the
// text would be read as a second assignment and the rest of the line dropped.
func TestStatusFlattensNewlines(t *testing.T) {
	sd := newListener(t)

	notifier, err := Dial(sd.addr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = notifier.Close() })

	require.NoError(t, notifier.Status("parsing\nIF-MIB\r\nand more"))
	assert.Equal(t, "STATUS=parsing IF-MIB  and more", sd.next())
}

func TestStatusOnAnUnsupervisedNotifierIsANoOp(t *testing.T) {
	t.Setenv(SocketEnv, "")

	notifier, err := New()
	require.NoError(t, err)

	assert.NoError(t, notifier.Status("anything"))
	var nilNotifier *Notifier
	assert.NoError(t, nilNotifier.Status("anything"))
}

func TestNewUsesTheEnvironmentSocket(t *testing.T) {
	sd := newListener(t)
	t.Setenv(SocketEnv, sd.addr)

	notifier, err := New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = notifier.Close() })

	require.NoError(t, notifier.Ready())
	assert.Equal(t, "READY=1", sd.next())
}

// An unsupervised process is the common case — every test run, every `go run`
// — and it must not be a special case at the call site.
func TestNewWithoutTheEnvironmentIsAWorkingNoOp(t *testing.T) {
	t.Setenv(SocketEnv, "")

	notifier, err := New()
	require.NoError(t, err)

	assert.False(t, notifier.Supervised())
	assert.NoError(t, notifier.Ready())
	assert.NoError(t, notifier.Stopping())
	assert.NoError(t, notifier.Close())
}

func TestNilNotifierIsSafe(t *testing.T) {
	var notifier *Notifier

	assert.False(t, notifier.Supervised())
	assert.NoError(t, notifier.Ready())
	assert.NoError(t, notifier.Stopping())
	assert.NoError(t, notifier.Close())
}

// systemd only ever sets an absolute path or an abstract name. Anything else
// is a misconfiguration, and it has to be reported: a service that swallowed
// it would hang in "activating" until TimeoutStartSec killed it, which is the
// failure this package exists to prevent.
func TestDialRejectsAddressesSystemdWouldNeverSet(t *testing.T) {
	for name, addr := range map[string]string{
		"empty":    "",
		"relative": "notify.sock",
		"scheme":   "unix://notify.sock",
	} {
		t.Run(name, func(t *testing.T) {
			notifier, err := Dial(addr)

			assert.Nil(t, notifier)
			var addrErr *AddressError
			require.ErrorAs(t, err, &addrErr)
			assert.Equal(t, addr, addrErr.Addr)
			assert.Contains(t, addrErr.Error(), "sdnotify:")
		})
	}
}

func TestDialReportsAnUnopenableSocket(t *testing.T) {
	notifier, err := Dial(filepath.Join(t.TempDir(), "no", "such", "notify"))

	assert.Nil(t, notifier)
	var dialErr *DialError
	require.ErrorAs(t, err, &dialErr)
	assert.Error(t, dialErr.Unwrap())
}

// A set-but-broken NOTIFY_SOCKET is the case that must not be mistaken for the
// unsupervised one.
func TestNewReportsABrokenEnvironmentSocket(t *testing.T) {
	t.Setenv(SocketEnv, "notify.sock")

	notifier, err := New()

	assert.Nil(t, notifier)
	var addrErr *AddressError
	assert.ErrorAs(t, err, &addrErr)
}

func TestSendAfterCloseIsHarmless(t *testing.T) {
	sd := newListener(t)

	notifier, err := Dial(sd.addr)
	require.NoError(t, err)

	require.NoError(t, notifier.Close())
	assert.False(t, notifier.Supervised())
	assert.NoError(t, notifier.Ready())
	assert.NoError(t, notifier.Close())
}

func TestCloseReportsAFailedClose(t *testing.T) {
	sd := newListener(t)

	notifier, err := Dial(sd.addr)
	require.NoError(t, err)

	// Closed behind the notifier's back, so Close finds a connection it still
	// believes it owns and cannot close.
	require.NoError(t, notifier.conn.Close())

	var closeErr *CloseError
	require.ErrorAs(t, notifier.Close(), &closeErr)
	assert.Error(t, closeErr.Unwrap())
}

// A socket that has gone away under a live notifier is reported rather than
// counted as delivered.
func TestReadyReportsAFailedWrite(t *testing.T) {
	sd := newListener(t)

	notifier, err := Dial(sd.addr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = notifier.Close() })

	// Close the underlying connection behind the notifier's back: the same
	// observable state as a socket the manager has torn down.
	require.NoError(t, notifier.conn.Close())

	var sendErr *SendError
	require.ErrorAs(t, notifier.Ready(), &sendErr)
	assert.Equal(t, "READY=1", sendErr.State)
	assert.Error(t, sendErr.Unwrap())
}
