package metrics

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang/snappy"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/prometheus/prometheus/prompb"
)

// sample is one reading for a series named by i, stamped at at.
func sample(i int, at time.Time) *Sample {
	return &Sample{
		Name:      "snmp_test_metric",
		Labels:    map[string]string{"i": strconv.Itoa(i)},
		Value:     float64(i),
		Timestamp: at,
	}
}

// TestOnlyOneRequestIsInFlightAtATime is the property the sender exists for.
//
// Before it, every poll worker that filled the buffer wrote inline and the
// background flusher wrote on a tick, so two requests could be on the wire with
// nothing deciding which arrived first. A batch taken later could land first and
// leave the earlier one permanently unacceptable to Prometheus -- the same
// failure as a malformed request, reached without one (go-netmon#69).
//
// The handler below holds each request open long enough that an overlap would
// be certain if one were possible.
func TestOnlyOneRequestIsInFlightAtATime(t *testing.T) {
	var inFlight atomic.Int32
	var maxInFlight atomic.Int32
	var requests atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		now := inFlight.Add(1)
		for {
			peak := maxInFlight.Load()
			if now <= peak || maxInFlight.CompareAndSwap(peak, now) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		inFlight.Add(-1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	// batchSize 1 so every Export enqueues a batch of its own, and a short
	// flush interval so the background flusher is competing too.
	exporter, err := NewPrometheusExporter(server.URL, 1, 5*time.Millisecond, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	defer func() { _ = exporter.Close() }()

	// Twelve producers, which is what a fleet of poll workers looks like.
	var wg sync.WaitGroup
	at := time.Now()
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = exporter.Export(context.Background(), []*Sample{sample(i, at)})
		}(i)
	}
	wg.Wait()

	if err := exporter.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if got := requests.Load(); got == 0 {
		t.Fatal("no requests reached the endpoint, so concurrency was not exercised")
	}
	if got := maxInFlight.Load(); got != 1 {
		t.Errorf("%d requests were in flight at once (of %d), want 1: two writers "+
			"means the order they arrive in is undecided", got, requests.Load())
	}
}

// TestASampleTooOldToLandIsDroppedRatherThanRetriedForever is the age-out half
// of the queue's overflow policy.
//
// A sample the endpoint has already moved past cannot be accepted however many
// times it is offered, and retrying it holds up everything behind it. Waiting
// longer than the horizon is the proxy for that: newer samples for the same
// series have almost certainly landed in the meantime.
func TestASampleTooOldToLandIsDroppedRatherThanRetriedForever(t *testing.T) {
	var received atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	exporter, err := NewPrometheusExporter(server.URL, 1000, time.Hour, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	defer func() { _ = exporter.Close() }()

	exporter.maxSampleAge = time.Minute
	own := privateCounters(exporter)

	// One sample from before the horizon, one from inside it.
	now := time.Now()
	exporter.now = func() time.Time { return now }
	if err := exporter.Export(context.Background(), []*Sample{
		sample(1, now.Add(-2*time.Minute)),
		sample(2, now.Add(-1*time.Second)),
	}); err != nil {
		t.Fatalf("Export: %v", err)
	}

	// The flush must NOT report success: one of the samples it was responsible
	// for was thrown away. A flush returning nil has to mean delivered, or the
	// silent loss this package exists to end reappears at the far end of the
	// pipe.
	var discarded *DroppedError
	if err := exporter.Flush(context.Background()); !errors.As(err, &discarded) {
		t.Fatalf("Flush = %v, want a *DroppedError naming what it threw away", err)
	}
	if discarded.Samples != 1 {
		t.Errorf("Flush reported %d discarded, want 1", discarded.Samples)
	}

	if dropped := testutil.ToFloat64(own.exports.WithLabelValues("dropped")); dropped != 1 {
		t.Errorf("dropped %v samples, want 1: the one older than the horizon", dropped)
	}
	if aged := testutil.ToFloat64(own.dropped.WithLabelValues("aged")); aged != 1 {
		t.Errorf("counted %v samples dropped for AGE, want 1: a clock problem and a "+
			"backpressure problem must not share one number", aged)
	}
	if got := received.Load(); got != 1 {
		t.Errorf("%d requests reached the endpoint, want 1 carrying the fresh sample", got)
	}
}

// TestAFreshSampleIsNotAgedOut is the other side of the same rule: the horizon
// must not discard data from a pipeline that is merely busy.
func TestAFreshSampleIsNotAgedOut(t *testing.T) {
	var received atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	exporter, err := NewPrometheusExporter(server.URL, 1000, time.Hour, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	defer func() { _ = exporter.Close() }()

	exporter.maxSampleAge = time.Minute
	own := privateCounters(exporter)

	now := time.Now()
	exporter.now = func() time.Time { return now }
	if err := exporter.Export(context.Background(), []*Sample{
		sample(1, now.Add(-59*time.Second)),
		sample(2, now),
	}); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if err := exporter.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if dropped := testutil.ToFloat64(own.exports.WithLabelValues("dropped")); dropped != 0 {
		t.Errorf("dropped %v samples that were still inside the horizon, want 0", dropped)
	}
	if got := received.Load(); got != 1 {
		t.Errorf("%d requests reached the endpoint, want 1", got)
	}
}

// TestARefusalTheEndpointWillRepeatIsNotRetried is one half of the retry rule.
//
// Prometheus Remote-Write 1.0: "Senders MUST NOT retry on HTTP 4xx responses
// other than 429". A 400 is a refusal the endpoint will repeat forever -- a
// malformed request, a sample older than what it holds, a label set it will not
// take -- so retrying it only delays everything behind it.
func TestARefusalTheEndpointWillRepeatIsNotRetried(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	exporter, err := NewPrometheusExporter(server.URL, 1, 10*time.Millisecond, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	defer func() { _ = exporter.Close() }()

	own := privateCounters(exporter)
	if err := exporter.Export(context.Background(), []*Sample{sample(1, time.Now())}); err != nil {
		t.Fatalf("Export: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if testutil.ToFloat64(own.dropped.WithLabelValues("rejected")) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if got := testutil.ToFloat64(own.dropped.WithLabelValues("rejected")); got != 1 {
		t.Fatalf("dropped %v samples as rejected, want 1", got)
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("the endpoint saw %d attempts, want exactly 1: a 4xx other than "+
			"429 must not be retried", got)
	}
	if got := exporter.BufferedSamples(); got != 0 {
		t.Errorf("%d samples still queued after a refusal that cannot be retried", got)
	}
}

// TestAnOutageIsRetriedUntilItRecovers is the other half, and the case the old
// count-based rule got wrong.
//
// A Prometheus restart lasting longer than a few flushes is routine. Giving up
// after N refusals discarded fresh samples that would have been accepted
// seconds later -- an outage-triggered drop dressed up as a memory bound. Age
// and depth are the bounds on memory; the error class decides retrying.
func TestAnOutageIsRetriedUntilItRecovers(t *testing.T) {
	var requests atomic.Int32
	var failing atomic.Bool
	failing.Store(true)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if failing.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	exporter, err := NewPrometheusExporter(server.URL, 1, 10*time.Millisecond, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	defer func() { _ = exporter.Close() }()

	own := privateCounters(exporter)
	if err := exporter.Export(context.Background(), []*Sample{sample(1, time.Now())}); err != nil {
		t.Fatalf("Export: %v", err)
	}

	// Well past the three attempts the old rule allowed.
	deadline := time.Now().Add(5 * time.Second)
	for requests.Load() < 6 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := requests.Load(); got < 6 {
		t.Fatalf("the endpoint saw %d attempts, want at least 6: an outage must "+
			"keep being retried", got)
	}
	if got := testutil.ToFloat64(own.exports.WithLabelValues("dropped")); got != 0 {
		t.Fatalf("dropped %v samples during an outage, want 0: they are still "+
			"young enough to be accepted when it ends", got)
	}

	failing.Store(false)
	for time.Now().Before(deadline) {
		if exporter.BufferedSamples() == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("%d samples still queued after the endpoint recovered", exporter.BufferedSamples())
}

// TestAQueuedBatchIsRetriedWithoutNewSamples is the retry cadence.
//
// A refused batch stays in the queue and the sender sleeps. Only the flush tick
// goes back for it -- enqueue wakes the sender when NEW samples arrive, and an
// exporter whose devices had gone quiet would otherwise hold a failed batch
// until something else happened to produce one.
func TestAQueuedBatchIsRetriedWithoutNewSamples(t *testing.T) {
	var requests atomic.Int32
	var failing atomic.Bool
	failing.Store(true)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if failing.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	exporter, err := NewPrometheusExporter(server.URL, 1, 10*time.Millisecond, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	defer func() { _ = exporter.Close() }()

	if err := exporter.Export(context.Background(), []*Sample{sample(1, time.Now())}); err != nil {
		t.Fatalf("Export: %v", err)
	}

	// Wait for the first refusal, then recover the endpoint and produce nothing
	// further. Only the tick can drive the retry.
	deadline := time.Now().Add(3 * time.Second)
	for requests.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if requests.Load() == 0 {
		t.Fatal("the endpoint was never called, so nothing was retried")
	}
	failing.Store(false)

	for time.Now().Before(deadline) {
		if exporter.BufferedSamples() == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("%d samples still queued: a refused batch was never retried without "+
		"new samples arriving to wake the sender", exporter.BufferedSamples())
}

// TestASampleOlderThanOneAlreadyWrittenIsCounted is the detection that would
// have named this fault on the day it started.
//
// For fifteen hours the exporter dropped a million samples an hour while every
// signal it published said "buffered and failing to flush" -- which is a
// different problem with a different fix. A counter above zero says which one
// it is.
func TestASampleOlderThanOneAlreadyWrittenIsCounted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	exporter, err := NewPrometheusExporter(server.URL, 1000, time.Hour, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	defer func() { _ = exporter.Close() }()

	own := privateCounters(exporter)
	at := time.Now()

	// The newer reading is written first, which is what two overlapping polls
	// of one device produce.
	if err := exporter.Export(context.Background(), []*Sample{sample(1, at)}); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if err := exporter.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got := testutil.ToFloat64(own.outOfOrder); got != 0 {
		t.Fatalf("counted %v out-of-order samples on the first write, want 0", got)
	}

	if err := exporter.Export(context.Background(), []*Sample{sample(1, at.Add(-time.Second))}); err != nil {
		t.Fatalf("Export: %v", err)
	}
	// The stale sample is taken out and dropped on its own, so the flush that
	// carried it reports what it lost rather than succeeding.
	var discarded *DroppedError
	if err := exporter.Flush(context.Background()); !errors.As(err, &discarded) {
		t.Fatalf("Flush = %v, want a *DroppedError for the stale sample", err)
	}

	if got := testutil.ToFloat64(own.outOfOrder); got != 1 {
		t.Errorf("counted %v out-of-order samples, want 1: a reading older than one "+
			"already written for its series is what the endpoint refuses", got)
	}
	if got := testutil.ToFloat64(own.dropped.WithLabelValues("superseded")); got != 1 {
		t.Errorf("counted %v dropped as superseded, want 1", got)
	}

	// The same series again, ASCENDING. That is the ordinary case -- every
	// series is written repeatedly -- and counting it would make the counter
	// report the poll rate rather than a fault.
	if err := exporter.Export(context.Background(), []*Sample{sample(1, at.Add(time.Minute))}); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if err := exporter.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got := testutil.ToFloat64(own.outOfOrder); got != 1 {
		t.Errorf("counted %v after writing the same series in ascending order, want "+
			"still 1: a repeat is not a violation", got)
	}
}

// TestAnotherSeriesIsNotAnOrderingViolation keeps the check specific. Series
// are independent; one being behind another says nothing.
func TestAnotherSeriesIsNotAnOrderingViolation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	exporter, err := NewPrometheusExporter(server.URL, 1000, time.Hour, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	defer func() { _ = exporter.Close() }()

	own := privateCounters(exporter)
	at := time.Now()

	if err := exporter.Export(context.Background(), []*Sample{sample(1, at)}); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if err := exporter.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	// A DIFFERENT series, stamped earlier. Nothing is wrong with that.
	if err := exporter.Export(context.Background(), []*Sample{sample(2, at.Add(-time.Minute))}); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if err := exporter.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if got := testutil.ToFloat64(own.outOfOrder); got != 0 {
		t.Errorf("counted %v out-of-order samples across two independent series, want 0", got)
	}
}

// TestExportQueuesWithoutReleasingTheBuffer closes the gap between detaching a
// batch and queueing it.
//
// Export took the buffer under bufferLock, released it, and only then took
// queueMu to enqueue. Two callers could therefore detach in one order and queue
// in the other, putting the older batch behind the newer one -- the disorder
// the sender exists to prevent, reintroduced one lock earlier.
//
// Which order two concurrent Exports "should" be in is not observable from
// outside: it is decided by which one wins bufferLock, and a test cannot
// control that without removing the race it is testing. So this asserts the
// structural property instead. With queueMu held, an Export that must enqueue
// blocks -- and while it is blocked it must STILL hold bufferLock, which a
// second Export proves by blocking too. If the two steps were separate, the
// first would have released the buffer before reaching for the queue and the
// second would sail past.
func TestExportQueuesWithoutReleasingTheBuffer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	// A batch size the SECOND export will not reach, so it needs the buffer and
	// nothing else. With a batch size of one it would enqueue too, block on the
	// same lock, and prove nothing either way -- which is how the first version
	// of this test passed against the code it was written to catch.
	const batchSize = 100
	exporter, err := NewPrometheusExporter(server.URL, batchSize, time.Hour, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	defer func() { _ = exporter.Close() }()

	at := time.Now()
	full := make([]*Sample, batchSize)
	for i := range full {
		full[i] = sample(i, at)
	}

	exporter.queueMu.Lock()

	queuing := make(chan struct{})
	go func() {
		_ = exporter.Export(context.Background(), full)
		close(queuing)
	}()

	select {
	case <-queuing:
		exporter.queueMu.Unlock()
		t.Fatal("Export finished while the queue was held: it did not enqueue at all")
	case <-time.After(100 * time.Millisecond):
	}

	// The first Export is inside enqueue, waiting for queueMu. The buffer must
	// still be held, so this one -- which only appends -- cannot proceed.
	second := make(chan struct{})
	go func() {
		_ = exporter.Export(context.Background(), []*Sample{sample(9999, at)})
		close(second)
	}()

	select {
	case <-second:
		exporter.queueMu.Unlock()
		<-queuing
		t.Fatal("a second Export took the buffer while the first was still queueing: " +
			"detaching and queueing are not one step, so two callers can detach in " +
			"one order and queue in the other")
	case <-time.After(100 * time.Millisecond):
	}

	exporter.queueMu.Unlock()
	<-queuing
	<-second
}

// TestDepthEvictionDoesNotDiscardAFlushWaiter guards the barrier through the
// other overflow path.
//
// A barrier carries no samples, so evicting one makes no progress towards the
// bound -- and completing it with a DroppedError tells a Flush its work was
// discarded when everything ahead of it may have been written.
func TestDepthEvictionDoesNotDiscardAFlushWaiter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	exporter, err := NewPrometheusExporter(server.URL, 10, time.Hour, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	defer func() { _ = exporter.Close() }()

	waiter := make(chan error, 1)
	at := time.Now()

	// A barrier at the front, then far more samples than the bound behind it.
	exporter.queueMu.Lock()
	exporter.queue = append(exporter.queue, &pending{done: waiter})
	for i := 0; i < exporter.maxBufferSize+10; i++ {
		exporter.queue = append(exporter.queue, &pending{samples: []*Sample{sample(i, at)}})
		exporter.queued++
	}
	exporter.boundQueueLocked()
	barriers := 0
	for _, batch := range exporter.queue {
		if len(batch.samples) == 0 {
			barriers++
		}
	}
	exporter.queueMu.Unlock()

	if barriers != 1 {
		t.Errorf("%d barriers survived depth eviction, want 1", barriers)
	}
	select {
	case err := <-waiter:
		t.Errorf("the flush waiter was completed by depth eviction with %v; its "+
			"work was not what overflowed", err)
	default:
	}
}

// TestAnAbandonedFlushTakesItsBarrierWithIt bounds the queue's other half.
//
// A barrier carries no samples, so it counts towards no sample bound and
// nothing else would ever account for it. A Flush whose context expires stops
// waiting, and without this its barrier would sit in the queue for the life of
// the process -- one per abandoned flush, against an endpoint that is by
// definition already stalled.
//
// Identity is what makes taking it back safe: if it is still queued the sender
// has not reached it, and if it is not, the sender owns it and completes it
// into a buffered channel nobody reads.
func TestAnAbandonedFlushTakesItsBarrierWithIt(t *testing.T) {
	// A handler that does not answer until the test lets it, so the sender is
	// stuck on the first batch and every barrier behind it waits.
	block := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer server.Close()

	exporter, err := NewPrometheusExporter(server.URL, 1, time.Hour, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	defer func() { _ = exporter.Close() }()

	// Registered LAST so it runs FIRST: Close waits for the sender, and the
	// sender is inside that handler. Releasing it afterwards deadlocks the
	// shutdown until the request's own timeout expires.
	defer close(block)

	// Something for the sender to get stuck on.
	if err := exporter.Export(context.Background(), []*Sample{sample(1, time.Now())}); err != nil {
		t.Fatalf("Export: %v", err)
	}

	const abandoned = 50
	for i := 0; i < abandoned; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
		if err := exporter.Flush(ctx); err == nil {
			cancel()
			t.Fatal("Flush succeeded against an endpoint that never answers")
		}
		cancel()
	}

	exporter.queueMu.Lock()
	barriers := 0
	for _, batch := range exporter.queue {
		if len(batch.samples) == 0 {
			barriers++
		}
	}
	exporter.queueMu.Unlock()

	if barriers != 0 {
		t.Errorf("%d barriers left in the queue after %d abandoned flushes, want 0: "+
			"a waiter that has given up must take its barrier with it, or nothing "+
			"ever will", barriers, abandoned)
	}
}

// watermark reports the newest timestamp recorded as WRITTEN for a series, and
// whether anything has been.
//
// Read directly rather than through the out-of-order counter. The counter can
// be correct for the wrong reason -- a sample the age horizon discarded never
// reaches the check at all -- and this metric has now been wrong twice, so the
// assertion is on the state itself.
func watermark(e *PrometheusExporter, s *Sample) (int64, bool) {
	e.sentMu.Lock()
	defer e.sentMu.Unlock()
	head, ok := e.lastSent[seriesKey(s)]
	return head.at, ok
}

// TestAFailedWriteDoesNotCountAsWritten keeps the ordering check honest across
// a batch that never landed.
//
// What the endpoint has accepted is only known once it has accepted it.
// Recording a batch as written when it was merely OFFERED makes the counter
// fire hardest while a fault is being recovered from -- so the metric that
// names this class of bug would argue against the mechanism fixing it.
//
// The endpoint refuses with 400, which is not retryable, so the batch is
// discarded rather than retried: nothing is ever written for the series.
func TestAFailedWriteDoesNotCountAsWritten(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	exporter, err := NewPrometheusExporter(server.URL, 1, 10*time.Millisecond, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	defer func() { _ = exporter.Close() }()

	exporter.maxSampleAge = time.Hour

	offered := sample(1, time.Now())
	if _, recorded := watermark(exporter, offered); recorded {
		t.Fatal("the series already had a watermark before anything was written")
	}

	own := privateCounters(exporter)
	if err := exporter.Export(context.Background(), []*Sample{offered}); err != nil {
		t.Fatalf("Export: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if testutil.ToFloat64(own.dropped.WithLabelValues("rejected")) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if testutil.ToFloat64(own.dropped.WithLabelValues("rejected")) == 0 {
		t.Fatal("the batch was never refused and discarded, so nothing was tested")
	}

	// THE assertion: nothing landed, so the series has no watermark.
	if at, recorded := watermark(exporter, offered); recorded {
		t.Errorf("the series has a watermark of %d after a batch that was refused "+
			"and discarded: nothing was written for it, so a later reading is not "+
			"late and must not be counted as such", at)
	}
}

// TestDisorderInsideOneBatchIsRepairedNotRefused separates the two facts this
// counter kept confusing.
//
// Two overlapping polls of one device put both readings in the same batch, in
// completion order. That is worth knowing -- it says something upstream is
// producing a series' readings out of order -- but the encoder sorts each
// series before building the request, so the write SUCCEEDS. Counting it as
// "the endpoint will refuse this" raised an ERROR on traffic that was about to
// be delivered correctly.
//
// The endpoint is checked too: it must actually accept the request, or the
// premise of the whole distinction is wrong.
func TestDisorderInsideOneBatchIsRepairedNotRefused(t *testing.T) {
	var accepted atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accepted.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	exporter, err := NewPrometheusExporter(server.URL, 1000, time.Hour, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	defer func() { _ = exporter.Close() }()

	own := privateCounters(exporter)
	at := time.Now()

	// One series, newest first, then two readings behind it, on a series
	// nothing has written.
	if err := exporter.Export(context.Background(), []*Sample{
		sample(1, at),
		sample(1, at.Add(-2*time.Second)),
		sample(1, at.Add(-time.Second)),
	}); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if err := exporter.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if got := testutil.ToFloat64(own.repaired); got != 2 {
		t.Errorf("counted %v repaired samples, want 2: both later readings are "+
			"behind the newest in their own batch", got)
	}
	if got := testutil.ToFloat64(own.outOfOrder); got != 0 {
		t.Errorf("counted %v as refused, want 0: the encoder sorts each series "+
			"before sending, so the endpoint accepts this", got)
	}
	if got := accepted.Load(); got != 1 {
		t.Errorf("the endpoint saw %d accepted requests, want 1 -- if it refused "+
			"this, the distinction being drawn here is wrong", got)
	}
}

// TestABatchInOrderWithinItselfIsNotCounted keeps that specific: a batch
// carrying many readings for one series ascending is the ordinary case.
func TestABatchInOrderWithinItselfIsNotCounted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	exporter, err := NewPrometheusExporter(server.URL, 1000, time.Hour, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	defer func() { _ = exporter.Close() }()

	own := privateCounters(exporter)
	at := time.Now()

	if err := exporter.Export(context.Background(), []*Sample{
		sample(1, at.Add(-2*time.Second)),
		sample(1, at.Add(-time.Second)),
		sample(1, at),
	}); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if err := exporter.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if got := testutil.ToFloat64(own.outOfOrder); got != 0 {
		t.Errorf("counted %v out-of-order samples in an ascending batch, want 0", got)
	}
}

// TestShutdownIsBounded stops the sender from holding the process open.
//
// Close's own deadline bounds only the Flush that precedes it. The drain after
// it then starts a fresh sendTimeout for every batch, and -- the part that
// survived the first version of this test -- a request ALREADY in flight has
// its own 30-second context that no deadline set later can reach. The bound has
// to be something the request itself observes.
//
// The handler blocks until its request context is canceled, so the only way
// this returns is a cancellation the sender actually propagates. An earlier
// version used a 200ms handler and asserted a 30-second ceiling -- which is the
// length of the unbounded stall, so it passed whether or not the bound worked.
// A bound test whose threshold equals the behavior it is bounding measures
// nothing.
func TestShutdownIsBounded(t *testing.T) {
	served := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(served) })
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()

	// Registered after server.Close so it runs BEFORE it. A server the client
	// has abandoned does not necessarily notice: net/http cancels the request
	// context when the connection closes, and a handler that is merely not
	// answering may never be told. Without this the teardown hangs even when
	// the thing under test works -- which is what it did.
	defer close(release)

	exporter, err := NewPrometheusExporter(server.URL, 1, time.Hour, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}

	// ONE batch. Each takes its full sendTimeout when nothing cancels it, so a
	// queue of them would make this test take half an hour to say what one
	// says in thirty seconds.
	exporter.queueMu.Lock()
	exporter.queue = append(exporter.queue, &pending{samples: []*Sample{sample(1, time.Now())}})
	exporter.queued++
	exporter.queueMu.Unlock()
	exporter.signal()

	// Wait until the sender is genuinely inside a request that will not return
	// on its own, which is the state this test exists to bound.
	select {
	case <-served:
	case <-time.After(10 * time.Second):
		t.Fatal("the sender never reached the endpoint, so no request was in flight")
	}

	restore := shutdownBudgetForTest(100 * time.Millisecond)
	defer restore()

	started := time.Now()
	err = exporter.Close()
	elapsed := time.Since(started)

	// Bounded AND honest. A shutdown that gave up on queued samples did not
	// succeed, and returning nil here was the third place in this package
	// where discarding data reported success.
	var discarded *DroppedError
	if !errors.As(err, &discarded) {
		t.Fatalf("Close = %v, want a *DroppedError: it abandoned the batch it "+
			"could not write, and the caller is the only one who can act on that", err)
	}
	if discarded.Samples != 1 {
		t.Errorf("Close reported %d samples discarded, want 1", discarded.Samples)
	}

	// Close's Flush deadline is 10s and cannot be shortened from here, so the
	// floor is about that. The ceiling is what discriminates: an in-flight
	// request that nothing cancels runs to its own 30-second timeout.
	if elapsed > 20*time.Second {
		t.Errorf("Close took %v: the request in flight is not canceled, so the "+
			"shutdown deadline bounds the loop and not the thing holding it up",
			elapsed)
	}
}

// privateCounters gives one exporter counters nobody else touches.
//
// The counters an exporter increments are process-global in production, which
// is what /metrics has to publish. In a test that makes every assertion on them
// a measurement shared with the whole package: a sender still running from an
// earlier test lands an increment inside this one's window, and it fails for a
// reason that has nothing to do with what it checks. Order-dependent, seed
// dependent, and impossible to read from the failure message.
//
// With private counters the assertions can be absolute rather than deltas,
// which is the other half of the same problem: a delta only means anything if
// you know nothing else is writing.
func privateCounters(e *PrometheusExporter) *counters {
	own := &counters{
		exports: prometheus.NewCounterVec(
			prometheus.CounterOpts{Name: "test_exports_total"}, []string{"status"}),
		dropped: prometheus.NewCounterVec(
			prometheus.CounterOpts{Name: "test_dropped_samples_total"}, []string{"reason"}),
		outOfOrder: prometheus.NewCounter(
			prometheus.CounterOpts{Name: "test_out_of_order_total"}),
		repaired: prometheus.NewCounter(
			prometheus.CounterOpts{Name: "test_repaired_order_total"}),
	}
	e.counts = own
	return own
}

// TestTheBacklogGaugeCountsEveryUndeliveredSample pins the quantity, not one
// writer of it.
//
// Undelivered samples live in three places: still accumulating towards a batch,
// queued for the sender, and the one batch being written. This has been wrong
// three times, each time because a writer reported its own slice as the total
// -- the batch on the wire left out entirely, then a refactor adding a third
// home while two writers kept reporting two.
//
// So the assertion is on the sum with all three non-empty at once, which is the
// state every point-fix agreed about locally and got wrong together.
func TestTheBacklogGaugeCountsEveryUndeliveredSample(t *testing.T) {
	inFlight := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(inFlight) })
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	defer close(release)

	// batchSize 10: a batch of exactly 10 is handed over, anything less stays
	// in the buffer.
	exporter, err := NewPrometheusExporter(server.URL, 10, time.Hour, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	defer func() { _ = exporter.Close() }()

	at := time.Now()
	batch := make([]*Sample, 10)
	for i := range batch {
		batch[i] = sample(i, at)
	}

	// One batch goes to the sender and is held by the endpoint: in flight.
	if err := exporter.Export(context.Background(), batch); err != nil {
		t.Fatalf("Export: %v", err)
	}
	select {
	case <-inFlight:
	case <-time.After(10 * time.Second):
		t.Fatal("the sender never reached the endpoint, so nothing is in flight")
	}

	// A second batch queues behind it, and three more samples stay in the
	// buffer without reaching the batch size.
	if err := exporter.Export(context.Background(), batch); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if err := exporter.Export(context.Background(), []*Sample{
		sample(100, at), sample(101, at), sample(102, at),
	}); err != nil {
		t.Fatalf("Export: %v", err)
	}

	// 10 in flight + 10 queued + 3 buffered.
	const want = 23
	if got := exporter.BufferedSamples(); got != want {
		t.Errorf("BufferedSamples = %d, want %d (10 in flight, 10 queued, 3 buffered)", got, want)
	}
	if got := testutil.ToFloat64(exportBufferedSamples); got != want {
		t.Errorf("gonetmon_export_buffered_samples = %v, want %d: the gauge and the "+
			"accessor are the same quantity, and an operator watching the gauge "+
			"during a stall sees whichever slice its last writer owned", got, want)
	}
}

// TestExportAfterCloseIsRefusedNotQueued closes the last hole in "nothing
// leaves without something observable saying so".
//
// Export used to write, so a call after Close failed loudly of its own accord.
// It queues now, and a queue whose sender has stopped is a place samples go to
// be forgotten: Export returned nil while the data could never be delivered.
// The async refactor turned a synchronous failure into permanent silent loss.
func TestExportAfterCloseIsRefusedNotQueued(t *testing.T) {
	var received atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	// batchSize 1, so any Export would enqueue a batch of its own.
	exporter, err := NewPrometheusExporter(server.URL, 1, time.Hour, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	if err := exporter.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var refused *ClosedError
	if err := exporter.Export(context.Background(), []*Sample{sample(1, time.Now())}); !errors.As(err, &refused) {
		t.Fatalf("Export after Close = %v, want a *ClosedError: the sender has "+
			"stopped, so anything accepted now is queued where nothing can send it", err)
	}
	if refused.Samples != 1 {
		t.Errorf("ClosedError named %d samples, want 1", refused.Samples)
	}

	if err := exporter.Flush(context.Background()); !errors.As(err, &refused) {
		t.Errorf("Flush after Close = %v, want a *ClosedError", err)
	}

	// And nothing is left sitting in a queue nobody drains.
	if got := exporter.BufferedSamples(); got != 0 {
		t.Errorf("%d samples queued after Close: they can never be delivered", got)
	}
}

// TestCloseStillFlushesWhatWasAlreadyBuffered guards the other direction: the
// admission check must not refuse Close's own final flush, or shutdown would
// discard the batch it exists to deliver.
func TestCloseStillFlushesWhatWasAlreadyBuffered(t *testing.T) {
	var received atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	// A batch size the sample below will not reach, so it is still in the
	// buffer when Close runs and only Close's flush can deliver it.
	exporter, err := NewPrometheusExporter(server.URL, 100, time.Hour, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	if err := exporter.Export(context.Background(), []*Sample{sample(1, time.Now())}); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if err := exporter.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if got := received.Load(); got != 1 {
		t.Errorf("the endpoint saw %d requests, want 1: Close must still deliver "+
			"what was buffered before it", got)
	}
}

// TestCloseReportsWhatAnUnendingOutageLeftUnwritten is the other way shutdown
// gives up.
//
// TestShutdownIsBounded covers a request canceled in flight. This covers the
// endpoint answering promptly and retryably forever: the batch is requeued each
// time -- correctly, since 503 is retryable and the samples are still young --
// until the shutdown deadline stops the loop. Whatever is queued then can never
// be delivered, because the sender is the only thing that could have delivered
// it and it is about to return.
//
// Both paths end at the same place, and both must tell the caller. A log line
// is not a return value: the process is exiting, and the only code that can act
// on "we lost 40,000 samples" is the code that called Close.
func TestCloseReportsWhatAnUnendingOutageLeftUnwritten(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	exporter, err := NewPrometheusExporter(server.URL, 1, 10*time.Millisecond, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}

	own := privateCounters(exporter)
	if err := exporter.Export(context.Background(), []*Sample{sample(1, time.Now())}); err != nil {
		t.Fatalf("Export: %v", err)
	}

	// Let it be refused a few times, so this is unmistakably the retry path
	// rather than a batch that never reached the endpoint at all.
	deadline := time.Now().Add(5 * time.Second)
	for requests.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := requests.Load(); got < 3 {
		t.Fatalf("the endpoint saw %d attempts, want at least 3: the batch is not "+
			"being retried, so this is not the path under test", got)
	}

	restore := shutdownBudgetForTest(50 * time.Millisecond)
	defer restore()

	var unwritten *DroppedError
	if err := exporter.Close(); !errors.As(err, &unwritten) {
		t.Fatalf("Close = %v, want a *DroppedError: the queue it abandoned has no "+
			"sender left and can never be delivered", err)
	}
	if unwritten.Samples != 1 {
		t.Errorf("Close reported %d samples unwritten, want 1", unwritten.Samples)
	}
	if got := testutil.ToFloat64(own.dropped.WithLabelValues("shutdown")); got != 1 {
		t.Errorf("counted %v samples dropped at shutdown, want 1: it goes through "+
			"the same discard path as every other drop, and names its reason", got)
	}
}

// TestOneStaleSampleDoesNotCostTheWholeBatch is the amplification this
// partition exists to remove.
//
// Prometheus refuses per REQUEST, not per sample: the whole write is rejected
// on the first unacceptable one. And a 400 is not retryable, so the batch is
// then discarded -- correctly, and reported as reason="rejected" perfectly
// accurately, about a loss that never needed to happen.
//
// The exporter already knew which sample was the problem. Sending the batch
// anyway turned one bad reading into a thousand lost ones.
func TestOneStaleSampleDoesNotCostTheWholeBatch(t *testing.T) {
	var delivered atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		raw, err := snappy.Decode(nil, body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var request prompb.WriteRequest
		if err := request.Unmarshal(raw); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		for _, ts := range request.Timeseries {
			delivered.Add(int64(len(ts.Samples)))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	exporter, err := NewPrometheusExporter(server.URL, 1000, time.Hour, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	defer func() { _ = exporter.Close() }()

	exporter.maxSampleAge = time.Hour
	own := privateCounters(exporter)
	at := time.Now()

	// Establish a head for series 0.
	if err := exporter.Export(context.Background(), []*Sample{sample(0, at)}); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if err := exporter.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	delivered.Store(0)

	// One reading behind that head, among 999 unrelated fresh ones.
	batch := []*Sample{sample(0, at.Add(-time.Minute))}
	for i := 1; i < 1000; i++ {
		batch = append(batch, sample(i, at))
	}
	if err := exporter.Export(context.Background(), batch); err != nil {
		t.Fatalf("Export: %v", err)
	}

	var discarded *DroppedError
	if err := exporter.Flush(context.Background()); !errors.As(err, &discarded) {
		t.Fatalf("Flush = %v, want a *DroppedError naming the one sample it dropped", err)
	}
	if discarded.Samples != 1 {
		t.Errorf("Flush reported %d discarded, want 1", discarded.Samples)
	}

	if got := delivered.Load(); got != 999 {
		t.Errorf("the endpoint received %d samples, want 999: one stale reading "+
			"must not take the batch with it", got)
	}
	if got := testutil.ToFloat64(own.dropped.WithLabelValues("superseded")); got != 1 {
		t.Errorf("dropped %v as superseded, want 1", got)
	}
	if got := testutil.ToFloat64(own.dropped.WithLabelValues("rejected")); got != 0 {
		t.Errorf("dropped %v as rejected, want 0: the request the endpoint saw was "+
			"acceptable, so nothing should have been refused", got)
	}
}

// TestTwoReadingsInOneMillisecondWithDifferentValues is the other way a sample
// is unacceptable, and the one a timestamp comparison alone misses.
//
// Two reads inside one millisecond is ordinary on a fast poller. Prometheus
// takes the same timestamp twice only if the value matches; if it differs the
// write is refused as a duplicate, with the same whole-batch cost. A guard
// testing only "older than" lets it straight through.
func TestTwoReadingsInOneMillisecondWithDifferentValues(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	exporter, err := NewPrometheusExporter(server.URL, 1000, time.Hour, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	defer func() { _ = exporter.Close() }()

	exporter.maxSampleAge = time.Hour
	own := privateCounters(exporter)
	at := time.Now()

	first := &Sample{Name: "snmp_dup", Labels: map[string]string{"d": "1"}, Value: 1, Timestamp: at}
	if err := exporter.Export(context.Background(), []*Sample{first}); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if err := exporter.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Same instant, different reading: a duplicate the endpoint refuses.
	conflicting := &Sample{Name: "snmp_dup", Labels: map[string]string{"d": "1"}, Value: 2, Timestamp: at}
	if err := exporter.Export(context.Background(), []*Sample{conflicting}); err != nil {
		t.Fatalf("Export: %v", err)
	}
	var discarded *DroppedError
	if err := exporter.Flush(context.Background()); !errors.As(err, &discarded) {
		t.Fatalf("Flush = %v, want a *DroppedError: same millisecond, different "+
			"value, which the endpoint refuses as a duplicate", err)
	}
	if got := testutil.ToFloat64(own.dropped.WithLabelValues("superseded")); got != 1 {
		t.Errorf("dropped %v as superseded, want 1", got)
	}

	// The SAME reading offered twice is not a conflict and must go through.
	again := &Sample{Name: "snmp_dup", Labels: map[string]string{"d": "1"}, Value: 1, Timestamp: at}
	if err := exporter.Export(context.Background(), []*Sample{again}); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if err := exporter.Flush(context.Background()); err != nil {
		t.Errorf("Flush = %v: the same reading offered twice is accepted by the "+
			"endpoint, so refusing it here discards a sample it would have taken", err)
	}
	if got := testutil.ToFloat64(own.dropped.WithLabelValues("superseded")); got != 1 {
		t.Errorf("dropped %v as superseded, want still 1", got)
	}
}

// TestTwoConflictingReadingsInOneBatchAreNotCollapsedAwaySilently covers the
// duplicate that arrives inside a SINGLE Export rather than across two.
//
// Both readings reach the encoder together, so the cross-batch check against
// what the endpoint has already accepted never sees them -- nothing had been
// accepted yet. The encoder used to collapse the pair and keep one, and the
// batch was then reported fully sent: no dropped counter moved, the flush
// returned nil, and a reading was gone. That is the same fail-open shape as a
// 4xx swallowed during shutdown, arriving one layer up.
//
// The first reading must be delivered, the second must be named as a discard,
// and the flush must say so.
func TestTwoConflictingReadingsInOneBatchAreNotCollapsedAwaySilently(t *testing.T) {
	var delivered atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		raw, err := snappy.Decode(nil, body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var request prompb.WriteRequest
		if err := request.Unmarshal(raw); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		for _, ts := range request.Timeseries {
			delivered.Add(int64(len(ts.Samples)))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	exporter, err := NewPrometheusExporter(server.URL, 1000, time.Hour, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	defer func() { _ = exporter.Close() }()

	exporter.maxSampleAge = time.Hour
	own := privateCounters(exporter)
	at := time.Now()
	series := map[string]string{"d": "pdu-03"}

	batch := []*Sample{
		{Name: "snmp_dup_in_batch", Labels: series, Value: 100, Timestamp: at},
		{Name: "snmp_dup_in_batch", Labels: series, Value: 150, Timestamp: at},
		{Name: "snmp_dup_in_batch", Labels: series, Value: 200, Timestamp: at.Add(time.Second)},
	}
	if err := exporter.Export(context.Background(), batch); err != nil {
		t.Fatalf("Export: %v", err)
	}

	var discarded *DroppedError
	if err := exporter.Flush(context.Background()); !errors.As(err, &discarded) {
		t.Fatalf("Flush = %v, want a *DroppedError: two of these three readings "+
			"claim one instant with different values, so one cannot be delivered", err)
	}
	if discarded.Samples != 1 {
		t.Errorf("Flush reported %d sample(s) lost, want 1", discarded.Samples)
	}
	if got := testutil.ToFloat64(own.dropped.WithLabelValues("superseded")); got != 1 {
		t.Errorf("dropped %v as superseded, want 1", got)
	}
	if got := testutil.ToFloat64(own.dropped.WithLabelValues("rejected")); got != 0 {
		t.Errorf("dropped %v as rejected, want 0: the request the endpoint saw was "+
			"acceptable, so the batch must not have been refused whole", got)
	}
	if got := testutil.ToFloat64(own.outOfOrder); got != 1 {
		t.Errorf("out_of_order = %v, want 1", got)
	}
	if got := delivered.Load(); got != 2 {
		t.Errorf("delivered %d sample(s), want 2: the conflicting reading is taken "+
			"out, and the other two go through rather than being lost with it", got)
	}
}

// TestTwoReadingsAreTheSameReadingOnlyIfTheEndpointSaysSo covers the two float
// values where Go's == and Prometheus's duplicate check disagree.
//
// Prometheus compares math.Float64bits. Comparing the float instead is wrong in
// both directions, and only one of the two is loud:
//
//   - +0 and -0 are == but have different bits. Judged "the same reading", the
//     pair is passed through and collapsed at encode time -- and Prometheus
//     would have refused the request as a duplicate. One reading gone, flush
//     reported successful. The silent loss again, from a sign bit.
//   - Two NaNs with the same bits are != but are the same reading to
//     Prometheus. Judged a conflict, one is discarded for no reason.
//
// A test using ordinary values cannot tell the two comparisons apart, which is
// why these two live here rather than as another case in the batch above.
func TestTwoReadingsAreTheSameReadingOnlyIfTheEndpointSaysSo(t *testing.T) {
	deliver := func(t *testing.T, batch []*Sample) (*PrometheusExporter, *counters, int64, error) {
		t.Helper()

		var delivered atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			raw, err := snappy.Decode(nil, body)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			var request prompb.WriteRequest
			if err := request.Unmarshal(raw); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			for _, ts := range request.Timeseries {
				delivered.Add(int64(len(ts.Samples)))
			}
			w.WriteHeader(http.StatusNoContent)
		}))
		t.Cleanup(server.Close)

		exporter, err := NewPrometheusExporter(server.URL, 1000, time.Hour, nil)
		if err != nil {
			t.Fatalf("NewPrometheusExporter: %v", err)
		}
		t.Cleanup(func() { _ = exporter.Close() })
		exporter.maxSampleAge = time.Hour

		// Installed BEFORE anything is exported: privateCounters swaps the
		// exporter's counter set, so reading it afterwards would read a set
		// nothing had ever written to and every count would be a zero that
		// cannot fail.
		own := privateCounters(exporter)

		if err := exporter.Export(context.Background(), batch); err != nil {
			t.Fatalf("Export: %v", err)
		}
		flushErr := exporter.Flush(context.Background())
		return exporter, own, delivered.Load(), flushErr
	}

	t.Run("positive and negative zero are two readings", func(t *testing.T) {
		at := time.Now()
		series := map[string]string{"d": "signed-zero"}
		_, own, delivered, err := deliver(t, []*Sample{
			{Name: "snmp_zero", Labels: series, Value: 0, Timestamp: at},
			{Name: "snmp_zero", Labels: series, Value: math.Copysign(0, -1), Timestamp: at},
		})

		var discarded *DroppedError
		if !errors.As(err, &discarded) {
			t.Fatalf("Flush = %v, want a *DroppedError: +0 and -0 have different "+
				"bits, so Prometheus refuses the second as a duplicate", err)
		}
		if got := testutil.ToFloat64(own.dropped.WithLabelValues("superseded")); got != 1 {
			t.Errorf("dropped %v as superseded, want 1", got)
		}
		if delivered != 1 {
			t.Errorf("delivered %d sample(s), want 1", delivered)
		}
	})

	t.Run("the same NaN twice is one reading", func(t *testing.T) {
		at := time.Now()
		series := map[string]string{"d": "not-a-number"}
		_, own, delivered, err := deliver(t, []*Sample{
			{Name: "snmp_nan", Labels: series, Value: math.NaN(), Timestamp: at},
			{Name: "snmp_nan", Labels: series, Value: math.NaN(), Timestamp: at},
		})

		if err != nil {
			t.Fatalf("Flush = %v, want nil: identical bits are the same reading to "+
				"Prometheus, so nothing here needed discarding", err)
		}
		if got := testutil.ToFloat64(own.dropped.WithLabelValues("superseded")); got != 0 {
			t.Errorf("dropped %v as superseded, want 0", got)
		}
		if delivered != 1 {
			t.Errorf("delivered %d sample(s), want 1: the repeat is collapsed, not sent twice", delivered)
		}
	})
}

// TestCloseReportsAFinal4xxThatThrewSamplesAway is the fifth instance of an
// operation reporting success while discarding, and the one that survived the
// structural fix.
//
// A discard carries three obligations: name the reason, charge the barrier, and
// tell Close. The first two were brought inside discardLocked and that closed
// four instances. The third was fed by a separate accumulator updated by hand,
// so a non-retryable refusal during the final drain went through the single
// path correctly, was counted correctly, and was still invisible to Close --
// which returned nil, so the DATA LOSS line never fired either.
//
// The endpoint answers 400, which is not retryable, so the batch is discarded
// as "rejected" rather than as "shutdown". That is the point: the reason is not
// what makes it Close's business, having happened during shutdown is.
func TestCloseReportsAFinal4xxThatThrewSamplesAway(t *testing.T) {
	var refused atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refused.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	// A batch size the sample will not reach, so nothing is written until
	// Close's own submit hands it to the final drain.
	exporter, err := NewPrometheusExporter(server.URL, 100, time.Hour, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	own := privateCounters(exporter)

	if err := exporter.Export(context.Background(), []*Sample{sample(1, time.Now())}); err != nil {
		t.Fatalf("Export: %v", err)
	}

	var lost *DroppedError
	if err := exporter.Close(); !errors.As(err, &lost) {
		t.Fatalf("Close = %v, want a *DroppedError: the final write was refused "+
			"for a reason that cannot be retried, and the batch was thrown away", err)
	}
	if lost.Samples != 1 {
		t.Errorf("Close reported %d samples lost, want 1", lost.Samples)
	}
	if got := refused.Load(); got == 0 {
		t.Error("the endpoint was never called, so nothing was refused and this " +
			"test proved nothing")
	}
	if got := testutil.ToFloat64(own.dropped.WithLabelValues("rejected")); got != 1 {
		t.Errorf("counted %v dropped as rejected, want 1: the reason is still its "+
			"own, it is being shut down that makes it Close's business", got)
	}
}
