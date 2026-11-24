//go:build integration
// +build integration

package integration

// Remote-write ordering against a real Prometheus: intra-request disorder is dropped silently, a reading behind the committed head is refused with 400.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/golang/snappy"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/prometheus/prompb"

	"github.com/jeremyhahn/go-netmon/internal/metrics"
)

// The reproduction's shape: many series, a fast cadence and repeated overlap, sized to run in seconds.
const (
	// orderingSeries is how many series one simulated poll produces. It is the
	// "more interfaces" half: one poll has to be big enough that two of them
	// overlap in something other than a single reading.
	orderingSeries = 64

	// orderingRounds is how many times the overlap is repeated. One trial of
	// anything timing-shaped is an anecdote.
	orderingRounds = 10

	// orderingPeriod is the simulated poll interval -- the "faster cadence"
	// half. Two polls a period apart overlap; the next period's pair starts
	// after the previous pair's newest reading, exactly as a running poller's
	// would.
	orderingPeriod = 2 * time.Second

	// orderingPollSpread exceeds orderingLateSpread so the poll that starts first finishes second.
	orderingPollSpread = 1200 * time.Millisecond

	// orderingLateSpread is how long the overtaking poll takes. Shorter, so it
	// completes -- and therefore exports -- before the one already running.
	orderingLateSpread = 300 * time.Millisecond

	// orderingOverlap is how long after the first poll starts that the second
	// one does. Larger than orderingPollSpread would mean no overlap at all.
	orderingOverlap = 1400 * time.Millisecond
)

// orderingMetric is the metric name every series in this file carries. It is
// not an snmp_ name, so nothing else in the suite can write to it.
const orderingMetric = "netmon_ordering_probe"

// queryWindow is the range every count_over_time in this file uses. It has to
// cover the whole simulated schedule with room for the time the test itself
// takes.
const queryWindow = "5m"

// RemoteWriteRefusedError reports a remote-write request the endpoint would not
// take. It carries the body because the reason is in it -- "out of order
// sample" and "duplicate sample for timestamp" are different faults, and a bare
// 400 does not distinguish them.
type RemoteWriteRefusedError struct {
	URL        string
	StatusCode int
	Body       string
}

func (e *RemoteWriteRefusedError) Error() string {
	return fmt.Sprintf("remote write to %s refused with HTTP %d: %s", e.URL, e.StatusCode, e.Body)
}

// RemoteWriteTransportError reports a request that never got an answer, which
// is a different fact from one that was refused: the endpoint may not have seen
// it at all.
type RemoteWriteTransportError struct {
	URL   string
	Stage string
	Err   error
}

func (e *RemoteWriteTransportError) Error() string {
	return fmt.Sprintf("remote write to %s failed at %s: %v", e.URL, e.Stage, e.Err)
}

func (e *RemoteWriteTransportError) Unwrap() error { return e.Err }

// CounterMissingError reports a self-instrumentation counter that is not published at all.
type CounterMissingError struct {
	Name   string
	Labels map[string]string
}

func (e *CounterMissingError) Error() string {
	if len(e.Labels) == 0 {
		return fmt.Sprintf("counter %s is not published by this build", e.Name)
	}
	return fmt.Sprintf("counter %s%v is not published by this build", e.Name, e.Labels)
}

// GatherError reports a failure to read the process's own metrics.
type GatherError struct {
	Err error
}

func (e *GatherError) Error() string { return fmt.Sprintf("gathering process metrics: %v", e.Err) }

func (e *GatherError) Unwrap() error { return e.Err }

