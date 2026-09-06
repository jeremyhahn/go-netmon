package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
	ws "github.com/jeremyhahn/go-netmon/internal/webui/websocket"
)

// stubMIBProvider answers the legacy provider-based MIB route.
type stubMIBProvider struct {
	detail *MIBDetail
	err    error
}

func (p *stubMIBProvider) ListMIBs() ([]MIBInfo, error) {
	if p.err != nil {
		return nil, p.err
	}
	return []MIBInfo{{Name: p.detail.Name}}, nil
}

func (p *stubMIBProvider) GetMIB(string) (*MIBDetail, error) {
	if p.err != nil {
		return nil, p.err
	}
	return p.detail, nil
}

func TestNewServerAndClose(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "webui.db")

	s, err := NewServer(Config{
		DBPath: dbPath,
		QueryFunc: func(context.Context, string) ([]ws.MetricValue, error) {
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	// A server built without a logger must still be usable and silent.
	if s.log() == nil {
		t.Fatal("NewServer left the server without a logger")
	}
	if s.log().Enabled(context.Background(), 0) {
		t.Error("a server built without a logger emits records")
	}

	// The routes must be registered, or every request would fall through to
	// the static file handler.
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/devices", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/devices = %d, want 200", rec.Code)
	}

	if err := s.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestNewServerRejectsAnUnusableDatabasePath(t *testing.T) {
	// A path under a regular file cannot become a directory.
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := writeFile(file); err != nil {
		t.Fatalf("writeFile: %v", err)
	}

	s, err := NewServer(Config{DBPath: filepath.Join(file, "webui.db")})
	if err == nil {
		_ = s.Close()
		t.Fatal("NewServer accepted a database path it cannot use")
	}
	if s != nil {
		t.Error("NewServer returned a server alongside an error")
	}
	if !strings.Contains(err.Error(), "storage") {
		t.Errorf("error = %q, want it to name the storage failure", err)
	}
}

// TestStartServesAndShutsDown covers the listener's whole lifetime: it answers
// while the context is live and returns once the context is canceled.
func TestStartServesAndShutsDown(t *testing.T) {
	s := newTestServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- s.Start(ctx, "127.0.0.1:0") }()

	cancel()

	select {
	case err := <-errCh:
		// ListenAndServe returns ErrServerClosed once Shutdown has run, and
		// any other error means it never came up.
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("Start: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Start did not return after its context was canceled")
	}
}

// TestLoadAndRegisterDevices is the startup path that tells the poller about
// everything already in the database.
func TestLoadAndRegisterDevices(t *testing.T) {
	s := newTestServer(t)
	poller := &recordingPoller{}
	s.pollerClient = poller
	ctx := context.Background()

	// With nothing stored there is nothing to register, and that is not an error.
	if err := s.LoadAndRegisterDevices(ctx); err != nil {
		t.Fatalf("LoadAndRegisterDevices on an empty store: %v", err)
	}

	storeDevice(t, s, "bastion", "192.0.2.14")
	storeDevice(t, s, "core-01", "192.0.2.1")

	if err := s.LoadAndRegisterDevices(ctx); err != nil {
		t.Fatalf("LoadAndRegisterDevices: %v", err)
	}
	if got := poller.calls(&poller.registered); len(got) != 2 {
		t.Errorf("registrations = %v, want both devices", got)
	}
}

// TestLoadAndRegisterDevicesSurvivesAFailingPoller keeps one unreachable poller
// from aborting startup.
func TestLoadAndRegisterDevicesSurvivesAFailingPoller(t *testing.T) {
	s := newTestServer(t)
	s.pollerClient = &recordingPoller{err: errors.New("poller is down")}

	storeDevice(t, s, "bastion", "192.0.2.14")

	if err := s.LoadAndRegisterDevices(context.Background()); err != nil {
		t.Errorf("LoadAndRegisterDevices: %v, want startup to continue", err)
	}
}

func TestHandleGetMIB(t *testing.T) {
	s := newTestServer(t)
	s.mibProvider = &stubMIBProvider{detail: &MIBDetail{
		Name:    "IF-MIB",
		Objects: []MIBObject{{OID: "1.3.6.1.2.1.2.2.1.10", Name: "ifInOctets", Pollable: true}},
	}}

	rec := do(t, s, http.MethodGet, "/api/mibs/IF-MIB", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "ifInOctets") {
		t.Errorf("body = %q, want the MIB's objects", rec.Body.String())
	}
}

