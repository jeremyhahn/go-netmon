//go:build !fips

package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	ws "github.com/jeremyhahn/go-netmon/internal/webui/websocket"
)

// The upgrade handshake test, non-FIPS only; see TestWebSocketRouteDeclaresItselfUnavailable.

// TestWebSocketUpgrade covers the upgrade handler: a browser that connects
// reaches the hub and receives what the hub streams.
func TestWebSocketUpgrade(t *testing.T) {
	s := newTestServer(t)
	s.queryFunc = func(context.Context, string) ([]ws.MetricValue, error) {
		return []ws.MetricValue{{Metric: map[string]string{"ifindex": "1"}, Value: 1}}, nil
	}
	s.hub = ws.NewHub(s.queryFunc, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.hub.Run(ctx)

	srv := httptest.NewServer(s.router)
	defer srv.Close()

	conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if resp != nil {
		_ = resp.Body.Close()
	}
	defer func() { _ = conn.Close() }()

	if err := conn.WriteJSON(map[string]any{"type": "subscribe", "queries": []string{"snmp_if_in"}}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}

	var msg struct {
		Type    string           `json:"type"`
		Seq     uint64           `json:"seq"`
		Metrics []ws.MetricValue `json:"metrics"`
	}
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatalf("read streamed message: %v", err)
	}
	// The first frame for a query is the whole reading: there is nothing to
	// express it as a change to, and it carries the sequence a later delta
	// names.
	if msg.Type != "metric_snapshot" || len(msg.Metrics) != 1 || msg.Seq == 0 {
		t.Errorf("message = %+v, want a sequenced snapshot", msg)
	}
}
