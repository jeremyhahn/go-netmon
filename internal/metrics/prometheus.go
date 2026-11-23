package metrics

import (
	"bytes"
	"context"
	"io"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golang/snappy"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/prometheus/prompb"

	"github.com/jeremyhahn/go-netmon/internal/mib/storage"
	"github.com/jeremyhahn/go-netmon/internal/snmp"
)

// exportBufferMultiplier bounds how many batches worth of samples may
// accumulate in a PrometheusExporter's buffer while remote-write is
// failing, before the oldest samples are dropped to bound memory use.
// Without a cap, a persistent remote-write outage combined with the
// existing "don't clear the buffer on error" retry behavior grows the
// buffer forever.
const exportBufferMultiplier = 10

// minExportBufferSize floors the retry backlog so it can always hold more than
// a single poll's worth of samples. One poll of one router produces ~15,000
// samples, so a cap derived from the default 1000 batch size would be smaller
// than the unit of work it is meant to buffer, and a brief remote-write outage
// would discard whole polls rather than replay them.
const minExportBufferSize = 100000

// defaultBatchSize is the batch size an exporter built without one uses, and
// the chunk SendCatalog falls back to.
const defaultBatchSize = 1000

// exportStallMultiplier x flushInterval is the minimum time without a
// successful flush before the background flusher starts logging a loud,
// repeating stall warning, even when its buffer is empty. A stalled
// exporter that has stopped receiving samples (e.g. because an upstream
// conversion step silently produces nothing) would otherwise tick forever
// with an empty buffer and never log a single line -- this is the failure
// mode that must never be silent again.
const exportStallMultiplier = 5

// minExportStallThreshold is a floor on the stall-detection window so that
// short flush intervals in tests/small deployments don't cause spurious
// warnings on the very first tick.
const minExportStallThreshold = time.Minute

// Every value the exporter's two label sets can take, declared rather than
// spelled at each call site.
//
// They are declared because a labeled counter's child DOES NOT EXIST until
// something increments it. A fresh process publishes no
// gonetmon_export_dropped_samples_total{reason="aged"} at all, which is not the
// same as publishing zero and is worse in both directions:
//
//   - an alert cannot fire on it. rate() over a series that does not exist is
//     EMPTY, not 0, and an empty result satisfies no comparison -- so the alarm
//     on "samples are being dropped" is silent on exactly the process that has
//     not dropped any yet, and stays silent until the first drop creates the
//     series it was supposed to warn about.
//   - absence stops meaning anything. A reader who finds the series missing
//     cannot tell "this build does not have this counter" from "this counter
//     has not been touched", so a check that treats absence as a signal --
//     scripts/measure-export-health.sh does, to catch a deploy that copied a
//     binary without restarting it -- reports the wrong cause confidently.
//
// So every child is created at registration, publishing 0 from the first
// scrape. Absence then means the build does not have it, which is a fact worth
// acting on. This is the same empty-answer disguise as a query that matches
// nothing and returns success: the shape this package exists to remove.
// There is deliberately NO "dropped" outcome among the export statuses.
//
// There was, and it counted SAMPLES while its two siblings counted FLUSHES --
// one metric, one label set, two units. Every obvious question asked of it was
// therefore nonsense: sum(gonetmon_exports_total) adds flushes to samples, and
// rate(...{status="dropped"}) / rate(...{status="success"}), which is how
// anyone would ask "what fraction are we losing", divides samples by flushes
// and returns something shaped like a ratio. #69's own measurement had to carry
// a paragraph warning that its two headline figures were not comparable, which
// is the tell.
//
// A flush is not "dropped" in the first place; samples are. So the count lives
// where it means something -- gonetmon_export_dropped_samples_total{reason},
// which carries the same total, in samples, attributed to a cause. Nothing is
// lost by the removal: sum(gonetmon_export_dropped_samples_total) is exactly
// what the removed child held.
const (
	dropAged       = "aged"
	dropDepth      = "depth"
	dropRejected   = "rejected"
	dropSuperseded = "superseded"
	dropShutdown   = "shutdown"

	exportSuccess = "success"
	exportError   = "error"
)

var (
	dropReasons    = []string{dropAged, dropDepth, dropRejected, dropSuperseded, dropShutdown}
	exportOutcomes = []string{exportSuccess, exportError}
)

// dropPhrase says, in words, what each reason means. It is the ONE place that
// wording lives.
//
// It is what a DroppedError reports and what the counter's help text is built
// from, so the sentence in a log and the sentence on /metrics are the same
// sentence about the same event. They used to be two separately maintained
// strings and the log's was simply wrong: every discard, whatever its reason,
// reported "to bound queue memory", so a poll superseded by a newer one sent
// its reader looking for memory pressure that was not there.
var dropPhrase = map[string]string{
	dropAged:       "stamped further back than the age horizon",
	dropDepth:      "the queue exceeded its sample bound",
	dropRejected:   "the endpoint refused them for a reason that cannot be retried",
	dropSuperseded: "another reading already owns that point of the series",
	dropShutdown:   "the process stopped with samples still queued",
}

// droppedSamplesHelp is the help text for gonetmon_export_dropped_samples_total,
// built from dropReasons and dropPhrase so that a reason cannot be added
// without being described.
func droppedSamplesHelp() string {
	var help strings.Builder
	help.WriteString("Samples discarded before reaching remote write, by reason: ")
	for i, reason := range dropReasons {
		if i > 0 {
			help.WriteString(", ")
		}
		help.WriteString(reason)
		help.WriteString(" (")
		help.WriteString(dropPhrase[reason])
		help.WriteString(")")
	}
	help.WriteString(". Every reason publishes 0 from the first scrape, so absence means the build does not have it.")
	return help.String()
}

