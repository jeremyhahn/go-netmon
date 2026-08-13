// Package websocket pushes live metric updates to connected browsers.
package websocket

import (
	"context"
	"encoding/json"
	"log"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/jeremyhahn/go-netmon/internal/logging"
)

// MaxQueriesInFlight caps how many Prometheus requests one streaming round has
// open at once.
//
// A round used to open one request per subscribed query simultaneously. A
// device dashboard subscribes about twenty, so a room of operators on different
// devices reached into the thousands, all landing on one Prometheus at the same
// instant. The ceiling turns that into a queue: the same work, at a rate a
// single Prometheus can actually serve.
const MaxQueriesInFlight = 32

// MaxSubscriptionsPerClient caps how many queries one browser may subscribe to.
//
// Every subscription is re-queried on every round on that client's behalf, so
// an unbounded count lets a single session set the load on Prometheus for
// everyone. The richest real dashboard measured here holds 179 — core-01
// across ten modules — so the ceiling is well clear of a legitimate page while
// still bounding a client that never lets go, or one that is not a browser at
// all. A refused subscription is answered rather than dropped silently, so the
// page can say the dashboard is too large instead of quietly missing cards.
const MaxSubscriptionsPerClient = 2048

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

	// steady tracks how long each query has read the same thing, which is what
	// lets a demonstrably static one be asked about less often.
	steady *steadyQueryRounds

	// round counts streaming rounds, so a backed-off query can be spread across
	// them rather than all falling due at once.
	round atomic.Int64

	// streaming is held for the duration of a round, so a tick that arrives
	// while the previous round is still running steps aside instead of piling
	// another full set of queries on top of it. Without it a Prometheus slower
	// than the tick made the backlog grow without bound.
	streaming atomic.Bool

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

	// MessageTypeMetricsUnchanged names the queries that were re-read this
	// round and found to be exactly as the client already has them.
	//
	// The dashboard is a tick-based live view: a chart has to keep advancing
	// while a value holds steady, or its line stops at the last change and
	// reads as data having stopped. Sending the whole result again to say
	// "still 37" is what this replaces -- the browser has the reading, so one
	// frame naming the queries lets it carry every one of them forward.
	MessageTypeMetricsUnchanged = "metrics_unchanged"

	MessageTypeError = "error"
)

// ClientMessage represents a message from the client
type ClientMessage struct {
	Type    string   `json:"type"`
	Queries []string `json:"queries,omitempty"`
}

// ServerMessage represents a message to the client
type ServerMessage struct {
	Type    string        `json:"type"`
	Query   string        `json:"query,omitempty"`
	Metrics []MetricValue `json:"metrics,omitempty"`

	// Queries carries the subjects of a metrics_unchanged frame: the queries
	// whose readings this client already holds.
	Queries []string `json:"queries,omitempty"`

	Error     string `json:"error,omitempty"`
	Timestamp int64  `json:"timestamp"`
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
		steady:        newSteadyQueryRounds(),
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
			total := len(h.clients)
			h.mu.Unlock()
			// Counted under the lock: reading the map after releasing it is a
			// data race against every other client connecting.
			h.logger.DebugContext(ctx, "client connected", "clients", total)

		case client := <-h.unregister:
			h.logger.DebugContext(ctx, "client disconnected", "clients", h.removeClient(client))

		case <-ticker.C:
			// Stream metrics to all subscribed clients
			h.streamMetrics(ctx)

		case <-ctx.Done():
			log.Println("Hub shutting down")
			return
		}
	}
}

