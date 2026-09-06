package metrics

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"
)

// shutdownBudget bounds how long the sender keeps writing after Close.
//
// Close's own deadline bounds only the Flush that precedes it. Without this the
// stop path drains the whole queue, starting a fresh sendTimeout for every
// batch -- up to a hundred sequential requests against the 100,000-sample
// floor, so a slow endpoint that never quite times out can hold shutdown for
// tens of minutes.
var shutdownBudget = 15 * time.Second

// shutdownBudgetForTest shortens the budget and returns a function restoring
// it. Tests would otherwise have to wait out the real one to prove it exists.
func shutdownBudgetForTest(d time.Duration) func() {
	previous := shutdownBudget
	shutdownBudget = d
	return func() { shutdownBudget = previous }
}

// retryable reports whether an endpoint's refusal is worth offering again.
//
// Prometheus Remote-Write 1.0 is explicit, and this is not a judgement call:
//
//	"Senders MUST NOT retry on HTTP 4xx responses other than 429"
//	-- prometheus.io/docs/specs/remote_write_spec, Retries & Backoff
//
// So 5xx and 429 are retried, transport failures are retried because the
// request may never have been seen at all, and every other 4xx is a refusal
// the endpoint will repeat forever: a malformed request, a sample older than
// what it holds, a rejected label set.
//
// Counting refusals and giving up after N was the previous rule and was wrong
// in the ordinary case: a Prometheus restart lasting longer than three flushes
// discarded fresh samples that would have been accepted seconds later. A
// retry bound that ignores the error class is not a backstop, it is an
// outage-triggered drop. Age and depth remain the bounds on memory.
func retryable(err error) bool {
	var refused *RemoteWriteError
	if !errors.As(err, &refused) {
		// A transport error: connection refused, reset, timed out. The write
		// may never have arrived, so it is worth repeating.
		return true
	}

	if refused.StatusCode == http.StatusTooManyRequests {
		return true
	}
	return refused.StatusCode >= 500
}

// minSampleAgeHorizon floors how long a sample may wait to be written.
//
// Below about a minute the horizon starts discarding samples that a briefly
// slow endpoint would still have accepted.
const minSampleAgeHorizon = time.Minute

// sampleAgeMultiplier x flushInterval is the horizon when that is the larger.
// Six intervals is long enough to ride out a handful of consecutive failures
// and short enough that what is dropped had almost certainly been superseded.
const sampleAgeMultiplier = 6

// sendTimeout bounds one remote-write request. The exporter's HTTP client
// carries the same ceiling; this is what stops a request that never returns
// from stalling the one sender behind it.
const sendTimeout = 30 * time.Second

// sampleAgeHorizon is how long a sample may wait to be written, derived from
// how often writing is attempted.
func sampleAgeHorizon(flushInterval time.Duration) time.Duration {
	horizon := flushInterval * sampleAgeMultiplier
	if horizon < minSampleAgeHorizon {
		return minSampleAgeHorizon
	}
	return horizon
}

// submit hands whatever is buffered to the sender without waiting for it, and
// wakes the sender whether or not there was anything to hand over.
//
// The wake is unconditional because a batch the endpoint refused stays in the
// queue, and nothing else would ever go back for it: the sender sleeps after a
// refusal, and enqueue only wakes it when NEW samples arrive. An exporter whose
// devices had gone quiet would then hold a failed batch until something else
// happened to produce a sample. The flush interval is the retry cadence.
func (e *PrometheusExporter) submit() {
	e.bufferLock.Lock()
	handed := len(e.buffer) > 0
	if handed {
		batch := e.buffer
		e.buffer = nil
		e.enqueue(&pending{samples: batch})
	}
	e.bufferLock.Unlock()

	if !handed {
		e.signal()
	}
	e.publishBacklog()
}

