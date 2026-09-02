package alarm

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"
)

// stubQuery answers rules from a table, and records what it was asked.
type stubQuery struct {
	mu      sync.Mutex
	answers map[string][]Sample
	fail    map[string]error
	asked   []string
}

func (s *stubQuery) run(_ context.Context, expr string) ([]Sample, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.asked = append(s.asked, expr)
	if err := s.fail[expr]; err != nil {
		return nil, err
	}
	return s.answers[expr], nil
}

// clock is time under the test's control, so a rule with a "for" can be
// exercised without waiting for it.
type clock struct {
	mu sync.Mutex
	at time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// testEvaluator builds an evaluator on a fixed clock.
func testEvaluator(t *testing.T, q *stubQuery, rules []Rule) (*Evaluator, *clock) {
	t.Helper()

	c := &clock{at: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)}
	e := New(q.run, rules, nil)
	e.now = c.now
	return e, c
}

// TestOneRuleFiresOncePerSeries is the shape of the whole feature.
//
// A rule about interfaces has to fire once per interface: "Gi0/0/2 is down"
// and "Gi0/0/5 is down" are two things to fix, and a bell that collapsed them
// into "InterfaceDown" would tell an operator to go and look rather than what
// to look at.
func TestOneRuleFiresOncePerSeries(t *testing.T) {
	q := &stubQuery{answers: map[string][]Sample{
		"iface_down": {
			{Labels: map[string]string{"device": "dist-01", "ifdescr": "Gi0/0/2"}, Value: 2},
			{Labels: map[string]string{"device": "dist-01", "ifdescr": "Gi0/0/5"}, Value: 2},
		},
	}}
	e, _ := testEvaluator(t, q, []Rule{
		{Name: "InterfaceDown", Expr: "iface_down", Severity: SeverityWarning},
	})

	e.evaluate(context.Background())

	firing := e.Firing()
	if len(firing) != 2 {
		t.Fatalf("firing = %d alarms, want one per series: %+v", len(firing), firing)
	}
	for _, a := range firing {
		if a.Device != "dist-01" {
			t.Errorf("device = %q, want it lifted out of the labels", a.Device)
		}
		if a.Labels["ifdescr"] == "" {
			t.Error("the alarm does not say which interface it is about")
		}
	}
	if firing[0].ID == firing[1].ID {
		t.Error("two interfaces produced one alarm ID; they would collapse into one row")
	}
}

// TestAnAlarmKeepsItsSinceWhileItStaysUp is what makes "since" mean how long
// something has been broken rather than how long ago the last evaluation ran.
func TestAnAlarmKeepsItsSinceWhileItStaysUp(t *testing.T) {
	q := &stubQuery{answers: map[string][]Sample{
		"down": {{Labels: map[string]string{"device": "dist-01"}, Value: 1}},
	}}
	e, c := testEvaluator(t, q, []Rule{
		{Name: "Down", Expr: "down", Severity: SeverityCritical},
	})

	e.evaluate(context.Background())
	first := e.Firing()[0].Since

	c.advance(10 * time.Minute)
	e.evaluate(context.Background())

	if got := e.Firing()[0].Since; !got.Equal(first) {
		t.Errorf("since = %v, want the original %v: an alarm that has been up for "+
			"ten minutes must not look like one raised a moment ago", got, first)
	}
}

// TestAnAlarmThatClearedAndCameBackIsNew is the other half of the same rule.
//
// Remembering it forever would report a fault that has been fixed and has
// recurred as having been up the whole time, which is the difference between
// a flapping link and a dead one.
func TestAnAlarmThatClearedAndCameBackIsNew(t *testing.T) {
	q := &stubQuery{answers: map[string][]Sample{
		"down": {{Labels: map[string]string{"device": "dist-01"}, Value: 1}},
	}}
	e, c := testEvaluator(t, q, []Rule{
		{Name: "Down", Expr: "down", Severity: SeverityCritical},
	})

	e.evaluate(context.Background())
	first := e.Firing()[0].Since

	// It clears.
	q.mu.Lock()
	q.answers["down"] = nil
	q.mu.Unlock()
	c.advance(time.Minute)
	e.evaluate(context.Background())
	if len(e.Firing()) != 0 {
		t.Fatalf("the alarm did not clear: %+v", e.Firing())
	}

	// And comes back.
	q.mu.Lock()
	q.answers["down"] = []Sample{{Labels: map[string]string{"device": "dist-01"}, Value: 1}}
	q.mu.Unlock()
	c.advance(time.Minute)
	e.evaluate(context.Background())

	if got := e.Firing()[0].Since; got.Equal(first) {
		t.Error("a fault that cleared and recurred is reported as having been up " +
			"the whole time; a flapping link would read as a dead one")
	}
}