func TestHandleGetMIBRejectsUnknownModule(t *testing.T) {
	s := newTestServer(t)
	s.mibProvider = &stubMIBProvider{err: errors.New("module not loaded")}

	rec := do(t, s, http.MethodGet, "/api/mibs/NO-SUCH-MIB", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestStoredMIBHandlers(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	mib := &storage.MIB{Name: "IF-MIB", FilePath: "/mibs/IF-MIB.mib", ObjectCount: 12}
	if err := s.storage.CreateMIB(ctx, mib); err != nil {
		t.Fatalf("CreateMIB: %v", err)
	}

	rec := do(t, s, http.MethodGet, "/api/mibs", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "IF-MIB") {
		t.Errorf("list body = %q, want IF-MIB", rec.Body.String())
	}

	rec = do(t, s, http.MethodGet, "/api/mibs/"+itoa(mib.ID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200", rec.Code)
	}

	rec = do(t, s, http.MethodPut, "/api/mibs/"+itoa(mib.ID)+"/tags", `{"tags":["interfaces"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("tags status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "interfaces") {
		t.Errorf("tags body = %q, want the new tag", rec.Body.String())
	}

	rec = do(t, s, http.MethodDelete, "/api/mibs/"+itoa(mib.ID), "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", rec.Code)
	}
	if rec = do(t, s, http.MethodGet, "/api/mibs/"+itoa(mib.ID), ""); rec.Code != http.StatusNotFound {
		t.Errorf("get after delete = %d, want 404", rec.Code)
	}
}

func TestStoredMIBHandlersRejectBadInput(t *testing.T) {
	s := newTestServer(t)

	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
	}{
		{"get an unknown ID", http.MethodGet, "/api/mibs/4242", "", http.StatusNotFound},
		{"delete an unknown ID", http.MethodDelete, "/api/mibs/4242", "", http.StatusInternalServerError},
		{"tag with a malformed body", http.MethodPut, "/api/mibs/1/tags", "{", http.StatusBadRequest},
		{"tag an unknown ID", http.MethodPut, "/api/mibs/4242/tags", `{"tags":["x"]}`, http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, s, tt.method, tt.path, tt.body)
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}

// TestListStoredMIBsFilteredNeedsAMIBStore keeps the "available data" filter
// from answering with an unfiltered list when it cannot reach the metadata
// store, which would tell the operator a MIB has data when nothing checked.
func TestListStoredMIBsFilteredNeedsAMIBStore(t *testing.T) {
	s := newTestServer(t)

	rec := do(t, s, http.MethodGet, "/api/mibs?filter=available", "")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 when the MIB store is unavailable (body %q)", rec.Code, rec.Body.String())
	}
}

func TestHandleGetLearnedTables(t *testing.T) {
	s := newTestServer(t)

	// With no upstream configured the route answers rather than failing, so a
	// deployment without go-netmon still renders.
	rec := do(t, s, http.MethodGet, "/api/learned-tables", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "not available") {
		t.Errorf("body = %q, want it to say the learner is unavailable", rec.Body.String())
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tables":{"ifTable":{}},"stats":{"learned":1}}`))
	}))
	defer upstream.Close()

	s.goNetmonBaseURL = upstream.URL
	rec = do(t, s, http.MethodGet, "/api/learned-tables", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "ifTable") {
		t.Errorf("body = %q, want the proxied payload", rec.Body.String())
	}
}

func TestHandleGetLearnedTablesReportsAFailingUpstream(t *testing.T) {
	s := newTestServer(t)

	t.Run("upstream refuses the request", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer upstream.Close()

		s.goNetmonBaseURL = upstream.URL
		if rec := do(t, s, http.MethodGet, "/api/learned-tables", ""); rec.Code != http.StatusBadGateway {
			t.Errorf("status = %d, want 502", rec.Code)
		}
	})

	t.Run("upstream is not listening", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		s.goNetmonBaseURL = upstream.URL
		upstream.Close()

		if rec := do(t, s, http.MethodGet, "/api/learned-tables", ""); rec.Code != http.StatusBadGateway {
			t.Errorf("status = %d, want 502", rec.Code)
		}
	})
}