// pending is one batch waiting to be written, and what has happened to it.
type pending struct {
	samples []*Sample

	// done, when non-nil, is closed with the outcome once the batch has been
	// attempted. Flush waits on it; the poll path does not.
	done chan error

	// discarded counts samples thrown away AHEAD of this barrier, BY REASON, so
	// the Flush waiting on it learns that what it was responsible for never
	// reached the endpoint. A flush that returns nil must mean "delivered",
	// never "thrown away" -- that is the silent loss this whole package exists
	// to end, and it would otherwise reappear at the far end of the pipe.
	//
	// By reason rather than as a total, because the total alone was what the
	// caller got and it was reported as backpressure regardless: a Flush whose
	// readings had been superseded by a later poll was told the queue was full.
	// The reason is known at the discard and is worth exactly as much to the
	// caller as it is to the counter it is already recorded on.
	discarded map[string]int
}

// charge records samples discarded ahead of this barrier, under the reason they
// were discarded for.
func (p *pending) charge(reason string, samples int) {
	if p.discarded == nil {
		p.discarded = make(map[string]int, 1)
	}
	p.discarded[reason] += samples
}

// finishBarrier completes a barrier, reporting anything discarded ahead of it.
//
// The reason is passed on only when there is ONE. A Flush that waited behind
// several different discards has several answers, and naming one of them would
// be a guess presented as a fact -- which is the same defect this carries the
// reason to fix, in the other direction.
func (p *pending) finishBarrier() {
	total := 0
	for _, samples := range p.discarded {
		total += samples
	}
	if total == 0 {
		p.finish(nil)
		return
	}

	reason := ""
	if len(p.discarded) == 1 {
		for named := range p.discarded {
			reason = named
		}
	}
	p.finish(&DroppedError{Samples: total, Reason: reason})
}

// enqueue hands a batch to the sender and reports whether it was accepted.
//
// It never blocks on the network. That is the whole point of the sender: a
// poll worker that fills the buffer used to perform the remote-write request
// itself, which put the slowest thing in the system inside the poll cycle.
func (e *PrometheusExporter) enqueue(batch *pending) {
	e.enqueueAll(batch)
}

// enqueueAll adds several entries to the queue as ONE step.
//
// Flush submits a batch and a barrier, and they have to arrive together: the
// sender drains on its own goroutine, so between two separate enqueues it can
// take the batch, expire a sample out of it, and find no barrier behind it to
// charge -- and the Flush that was responsible for that sample then returns nil
// having lost it. Measured at 1 failure in 30 under load before this.
func (e *PrometheusExporter) enqueueAll(batches ...*pending) {
	e.queueMu.Lock()
	for _, batch := range batches {
		// A zero-sample entry with a waiter is a BARRIER: it carries nothing,
		// and completes when the sender reaches it. That is how Flush waits for
		// what is already queued as well as for what it submitted -- without
		// it, Flush returned as soon as its own batch was attempted while an
		// earlier one was still unwritten, which is not what "flush" means.
		if len(batch.samples) == 0 && batch.done == nil {
			continue
		}
		e.queue = append(e.queue, batch)
		e.queued += len(batch.samples)
	}
	e.boundQueueLocked()
	e.queueMu.Unlock()

	// The gauge is NOT published here. enqueue is called with bufferLock held
	// -- that is what makes detaching and queueing one step -- and backlog
	// takes bufferLock, so publishing from here deadlocks against the caller.
	// Every caller publishes once it is out of the locks.
	e.signal()
}

// signal nudges the sender.
//
// Buffered by one, so a wake is never missed and never blocks: the sender
// re-reads the queue after every send, so one pending wake is enough to bring
// it back however many batches arrived.
func (e *PrometheusExporter) signal() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// finish reports a batch's outcome to whoever is waiting for it, if anyone is.
func (p *pending) finish(err error) {
	if p.done != nil {
		p.done <- err
		close(p.done)
		p.done = nil
	}
}