// Self-instrumentation for the Prometheus exporter. These are package-level
// so that every PrometheusExporter instance (including ones created
// repeatedly in tests) shares a single registration; registering per
// instance would panic on the second call with "duplicate metrics collector
// registration attempted".
var (
	exportsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gonetmon_exports_total",
			Help: "Total FLUSHES to remote write, by outcome (success, error). Samples discarded are counted in gonetmon_export_dropped_samples_total, which is in samples and so cannot be compared with this. Every outcome publishes 0 from the first scrape, so absence means the build does not have it.",
		},
		[]string{"status"},
	)
	// droppedSamples counts samples discarded before delivery, by reason.
	//
	// The aggregate alone could not tell a clock problem from a backpressure
	// problem: "too old to land" and "queue full" are different faults with
	// different fixes, and a single number conflating them is the silent,
	// unattributable loss this package exists to end.
	droppedSamples = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gonetmon_export_dropped_samples_total",
			Help: droppedSamplesHelp(),
		},
		[]string{"reason"},
	)
	// repairedOrderTotal counts samples the poller handed over out of order for
	// their series, which the encoder sorts before sending.
	//
	// Separate from outOfOrderTotal because they are different facts and the
	// difference matters operationally: this one is repaired and the request is
	// accepted, so it is a signal about the POLLER -- two overlapping polls of
	// one device -- and not about the endpoint. Counting it as out-of-order
	// raised an ERROR on traffic that was about to be delivered correctly.
	repairedOrderTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "gonetmon_export_repaired_order_total",
			Help: "Samples that arrived out of order for their series and were sorted before sending. The write succeeds; above zero means something upstream is producing a series' readings out of order, usually two overlapping polls of one device.",
		},
	)
	// outOfOrderTotal counts samples the endpoint would refuse: older than one
	// already sent for their series, or claiming an instant another reading has
	// already taken with a different value. Both are one fact operationally --
	// a reading that cannot be delivered because another owns that point of the
	// series -- and both are dropped as "superseded".
	//
	// It reports rather than prevents, and it exists because this class of
	// fault was invisible for fifteen hours: the exporter was dropping a
	// million samples an hour and every signal it published said "healthy but
	// failing to flush", which is a different problem with a different fix.
	// One counter above zero names it immediately.
	outOfOrderTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "gonetmon_export_out_of_order_total",
			Help: "Total samples remote write would refuse: older than one already sent for the same series, or a different value at an instant that series already holds. Above zero means writes are being ordered wrongly, and the endpoint would reject the whole request that carries them.",
		},
	)
	exportLastSuccessTimestamp = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "gonetmon_export_last_success_timestamp_seconds",
			Help: "Unix timestamp of the last successful metrics flush to Prometheus remote write. 0 if never succeeded.",
		},
	)
	exportBufferedSamples = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "gonetmon_export_buffered_samples",
			Help: "Number of samples currently buffered awaiting flush to Prometheus remote write.",
		},
	)
	exportConsecutiveFailures = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "gonetmon_export_consecutive_failures",
			Help: "Number of consecutive flush failures since the last successful flush.",
		},
	)
	exportStalled = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "gonetmon_export_stalled",
			Help: "1 if no successful flush has occurred within the stall threshold despite samples having been submitted for export, 0 otherwise.",
		},
	)
	metricNameUnresolvedSamples = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gonetmon_metric_name_unresolved_samples_total",
			Help: "Total number of samples whose instance OID matched no object in any loaded MIB, and which therefore fall back to a raw-OID metric name.",
		},
		[]string{"device"},
	)
	sampleLabelConflicts = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gonetmon_sample_label_conflicts_total",
			Help: "Total number of configured device tags dropped from a sample because the tag name collided with a structural label (device, oid, an index label, or an info metric's value), which the poller never lets a tag overwrite.",
		},
		[]string{"device", "label"},
	)
)

func init() {
	prometheus.MustRegister(
		exportsTotal,
		droppedSamples,
		outOfOrderTotal,
		repairedOrderTotal,
		exportLastSuccessTimestamp,
		exportBufferedSamples,
		exportConsecutiveFailures,
		exportStalled,
		metricNameUnresolvedSamples,
		sampleLabelConflicts,
	)

	publishZeroChildren(sharedCounters())
}

// publishZeroChildren creates every child of the two labeled export counters,
// so each publishes 0 before anything has happened to it. See the label-set
// declarations above for why absence must not be the resting state.
func publishZeroChildren(c *counters) {
	for _, reason := range dropReasons {
		c.dropped.WithLabelValues(reason)
	}
	for _, outcome := range exportOutcomes {
		c.exports.WithLabelValues(outcome)
	}
}