// streamMetrics queries Prometheus and sends updates to subscribed clients.
//
// One round runs at a time and at most MaxQueriesInFlight queries run within
// it. Both bounds exist for the same reason: the number of subscribed queries
// is set by how many people are watching how many devices, and neither the
// process nor Prometheus can absorb that number as simultaneous work.
func (h *Hub) streamMetrics(ctx context.Context) {
	if !h.streaming.CompareAndSwap(false, true) {
		h.logger.DebugContext(ctx, "skipping stream round: the previous one is still running")
		return
	}
	defer h.streaming.Store(false)

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

	// The round has to finish before the next one may start, so it is waited
	// on rather than left running in the background.
	var wg sync.WaitGroup
	inFlight := make(chan struct{}, MaxQueriesInFlight)
	steady := &steadyQueries{byClient: make(map[*Client][]string)}
	round := h.round.Add(1)

	for query, clients := range queries {
		// A reading that has held still for several rounds is asked about at
		// half rate. Its subscribers are still told it is steady every round,
		// so their charts advance exactly as before -- what is saved is the
		// question, not the notification.
		if !h.steady.due(query, round) {
			for _, client := range clients {
				steady.add(client, query)
			}
			continue
		}

		wg.Add(1)
		go func(q string, cls []*Client) {
			defer wg.Done()

			select {
			case inFlight <- struct{}{}:
				defer func() { <-inFlight }()
			case <-ctx.Done():
				return
			}

			h.streamQuery(ctx, q, cls, steady)
		}(query, clients)
	}

	wg.Wait()
	h.sendSteady(ctx, steady)
}

// steadyQueries collects, per client, the queries a round found unchanged.
//
// They are gathered across the whole round and sent as one frame per client
// rather than one per query: a dashboard holds well over a hundred
// subscriptions and almost all of them are steady on any given tick.
type steadyQueries struct {
	mu       sync.Mutex
	byClient map[*Client][]string
}

func (s *steadyQueries) add(client *Client, query string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.byClient[client] = append(s.byClient[client], query)
}

// sendSteady tells each client which of its subscriptions are still reading
// what it already has, so its charts can carry those values to the current tick.
func (h *Hub) sendSteady(ctx context.Context, steady *steadyQueries) {
	steady.mu.Lock()
	defer steady.mu.Unlock()

	now := time.Now().Unix()
	for client, queries := range steady.byClient {
		if len(queries) == 0 {
			continue
		}

		data, err := json.Marshal(ServerMessage{
			Type:      MessageTypeMetricsUnchanged,
			Queries:   queries,
			Timestamp: now,
		})
		if err != nil {
			log.Printf("Error marshaling unchanged notice: %v", err)
			continue
		}

		if !client.trySend(data) {
			// Losing a heartbeat costs one tick of chart advance and nothing
			// else, so unlike a reading it is not worth re-sending.
			h.logger.DebugContext(ctx, "dropped unchanged notice: client is slow or gone",
				"queries", len(queries))
		}
	}
}

// streamQuery runs one subscribed query and delivers the result to the clients
// that do not already have it. Clients that already have it are recorded in
// steady, to be told so once for the whole round.
func (h *Hub) streamQuery(ctx context.Context, query string, clients []*Client, steady *steadyQueries) {
	metrics, err := h.queryFunc(ctx, query)
	if err != nil {
		log.Printf("Error querying Prometheus for %s: %v", query, err)
		return
	}

	h.traceResult(ctx, query, metrics, len(clients))

	// Who has not already been sent exactly this reading? Asking before
	// marshaling means an unchanged query costs nothing at all: no
	// serialization, no frame, no work in the browser.
	reading := fingerprint(metrics)
	h.steady.observe(query, reading)
	recipients := clients[:0:0]
	for _, client := range clients {
		if client.needs(query, reading) {
			recipients = append(recipients, client)
			continue
		}
		// It already has this reading, but its charts still have to advance to
		// this tick, so it is told the query is steady rather than left silent.
		steady.add(client, query)
	}
	if len(recipients) == 0 {
		return
	}

	data, err := json.Marshal(ServerMessage{
		Type:      MessageTypeMetricUpdate,
		Query:     query,
		Metrics:   metrics,
		Timestamp: time.Now().Unix(),
	})
	if err != nil {
		log.Printf("Error marshaling message: %v", err)
		return
	}

	// This runs outside the hub lock, so a client may disconnect at any point
	// during the loop; trySend handles that rather than panicking.
	for _, client := range recipients {
		if client.trySend(data) {
			continue
		}

		// A dropped frame means the browser never received this reading, so
		// the record of what it has must not claim otherwise -- the next
		// round has to try again.
		client.forget(query)

		// One line per slow client per query per tick is itself a load at
		// fleet scale, and stdlib logging serializes on a global lock, so
		// this is gated like the rest of the round.
		h.logger.DebugContext(ctx, "dropped metric update: client is slow or gone", "query", query)
	}
}

