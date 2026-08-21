package poller

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/jeremyhahn/go-netmon/internal/snmp"
)

// timeout is the error a device that never answers produces: gosnmp rewrites an
// exhausted retry sequence into this text and drops the deadline error it came
// from, so the text is all a caller gets.
func timeout() error {
	return &snmp.GetError{Count: 3, Err: errors.New("request timeout (after 3 retries)")}
}

func TestBackoffWidensFromTheIntervalAndStopsAtTheCeiling(t *testing.T) {
	t.Parallel()

	const (
		interval = time.Minute
		ceiling  = 15 * time.Minute
	)

	tests := []struct {
		name     string
		failures uint32
		want     time.Duration
	}{
		{"answering, so no backoff at all", 0, interval},
		{"first failure keeps the normal interval", 1, interval},
		{"second failure doubles it", 2, 2 * interval},
		{"third", 3, 4 * interval},
		{"fourth", 4, 8 * interval},
		{"the fifth would pass the ceiling", 5, ceiling},
		{"and every one after it stays there", 9, ceiling},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := backoff(interval, ceiling, tt.failures); got != tt.want {
				t.Errorf("backoff(%s, %s, %d) = %s, want %s", interval, ceiling, tt.failures, got, tt.want)
			}
		})
	}
}

// TestBackoffSurvivesDegenerateSchedules covers the inputs that would otherwise
// produce a wait of zero — which polls a dead device in a loop — or a negative
// one, which is what shifting a duration off the end of an int64 gives.
func TestBackoffSurvivesDegenerateSchedules(t *testing.T) {
	t.Parallel()

	const ceiling = 15 * time.Minute

	tests := []struct {
		name     string
		interval time.Duration
		failures uint32
		want     time.Duration
	}{
		{
			name:     "an interval already wider than the ceiling is its own backoff",
			interval: time.Hour, failures: 3, want: time.Hour,
		},
		{
			name:     "a device polled exactly at the ceiling keeps that interval",
			interval: ceiling, failures: 4, want: ceiling,
		},
		{
			name:     "a failure count that would shift past an int64 lands on the ceiling",
			interval: time.Second, failures: 63, want: ceiling,
		},
		{
			name:     "the same, just under the shift guard",
			interval: time.Second, failures: maxBackoffShift, want: ceiling,
		},
		{
			name:     "a zero interval backs off rather than spinning",
			interval: 0, failures: 1, want: ceiling,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := backoff(tt.interval, ceiling, tt.failures)
			if got != tt.want {
				t.Errorf("backoff(%s, %s, %d) = %s, want %s", tt.interval, ceiling, tt.failures, got, tt.want)
			}
			if got <= 0 {
				t.Errorf("backoff(%s, %s, %d) = %s: a wait of zero polls a dead device in a loop",
					tt.interval, ceiling, tt.failures, got)
			}
		})
	}
}

// TestCycleCountsAnyReplyAsReachable is the regression that protects a device
// which answers some objects and fails on others. Every error below came back
// *from the device*, so none of them may ever add up to "unreachable" no matter
// how many of them a cycle collects.
func TestCycleCountsAnyReplyAsReachable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
	}{
		{"the operation succeeded", nil},
		{"the agent refused the request", &snmp.ResponseError{Status: gosnmp.NoSuchName, Count: 4}},
		{"the agent could not fit a varbind in a response", &snmp.TooBigError{Count: 1, OID: "1.3.6.1.2.1.1.1.0"}},
		{"a table broke off part-way through", &snmp.WalkError{
			Op: "BULKWALK", OID: "1.3.6.1.2.1.2.2", Received: 12, Err: errors.New("request timeout (after 3 retries)"),
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c cycle
			for i := 0; i < silenceThreshold*3; i++ {
				if c.record(tt.err) {
					t.Fatalf("operation %d abandoned the cycle: the device answered", i+1)
				}
			}
			if c.abandoned() {
				t.Error("the cycle was abandoned even though every operation was answered")
			}
			if c.unreachable() {
				t.Error("a device that answered every operation was judged unreachable")
			}
		})
	}
}