// TestDisorderInsideOneRequestIsDiscardedWithoutASound checks Prometheus accepts a request with descending samples in a series and drops the older one.
func TestDisorderInsideOneRequestIsDiscardedWithoutASound(t *testing.T) {
	prometheusURL := prometheusTarget()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err := waitForPrometheus(ctx, prometheusURL, serviceDeadline); err != nil {
		t.Fatalf("prometheus is not available: %v", err)
	}

	run := runID(t)
	at := time.Now().Add(-10 * time.Second)

	// One series, two readings, offered newest first -- which is exactly what a
	// poll that started earlier and finished later hands over.
	request := &prompb.WriteRequest{Timeseries: []prompb.TimeSeries{
		probeSeries(run, "0", []prompb.Sample{
			{Timestamp: at.Add(time.Second).UnixMilli(), Value: 1},
			{Timestamp: at.UnixMilli(), Value: 2},
		}),
	}}

	if err := postRemoteWrite(ctx, remoteWriteURL(prometheusURL), request); err != nil {
		t.Fatalf("the endpoint refused a request whose samples descend within a series (%s): %v\n"+
			"That is what the incident report assumed, and the tests below are written for the behavior measured "+
			"instead. If the receiver now refuses, the intra-request repair is preventing a 400 "+
			"rather than silent loss and this file's reasoning needs revisiting",
			prometheusVersion(ctx, prometheusURL), err)
	}

	stored, err := storedSeries(ctx, prometheusURL, run)
	if err != nil {
		t.Fatalf("counting what the accepted request actually stored: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("%d series stored, want 1", len(stored))
	}
	if got := stored["0"]; got != 1 {
		t.Errorf("the accepted request stored %v of its 2 readings, want 1: the endpoint was "+
			"expected to keep the newer and discard the older in silence", got)
	} else {
		t.Logf("endpoint (%s) accepted the disordered request and stored 1 of its 2 readings: "+
			"the loss is real and the response says nothing about it",
			prometheusVersion(ctx, prometheusURL))
	}
}

// TestOneReadingBehindTheHeadIsRefusedAtItsMeasuredCost checks a reading behind the committed head is refused, with the per-version collateral in refusalCommitsTheRest.
func TestOneReadingBehindTheHeadIsRefusedAtItsMeasuredCost(t *testing.T) {
	prometheusURL := prometheusTarget()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err := waitForPrometheus(ctx, prometheusURL, serviceDeadline); err != nil {
		t.Fatalf("prometheus is not available: %v", err)
	}

	run := runID(t)
	at := time.Now().Add(-10 * time.Second)

	// First, establish a head for one series.
	head := &prompb.WriteRequest{Timeseries: []prompb.TimeSeries{
		probeSeries(run, "head", []prompb.Sample{{Timestamp: at.Add(2 * time.Second).UnixMilli(), Value: 1}}),
	}}
	if err := postRemoteWrite(ctx, remoteWriteURL(prometheusURL), head); err != nil {
		t.Fatalf("establishing the head: %v", err)
	}

	// Then a request of many good, untouched series carrying ONE reading that
	// falls behind that head. The good ones are what the refusal costs.
	const good = 200
	request := &prompb.WriteRequest{}
	for i := 0; i < good; i++ {
		request.Timeseries = append(request.Timeseries, probeSeries(run, strconv.Itoa(i), []prompb.Sample{
			{Timestamp: at.Add(3 * time.Second).UnixMilli(), Value: float64(i)},
		}))
	}
	request.Timeseries = append(request.Timeseries, probeSeries(run, "head", []prompb.Sample{
		{Timestamp: at.UnixMilli(), Value: 2},
	}))

	err := postRemoteWrite(ctx, remoteWriteURL(prometheusURL), request)
	if err == nil {
		t.Fatal("the endpoint accepted a reading older than one it had already committed for that " +
			"series; every ordering assertion in this file assumes it would not, so they now prove nothing")
	}

	refused, ok := err.(*RemoteWriteRefusedError)
	if !ok {
		t.Fatalf("the request failed without being refused: %v", err)
	}
	if refused.StatusCode != http.StatusBadRequest {
		t.Errorf("request answered HTTP %d, want 400: %s", refused.StatusCode, refused.Body)
	}
	t.Logf("endpoint (%s) refused it: HTTP %d %q",
		prometheusVersion(ctx, prometheusURL), refused.StatusCode, refused.Body)

	// What the refusal cost. storedSeries retries, because a query issued the
	// instant after a write can see nothing for a reason that is not "nothing
	// was written" -- and those are the two answers this must not conflate.
	stored, err := storedSeries(ctx, prometheusURL, run)
	if err != nil {
		t.Fatalf("counting what survived the refused request: %v", err)
	}
	if got := stored["head"]; got != 1 {
		t.Errorf("the head series holds %v samples, want 1: the reading behind its head was "+
			"committed, or the head itself was lost (endpoint %s)", got, prometheusVersion(ctx, prometheusURL))
	}
	delete(stored, "head")
	var lost []string
	for i := 0; i < good; i++ {
		if stored[strconv.Itoa(i)] != 1 {
			lost = append(lost, strconv.Itoa(i))
		}
	}
	version := prometheusVersion(ctx, prometheusURL)
	commitsTheRest, measured := refusalCommitsTheRest[version]
	switch {
	case !measured:
		t.Errorf("the endpoint is Prometheus %s, whose cost for a request refused for one reading "+
			"behind its head has not been recorded: it lost %d of %d good series here. Record that "+
			"in refusalCommitsTheRest and docs/metrics/export-ordering.md", version, len(lost), good)
	case commitsTheRest && len(lost) != 0:
		t.Errorf("%d of %d good series were lost with a request refused for ONE reading behind its "+
			"head (endpoint %s, recorded as committing the rest): the receiver now rolls the request "+
			"back, and the incident's cost arithmetic applies again. Lost: %v", len(lost), good, version, lost)
	case !commitsTheRest && len(lost) != good:
		t.Errorf("%d of %d good series survived a request refused for ONE reading behind its head "+
			"(endpoint %s, recorded as rolling the request back): the receiver's behaviour has "+
			"changed; re-measure it and update refusalCommitsTheRest and "+
			"docs/metrics/export-ordering.md", good-len(lost), good, version)
	case commitsTheRest:
		t.Logf("all %d good series in the refused request were committed (endpoint %s): "+
			"one reading behind its head costs that reading alone", good, version)
	default:
		t.Logf("all %d good series in the refused request were lost with it (endpoint %s): "+
			"one reading behind its head costs the whole request, the incident's arithmetic", good, version)
	}
}

// refusalCommitsTheRest records per Prometheus version whether a refused request commits its other readings; unknown versions fail.
var refusalCommitsTheRest = map[string]bool{
	// Prometheus 2.45.0.
	"2.45.0": false,
	// The current pin (test/integration/Dockerfile.prometheus): the whole request is lost.
	"3.14.0": false,
}

// TestOverlappingPollsAreRepairedAndNothingIsLost checks overlapping polls are reordered: repaired_order_total rises, nothing out of order or dropped.
func TestOverlappingPollsAreRepairedAndNothingIsLost(t *testing.T) {
	prometheusURL := prometheusTarget()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err := waitForPrometheus(ctx, prometheusURL, serviceDeadline); err != nil {
		t.Fatalf("prometheus is not available: %v", err)
	}

	run := runID(t)

	// A batch size and flush interval that put the test in charge of when a
	// request is made: one flush per round, so each round's assertions are
	// about that round's request.
	exporter, buildErr := metrics.NewPrometheusExporter(remoteWriteURL(prometheusURL), 1<<20, time.Hour, nil)
	if buildErr != nil {
		t.Fatalf("NewPrometheusExporter: %v", buildErr)
	}
	defer func() { _ = exporter.Close() }()

	before := exportCounters(t)

	// The schedule ends before now, so every sample is in the past by the time
	// it is written. A sample stamped in the future is a different rejection
	// with a different message, and it would be indistinguishable from this one
	// in the counters.
	start := time.Now().Add(-(orderingPeriod*orderingRounds + 2*time.Second))

	for round := 0; round < orderingRounds; round++ {
		base := start.Add(time.Duration(round) * orderingPeriod)

		// The poll that starts first and finishes second.
		straddling := poll(run, base, orderingPollSpread)
		// The poll that starts second and finishes first -- the reconfiguration's
		// replacement worker, or simply a faster cycle.
		overtaking := poll(run, base.Add(orderingOverlap), orderingLateSpread)

		// Exported in completion order, in one request, which orderedSamples repairs.
		if err := exporter.Export(ctx, overtaking); err != nil {
			t.Fatalf("round %d: exporting the overtaking poll: %v", round, err)
		}
		if err := exporter.Export(ctx, straddling); err != nil {
			t.Fatalf("round %d: exporting the straddling poll: %v", round, err)
		}

		if err := exporter.Flush(ctx); err != nil {
			t.Fatalf("round %d: the endpoint refused a request carrying two overlapping polls, "+
				"which is the whole out-of-order failure: %v", round, err)
		}
	}

	after := exportCounters(t)

	// Every series is touched by both polls of every round, so every round
	// hands over exactly orderingSeries readings that are older than one
	// already in the batch.
	wantRepaired := float64(orderingSeries * orderingRounds)
	if got := after.repaired - before.repaired; got != wantRepaired {
		t.Errorf("gonetmon_export_repaired_order_total rose by %v, want %v: "+
			"the reproduction did not produce the disorder it exists to produce, so a pass below "+
			"would be the same absence of evidence this reproduction exists to replace", got, wantRepaired)
	} else {
		t.Logf("produced and repaired %v out-of-order samples across %d rounds of overlapping polls",
			got, orderingRounds)
	}

	if got := after.outOfOrder - before.outOfOrder; got != 0 {
		t.Errorf("gonetmon_export_out_of_order_total rose by %v, want 0: samples the endpoint "+
			"would refuse were produced, which the encoder is supposed to have made impossible "+
			"within a request", got)
	}
	if got := after.droppedTotal - before.droppedTotal; got != 0 {
		t.Errorf("%v sample(s) were dropped, want 0: the disorder was supposed to cost nothing", got)
	}
	if got := after.errors - before.errors; got != 0 {
		t.Errorf("%v flush(es) failed, want 0: the endpoint refused a request", got)
	}

	// The positive half. The counters say the disorder happened and was
	// repaired; this says the readings it was made of are in the database.
	stored, err := storedSeries(ctx, prometheusURL, run)
	if err != nil {
		t.Fatalf("reading back what was written: %v", err)
	}
	if len(stored) != orderingSeries {
		t.Fatalf("%d series stored, want %d: the write was accepted but the data is not there",
			len(stored), orderingSeries)
	}

	// Two polls a round, every round, every series.
	wantSamples := float64(2 * orderingRounds)
	for _, index := range orderedIndexes(stored) {
		if stored[index] != wantSamples {
			t.Errorf("series %s holds %v samples, want %v: readings that reached the exporter "+
				"did not reach the database", index, stored[index], wantSamples)
		}
	}
	t.Logf("all %d series hold all %v readings of both polls in all %d rounds",
		len(stored), wantSamples, orderingRounds)
}

// TestAPollTheEndpointHasMovedPastIsDroppedNotRetriedForever checks a superseded poll is dropped immediately and counted, with no collateral.
func TestAPollTheEndpointHasMovedPastIsDroppedNotRetriedForever(t *testing.T) {
	prometheusURL := prometheusTarget()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err := waitForPrometheus(ctx, prometheusURL, serviceDeadline); err != nil {
		t.Fatalf("prometheus is not available: %v", err)
	}

	run := runID(t)

	exporter, buildErr := metrics.NewPrometheusExporter(remoteWriteURL(prometheusURL), 1<<20, time.Hour, nil)
	if buildErr != nil {
		t.Fatalf("NewPrometheusExporter: %v", buildErr)
	}
	defer func() { _ = exporter.Close() }()

	before := exportCounters(t)

	base := time.Now().Add(-30 * time.Second)

	// The newer poll goes first, in its own request, so the endpoint's head for
	// every series advances past what the older poll holds.
	newer := poll(run, base.Add(orderingOverlap), orderingLateSpread)
	if err := exporter.Export(ctx, newer); err != nil {
		t.Fatalf("exporting the newer poll: %v", err)
	}
	if err := exporter.Flush(ctx); err != nil {
		t.Fatalf("the newer poll was refused: %v", err)
	}

	// Now the poll that was already running when it was superseded. Every
	// reading in it is behind the head the request above established.
	older := poll(run, base, orderingPollSpread)
	if err := exporter.Export(ctx, older); err != nil {
		t.Fatalf("exporting the older poll: %v", err)
	}

	// Flush must return an error naming the discard.
	flushErr := exporter.Flush(ctx)
	if flushErr == nil {
		t.Error("Flush reported success for a batch every sample of which the endpoint had " +
			"already moved past; a discard reported as a delivery is the loss this exporter exists to end")
	} else {
		t.Logf("Flush reported the discard rather than a delivery: %v", flushErr)
	}

	after := exportCounters(t)

	if got := after.superseded - before.superseded; got != float64(len(older)) {
		t.Errorf("gonetmon_export_dropped_samples_total{reason=\"superseded\"} rose by %v, want %d: "+
			"the unlandable readings were not given up on by name", got, len(older))
	}
	if got := after.outOfOrder - before.outOfOrder; got != float64(len(older)) {
		t.Errorf("gonetmon_export_out_of_order_total rose by %v, want %d", got, len(older))
	}

	// An unlandable batch must not make a request, fail a flush or stay in the queue.
	if got := after.errors - before.errors; got != 0 {
		t.Errorf("%v flush(es) failed, want 0: the unlandable readings were OFFERED to the endpoint "+
			"rather than dropped before the request, which is what makes the retry loop possible", got)
	}
	if got := after.rejected - before.rejected; got != 0 {
		t.Errorf("%v sample(s) were dropped as \"rejected\", want 0: readings that could still have "+
			"landed were sent in a request the endpoint refused, and lost with it", got)
	}
	if got := exporter.BufferedSamples(); got != 0 {
		t.Errorf("%d sample(s) are still queued, want 0: an unlandable batch that stays in the queue "+
			"is the self-sustaining backlog the incident produced", got)
	}

	// And the newer poll -- the data that could land -- is intact.
	stored, err := storedSeries(ctx, prometheusURL, run)
	if err != nil {
		t.Fatalf("reading back what was written: %v", err)
	}
	if len(stored) != orderingSeries {
		t.Fatalf("%d series stored, want %d", len(stored), orderingSeries)
	}
	for _, index := range orderedIndexes(stored) {
		if stored[index] != 1 {
			t.Errorf("series %s holds %v samples, want 1: the landable poll was affected by the "+
				"unlandable one", index, stored[index])
		}
	}
}

// poll returns one poll's worth of samples, stamped the way a real poll stamps
// them: per reading as it arrives, spread across however long the poll took.
//
// This is the detail the whole failure rests on. A poll stamped once, at
// completion, could not produce disorder no matter how two polls overlapped.
func poll(run string, start time.Time, spread time.Duration) []*metrics.Sample {
	step := spread / time.Duration(orderingSeries)
	samples := make([]*metrics.Sample, 0, orderingSeries)
	for i := 0; i < orderingSeries; i++ {
		samples = append(samples, &metrics.Sample{
			Name: orderingMetric,
			Labels: map[string]string{
				"run":     run,
				"device":  orderingDevice,
				"ifindex": strconv.Itoa(i),
			},
			Value:     float64(i),
			Timestamp: start.Add(time.Duration(i) * step),
		})
	}
	return samples
}

// orderingDevice is the device label this file's series carry.
const orderingDevice = "ordering-test-router"

// dropReasons lists every sample drop reason, stated independently of the exporter.
var dropReasons = []string{"aged", "depth", "rejected", "superseded", "shutdown"}

// exportCounts is the exporter's self-instrumentation at one instant. Read
// before and after, because the counters are process-global and every test in
// this binary shares them.
type exportCounts struct {
	repaired     float64
	outOfOrder   float64
	droppedTotal float64
	superseded   float64
	rejected     float64
	errors       float64
}

// exportCounters reads the exporter's counters from the process registry; dropped is summed over gonetmon_export_dropped_samples_total reasons.
func exportCounters(t *testing.T) exportCounts {
	t.Helper()

	counts := exportCounts{}
	var err error

	if counts.repaired, err = counterValue("gonetmon_export_repaired_order_total", nil); err != nil {
		t.Fatalf("%v", err)
	}
	if counts.outOfOrder, err = counterValue("gonetmon_export_out_of_order_total", nil); err != nil {
		t.Fatalf("%v", err)
	}
	for _, reason := range dropReasons {
		dropped, err := counterValue("gonetmon_export_dropped_samples_total", map[string]string{"reason": reason})
		if err != nil {
			t.Fatalf("%v", err)
		}
		counts.droppedTotal += dropped

		switch reason {
		case "superseded":
			counts.superseded = dropped
		case "rejected":
			counts.rejected = dropped
		}
	}
	if counts.errors, err = counterValue("gonetmon_exports_total", map[string]string{"status": "error"}); err != nil {
		t.Fatalf("%v", err)
	}
	return counts
}

// counterValue reads one counter child from the default registry; a missing counter is an error.
func counterValue(name string, labels map[string]string) (float64, error) {
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		return 0, &GatherError{Err: err}
	}

	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			published := make(map[string]string, len(metric.GetLabel()))
			for _, pair := range metric.GetLabel() {
				published[pair.GetName()] = pair.GetValue()
			}

			matched := true
			for wantName, wantValue := range labels {
				if published[wantName] != wantValue {
					matched = false
					break
				}
			}
			if matched {
				return metric.GetCounter().GetValue(), nil
			}
		}
	}
	return 0, &CounterMissingError{Name: name, Labels: labels}
}

