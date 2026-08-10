// Package websocket pushes live metric updates to connected browsers.
package websocket

import (
	"context"
	"encoding/json"
	"log"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/jeremyhahn/go-netmon/internal/logging"
)

// Hub maintains the set of active clients and broadcasts messages to them
type Hub struct {
	// Registered clients
	clients map[*Client]bool

	// Inbound messages from clients
	broadcast chan []byte

	// Register requests from clients
	register chan *Client

	// Unregister requests from clients
	unregister chan *Client

	// Prometheus query function
	queryFunc QueryFunc

	// Subscriptions map: OID -> list of clients
	subscriptions map[string][]*Client

	// logger carries the hub's level-gated diagnostics. Streaming runs on a
	// timer for every subscribed query, so these are the noisiest statements in
	// the process and must cost nothing unless debug logging is on.
	logger *slog.Logger

	mu sync.RWMutex
}

// QueryFunc is a function that queries Prometheus for metrics
type QueryFunc func(ctx context.Context, query string) ([]MetricValue, error)

// MetricValue represents a single metric data point
type MetricValue struct {
	Metric    map[string]string `json:"metric"`
	Value     float64           `json:"value"`
	Timestamp int64             `json:"timestamp"`
}

// Message types
const (
	MessageTypeSubscribe    = "subscribe"
	MessageTypeUnsubscribe  = "unsubscribe"
	MessageTypeMetricUpdate = "metric_update"
	MessageTypeError        = "error"
)

// ClientMessage represents a message from the client
type ClientMessage struct {
	Type    string   `json:"type"`
	Queries []string `json:"queries,omitempty"`
}

// ServerMessage represents a message to the client
type ServerMessage struct {
	Type      string        `json:"type"`
	Query     string        `json:"query,omitempty"`
	Metrics   []MetricValue `json:"metrics,omitempty"`
	Error     string        `json:"error,omitempty"`
	Timestamp int64         `json:"timestamp"`
}

// NewHub creates a new Hub that reports its stream tracing to logger.
//
// A nil logger discards those records, so a caller that has not configured
// logging pays nothing for them.
func NewHub(queryFunc QueryFunc, logger *slog.Logger) *Hub {
	return &Hub{
		clients:       make(map[*Client]bool),
		broadcast:     make(chan []byte, 256),
		register:      make(chan *Client),
		unregister:    make(chan *Client),
		queryFunc:     queryFunc,
		subscriptions: make(map[string][]*Client),
		logger:        logging.OrDiscard(logger),
	}
}

// RegisterClient adds a client to the hub
func (h *Hub) RegisterClient(client *Client) {
	h.register <- client
}

// Run starts the hub
func (h *Hub) Run(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second) // Stream updates every 5 seconds
	defer ticker.Stop()

	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
			log.Printf("Client connected. Total clients: %d", len(h.clients))

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				client.close()
				// Remove from all subscriptions
				for query, clients := range h.subscriptions {
					for i, c := range clients {
						if c == client {
							h.subscriptions[query] = append(clients[:i], clients[i+1:]...)
							break
						}
					}
				}
			}
			h.mu.Unlock()
			log.Printf("Client disconnected. Total clients: %d", len(h.clients))

		case <-ticker.C:
			// Stream metrics to all subscribed clients
			h.streamMetrics(ctx)

		case <-ctx.Done():
			log.Println("Hub shutting down")
			return
		}
	}
}

