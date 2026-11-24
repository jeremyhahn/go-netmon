package metrics

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"
)

// shutdownBudget bounds how long the sender keeps writing after Close.
var shutdownBudget = 15 * time.Second

// shutdownBudgetForTest shortens the budget and returns a function restoring
// it. Tests would otherwise have to wait out the real one to prove it exists.
func shutdownBudgetForTest(d time.Duration) func() {
	previous := shutdownBudget
	shutdownBudget = d
	return func() { shutdownBudget = previous }
}

// retryable reports whether a refusal may be retried: 5xx, 429 and transport errors, per the remote-write spec.
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

// submit hands the buffer to the sender without waiting and always wakes it, so refused batches are retried each interval.
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

	// discarded counts samples dropped ahead of this barrier, by reason, so the waiting Flush reports them.
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

// finishBarrier completes a barrier, naming a reason only when exactly one applies.
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

// enqueue hands a batch to the sender without blocking on the network and reports whether it was accepted.
func (e *PrometheusExporter) enqueue(batch *pending) {
	e.enqueueAll(batch)
}

// enqueueAll adds several entries to the queue as one step.
func (e *PrometheusExporter) enqueueAll(batches ...*pending) {
	e.queueMu.Lock()
	for _, batch := range batches {
		// A zero-sample entry with a waiter is a barrier that completes when the sender reaches it.
		if len(batch.samples) == 0 && batch.done == nil {
			continue
		}
		e.queue = append(e.queue, batch)
		e.queued += len(batch.samples)
	}
	e.boundQueueLocked()
	e.queueMu.Unlock()

	// Not published here: the caller holds bufferLock, which backlog takes.
	e.signal()
}

// signal wakes the sender; the channel is buffered by one, so it never blocks or misses.
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

// boundQueueLocked drops samples older than the horizon (by their own timestamp), then the oldest batches; caller holds queueMu.
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

	// Still over: evict the oldest batches, stepping over barriers.
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
		// A barrier keeps its place until the sender reaches it.
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

// abandon removes a barrier its waiter has given up on; a barrier already taken by the sender completes into an unread channel.
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

// discardLocked is the only way a sample leaves undelivered: it counts the reason, charges barriers from behind, and records shutdown losses for Close; caller holds queueMu.
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

// maxTrackedSeries bounds the ordering check's memory; the map is cleared, not evicted, when exceeded.
const maxTrackedSeries = 50000

// partition splits a batch into samples the endpoint will accept and those it would refuse (older than its head, or a different value at a taken instant).
func (e *PrometheusExporter) partition(samples []*Sample) (sendable, refused []*Sample) {
	e.sentMu.Lock()
	defer e.sentMu.Unlock()

	// maxAt detects out-of-order samples; claimed detects a different value at an already-taken instant.
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

// admissible returns what the endpoint can still take and the error owed for the rest (discarded as superseded); a plain error avoids a typed nil.
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
		// Bound checked per sample, since one Export can carry a whole poll; the map is cleared when full.
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

// sender is the only writer to the remote-write endpoint, so writes arrive in order.
func (e *PrometheusExporter) sender() {
	defer e.wg.Done()

	for {
		select {
		case <-e.wake:
			e.drain()
		case <-e.stopChan:
			// One last attempt at whatever is queued, bounded by Close's deadline.
			e.drainUntil(time.Time{})

			// Whatever survives is discarded and reported, including after a retryable refusal.
			e.giveUp()
			return
		}
	}
}

// giveUp discards whatever is queued when the shutdown deadline passes, reporting it to Close.
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

// keepWhatLanded removes the accepted prefix of a partly delivered batch so it is neither retried nor discarded.
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
func (e *PrometheusExporter) drain() {
	e.drainUntil(time.Time{})
}

// drainUntil is drain with a deadline. A zero deadline means no bound, which is
// the ordinary case: the sender is not in anyone's way.
func (e *PrometheusExporter) drainUntil(deadline time.Time) {
	for {
		// The shutdown deadline also bounds a drain already running.
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

		// Drop what the endpoint would refuse before sending, since one bad sample fails the whole request.
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
		// Fail the waiters behind a refused batch now rather than letting them time out.
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