// remoteWriteURL is the receive endpoint of a Prometheus base URL.
func remoteWriteURL(base string) string { return base + "/api/v1/write" }

// postRemoteWrite sends one request the way the exporter does -- protobuf,
// snappy, the version header -- and reports the endpoint's answer as an error
// when it is not an acceptance.
func postRemoteWrite(ctx context.Context, endpoint string, request *prompb.WriteRequest) error {
	data, err := request.Marshal()
	if err != nil {
		return &RemoteWriteTransportError{URL: endpoint, Stage: "marshal", Err: err}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(snappy.Encode(nil, data)))
	if err != nil {
		return &RemoteWriteTransportError{URL: endpoint, Stage: "build request", Err: err}
	}
	req.Header.Set("Content-Encoding", "snappy")
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return &RemoteWriteTransportError{URL: endpoint, Stage: "request", Err: err}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &RemoteWriteTransportError{URL: endpoint, Stage: "read body", Err: err}
	}

	if resp.StatusCode/100 != 2 {
		return &RemoteWriteRefusedError{URL: endpoint, StatusCode: resp.StatusCode, Body: string(body)}
	}
	return nil
}

// probeSeries is one series of the probe metric, identified by index.
func probeSeries(run, index string, samples []prompb.Sample) prompb.TimeSeries {
	return prompb.TimeSeries{
		// Lexicographical, which the remote-write spec requires of label names.
		Labels: []prompb.Label{
			{Name: "__name__", Value: orderingMetric},
			{Name: "device", Value: orderingDevice},
			{Name: "ifindex", Value: index},
			{Name: "run", Value: run},
		},
		Samples: samples,
	}
}