// TestForHoldsAnAlarmBackUntilTheConditionHas is what separates a condition
// from a blip. An interface that bounces for one poll is not something to
// raise, and a bell that rang for every bounce is a bell nobody reads.
func TestForHoldsAnAlarmBackUntilTheConditionHas(t *testing.T) {
	q := &stubQuery{answers: map[string][]Sample{
		"down": {{Labels: map[string]string{"device": "dist-01"}, Value: 1}},
	}}
	e, c := testEvaluator(t, q, []Rule{
		{Name: "Down", Expr: "down", Severity: SeverityWarning, For: 5 * time.Minute},
	})

	e.evaluate(context.Background())
	if len(e.Firing()) != 0 {
		t.Fatal("the alarm was raised on its first evaluation, before its \"for\" elapsed")
	}

	c.advance(4 * time.Minute)
	e.evaluate(context.Background())
	if len(e.Firing()) != 0 {
		t.Fatal("the alarm was raised before its \"for\" elapsed")
	}

	c.advance(2 * time.Minute)
	e.evaluate(context.Background())
	if len(e.Firing()) != 1 {
		t.Fatalf("the alarm was not raised after its \"for\" elapsed: %+v", e.Firing())
	}
}

// TestTheWorstAlarmIsFirst is what a bell showing thirty rows depends on.
func TestTheWorstAlarmIsFirst(t *testing.T) {
	q := &stubQuery{answers: map[string][]Sample{
		"info":     {{Labels: map[string]string{"device": "a"}, Value: 1}},
		"critical": {{Labels: map[string]string{"device": "b"}, Value: 1}},
		"warning":  {{Labels: map[string]string{"device": "c"}, Value: 1}},
	}}
	e, _ := testEvaluator(t, q, []Rule{
		{Name: "Info", Expr: "info", Severity: SeverityInfo},
		{Name: "Critical", Expr: "critical", Severity: SeverityCritical},
		{Name: "Warning", Expr: "warning", Severity: SeverityWarning},
	})

	e.evaluate(context.Background())

	want := []string{"Critical", "Warning", "Info"}
	firing := e.Firing()
	if len(firing) != len(want) {
		t.Fatalf("firing = %d, want %d", len(firing), len(want))
	}
	for i, name := range want {
		if firing[i].Rule != name {
			t.Errorf("firing[%d] = %s, want %s", i, firing[i].Rule, name)
		}
	}
}

// TestARuleThatCannotBeEvaluatedDoesNotReadAsQuiet is the failure mode that
// would matter most.
//
// A rule Prometheus refuses is not a rule that found nothing. Reporting
// all-clear on the strength of a query that never ran is the one moment an
// operator most needs to be told otherwise -- so the other rules still answer,
// and the failure is logged rather than swallowed into an empty bell.
func TestARuleThatCannotBeEvaluatedDoesNotReadAsQuiet(t *testing.T) {
	q := &stubQuery{
		answers: map[string][]Sample{
			"reachable": {{Labels: map[string]string{"device": "b"}, Value: 1}},
		},
		fail: map[string]error{"broken": errors.New("prometheus is unwell")},
	}
	e, _ := testEvaluator(t, q, []Rule{
		{Name: "Broken", Expr: "broken", Severity: SeverityCritical},
		{Name: "Reachable", Expr: "reachable", Severity: SeverityWarning},
	})

	e.evaluate(context.Background())

	firing := e.Firing()
	if len(firing) != 1 || firing[0].Rule != "Reachable" {
		t.Errorf("firing = %+v, want the rule that COULD be evaluated still reported; "+
			"one broken rule must not silence the rest", firing)
	}
}

// TestNoRulesIsNotTheSameAsNothingWrong keeps the bell honest about what it is
// watching.
func TestNoRulesIsNotTheSameAsNothingWrong(t *testing.T) {
	e, _ := testEvaluator(t, &stubQuery{}, nil)

	e.evaluate(context.Background())

	if got := e.Firing(); len(got) != 0 {
		t.Errorf("firing = %+v, want none", got)
	}
	if got := e.RuleCount(); got != 0 {
		t.Errorf("RuleCount = %d, want 0: a reader has to be able to tell "+
			"\"nothing is wrong\" from \"nothing is being watched\"", got)
	}
}