func TestCycleAbandonsAfterConsecutiveSilence(t *testing.T) {
	t.Parallel()

	var c cycle
	for i := 1; i < silenceThreshold; i++ {
		if c.record(timeout()) {
			t.Fatalf("the cycle was abandoned after %d unanswered operations, before the threshold of %d",
				i, silenceThreshold)
		}
	}

	if !c.record(timeout()) {
		t.Fatalf("the cycle was not abandoned after %d unanswered operations", silenceThreshold)
	}
	if !c.unreachable() {
		t.Error("a device that answered nothing at all was not judged unreachable")
	}
}

// TestCycleForgetsSilenceOnceTheDeviceSpeaks pins the "consecutive" in the rule:
// a device that times out twice, answers, then times out twice more has never
// been silent for three operations in a row and must keep being polled.
func TestCycleForgetsSilenceOnceTheDeviceSpeaks(t *testing.T) {
	t.Parallel()

	var c cycle
	for _, err := range []error{timeout(), timeout(), nil, timeout(), timeout()} {
		if c.record(err) {
			t.Fatal("the cycle was abandoned; the device answered in the middle of it")
		}
	}

	if c.unreachable() {
		t.Error("a device that answered one operation out of five was judged unreachable")
	}
}

// TestCycleWithNothingAttemptedIsNotUnreachable keeps a device with no objects
// selected from being reported as down: nothing was asked of it.
func TestCycleWithNothingAttemptedIsNotUnreachable(t *testing.T) {
	t.Parallel()

	var c cycle
	if c.unreachable() {
		t.Error("a cycle that attempted nothing reported the device unreachable")
	}
	if c.abandoned() {
		t.Error("a cycle that attempted nothing reported itself abandoned")
	}
}

// TestCycleUnreachableWithoutReachingTheThreshold covers the device with only
// one or two objects selected: it can never reach the silence threshold, but a
// cycle in which everything attempted went unanswered is still a device that is
// not there.
func TestCycleUnreachableWithoutReachingTheThreshold(t *testing.T) {
	t.Parallel()

	var c cycle
	c.record(timeout())

	if c.abandoned() {
		t.Error("one unanswered operation abandoned the cycle")
	}
	if !c.unreachable() {
		t.Error("a device that answered the only operation attempted of it was called reachable")
	}
}

func TestReachabilityBacksOffAndRecovers(t *testing.T) {
	t.Parallel()

	const interval = time.Minute
	r := newReachability(t.Name(), interval)
	t.Cleanup(r.forget)

	if r.unreachable() {
		t.Error("a newly registered device starts out unreachable")
	}
	before := time.Now()
	got := r.schedule()
	after := time.Now()
	if got != interval {
		t.Errorf("first schedule = %s, want the device's own interval %s", got, interval)
	}

	// due() must report the instant schedule() computed: its own time.Now(),
	// which lies between the two readings taken around the call, plus the delay
	// it returned. Bracketing it that way is deterministic and says more than
	// the "is it in the future" check this replaces, which passed for any
	// later-but-wrong value and, at a short interval, would fail on a stalled
	// runner for reasons unrelated to the code (see unreachable_test.go's
	// awaitScheduledRetry for where that shape actually bit).
	earliest, latest := before.Add(got), after.Add(got)
	if due := r.due(); due.Before(earliest) || due.After(latest) {
		t.Errorf("due = %s, want the scheduled instant within [%s, %s]", due, earliest, latest)
	}

	for i, want := range []time.Duration{interval, 2 * interval, 4 * interval} {
		r.silent()
		if !r.unreachable() {
			t.Fatalf("after %d failed cycles the device is still reported reachable", i+1)
		}
		if got := r.failures(); got != i+1 {
			t.Errorf("failures = %d, want %d", got, i+1)
		}
		if got := r.schedule(); got != want {
			t.Errorf("after %d failed cycles schedule = %s, want %s", i+1, got, want)
		}
	}

	// One reply is enough: a recovered device must not serve out the rest of
	// its penalty before being polled normally again.
	r.answered()
	if r.unreachable() {
		t.Error("a device that answered is still reported unreachable")
	}
	if got := r.failures(); got != 0 {
		t.Errorf("failures = %d after recovery, want 0", got)
	}
	if got := r.schedule(); got != interval {
		t.Errorf("schedule after recovery = %s, want the normal interval %s", got, interval)
	}
}