// boundQueueLocked keeps the queue within maxBufferSize. The caller must hold
// queueMu.
//
// Age first, then depth. Dropping by AGE is the principled half: a sample
// STAMPED longer ago than the horizon has almost certainly been superseded at
// the endpoint by a newer one for its series, which makes it not merely stale
// but unacceptable -- retrying it can only fail. Dropping by depth is the
// backstop for a burst that outruns the endpoint while every sample in it is
// still fresh.
//
// The horizon is measured against the sample's own timestamp, not against how
// long it has sat in this queue. That is deliberate: what makes a sample
// unacceptable is where it falls relative to the endpoint's head for its
// series, which its timestamp says and its residence time does not. The cost
// is that a device reporting historical or clock-skewed readings has them
// dropped without ever being offered.
func (e *PrometheusExporter) boundQueueLocked() {
	if e.queued <= e.maxBufferSize {
		return
	}

	if aged := e.expireLocked(); aged > 0 {
		log.Printf("[metrics] dropped %d sample(s) that had waited longer than %s to be written -- remote write at %s is not keeping up",
			aged, e.maxSampleAge, e.remoteWriteURL)
	}
	if e.queued <= e.maxBufferSize {
		return
	}

	// Still over. The oldest batches go, because the queue is in the order the
	// samples were taken and the front of it is the furthest behind.
	//
	// Barriers are stepped over rather than evicted. One carries no samples,
	// so removing it makes no progress towards the bound, and completing it
	// with a DroppedError would tell a Flush that its work was discarded when
	// everything ahead of it may have been written successfully.
	dropped := 0
	kept := make([]*pending, 0, len(e.queue))
	for i, batch := range e.queue {
		if e.queued <= e.maxBufferSize {
			kept = append(kept, e.queue[i:]...)
			break
		}
		if len(batch.samples) == 0 {
			kept = append(kept, batch)
			continue
		}
		e.queued -= len(batch.samples)
		dropped += len(batch.samples)
		batch.finish(e.discardLocked(len(batch.samples), dropDepth, i+1))
	}
	e.queue = kept
	if dropped > 0 {
		log.Printf("[metrics] ERROR: export queue exceeded %d samples; dropping the %d oldest awaiting write to bound memory use -- remote write at %s has been failing",
			e.maxBufferSize, dropped, e.remoteWriteURL)
	}
}

// expireLocked drops samples older than the horizon and returns how many.
// The caller must hold queueMu.
func (e *PrometheusExporter) expireLocked() int {
	if e.maxSampleAge <= 0 {
		return 0
	}

	cutoff := e.now().Add(-e.maxSampleAge)
	dropped := 0
	kept := e.queue[:0]

	for i, batch := range e.queue {
		// A barrier carries nothing by design and must keep its place: it
		// completes when the sender REACHES it, which is what tells Flush that
		// everything ahead of it has been attempted. Treating "no samples" as
		// "entirely expired" completed every barrier the moment it was queued,
		// so Flush returned before anything had been written.
		if len(batch.samples) == 0 {
			kept = append(kept, batch)
			continue
		}

		fresh := batch.samples[:0]
		for _, sample := range batch.samples {
			if sample.Timestamp.Before(cutoff) {
				dropped++
				continue
			}
			fresh = append(fresh, sample)
		}
		aged := len(batch.samples) - len(fresh)
		batch.samples = fresh
		owed := e.discardLocked(aged, dropAged, i+1)

		if len(batch.samples) == 0 {
			// The batch is gone, not flushed. Its own waiter hears that.
			batch.finish(owed)
			continue
		}
		kept = append(kept, batch)
	}

	e.queue = kept
	e.queued -= dropped
	return dropped
}

// abandon removes a barrier its waiter has given up on.
//
// Identity makes this safe without any further coordination: if the barrier is
// still in the queue the sender has not reached it, so removing it loses
// nothing; if it is not, the sender already owns it and will complete it into
// a buffered channel nobody reads. Eviction by policy was the alternative and
// was worse -- it could not tell an abandoned waiter from a live one, so a
// busy exporter would hand a synthetic failure to a Flush whose work then
// succeeded.
func (e *PrometheusExporter) abandon(barrier *pending) {
	e.queueMu.Lock()
	defer e.queueMu.Unlock()

	for i, batch := range e.queue {
		if batch == barrier {
			e.queue = append(e.queue[:i], e.queue[i+1:]...)
			return
		}
	}
}

