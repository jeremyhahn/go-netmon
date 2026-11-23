package client

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"testing"
	"time"
)

// The metrics endpoints are the only ones whose response shape differs from the
// SDK's: the server encodes each range sample as a positional [timestamp,
// value] array in the Prometheus style, and reports instant timestamps as bare
// Unix numbers. The conversion in between is where a graph silently ends up
// with points in 1970, or with a series that quietly drops half its samples.

func TestQueryRangeDecodesPositionalSamples(t *testing.T) {
	const body = `[
	  {
	    "metric": "ifInOctets",
	    "labels": {"device": "core-01", "ifIndex": "1"},
	    "values": [[1700000000, 1234], [1700000060, "2345"], [1700000120, 3456.5]]
	  }
	]`

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/metrics/query" {
			t.Errorf("path = %q, want /api/metrics/query", r.URL.Path)
		}
		var sent map[string]string
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if sent["metric"] != "ifInOctets" || sent["time_range"] != "6h" {
			t.Errorf("request body = %v, want the metric and time range", sent)
		}
		_, _ = w.Write([]byte(body))
	})

	series, err := c.QueryRange(context.Background(), "ifInOctets", "6h")
	if err != nil {
		t.Fatalf("QueryRange: %v", err)
	}
	if len(series) != 1 {
		t.Fatalf("got %d series, want 1", len(series))
	}

	got := series[0]
	if got.Metric != "ifInOctets" {
		t.Errorf("Metric = %q, want %q", got.Metric, "ifInOctets")
	}
	if got.Labels["device"] != "core-01" {
		t.Errorf("Labels = %v, want the device label preserved", got.Labels)
	}
	if len(got.Values) != 3 {
		t.Fatalf("got %d samples, want 3", len(got.Values))
	}

	// A numeric and a string-encoded value must decode identically; the server
	// uses both forms depending on which backend answered.
	want := []float64{1234, 2345, 3456.5}
	for i, sample := range got.Values {
		if sample.Value != want[i] {
			t.Errorf("sample %d value = %v, want %v", i, sample.Value, want[i])
		}
	}
	if !got.Values[0].Timestamp.Equal(time.Unix(1700000000, 0)) {
		t.Errorf("sample 0 timestamp = %v, want %v", got.Values[0].Timestamp, time.Unix(1700000000, 0))
	}
}

// TestQueryRangeSkipsMalformedSamples covers the guard against a truncated or
// malformed pair. Dropping the bad sample keeps the rest of the series usable;
// letting it through would put a zero-valued point at the epoch in the middle
// of a graph.
func TestQueryRangeSkipsMalformedSamples(t *testing.T) {
	const body = `[
	  {
	    "metric": "ifInOctets",
	    "values": [
	      [1700000000],
	      [1700000060, 10],
	      [1700000120, "not-a-number"],
	      ["not-a-time", 20],
	      [1700000180, 30, 40]
	    ]
	  }
	]`

	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	})

	series, err := c.QueryRange(context.Background(), "ifInOctets", "6h")
	if err != nil {
		t.Fatalf("QueryRange: %v", err)
	}
	if len(series) != 1 {
		t.Fatalf("got %d series, want 1", len(series))
	}
	if len(series[0].Values) != 1 {
		t.Fatalf("got %d samples, want only the well-formed one: %+v", len(series[0].Values), series[0].Values)
	}
	if series[0].Values[0].Value != 10 {
		t.Errorf("value = %v, want 10", series[0].Values[0].Value)
	}
}

func TestQueryRangeWithNoSeries(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	})

	series, err := c.QueryRange(context.Background(), "ifInOctets", "6h")
	if err != nil {
		t.Fatalf("QueryRange: %v", err)
	}
	if len(series) != 0 {
		t.Errorf("got %d series, want none", len(series))
	}
}