// TestReachabilityPublishesTheDeviceState covers the series an operator alerts
// on, including the two states that are easy to forget: a device is published
// as reachable the moment it is registered, so an alert has a series to watch
// before anything has gone wrong, and it is dropped when the device is removed.
func TestReachabilityPublishesTheDeviceState(t *testing.T) {
	t.Parallel()

	device := t.Name()
	r := newReachability(device, time.Minute)
	t.Cleanup(r.forget)

	gauge := func() float64 {
		return testutil.ToFloat64(deviceUnreachable.WithLabelValues(device))
	}

	if got := gauge(); got != 0 {
		t.Errorf("gonetmon_device_unreachable{device=%q} = %v on registration, want 0", device, got)
	}

	r.silent()
	if got := gauge(); got != 1 {
		t.Errorf("gonetmon_device_unreachable{device=%q} = %v while unreachable, want 1", device, got)
	}

	r.silent()
	if got := gauge(); got != 1 {
		t.Errorf("gonetmon_device_unreachable{device=%q} = %v while still unreachable, want 1", device, got)
	}

	r.answered()
	if got := gauge(); got != 0 {
		t.Errorf("gonetmon_device_unreachable{device=%q} = %v after recovery, want 0", device, got)
	}

	r.forget()
	if publishesUnreachable(t, device) {
		t.Error("a removed device still has a gonetmon_device_unreachable series")
	}
}

// publishesUnreachable reports whether gonetmon_device_unreachable currently
// carries a value for the named device. Other tests in this package register
// devices of their own, so only this device's own series may be asserted on.
func publishesUnreachable(t *testing.T, device string) bool {
	t.Helper()

	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gathering metrics: %v", err)
	}

	for _, family := range families {
		if family.GetName() != "gonetmon_device_unreachable" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "device" && label.GetValue() == device {
					return true
				}
			}
		}
	}
	return false
}

// TestUnreachableErrorReportsWhatWasSkipped keeps the poll result honest: the
// message is the only record an operator has of a cycle that was cut short, so
// it has to say how much of it was never attempted.
func TestUnreachableErrorReportsWhatWasSkipped(t *testing.T) {
	t.Parallel()

	cause := errors.New("request timeout (after 3 retries)")
	err := &UnreachableError{Device: "bastion", Attempts: 4, Skipped: 59, Err: cause}

	message := err.Error()
	for _, want := range []string{"bastion", "4", "59", cause.Error()} {
		if !strings.Contains(message, want) {
			t.Errorf("Error() = %q, want it to mention %q", message, want)
		}
	}
	if !errors.Is(err, cause) {
		t.Error("the transport failure is not reachable with errors.Is")
	}
}

// TestUnreachableErrorWithoutACauseIsStillReadable covers the degenerate case:
// a device with a single object selected can fail its only operation without a
// transport error ever being recorded, and the message must still be a
// sentence.
func TestUnreachableErrorWithoutACauseIsStillReadable(t *testing.T) {
	t.Parallel()

	message := (&UnreachableError{Device: "bastion", Attempts: 1}).Error()

	if message == "" {
		t.Fatal("Error() on an UnreachableError with no cause is empty")
	}
	if strings.Contains(message, "skipped") {
		t.Errorf("Error() = %q, want no mention of skipped work when none was skipped", message)
	}
}

func TestClientErrorNamesTheDevice(t *testing.T) {
	t.Parallel()

	cause := os.ErrPermission
	err := &ClientError{Device: "bastion", Err: cause}

	if !strings.Contains(err.Error(), "bastion") {
		t.Errorf("Error() = %q, want it to name the device", err)
	}
	if !errors.Is(err, cause) {
		t.Error("the underlying failure is not reachable with errors.Is")
	}
}

func TestIntervalErrorExplainsHowToFixIt(t *testing.T) {
	t.Parallel()

	err := &IntervalError{Device: "bastion", Interval: 0}

	message := err.Error()
	for _, want := range []string{"bastion", "poll_interval", "default_poll_interval"} {
		if !strings.Contains(message, want) {
			t.Errorf("Error() = %q, want it to mention %q", message, want)
		}
	}
}

// TestTimeoutIsUnanswered is the bridge between the two packages: the poller's
// whole judgement rests on snmp.Unanswered agreeing with it about what silence
// looks like coming out of gosnmp.
func TestTimeoutIsUnanswered(t *testing.T) {
	t.Parallel()

	if !snmp.Unanswered(timeout()) {
		t.Error("an exhausted retry sequence was not recognized as the device never answering")
	}
	if snmp.Unanswered(nil) {
		t.Error("a successful operation was recognized as silence")
	}
}