// releaseBarriersLocked completes every barrier in the queue with err and
// removes it, returning how many. The caller must hold queueMu.
func (e *PrometheusExporter) releaseBarriersLocked(err error) int {
	released := 0
	kept := e.queue[:0]
	for _, batch := range e.queue {
		if len(batch.samples) == 0 {
			batch.finish(err)
			released++
			continue
		}
		kept = append(kept, batch)
	}
	e.queue = kept
	return released
}

// discardLocked is the ONE way a sample leaves this exporter undelivered. The
// caller must hold queueMu.
//
// It does both halves of "nothing leaves without something observable saying
// so": it names the reason on a counter, and it tells whoever is owed an
// answer. Those were separate calls at each drop site, applied by hand -- and
// three review rounds found three different exits where one half had been
// forgotten: aged-out samples completing a waiter with nil, Export returning
// nil into a queue nothing could drain, Close returning nil after giving up.
//
// One path means silently dropping is not something this code can express. A
// new exit that forgets to call it drops nothing at all, which is a visible
// bug rather than a plausible silence.
//
// behind is the queue index from which barriers are owed the news: samples
// discarded ahead of a Flush are samples that Flush was waiting for.
//
// A discard carries THREE obligations, not two. Naming the reason and charging
// the barrier were brought in here and that closed four instances of the class
// -- and a fifth appeared anyway, because the third obligation, telling Close,
// was fed by a separate accumulator that call sites updated by hand. A final
// 4xx during shutdown went through this path correctly, counted correctly, and
// was still invisible to Close, which returned nil having thrown a batch away.
//
// So the accumulation lives here too. Anything discarded once shutdown has
// begun is what Close reports, whatever the reason for it: "went through the
// single discard path" and "is reported by Close" are now one fact instead of
// two.
func (e *PrometheusExporter) discardLocked(samples int, reason string, behind int) *DroppedError {
	if samples <= 0 {
		return nil
	}

	e.counts.dropped.WithLabelValues(reason).Add(float64(samples))

	if e.closed.Load() {
		e.shutdownDropped.Add(int64(samples))
	}

	for i := behind; i < len(e.queue); i++ {
		if len(e.queue[i].samples) == 0 {
			e.queue[i].charge(reason, samples)
		}
	}

	return &DroppedError{Samples: samples, Reason: reason}
}

// takeLocked removes and returns the batch at the head of the queue.
func (e *PrometheusExporter) takeLocked() *pending {
	if len(e.queue) == 0 {
		return nil
	}
	batch := e.queue[0]
	e.queue = e.queue[1:]
	e.queued -= len(batch.samples)
	return batch
}

// requeueLocked puts a refused batch back at the head, where it was taken from.
// The caller must hold queueMu.
func (e *PrometheusExporter) requeueLocked(batch *pending) {
	e.queue = append([]*pending{batch}, e.queue...)
	e.queued += len(batch.samples)
	e.boundQueueLocked()
}

// maxTrackedSeries bounds the memory the ordering check may use.
//
// The reference fleet carries about 5,000 series. The map is cleared rather
// than evicted when it grows past this, because the check is a smoke alarm:
// losing its history costs one missed detection, and a bounded reset is
// simpler to reason about than any eviction policy worth arguing over.
const maxTrackedSeries = 50000