// PrometheusExporter exports metrics to Prometheus via remote write
type PrometheusExporter struct {
	remoteWriteURL string
	client         *http.Client
	batchSize      int
	flushInterval  time.Duration

	buffer        []*Sample
	bufferLock    sync.Mutex
	maxBufferSize int

	// queue holds batches waiting to be written, oldest first, and is drained
	// by one sender goroutine. Everything about ordering depends on there
	// being exactly one writer to the endpoint -- see sender.
	queueMu sync.Mutex
	queue   []*pending
	queued  int
	// inFlight is the size of the batch the sender is writing right now. It
	// has left the queue and has not been delivered, so it belongs to neither
	// count without being named.
	//
	// A batch is written as several bounded requests, so part of it may in fact
	// have landed while this still counts the whole. That over-reports the
	// backlog for the length of one send and never under-reports it, which is
	// the direction a backlog gauge has to err: reporting less than is
	// outstanding is how a stall reads as quiet.
	inFlight int
	wake     chan struct{}

	// maxSampleAge is how far behind the present a sample's own timestamp may
	// be before it is dropped as unlandable rather than retried forever. It is
	// not residence time -- see boundQueueLocked.
	maxSampleAge time.Duration

	// now is time.Now, replaced in tests that need the age horizon to bite
	// without waiting for it.
	now func() time.Time

	// shutdownAt is when the sender must stop writing, in unix nanoseconds, or
	// zero while the exporter is running. Set by Close. It bounds the LOOP.
	shutdownAt atomic.Int64

	// shutdownDropped is how many samples the bounded drain gave up on, which
	// is what Close has to tell its caller. A shutdown that discarded data did
	// not succeed.
	shutdownDropped atomic.Int64

	// sendCtx is the parent of every remote-write request, and cancelSend is
	// what Close pulls when its deadline expires. A deadline the request
	// cannot observe bounds nothing: the loop stops between batches, while the
	// request already in flight runs to its own timeout regardless.
	sendCtx    context.Context
	cancelSend context.CancelFunc

	// counts are the counters this exporter increments. See counters.
	counts *counters

	// lastSent is the newest point written for each series, which is what the
	// admission check compares against. Only the sender touches it, but Close
	// can run beside it.
	sentMu   sync.Mutex
	lastSent map[string]sentPoint

	// published is the set of metric names this exporter has sent, which is
	// what the catalog describes. It carries no device dimension, so it is
	// bounded by the number of distinct metrics rather than by the fleet.
	published *publishedMetrics

	// Table index cache for adding generic table index labels
	tableIndexCache *TableIndexCache

	// OID learner for discovering tables from SNMP responses
	oidLearner *OIDLearner

	stopChan chan struct{}
	wg       sync.WaitGroup
	closed   atomic.Bool

	// Health tracking, used to make stalls loud regardless of buffer
	// state. lastAppendUnixNano is set on every Export() call that
	// receives at least one sample; lastSuccessUnixNano is set on every
	// successful flush. Both are unix nanoseconds, 0 meaning "never".
	lastAppendUnixNano  atomic.Int64
	lastSuccessUnixNano atomic.Int64
	consecutiveFailures atomic.Int64
	stalled             atomic.Bool
	stallThreshold      time.Duration
}

// maxBufferSize returns how many samples may back up awaiting retry.
func maxBufferSize(batchSize int) int {
	size := batchSize * exportBufferMultiplier
	if size < minExportBufferSize {
		return minExportBufferSize
	}
	return size
}

// NewPrometheusExporter creates a new Prometheus exporter
func NewPrometheusExporter(remoteWriteURL string, batchSize int, flushInterval time.Duration, mibStorage *storage.Storage) (*PrometheusExporter, error) {
	if remoteWriteURL == "" {
		return nil, &ConfigError{Field: "remoteWriteURL", Reason: "cannot be empty"}
	}

	if batchSize <= 0 {
		batchSize = defaultBatchSize
	}

	if flushInterval <= 0 {
		flushInterval = 10 * time.Second
	}

	// Create table index cache and OID learner if MIB storage is provided
	var tableIndexCache *TableIndexCache
	var oidLearner *OIDLearner
	if mibStorage != nil {
		tableIndexCache = NewTableIndexCache(mibStorage)
		oidLearner = NewOIDLearner(mibStorage)
	}

	sendCtx, cancelSend := context.WithCancel(context.Background())

	stallThreshold := flushInterval * exportStallMultiplier
	if stallThreshold < minExportStallThreshold {
		stallThreshold = minExportStallThreshold
	}

	exporter := &PrometheusExporter{
		published:      newPublishedMetrics(),
		remoteWriteURL: remoteWriteURL,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		batchSize:       batchSize,
		flushInterval:   flushInterval,
		maxBufferSize:   maxBufferSize(batchSize),
		buffer:          make([]*Sample, 0, batchSize),
		tableIndexCache: tableIndexCache,
		oidLearner:      oidLearner,
		stopChan:        make(chan struct{}),
		stallThreshold:  stallThreshold,
		counts:          sharedCounters(),
		sendCtx:         sendCtx,
		cancelSend:      cancelSend,
		wake:            make(chan struct{}, 1),
		lastSent:        make(map[string]sentPoint),
		maxSampleAge:    sampleAgeHorizon(flushInterval),
		now:             time.Now,
	}

	// One sender, so the order batches reach the endpoint is the order they
	// left the buffer. Started before the flusher, which only feeds it.
	exporter.wg.Add(1)
	go exporter.sender()

	// Start background flusher
	exporter.wg.Add(1)
	go exporter.backgroundFlusher()

	return exporter, nil
}

// RefreshMIBMetadata rebuilds the metric name index and table index metadata
// from the MIB store the exporter was constructed with.
//
// The caller is whoever loaded MIB modules into that store: the exporter has no
// way to notice that the store grew, and until it is told, every object of
// every newly loaded module falls back to a raw-OID metric name.
//
// An exporter constructed without a MIB store has no MIB-derived state and
// refreshing it is a no-op rather than an error.
func (e *PrometheusExporter) RefreshMIBMetadata() error {
	if e.tableIndexCache == nil {
		return nil
	}
	return e.tableIndexCache.Refresh()
}

