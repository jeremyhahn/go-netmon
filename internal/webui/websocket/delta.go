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

// fingerprint summarizes a query result as an order-independent sum of per-series hashes, excluding timestamps.
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

// observe records what a query read, extending or ending its steady streak.
func (s *steadyQueryRounds) observe(query string, reading uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	previous, seen := s.last[query]
	s.last[query] = reading
	if seen && previous == reading {
		s.rounds[query]++
		return
	}
	s.rounds[query] = 0
}

// wake clears a query's steady streak because a new subscriber needs a value.
func (s *steadyQueryRounds) wake(query string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.rounds, query)
}

// queries returns every query this tracker holds anything for, for the post-round sweep.
func (s *steadyQueryRounds) queries() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Both maps: last is written on every ask, rounds only once steady.
	held := make([]string, 0, len(s.last))
	seen := make(map[string]struct{}, len(s.last))
	for query := range s.last {
		seen[query] = struct{}{}
		held = append(held, query)
	}
	for query := range s.rounds {
		if _, already := seen[query]; already {
			continue
		}
		seen[query] = struct{}{}
		held = append(held, query)
	}
	return held
}

// forget drops a query nobody subscribes to any more.
func (s *steadyQueryRounds) forget(query string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.rounds, query)
	delete(s.last, query)
}

// held is a client's sequence for one query and the epoch it was recorded in, so a resync cannot be overwritten.
type held struct {
	seq   uint64
	epoch uint64
}

// at reports what this client holds for a query; zero means never sent.
func (c *Client) at(query string) held {
	c.seenMu.Lock()
	defer c.seenMu.Unlock()

	return c.seen[query]
}

// mark records a client was sent a query at a sequence, unless it gave up on that query since.
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

// forget makes the next round send a query whole to this client, raising the epoch.
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

// discard drops a query's record entirely, on subscribe or unsubscribe.
func (c *Client) discard(query string) {
	c.seenMu.Lock()
	defer c.seenMu.Unlock()

	delete(c.seen, query)
}

// refuse tells a client that some of its subscriptions were declined.
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
