package websocket

import (
	"encoding/binary"
	"encoding/json"
	"hash/fnv"
	"math"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Recognizing a result the browser already has.
//
// A subscription is re-queried on every streaming round, but a device is polled
// every 30 to 60 seconds, so most rounds read back exactly the reading they
// read last time. Sending it again costs a serialization, a frame, and a
// browser wake-up to plot a point the chart is already making.

// fingerprint summarizes a query's result.
//
// Equal fingerprints mean the reading has not moved: same rows, same numbers.
// It is what the backoff below counts, and it answers the same question the
// delta does -- but it answers it in one number, cheaply, for a query with no
// subscribers left to diff for.
//
// The timestamp is deliberately excluded. An instant query is evaluated at the
// moment it is asked, and Prometheus stamps the result with that evaluation
// time rather than with the scrape the value came from -- so the same unchanged
// reading arrives with a later timestamp every five seconds. Including it made
// every round look like a change and the filter never fired once.
//
// This costs the charts nothing: a steady query is still announced every round
// by MessageTypeMetricsUnchanged, and the browser carries the reading it holds
// forward to that tick. What is saved is the numbers, not the notification.
//
// Per-series hashes are summed, so the same samples in a different order
// fingerprint the same: Prometheus does not promise an order, and treating a
// reordering as a change would defeat the whole thing.
func fingerprint(metrics []MetricValue) uint64 {
	// A distinct value for "no samples", so an empty result is not confused
	// with a result whose series happen to cancel out.
	if len(metrics) == 0 {
		return 0x9E3779B97F4A7C15
	}

	var (
		sum  uint64
		keys []string
	)
	for _, metric := range metrics {
		h := fnv.New64a()

		keys = keys[:0]
		for key := range metric.Metric {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			_, _ = h.Write([]byte(key))
			_, _ = h.Write([]byte{0})
			_, _ = h.Write([]byte(metric.Metric[key]))
			_, _ = h.Write([]byte{0})
		}

		// The bit pattern rather than the decimal form: it is exact, and it
		// distinguishes values that would print the same.
		var scratch [8]byte
		binary.LittleEndian.PutUint64(scratch[:], math.Float64bits(metric.Value))
		_, _ = h.Write(scratch[:])

		sum += h.Sum64()
	}

	// Fold in the count so that a series vanishing cannot be masked by the
	// arithmetic of the sum.
	return sum ^ (uint64(len(metrics)) * 0x9E3779B97F4A7C15)
}

// SteadyRoundsBeforeBackoff is how many consecutive rounds a query must read
// the same thing before it is re-queried at half rate.
//
// The tick is 5 seconds and no device here is polled faster than 30, so most
// rounds ask Prometheus a question whose answer cannot have changed. Backing
// off halves that.
//
// It only ever applies to a reading that has already held still for this many
// rounds, so a value that is moving is never delayed: the cost falls entirely
// on values that have demonstrably stopped. When one does change, the query
// returns to every round immediately.
const SteadyRoundsBeforeBackoff = 6

// steadyQueryRounds tracks how long each query has read the same thing.
//
// It lives on the hub rather than the client because the question -- has this
// reading moved? -- is about Prometheus, not about who is watching.
type steadyQueryRounds struct {
	mu     sync.Mutex
	rounds map[string]int
	last   map[string]uint64
}

func newSteadyQueryRounds() *steadyQueryRounds {
	return &steadyQueryRounds{rounds: map[string]int{}, last: map[string]uint64{}}
}

// due reports whether a query should be asked this round.
func (s *steadyQueryRounds) due(query string, round int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.rounds[query] < SteadyRoundsBeforeBackoff {
		return true
	}
	// Half rate, phase-independent: which rounds a query lands on depends only
	// on the round counter, so the backed-off queries stay spread out rather
	// than all falling due together.
	return round%2 == 0
}

// observe records what a query read, and reports whether it moved.
func (s *steadyQueryRounds) observe(query string, reading uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	previous, seen := s.last[query]
	s.last[query] = reading
	if seen && previous == reading {
		s.rounds[query]++
		return false
	}
	s.rounds[query] = 0
	return true
}

// forget drops a query nobody subscribes to any more.
func (s *steadyQueryRounds) forget(query string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.rounds, query)
	delete(s.last, query)
}