// Export sends metrics to Prometheus
// The context is unused: Export no longer performs the write, so there is
// nothing here for it to bound. It stays in the signature because Exporter
// requires it and because a future implementation may block again.
func (e *PrometheusExporter) Export(_ context.Context, samples []*Sample) error {
	if len(samples) == 0 {
		return nil
	}

	e.bufferLock.Lock()

	// Refused rather than queued once the sender has been told to stop.
	//
	// Export used to write, so a call after Close failed loudly of its own
	// accord. It queues now, and a queue with no sender is a place samples go
	// to be forgotten: Export returned nil while the data could never be
	// delivered. Checked under bufferLock, which Close takes as a barrier
	// after setting the flag, so a call that gets past here has finished
	// queueing before shutdown proceeds.
	if e.closed.Load() {
		e.bufferLock.Unlock()
		return &ClosedError{Samples: len(samples)}
	}

	e.lastAppendUnixNano.Store(time.Now().UnixNano())

	// Add samples to buffer.
	//
	// The buffer cap is deliberately NOT applied here. It exists to bound a
	// retry backlog while remote write is failing, and Flush enforces it when
	// it puts a failed batch back. Applying it to an arriving batch instead
	// discards fresh data on a perfectly healthy path: one poll of a single
	// router delivers ~15,000 samples, so a cap sized off the batch size would
	// throw most of that away before the first flush was even attempted.
	e.buffer = append(e.buffer, samples...)

	// Handed to the sender rather than written here. The caller is a poll
	// worker in the middle of a cycle, and writing inline put the slowest
	// thing in the system inside the poll -- as well as putting a second
	// request on the wire alongside whatever the background flusher was
	// already sending, with nothing deciding which arrived first.
	//
	// Detached and queued under ONE hold of bufferLock. Splitting them lets
	// two callers detach in one order and queue in the other, which puts the
	// older batch behind the newer one and reintroduces exactly the disorder
	// the sender exists to prevent.
	if len(e.buffer) >= e.batchSize {
		batch := e.buffer
		e.buffer = nil
		e.enqueue(&pending{samples: batch})
	}
	e.bufferLock.Unlock()

	e.publishBacklog()
	return nil
}

// ExportSNMPResults converts SNMP results to samples and exports them
func (e *PrometheusExporter) ExportSNMPResults(ctx context.Context, deviceName string, results []*snmp.Result, labels map[string]string) error {
	// Convert SNMP results to samples with generic table index labels
	samples := ConvertSNMPResultsWithTableIndexes(deviceName, results, labels, e.tableIndexCache, e.oidLearner)

	return e.Export(ctx, samples)
}

// Flush ensures all buffered metrics are sent
func (e *PrometheusExporter) Flush(ctx context.Context) error {
	// Take the pending batch under the lock, then release it before touching
	// the network. Holding the buffer lock across the remote-write request
	// makes every Export call — and therefore every poll worker that exports
	// inline — block for as long as that request takes. A remote-write
	// endpoint that accepts the connection and then stalls would wedge
	// collection entirely, which is a far worse failure than losing a batch.
	e.bufferLock.Lock()
	// Refused once closed, for the same reason Export is: there is nothing left
	// to send it.
	//
	// Close's own flush is refused too, and nothing is lost by that -- the
	// buffer is handed over by the flusher's stop branch and written by the
	// sender's final drain, which Close waits for. Exempting it needed a second
	// flag whose removal changed no observable behavior, which is how I found
	// out it was doing nothing.
	if e.closed.Load() {
		e.bufferLock.Unlock()
		return &ClosedError{}
	}
	// The batch and its barrier are queued as ONE step. Submitting them
	// separately let the sender take the batch in between, expire a sample out
	// of it, and find no barrier behind it to charge -- so the Flush that was
	// responsible for that sample returned nil having lost it. Measured at 1
	// failure in 30 under load.
	done := make(chan error, 1)
	barrier := &pending{done: done}

	if len(e.buffer) > 0 {
		batch := e.buffer
		e.buffer = nil
		e.enqueueAll(&pending{samples: batch}, barrier)
	} else {
		e.enqueueAll(barrier)
	}
	e.bufferLock.Unlock()

	// Flush keeps its synchronous contract: it returns when the batch it
	// submitted has been attempted. It does not do the writing -- the sender
	// does, in queue order -- so two callers of Flush can no longer put two
	// requests on the wire at once.
	//
	// Which metric names reached Prometheus is recorded by the sender on the
	// write that lands, not here: a batch whose write failed was never
	// published, and the catalog describing a series that never arrived would
	// offer a dashboard panel that can never have data.
	e.publishBacklog()

	// A barrier rather than the batch itself is what this waits on, because
	// Export hands whole batches over without waiting: by the time Flush is
	// called, the work it must wait for is usually not the work it just
	// submitted.
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		// Taken back out, or it would sit in the queue for the life of the
		// process: a barrier carries no samples, so nothing else would ever
		// account for it.
		e.abandon(barrier)
		return ctx.Err()
	}
}

