package cli

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// testSeries is the range-query payload the fake API serves, deliberately in
// descending timestamp order so the renderer cannot rely on server ordering.
//
// It reproduces the server's actual encoding — samples are positional
// [timestamp, value] arrays, not objects — so these tests exercise the same
// decoding path a live server drives.
func testSeries() []map[string]any {
	now := time.Now()
	return []map[string]any{
		{
			"metric": "snmp_ifhcinoctets",
			"labels": map[string]string{"device": "core-01", "ifindex": "2"},
			"values": [][]any{
				{now.Add(-30 * time.Second).Unix(), 4096.0},
				{now.Add(-5 * time.Minute).Unix(), 1024.0},
			},
		},
		{
			"metric": "snmp_ifhcinoctets",
			"labels": map[string]string{"device": "core-01", "ifindex": "1"},
			"values": [][]any{
				{now.Add(-90 * time.Second).Unix(), 12.5},
			},
		},
	}
}

// testInstantValues is the instant-query payload the fake API serves, in the
// server's real envelope: a "metrics" object with Unix timestamps.
func testInstantValues(values ...map[string]any) map[string]any {
	return map[string]any{"metrics": values}
}

// instantValue builds one element of an instant-query response.
func instantValue(labels map[string]string, value float64) map[string]any {
	return map[string]any{
		"metric":    labels,
		"value":     value,
		"timestamp": time.Now().Unix(),
	}
}

func TestMetricsQueryRendersLatestSample(t *testing.T) {
	var request struct {
		Metric    string `json:"metric"`
		TimeRange string `json:"time_range"`
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/metrics/query", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode query request: %v", err)
		}
		respondJSON(t, w, testSeries())
	})

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newMetricsCommand(env),
		"query", "snmp_ifhcinoctets", "--range", "1d"); err != nil {
		t.Fatalf("query: %v", err)
	}

	if request.Metric != "snmp_ifhcinoctets" || request.TimeRange != "1d" {
		t.Errorf("sent metric %q range %q", request.Metric, request.TimeRange)
	}

	got := out.String()
	for _, want := range []string{
		"METRIC", "LABELS", "SAMPLES", "LATEST", "TIME",
		`device="core-01",ifindex="2"`,
		"4096", // the newest sample wins over the older, smaller one
		"ago",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}

	// Series are sorted by labels, so ifindex 1 precedes ifindex 2.
	if strings.Index(got, `ifindex="1"`) > strings.Index(got, `ifindex="2"`) {
		t.Errorf("series not sorted by labels:\n%s", got)
	}
}

func TestMetricsQueryRejectsUnknownRange(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s for an invalid range", r.Method, r.URL.Path)
	})

	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newMetricsCommand(env), "query", "snmp_x", "--range", "2h")
	if err == nil {
		t.Fatal("expected an error for --range 2h")
	}

	var rangeErr *InvalidRangeError
	if !errors.As(err, &rangeErr) {
		t.Fatalf("got %T, want *InvalidRangeError", err)
	}
	if rangeErr.Value != "2h" {
		t.Errorf("Value = %q, want 2h", rangeErr.Value)
	}
	for _, accepted := range metricRanges {
		if !strings.Contains(err.Error(), accepted) {
			t.Errorf("error does not list %q: %v", accepted, err)
		}
	}
}

func TestMetricsQueryServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/metrics/query", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "prometheus unreachable", http.StatusInternalServerError)
	})

	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newMetricsCommand(env), "query", "snmp_x")
	if err == nil {
		t.Fatal("expected an error from a 500 response")
	}

	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("got %T, want *client.APIError", err)
	}
	if apiErr.StatusCode != http.StatusInternalServerError {
		t.Errorf("StatusCode = %d, want 500", apiErr.StatusCode)
	}
}

func TestMetricsQueryEmptyResult(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/metrics/query", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, []map[string]any{})
	})

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newMetricsCommand(env), "query", "snmp_missing"); err != nil {
		t.Fatalf("query: %v", err)
	}
	if !strings.Contains(out.String(), "No samples") {
		t.Errorf("expected an empty-result notice, got:\n%s", out)
	}
}