// traceResult records what a query returned, when debug logging is on. Every
// sample carries its full label set, so the per-metric trace is skipped as a
// block rather than gated one record at a time.
func (h *Hub) traceResult(ctx context.Context, query string, metrics []MetricValue, clients int) {
	if !h.logger.Enabled(ctx, slog.LevelDebug) {
		return
	}

	h.logger.DebugContext(ctx, "query returned metrics",
		"query", query, "metrics", len(metrics), "clients", clients)
	for i, m := range metrics {
		if i >= 3 { // A sample is enough to see the shape.
			break
		}
		h.logger.DebugContext(ctx, "sample metric",
			"query", query, "index", i, "value", m.Value, "labels", m.Metric)
	}
}

// Subscribe adds a client to a query subscription, reporting whether it was
// accepted.
//
// The client remembers what it subscribed to, which is what lets it be removed
// later without walking the whole hub, and lets its total be capped.
func (h *Hub) Subscribe(client *Client, query string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	if client.subs == nil {
		client.subs = make(map[string]struct{})
	}
	if _, already := client.subs[query]; already {
		return true
	}
	if len(client.subs) >= MaxSubscriptionsPerClient {
		return false
	}
	client.subs[query] = struct{}{}
	h.subscriptions[query] = append(h.subscriptions[query], client)
	return true
}

// Unsubscribe removes a client from a query subscription
func (h *Hub) Unsubscribe(client *Client, query string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.detach(client, query)
	delete(client.subs, query)
	// The card this query fed is gone, so nothing in the browser holds the
	// value any more: coming back to this device has to deliver it again.
	client.forget(query)
}

// removeClient drops a client and everything it was subscribed to, returning
// how many clients remain.
//
// It visits only that client's own subscriptions. Walking every subscription in
// the hub instead — with the write lock held, so streaming and every other
// connect stalled behind it — made one operator refreshing their browser a
// fleet-wide pause once the hub carried tens of thousands of queries.
func (h *Hub) removeClient(client *Client) int {
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, known := h.clients[client]; known {
		delete(h.clients, client)
		client.close()
	}

	for query := range client.subs {
		h.detach(client, query)
	}
	client.subs = nil

	client.seenMu.Lock()
	client.seen = nil
	client.seenMu.Unlock()

	return len(h.clients)
}

// detach removes one client from one query's subscriber list. The caller holds
// the write lock. An emptied list is deleted rather than left behind, so a hub
// that has seen a lot of dashboards does not keep growing.
func (h *Hub) detach(client *Client, query string) {
	clients := h.subscriptions[query]
	for i, c := range clients {
		if c != client {
			continue
		}
		clients = append(clients[:i], clients[i+1:]...)
		if len(clients) == 0 {
			delete(h.subscriptions, query)
			h.steady.forget(query)
			return
		}
		h.subscriptions[query] = clients
		return
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

	// subs is every query this client is subscribed to. It is guarded by the
	// hub's lock, not the one above, because the hub is what mutates it.
	subs map[string]struct{}

	// seen records the fingerprint of the last result delivered for each query,
	// which is how an unchanged result is recognized and skipped. Streaming
	// rounds touch it from several goroutines at once, so it has a lock of its
	// own rather than sharing the one that guards closing.
	seenMu sync.Mutex
	seen   map[string]uint64
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
			var refused int
			for _, query := range msg.Queries {
				if !c.hub.Subscribe(c, query) {
					refused++
				}
			}
			if refused > 0 {
				c.refuse(refused)
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