// flushLocked flushes the buffer (must be called with lock held). Every
// exit path updates the exporter's health signals (gonetmon_exports_total,
// last-success timestamp, consecutive failure count) and logs loudly on
// failure -- a stalled or failing exporter must never be silent.
// send writes a batch to the remote-write endpoint as one or more BOUNDED
// requests, and returns how many samples were delivered before the first
// failure. It touches no shared state, so it is safe to call without the buffer
// lock held.
//
// It used to send the batch as a single request, whatever its size. batch_size
// decided when a flush was TRIGGERED and nothing at all decided how much went
// into a request, so a slow endpoint, a burst of devices finishing together, or
// a retry backlog all shipped as one protobuf -- and the backlog floor alone is
// 100,000 samples. A request too large to be accepted is then refused at the
// same size on every retry, so it can never drain and everything queues behind
// it (#63).
//
// The count returned is what makes a partial delivery survivable: the caller
// puts back batch[delivered:] and nothing else, so a prefix the endpoint
// accepted is not offered to it again on the next attempt. It is a prefix of
// the SORTED batch, which is why send reorders its argument -- see
// orderForSplitting.
func (e *PrometheusExporter) send(ctx context.Context, batch []*Sample) (int, error) {
	if len(batch) == 0 {
		return 0, nil
	}

	// A batch that fits in one request has no boundary to choose, and that is
	// the ordinary case. Taking it here keeps the path every sample travels
	// exactly as cheap as it was -- the request is built once and sent, plus
	// one Size() call -- rather than paying for a sort that would decide
	// nothing.
	if count, request := e.nextRequest(batch); count == len(batch) {
		if err := e.post(ctx, request); err != nil {
			return 0, err
		}
		return count, nil
	}

	// It does not fit, so there will be a boundary. Ordered before one is
	// chosen: see orderForSplitting -- a boundary cut in buffer order can put a
	// series' newer reading in the earlier request and its older one in the
	// later, and the later request is then refused whole.
	orderForSplitting(batch)

	delivered := 0
	for delivered < len(batch) {
		count, request := e.nextRequest(batch[delivered:])
		if err := e.post(ctx, request); err != nil {
			return delivered, err
		}
		delivered += count
	}
	return delivered, nil
}

// post writes one already-bounded request to the endpoint.
func (e *PrometheusExporter) post(ctx context.Context, writeRequest *prompb.WriteRequest) error {
	data, err := writeRequest.Marshal()
	if err != nil {
		return &MarshalError{Cause: err}
	}

	compressed := snappy.Encode(nil, data)

	req, err := http.NewRequestWithContext(ctx, "POST", e.remoteWriteURL, bytes.NewReader(compressed))
	if err != nil {
		return &RequestError{URL: e.remoteWriteURL, Cause: err}
	}

	req.Header.Set("Content-Encoding", "snappy")
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")

	resp, err := e.client.Do(req)
	if err != nil {
		return &SendError{URL: e.remoteWriteURL, Cause: err}
	}
	// Closing the body only returns the connection to the pool; the call's
	// outcome is already determined by what was read above.
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode/100 != 2 {
		return &RemoteWriteError{
			URL:        e.remoteWriteURL,
			StatusCode: resp.StatusCode,
			Body:       string(body),
		}
	}
	return nil
}

// recordSuccessLocked updates health signals after a successful flush and
// logs a loud recovery message if the exporter had previously been
// failing or stalled.
func (e *PrometheusExporter) recordSuccess(sampleCount int) {
	now := time.Now()
	e.lastSuccessUnixNano.Store(now.UnixNano())
	exportLastSuccessTimestamp.Set(float64(now.Unix()))

	failures := e.consecutiveFailures.Swap(0)
	exportConsecutiveFailures.Set(0)

	wasStalled := e.stalled.Swap(false)
	exportStalled.Set(0)

	e.counts.exports.WithLabelValues(exportSuccess).Inc()

	if failures > 0 || wasStalled {
		log.Printf("[metrics] flush recovered: successfully flushed %d samples to %s after %d consecutive failure(s)",
			sampleCount, e.remoteWriteURL, failures)
		return
	}

	log.Printf("[metrics] flushed %d samples to %s", sampleCount, e.remoteWriteURL)
}

// recordFailureLocked updates health signals after a failed flush and
// always logs the failure loudly. The buffer is deliberately left intact
// so the same samples are retried on the next flush -- see Export for the
// bound that keeps this from growing forever.
func (e *PrometheusExporter) recordFailure(sampleCount int, cause error) error {
	failures := e.consecutiveFailures.Add(1)
	exportConsecutiveFailures.Set(float64(failures))
	e.counts.exports.WithLabelValues(exportError).Inc()

	log.Printf("[metrics] ERROR: flush failed (%d consecutive failure(s), %d samples buffered for retry): %v",
		failures, sampleCount, cause)

	return cause
}

// counters are the counters one exporter increments.
//
// Held per exporter rather than reached for globally, because a test asserting
// on a process-global counter shares its measurement with every other test in
// the package: a still-live sender from an earlier test lands an increment
// inside a later test's before/after window, and that test then fails for a
// reason unrelated to what it checks. Production uses one shared set, which is
// what /metrics must expose; a test can hold its own.
type counters struct {
	exports    *prometheus.CounterVec
	dropped    *prometheus.CounterVec
	outOfOrder prometheus.Counter
	repaired   prometheus.Counter
}

// sharedCounters are the process-wide counters, the ones /metrics publishes.
func sharedCounters() *counters {
	return &counters{
		exports:    exportsTotal,
		dropped:    droppedSamples,
		outOfOrder: outOfOrderTotal,
		repaired:   repairedOrderTotal,
	}
}

