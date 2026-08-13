package websocket

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// What the hub costs when a room full of operators is watching.
//
// Every tick, the hub asked Prometheus about every subscribed query, one
// goroutine per query, with nothing bounding how many ran at once and nothing
// stopping the next tick from starting while the last was still going. A
// dashboard subscribes ~22 queries, so 25 browsers on different devices is ~550
// queries a tick; if Prometheus takes longer than the tick to answer them, the
// next tick piles another 550 on top, and the pile never drains.
//
// These tests pin the two properties that keep it bounded: a ceiling on how
// many queries are in flight at once, and no overlapping ticks.

// slowQuery answers after delay, recording how many calls overlap.
type slowQuery struct {
	delay time.Duration

	inFlight atomic.Int64
	peak     atomic.Int64
	calls    atomic.Int64
}

func (q *slowQuery) fn() QueryFunc {
	return func(ctx context.Context, _ string) ([]MetricValue, error) {
		q.calls.Add(1)
		now := q.inFlight.Add(1)
		defer q.inFlight.Add(-1)

		for {
			peak := q.peak.Load()
			if now <= peak || q.peak.CompareAndSwap(peak, now) {
				break
			}
		}

		select {
		case <-time.After(q.delay):
		case <-ctx.Done():
		}
		return nil, nil
	}
}

// subscribeMany wires n distinct queries, as a room of browsers would.
func subscribeMany(h *Hub, n int) {
	client := &Client{send: make(chan []byte, 4096)}
	for i := range n {
		h.Subscribe(client, "query_"+itoa(i))
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}

// TestStreamBoundsQueriesInFlight is the ceiling. Without it a tick opens one
// Prometheus request per subscribed query at once, which at fleet scale is
// thousands of concurrent requests against a single Prometheus.
func TestStreamBoundsQueriesInFlight(t *testing.T) {
	q := &slowQuery{delay: 20 * time.Millisecond}
	h := NewHub(q.fn(), nil)

	const queries = 400
	subscribeMany(h, queries)

	h.streamMetrics(context.Background())

	if peak := q.peak.Load(); peak > int64(MaxQueriesInFlight) {
		t.Errorf("%d queries ran at once, above the ceiling of %d; a tick must not open one request per subscription",
			peak, MaxQueriesInFlight)
	}
	if got := q.calls.Load(); got != queries {
		t.Errorf("ran %d queries, want all %d: bounding concurrency must not drop work", got, queries)
	}
}

// TestStreamDoesNotOverlapItself is the pile-up. A tick that is still running
// when the next fires must not start a second round: the queries would stack
// until the process runs out of memory or Prometheus stops answering.
func TestStreamDoesNotOverlapItself(t *testing.T) {
	q := &slowQuery{delay: 60 * time.Millisecond}
	h := NewHub(q.fn(), nil)
	subscribeMany(h, 8)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Three ticks arriving faster than one round can finish.
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.streamMetrics(ctx)
		}()
		time.Sleep(5 * time.Millisecond)
	}
	wg.Wait()

	// Two of the three ticks should have found a round already running and
	// stepped aside, so the work done is one round's worth, not three.
	if got := q.calls.Load(); got > 8*2 {
		t.Errorf("ran %d queries across three overlapping ticks; want about one round of 8, so ticks are not stacking", got)
	}
}

