package metrics

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
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

// captureLogs temporarily redirects the standard logger to a buffer so
// tests can assert on what an operator would actually see, and restores it
// on return. Tests in this package run sequentially (none use
// t.Parallel()), so swapping the process-wide log output is safe here.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prevOutput := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.SetOutput(prevOutput)
		log.SetFlags(prevFlags)
	})
	return &buf
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

	// This should fail because batch size is reached and flush returns error
	err = exporter.Export(ctx, samples)
	if err == nil {
		t.Error("Expected error when server returns 500")
	}

	var remoteWriteErr *RemoteWriteError
	if !errors.As(err, &remoteWriteErr) {
		t.Fatalf("Expected a typed *RemoteWriteError, got %T: %v", err, err)
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

	// Reaching batch size triggers an immediate flush attempt, which fails
	// against the outage server.
	samples := []*Sample{
		{Name: "test_metric", Labels: map[string]string{"device": "r1"}, Value: 1, Timestamp: time.Now()},
		{Name: "test_metric", Labels: map[string]string{"device": "r1"}, Value: 2, Timestamp: time.Now()},
	}
	if err := exporter.Export(ctx, samples); err == nil {
		t.Fatal("Expected export to fail while the endpoint is down")
	}

	// Let the background flusher retry a few times against the still-failing
	// endpoint.
	deadline := time.Now().Add(2 * time.Second)
	for testutil.ToFloat64(exportsTotal.WithLabelValues("error")) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := testutil.ToFloat64(exportsTotal.WithLabelValues("error")); got < 2 {
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
	droppedBefore := testutil.ToFloat64(exportsTotal.WithLabelValues("dropped"))

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

	if got := exporter.BufferedSamples(); got > cap {
		t.Errorf("Expected buffered samples to be capped at %d, got %d", cap, got)
	}

	droppedAfter := testutil.ToFloat64(exportsTotal.WithLabelValues("dropped"))
	if droppedAfter <= droppedBefore {
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

	// exportsTotal is a package-level counter shared by every test in this
	// package, so measure the delta rather than the absolute value.
	droppedBefore := testutil.ToFloat64(exportsTotal.WithLabelValues("dropped"))

	if err := exporter.Export(context.Background(), samples); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if err := exporter.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if got := received.Load(); got != batch {
		t.Errorf("remote write received %d samples, want %d — a healthy pipeline must not drop data", got, batch)
	}
	if dropped := testutil.ToFloat64(exportsTotal.WithLabelValues("dropped")) - droppedBefore; dropped != 0 {
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
