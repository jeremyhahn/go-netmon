package websocket

import (
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Sending what changed, once, to everyone who is up to date.
//
// A subscribed query is re-read on every round and its result is usually the
// same one it read last time, because a device is polled every 30 to 60 seconds
// and the round is five. When it does change, usually one series in it moved
// and the rest did not -- a panel of forty interfaces where one is passing
// traffic sends forty readings to say so.
//
// The diff is computed ONCE per query per round, against the previous result,
// and the same frame is sent to every client that holds that previous result.
// Diffing per client would be the same work multiplied by the number of people
// looking, which is exactly backwards: the readings are identical for all of
// them and it is only what each has already SEEN that differs.
//
// What makes that safe is the sequence number. Every result a query has ever
// produced has one, and a delta names the sequence it must be applied to. A
// client at that sequence can take the delta; a client at any other sequence --
// new, behind, or one frame short of an answer that never arrived -- is sent
// the whole reading instead. There is no path by which a client applies a delta
// to something other than what it was computed against, so there is no path by
// which it renders a stale value as a current one.

// SnapshotInterval is how many changed frames a query may send as deltas before
// one is sent whole.
//
// It bounds drift that nothing else would catch. A client whose sequence is
// somehow right while its contents are not -- a rendering bug, a frame the
// browser accepted and mishandled -- would otherwise never be corrected, since
// every subsequent frame would be a delta it believes it can apply. Twenty
// changed frames on a 30-second poll interval is a full reading roughly every
// ten minutes, which is cheap and is the only thing standing between a subtle
// client-side bug and a panel that is wrong for as long as the page is open.
const SnapshotInterval = 20

// queryState is what one query last read, and what that reading is called.
type queryState struct {
	// seq increments when the READING changes, not when the query runs. A
	// client holding seq N holds the result that produced N, however many
	// rounds ago that was.
	seq uint64

	// byKey is the reading at seq, indexed by series identity.
	byKey map[string]MetricValue

	// sinceSnapshot counts changed frames sent as deltas since the last whole
	// reading was sent.
	sinceSnapshot int
}

// Delta is what changed between two readings of one query.
type Delta struct {
	// Changed are series that are new or whose value moved. A renderer
	// overwrites its own copy of each by label set.
	Changed []MetricValue `json:"changed,omitempty"`

	// Removed are series that are no longer in the reading -- an interface
	// that went away, a table row that was deleted. They carry their full
	// label set rather than a key, because a key would be a second identity
	// scheme that the server and the browser would have to agree on
	// character for character, in two languages, forever.
	Removed []map[string]string `json:"removed,omitempty"`
}

// Empty reports whether this delta asks a renderer to do nothing.
func (d Delta) Empty() bool {
	return len(d.Changed) == 0 && len(d.Removed) == 0
}

// update is what one round decided to send for one query.
type update struct {
	// Seq is the sequence the reading now has.
	Seq uint64

	// BaseSeq is the sequence a delta applies to. Meaningless when Snapshot.
	BaseSeq uint64

	// Metrics is the whole reading, for the clients that need it.
	Metrics []MetricValue

	// Delta is what changed, for the clients that are at BaseSeq.
	Delta Delta

	// Changed says the reading moved. When it did not, there is nothing to
	// send anyone and the round only has to say the query is still steady.
	Changed bool

	// Snapshot forces the whole reading on everyone, whatever sequence they
	// hold. It is how SnapshotInterval is enforced.
	Snapshot bool
}

// queryStates holds the last reading of every subscribed query.
type queryStates struct {
	mu     sync.Mutex
	states map[string]*queryState
}

func newQueryStates() *queryStates {
	return &queryStates{states: map[string]*queryState{}}
}

// advance records a new reading and reports what to send for it.
//
// This is the once-per-round diff. It runs on the streaming goroutine for one
// query, and its result is fanned to every client subscribed to that query.
func (s *queryStates) advance(query string, metrics []MetricValue) update {
	s.mu.Lock()
	defer s.mu.Unlock()

	current := byKey(metrics)

	state, known := s.states[query]
	if !known {
		// The first reading is a snapshot by definition: there is nothing to
		// express it as a change to.
		s.states[query] = &queryState{seq: 1, byKey: current}
		return update{Seq: 1, Metrics: metrics, Changed: true, Snapshot: true}
	}

	delta := diff(state.byKey, current)
	if delta.Empty() {
		// The reading did not move. Its sequence does not either, so a client
		// that already holds it stays up to date without a frame.
		return update{Seq: state.seq, Changed: false}
	}

	base := state.seq
	state.seq++
	state.byKey = current
	state.sinceSnapshot++

	snapshot := state.sinceSnapshot >= SnapshotInterval
	if snapshot {
		state.sinceSnapshot = 0
	}

	return update{
		Seq:      state.seq,
		BaseSeq:  base,
		Metrics:  metrics,
		Delta:    delta,
		Changed:  true,
		Snapshot: snapshot,
	}
}

// reading returns a query's current whole reading, or nil for one that has
// never been read.
func (s *queryStates) reading(query string) []MetricValue {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, known := s.states[query]
	if !known {
		return nil
	}

	metrics := make([]MetricValue, 0, len(state.byKey))
	for _, metric := range state.byKey {
		metrics = append(metrics, metric)
	}
	// Ordered, so a snapshot of an unchanged reading is byte-identical
	// whenever it is produced.
	sort.Slice(metrics, func(i, j int) bool {
		return seriesKey(metrics[i].Metric) < seriesKey(metrics[j].Metric)
	})
	return metrics
}

// queries names every query a reading is held for.
func (s *queryStates) queries() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	names := make([]string, 0, len(s.states))
	for query := range s.states {
		names = append(names, query)
	}
	return names
}