// TestUnsubscribeIsNotFleetWide covers the disconnect path. Removing a client
// walked every subscription in the hub, holding the write lock throughout, so a
// page refresh by one operator stalled streaming for everyone. A client must
// cost only its own subscriptions to remove.
func TestUnsubscribeIsNotFleetWide(t *testing.T) {
	h := NewHub(func(context.Context, string) ([]MetricValue, error) { return nil, nil }, nil)

	// One busy hub: many queries belonging to other people.
	other := &Client{send: make(chan []byte, 16)}
	for i := range 5000 {
		h.Subscribe(other, "background_"+itoa(i))
	}

	leaving := &Client{send: make(chan []byte, 16)}
	for i := range 4 {
		h.Subscribe(leaving, "mine_"+itoa(i))
	}

	h.removeClient(leaving)

	h.mu.RLock()
	defer h.mu.RUnlock()

	for i := range 4 {
		if got := len(h.subscriptions["mine_"+itoa(i)]); got != 0 {
			t.Errorf("mine_%d still has %d subscribers after the client left", i, got)
		}
	}
	// Everyone else's subscriptions are untouched.
	if got := len(h.subscriptions["background_0"]); got != 1 {
		t.Errorf("another client's subscription was disturbed: %d subscribers", got)
	}
}

// BenchmarkClientDisconnect measures the cost of one browser going away on a
// hub carrying a datacenter's worth of subscriptions.
func BenchmarkClientDisconnect(b *testing.B) {
	h := NewHub(func(context.Context, string) ([]MetricValue, error) { return nil, nil }, nil)

	other := &Client{send: make(chan []byte, 16)}
	for i := range 20000 {
		h.Subscribe(other, "background_"+itoa(i))
	}

	clients := make([]*Client, b.N)
	for i := range clients {
		clients[i] = &Client{send: make(chan []byte, 16)}
		for j := range 22 {
			h.Subscribe(clients[i], "session_"+itoa(i)+"_"+itoa(j))
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.removeClient(clients[i])
	}
}

// TestSubscriptionsAreCappedPerClient bounds what one session can ask the
// server to do on its behalf. Every subscription is re-queried on every round,
// so an unbounded count lets one browser set the Prometheus load for everyone.
func TestSubscriptionsAreCappedPerClient(t *testing.T) {
	h := NewHub(func(context.Context, string) ([]MetricValue, error) { return nil, nil }, nil)
	client := &Client{send: make(chan []byte, 16)}

	for i := range MaxSubscriptionsPerClient {
		if !h.Subscribe(client, "query_"+itoa(i)) {
			t.Fatalf("subscription %d was refused below the cap of %d", i, MaxSubscriptionsPerClient)
		}
	}

	if h.Subscribe(client, "one_too_many") {
		t.Errorf("a client was allowed past %d subscriptions", MaxSubscriptionsPerClient)
	}

	// Re-subscribing to something it already holds is not a new subscription
	// and must keep working, or a reconnect would be refused at the cap.
	if !h.Subscribe(client, "query_0") {
		t.Error("re-subscribing to a query the client already holds was refused")
	}

	// Releasing one makes room again.
	h.Unsubscribe(client, "query_0")
	if !h.Subscribe(client, "one_too_many") {
		t.Error("a slot freed by unsubscribing was not reusable")
	}
}

// TestARefusedSubscriptionIsReported is the bad-input half: a client that asked
// for more than it may have must be told, not silently left with cards that
// wait forever for data nobody is collecting.
func TestARefusedSubscriptionIsReported(t *testing.T) {
	h := NewHub(func(context.Context, string) ([]MetricValue, error) { return nil, nil }, nil)
	client := &Client{send: make(chan []byte, 4)}

	for i := range MaxSubscriptionsPerClient {
		h.Subscribe(client, "query_"+itoa(i))
	}
	// Drain whatever the successful subscriptions produced.
	for len(client.send) > 0 {
		<-client.send
	}

	if h.Subscribe(client, "one_too_many") {
		t.Fatal("the cap did not hold")
	}
	client.refuse(1)

	select {
	case raw := <-client.send:
		var msg ServerMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			t.Fatalf("decoding refusal: %v", err)
		}
		if msg.Type != MessageTypeError {
			t.Errorf("refusal arrived as %q, want %q", msg.Type, MessageTypeError)
		}
		if !strings.Contains(msg.Error, "subscription limit") {
			t.Errorf("refusal said %q, which does not name the limit", msg.Error)
		}
	default:
		t.Error("nothing was sent to say the subscription was refused")
	}
}
