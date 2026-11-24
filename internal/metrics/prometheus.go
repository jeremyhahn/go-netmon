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

// exportBufferMultiplier bounds buffered batches while remote write fails; the oldest samples are dropped beyond it.
const exportBufferMultiplier = 10

// minExportBufferSize floors the retry backlog above one poll's worth of samples (~15,000 for one router).
const minExportBufferSize = 100000

// defaultBatchSize is the batch size an exporter built without one uses, and
// the chunk SendCatalog falls back to.
const defaultBatchSize = 1000

// exportStallMultiplier x flushInterval without a successful flush starts a repeating stall warning, even with an empty buffer.
const exportStallMultiplier = 5

// minExportStallThreshold is a floor on the stall-detection window so that
// short flush intervals in tests/small deployments don't cause spurious
// warnings on the very first tick.
const minExportStallThreshold = time.Minute

// Every label value of the exporter's counters, pre-created at registration so each publishes 0 from the first scrape; drops are counted in samples by reason.
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

// dropPhrase is the single wording of each drop reason, shared by DroppedError and the counter help.
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

// Exporter self-instrumentation, registered once per process.
var (
	exportsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gonetmon_exports_total",
			Help: "Total FLUSHES to remote write, by outcome (success, error). Samples discarded are counted in gonetmon_export_dropped_samples_total, which is in samples and so cannot be compared with this. Every outcome publishes 0 from the first scrape, so absence means the build does not have it.",
		},
		[]string{"status"},
	)
	// droppedSamples counts samples discarded before delivery, by reason.
	droppedSamples = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gonetmon_export_dropped_samples_total",
			Help: droppedSamplesHelp(),
		},
		[]string{"reason"},
	)
	// repairedOrderTotal counts out-of-order samples the encoder sorted before sending (overlapping polls), not endpoint refusals.
	repairedOrderTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "gonetmon_export_repaired_order_total",
			Help: "Samples that arrived out of order for their series and were sorted before sending. The write succeeds; above zero means something upstream is producing a series' readings out of order, usually two overlapping polls of one device.",
		},
	)
	// outOfOrderTotal counts samples the endpoint would refuse as older than, or conflicting with, one already sent; dropped as superseded.
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
	// inFlight is the size of the batch being written; it may over-report the backlog briefly, never under-report.
	inFlight int
	wake     chan struct{}

	// maxSampleAge is how far behind the present a sample's own timestamp may
	// be before it is dropped as unlandable rather than retried forever. It is
	// not residence time -- see boundQueueLocked.
	maxSampleAge time.Duration

	// now is time.Now, replaced in tests that need the age horizon to bite
	// without waiting for it.
	now func() time.Time

	// shutdownAt is when the sender must stop writing, in unix nanoseconds, or zero while running; set by Close.
	shutdownAt atomic.Int64

	// shutdownDropped is how many samples the bounded drain gave up on, which
	// is what Close has to tell its caller. A shutdown that discarded data did
	// not succeed.
	shutdownDropped atomic.Int64

	// sendCtx parents every remote-write request; Close calls cancelSend when its deadline expires.
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

	// Health tracking in unix nanoseconds (0 = never): last Export with samples, last successful flush.
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

