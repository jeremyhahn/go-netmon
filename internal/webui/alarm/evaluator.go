package alarm

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/logging"
)

// DefaultInterval is how often the rules are evaluated.
const DefaultInterval = 15 * time.Second

// Evaluator runs the rules and holds what is firing; rule sets are swapped whole and readers never lock.
type Evaluator struct {
	query    QueryFunc
	interval time.Duration
	now      func() time.Time
	logger   *slog.Logger

	// rules is the current rule set. Replaced by Reload, read by every
	// evaluation.
	rules atomic.Pointer[[]Rule]

	// firing is the current answer, published as one immutable snapshot.
	firing atomic.Pointer[Snapshot]

	// generation counts rule-set swaps so a stale evaluation discards itself; written under mu.
	generation atomic.Uint64

	// raised remembers when each alarm ID was first seen, so an alarm that
	// stays up keeps its Since. It is only touched by the evaluation loop,
	// which is single-threaded, but Reload can run concurrently with it.
	mu     sync.Mutex
	raised map[string]time.Time

	// evalMu serializes evaluations; it is separate from mu, which Reload takes.
	evalMu sync.Mutex
}

// New builds an evaluator over query. A nil logger discards its records.
func New(query QueryFunc, rules []Rule, logger *slog.Logger) *Evaluator {
	e := &Evaluator{
		query:    query,
		interval: DefaultInterval,
		now:      time.Now,
		logger:   logging.OrDiscard(logger),
		raised:   map[string]time.Time{},
	}
	e.Reload(rules)
	e.publish(Snapshot{Alarms: []Alarm{}})
	return e
}

// Reload replaces the rule set; an evaluation in flight finishes with its original rules.
func (e *Evaluator) Reload(rules []Rule) {
	set := make([]Rule, len(rules))
	copy(set, rules)

	// Bump the generation under the lock commit uses.
	e.mu.Lock()
	e.rules.Store(&set)
	e.generation.Add(1)
	e.mu.Unlock()

	e.logger.Info("alarm rules loaded", "rules", len(set))
}

// RuleCount is how many rules are loaded.
func (e *Evaluator) RuleCount() int {
	if rules := e.rules.Load(); rules != nil {
		return len(*rules)
	}
	return 0
}

// Firing returns current alarms, worst first; the slice is immutable.
func (e *Evaluator) Firing() []Alarm {
	return e.Snapshot().Alarms
}

// Snapshot is the whole of the last evaluation, including what it could not
// find out.
func (e *Evaluator) Snapshot() Snapshot {
	if held := e.firing.Load(); held != nil {
		return *held
	}
	return Snapshot{}
}

// EvaluateOnce runs every rule once and publishes the result; Run calls it on each tick.
func (e *Evaluator) EvaluateOnce(ctx context.Context) {
	e.evaluate(ctx)
}

// Run evaluates the rules until ctx is done.
func (e *Evaluator) Run(ctx context.Context) {
	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()

	// Once immediately, so a browser opened seconds after a restart is not
	// told everything is fine when it is not.
	e.evaluate(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.evaluate(ctx)
		}
	}
}

// evaluate runs every rule once and publishes what is firing.
func (e *Evaluator) evaluate(ctx context.Context) {
	e.evalMu.Lock()
	defer e.evalMu.Unlock()

	// The generation this evaluation belongs to; a reload during it makes the result stale.
	generation := e.generation.Load()

	rules := e.rules.Load()
	if rules == nil || len(*rules) == 0 {
		// No rules is no alarms, and nothing remembered about any.
		e.commit(generation, map[string]time.Time{}, Snapshot{Alarms: []Alarm{}})
		return
	}

	now := e.now()
	held := e.Snapshot()
	since := e.sinceHeld()
	pending := make([]Alarm, 0, len(*rules))
	// next becomes raised only if this evaluation publishes.
	next := make(map[string]time.Time, len(*rules))
	unevaluated := 0

	for _, rule := range *rules {
		samples, err := e.query(ctx, rule.Expr)
		if err != nil {
			// A rule that cannot be evaluated keeps its last alarms and is counted as unevaluated.
			e.logger.Error("alarm rule could not be evaluated",
				"rule", rule.Name, "error", err)
			unevaluated++
			for _, alarm := range held.Alarms {
				if alarm.Rule == rule.Name {
					next[alarm.ID] = alarm.Since
					pending = append(pending, alarm)
				}
			}
			continue
		}

		for _, sample := range samples {
			alarm := newAlarm(rule, sample)
			alarm.Since = now
			if at, known := since[alarm.ID]; known {
				alarm.Since = at
			}
			next[alarm.ID] = alarm.Since

			// For is what separates a condition from a blip. Until it has
			// held that long the alarm is pending: remembered, not shown.
			if now.Sub(alarm.Since) < rule.For {
				continue
			}
			pending = append(pending, alarm)
		}
	}

	sortAlarms(pending)
	e.commit(generation, next, Snapshot{Alarms: pending, Unevaluated: unevaluated})
}

// commit publishes a result, unless the rules changed while it was being
// produced. The next tick will answer against the rules that are loaded now.
func (e *Evaluator) commit(generation uint64, raised map[string]time.Time, snapshot Snapshot) {
	// Logged outside the lock. A slog handler is caller-supplied code, and
	// running it inside the critical section makes the section only as
	// reentrancy-safe as whatever was passed in.
	stale := false
	defer func() {
		if stale {
			e.logger.Debug("discarding an evaluation the rules changed under")
		}
	}()

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.generation.Load() != generation {
		stale = true
		return
	}
	// Replacing the map rather than deleting from it is what forgets an alarm
	// that has cleared: an ID this evaluation did not see is simply not in the
	// map it built.
	e.raised = raised
	e.publish(snapshot)
}

// sinceHeld copies when each alarm ID was first raised.
func (e *Evaluator) sinceHeld() map[string]time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()

	held := make(map[string]time.Time, len(e.raised))
	for id, at := range e.raised {
		held[id] = at
	}
	return held
}

// publish makes a new result visible to readers.
func (e *Evaluator) publish(snapshot Snapshot) {
	e.firing.Store(&snapshot)
}
