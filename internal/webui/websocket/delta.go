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

// needs reports whether this client still has to be sent this result, and
// records that it is about to be.
//
// The record is per client rather than per query because a browser that has
// just opened a dashboard needs the current value even when it has not moved
// for an hour and every other session already has it.
func (c *Client) needs(query string, fingerprint uint64) bool {
	c.seenMu.Lock()
	defer c.seenMu.Unlock()

	if seen, ok := c.seen[query]; ok && seen == fingerprint {
		return false
	}
	if c.seen == nil {
		c.seen = make(map[string]uint64)
	}
	c.seen[query] = fingerprint
	return true
}

// forget drops what this client was believed to have for a query, so the next
// round sends it again. It is used when a frame could not be delivered and when
// a client unsubscribes.
func (c *Client) forget(query string) {
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
