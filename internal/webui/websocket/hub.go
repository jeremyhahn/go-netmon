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

	// states holds the last reading of every subscribed query, and the
	// sequence number that names it. It is what makes the diff a
	// once-per-round job rather than a once-per-client one.
	states *queryStates

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
	MessageTypeSubscribe   = "subscribe"
	MessageTypeUnsubscribe = "unsubscribe"
	// MessageTypeMetricSnapshot carries a query's WHOLE reading, with the
	// sequence number that names it. It is what a client gets when it is new
	// to a query, when it has fallen behind, and periodically regardless, so
	// that nothing can drift indefinitely.
	MessageTypeMetricSnapshot = "metric_snapshot"

	// MessageTypeMetricDelta carries only what changed, and names the sequence
	// it must be applied to. A client at any other sequence must discard it
	// and ask for a snapshot: applying a delta to something other than what it
	// was computed against is the one way a stale value could render as a
	// current one.
	MessageTypeMetricDelta = "metric_delta"

	// MessageTypeMetricsUnchanged names the queries that were re-read this
	// round and found to be exactly as the client already has them.
	//
	// The dashboard is a tick-based live view: a chart has to keep advancing
	// while a value holds steady, or its line stops at the last change and
	// reads as data having stopped. Sending the whole result again to say
	// "still 37" is what this replaces -- the browser has the reading, so one
	// frame naming the queries lets it carry every one of them forward.
	MessageTypeMetricsUnchanged = "metrics_unchanged"

	// MessageTypeResync is a client saying it has lost its place -- a delta
	// whose base sequence it does not hold. The server forgets what it
	// believed that client had, and the next round sends the whole reading.
	//
	// The server already resends on a frame it could not queue; this is the
	// half it cannot see, where the frame was queued, the socket accepted it
	// and the browser never applied it.
	MessageTypeResync = "resync"

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

	// Seq names the reading this frame leaves the client holding.
	Seq uint64 `json:"seq,omitempty"`

	// BaseSeq is the reading a delta applies to. A client holding anything
	// else must not apply it.
	BaseSeq uint64 `json:"base_seq,omitempty"`

	// Delta is what changed, on a delta frame.
	Delta *Delta `json:"delta,omitempty"`

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
		states:        newQueryStates(),
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
	h.forgetUnsubscribed()
}

