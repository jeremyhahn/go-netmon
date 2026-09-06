//go:build !fips

package websocket

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// The client pumps driven over a REAL connection, which only a non-FIPS build
// can make.
//
// dialHub performs an RFC 6455 handshake, and Sec-WebSocket-Accept is a SHA-1
// digest of the client key and a fixed GUID (section 4.2.2). Under
// GODEBUG=fips140=only the runtime refuses that hash, so the dial fails on
// both the server and the client side -- gorilla/websocket computes the same
// digest in Upgrader.Upgrade and in Dialer.Dial.
//
// The Hub itself, its client registry, the delta encoding and the query path
// touch no non-approved primitive and stay in stream_test.go, covered in both
// builds. Only the handshake moved.

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
