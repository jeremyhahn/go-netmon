package metrics

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang/snappy"
	"github.com/gosnmp/gosnmp"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/prometheus/prometheus/prompb"

	"github.com/jeremyhahn/go-netmon/internal/snmp"
)

// syncBuffer is a bytes.Buffer that may be written and read concurrently.
//
// bytes.Buffer is not safe for concurrent use, and the standard logger here is
// written by whatever goroutine happens to log. A test asserting on log output
// reads the buffer from the test goroutine while the component under test logs
// from its own -- PrometheusExporter's backgroundFlusher calls log.Printf from
// recordFailure on every failed flush, and it is still running when the test
// makes its assertions.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLogs temporarily redirects the standard logger to a buffer so
// tests can assert on what an operator would actually see, and restores it
// on return.
//
// The buffer is synchronized. Tests in this package do run sequentially, and
// that was the stated reason a plain bytes.Buffer was thought safe here, but
// sequential tests do not imply sequential goroutines: the exporter a test
// constructs starts a background flusher that keeps logging while that same
// test reads the buffer. Nothing about another test starting or not starting
// affects it.
func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prevOutput := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(buf)
	t.Cleanup(func() {
		log.SetOutput(prevOutput)
		log.SetFlags(prevFlags)
	})
	return buf
}

func TestNewPrometheusExporter(t *testing.T) {
	exporter, err := NewPrometheusExporter("http://localhost:9090/api/v1/write", 1000, 10*time.Second, nil)
	if err != nil {
		t.Fatalf("Failed to create exporter: %v", err)
	}
	defer exporter.Close()

	if exporter.remoteWriteURL != "http://localhost:9090/api/v1/write" {
		t.Errorf("Expected remote write URL 'http://localhost:9090/api/v1/write', got %s", exporter.remoteWriteURL)
	}

	if exporter.batchSize != 1000 {
		t.Errorf("Expected batch size 1000, got %d", exporter.batchSize)
	}

	if exporter.flushInterval != 10*time.Second {
		t.Errorf("Expected flush interval 10s, got %v", exporter.flushInterval)
	}
}

func TestNewPrometheusExporter_EmptyURL(t *testing.T) {
	_, err := NewPrometheusExporter("", 1000, 10*time.Second, nil)
	if err == nil {
		t.Error("Expected error for empty URL")
	}
}

func TestNewPrometheusExporter_Defaults(t *testing.T) {
	exporter, err := NewPrometheusExporter("http://localhost:9090/api/v1/write", 0, 0, nil)
	if err != nil {
		t.Fatalf("Failed to create exporter: %v", err)
	}
	defer exporter.Close()

	if exporter.batchSize != 1000 {
		t.Errorf("Expected default batch size 1000, got %d", exporter.batchSize)
	}

	if exporter.flushInterval != 10*time.Second {
		t.Errorf("Expected default flush interval 10s, got %v", exporter.flushInterval)
	}
}

