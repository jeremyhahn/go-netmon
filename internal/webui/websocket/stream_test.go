package websocket

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/logging"
)

// safeBuffer is a bytes.Buffer safe for the streaming goroutines to write to
// while the test reads it.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// sampleMetrics is what a query answers with in these tests.
func sampleMetrics() []MetricValue {
	return []MetricValue{
		{Metric: map[string]string{"__name__": "snmp_if_in", "ifindex": "1"}, Value: 1, Timestamp: 1700000000},
		{Metric: map[string]string{"__name__": "snmp_if_in", "ifindex": "2"}, Value: 2, Timestamp: 1700000000},
	}
}

// levelLogger builds a logger at the named level writing into sink.
func levelLogger(t *testing.T, level string, sink *safeBuffer) *slog.Logger {
	t.Helper()

	logger, err := logging.NewWithWriter(
		config.LoggingConfig{Level: level, Format: "json", Output: "stdout"}, sink)
	if err != nil {
		t.Fatalf("NewWithWriter: %v", err)
	}
	return logger
}

// waitForMessage reads one message from a client's queue.
func waitForMessage(t *testing.T, c *Client) ServerMessage {
	t.Helper()

	select {
	case data := <-c.send:
		var msg ServerMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("message is not a ServerMessage (%q): %v", data, err)
		}
		return msg
	case <-time.After(2 * time.Second):
		t.Fatal("no message was streamed to the client")
		return ServerMessage{}
	}
}

func TestStreamMetricsSendsToSubscribers(t *testing.T) {
	hub := NewHub(func(context.Context, string) ([]MetricValue, error) {
		return sampleMetrics(), nil
	}, nil)

	client := newTestClient()
	hub.Subscribe(client, "snmp_if_in")

	hub.streamMetrics(context.Background())

	msg := waitForMessage(t, client)
	// The first frame for a query is always the whole reading: there is
	// nothing to express it as a change to.
	if msg.Type != MessageTypeMetricSnapshot {
		t.Errorf("Type = %q, want %q", msg.Type, MessageTypeMetricSnapshot)
	}
	if msg.Seq == 0 {
		t.Error("the frame carries no sequence, so a later delta could not name it")
	}
	if msg.Query != "snmp_if_in" {
		t.Errorf("Query = %q, want snmp_if_in", msg.Query)
	}
	if len(msg.Metrics) != 2 {
		t.Errorf("Metrics = %d, want 2", len(msg.Metrics))
	}
}

// TestStreamMetricsWithNoSubscribers keeps the hub from querying Prometheus on
// every tick when nobody is watching.
func TestStreamMetricsWithNoSubscribers(t *testing.T) {
	var queried int
	hub := NewHub(func(context.Context, string) ([]MetricValue, error) {
		queried++
		return nil, nil
	}, nil)

	hub.streamMetrics(context.Background())

	if queried != 0 {
		t.Errorf("the hub ran %d queries with no subscribers, want none", queried)
	}
}