// forget drops a query nobody subscribes to any more.
//
// Its sequence goes with it. A query that is subscribed again starts from one,
// and every client is new to it, so nobody can hold a sequence from before the
// gap and be told it is still current.
func (s *queryStates) forget(query string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.states, query)
}

// diff reports what changed between two readings.
func diff(previous, current map[string]MetricValue) Delta {
	var delta Delta

	for key, metric := range current {
		held, existed := previous[key]
		// The timestamp is deliberately not compared. An instant query is
		// stamped at the moment it is asked rather than at the scrape the
		// value came from, so an unchanged reading arrives with a later
		// timestamp every five seconds. Comparing it would make every round a
		// change and the whole mechanism a no-op with extra steps.
		if existed && held.Value == metric.Value {
			continue
		}
		delta.Changed = append(delta.Changed, metric)
	}

	for key, metric := range previous {
		if _, still := current[key]; !still {
			delta.Removed = append(delta.Removed, metric.Metric)
		}
	}

	// Sorted so two collectors given the same readings produce byte-identical
	// frames, and so a test can assert on one.
	sort.Slice(delta.Changed, func(i, j int) bool {
		return seriesKey(delta.Changed[i].Metric) < seriesKey(delta.Changed[j].Metric)
	})
	sort.Slice(delta.Removed, func(i, j int) bool {
		return seriesKey(delta.Removed[i]) < seriesKey(delta.Removed[j])
	})
	return delta
}

// byKey indexes a reading by series identity.
func byKey(metrics []MetricValue) map[string]MetricValue {
	indexed := make(map[string]MetricValue, len(metrics))
	for _, metric := range metrics {
		indexed[seriesKey(metric.Metric)] = metric
	}
	return indexed
}

// seriesKey identifies one series by its labels.
//
// Internal to the server: the browser never sees it, which is why a removal
// carries its label set instead. Length-prefixed so the encoding is injective
// -- label values may contain any byte, so no separator can be reserved, and
// two different series must never key the same.
func seriesKey(labels map[string]string) string {
	names := make([]string, 0, len(labels))
	for name := range labels {
		names = append(names, name)
	}
	sort.Strings(names)

	var key strings.Builder
	for _, name := range names {
		key.WriteString(strconv.Itoa(len(name)))
		key.WriteByte(':')
		key.WriteString(name)
		key.WriteString(strconv.Itoa(len(labels[name])))
		key.WriteByte(':')
		key.WriteString(labels[name])
	}
	return key.String()
}