// streamMetrics queries Prometheus and sends updates to subscribed clients
func (h *Hub) streamMetrics(ctx context.Context) {
	h.mu.RLock()
	queries := make(map[string][]*Client)
	for query, clients := range h.subscriptions {
		if len(clients) > 0 {
			queries[query] = clients
		}
	}
	h.mu.RUnlock()

	if len(queries) > 0 {
		h.logger.DebugContext(ctx, "streaming metrics", "queries", len(queries))
	}

	for query, clients := range queries {
		go func(q string, cls []*Client) {
			metrics, err := h.queryFunc(ctx, q)
			if err != nil {
				log.Printf("Error querying Prometheus for %s: %v", q, err)
				return
			}

			// Every sample carries its full label set, so the per-metric trace
			// is skipped as a block rather than gated one record at a time.
			if h.logger.Enabled(ctx, slog.LevelDebug) {
				h.logger.DebugContext(ctx, "query returned metrics",
					"query", q, "metrics", len(metrics), "clients", len(cls))
				for i, m := range metrics {
					if i >= 3 { // A sample is enough to see the shape.
						break
					}
					h.logger.DebugContext(ctx, "sample metric",
						"query", q, "index", i, "value", m.Value, "labels", m.Metric)
				}
			}

			msg := ServerMessage{
				Type:      MessageTypeMetricUpdate,
				Query:     q,
				Metrics:   metrics,
				Timestamp: time.Now().Unix(),
			}

			data, err := json.Marshal(msg)
			if err != nil {
				log.Printf("Error marshaling message: %v", err)
				return
			}

			// Send to all subscribed clients. This runs outside the hub lock,
			// so a client may disconnect at any point during the loop;
			// trySend handles that case rather than panicking.
			for _, client := range cls {
				if !client.trySend(data) {
					log.Printf("Dropped metric update for query %q: client is slow or gone", q)
				}
			}
		}(query, clients)
	}
}

// Subscribe adds a client to a query subscription
func (h *Hub) Subscribe(client *Client, query string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.subscriptions[query] = append(h.subscriptions[query], client)
	log.Printf("Client subscribed to: %s", query)
}

// Unsubscribe removes a client from a query subscription
func (h *Hub) Unsubscribe(client *Client, query string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	clients := h.subscriptions[query]
	for i, c := range clients {
		if c == client {
			h.subscriptions[query] = append(clients[:i], clients[i+1:]...)
			log.Printf("Client unsubscribed from: %s", query)
			return
		}
	}
}

// Client represents a WebSocket client
type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte

	// mu guards closed and serializes it against sends. The metric streamer
	// sends from goroutines that outlive the hub lock, so without this a
	// client disconnecting mid-stream could be sent to after its channel was
	// closed, which panics and takes the whole process down.
	mu     sync.Mutex
	closed bool
}

// NewClient creates a new client
func NewClient(hub *Hub, conn *websocket.Conn) *Client {
	return &Client{
		hub:  hub,
		conn: conn,
		send: make(chan []byte, 256),
	}
}

// trySend queues a message for the client, reporting whether it was accepted.
//
// It drops the message rather than blocking when the client's buffer is full,
// since one slow browser must not stall metric streaming for everyone, and
// reports false once the client has gone away.
func (c *Client) trySend(data []byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return false
	}

	select {
	case c.send <- data:
		return true
	default:
		return false
	}
}

// close releases the client's send channel exactly once. Closing signals
// WritePump to finish; holding the same lock trySend uses is what makes the
// close safe while streams are in flight.
func (c *Client) close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return
	}
	c.closed = true
	close(c.send)
}

// ReadPump pumps messages from the WebSocket connection to the hub
func (c *Client) ReadPump() {
	defer func() {
		c.hub.unregister <- c
		// The read loop has ended, so the connection is finished either way.
		_ = c.conn.Close()
	}()

	// A deadline can only fail to apply on a connection that is already gone,
	// which the very next read reports.
	_ = c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		_ = c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket error: %v", err)
			}
			break
		}

		var msg ClientMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			log.Printf("Error unmarshaling client message: %v", err)
			continue
		}

		switch msg.Type {
		case MessageTypeSubscribe:
			for _, query := range msg.Queries {
				c.hub.Subscribe(c, query)
			}
		case MessageTypeUnsubscribe:
			for _, query := range msg.Queries {
				c.hub.Unsubscribe(c, query)
			}
		}
	}
}

// WritePump pumps messages from the hub to the WebSocket connection
func (c *Client) WritePump() {
	ticker := time.NewTicker(54 * time.Second)
	defer func() {
		ticker.Stop()
		// The write loop has ended, so the connection is finished either way.
		_ = c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			// A deadline can only fail to apply on a connection that is already
			// gone, which the write below reports.
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				// Best-effort courtesy frame on a hub-closed channel.
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}

		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