// Export queues metrics for sending to Prometheus.
// The context is unused: Export does not perform the write. It stays in the
// signature because Exporter requires it.
func (e *PrometheusExporter) Export(_ context.Context, samples []*Sample) error {
	if len(samples) == 0 {
		return nil
	}

	e.bufferLock.Lock()

	// Refused once the sender has been told to stop; checked under bufferLock, which Close uses as a barrier.
	if e.closed.Load() {
		e.bufferLock.Unlock()
		return &ClosedError{Samples: len(samples)}
	}

	e.lastAppendUnixNano.Store(time.Now().UnixNano())

	// The buffer cap is enforced by Flush on retried batches, not on arriving samples.
	e.buffer = append(e.buffer, samples...)

	// Detach and queue under one hold of bufferLock so batches reach the sender in order.
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
	// Take the pending batch under the lock and release it before any network I/O.
	e.bufferLock.Lock()
	// Refused once closed; Close's final buffer reaches the sender through its own drain.
	if e.closed.Load() {
		e.bufferLock.Unlock()
		return &ClosedError{}
	}
	// Queue the batch and its barrier as one step, so an expired sample is always charged to its Flush.
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

	// Flush returns once its batch has been attempted; the sender writes in queue order and records published names on success.
	e.publishBacklog()

	// Wait on a barrier, which completes when everything queued ahead of it has been attempted.
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

// send writes a batch as one or more bounded requests and returns how many samples were delivered before the first failure; it sorts batch (see orderForSplitting).
func (e *PrometheusExporter) send(ctx context.Context, batch []*Sample) (int, error) {
	if len(batch) == 0 {
		return 0, nil
	}

	// A batch that fits in one request is sent as is.
	if count, request := e.nextRequest(batch); count == len(batch) {
		if err := e.post(ctx, request); err != nil {
			return 0, err
		}
		return count, nil
	}

	// Order before choosing boundaries; see orderForSplitting.
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

// recordSuccessLocked updates health signals after a successful flush and logs recovery from a failure or stall.
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

// recordFailureLocked updates health signals after a failed flush and logs it; the buffer is kept for retry.
func (e *PrometheusExporter) recordFailure(sampleCount int, cause error) error {
	failures := e.consecutiveFailures.Add(1)
	exportConsecutiveFailures.Set(float64(failures))
	e.counts.exports.WithLabelValues(exportError).Inc()

	log.Printf("[metrics] ERROR: flush failed (%d consecutive failure(s), %d samples buffered for retry): %v",
		failures, sampleCount, cause)

	return cause
}

// counters are the counters one exporter increments; tests may hold their own set.
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

// sentPoint is the newest accepted sample of a series; equal timestamp with a different value is a duplicate, same value is accepted.
type sentPoint struct {
	at    int64
	value float64
}

// sameValue compares math.Float64bits, as Prometheus does.
func sameValue(a, b float64) bool {
	return math.Float64bits(a) == math.Float64bits(b)
}

// seriesKey identifies a series by name and labels, length-prefixed because label values are device-supplied.
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

// orderedSamples sorts one series' samples ascending and collapses identical ones; differing values at one instant are left for partition.
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

// backgroundFlusher flushes on every tick until stopChan closes, and runs the stall watchdog.
func (e *PrometheusExporter) backgroundFlusher() {
	defer e.wg.Done()

	ticker := time.NewTicker(e.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// Hand over without waiting, so a slow endpoint never stops the buffer draining.
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

// checkStall logs a repeating warning while samples arrive but no flush has succeeded within stallThreshold.
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

	// Submit the last partial buffer before the sender is told to stop, so the final drain sees it.
	e.submit()
	e.shutdownAt.Store(time.Now().Add(shutdownBudget).UnixNano())

	close(e.stopChan)

	// Wait for the sender and cancel its in-flight request if the deadline passes.
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

	// Report discarded telemetry to the caller rather than nil.
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

// backlog counts accepted, undelivered samples: buffered, queued and in flight; never call it holding either lock.
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
func (e *PrometheusExporter) publishedMetricNames() *publishedMetrics {
	return e.published
}

// TableIndexCache returns the MIB-derived index this exporter names and
// describes samples with. It is what a CatalogEmitter reads descriptions from.
func (e *PrometheusExporter) TableIndexCache() *TableIndexCache {
	return e.tableIndexCache
}

// ForgetDevice drops the per-device state the exporter keeps for naming a
// device's samples: its friendly-name cache entries. It implements
// DeviceForgetter.
func (e *PrometheusExporter) ForgetDevice(deviceName string) {
	if e.tableIndexCache != nil {
		e.tableIndexCache.ForgetDevice(deviceName)
	}
}

// SendCatalog delivers the metric catalog without touching the sample buffer.
//
// The catalog never shares a batch with device telemetry, so a rejected catalog
// write can never delay or discard measurements, and it stays out of the
// buffer's size accounting.
//
// Delivery is synchronous and the error is the delivery's: the caller learns
// whether Prometheus took it, not whether it was queued. Chunks are sent in
// order and the first failure stops the rest, because a half-written catalog
// is worse than one that is a refresh behind. send bounds each request by
// series count and encoded size.
func (e *PrometheusExporter) SendCatalog(ctx context.Context, samples []*Sample) error {
	_, err := e.send(ctx, samples)
	return err
}