func TestMetricsQueryJSONDumpsRawSeries(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/metrics/query", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, testSeries())
	})

	env, out := newFakeEnv(t, mux)
	env.Opts.JSON = true
	if err := runCommand(t, newMetricsCommand(env), "query", "snmp_ifhcinoctets"); err != nil {
		t.Fatalf("query: %v", err)
	}

	page := decodePage[client.TimeSeries](t, out.Bytes())
	if len(page.Items) != 2 {
		t.Fatalf("decoded %d series, want 2", len(page.Items))
	}
	if page.Total != 2 || page.Truncated {
		t.Errorf("counts = %+v, want a complete 2-series listing", page.listCounts)
	}
}

func TestMetricsInstantRendersSeries(t *testing.T) {
	var request struct {
		Query string `json:"query"`
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/metrics/instant", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode instant request: %v", err)
		}
		respondJSON(t, w, testInstantValues(instantValue(map[string]string{
			"__name__": "snmp_ifoperstatus",
			"device":   "core-01",
			"ifindex":  "1",
		}, 1)))
	})

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newMetricsCommand(env),
		"instant", `snmp_ifoperstatus{device="core-01"}`); err != nil {
		t.Fatalf("instant: %v", err)
	}

	if request.Query != `snmp_ifoperstatus{device="core-01"}` {
		t.Errorf("forwarded query %q", request.Query)
	}
	got := out.String()
	for _, want := range []string{
		"LABELS", "VALUE", "TIME",
		`snmp_ifoperstatus{device="core-01",ifindex="1"}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestMetricsInstantEmptyAndServerError(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/metrics/instant", func(w http.ResponseWriter, _ *http.Request) {
			respondJSON(t, w, testInstantValues())
		})

		env, out := newFakeEnv(t, mux)
		if err := runCommand(t, newMetricsCommand(env), "instant", "up"); err != nil {
			t.Fatalf("instant: %v", err)
		}
		if !strings.Contains(out.String(), "No series matched") {
			t.Errorf("expected a no-match notice, got:\n%s", out)
		}
	})

	t.Run("rejected query", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/metrics/instant", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "parse error: unexpected }", http.StatusInternalServerError)
		})

		env, _ := newFakeEnv(t, mux)
		err := runCommand(t, newMetricsCommand(env), "instant", "}")
		if err == nil {
			t.Fatal("expected an error for a query the server rejects")
		}
		if !strings.Contains(err.Error(), "parse error") {
			t.Errorf("error lost the server's explanation: %v", err)
		}
	})
}

func TestMetricsSystemUsesCanonicalDeviceName(t *testing.T) {
	var request struct {
		Device string   `json:"device"`
		Fields []string `json:"fields"`
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/devices", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"devices": []client.Device{testDeviceFixture()}})
	})
	mux.HandleFunc("/api/devices/7", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, testDeviceFixture())
	})
	mux.HandleFunc("/api/metrics/system-info", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode system-info request: %v", err)
		}
		respondJSON(t, w, client.SystemInfo{
			Device:    "core-01",
			Timestamp: time.Now().Unix(),
			Values: map[string]string{
				"sys_location": "rack 4",
				"sys_name":     "core-01",
			},
		})
	})

	env, out := newFakeEnv(t, mux)
	// An upper-case reference must still query the stored name, because that
	// is the name the exporter labels samples with.
	if err := runCommand(t, newMetricsCommand(env),
		"system", "CORE-01", "--fields", "sys_name,sys_location"); err != nil {
		t.Fatalf("system: %v", err)
	}

	if request.Device != "core-01" {
		t.Errorf("queried device %q, want the canonical core-01", request.Device)
	}
	if len(request.Fields) != 2 || request.Fields[0] != "sys_name" {
		t.Errorf("forwarded fields %v", request.Fields)
	}

	got := out.String()
	if !strings.Contains(got, "sys_name") || !strings.Contains(got, "rack 4") {
		t.Errorf("output missing system values:\n%s", got)
	}
	// sys_name is listed before sys_location whatever order the map yields.
	if strings.Index(got, "sys_name") > strings.Index(got, "sys_location") {
		t.Errorf("system fields not in display order:\n%s", got)
	}
}

func TestMetricsSystemUnknownDevice(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/devices", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"devices": []client.Device{}})
	})
	mux.HandleFunc("/api/metrics/system-info", func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("system-info must not be queried for an unresolvable device")
	})

	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newMetricsCommand(env), "system", "ghost")
	if err == nil {
		t.Fatal("expected an error for an unknown device")
	}

	var notFound *client.NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("got %T, want *client.NotFoundError", err)
	}
}

func TestMetricsSystemWithoutCachedData(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/devices/7", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, testDeviceFixture())
	})
	mux.HandleFunc("/api/metrics/system-info", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]string{})
	})

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newMetricsCommand(env), "system", "7"); err != nil {
		t.Fatalf("system: %v", err)
	}
	if !strings.Contains(out.String(), "No system information cached") {
		t.Errorf("expected a no-data notice, got:\n%s", out)
	}
}

func TestFormatValueRendering(t *testing.T) {
	cases := []struct {
		value float64
		want  string
	}{
		{value: 0, want: "0"},
		{value: 4096, want: "4096"},
		{value: -17, want: "-17"},
		{value: 12.5, want: "12.500"},
		{value: math.NaN(), want: "NaN"},
		{value: math.Inf(1), want: "+Inf"},
	}

	for _, c := range cases {
		if got := formatValue(c.value); got != c.want {
			t.Errorf("formatValue(%v) = %q, want %q", c.value, got, c.want)
		}
	}
}

func TestFormatSeriesAndLabels(t *testing.T) {
	labels := map[string]string{
		"__name__": "snmp_ifinoctets",
		"ifindex":  "3",
		"device":   "core-01",
	}
	if got, want := formatSeries(labels), `snmp_ifinoctets{device="core-01",ifindex="3"}`; got != want {
		t.Errorf("formatSeries = %q, want %q", got, want)
	}

	// Degenerate label sets must not produce stray braces or panic.
	if got := formatSeries(nil); got != "" {
		t.Errorf("formatSeries(nil) = %q, want empty", got)
	}
	if got := formatSeries(map[string]string{"__name__": "up"}); got != "up" {
		t.Errorf("formatSeries(name only) = %q, want up", got)
	}
	if got := formatLabels(map[string]string{"__name__": "up"}); got != "" {
		t.Errorf("formatLabels kept the metric name: %q", got)
	}
}

func TestLatestSampleHandlesEmptySeries(t *testing.T) {
	if _, ok := latestSample(nil); ok {
		t.Error("latestSample(nil) reported a sample")
	}

	now := time.Now()
	samples := []client.MetricSample{
		{Timestamp: now.Add(-time.Minute), Value: 1},
		{Timestamp: now, Value: 2},
		{Timestamp: now.Add(-2 * time.Minute), Value: 3},
	}
	got, ok := latestSample(samples)
	if !ok || got.Value != 2 {
		t.Errorf("latestSample = %+v (ok=%v), want the sample valued 2", got, ok)
	}
}

func TestSortSeriesOrdersByMetricThenLabels(t *testing.T) {
	series := []client.TimeSeries{
		{Metric: "snmp_ifoutoctets", Labels: map[string]string{"ifindex": "1"}},
		{Metric: "snmp_ifinoctets", Labels: map[string]string{"ifindex": "2"}},
		{Metric: "snmp_ifinoctets", Labels: map[string]string{"ifindex": "1"}},
	}
	sortSeries(series)

	got := []string{
		series[0].Metric + series[0].Labels["ifindex"],
		series[1].Metric + series[1].Labels["ifindex"],
		series[2].Metric + series[2].Labels["ifindex"],
	}
	want := []string{"snmp_ifinoctets1", "snmp_ifinoctets2", "snmp_ifoutoctets1"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sortSeries = %v, want %v", got, want)
		}
	}

	// An empty set must not panic.
	sortSeries(nil)
}