// held is what a client has for one query: the sequence, and the epoch that
// sequence was recorded in.
//
// The epoch is what makes the record safe to write from a streaming goroutine
// while the client's own read loop can clear it at any moment. A frame is
// queued and its sequence recorded in two steps, and between them the browser
// can say it has lost its place: without the epoch, the record written second
// would quietly overwrite the clearing that happened first, and the server
// would go on believing a client is current while the browser has already
// given up on that frame.
type held struct {
	seq   uint64
	epoch uint64
}

// at reports what this client holds for a query. A zero sequence means it has
// never been sent one.
//
// The record is per client rather than per query because the readings are the
// same for everyone and only what each has already SEEN differs. It is what
// decides whether a client can be handed the delta the round already computed
// or has to be handed the whole reading.
func (c *Client) at(query string) held {
	c.seenMu.Lock()
	defer c.seenMu.Unlock()

	return c.seen[query]
}

// mark records that a client has been sent a query at a sequence, unless it
// gave up on that query in the meantime.
//
// Optimistic about delivery: the sequence is recorded when the frame is handed
// to the client's send queue, not when the browser applies it. A frame that
// cannot be queued calls forget instead, a connection that dies after queueing
// takes the client with it, and a browser that could not apply a frame says so
// -- and this is the case that needs the epoch, because that message is
// processed by another goroutine that may run between the send and this call.
func (c *Client) mark(query string, at held) {
	c.seenMu.Lock()
	defer c.seenMu.Unlock()

	if c.seen == nil {
		c.seen = make(map[string]held)
	}
	// Something cleared this query while the frame was on its way to the
	// queue. Whatever it decided stands: recording delivery over it is how the
	// server comes to believe a browser holds a frame it has already rejected.
	if c.seen[query].epoch != at.epoch {
		return
	}
	c.seen[query] = at
}

// forget drops what this client was believed to have for a query, so the next
// round sends it whole.
//
// Used when a frame could not be delivered and when a client says it has lost
// its place. Both mean the same thing: the server no longer knows what this
// browser is holding, and the only safe answer to that is the entire reading.
//
// The entry stays, holding a raised epoch, because a delivery for the previous
// one may still be on its way to the send queue and must not be able to write
// itself back in. discard is the version that reclaims the memory, for a query
// the client has finished with entirely.
func (c *Client) forget(query string) {
	c.seenMu.Lock()
	defer c.seenMu.Unlock()

	if c.seen == nil {
		c.seen = make(map[string]held)
	}
	// The epoch advances rather than the entry being deleted, so a delivery
	// already in flight for the OLD epoch cannot write itself back in.
	c.seen[query] = held{epoch: c.seen[query].epoch + 1}
}

// discard drops a query's record entirely.
//
// Used when the client subscribes or unsubscribes, both of which mean it is
// new to the query whatever it held before. Unlike forget it reclaims the
// entry, which is what stops the record growing for the life of a session as
// an operator moves between devices all day.
//
// A delivery already in flight can recreate the entry after this, leaving one
// stale record for a query the client never returns to. Subscribing to it again
// clears it, and it is one map entry either way -- worth naming, not worth
// serializing the streaming path against the hub lock to prevent.
func (c *Client) discard(query string) {
	c.seenMu.Lock()
	defer c.seenMu.Unlock()

	delete(c.seen, query)
}

// refuse tells a client that some of its subscriptions were declined.
//
// Saying so matters: a page whose subscriptions were silently dropped shows
// cards that wait forever for data nobody is collecting, which reads as a
// broken device rather than as a limit having been reached.
func (c *Client) refuse(count int) {
	data, err := json.Marshal(ServerMessage{
		Type: MessageTypeError,
		Error: "subscription limit reached: " + strconv.Itoa(count) +
			" queries were refused; a client may hold at most " +
			strconv.Itoa(MaxSubscriptionsPerClient),
		Timestamp: time.Now().Unix(),
	})
	if err != nil {
		return
	}
	c.trySend(data)
}