// TestReloadSwapsTheRuleSetWhole is hot reload from the evaluator's side.
func TestReloadSwapsTheRuleSetWhole(t *testing.T) {
	q := &stubQuery{answers: map[string][]Sample{
		"old": {{Labels: map[string]string{"device": "a"}, Value: 1}},
		"new": {{Labels: map[string]string{"device": "b"}, Value: 1}},
	}}
	e, _ := testEvaluator(t, q, []Rule{
		{Name: "Old", Expr: "old", Severity: SeverityWarning},
	})
	e.evaluate(context.Background())
	if e.Firing()[0].Rule != "Old" {
		t.Fatalf("firing = %+v, want the rule that was loaded", e.Firing())
	}

	e.Reload([]Rule{{Name: "New", Expr: "new", Severity: SeverityWarning}})
	e.evaluate(context.Background())

	firing := e.Firing()
	if len(firing) != 1 || firing[0].Rule != "New" {
		t.Errorf("firing = %+v, want only the reloaded rule: a rule that was "+
			"removed must stop firing", firing)
	}
	if got := e.RuleCount(); got != 1 {
		t.Errorf("RuleCount = %d, want 1", got)
	}
}

// TestTheCallerCannotMutateTheLoadedRules guards the swap.
//
// Reload takes a slice the caller still holds. Storing it directly would let a
// later edit to that slice change the running rules without an evaluation, and
// with the evaluation loop reading it concurrently.
func TestTheCallerCannotMutateTheLoadedRules(t *testing.T) {
	q := &stubQuery{answers: map[string][]Sample{
		"old": {{Labels: map[string]string{"device": "a"}, Value: 1}},
	}}
	rules := []Rule{{Name: "Old", Expr: "old", Severity: SeverityWarning}}
	e, _ := testEvaluator(t, q, rules)

	rules[0].Expr = "something else entirely"
	e.evaluate(context.Background())

	if len(e.Firing()) != 1 {
		t.Error("editing the caller's slice changed the running rules")
	}
}

// TestForgettingClearedAlarmsBoundsTheState keeps a long-running process from
// remembering every series that has ever tripped a rule.
func TestForgettingClearedAlarmsBoundsTheState(t *testing.T) {
	q := &stubQuery{answers: map[string][]Sample{"down": nil}}
	e, c := testEvaluator(t, q, []Rule{
		{Name: "Down", Expr: "down", Severity: SeverityWarning},
	})

	for i := 0; i < 50; i++ {
		q.mu.Lock()
		q.answers["down"] = []Sample{
			{Labels: map[string]string{"device": "d", "ifindex": strconv.Itoa(i)}, Value: 1},
		}
		q.mu.Unlock()
		c.advance(time.Minute)
		e.evaluate(context.Background())
	}

	e.mu.Lock()
	remembered := len(e.raised)
	e.mu.Unlock()

	if remembered != 1 {
		t.Errorf("remembering %d alarms, want 1: only the one still firing", remembered)
	}
}

// TestAFailedRuleDoesNotClearWhatItHadAlreadyRaised is the failure mode that
// would matter most, and the earlier test for it could not see.
//
// A Prometheus that stops answering would otherwise produce an all-clear: the
// rule is skipped, its alarms are not in the new result, and the bell empties.
// The outage manufactures the good news. What the rule last knew is carried
// forward instead, with a count saying the answer is incomplete.
func TestAFailedRuleDoesNotClearWhatItHadAlreadyRaised(t *testing.T) {
	q := &stubQuery{answers: map[string][]Sample{
		"down": {{Labels: map[string]string{"device": "core-02"}, Value: 1}},
	}}
	e, c := testEvaluator(t, q, []Rule{
		{Name: "Down", Expr: "down", Severity: SeverityCritical},
	})

	e.evaluate(context.Background())
	if len(e.Firing()) != 1 {
		t.Fatal("the alarm did not raise while the query was answering")
	}
	raised := e.Firing()[0].Since

	// Prometheus stops answering.
	q.mu.Lock()
	q.fail = map[string]error{"down": errors.New("prometheus is unwell")}
	q.mu.Unlock()
	c.advance(time.Minute)
	e.evaluate(context.Background())

	held := e.Snapshot()
	if len(held.Alarms) != 1 {
		t.Fatalf("alarms = %+v, want the last known one carried forward: a failed "+
			"query must not read as the fault having cleared", held.Alarms)
	}
	if !held.Alarms[0].Since.Equal(raised) {
		t.Errorf("since = %v, want the original %v", held.Alarms[0].Since, raised)
	}
	if held.Unevaluated != 1 {
		t.Errorf("unevaluated = %d, want 1: the answer is incomplete and has to "+
			"say so, or it reads as current", held.Unevaluated)
	}

	// And it clears for real once the query answers again and finds nothing.
	q.mu.Lock()
	q.fail = nil
	q.answers["down"] = nil
	q.mu.Unlock()
	c.advance(time.Minute)
	e.evaluate(context.Background())

	if got := e.Snapshot(); len(got.Alarms) != 0 || got.Unevaluated != 0 {
		t.Errorf("snapshot = %+v, want cleared once the query answered", got)
	}
}

