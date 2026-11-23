package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/mux"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/logging"
	ws "github.com/jeremyhahn/go-netmon/internal/webui/websocket"
)

// safeBuffer is a bytes.Buffer safe for a handler goroutine to write to while
// the test reads it.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// loggingServer builds a Server whose diagnostics land in sink at the given
// configured level.
func loggingServer(t *testing.T, level string, sink *safeBuffer) *Server {
	t.Helper()

	logger, err := logging.NewWithWriter(
		config.LoggingConfig{Level: level, Format: "json", Output: "stdout"}, sink)
	if err != nil {
		t.Fatalf("NewWithWriter: %v", err)
	}

	s := &Server{
		router: mux.NewRouter(),
		logger: logger,
		hub:    ws.NewHub(nil, logger),
		queryFunc: func(_ context.Context, _ string) ([]ws.MetricValue, error) {
			return []ws.MetricValue{
				{Metric: map[string]string{"__name__": "snmp_ifname_info", "ifindex": "1", "value": "Gi0/0"}, Value: 1},
			}, nil
		},
	}
	s.routes()
	return s
}

// TestDebugTracingIsSilentAtInfo is the guarantee the whole log change rests
// on: the query path used to log a line per query and a line per metric
// unconditionally, several hundred lines a second with a dashboard open.
func TestDebugTracingIsSilentAtInfo(t *testing.T) {
	tests := []struct {
		name      string
		level     string
		wantTrace bool
	}{
		{"info is silent", "info", false},
		{"debug traces the query", "debug", true},
	}

	body := `{"query":"label_values({device=\"bastion\"}, ifindex)"}`

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sink safeBuffer
			s := loggingServer(t, tt.level, &sink)

			rec := httptest.NewRecorder()
			s.router.ServeHTTP(rec,
				httptest.NewRequest(http.MethodPost, "/api/metrics/instant", strings.NewReader(body)))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
			}

			out := sink.String()
			traced := strings.Contains(out, "label_values query detected")
			if traced != tt.wantTrace {
				t.Fatalf("tracing present = %v, want %v (output %q)", traced, tt.wantTrace, out)
			}

			if !tt.wantTrace {
				if out != "" {
					t.Errorf("info level produced %q, want nothing on the query path", out)
				}
				return
			}

			// At debug the records must be structured, not preformatted text.
			var record map[string]any
			first, _, _ := strings.Cut(out, "\n")
			if err := json.Unmarshal([]byte(first), &record); err != nil {
				t.Fatalf("debug record is not JSON (%q): %v", first, err)
			}
			if record["level"] != "DEBUG" {
				t.Errorf("record level = %v, want DEBUG", record["level"])
			}
			if !strings.Contains(out, "instant query returned") {
				t.Errorf("debug output %q is missing the result trace", out)
			}
		})
	}
}

// TestConvertToTimeSeriesTracingIsGated covers the helper that logs a fallback
// per malformed metric; it takes the logger as an argument rather than reaching
// for a package-level one.
func TestConvertToTimeSeriesTracingIsGated(t *testing.T) {
	// A metric whose values are in none of the shapes the converter knows,
	// which is the branch that logs the map's keys.
	metrics := []interface{}{
		map[string]interface{}{"metric": map[string]string{"__name__": "x"}, "unexpected": 1},
		"not a map at all",
	}

	tests := []struct {
		name      string
		level     string
		wantTrace bool
	}{
		{"info is silent", "info", false},
		{"debug reports the shapes it fell back on", "debug", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sink safeBuffer
			logger, err := logging.NewWithWriter(
				config.LoggingConfig{Level: tt.level, Format: "json", Output: "stdout"}, &sink)
			if err != nil {
				t.Fatalf("NewWithWriter: %v", err)
			}

			series := convertToTimeSeries(metrics, logger)
			if len(series) != 1 {
				t.Fatalf("series = %d, want the one well-formed metric", len(series))
			}

			traced := strings.Contains(sink.String(), "no values found in metric")
			if traced != tt.wantTrace {
				t.Errorf("tracing present = %v, want %v (output %q)", traced, tt.wantTrace, sink.String())
			}
		})
	}
}

// TestConvertToTimeSeriesWithoutALogger keeps the converter usable by callers
// that have none.
func TestConvertToTimeSeriesWithoutALogger(t *testing.T) {
	series := convertToTimeSeries([]interface{}{
		map[string]interface{}{
			"metric": map[string]string{"__name__": "snmp_if_in", "ifindex": "1"},
			"values": [][]interface{}{{1.0, "5"}},
		},
	}, nil)

	if len(series) != 1 {
		t.Fatalf("series = %d, want 1", len(series))
	}
	if series[0].Metric != "snmp_if_in" {
		t.Errorf("Metric = %q, want snmp_if_in", series[0].Metric)
	}
	if series[0].Labels["ifindex"] != "1" {
		t.Errorf("Labels = %v, want ifindex preserved", series[0].Labels)
	}
}

// TestServerLoggerDefaultsToDiscard keeps a Server built without a logger from
// panicking at a call site, and keeps it from emitting.
func TestServerLoggerDefaultsToDiscard(t *testing.T) {
	s := &Server{}

	if s.log() == nil {
		t.Fatal("log() returned nil")
	}
	if s.log().Enabled(context.Background(), slog.LevelError) {
		t.Error("a Server built without a logger emits records")
	}
	// A record on the default logger must not panic.
	s.log().DebugContext(context.Background(), "dropped", "device", "bastion")
}