// samplesToTimeSeries converts samples to Prometheus TimeSeries
func (e *PrometheusExporter) samplesToTimeSeries(samples []*Sample) []prompb.TimeSeries {
	// Group samples by metric name and label set
	type key struct {
		name   string
		labels string // serialized labels for uniqueness
	}

	grouped := make(map[key]*prompb.TimeSeries)

	for _, sample := range samples {
		// Create label set with __name__ first
		labels := make([]prompb.Label, 0, len(sample.Labels)+1)
		labels = append(labels, prompb.Label{
			Name:  "__name__",
			Value: sample.Name,
		})

		// Sort label names to ensure lexicographical order (required by Prometheus remote write spec)
		labelNames := make([]string, 0, len(sample.Labels))
		for k := range sample.Labels {
			labelNames = append(labelNames, k)
		}
		sort.Strings(labelNames)

		for _, k := range labelNames {
			labels = append(labels, prompb.Label{
				Name:  k,
				Value: sample.Labels[k],
			})
		}
		labelKey := seriesKey(sample)

		// Create or get TimeSeries
		k := key{name: sample.Name, labels: labelKey}
		ts, exists := grouped[k]
		if !exists {
			ts = &prompb.TimeSeries{
				Labels: labels,
			}
			grouped[k] = ts
		}

		// Add sample
		ts.Samples = append(ts.Samples, prompb.Sample{
			Value:     sample.Value,
			Timestamp: sample.Timestamp.UnixMilli(),
		})
	}

	// Convert map to slice
	result := make([]prompb.TimeSeries, 0, len(grouped))
	for _, ts := range grouped {
		ts.Samples = orderedSamples(ts.Samples)
		result = append(result, *ts)
	}

	return result
}

// sentPoint is the newest sample the endpoint has accepted for a series.
//
// The value is kept as well as the timestamp because Prometheus distinguishes
// the two ways a sample can be unacceptable: older than the head is "out of
// order", and equal to the head with a DIFFERENT value is "duplicate sample for
// timestamp". Equal timestamp AND equal value is accepted -- it is the same
// reading offered twice -- so refusing on the timestamp alone would discard
// samples the endpoint would have taken.
type sentPoint struct {
	at    int64
	value float64
}

// sameValue reports whether two readings are the same one, using the identity
// the ENDPOINT uses.
//
// Prometheus compares math.Float64bits, not the float. Go's == disagrees with
// that in both directions, and each disagreement is a defect this package
// exists to prevent: +0 and -0 are == but have different bits, so a pair of
// them would be judged the same reading here, collapsed, and Prometheus would
// have refused the request as a duplicate -- one reading gone with the flush
// reported successful. Two NaNs with identical bits are != but are the same
// reading to Prometheus, so they would be charged as a discard that never
// needed to happen.
//
// The rule is not "compare floats sensibly". It is "answer the question the
// endpoint will answer", and the only way to be sure of that is to ask it the
// same way.
func sameValue(a, b float64) bool {
	return math.Float64bits(a) == math.Float64bits(b)
}

// seriesKey identifies one series: its name and every label it carries.
//
// Length-prefixed rather than delimited. SNMP label values are device-supplied
// -- an ifAlias or entPhysicalName is whatever someone typed into the box -- so
// a comma or an equals sign in one of them makes a delimited key ambiguous, and
// two different series can produce the same string. Two series sharing a key
// are merged by the encoder, which interleaves their readings into one and puts
// them out of order, and a request whose samples descend is rejected whole
// (#69).
//
// One function, because the ordering check and the encoder must agree on what
// "the same series" means or the check reports on something the request does
// not contain.
func seriesKey(sample *Sample) string {
	names := make([]string, 0, len(sample.Labels))
	for name := range sample.Labels {
		names = append(names, name)
	}
	sort.Strings(names)

	var key strings.Builder
	key.WriteString(strconv.Itoa(len(sample.Name)))
	key.WriteByte(':')
	key.WriteString(sample.Name)
	for _, name := range names {
		value := sample.Labels[name]
		key.WriteString(strconv.Itoa(len(name)))
		key.WriteByte(':')
		key.WriteString(name)
		key.WriteString(strconv.Itoa(len(value)))
		key.WriteByte(':')
		key.WriteString(value)
	}
	return key.String()
}

