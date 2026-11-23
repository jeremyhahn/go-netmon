package metrics

// What one remote-write request may contain.
//
// batch_size decided when a flush was TRIGGERED and nothing decided how much
// went into a request, so Flush took the whole buffer and sent it as one
// protobuf -- and the retry backlog floor alone is 100,000 samples. A request
// too large to be accepted is refused at the same size on every attempt, so it
// can never drain and everything queues behind it (#63).

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang/snappy"
	"github.com/prometheus/prometheus/prompb"
)

// receivedRequest is one request as the endpoint saw it.
type receivedRequest struct {
	samples int
	bytes   int
	// series maps a series' "i" label to the timestamps it carried, in the
	// order they arrived.
	series map[string][]int64
}

// recorder is a remote-write endpoint that remembers every request, and refuses
// the ones a test tells it to.
type recorder struct {
	mu       sync.Mutex
	requests []receivedRequest
	// refuse, when set, decides the status for the request with that index.
	refuse func(index int) int
}

func (r *recorder) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
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

		seen := receivedRequest{bytes: len(raw), series: map[string][]int64{}}
		for _, ts := range request.Timeseries {
			seen.samples += len(ts.Samples)
			key := ""
			for _, label := range ts.Labels {
				if label.Name == "i" {
					key = label.Value
				}
			}
			for _, s := range ts.Samples {
				seen.series[key] = append(seen.series[key], s.Timestamp)
			}
		}

		r.mu.Lock()
		index := len(r.requests)
		r.requests = append(r.requests, seen)
		refuse := r.refuse
		r.mu.Unlock()

		if refuse != nil {
			if status := refuse(index); status != 0 {
				w.WriteHeader(status)
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// taken returns a copy of what the endpoint has received.
func (r *recorder) taken() []receivedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]receivedRequest(nil), r.requests...)
}

// delivered is the set of series the endpoint has accepted, with how many times
// each was delivered. Requests it refused are excluded, since nothing in them
// was stored.
func (r *recorder) delivered(accepted func(index int) bool) map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()

	counts := map[string]int{}
	for index, request := range r.requests {
		if !accepted(index) {
			continue
		}
		for key, stamps := range request.series {
			counts[key] += len(stamps)
		}
	}
	return counts
}