func TestQueryInstantDecodesTimestamps(t *testing.T) {
	const body = `{"metrics":[
	  {"metric":{"__name__":"up","device":"core-01"},"value":1,"timestamp":1700000000},
	  {"metric":{"__name__":"up","device":"core-02"},"value":0,"timestamp":0}
	]}`

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/metrics/instant" {
			t.Errorf("path = %q, want /api/metrics/instant", r.URL.Path)
		}
		_, _ = w.Write([]byte(body))
	})

	values, err := c.QueryInstant(context.Background(), "up")
	if err != nil {
		t.Fatalf("QueryInstant: %v", err)
	}
	if len(values) != 2 {
		t.Fatalf("got %d values, want 2", len(values))
	}

	if values[0].Value != 1 {
		t.Errorf("value = %v, want 1", values[0].Value)
	}
	if values[0].Metric["device"] != "core-01" {
		t.Errorf("Metric = %v, want the device label preserved", values[0].Metric)
	}
	if !values[0].Time.Equal(time.Unix(1700000000, 0)) {
		t.Errorf("Time = %v, want %v", values[0].Time, time.Unix(1700000000, 0))
	}
	if !values[1].Time.Equal(time.Unix(0, 0)) {
		t.Errorf("a zero timestamp became %v, want the epoch", values[1].Time)
	}
}

// TestUnixSeconds covers the heuristic that tells a seconds timestamp from a
// milliseconds one. The server emits both, and reading milliseconds as seconds
// puts a sample five thousand years in the future, which silently empties any
// graph with a time window.
func TestUnixSeconds(t *testing.T) {
	tests := []struct {
		name string
		ts   float64
		want time.Time
	}{
		{name: "seconds", ts: 1700000000, want: time.Unix(1700000000, 0)},
		{name: "milliseconds", ts: 1700000000000, want: time.UnixMilli(1700000000000)},
		{name: "epoch", ts: 0, want: time.Unix(0, 0)},
		{name: "fractional seconds", ts: 1700000000.5, want: time.Unix(1700000000, int64(500*time.Millisecond))},
		{name: "negative", ts: -1, want: time.Unix(-1, 0)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := unixSeconds(tt.ts)
			if !got.Equal(tt.want) {
				t.Errorf("unixSeconds(%v) = %v, want %v", tt.ts, got, tt.want)
			}
		})
	}
}

// TestUnixSecondsThresholdBoundary pins where the seconds/milliseconds decision
// flips, since a value just either side of it is interpreted very differently.
func TestUnixSecondsThresholdBoundary(t *testing.T) {
	const threshold = 1e11

	if got := unixSeconds(threshold); !got.Equal(time.Unix(threshold, 0)) {
		t.Errorf("a value at the threshold was read as milliseconds: %v", got)
	}
	if got := unixSeconds(threshold + 1); !got.Equal(time.UnixMilli(threshold + 1)) {
		t.Errorf("a value past the threshold was read as seconds: %v", got)
	}
}

// TestToFloat covers every JSON number shape the server has been seen to emit,
// plus the shapes that must be rejected rather than silently read as zero.
func TestToFloat(t *testing.T) {
	accepted := []struct {
		name  string
		value any
		want  float64
	}{
		{name: "float64", value: float64(1.5), want: 1.5},
		{name: "int64", value: int64(7), want: 7},
		{name: "json.Number", value: json.Number("42.25"), want: 42.25},
		{name: "string", value: "8.5", want: 8.5},
		{name: "string in exponent form", value: "1e3", want: 1000},
	}
	for _, tt := range accepted {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := toFloat(tt.value)
			if !ok {
				t.Fatalf("toFloat(%v) rejected a valid number", tt.value)
			}
			if got != tt.want {
				t.Errorf("toFloat(%v) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}

	rejected := map[string]any{
		"nil":                nil,
		"bool":               true,
		"unparseable string": "not-a-number",
		"malformed number":   json.Number("not-a-number"),
		"object":             map[string]any{"value": 1},
		"array":              []any{1},
	}
	for name, value := range rejected {
		t.Run("rejects "+name, func(t *testing.T) {
			if _, ok := toFloat(value); ok {
				t.Errorf("toFloat(%v) accepted a value that is not a number", value)
			}
		})
	}
}

// TestToFloatPreservesSpecialValues checks that NaN and infinity survive rather
// than being rejected: a counter reset can legitimately produce them, and
// silently dropping the sample hides the reset.
func TestToFloatPreservesSpecialValues(t *testing.T) {
	nan, ok := toFloat(math.NaN())
	if !ok {
		t.Error("toFloat rejected NaN")
	}
	if !math.IsNaN(nan) {
		t.Errorf("toFloat(NaN) = %v, want NaN", nan)
	}

	inf, ok := toFloat(math.Inf(1))
	if !ok {
		t.Error("toFloat rejected +Inf")
	}
	if !math.IsInf(inf, 1) {
		t.Errorf("toFloat(+Inf) = %v, want +Inf", inf)
	}
}