// partition splits a batch into what the endpoint can take and what it will
// refuse, and returns both.
//
// Refusing is per REQUEST, not per sample: Prometheus rejects the whole write
// on the first unacceptable sample in it. So identifying the offender and
// sending the batch anyway costs every other sample in it -- one stale reading
// among a thousand good ones lost all thousand, and the non-retryable path then
// reported reason="rejected" perfectly accurately about a loss that never
// needed to happen.
//
// The exporter already knew which sample it was. This acts on that.
//
// Two ways a sample is unacceptable, and Prometheus names them differently:
// older than the head for its series is "out of order", and equal to the head
// with a different value is "duplicate sample for timestamp". Equal timestamp
// AND equal value is fine -- the same reading offered twice -- so the value has
// to be compared, not just the millisecond. Two reads inside one millisecond is
// ordinary on a fast poller.
//
// A conflict inside ONE batch is refused the same way, and for the same
// reason: the request Prometheus receives carries both, so two readings for one
// series at one instant are a duplicate whether they came from one Export or
// two. Collapsing them at encode time instead would deliver one and report the
// batch fully sent, which is the silent loss this package exists to end.
//
// Disorder within the batch is different and is NOT refused: orderedSamples
// sorts each series before the request is built, so it is repaired rather than
// rejected. It is counted, because it says something upstream produced one
// series' readings out of order.
func (e *PrometheusExporter) partition(samples []*Sample) (sendable, refused []*Sample) {
	e.sentMu.Lock()
	defer e.sentMu.Unlock()

	// Two maps because they answer two different questions. maxAt holds the
	// newest instant seen so far for a series, which is what says a sample
	// arrived out of order. claimed holds the value already taken at a given
	// instant, which is what says a sample conflicts -- and a conflict need not
	// be at the newest instant, so the running maximum cannot detect it.
	maxAt := make(map[string]int64, len(samples))
	claimed := make(map[instant]float64, len(samples))
	repaired := 0
	sendable = make([]*Sample, 0, len(samples))

	for _, sample := range samples {
		key := seriesKey(sample)
		at := sample.Timestamp.UnixMilli()

		if head, written := e.lastSent[key]; written {
			if at < head.at || (at == head.at && !sameValue(sample.Value, head.value)) {
				refused = append(refused, sample)
				continue
			}
		}

		if value, taken := claimed[instant{series: key, at: at}]; taken && !sameValue(sample.Value, value) {
			refused = append(refused, sample)
			continue
		}
		claimed[instant{series: key, at: at}] = sample.Value

		if previous, inBatch := maxAt[key]; inBatch && at < previous {
			repaired++
		} else {
			maxAt[key] = at
		}
		sendable = append(sendable, sample)
	}

	if repaired > 0 {
		e.counts.repaired.Add(float64(repaired))
		log.Printf("[metrics] %d of %d sample(s) in this batch arrived out of order for their series and were sorted before sending; something upstream is producing one series' readings out of order",
			repaired, len(samples))
	}
	if len(refused) > 0 {
		e.counts.outOfOrder.Add(float64(len(refused)))
		log.Printf("[metrics] ERROR: %d of %d sample(s) conflict with a reading already accepted for their series by %s, or with another in this same batch; sending the other %d rather than losing them with these",
			len(refused), len(samples), e.remoteWriteURL, len(sendable))
	}
	return sendable, refused
}

// admissible returns what of a batch the endpoint can still take, and what its
// waiter is owed for the rest.
//
// The rest is discarded as "superseded" here rather than sent, because a
// request is refused whole: one reading the endpoint has moved past would cost
// every other reading traveling with it.
//
// The second return is an error rather than a *DroppedError deliberately. A nil
// pointer in an error interface is not nil, and a caller passing this to a
// waiter would hand it a failure that is not there.
func (e *PrometheusExporter) admissible(samples []*Sample) ([]*Sample, error) {
	sendable, refused := e.partition(samples)
	if len(refused) == 0 {
		return sendable, nil
	}

	e.queueMu.Lock()
	dropped := e.discardLocked(len(refused), dropSuperseded, 0)
	e.queueMu.Unlock()

	if dropped == nil {
		return sendable, nil
	}
	return sendable, dropped
}

// instant identifies one point of one series: the series key and the
// millisecond it claims. It is the unit Prometheus refuses a duplicate at.
type instant struct {
	series string
	at     int64
}