// TestAnEvaluationTheRulesChangedUnderIsDiscarded is the reload race.
//
// An evaluation snapshots the rule set at its start. If a reload lands while it
// is running, publishing its result would show alarms belonging to a rule that
// has been removed, beside a count of the rules that are loaded now -- two
// numbers on one panel that contradict each other until the next tick.
func TestAnEvaluationTheRulesChangedUnderIsDiscarded(t *testing.T) {
	reloaded := make(chan struct{})
	q := &stubQuery{answers: map[string][]Sample{
		"old": {{Labels: map[string]string{"device": "a"}, Value: 1}},
	}}

	e, _ := testEvaluator(t, q, []Rule{
		{Name: "Old", Expr: "old", Severity: SeverityWarning},
	})

	// The reload happens while the query for the old rule is in flight.
	e.query = func(ctx context.Context, expr string) ([]Sample, error) {
		e.Reload([]Rule{{Name: "New", Expr: "new", Severity: SeverityWarning}})
		close(reloaded)
		return q.run(ctx, expr)
	}

	e.evaluate(context.Background())
	<-reloaded

	if got := e.Firing(); len(got) != 0 {
		t.Errorf("firing = %+v, want none published: the result names a rule that "+
			"was removed while it was being produced", got)
	}
	if got := e.RuleCount(); got != 1 {
		t.Errorf("RuleCount = %d, want the reloaded 1", got)
	}
}

// TestADiscardedEvaluationLeavesNothingBehind is the other half of discarding.
//
// Not publishing the result is not the same as not having run. The evaluation
// also records when it first saw each alarm, and that record is what "for"
// measures against -- so an evaluation thrown away for naming rules that no
// longer exist could still leave its first sighting behind, and the rule that
// replaced it would inherit a clock it never started.
//
// The reloaded rule below asks for five minutes. If the discarded run's
// sighting survives, the very first evaluation of the NEW rule sees an alarm
// that has apparently been up for five minutes and fires it immediately --
// which is exactly what "for" exists to prevent.
func TestADiscardedEvaluationLeavesNothingBehind(t *testing.T) {
	reloaded := make(chan struct{})
	q := &stubQuery{answers: map[string][]Sample{
		"old": {{Labels: map[string]string{"device": "a"}, Value: 1}},
		"new": {{Labels: map[string]string{"device": "a"}, Value: 1}},
	}}

	e, c := testEvaluator(t, q, []Rule{
		{Name: "Watch", Expr: "old", Severity: SeverityWarning},
	})

	// The reload lands while the query for the old rule is in flight, and the
	// replacement keeps the rule NAME so both produce the same alarm ID -- the
	// case where an inherited sighting is invisible in the published answer.
	e.query = func(ctx context.Context, expr string) ([]Sample, error) {
		select {
		case <-reloaded:
		default:
			e.Reload([]Rule{
				{Name: "Watch", Expr: "new", Severity: SeverityWarning, For: 5 * time.Minute},
			})
			close(reloaded)
		}
		return q.run(ctx, expr)
	}

	e.evaluate(context.Background())
	<-reloaded
	if got := e.Firing(); len(got) != 0 {
		t.Fatalf("firing = %+v, want none: the discarded result was published", got)
	}

	c.advance(5 * time.Minute)
	e.evaluate(context.Background())

	if got := e.Firing(); len(got) != 0 {
		t.Errorf("firing = %+v after one evaluation of a rule that asks for five "+
			"minutes: it inherited a first sighting from the evaluation that was "+
			"thrown away", got)
	}

	// And it does fire once the condition really has held that long.
	c.advance(5 * time.Minute)
	e.evaluate(context.Background())
	if got := e.Firing(); len(got) != 1 {
		t.Errorf("firing = %+v, want the alarm once its five minutes have "+
			"genuinely passed", got)
	}
}

