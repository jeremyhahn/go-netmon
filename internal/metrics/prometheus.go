package metrics

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
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

// Self-instrumentation for the Prometheus exporter. These are package-level
// so that every PrometheusExporter instance (including ones created
// repeatedly in tests) shares a single registration; registering per
// instance would panic on the second call with "duplicate metrics collector
// registration attempted".
var (
	exportsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gonetmon_exports_total",
			Help: "Total number of metric export outcomes, by status (success, error, dropped).",
		},
		[]string{"status"},
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
		exportLastSuccessTimestamp,
		exportBufferedSamples,
		exportConsecutiveFailures,
		exportStalled,
		metricNameUnresolvedSamples,
		sampleLabelConflicts,
	)
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
		batchSize = 1000
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

	stallThreshold := flushInterval * exportStallMultiplier
	if stallThreshold < minExportStallThreshold {
		stallThreshold = minExportStallThreshold
	}

	exporter := &PrometheusExporter{
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
	}

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
func (e *PrometheusExporter) Export(ctx context.Context, samples []*Sample) error {
	if len(samples) == 0 {
		return nil
	}

	e.lastAppendUnixNano.Store(time.Now().UnixNano())

	e.bufferLock.Lock()

	// Add samples to buffer.
	//
	// The buffer cap is deliberately NOT applied here. It exists to bound a
	// retry backlog while remote write is failing, and Flush enforces it when
	// it puts a failed batch back. Applying it to an arriving batch instead
	// discards fresh data on a perfectly healthy path: one poll of a single
	// router delivers ~15,000 samples, so a cap sized off the batch size would
	// throw most of that away before the first flush was even attempted.
	e.buffer = append(e.buffer, samples...)
	exportBufferedSamples.Set(float64(len(e.buffer)))

	full := len(e.buffer) >= e.batchSize
	e.bufferLock.Unlock()

	// Flush outside the lock so a slow remote write never blocks the caller,
	// which for the poller is a worker in the middle of a poll cycle.
	if full {
		return e.Flush(ctx)
	}
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
	if len(e.buffer) == 0 {
		e.bufferLock.Unlock()
		return nil
	}
	batch := e.buffer
	e.buffer = nil
	exportBufferedSamples.Set(0)
	e.bufferLock.Unlock()

	if err := e.send(ctx, batch); err != nil {
		// Put the batch back so the next flush retries it, ahead of anything
		// buffered while the request was in flight.
		e.bufferLock.Lock()
		e.buffer = append(batch, e.buffer...)
		e.enforceBufferCapLocked()
		exportBufferedSamples.Set(float64(len(e.buffer)))
		e.bufferLock.Unlock()

		return e.recordFailure(len(batch), err)
	}

	e.recordSuccess(len(batch))
	return nil
}

// flushLocked flushes the buffer (must be called with lock held). Every
// exit path updates the exporter's health signals (gonetmon_exports_total,
// last-success timestamp, consecutive failure count) and logs loudly on
// failure -- a stalled or failing exporter must never be silent.
// send writes one batch to the remote-write endpoint. It touches no shared
// state, so it is safe to call without the buffer lock held.
func (e *PrometheusExporter) send(ctx context.Context, batch []*Sample) error {
	writeRequest := &prompb.WriteRequest{
		Timeseries: e.samplesToTimeSeries(batch),
	}

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

// enforceBufferCapLocked drops the oldest samples once the buffer exceeds its
// cap, loudly. The caller must hold bufferLock.
func (e *PrometheusExporter) enforceBufferCapLocked() {
	if len(e.buffer) <= e.maxBufferSize {
		return
	}

	dropped := len(e.buffer) - e.maxBufferSize
	log.Printf("[metrics] ERROR: export buffer exceeded %d samples (cap enforced at %dx batch size); dropping %d oldest buffered samples to bound memory use -- remote write at %s has been failing",
		e.maxBufferSize, exportBufferMultiplier, dropped, e.remoteWriteURL)
	e.buffer = e.buffer[dropped:]
	exportsTotal.WithLabelValues("dropped").Add(float64(dropped))
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

	exportsTotal.WithLabelValues("success").Inc()

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
	exportsTotal.WithLabelValues("error").Inc()

	log.Printf("[metrics] ERROR: flush failed (%d consecutive failure(s), %d samples buffered for retry): %v",
		failures, sampleCount, cause)

	return cause
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

		labelKey := sample.Name
		for _, k := range labelNames {
			v := sample.Labels[k]
			labels = append(labels, prompb.Label{
				Name:  k,
				Value: v,
			})
			labelKey += fmt.Sprintf(",%s=%s", k, v)
		}

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
		result = append(result, *ts)
	}

	return result
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
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			// flushLocked (via Flush) already logs loudly and updates
			// health metrics on failure; nothing further to do with the
			// error here.
			_ = e.Flush(ctx)
			cancel()
			e.checkStall()
		case <-e.stopChan:
			// Final flush before stopping
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_ = e.Flush(ctx)
			cancel()
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

	// Flush any remaining metrics before closing
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	_ = e.Flush(ctx)
	cancel()

	close(e.stopChan)
	e.wg.Wait()
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
func (e *PrometheusExporter) BufferedSamples() int {
	e.bufferLock.Lock()
	defer e.bufferLock.Unlock()
	return len(e.buffer)
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