// recordSent notes what the endpoint has accepted, so the next batch can be
// checked against it. Called only after a successful write.
func (e *PrometheusExporter) recordSent(samples []*Sample) {
	e.sentMu.Lock()
	defer e.sentMu.Unlock()

	for _, sample := range samples {
		// Checked inside the loop, not once before it: one Export carries as
		// many samples as a poll produced -- 15,000 from a single router -- so
		// a bound tested only on entry is not a bound.
		//
		// Cleared rather than evicted. The check is a smoke alarm: losing its
		// history costs one missed detection, and a reset is simpler to reason
		// about than any eviction policy worth arguing over.
		if len(e.lastSent) >= maxTrackedSeries {
			log.Printf("[metrics] ordering check tracked %d series, at the %d bound; starting over",
				len(e.lastSent), maxTrackedSeries)
			e.lastSent = make(map[string]sentPoint, maxTrackedSeries)
		}

		key := seriesKey(sample)
		at := sample.Timestamp.UnixMilli()
		if head, seen := e.lastSent[key]; seen && at < head.at {
			// The endpoint accepted it, but it is not the newest this series
			// has: the head must not go backwards or the next batch would be
			// measured against the wrong point.
			continue
		}
		e.lastSent[key] = sentPoint{at: at, value: sample.Value}
	}
}

// sender owns the remote-write endpoint. It is the only thing that writes to
// it, which is what makes the ORDER of the writes knowable.
//
// Before this, every poll worker that filled the buffer wrote inline and the
// background flusher wrote on a tick, so two requests could be in flight with
// nothing deciding which arrived first. A batch taken later could land first
// and leave the earlier one permanently unacceptable -- the same failure as a
// malformed request, reached without one (go-netmon#69).
func (e *PrometheusExporter) sender() {
	defer e.wg.Done()

	for {
		select {
		case <-e.wake:
			e.drain()
		case <-e.stopChan:
			// Whatever is queued gets one last attempt, so a clean shutdown
			// does not discard a poll that has already been collected -- under
			// the deadline Close set, or a slow endpoint holds the process
			// open.
			e.drainUntil(time.Time{})

			// And whatever survives that is abandoned, out loud. drainUntil
			// stops on the first refusal as well as at the deadline, so an
			// endpoint answering retryably forever left the queue populated
			// and the sender simply returned -- the last exit where discarding
			// data reported success, reached by the one path that does not go
			// through the deadline.
			e.giveUp()
			return
		}
	}
}

// giveUp abandons whatever is still queued when the shutdown deadline passes.
//
// It is a drop like any other and goes through the one path that names a
// reason and tells whoever is owed -- here, Close, which must not report a
// shutdown that threw telemetry away as a success.
func (e *PrometheusExporter) giveUp() {
	e.queueMu.Lock()
	left := e.queued
	if owed := e.discardLocked(left, dropShutdown, 0); owed != nil {
		e.queued = 0
		for _, batch := range e.queue {
			batch.finish(owed)
		}
		e.queue = nil
	}
	e.queueMu.Unlock()

	if left > 0 {
		log.Printf("[metrics] shutting down with %d sample(s) unwritten: %s did not keep up within %s",
			left, e.remoteWriteURL, shutdownBudget)
	}
}

// keepWhatLanded takes an accepted prefix off a batch that failed partway
// through, so that neither the retry nor the discard that follows reaches
// samples the endpoint already holds.
//
// A batch is several requests now (#63), so a failure can arrive with part of
// it delivered. Offering that part again is not merely wasteful: the endpoint's
// head has moved to the end of it, so on the next attempt those samples are
// behind their own head -- and the request carrying them is refused whole,
// taking the undelivered remainder with it. A retry that redelivers is a retry
// that cannot succeed.
func (e *PrometheusExporter) keepWhatLanded(batch *pending, delivered int) {
	if delivered <= 0 {
		return
	}

	e.recordSent(batch.samples[:delivered])
	e.published.record(batch.samples[:delivered])
	batch.samples = batch.samples[delivered:]

	log.Printf("[metrics] %d of this batch's samples were accepted before %s failed; only the remaining %d are still owed",
		delivered, e.remoteWriteURL, len(batch.samples))
}

