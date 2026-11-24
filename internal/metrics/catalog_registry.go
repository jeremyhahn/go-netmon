package metrics

import (
	"sort"
	"sync"
	"time"
)

// publishedMetricRetention is how long a name stays published after its last write; longer than any poll interval.
const publishedMetricRetention = 24 * time.Hour

// catalogFamilies are the catalog's own metric names, excluded from the published set.
var catalogFamilies = map[string]bool{
	catalogMetricName:     true,
	catalogEnumMetricName: true,
}

// publishedMetrics records the metric names written to Prometheus, keyed by name only so it does not grow with the fleet.
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

// record notes the names in a batch after a successful send, locking once per flush.
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

// snapshot returns the names published within the retention window, sorted, and evicts the rest.
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