// forgetUnsubscribed drops the state of queries nobody watches any more.
//
// Unsubscribing already does this, but it can happen WHILE the round is
// running: detach deletes the state, the query that was already in flight
// returns, and advance puts it back. Nothing would ever take it out again,
// because the removal only happens on the transition to zero subscribers and
// that has already been and gone. Sweeping after the round is what makes the
// bound hold whenever the last reader leaves mid-round.
func (h *Hub) forgetUnsubscribed() {
	h.mu.RLock()
	watched := make(map[string]bool, len(h.subscriptions))
	for query := range h.subscriptions {
		watched[query] = true
	}
	h.mu.RUnlock()

	for _, query := range h.states.queries() {
		if !watched[query] {
			h.states.forget(query)
		}
	}
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

// streamQuery runs one subscribed query and delivers what changed.
//
// The diff happens HERE, once, and the frames it produces are shared by every
// client that can take them. Diffing per client would be the same work
// multiplied by the number of people looking, and the readings are identical
// for all of them: it is only what each has already seen that differs, and that
// is a sequence number rather than a comparison.
func (h *Hub) streamQuery(ctx context.Context, query string, clients []*Client, steady *steadyQueries) {
	metrics, err := h.queryFunc(ctx, query)
	if err != nil {
		log.Printf("Error querying Prometheus for %s: %v", query, err)
		return
	}

	h.traceResult(ctx, query, metrics, len(clients))
	h.steady.observe(query, fingerprint(metrics))

	// One diff for everyone.
	next := h.states.advance(query, metrics)

	if !next.Changed {
		// Nothing moved, so nobody needs a frame -- but every chart still has
		// to reach this tick, or its line stops at the last change and reads
		// as data having stopped. The clients that are up to date are told the
		// query is steady; any that are not still get the reading.
		for _, client := range clients {
			if client.at(query).seq == next.Seq {
				steady.add(client, query)
				continue
			}
			h.sendSnapshot(ctx, client, query, next)
		}
		return
	}

	// The two frames this round can send, marshaled at most once each however
	// many clients take them.
	var snapshot, delta []byte

	for _, client := range clients {
		at := client.at(query)

		if at.seq == next.Seq {
			// Already has this reading: a client that subscribed twice, or one
			// whose frame this round is a repeat.
			steady.add(client, query)
			continue
		}

		// A delta is only ever applied to the reading it was computed
		// against. Every other client -- new, behind, or one frame short of an
		// answer that never arrived -- gets the whole thing, which is what
		// makes it impossible for a dropped frame to leave a stale value
		// rendering as a current one.
		if at.seq == next.BaseSeq && !next.Snapshot {
			if delta == nil {
				delta = h.encode(ctx, ServerMessage{
					Type:      MessageTypeMetricDelta,
					Query:     query,
					Seq:       next.Seq,
					BaseSeq:   next.BaseSeq,
					Delta:     &next.Delta,
					Timestamp: time.Now().Unix(),
				})
				if delta == nil {
					return
				}
			}
			h.deliver(ctx, client, query, held{seq: next.Seq, epoch: at.epoch}, delta, "delta")
			continue
		}

		if snapshot == nil {
			snapshot = h.encode(ctx, ServerMessage{
				Type:      MessageTypeMetricSnapshot,
				Query:     query,
				Seq:       next.Seq,
				Metrics:   metrics,
				Timestamp: time.Now().Unix(),
			})
			if snapshot == nil {
				return
			}
		}
		h.deliver(ctx, client, query, held{seq: next.Seq, epoch: at.epoch}, snapshot, "snapshot")
	}
}

// sendSnapshot hands one client the whole current reading of a query.
//
// Used on the unchanged path, where there is no shared frame to reuse because
// almost nobody needs one: a client that is behind on a query that has not
// moved is either newly subscribed or recovering from a dropped frame.
func (h *Hub) sendSnapshot(ctx context.Context, client *Client, query string, next update) {
	at := client.at(query)
	reading := h.states.reading(query)
	if reading == nil {
		return
	}
	frame := h.encode(ctx, ServerMessage{
		Type:      MessageTypeMetricSnapshot,
		Query:     query,
		Seq:       next.Seq,
		Metrics:   reading,
		Timestamp: time.Now().Unix(),
	})
	if frame == nil {
		return
	}
	h.deliver(ctx, client, query, held{seq: next.Seq, epoch: at.epoch}, frame, "snapshot")
}

// encode marshals a frame, or reports the failure and returns nil.
func (h *Hub) encode(ctx context.Context, message ServerMessage) []byte {
	data, err := json.Marshal(message)
	if err != nil {
		h.logger.ErrorContext(ctx, "could not marshal a metric frame",
			"type", message.Type, "query", message.Query, "error", err)
		return nil
	}
	return data
}

// deliver sends one frame and records what the client now holds.
//
// This runs outside the hub lock, so a client may disconnect at any point
// during the loop; trySend handles that rather than panicking.
func (h *Hub) deliver(ctx context.Context, client *Client, query string, at held, frame []byte, kind string) {
	if client.trySend(frame) {
		client.mark(query, at)
		return
	}

	// The browser never received this, so the record of what it holds must not
	// claim otherwise. Forgetting it means the next round sends the whole
	// reading rather than a delta against something that never arrived.
	client.forget(query)

	// One line per slow client per query per tick is itself a load at fleet
	// scale, and stdlib logging serializes on a global lock, so this is gated
	// like the rest of the round.
	h.logger.DebugContext(ctx, "dropped a metric frame: client is slow or gone",
		"query", query, "frame", kind)
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
	// New to this query whatever it held before -- including a record a
	// delivery in flight during the last unsubscribe may have left behind.
	client.discard(query)
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
	client.discard(query)
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
			// The sequence goes with the last subscriber. A query subscribed
			// again starts from one and every client is new to it, so nobody
			// can hold a sequence from before the gap and be told it is still
			// current.
			h.states.forget(query)
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

	// seen records the sequence of the last reading delivered for each query,
	// which is what decides whether this client can take a delta or needs the
	// whole thing. Streaming rounds touch it from several goroutines at once
	// and the client's own read loop clears it, so it has a lock of its own
	// rather than sharing the one that guards closing.
	seenMu sync.Mutex
	seen   map[string]held
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
		case MessageTypeResync:
			// The client says it cannot apply what it was sent. Forgetting
			// what it was believed to hold is the whole of the answer: the
			// next round finds it behind and sends the reading whole.
			for _, query := range msg.Queries {
				c.forget(query)
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