// storedSeries returns the samples Prometheus holds per series of one run, keyed by ifindex, retrying an empty answer.
func storedSeries(ctx context.Context, prometheusURL, run string) (map[string]float64, error) {
	query := fmt.Sprintf("count_over_time(%s{run=%q}[%s])", orderingMetric, run, queryWindow)
	client := &http.Client{Timeout: 10 * time.Second}

	const attempts = 5
	var lastErr error
	var counts map[string]float64
	for attempt := 1; attempt <= attempts; attempt++ {
		resp, err := queryPrometheus(ctx, client, prometheusURL, query)
		if err != nil {
			lastErr = err
			time.Sleep(probeInterval)
			continue
		}

		counts = make(map[string]float64, len(resp.Data.Result))
		for _, series := range resp.Data.Result {
			if len(series.Value) != 2 {
				return nil, &MetricShapeError{Query: query, Metric: series.Metric, Reason: "sample is not a [timestamp, value] pair"}
			}
			raw, ok := series.Value[1].(string)
			if !ok {
				return nil, &MetricShapeError{Query: query, Metric: series.Metric, Reason: "sample value is not a string"}
			}
			value, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				return nil, &MetricShapeError{Query: query, Metric: series.Metric, Reason: "sample value " + raw + " is not a number"}
			}
			counts[series.Metric["ifindex"]] = value
		}
		if len(counts) > 0 {
			return counts, nil
		}
		lastErr = &MetricsNotFoundError{Query: query, Attempts: attempt, Status: resp.Status}
		time.Sleep(probeInterval)
	}

	// An empty result is a legitimate answer to "what survived a refused
	// request", so it is returned rather than raised -- but only after the
	// retries above have ruled out "not visible yet".
	if counts != nil {
		return counts, nil
	}
	return nil, lastErr
}

// prometheusVersion is the receiver's own version, for failure messages. What
// a remote-write receiver does with a disordered request is a property of its
// version, so a message reporting a surprise has to say which one surprised us.
func prometheusVersion(ctx context.Context, prometheusURL string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, prometheusURL+"/api/v1/status/buildinfo", nil)
	if err != nil {
		return "unknown"
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "unknown"
	}
	defer resp.Body.Close()

	var info struct {
		Data struct {
			Version string `json:"version"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil || info.Data.Version == "" {
		return "unknown"
	}
	return info.Data.Version
}

// runID labels one test's series so no other test -- or another run against a
// Prometheus that kept its data -- can be mistaken for it.
func runID(t *testing.T) string {
	t.Helper()
	return url.QueryEscape(t.Name()) + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

// orderedIndexes returns a map's keys in order, so a failure names the same series
// every run.
func orderedIndexes(counts map[string]float64) []string {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