// orderedSamples puts one series' samples in the order remote write requires.
//
// They arrive in BUFFER order, which is the order polls COMPLETED. That is not
// the order their varbinds were stamped: a timestamp is taken per result as it
// arrives (snmp.Client), while samples reach the buffer in whole-poll batches
// at the end of a poll, so a poll that starts earlier and finishes later
// appends older samples after a newer poll's. Two polls of one device overlap
// whenever a device is reconfigured, because replacing a worker does not wait
// for the poll already running.
//
// The remote-write spec requires the samples of a series to ascend, and
// Prometheus rejects the WHOLE request on the first that does not -- 400 "out
// of order sample". One such pair cost monitor-01 a million samples an hour for
// fifteen hours, because a rejected batch is re-queued and can never land once
// newer samples for its series have been accepted (go-netmon#69).
//
// This orders one REQUEST. It does not order two, and Flush is not serialized:
// a poll worker that fills the buffer flushes inline while the background
// flusher may already be sending, so a batch taken later can reach Prometheus
// first and leave the earlier one out of order against what was just accepted.
// That is the same permanent backlog by another route, and it is filed on #69
// rather than fixed here -- serializing the send costs poll latency, which is a
// trade rather than a correction.
//
// Samples that are IDENTICAL -- same timestamp and same value -- are collapsed
// to one. That is lossless, and it is what Prometheus does with them anyway:
// the same reading offered twice is accepted and stored once.
//
// Two DIFFERENT values at one instant are left alone, deliberately. They are as
// invalid as two out of order -- Prometheus answers "duplicate sample for
// timestamp" -- but keeping one and discarding the other is a loss, and this
// function has no way to name it: it returns samples, not counts, and its
// caller reports the batch as fully sent. partition takes such a pair out of
// the batch before the request is built and charges the discard to whoever is
// owed one. If a pair ever reached here regardless, the endpoint refuses the
// request and the batch is discarded as "rejected" -- more samples lost, but
// lost visibly, which is the failure that can be found and fixed.
func orderedSamples(samples []prompb.Sample) []prompb.Sample {
	if len(samples) < 2 {
		return samples
	}

	sort.SliceStable(samples, func(i, j int) bool {
		return samples[i].Timestamp < samples[j].Timestamp
	})

	ordered := samples[:1]
	for _, sample := range samples[1:] {
		last := ordered[len(ordered)-1]
		if sample.Timestamp == last.Timestamp && sameValue(sample.Value, last.Value) {
			continue
		}
		ordered = append(ordered, sample)
	}
	return ordered
}

// backgroundFlusher periodically flushes the buffer. It never exits on
// error and never exits on a flush that finds an empty buffer -- the loop
// only ever ends via stopChan (closed by Close). On every tick it also
// runs the stall watchdog, which is what makes a stall loud even when the
// buffer is empty (e.g. because whatever normally fills it has itself
// stopped producing samples) -- flushLocked alone cannot detect that case,
// since it has nothing to flush and nothing to report.
func (e *PrometheusExporter) backgroundFlusher() {
	defer e.wg.Done()

	ticker := time.NewTicker(e.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// Handed over without waiting. The tick exists to move what is
			// buffered on to the sender, not to write it: waiting here would
			// make the flusher and the sender take turns, and a slow endpoint
			// would stop the buffer being drained at all.
			e.submit()
			e.checkStall()
		case <-e.stopChan:
			// The sender does the final write; this only makes sure the last
			// partial batch is in the queue before it does.
			e.submit()
			return
		}
	}
}

// checkStall logs a loud, repeating warning every time it is called while
// the exporter has received samples but has not managed a successful
// flush within stallThreshold. It is the safety net for the exact failure
// mode this package exists to prevent: a pipeline that keeps accepting
// samples but silently stops delivering them.
func (e *PrometheusExporter) checkStall() {
	lastAppend := e.lastAppendUnixNano.Load()
	if lastAppend == 0 {
		// Nothing has ever been submitted for export; nothing to stall.
		return
	}

	lastSuccess := e.lastSuccessUnixNano.Load()
	since := time.Since(time.Unix(0, lastAppend))
	if lastSuccess != 0 {
		since = time.Since(time.Unix(0, lastSuccess))
	}

	if since < e.stallThreshold {
		e.stalled.Store(false)
		exportStalled.Set(0)
		return
	}

	e.stalled.Store(true)
	exportStalled.Set(1)

	e.bufferLock.Lock()
	bufferedNow := len(e.buffer)
	e.bufferLock.Unlock()

	log.Printf("[metrics] ERROR: metrics export appears STALLED -- no successful flush to %s in %s (buffered samples: %d, consecutive failures: %d). Samples are still being submitted for export; investigate the exporter and remote-write endpoint.",
		e.remoteWriteURL, since.Round(time.Second), bufferedNow, e.consecutiveFailures.Load())
}

// Close closes the exporter and releases resources
func (e *PrometheusExporter) Close() error {
	// Use atomic CAS to ensure we only close once
	if !e.closed.CompareAndSwap(false, true) {
		// Already closed
		return nil
	}

	// No more samples from here on. Taken and released under bufferLock, which
	// Export checks the flag under, so any Export already inside has finished
	// queueing before the sender is told to stop -- and none can start after.
	e.bufferLock.Lock()
	e.bufferLock.Unlock() //nolint:gocritic,staticcheck // a barrier, not a critical section

	// Hand the last partial buffer to the sender, then bound what it does with
	// it.
	//
	// Close used to call Flush here, which needed an exemption from the
	// admission check above and a flag to carry it. Submitting directly is
	// what that was really for: it moves the buffer into the queue BEFORE the
	// sender is told to stop, so the final drain is guaranteed to see it.
	//
	// Leaving it to the flusher's own stop branch is not enough and was a race
	// -- the sender can reach its final drain, find the queue empty, and
	// finish before the flusher submits, and the last partial batch is lost.
	// It passed most of the time, which is why a mutation of the exemption
	// looked harmless.
	e.submit()
	e.shutdownAt.Store(time.Now().Add(shutdownBudget).UnixNano())

	close(e.stopChan)

	// Then wait for the sender, and cancel the request it is inside if it does
	// not finish in time.
	//
	// Stopping the loop is not enough. The request already in flight carries
	// its own sendTimeout and cannot see a deadline recorded after it started,
	// so a slow endpoint held shutdown for the full 30 seconds however short
	// the budget was. Canceling the context it was built from is the only
	// bound it can observe.
	stopped := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(shutdownBudget):
		log.Printf("[metrics] shutdown budget of %s expired with a remote write to %s still in flight; canceling it",
			shutdownBudget, e.remoteWriteURL)
		e.cancelSend()
		<-stopped
	}

	e.cancelSend()

	// A shutdown that threw telemetry away did not succeed, and the caller is
	// the only one who can act on that -- by not exiting quite yet, or by
	// saying so in its own logs. Returning nil here was the third place in this
	// package where discarding data reported success.
	if dropped := e.shutdownDropped.Load(); dropped > 0 {
		return &DroppedError{Samples: int(dropped)}
	}
	return nil
}

