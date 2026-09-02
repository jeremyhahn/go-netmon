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
//
// Slower than the dashboard's five-second tick and for a different reason: an
// alarm is a condition that has held, not a reading, and every rule costs a
// Prometheus query whether or not anything is wrong. Fifteen seconds is well
// inside the shortest poll interval a device here uses, so no state change can
// be missed for longer than one poll.
const DefaultInterval = 15 * time.Second

// Evaluator runs the rules and holds what is currently firing.
//
// The rule set is swapped whole rather than edited, so a reload cannot be
// observed half-applied: an evaluation either uses the rules before it or the
// rules after it. Readers of the firing set take a snapshot pointer and never
// hold a lock, because the bell is polled by every open browser and the
// evaluation loop must not wait on any of them.
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

	// generation counts rule-set swaps. An evaluation that started before a
	// reload must not publish over the one that started after it. It is
	// written under mu -- see Reload -- and read without it at the start of an
	// evaluation, where a slightly stale value only means the evaluation
	// discards itself.
	generation atomic.Uint64

	// raised remembers when each alarm ID was first seen, so an alarm that
	// stays up keeps its Since. It is only touched by the evaluation loop,
	// which is single-threaded, but Reload can run concurrently with it.
	mu     sync.Mutex
	raised map[string]time.Time

	// evalMu serializes evaluations. EvaluateOnce is exported, so a caller
	// forcing one can run alongside Run's tick: both would read the same
	// sightings and the same generation, and whichever finished LAST would
	// publish over the other -- an older answer replacing a newer one, with
	// nothing to detect it, since every access is properly synchronized.
	//
	// It is not mu. A query can call Reload, which takes mu, and an evaluation
	// holding mu across its queries would deadlock on that.
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

// Reload replaces the rule set.
//
// It is the whole of hot reload: the config file is re-read elsewhere, and the
// rules it yielded arrive here. Swapping the pointer means an evaluation in
// flight finishes against the rules it started with rather than seeing a set
// that is half old and half new.
func (e *Evaluator) Reload(rules []Rule) {
	set := make([]Rule, len(rules))
	copy(set, rules)

	// Under the same lock commit uses. Bumping the generation outside it left
	// a window between commit's check and its write: a reload that landed
	// there was not seen by the check, and the evaluation it should have
	// discarded published anyway and left its sightings behind. The two are
	// now ordered -- either commit finishes first, or the reload does and
	// commit sees the generation it changed.
	e.mu.Lock()
	e.rules.Store(&set)
	e.generation.Add(1)
	e.mu.Unlock()

	e.logger.Info("alarm rules loaded", "rules", len(set))
}

// RuleCount is how many rules are loaded.
//
// Zero rules and zero alarms are not the same answer: the first is nothing
// being watched and the second is nothing being wrong, and a bell that shows
// the same thing for both is a bell that lies.
func (e *Evaluator) RuleCount() int {
	if rules := e.rules.Load(); rules != nil {
		return len(*rules)
	}
	return 0
}

// Firing is what is wrong right now, worst first.
//
// The slice is never modified after publication, so callers may read it
// without copying and without a lock.
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

// EvaluateOnce runs every rule a single time and publishes the result.
//
// It is what a test drives, and what Run calls on its own tick. Exported so a
// caller can force an evaluation -- a browser opened seconds after a reload
// should not have to wait a full interval to see the rules that were just
// loaded.
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

	// The generation this evaluation belongs to. A reload during it makes the
	// result stale before it is published: the answer would name rules that
	// have been removed while the count beside it says how many are loaded
	// now, and the two would disagree until the next tick.
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
	// next is what `raised` becomes if this evaluation is the one that
	// publishes. Nothing is written back until then: an evaluation the rules
	// changed under is discarded, and one that had already recorded a first
	// sighting would leave that behind for the rules that replaced it.
	next := make(map[string]time.Time, len(*rules))
	unevaluated := 0

	for _, rule := range *rules {
		samples, err := e.query(ctx, rule.Expr)
		if err != nil {
			// A rule that cannot be evaluated is not a rule that is quiet.
			// Dropping it would clear whatever it had already raised, so a
			// Prometheus that stops answering reads as a fleet that has just
			// come good -- an all-clear produced by the outage itself, at the
			// one moment an operator most needs otherwise.
			//
			// What it last knew is carried forward, and the count of rules
			// that could not be asked goes with it so the bell can say the
			// answer is incomplete rather than imply it is current.
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
//
// A copy, because the evaluation reads it while Reload can be running: an
// alarm that stays up keeps its original timestamp, which is what makes
// "since" mean how long something has been broken.
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
