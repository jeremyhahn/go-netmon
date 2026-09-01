package metrics

import (
	"sort"
	"sync"
	"time"
)

// publishedMetricRetention is how long a metric name stays in the published
// set after it was last written to Prometheus.
//
// Without eviction the set is "everything ever published" rather than "what
// this fleet publishes", and that is the whole rationale for describing only
// published metrics: unassign a MIB or retire a device and the catalog goes on
// describing metrics no device produces, offering panels that can never have
// data. Over a long-lived process the set drifts toward the 9,202 objects the
// loaded modules could produce, which is what publishing-only was chosen to
// avoid.
//
// A day is deliberately far longer than any plausible poll interval. Evicting
// on a shorter horizon would drop a metric from a device polled hourly, and
// although that is self-healing -- the name returns on the device's very next
// successful export, so the cost is a description missing for one poll rather
// than data lost -- a description that flickers is worse than one that lingers.
// The window has to exceed the slowest cadence in the fleet, not the emitter's.
const publishedMetricRetention = 24 * time.Hour

// catalogFamilies are the metric names the catalog itself publishes.
//
// They are excluded from the published set because the catalog describes the
// metrics this collector reads from devices, and it does not describe itself.
// Recording them would be circular, and it corrupted the one number that says
// how good the catalog's coverage is: the emitter's own output flows through
// the same export path as everything else, so on the first tick these entered
// the set, could never be described, and pinned
// gonetmon_catalog_undescribed_metrics at two forever -- on a perfectly
// described fleet.
var catalogFamilies = map[string]bool{
	catalogMetricName:     true,
	catalogEnumMetricName: true,
}

// publishedMetrics records which metric names this collector has actually
// written to Prometheus, so the catalog describes what exists rather than what
// might.
//
// Cataloging every object in every loaded module would describe 9,202 metrics
// on a fleet that publishes a few hundred, and would promise a renderer panels
// for objects no device answers.
//
// It is process-global state, and deliberately so -- the catalog is global. It
// is keyed on the metric name alone, with no device dimension, so its size is
// the number of distinct metrics published across the whole fleet and not a
// multiple of it. That is the property that has to hold at a hundred thousand
// devices, and it holds because nothing here is per-device.
type publishedMetrics struct {
	mu       sync.Mutex
	lastSeen map[string]time.Time

	// now is the clock, injected so tests do not wait for a day to pass.
	now func() time.Time
}

// newPublishedMetrics returns an empty set.
func newPublishedMetrics() *publishedMetrics {
	return &publishedMetrics{
		lastSeen: make(map[string]time.Time),
		now:      time.Now,
	}
}

// record notes the metric names in a batch that reached Prometheus.
//
// It is called after a successful send, not when samples are buffered. A
// sample that was dropped or whose write failed was never published, and
// recording it would have the catalog describe a series that never arrived --
// a panel promising data that does not exist. Failed batches are put back and
// retried, so a name is recorded on the flush that actually lands.
//
// Names are deduplicated before the lock is taken, so the mutex is held once
// per flush rather than once per sample.
func (p *publishedMetrics) record(samples []*Sample) {
	if p == nil || len(samples) == 0 {
		return
	}

	names := make(map[string]bool, 16)
	for _, sample := range samples {
		if sample == nil || catalogFamilies[sample.Name] {
			continue
		}
		names[sample.Name] = true
	}
	if len(names) == 0 {
		return
	}

	seen := p.now()

	p.mu.Lock()
	for name := range names {
		p.lastSeen[name] = seen
	}
	size := len(p.lastSeen)
	catalogPublishedMetrics.Set(float64(size))
	p.mu.Unlock()
}

// snapshot returns the names published within the retention window, in sorted
// order, and forgets the rest.
//
// Eviction happens here because this is what the emitter calls once per tick:
// the set is pruned on the same cadence the catalog is written, so a name
// drops out of the set and out of the catalog in the same breath rather than
// lingering in one and not the other.
func (p *publishedMetrics) snapshot() []string {
	if p == nil {
		return nil
	}

	cutoff := p.now().Add(-publishedMetricRetention)

	p.mu.Lock()
	for name, seen := range p.lastSeen {
		if seen.Before(cutoff) {
			delete(p.lastSeen, name)
		}
	}

	names := make([]string, 0, len(p.lastSeen))
	for name := range p.lastSeen {
		names = append(names, name)
	}
	catalogPublishedMetrics.Set(float64(len(p.lastSeen)))
	p.mu.Unlock()

	sort.Strings(names)
	return names
}

// len reports how many distinct metric names are currently published.
func (p *publishedMetrics) len() int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.lastSeen)
}