func TestPrometheusExporter_Export(t *testing.T) {
	// Create test server
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)

		// Verify headers
		if r.Header.Get("Content-Encoding") != "snappy" {
			t.Errorf("Expected Content-Encoding: snappy, got %s", r.Header.Get("Content-Encoding"))
		}

		if r.Header.Get("Content-Type") != "application/x-protobuf" {
			t.Errorf("Expected Content-Type: application/x-protobuf, got %s", r.Header.Get("Content-Type"))
		}

		// Read and decompress body
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("Failed to read body: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		decompressed, err := snappy.Decode(nil, body)
		if err != nil {
			t.Errorf("Failed to decompress body: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		// Unmarshal WriteRequest
		var writeRequest prompb.WriteRequest
		if err := writeRequest.Unmarshal(decompressed); err != nil {
			t.Errorf("Failed to unmarshal WriteRequest: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		// Verify we got timeseries
		if len(writeRequest.Timeseries) == 0 {
			t.Error("Expected at least one timeseries")
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	exporter, err := NewPrometheusExporter(server.URL, 5, 100*time.Millisecond, nil)
	if err != nil {
		t.Fatalf("Failed to create exporter: %v", err)
	}
	defer exporter.Close()

	// Create test samples
	samples := []*Sample{
		{
			Name:      "test_metric_1",
			Labels:    map[string]string{"device": "router1"},
			Value:     123.45,
			Timestamp: time.Now(),
		},
		{
			Name:      "test_metric_2",
			Labels:    map[string]string{"device": "router1"},
			Value:     678.90,
			Timestamp: time.Now(),
		},
	}

	ctx := context.Background()
	if err := exporter.Export(ctx, samples); err != nil {
		t.Errorf("Failed to export samples: %v", err)
	}

	// Wait for flush
	time.Sleep(200 * time.Millisecond)

	if requestCount.Load() == 0 {
		t.Error("Expected at least one request to be sent")
	}
}

func TestPrometheusExporter_ExportSNMPResults(t *testing.T) {
	// Create test server
	var requestReceived atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestReceived.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	exporter, err := NewPrometheusExporter(server.URL, 2, 50*time.Millisecond, nil)
	if err != nil {
		t.Fatalf("Failed to create exporter: %v", err)
	}
	defer exporter.Close()

	// Create SNMP results
	results := []*snmp.Result{
		{
			OID:       "1.3.6.1.2.1.1.3.0",
			Type:      gosnmp.TimeTicks,
			Value:     uint(123456),
			Timestamp: time.Now(),
		},
		{
			OID:       "1.3.6.1.2.1.2.2.1.10.1",
			Type:      gosnmp.Counter32,
			Value:     uint(999999),
			Timestamp: time.Now(),
		},
	}

	ctx := context.Background()
	err = exporter.ExportSNMPResults(ctx, "test-device", results, map[string]string{"location": "dc1"})
	if err != nil {
		t.Errorf("Failed to export SNMP results: %v", err)
	}

	// Wait for flush
	time.Sleep(100 * time.Millisecond)

	if !requestReceived.Load() {
		t.Error("Expected request to be sent")
	}
}

func TestPrometheusExporter_Batching(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	exporter, err := NewPrometheusExporter(server.URL, 5, 1*time.Second, nil)
	if err != nil {
		t.Fatalf("Failed to create exporter: %v", err)
	}
	defer exporter.Close()

	ctx := context.Background()

	// Send 3 samples (should not flush, batch size is 5)
	for i := 0; i < 3; i++ {
		samples := []*Sample{
			{
				Name:      "test_metric",
				Labels:    map[string]string{"device": "router1"},
				Value:     float64(i),
				Timestamp: time.Now(),
			},
		}
		if err := exporter.Export(ctx, samples); err != nil {
			t.Errorf("Failed to export samples: %v", err)
		}
	}

	// Should not have flushed yet
	time.Sleep(100 * time.Millisecond)
	if requestCount.Load() != 0 {
		t.Errorf("Expected 0 requests, got %d", requestCount.Load())
	}

	// Send 2 more samples (should trigger flush at batch size 5)
	for i := 0; i < 2; i++ {
		samples := []*Sample{
			{
				Name:      "test_metric",
				Labels:    map[string]string{"device": "router1"},
				Value:     float64(i + 3),
				Timestamp: time.Now(),
			},
		}
		if err := exporter.Export(ctx, samples); err != nil {
			t.Errorf("Failed to export samples: %v", err)
		}
	}

	// Should have flushed now
	time.Sleep(100 * time.Millisecond)
	if requestCount.Load() == 0 {
		t.Error("Expected at least one request after reaching batch size")
	}
}

func TestPrometheusExporter_Flush(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	exporter, err := NewPrometheusExporter(server.URL, 1000, 10*time.Second, nil)
	if err != nil {
		t.Fatalf("Failed to create exporter: %v", err)
	}
	defer exporter.Close()

	ctx := context.Background()

	// Add some samples
	samples := []*Sample{
		{
			Name:      "test_metric",
			Labels:    map[string]string{"device": "router1"},
			Value:     123.45,
			Timestamp: time.Now(),
		},
	}

	if err := exporter.Export(ctx, samples); err != nil {
		t.Errorf("Failed to export samples: %v", err)
	}

	// Manual flush
	if err := exporter.Flush(ctx); err != nil {
		t.Errorf("Failed to flush: %v", err)
	}

	// Should have sent request
	if requestCount.Load() == 0 {
		t.Error("Expected at least one request after flush")
	}
}

func TestPrometheusExporter_Close(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	exporter, err := NewPrometheusExporter(server.URL, 1000, 10*time.Second, nil)
	if err != nil {
		t.Fatalf("Failed to create exporter: %v", err)
	}

	// Close should not error
	if err := exporter.Close(); err != nil {
		t.Errorf("Failed to close exporter: %v", err)
	}

	// Double close should not panic
	if err := exporter.Close(); err != nil {
		t.Errorf("Failed to close exporter twice: %v", err)
	}
}

func TestPrometheusExporter_ErrorHandling(t *testing.T) {
	// Server that returns errors
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Internal Server Error"))
	}))
	defer server.Close()

	exporter, err := NewPrometheusExporter(server.URL, 2, 100*time.Millisecond, nil)
	if err != nil {
		t.Fatalf("Failed to create exporter: %v", err)
	}
	defer exporter.Close()

	ctx := context.Background()

	// Add samples that will trigger a flush
	samples := []*Sample{
		{
			Name:      "test_metric",
			Labels:    map[string]string{"device": "router1"},
			Value:     123.45,
			Timestamp: time.Now(),
		},
		{
			Name:      "test_metric",
			Labels:    map[string]string{"device": "router2"},
			Value:     678.90,
			Timestamp: time.Now(),
		},
	}

	// Export hands the batch to the sender and returns; the write happens
	// there, so the write's error surfaces at Flush, which waits for the queue
	// to be attempted. What matters is unchanged: the caller gets the typed
	// error carrying the status the endpoint actually returned.
	if err := exporter.Export(ctx, samples); err != nil {
		t.Fatalf("Export: %v", err)
	}
	flushErr := exporter.Flush(ctx)
	if flushErr == nil {
		t.Error("Expected error when server returns 500")
	}

	var remoteWriteErr *RemoteWriteError
	if !errors.As(flushErr, &remoteWriteErr) {
		t.Fatalf("Expected a typed *RemoteWriteError, got %T: %v", flushErr, flushErr)
	}
	if remoteWriteErr.StatusCode != http.StatusInternalServerError {
		t.Errorf("Expected status 500 in typed error, got %d", remoteWriteErr.StatusCode)
	}
}

// TestPrometheusExporter_RecoversAfterEndpointOutage proves the stall
// scenario described in the incident this test file was written for: a
// remote-write endpoint that fails for a while and then recovers must not
// leave the exporter permanently unable to flush. It also asserts on the
// operator-visible signals (loud logging, health metrics) that must fire
// during the outage.
func TestPrometheusExporter_RecoversAfterEndpointOutage(t *testing.T) {
	buf := captureLogs(t)

	var requestCount atomic.Int32
	var failing atomic.Bool
	failing.Store(true)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		if failing.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	exporter, err := NewPrometheusExporter(server.URL, 2, 20*time.Millisecond, nil)
	if err != nil {
		t.Fatalf("Failed to create exporter: %v", err)
	}
	defer exporter.Close()

	ctx := context.Background()
	own := privateCounters(exporter)

	// Reaching batch size triggers an immediate flush attempt, which fails
	// against the outage server.
	samples := []*Sample{
		{Name: "test_metric", Labels: map[string]string{"device": "r1"}, Value: 1, Timestamp: time.Now()},
		{Name: "test_metric", Labels: map[string]string{"device": "r1"}, Value: 2, Timestamp: time.Now()},
	}
	// Export hands the batch to the sender and returns; it does not write, so
	// it cannot report a write failure. The failure is visible where it now
	// happens -- the error counter, and the loud log -- and that is what the
	// wait below is for.
	if err := exporter.Export(ctx, samples); err != nil {
		t.Fatalf("Export: %v", err)
	}

	// Let the background flusher retry a few times against the still-failing
	// endpoint.
	// Waited for by the LOG, not only by the counter. recordFailure increments
	// the counter before it writes the line, and the failure now happens on the
	// sender goroutine, so a wait on the counter alone can arrive between the
	// two and read an empty buffer.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if testutil.ToFloat64(own.exports.WithLabelValues("error")) >= 2 &&
			strings.Contains(buf.String(), "ERROR") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := testutil.ToFloat64(own.exports.WithLabelValues("error")); got < 2 {
		t.Fatalf("Expected at least 2 recorded flush failures, got %v", got)
	}
	if !strings.Contains(buf.String(), "ERROR") {
		t.Errorf("Expected loud ERROR logging while the endpoint is failing, got: %s", buf.String())
	}
	if _, ok := exporter.LastSuccessfulFlush(); ok {
		t.Error("Expected no successful flush yet")
	}

	// Recover the endpoint and confirm the exporter resumes flushing on
	// its own, without needing a restart.
	failing.Store(false)

	deadline = time.Now().Add(2 * time.Second)
	for {
		if _, ok := exporter.LastSuccessfulFlush(); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Exporter never recovered after the endpoint came back up")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if exporter.BufferedSamples() != 0 {
		t.Errorf("Expected buffer to be drained after recovery, got %d buffered", exporter.BufferedSamples())
	}

	// Prove it didn't just recover once and then wedge again: export more
	// samples and confirm they also make it out.
	countBeforeMore := requestCount.Load()
	moreSamples := []*Sample{
		{Name: "test_metric", Labels: map[string]string{"device": "r1"}, Value: 3, Timestamp: time.Now()},
		{Name: "test_metric", Labels: map[string]string{"device": "r1"}, Value: 4, Timestamp: time.Now()},
	}
	if err := exporter.Export(ctx, moreSamples); err != nil {
		t.Errorf("Expected export to succeed after recovery: %v", err)
	}

	deadline = time.Now().Add(1 * time.Second)
	for requestCount.Load() <= countBeforeMore && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if requestCount.Load() <= countBeforeMore {
		t.Error("Expected additional requests to be sent after recovery, exporter appears wedged")
	}
}

// TestPrometheusExporter_BufferCapDropsOldestSamplesLoudly proves that a
// sustained remote-write outage cannot grow the buffer forever, and that
// every drop is loud (logged and counted), never silent.
func TestPrometheusExporter_BufferCapDropsOldestSamplesLoudly(t *testing.T) {
	buf := captureLogs(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	// A batch size large enough that the cap derives from it rather than from
	// minExportBufferSize, so the test drives the cap it means to.
	const batchSize = minExportBufferSize / 2
	exporter, err := NewPrometheusExporter(server.URL, batchSize, time.Hour, nil)
	if err != nil {
		t.Fatalf("Failed to create exporter: %v", err)
	}
	defer exporter.Close()

	ctx := context.Background()
	own := privateCounters(exporter)

	cap := maxBufferSize(batchSize)

	// The cap bounds the RETRY backlog, so each round has to actually attempt a
	// flush and fail: Export buffers, the batch-size trigger flushes, the flush
	// fails, and the batch goes back. Repeat until the backlog exceeds the cap.
	round := make([]*Sample, batchSize)
	for i := range round {
		round[i] = &Sample{
			Name: "test_metric", Labels: map[string]string{"i": strconv.Itoa(i)},
			Value: float64(i), Timestamp: time.Now(),
		}
	}
	for i := 0; i < exportBufferMultiplier+3; i++ {
		_ = exporter.Export(ctx, round) // expected to fail; the backlog is what's under test
	}

	// The bound is on the QUEUE. One batch may have left it and be on the wire,
	// and BufferedSamples counts that too -- it is undelivered, and reporting
	// zero for it is how a stall reads as quiet.
	if ceiling := cap + batchSize; exporter.BufferedSamples() > ceiling {
		t.Errorf("Expected undelivered samples to be capped at %d (queue %d plus one "+
			"batch in flight), got %d", ceiling, cap, exporter.BufferedSamples())
	}

	if testutil.ToFloat64(own.exports.WithLabelValues("dropped")) == 0 {
		t.Error("Expected the dropped-samples counter to increase once the buffer cap was exceeded")
	}

	if !strings.Contains(buf.String(), "dropping") {
		t.Errorf("Expected a loud log line about dropped samples, got: %s", buf.String())
	}
}

// TestPrometheusExporter_StallWatchdogDetectsSilentStall reproduces the
// exact incident this package was fixed for: samples keep being submitted
// for export, but no successful flush occurs (regardless of why). Even
// with an empty buffer -- the state flushLocked's early return would
// otherwise handle completely silently -- the exporter must report this
// loudly.
func TestPrometheusExporter_StallWatchdogDetectsSilentStall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Large batch size and flush interval so the background flusher and
	// batch-triggered flush don't run during the test; the watchdog is
	// exercised directly and deterministically instead of via a timer.
	exporter, err := NewPrometheusExporter(server.URL, 1000, time.Hour, nil)
	if err != nil {
		t.Fatalf("Failed to create exporter: %v", err)
	}
	defer exporter.Close()

	ctx := context.Background()
	samples := []*Sample{
		{Name: "test_metric", Labels: map[string]string{"device": "r1"}, Value: 1, Timestamp: time.Now()},
	}
	if err := exporter.Export(ctx, samples); err != nil {
		t.Fatalf("Failed to export samples: %v", err)
	}

	if exporter.Stalled() {
		t.Fatal("Exporter should not be considered stalled immediately after its first append")
	}

	// Force the stall window to have elapsed without touching real time.
	exporter.stallThreshold = 0

	buf := captureLogs(t)
	exporter.checkStall()

	if !exporter.Stalled() {
		t.Error("Expected the watchdog to detect the stall")
	}
	if !strings.Contains(buf.String(), "STALLED") {
		t.Errorf("Expected a loud STALLED warning, got: %s", buf.String())
	}
	if got := testutil.ToFloat64(exportStalled); got != 1 {
		t.Errorf("Expected gonetmon_export_stalled=1, got %v", got)
	}

	// A successful flush must clear the stall, loudly announcing recovery.
	if err := exporter.Flush(ctx); err != nil {
		t.Fatalf("Failed to flush: %v", err)
	}
	if exporter.Stalled() {
		t.Error("Expected the stall to clear after a successful flush")
	}
	if !strings.Contains(buf.String(), "recovered") {
		t.Errorf("Expected a loud recovery log line, got: %s", buf.String())
	}
	if got := testutil.ToFloat64(exportStalled); got != 0 {
		t.Errorf("Expected gonetmon_export_stalled=0 after recovery, got %v", got)
	}
}

// TestPrometheusExporter_ExportSNMPResults_NilAndEmptyInput is the
// bad-input counterpart to TestPrometheusExporter_ExportSNMPResults: it
// must handle nil/empty inputs without error or panic and must not touch
// the network.
func TestPrometheusExporter_ExportSNMPResults_NilAndEmptyInput(t *testing.T) {
	var requestReceived atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestReceived.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	exporter, err := NewPrometheusExporter(server.URL, 1000, time.Hour, nil)
	if err != nil {
		t.Fatalf("Failed to create exporter: %v", err)
	}
	defer exporter.Close()

	ctx := context.Background()

	if err := exporter.ExportSNMPResults(ctx, "device1", nil, nil); err != nil {
		t.Errorf("Expected nil results to be handled without error, got: %v", err)
	}
	if err := exporter.ExportSNMPResults(ctx, "device1", []*snmp.Result{}, map[string]string{}); err != nil {
		t.Errorf("Expected empty results to be handled without error, got: %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	if requestReceived.Load() {
		t.Error("Expected no remote write request for empty input")
	}
	if exporter.BufferedSamples() != 0 {
		t.Errorf("Expected nothing buffered for empty input, got %d", exporter.BufferedSamples())
	}
}

// TestNewPrometheusExporter_TypedConfigError proves construction errors are
// typed, not opaque fmt.Errorf strings, so callers can distinguish and
// handle specific failure modes.
func TestNewPrometheusExporter_TypedConfigError(t *testing.T) {
	_, err := NewPrometheusExporter("", 1000, 10*time.Second, nil)
	var configErr *ConfigError
	if !errors.As(err, &configErr) {
		t.Fatalf("Expected a typed *ConfigError, got %T: %v", err, err)
	}
	if configErr.Field != "remoteWriteURL" {
		t.Errorf("Expected error about remoteWriteURL, got field %q", configErr.Field)
	}
}

// BenchmarkPrometheusExporter_Export benchmarks the export performance
func BenchmarkPrometheusExporter_Export(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	exporter, _ := NewPrometheusExporter(server.URL, 10000, 1*time.Hour, nil)
	defer exporter.Close()

	sample := &Sample{
		Name:      "test_metric",
		Labels:    map[string]string{"device": "router1"},
		Value:     123.45,
		Timestamp: time.Now(),
	}

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = exporter.Export(ctx, []*Sample{sample})
	}
}

// TestPrometheusExporter_ExportNotBlockedBySlowRemoteWrite is the regression
// for a wedge that took down collection entirely: the flush held the buffer
// lock across the remote-write HTTP request, so a remote endpoint that
// accepted the connection and then stalled blocked every Export call — and
// with it every poll worker, since the poller exports inline mid-poll.
func TestPrometheusExporter_ExportNotBlockedBySlowRemoteWrite(t *testing.T) {
	release := make(chan struct{})
	var inFlight sync.WaitGroup
	inFlight.Add(1)

	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { inFlight.Done() })
		<-release // hold the request open
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	defer close(release)

	// A large batch size means only the explicit Flush below reaches the
	// network; later Exports just buffer, which is what must stay unblocked.
	exporter, err := NewPrometheusExporter(server.URL, 1000, time.Hour, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	defer exporter.Close()

	if err := exporter.Export(context.Background(), []*Sample{
		{Name: "first", Value: 1, Timestamp: time.Now()},
	}); err != nil {
		t.Fatalf("Export: %v", err)
	}

	go func() { _ = exporter.Flush(context.Background()) }()
	inFlight.Wait() // the flush is now stuck inside the handler

	// Buffering more samples must not wait on that in-flight request.
	done := make(chan error, 1)
	go func() {
		done <- exporter.Export(context.Background(), []*Sample{
			{Name: "second", Value: 2, Timestamp: time.Now()},
		})
	}()

	select {
	case <-done:
		// Reaching here at all is the point: buffering is not blocked by an
		// in-flight remote write.
	case <-time.After(5 * time.Second):
		t.Fatal("Export blocked behind an in-flight remote write; the buffer lock is held across the request")
	}
}

// TestPrometheusExporter_FailedBatchIsRetriedNotLost verifies that a batch the
// endpoint rejects stays buffered for the next attempt rather than vanishing
// now that the batch is removed from the buffer before the request.
func TestPrometheusExporter_FailedBatchIsRetriedNotLost(t *testing.T) {
	var attempts atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	exporter, err := NewPrometheusExporter(server.URL, 100, time.Hour, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	defer exporter.Close()

	if err := exporter.Export(context.Background(), []*Sample{
		{Name: "kept", Value: 1, Timestamp: time.Now()},
	}); err != nil {
		t.Fatalf("Export: %v", err)
	}

	// First flush fails; the sample must survive for the retry.
	if err := exporter.Flush(context.Background()); err == nil {
		t.Fatal("expected the first flush to fail")
	}
	if got := exporter.BufferedSamples(); got != 1 {
		t.Fatalf("buffered %d samples after a failed flush, want 1 — the batch must be retried, not dropped", got)
	}

	if err := exporter.Flush(context.Background()); err != nil {
		t.Fatalf("second flush: %v", err)
	}
	if got := exporter.BufferedSamples(); got != 0 {
		t.Errorf("buffered %d samples after a successful flush, want 0", got)
	}
}

// TestPrometheusExporter_LargeBatchIsNotTruncated is the regression for silent
// data loss on a healthy pipeline. One poll of one router produces ~15,000
// samples; with the retry cap applied to arriving data rather than to the retry
// backlog, everything past the cap was discarded before a flush was even
// attempted — 8,262 samples dropped in production while remote write was up.
func TestPrometheusExporter_LargeBatchIsNotTruncated(t *testing.T) {
	var received atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		raw, err := snappy.Decode(nil, body)
		if err != nil {
			t.Errorf("decode snappy: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var req prompb.WriteRequest
		if err := req.Unmarshal(raw); err != nil {
			t.Errorf("unmarshal: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		for _, ts := range req.Timeseries {
			received.Add(int64(len(ts.Samples)))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	exporter, err := NewPrometheusExporter(server.URL, 1000, time.Hour, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	defer exporter.Close()

	// A realistic single-poll batch, well past batchSize * exportBufferMultiplier.
	const batch = 15000
	samples := make([]*Sample, 0, batch)
	now := time.Now()
	for i := 0; i < batch; i++ {
		samples = append(samples, &Sample{
			Name:      "snmp_test_metric",
			Labels:    map[string]string{"i": strconv.Itoa(i)},
			Value:     float64(i),
			Timestamp: now,
		})
	}

	// Counters private to this exporter, so the assertion is about this
	// exporter and not about whatever else the package is running. A delta on
	// the shared counter was the previous workaround and is not one: another
	// test's sender can increment inside the window.
	own := privateCounters(exporter)

	if err := exporter.Export(context.Background(), samples); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if err := exporter.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if got := received.Load(); got != batch {
		t.Errorf("remote write received %d samples, want %d — a healthy pipeline must not drop data", got, batch)
	}
	if dropped := testutil.ToFloat64(own.exports.WithLabelValues("dropped")); dropped != 0 {
		t.Errorf("dropped %v samples with remote write healthy, want 0", dropped)
	}
}

// TestMaxBufferSize verifies the retry backlog can always hold more than a
// single poll, whatever the configured batch size.
func TestMaxBufferSize(t *testing.T) {
	tests := []struct {
		batchSize int
		want      int
	}{
		{1000, minExportBufferSize},             // default: the floor applies
		{100, minExportBufferSize},              // small batches still get the floor
		{50000, 50000 * exportBufferMultiplier}, // a large batch scales past it
	}

	for _, tt := range tests {
		if got := maxBufferSize(tt.batchSize); got != tt.want {
			t.Errorf("maxBufferSize(%d) = %d, want %d", tt.batchSize, got, tt.want)
		}
	}
}

// TestOverlappingPollsOfOneDeviceEncodeInTimestampOrder is the trigger from
// go-netmon#69, at the layer that decides whether the request is valid.
//
// Two polls of one device overlap whenever the device is reconfigured. Their
// samples reach the buffer in whole-poll batches at completion, while each
// sample was stamped as its varbind arrived -- so the poll that started first
// and finished second appends OLDER samples after the other's. Encoding them in
// buffer order produces a series whose samples descend, and Prometheus rejects
// the entire request with 400 "out of order sample".
//
// The fixture is that exact shape: the later-appended samples are the earlier
// ones, which is what a poll finishing late looks like from the buffer.
func TestOverlappingPollsOfOneDeviceEncodeInTimestampOrder(t *testing.T) {
	base := time.Date(2026, 9, 1, 20, 51, 55, 0, time.UTC)
	series := map[string]string{"device": "pdu-03", "ifindex": "1"}

	// Poll B started later and finished first; poll A finished after it.
	samples := []*Sample{
		{Name: "snmp_ifHCInOctets", Labels: series, Value: 300, Timestamp: base.Add(2 * time.Second)},
		{Name: "snmp_ifHCInOctets", Labels: series, Value: 100, Timestamp: base},
		{Name: "snmp_ifHCInOctets", Labels: series, Value: 200, Timestamp: base.Add(1 * time.Second)},
	}

	e := &PrometheusExporter{}
	encoded := e.samplesToTimeSeries(samples)

	if len(encoded) != 1 {
		t.Fatalf("series = %d, want 1: the fixture is one series", len(encoded))
	}
	got := encoded[0].Samples
	if len(got) != 3 {
		t.Fatalf("samples = %d, want 3", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].Timestamp <= got[i-1].Timestamp {
			t.Fatalf("sample %d (%d) does not follow sample %d (%d): remote write "+
				"rejects the whole request on the first that does not ascend",
				i, got[i].Timestamp, i-1, got[i-1].Timestamp)
		}
	}
	if got[0].Value != 100 || got[2].Value != 300 {
		t.Errorf("values = %v, want the readings to travel with their timestamps",
			[]float64{got[0].Value, got[1].Value, got[2].Value})
	}
}

// TestOnlyIdenticalReadingsForOneInstantAreCollapsed pins which of the two
// same-timestamp cases the encoder may act on by itself.
//
// The same reading offered twice is lossless to collapse, and Prometheus
// stores it once regardless. Two DIFFERENT values at one instant are a loss to
// collapse, and the encoder cannot report a loss: it returns samples, and its
// caller records the batch as fully sent. That pair is partition's business
// (TestTwoConflictingReadingsInOneBatchAreNotCollapsedAwaySilently), so the
// encoder must leave it alone -- collapsing it here would deliver one reading,
// discard the other, and call the flush a success.
func TestOnlyIdenticalReadingsForOneInstantAreCollapsed(t *testing.T) {
	at := time.Date(2026, 9, 1, 20, 51, 55, 0, time.UTC)
	series := map[string]string{"device": "pdu-03", "ifindex": "1"}

	repeated := []*Sample{
		{Name: "snmp_ifHCInOctets", Labels: series, Value: 100, Timestamp: at},
		{Name: "snmp_ifHCInOctets", Labels: series, Value: 100, Timestamp: at},
		{Name: "snmp_ifHCInOctets", Labels: series, Value: 200, Timestamp: at.Add(time.Second)},
	}

	e := &PrometheusExporter{}
	got := e.samplesToTimeSeries(repeated)[0].Samples
	if len(got) != 2 {
		t.Fatalf("samples = %d, want 2: the same reading offered twice is stored once", len(got))
	}
	if got[0].Timestamp == got[1].Timestamp {
		t.Error("two samples still share a timestamp")
	}

	conflicting := []*Sample{
		{Name: "snmp_ifHCInOctets", Labels: series, Value: 100, Timestamp: at},
		{Name: "snmp_ifHCInOctets", Labels: series, Value: 150, Timestamp: at},
		{Name: "snmp_ifHCInOctets", Labels: series, Value: 200, Timestamp: at.Add(time.Second)},
	}

	got = e.samplesToTimeSeries(conflicting)[0].Samples
	if len(got) != 3 {
		t.Fatalf("samples = %d, want 3: the encoder must not choose between two "+
			"readings for one instant, because it has no way to say it discarded one", len(got))
	}
	kept := []float64{got[0].Value, got[1].Value, got[2].Value}
	for _, want := range []float64{100, 150, 200} {
		if !slices.Contains(kept, want) {
			t.Errorf("encoded %v, want %v among them", kept, want)
		}
	}
}

// TestSeriesAreOrderedIndependently guards the obvious way to get this wrong:
// ordering the buffer as a whole rather than each series, which would still
// leave a series interleaved with its neighbours' timestamps.
func TestSeriesAreOrderedIndependently(t *testing.T) {
	base := time.Date(2026, 9, 1, 20, 51, 55, 0, time.UTC)
	one := map[string]string{"device": "pdu-03", "ifindex": "1"}
	two := map[string]string{"device": "pdu-03", "ifindex": "2"}

	samples := []*Sample{
		{Name: "snmp_ifHCInOctets", Labels: one, Value: 1, Timestamp: base.Add(3 * time.Second)},
		{Name: "snmp_ifHCInOctets", Labels: two, Value: 2, Timestamp: base.Add(1 * time.Second)},
		{Name: "snmp_ifHCInOctets", Labels: one, Value: 3, Timestamp: base},
		{Name: "snmp_ifHCInOctets", Labels: two, Value: 4, Timestamp: base.Add(2 * time.Second)},
	}

	e := &PrometheusExporter{}
	encoded := e.samplesToTimeSeries(samples)

	if len(encoded) != 2 {
		t.Fatalf("series = %d, want 2", len(encoded))
	}
	for _, ts := range encoded {
		if len(ts.Samples) != 2 {
			t.Fatalf("series has %d samples, want 2", len(ts.Samples))
		}
		if ts.Samples[1].Timestamp <= ts.Samples[0].Timestamp {
			t.Errorf("series %v is not in ascending order", ts.Labels)
		}
	}
}

// TestTwoSeriesCannotShareAKeyThroughTheirLabelText guards the grouping key.
//
// Label values come from the device -- an ifAlias or entPhysicalName is
// whatever someone typed into it -- so a comma or an equals sign inside one
// made a delimited key ambiguous, and two different series produced the same
// string. Merging them interleaves their readings into one series, which is a
// descending pair and a rejected request (#69).
//
// The two fixtures below flatten to the identical string under `name,k=v`
// joining -- verified by mutation, because the first pair written here did not:
//
//	{a: "1,b=2"}          -> "snmp_ifHCInOctets" + ",a=1,b=2"
//	{a: "1", b: "2"}      -> "snmp_ifHCInOctets" + ",a=1" + ",b=2"
func TestTwoSeriesCannotShareAKeyThroughTheirLabelText(t *testing.T) {
	at := time.Date(2026, 9, 1, 20, 51, 55, 0, time.UTC)
	samples := []*Sample{
		{
			Name:      "snmp_ifHCInOctets",
			Labels:    map[string]string{"a": "1,b=2"},
			Value:     100,
			Timestamp: at.Add(time.Second),
		},
		{
			Name:      "snmp_ifHCInOctets",
			Labels:    map[string]string{"a": "1", "b": "2"},
			Value:     200,
			Timestamp: at,
		},
	}

	e := &PrometheusExporter{}
	encoded := e.samplesToTimeSeries(samples)

	if len(encoded) != 2 {
		t.Fatalf("series = %d, want 2: two label sets were merged into one, so "+
			"one series now carries both readings", len(encoded))
	}
	for _, ts := range encoded {
		if len(ts.Samples) != 1 {
			t.Errorf("series %v holds %d samples, want its own 1", ts.Labels, len(ts.Samples))
		}
	}
}