// TestReloadWaitsForACommitInFlight closes the window between commit's
// generation check and its write.
//
// The check passing does not make the result current: a reload landing in that
// window was invisible to it, so the evaluation it should have discarded
// published anyway and left its sightings behind. Both halves are now inside
// one critical section, and Reload takes the same lock -- so either commit
// finishes first, or the reload does and commit sees the generation it changed.
//
// That is a property of the LOCKING, and there is no point in the middle of a
// critical section for a test to stand: hammering reloads against evaluations
// passes either way, which is what a first version of this test did. So it
// asserts the ordering directly -- with the lock commit uses held, a reload
// must wait.
func TestReloadWaitsForACommitInFlight(t *testing.T) {
	q := &stubQuery{answers: map[string][]Sample{
		"a": {{Labels: map[string]string{"device": "a"}, Value: 1}},
	}}
	e, _ := testEvaluator(t, q, []Rule{{Name: "A", Expr: "a", Severity: SeverityWarning}})

	e.mu.Lock()

	reloaded := make(chan struct{})
	go func() {
		e.Reload([]Rule{{Name: "B", Expr: "a", Severity: SeverityWarning}})
		close(reloaded)
	}()

	select {
	case <-reloaded:
		e.mu.Unlock()
		t.Fatal("Reload did not wait for the lock commit holds, so a reload can " +
			"land between commit's generation check and its write, and the " +
			"evaluation that should have been discarded publishes anyway")
	case <-time.After(100 * time.Millisecond):
	}

	e.mu.Unlock()

	select {
	case <-reloaded:
	case <-time.After(5 * time.Second):
		t.Fatal("Reload never completed after the lock was released")
	}

	if got := e.RuleCount(); got != 1 {
		t.Errorf("RuleCount = %d, want the reloaded 1", got)
	}
	e.evaluate(context.Background())
	if got := e.Firing(); len(got) != 1 || got[0].Rule != "B" {
		t.Errorf("firing = %+v, want the reloaded rule", got)
	}
}

// TestEvaluationsDoNotOverlap serializes the exported forced evaluation with
// the one Run drives.
//
// EvaluateOnce exists so a browser opened seconds after a reload does not wait
// a whole interval, which means it runs alongside Run's tick. Two overlapping
// evaluations read the same sightings and the same generation, and whichever
// finished LAST would publish over the other -- an older answer replacing a
// newer one. Nothing detects it: every access is properly synchronized, so the
// race detector is quiet and only the order is wrong.
//
// Asserted the same way as the reload ordering, and for the same reason: with
// an evaluation in progress, a second must wait rather than interleave.
func TestEvaluationsDoNotOverlap(t *testing.T) {
	q := &stubQuery{answers: map[string][]Sample{
		"a": {{Labels: map[string]string{"device": "a"}, Value: 1}},
	}}
	e, _ := testEvaluator(t, q, []Rule{{Name: "A", Expr: "a", Severity: SeverityWarning}})

	e.evalMu.Lock()

	done := make(chan struct{})
	go func() {
		e.EvaluateOnce(context.Background())
		close(done)
	}()

	select {
	case <-done:
		e.evalMu.Unlock()
		t.Fatal("a forced evaluation ran while another was in progress: two of " +
			"them read the same state and the slower one publishes last, " +
			"replacing a newer answer with an older one")
	case <-time.After(100 * time.Millisecond):
	}

	e.evalMu.Unlock()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the forced evaluation never completed after the lock was released")
	}

	if got := e.Firing(); len(got) != 1 {
		t.Errorf("firing = %+v, want the one rule to have been evaluated", got)
	}
}

// TestAQueryMayReloadWithoutDeadlock is why evaluations are serialized on a
// lock of their own rather than on the one commit and Reload share.
//
// The rules are re-read from the config file by a signal handler, so a reload
// can land at any moment -- including while a query is in flight. An evaluation
// that held commit's lock across its queries would deadlock against it.
func TestAQueryMayReloadWithoutDeadlock(t *testing.T) {
	q := &stubQuery{answers: map[string][]Sample{
		"a": {{Labels: map[string]string{"device": "a"}, Value: 1}},
	}}
	e, _ := testEvaluator(t, q, []Rule{{Name: "A", Expr: "a", Severity: SeverityWarning}})

	e.query = func(ctx context.Context, expr string) ([]Sample, error) {
		e.Reload([]Rule{{Name: "A", Expr: "a", Severity: SeverityWarning}})
		return q.run(ctx, expr)
	}

	done := make(chan struct{})
	go func() {
		e.evaluate(context.Background())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("an evaluation whose query reloaded the rules never returned")
	}
}