// LastSuccessfulFlush returns the time of the most recent successful flush
// to the remote write endpoint, and false if no flush has ever succeeded.
func (e *PrometheusExporter) LastSuccessfulFlush() (time.Time, bool) {
	nanos := e.lastSuccessUnixNano.Load()
	if nanos == 0 {
		return time.Time{}, false
	}
	return time.Unix(0, nanos), true
}

// BufferedSamples returns the number of samples currently buffered and
// awaiting flush.
// Both halves are counted: samples still accumulating towards a batch, and
// batches already handed to the sender and awaiting a write. A caller asking
// how much is undelivered means both, and counting only the first would report
// zero while a whole backlog waited behind a failing endpoint.
// It is e.backlog, which is the one definition of the quantity.
func (e *PrometheusExporter) BufferedSamples() int {
	return e.backlog()
}

// backlog is how many samples have been accepted and not yet delivered.
//
// One meaning, one accessor. Undelivered samples live in three places -- still
// accumulating towards a batch, queued for the sender, and the one batch being
// written -- and this has now been wrong three times, in three different ways,
// because each place had its own writer reporting its own slice as if it were
// the total. First the batch on the wire was left out, so the count read zero
// during a stall; then a refactor added a third home and the two writers each
// kept reporting their own.
//
// A fourth place cannot silently reintroduce that. If one is added and not
// counted here it is simply missing, which is a visible bug rather than a
// plausible number.
//
// Never call this while holding either lock. The order is bufferLock then
// queueMu, which is the order everything else takes them in.
func (e *PrometheusExporter) backlog() int {
	e.bufferLock.Lock()
	buffered := len(e.buffer)
	e.bufferLock.Unlock()

	e.queueMu.Lock()
	pending := e.queued + e.inFlight
	e.queueMu.Unlock()

	return buffered + pending
}

// publishBacklog is the only writer of gonetmon_export_buffered_samples.
//
// The gauge and the accessor were separately maintained and disagreed: one
// counted the buffer, the other the queue, and an operator watching the gauge
// saw whichever slice the last writer happened to own.
func (e *PrometheusExporter) publishBacklog() {
	exportBufferedSamples.Set(float64(e.backlog()))
}

// Stalled reports whether the exporter has received samples but has not
// completed a successful flush within its stall threshold.
func (e *PrometheusExporter) Stalled() bool {
	return e.stalled.Load()
}

// GetOIDLearner returns the OID learner for accessing learned table information
func (e *PrometheusExporter) GetOIDLearner() *OIDLearner {
	return e.oidLearner
}

// publishedMetricNames returns the set of metric names this exporter has sent.
//
// It is what a CatalogEmitter describes. It is a method rather than a field
// read so that the emitter stays a separate component: the catalog is written
// by one designated process, and that process may not own any particular
// exporter forever.
func (e *PrometheusExporter) publishedMetricNames() *publishedMetrics {
	return e.published
}

// TableIndexCache returns the MIB-derived index this exporter names and
// describes samples with. It is what a CatalogEmitter reads descriptions from.
func (e *PrometheusExporter) TableIndexCache() *TableIndexCache {
	return e.tableIndexCache
}

// ForgetDevice drops the per-device state the exporter keeps for naming a
// device's samples: its friendly-name cache entries (#54). It implements
// DeviceForgetter.
func (e *PrometheusExporter) ForgetDevice(deviceName string) {
	if e.tableIndexCache != nil {
		e.tableIndexCache.ForgetDevice(deviceName)
	}
}

// SendCatalog delivers the metric catalog without touching the sample buffer.
//
// The catalog does not share a batch with device telemetry, and that is the
// point rather than an optimization. Sharing one buffer means a rejected
// catalog write puts device samples back with it, delaying or -- once the
// retry backlog hits its cap -- discarding measurements. The catalog is a
// description; it must never be able to cost a reading.
//
// It also removes the catalog from the buffer's size accounting entirely. One
// emission is around 20,000 series, which is more than a full batch on any
// sane batch_size, so routing it through the buffer forced an immediate flush
// of whatever telemetry happened to be queued at that instant and sent the
// whole lot as a single request.
//
// Delivery is synchronous and the error is the delivery's: the caller learns
// whether Prometheus took it, not whether it was queued. Chunks are sent in
// order and the first failure stops the rest, because a half-written catalog
// describing an arbitrary subset of metrics is worse than one that is a
// refresh behind.
//
// Chunking is no longer this function's business. It used to cut the catalog
// into batch_size pieces itself, because one emission is around 20,000 series
// and nothing else bounded a request -- and it deliberately did not attempt a
// byte bound, on the grounds that bounding a request by its encoded size
// belongs to every send rather than to one caller. It does now (#63), so this
// hands the whole catalog to send and gets both bounds for free.
func (e *PrometheusExporter) SendCatalog(ctx context.Context, samples []*Sample) error {
	_, err := e.send(ctx, samples)
	return err
}