// TestABatchBeyondBatchSizeBecomesSeveralRequests is the count bound, and it is
// what batch_size has always claimed to do.
func TestABatchBeyondBatchSizeBecomesSeveralRequests(t *testing.T) {
	endpoint := &recorder{}
	server := httptest.NewServer(endpoint.handler(t))
	defer server.Close()

	const batchSize = 100
	const samples = 500

	exporter, err := NewPrometheusExporter(server.URL, batchSize, time.Hour, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	defer func() { _ = exporter.Close() }()
	exporter.maxSampleAge = time.Hour

	at := time.Now()
	batch := make([]*Sample, 0, samples)
	for i := 0; i < samples; i++ {
		batch = append(batch, sample(i, at))
	}
	if err := exporter.Export(context.Background(), batch); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if err := exporter.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	requests := endpoint.taken()
	if len(requests) != samples/batchSize {
		t.Errorf("%d requests reached the endpoint, want %d: batch_size bounds the flush "+
			"trigger and must also bound the request", len(requests), samples/batchSize)
	}

	total := 0
	for i, request := range requests {
		total += request.samples
		if request.samples > batchSize {
			t.Errorf("request %d carried %d samples, want at most %d", i, request.samples, batchSize)
		}
	}
	if total != samples {
		t.Errorf("%d samples were delivered across %d requests, want %d: splitting a batch "+
			"must not lose any of it", total, len(requests), samples)
	}
}

// TestARequestIsBoundedByBytesAndNotOnlyByCount is the half a sample count
// cannot do.
//
// A sample is not a fixed size. A counter reading is a handful of short labels;
// an entity-sensor sample carries entPhysicalName, and an ifAlias is whatever
// somebody typed into the box. One count therefore produces requests of wildly
// different sizes, and nothing in the path noticed.
func TestARequestIsBoundedByBytesAndNotOnlyByCount(t *testing.T) {
	endpoint := &recorder{}
	server := httptest.NewServer(endpoint.handler(t))
	defer server.Close()

	// A count bound that would allow a request several times over the byte
	// bound, so the byte bound is the one that has to bind.
	const batchSize = 2000
	const wide = 8 << 10

	exporter, err := NewPrometheusExporter(server.URL, batchSize, time.Hour, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	defer func() { _ = exporter.Close() }()
	exporter.maxSampleAge = time.Hour

	at := time.Now()
	batch := make([]*Sample, 0, batchSize)
	for i := 0; i < batchSize; i++ {
		batch = append(batch, fatSample(i, at, wide))
	}
	if err := exporter.Export(context.Background(), batch); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if err := exporter.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	requests := endpoint.taken()
	if len(requests) < 2 {
		t.Fatalf("%d request(s) reached the endpoint: %d samples of ~%dB of labels each is "+
			"far past the byte bound, so it should have been split", len(requests), batchSize, wide)
	}

	total := 0
	for i, request := range requests {
		total += request.samples
		if request.bytes > maxRequestBytes {
			t.Errorf("request %d marshaled to %d bytes, want at most %d",
				i, request.bytes, maxRequestBytes)
		}
		if request.samples >= batchSize {
			t.Errorf("request %d carried %d samples, which is the whole count bound: "+
				"the byte bound did not bind", i, request.samples)
		}
	}
	if total != batchSize {
		t.Errorf("%d samples delivered, want %d", total, batchSize)
	}
}

// TestSplittingKeepsEachSeriesAscendingAcrossRequests is the property that
// makes splitting safe at all, and the one an index-order split does not have.
//
// Requests reach the endpoint in order and each advances the endpoint's head
// for the series it carries. A boundary that puts a series' NEWER reading in
// the earlier request and its older one in the later gets the later request
// refused -- with every other reading in it. That is #69's failure, reached
// through #63's fix rather than through a poller.
func TestSplittingKeepsEachSeriesAscendingAcrossRequests(t *testing.T) {
	endpoint := &recorder{}
	server := httptest.NewServer(endpoint.handler(t))
	defer server.Close()

	const batchSize = 8
	const series = 16
	const rounds = 4

	exporter, err := NewPrometheusExporter(server.URL, batchSize, time.Hour, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	defer func() { _ = exporter.Close() }()
	exporter.maxSampleAge = time.Hour

	// Buffer order: whole rounds, so one series' readings are far apart in the
	// slice and a split by index alone would separate them badly.
	at := time.Now().Add(-time.Minute)
	batch := make([]*Sample, 0, series*rounds)
	for round := 0; round < rounds; round++ {
		for i := 0; i < series; i++ {
			batch = append(batch, sample(i, at.Add(time.Duration(round)*time.Second)))
		}
	}

	if err := exporter.Export(context.Background(), batch); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if err := exporter.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	requests := endpoint.taken()
	if len(requests) < 2 {
		t.Fatalf("%d request(s) reached the endpoint, want several: nothing was split, so "+
			"the property under test was not exercised", len(requests))
	}

	// Across every request, in the order the endpoint received them, each
	// series' timestamps must never go backwards.
	head := map[string]int64{}
	for i, request := range requests {
		for key, stamps := range request.series {
			for _, at := range stamps {
				if previous, seen := head[key]; seen && at < previous {
					t.Errorf("request %d carries series %s at %d, behind the %d an earlier "+
						"request already delivered: the endpoint would refuse this request whole",
						i, key, at, previous)
				}
				head[key] = at
			}
		}
	}
}

// TestAPartiallyDeliveredBatchPutsBackOnlyTheRemainder is the interaction the
// issue calls out: whatever chunking is chosen, a batch that fails midway must
// put back only what was not delivered.
//
// Redelivering an accepted prefix is not merely wasteful. The endpoint's head
// has moved to the end of it, so on the next attempt those samples are behind
// their own head -- and the request carrying them is refused whole, taking the
// remainder with it. A retry that redelivers is a retry that cannot succeed.
func TestAPartiallyDeliveredBatchPutsBackOnlyTheRemainder(t *testing.T) {
	const batchSize = 100
	const samples = 500
	const failFrom = 2

	var outage sync.Mutex
	failing := true
	endpoint := &recorder{
		refuse: func(index int) int {
			outage.Lock()
			defer outage.Unlock()
			if failing && index >= failFrom {
				return http.StatusServiceUnavailable
			}
			return 0
		},
	}
	server := httptest.NewServer(endpoint.handler(t))
	defer server.Close()

	exporter, err := NewPrometheusExporter(server.URL, batchSize, time.Hour, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	defer func() { _ = exporter.Close() }()
	exporter.maxSampleAge = time.Hour

	at := time.Now()
	batch := make([]*Sample, 0, samples)
	for i := 0; i < samples; i++ {
		batch = append(batch, sample(i, at))
	}
	if err := exporter.Export(context.Background(), batch); err != nil {
		t.Fatalf("Export: %v", err)
	}

	if err := exporter.Flush(context.Background()); err == nil {
		t.Fatal("Flush reported success while the endpoint was refusing")
	}

	// Two requests landed; the rest of the batch is still owed.
	if got := exporter.BufferedSamples(); got != samples-failFrom*batchSize {
		t.Errorf("%d samples are still owed, want %d: what the endpoint accepted before it "+
			"failed must come off the batch", got, samples-failFrom*batchSize)
	}

	outage.Lock()
	failing = false
	outage.Unlock()

	if err := exporter.Flush(context.Background()); err != nil {
		t.Fatalf("Flush after recovery: %v", err)
	}

	// Every series delivered exactly once, counting only the requests the
	// endpoint actually accepted.
	accepted := func(index int) bool {
		return index < failFrom || index > failFrom
	}
	counts := endpoint.delivered(accepted)
	if len(counts) != samples {
		t.Errorf("%d series were delivered, want %d", len(counts), samples)
	}
	for i := 0; i < samples; i++ {
		key := strconv.Itoa(i)
		switch counts[key] {
		case 1:
		case 0:
			t.Errorf("series %s was never delivered", key)
		default:
			t.Errorf("series %s was delivered %d times: an accepted prefix was offered again, "+
				"and the endpoint's head has already moved past it", key, counts[key])
		}
	}
}

// TestASampleTooLargeForTheByteBoundIsStillSent is the bad-input half.
//
// A bound that refuses to emit anything is not a bound, it is a stall: the
// batch would never drain and the reading would be offered at the same size
// forever, which is the defect being fixed rather than a smaller copy of it.
func TestASampleTooLargeForTheByteBoundIsStillSent(t *testing.T) {
	endpoint := &recorder{}
	server := httptest.NewServer(endpoint.handler(t))
	defer server.Close()

	exporter, err := NewPrometheusExporter(server.URL, 1000, time.Hour, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	defer func() { _ = exporter.Close() }()
	exporter.maxSampleAge = time.Hour

	oversized := fatSample(0, time.Now(), maxRequestBytes+1)
	if err := exporter.Export(context.Background(), []*Sample{oversized}); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if err := exporter.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	requests := endpoint.taken()
	if len(requests) != 1 {
		t.Fatalf("%d requests reached the endpoint, want 1", len(requests))
	}
	if requests[0].samples != 1 {
		t.Errorf("the oversized reading was not delivered: %d samples in the request",
			requests[0].samples)
	}
	if requests[0].bytes <= maxRequestBytes {
		t.Errorf("the request marshaled to %d bytes, which is inside the bound: the test "+
			"did not build a sample that exceeds it", requests[0].bytes)
	}
}

// TestSendingNothingMakesNoRequest is the other bad-input case: an empty batch
// must not produce an empty request, which a receiver counts as traffic and an
// operator counts as a working pipeline.
func TestSendingNothingMakesNoRequest(t *testing.T) {
	endpoint := &recorder{}
	server := httptest.NewServer(endpoint.handler(t))
	defer server.Close()

	exporter, err := NewPrometheusExporter(server.URL, 1000, time.Hour, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	defer func() { _ = exporter.Close() }()

	delivered, err := exporter.send(context.Background(), nil)
	if err != nil {
		t.Fatalf("send of nothing: %v", err)
	}
	if delivered != 0 {
		t.Errorf("send of nothing reported %d delivered, want 0", delivered)
	}
	if got := len(endpoint.taken()); got != 0 {
		t.Errorf("%d requests reached the endpoint for an empty batch, want 0", got)
	}
}

// TestTheCatalogGoesThroughTheSameBounds closes the gap SendCatalog's own
// comment named: it chunked by count and deliberately did not attempt a byte
// bound, because that belongs to every send rather than to one caller.
func TestTheCatalogGoesThroughTheSameBounds(t *testing.T) {
	endpoint := &recorder{}
	server := httptest.NewServer(endpoint.handler(t))
	defer server.Close()

	const batchSize = 250
	const entries = 1200

	exporter, err := NewPrometheusExporter(server.URL, batchSize, time.Hour, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	defer func() { _ = exporter.Close() }()

	at := time.Now()
	catalog := make([]*Sample, 0, entries)
	for i := 0; i < entries; i++ {
		catalog = append(catalog, sample(i, at))
	}
	if err := exporter.SendCatalog(context.Background(), catalog); err != nil {
		t.Fatalf("SendCatalog: %v", err)
	}

	requests := endpoint.taken()
	total := 0
	for i, request := range requests {
		total += request.samples
		if request.samples > batchSize {
			t.Errorf("catalog request %d carried %d samples, want at most %d",
				i, request.samples, batchSize)
		}
		if request.bytes > maxRequestBytes {
			t.Errorf("catalog request %d marshaled to %d bytes, want at most %d",
				i, request.bytes, maxRequestBytes)
		}
	}
	if total != entries {
		t.Errorf("%d catalog samples delivered, want %d", total, entries)
	}
}

// TestOrderForSplittingGroupsEachSeriesAndAscendsWithinIt is the sort on its
// own, since everything above depends on it.
func TestOrderForSplittingGroupsEachSeriesAndAscendsWithinIt(t *testing.T) {
	at := time.Now()

	// Interleaved: whole rounds, so no series' readings are adjacent.
	samples := []*Sample{}
	for round := 2; round >= 0; round-- {
		for i := 0; i < 4; i++ {
			samples = append(samples, sample(i, at.Add(time.Duration(round)*time.Second)))
		}
	}

	orderForSplitting(samples)

	seen := map[string]bool{}
	previousKey := ""
	var previousAt time.Time
	for index, s := range samples {
		key := seriesKey(s)
		if key != previousKey {
			if seen[key] {
				t.Errorf("series %s resumes at index %d after another series: its readings are "+
					"not contiguous, so a split can separate them in the wrong order", key, index)
			}
			seen[key] = true
			previousKey = key
			previousAt = s.Timestamp
			continue
		}
		if s.Timestamp.Before(previousAt) {
			t.Errorf("series %s descends at index %d: %s then %s",
				key, index, previousAt, s.Timestamp)
		}
		previousAt = s.Timestamp
	}
}

// TestOrderForSplittingHandlesNothingToOrder is its bad-input half.
func TestOrderForSplittingHandlesNothingToOrder(t *testing.T) {
	orderForSplitting(nil)

	one := []*Sample{sample(0, time.Now())}
	orderForSplitting(one)
	if len(one) != 1 || one[0] == nil {
		t.Errorf("a single sample did not survive being ordered: %v", one)
	}
}

// TestRequestSamplesFallsBackWhenBatchSizeIsUnset covers the bound's own
// defaulting: an exporter built with a nonsense batch size must still bound its
// requests, rather than treating "no bound configured" as "no bound".
func TestRequestSamplesFallsBackWhenBatchSizeIsUnset(t *testing.T) {
	exporter := &PrometheusExporter{batchSize: 0}
	if got := exporter.requestSamples(); got != defaultBatchSize {
		t.Errorf("requestSamples with batchSize 0 = %d, want %d", got, defaultBatchSize)
	}

	exporter.batchSize = -1
	if got := exporter.requestSamples(); got != defaultBatchSize {
		t.Errorf("requestSamples with a negative batchSize = %d, want %d", got, defaultBatchSize)
	}

	exporter.batchSize = 42
	if got := exporter.requestSamples(); got != 42 {
		t.Errorf("requestSamples = %d, want the configured 42", got)
	}
}

// fatSample is a reading whose label value is wide bytes long, which is what
// makes a sample count a poor proxy for a request's size.
func fatSample(i int, at time.Time, wide int) *Sample {
	return &Sample{
		Name: "snmp_test_metric",
		Labels: map[string]string{
			"i":       strconv.Itoa(i),
			"ifalias": strings.Repeat("x", wide),
		},
		Value:     float64(i),
		Timestamp: at,
	}
}