// drain writes queued batches until the queue is empty or one is refused.
//
// It stops on the first refusal rather than carrying on to the next batch:
// carrying on would write newer samples over the head of series the refused
// batch still holds, which is exactly what makes a refused batch unlandable.
func (e *PrometheusExporter) drain() {
	e.drainUntil(time.Time{})
}

// drainUntil is drain with a deadline. A zero deadline means no bound, which is
// the ordinary case: the sender is not in anyone's way.
func (e *PrometheusExporter) drainUntil(deadline time.Time) {
	for {
		// The shutdown deadline applies to a drain ALREADY RUNNING, not only to
		// the one the stop branch starts. Close's Flush waits on whatever the
		// sender is doing, so a bound that took effect only after that had
		// returned bounded nothing: the drain holding it up was the unbounded
		// one.
		if at := e.shutdownAt.Load(); at != 0 {
			if closing := time.Unix(0, at); deadline.IsZero() || closing.Before(deadline) {
				deadline = closing
			}
		}

		if !deadline.IsZero() && !e.now().Before(deadline) {
			e.giveUp()
			return
		}

		e.queueMu.Lock()
		e.expireLocked()
		batch := e.takeLocked()
		if batch != nil {
			e.inFlight = len(batch.samples)
		}
		e.queueMu.Unlock()

		e.publishBacklog()
		if batch == nil {
			return
		}
		if len(batch.samples) == 0 {
			// A barrier: everything queued ahead of it has been attempted --
			// or discarded, which is not the same as flushed and must not be
			// reported as it.
			batch.finishBarrier()
			continue
		}

		// Whatever the endpoint would refuse is taken out first, and dropped on
		// its own. Sending it costs every other sample in the batch: Prometheus
		// rejects the whole write on the first unacceptable sample, and the
		// non-retryable path then discards all of them.
		sendable, owed := e.admissible(batch.samples)
		if len(sendable) == 0 {
			e.queueMu.Lock()
			e.inFlight = 0
			e.queueMu.Unlock()
			// The error the discard path produced, rather than a second one
			// built here: the reason is what the waiter needs, and a duplicate
			// construction is where it went missing.
			batch.finish(owed)
			continue
		}
		batch.samples = sendable

		// Built from the exporter's own context, so Close can cancel a request
		// that is already on the wire. context.Background() here meant the
		// shutdown deadline could only ever stop the loop between batches.
		ctx, cancel := context.WithTimeout(e.sendCtx, sendTimeout)
		delivered, err := e.send(ctx, batch.samples)
		cancel()

		if err == nil {
			e.recordSent(batch.samples)
			e.published.record(batch.samples)
			e.recordSuccess(len(batch.samples))
			e.queueMu.Lock()
			e.inFlight = 0
			e.queueMu.Unlock()
			batch.finish(nil)
			continue
		}

		e.keepWhatLanded(batch, delivered)

		if !retryable(err) {
			e.queueMu.Lock()
			e.inFlight = 0
			// The waiter hears the endpoint's own words rather than a generic
			// DroppedError -- "400: out of order sample" says why.
			_ = e.discardLocked(len(batch.samples), dropRejected, 0)
			e.queueMu.Unlock()

			log.Printf("[metrics] ERROR: discarding %d sample(s) %s refused for a reason that cannot be retried: %v",
				len(batch.samples), e.remoteWriteURL, err)
			_ = e.recordFailure(len(batch.samples), err)
			batch.finish(err)
			continue
		}

		e.queueMu.Lock()
		e.inFlight = 0
		e.requeueLocked(batch)
		// Everyone waiting behind a batch that was just refused is told now.
		// Their flush cannot complete -- the refused batch is still ahead of
		// them and will be retried -- and leaving them to block until their
		// context expires would report a timeout for a failure we already know
		// the reason for.
		released := e.releaseBarriersLocked(err)
		e.queueMu.Unlock()

		e.publishBacklog()
		_ = e.recordFailure(len(batch.samples), err)
		batch.finish(err)
		if released > 0 {
			log.Printf("[metrics] %d flush(es) waiting behind the refused batch were told it failed", released)
		}
		return
	}
}