// TestStreamMetricsSurvivesAFailingQuery keeps one broken query from stopping
// the stream for the others.
func TestStreamMetricsSurvivesAFailingQuery(t *testing.T) {
	hub := NewHub(func(_ context.Context, query string) ([]MetricValue, error) {
		if query == "broken" {
			return nil, errors.New("prometheus is down")
		}
		return sampleMetrics(), nil
	}, nil)

	good := newTestClient()
	bad := newTestClient()
	hub.Subscribe(good, "snmp_if_in")
	hub.Subscribe(bad, "broken")

	hub.streamMetrics(context.Background())

	if msg := waitForMessage(t, good); len(msg.Metrics) != 2 {
		t.Errorf("the working query streamed %d metrics, want 2", len(msg.Metrics))
	}

	select {
	case data := <-bad.send:
		t.Errorf("the failing query streamed %q, want nothing", data)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestStreamMetricsDropsForAGoneClient covers the disconnect that happens
// between taking the subscriber list and sending to it.
func TestStreamMetricsDropsForAGoneClient(t *testing.T) {
	hub := NewHub(func(context.Context, string) ([]MetricValue, error) {
		return sampleMetrics(), nil
	}, nil)

	client := newTestClient()
	hub.Subscribe(client, "snmp_if_in")
	client.close()

	// A send to a closed client must be dropped, not panic.
	hub.streamMetrics(context.Background())
	time.Sleep(100 * time.Millisecond)
}

// TestStreamTracingIsSilentAtInfo is the websocket half of the log-level
// guarantee. The hub logged a line per query and a line per metric on every
// five-second tick, which is where most of the flood came from.
func TestStreamTracingIsSilentAtInfo(t *testing.T) {
	tests := []struct {
		name      string
		level     string
		wantTrace bool
	}{
		{"info is silent", "info", false},
		{"debug traces the stream", "debug", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sink safeBuffer
			hub := NewHub(func(context.Context, string) ([]MetricValue, error) {
				return sampleMetrics(), nil
			}, levelLogger(t, tt.level, &sink))

			client := newTestClient()
			hub.Subscribe(client, "snmp_if_in")

			hub.streamMetrics(context.Background())
			waitForMessage(t, client)

			out := sink.String()
			if got := strings.Contains(out, "streaming metrics"); got != tt.wantTrace {
				t.Errorf("stream trace present = %v, want %v (output %q)", got, tt.wantTrace, out)
			}
			if got := strings.Contains(out, "sample metric"); got != tt.wantTrace {
				t.Errorf("per-metric trace present = %v, want %v (output %q)", got, tt.wantTrace, out)
			}
			if !tt.wantTrace && out != "" {
				t.Errorf("info level produced %q, want nothing on the streaming path", out)
			}
		})
	}
}

// TestSampleTraceIsCapped keeps the per-metric trace from printing an entire
// interface table even when debug logging is on.
func TestSampleTraceIsCapped(t *testing.T) {
	many := make([]MetricValue, 10)
	for i := range many {
		many[i] = MetricValue{Metric: map[string]string{"ifindex": "x"}, Value: float64(i)}
	}

	var sink safeBuffer
	hub := NewHub(func(context.Context, string) ([]MetricValue, error) {
		return many, nil
	}, levelLogger(t, "debug", &sink))

	client := newTestClient()
	hub.Subscribe(client, "snmp_if_in")

	hub.streamMetrics(context.Background())
	waitForMessage(t, client)

	if got := strings.Count(sink.String(), "sample metric"); got != 3 {
		t.Errorf("per-metric records = %d, want the first 3 only", got)
	}
}

func TestSubscribeAndUnsubscribe(t *testing.T) {
	hub := NewHub(func(context.Context, string) ([]MetricValue, error) {
		return sampleMetrics(), nil
	}, nil)

	client := newTestClient()
	other := newTestClient()

	hub.Subscribe(client, "snmp_if_in")
	hub.Subscribe(other, "snmp_if_in")

	hub.Unsubscribe(client, "snmp_if_in")

	hub.mu.RLock()
	remaining := len(hub.subscriptions["snmp_if_in"])
	hub.mu.RUnlock()
	if remaining != 1 {
		t.Errorf("subscribers = %d, want the one that stayed", remaining)
	}

	// Unsubscribing twice, or from a query never subscribed to, is a no-op
	// rather than a panic.
	hub.Unsubscribe(client, "snmp_if_in")
	hub.Unsubscribe(client, "never-subscribed")

	hub.mu.RLock()
	remaining = len(hub.subscriptions["snmp_if_in"])
	hub.mu.RUnlock()
	if remaining != 1 {
		t.Errorf("subscribers = %d after repeated unsubscribes, want 1", remaining)
	}
}

// TestRunRegistersAndUnregisters covers the hub loop's own bookkeeping and its
// shutdown.
func TestRunRegistersAndUnregisters(t *testing.T) {
	hub := NewHub(func(context.Context, string) ([]MetricValue, error) {
		return nil, nil
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		hub.Run(ctx)
		close(done)
	}()

	client := newTestClient()
	hub.RegisterClient(client)

	waitFor(t, 2*time.Second, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		return hub.clients[client]
	}, "the client was never registered")

	hub.Subscribe(client, "snmp_if_in")
	hub.unregister <- client

	waitFor(t, 2*time.Second, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		return !hub.clients[client] && len(hub.subscriptions["snmp_if_in"]) == 0
	}, "the client was never unregistered or left its subscription behind")

	// Unregistering a client the hub does not know must not disturb it.
	hub.unregister <- newTestClient()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after its context was canceled")
	}
}

// waitFor polls cond until it holds or the timeout expires.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s (waited %v)", msg, timeout)
}

// dialHub starts an HTTP server that upgrades to a hub client and returns the
// browser side of the connection.
func dialHub(t *testing.T, hub *Hub) (*websocket.Conn, *Client) {
	t.Helper()

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	clientCh := make(chan *Client, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		c := NewClient(hub, conn)
		hub.RegisterClient(c)
		clientCh <- c
		go c.WritePump()
		go c.ReadPump()
	}))
	t.Cleanup(srv.Close)

	conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	// The handshake response body carries nothing once the upgrade succeeded.
	if resp != nil {
		_ = resp.Body.Close()
	}
	t.Cleanup(func() { _ = conn.Close() })

	select {
	case c := <-clientCh:
		return conn, c
	case <-time.After(2 * time.Second):
		t.Fatal("the hub never registered the client")
		return nil, nil
	}
}

// TestClientPumpsCarryTheSubscribeProtocol covers the read and write loops end
// to end over a real WebSocket: a subscribe reaches the hub, and what the hub
// streams reaches the browser.
func TestClientPumpsCarryTheSubscribeProtocol(t *testing.T) {
	hub := NewHub(func(context.Context, string) ([]MetricValue, error) {
		return sampleMetrics(), nil
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hub.Run(ctx)

	conn, client := dialHub(t, hub)

	if err := conn.WriteJSON(ClientMessage{Type: MessageTypeSubscribe, Queries: []string{"snmp_if_in"}}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		return len(hub.subscriptions["snmp_if_in"]) == 1
	}, "the subscribe never reached the hub")

	hub.streamMetrics(ctx)

	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	var msg ServerMessage
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatalf("read streamed message: %v", err)
	}
	if msg.Type != MessageTypeMetricSnapshot || len(msg.Metrics) != 2 {
		t.Errorf("message = %+v, want a snapshot with 2 metrics", msg)
	}

	if err := conn.WriteJSON(ClientMessage{Type: MessageTypeUnsubscribe, Queries: []string{"snmp_if_in"}}); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		return len(hub.subscriptions["snmp_if_in"]) == 0
	}, "the unsubscribe never reached the hub")

	// A message that is not a ClientMessage is ignored rather than ending the
	// read loop.
	if err := conn.WriteMessage(websocket.TextMessage, []byte("not json")); err != nil {
		t.Fatalf("write malformed message: %v", err)
	}
	if err := conn.WriteJSON(ClientMessage{Type: MessageTypeSubscribe, Queries: []string{"after-garbage"}}); err != nil {
		t.Fatalf("subscribe after garbage: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		return len(hub.subscriptions["after-garbage"]) == 1
	}, "the read loop stopped after a malformed message")

	// Closing the browser side must unregister the client.
	_ = conn.Close()
	waitFor(t, 3*time.Second, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		return !hub.clients[client]
	}, "a disconnected client stayed registered")
}