func TestMetricsQueryHandler(t *testing.T) {
	stub := &promStub{body: `{
		"status":"success",
		"data":{"resultType":"matrix","result":[
			{"metric":{"__name__":"snmp_if_in","ifindex":"1"},"values":[[1700000000,"1"]]}
		]}
	}`}
	s := newTestServer(t)
	s.promClient = stub.start(t)

	rec := do(t, s, http.MethodPost, "/api/metrics/query", `{"metric":"snmp_if_in","time_range":"6h"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}

	var series []TimeSeriesData
	if err := json.Unmarshal(rec.Body.Bytes(), &series); err != nil {
		t.Fatalf("response: %v", err)
	}
	if len(series) != 1 || series[0].Metric != "snmp_if_in" {
		t.Errorf("series = %+v, want one snmp_if_in series", series)
	}
}

func TestMetricsQueryHandlerTimeRanges(t *testing.T) {
	stub := &promStub{body: `{"status":"success","data":{"resultType":"matrix","result":[]}}`}
	s := newTestServer(t)
	s.promClient = stub.start(t)

	for _, timeRange := range []string{"6h", "1d", "24h", "7d", "1w", "30d", "1M", "365d", "1y", "unrecognized"} {
		rec := do(t, s, http.MethodPost, "/api/metrics/query", `{"metric":"x","time_range":"`+timeRange+`"}`)
		if rec.Code != http.StatusOK {
			t.Errorf("time_range %q: status = %d, want 200", timeRange, rec.Code)
		}
	}

	// An explicit window overrides the named range.
	body := `{"metric":"x","start":"2026-01-01T00:00:00Z","end":"2026-01-02T00:00:00Z"}`
	if rec := do(t, s, http.MethodPost, "/api/metrics/query", body); rec.Code != http.StatusOK {
		t.Errorf("explicit window: status = %d, want 200", rec.Code)
	}
}

func TestMetricsQueryHandlerRejectsBadInput(t *testing.T) {
	s := newTestServer(t)

	tests := []struct {
		name       string
		body       string
		withClient bool
		wantStatus int
	}{
		{"a malformed body", "{", true, http.StatusBadRequest},
		{"no metric", `{"time_range":"6h"}`, true, http.StatusBadRequest},
		{"an unparsable start", `{"metric":"x","start":"yesterday","end":"2026-01-02T00:00:00Z"}`, true, http.StatusBadRequest},
		{"an unparsable end", `{"metric":"x","start":"2026-01-01T00:00:00Z","end":"tomorrow"}`, true, http.StatusBadRequest},
		{"no Prometheus configured", `{"metric":"x","time_range":"6h"}`, false, http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.withClient {
				s.promClient = (&promStub{body: `{"status":"success","data":{"result":[]}}`}).start(t)
			} else {
				s.promClient = nil
			}

			rec := do(t, s, http.MethodPost, "/api/metrics/query", tt.body)
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}

// TestMetricsQueryHandlerReportsAFailingPrometheus keeps a broken query from
// looking like an empty chart.
func TestMetricsQueryHandlerReportsAFailingPrometheus(t *testing.T) {
	s := newTestServer(t)
	s.promClient = (&promStub{body: `{"status":"error","error":"parse error"}`}).start(t)

	rec := do(t, s, http.MethodPost, "/api/metrics/query", `{"metric":"x(","time_range":"6h"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (body %q)", rec.Code, rec.Body.String())
	}
}

// TestMetricsQueryHandlerTracesTheSampleAtDebug covers the sample trace, which
// reaches into the first result's nested maps and so only runs when the records
// will be kept.
func TestMetricsQueryHandlerTracesTheSampleAtDebug(t *testing.T) {
	stub := &promStub{body: `{
		"status":"success",
		"data":{"resultType":"matrix","result":[
			{"metric":{"__name__":"snmp_if_in","ifindex":"1"},"values":[[1700000000,"1"]]}
		]}
	}`}

	for _, level := range []string{"info", "debug"} {
		t.Run(level, func(t *testing.T) {
			var sink safeBuffer
			s := loggingServer(t, level, &sink)
			s.promClient = stub.start(t)

			rec := do(t, s, http.MethodPost, "/api/metrics/query", `{"metric":"snmp_if_in","time_range":"6h"}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}

			traced := strings.Contains(sink.String(), "first raw metric")
			if want := level == "debug"; traced != want {
				t.Errorf("sample trace present = %v, want %v (output %q)", traced, want, sink.String())
			}
		})
	}
}

func TestSystemInfoHandler(t *testing.T) {
	stub := &promStub{body: `{
		"status":"success",
		"data":{"resultType":"vector","result":[
			{"metric":{"__name__":"snmp_system_info","sys_name":"bastion","sys_descr":"PDU"},"value":[1700000000,"1"]}
		]}
	}`}
	s := newTestServer(t)
	s.promClient = stub.start(t)

	rec := do(t, s, http.MethodPost, "/api/metrics/system-info", `{"device":"bastion"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}

	var resp SystemInfoResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response: %v", err)
	}
	if resp.Values["sys_name"] != "bastion" {
		t.Errorf("values = %v, want sys_name bastion", resp.Values)
	}

	// A named field set narrows what comes back.
	rec = do(t, s, http.MethodPost, "/api/metrics/system-info", `{"device":"bastion","fields":["sys_descr"]}`)
	var narrowed SystemInfoResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &narrowed); err != nil {
		t.Fatalf("response: %v", err)
	}
	if len(narrowed.Values) != 1 || narrowed.Values["sys_descr"] != "PDU" {
		t.Errorf("values = %v, want only sys_descr", narrowed.Values)
	}
}

// TestSystemInfoHandlerWithNoData answers with an empty set rather than an
// error, since a device that has not been polled yet is not a failure.
func TestSystemInfoHandlerWithNoData(t *testing.T) {
	s := newTestServer(t)
	s.promClient = (&promStub{body: `{"status":"success","data":{"resultType":"vector","result":[]}}`}).start(t)

	rec := do(t, s, http.MethodPost, "/api/metrics/system-info", `{"device":"bastion"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp SystemInfoResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response: %v", err)
	}
	if len(resp.Values) != 0 {
		t.Errorf("values = %v, want none", resp.Values)
	}
	if resp.Timestamp == 0 {
		t.Error("Timestamp = 0, want the time the question was asked")
	}
}

func TestSystemInfoHandlerRejectsBadInput(t *testing.T) {
	s := newTestServer(t)

	tests := []struct {
		name       string
		body       string
		withClient bool
		wantStatus int
	}{
		{"a malformed body", "{", true, http.StatusBadRequest},
		{"no device", `{}`, true, http.StatusBadRequest},
		{"no Prometheus configured", `{"device":"bastion"}`, false, http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.withClient {
				s.promClient = (&promStub{body: `{"status":"success","data":{"result":[]}}`}).start(t)
			} else {
				s.promClient = nil
			}

			rec := do(t, s, http.MethodPost, "/api/metrics/system-info", tt.body)
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestSystemInfoHandlerReportsAFailingPrometheus(t *testing.T) {
	s := newTestServer(t)
	s.promClient = (&promStub{body: `{"status":"error","error":"parse error"}`}).start(t)

	rec := do(t, s, http.MethodPost, "/api/metrics/system-info", `{"device":"bastion"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestQueryMetricsHandlerReportsAFailingQuery(t *testing.T) {
	s := newTestServer(t)
	s.queryFunc = func(context.Context, string) ([]ws.MetricValue, error) {
		return nil, errors.New("prometheus is down")
	}
	s.routes()

	tests := []struct {
		name string
		body string
	}{
		{"a plain PromQL query", `{"query":"snmp_if_in"}`},
		{"a label_values query", `{"query":"label_values({device=\"bastion\"}, ifindex)"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, s, http.MethodPost, "/api/metrics/instant", tt.body)
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500 (body %q)", rec.Code, rec.Body.String())
			}
		})
	}

	if rec := do(t, s, http.MethodPost, "/api/metrics/instant", "{"); rec.Code != http.StatusBadRequest {
		t.Errorf("malformed body status = %d, want 400", rec.Code)
	}
}

// writeFile creates an empty regular file at path.
func writeFile(path string) error {
	return os.WriteFile(path, nil, 0o600)
}

// TestWebSocketUpgradeRejectsAPlainRequest keeps a non-WebSocket request from
// being upgraded.
func TestWebSocketUpgradeRejectsAPlainRequest(t *testing.T) {
	s := newTestServer(t)

	rec := do(t, s, http.MethodGet, "/ws", "")
	if rec.Code == http.StatusSwitchingProtocols {
		t.Errorf("status = %d, want a plain GET to be refused", rec.Code)
	}
}
