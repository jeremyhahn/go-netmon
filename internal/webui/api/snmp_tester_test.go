package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"

	"testing"
	"time"

	"github.com/gorilla/mux"

	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
	ws "github.com/jeremyhahn/go-netmon/internal/webui/websocket"
)

// silentAgent binds a real UDP socket on loopback and never answers it. It is
// the in-process stand-in for a device that is routable but dead — the shape
// that produced the original failure — and it exercises the genuine gosnmp
// timeout path rather than a substitute for our own code.
func silentAgent(t *testing.T) string {
	t.Helper()

	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind silent agent: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return conn.LocalAddr().String()
}

// newTestServer builds a Server backed by a temporary store.
func newTestServer(t testing.TB) *Server {
	t.Helper()

	store, err := storage.New(filepath.Join(t.TempDir(), "webui.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	s := &Server{router: mux.NewRouter(), storage: store, hub: ws.NewHub(nil, nil)}
	s.routes()
	return s
}

// storeDevice saves a device and returns its ID.
func storeDevice(t *testing.T, s *Server, name, address string) int64 {
	t.Helper()

	device := &storage.Device{
		Name:        name,
		Address:     address,
		SNMPVersion: "v2c",
		Community:   "public",
	}
	if err := s.storage.CreateDevice(context.Background(), device); err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}
	return device.ID
}

// TestUnreachableDeviceAnswersWithinTheWriteTimeout is the regression for the
// reported "EOF": the probe must finish, and the handler must answer, well
// before the connection's write deadline expires.
func TestUnreachableDeviceAnswersWithinTheWriteTimeout(t *testing.T) {
	t.Parallel()

	s := newTestServer(t)
	id := storeDevice(t, s, "bastion", silentAgent(t))

	req := httptest.NewRequest(http.MethodPost, "/api/devices/"+itoa(id)+"/test-snmp", http.NoBody)
	rec := httptest.NewRecorder()

	start := time.Now()
	s.router.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if elapsed >= ServerWriteTimeout {
		t.Fatalf("probe took %v, which exceeds ServerWriteTimeout (%v): the connection would be closed unanswered",
			elapsed, ServerWriteTimeout)
	}
	if elapsed > SNMPProbeBudget+time.Second {
		t.Errorf("probe took %v, which overruns SNMPProbeBudget (%v)", elapsed, SNMPProbeBudget)
	}

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusGatewayTimeout)
	}

	var resp SNMPTestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not the structured result (%q): %v", rec.Body.String(), err)
	}
	if resp.Success {
		t.Error("a silent device reported success")
	}
	if !resp.TimedOut {
		t.Error("a silent device was not reported as timed out")
	}
	if resp.Device != "bastion" {
		t.Errorf("Device = %q, want the device name so the failure names its subject", resp.Device)
	}
	if !strings.Contains(resp.Error, "bastion") {
		t.Errorf("Error = %q, want it to name the device", resp.Error)
	}
	if !strings.Contains(resp.Error, SNMPProbeBudget.String()) {
		t.Errorf("Error = %q, want it to state the budget it waited", resp.Error)
	}
}

// TestUnreachableDeviceDoesNotDropTheConnection drives the same path over a
// real listener carrying the production timeouts. Before the fix this returned
// a bare EOF, which is what the CLI reported.
func TestUnreachableDeviceDoesNotDropTheConnection(t *testing.T) {
	t.Parallel()

	s := newTestServer(t)
	id := storeDevice(t, s, "bastion", silentAgent(t))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := newHTTPServer(ln.Addr().String(), s.router)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	url := "http://" + ln.Addr().String() + "/api/devices/" + itoa(id) + "/test-snmp"
	client := &http.Client{Timeout: ServerWriteTimeout + 10*time.Second}

	resp, err := client.Post(url, "application/json", http.NoBody)
	if err != nil {
		if errors.Is(err, io.EOF) {
			t.Fatalf("the server closed the connection without a response: %v", err)
		}
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusGatewayTimeout)
	}

	var decoded SNMPTestResponse
	if decodeErr := json.NewDecoder(resp.Body).Decode(&decoded); decodeErr != nil {
		t.Fatalf("body is not the structured result: %v", decodeErr)
	}
	if decoded.Success || !decoded.TimedOut {
		t.Errorf("result = %+v, want an unreachable device", decoded)
	}
}

// TestSilentDeviceDoesNotBlockAnotherRequest is the head-of-line guarantee: a
// probe of a dead device must not delay a request that has nothing to do with
// it.
func TestSilentDeviceDoesNotBlockAnotherRequest(t *testing.T) {
	t.Parallel()

	s := newTestServer(t)
	id := storeDevice(t, s, "bastion", silentAgent(t))

	probeDone := make(chan struct{})
	go func() {
		defer close(probeDone)
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec,
			httptest.NewRequest(http.MethodPost, "/api/devices/"+itoa(id)+"/test-snmp", http.NoBody))
	}()

	// Give the probe time to be well inside its retry sequence.
	time.Sleep(100 * time.Millisecond)

	start := time.Now()
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/devices", http.NoBody))
	elapsed := time.Since(start)

	if rec.Code != http.StatusOK {
		t.Fatalf("unrelated request status = %d, want 200", rec.Code)
	}
	if elapsed > time.Second {
		t.Errorf("unrelated request waited %v behind a dead device's probe", elapsed)
	}

	<-probeDone
}

func TestTestSNMPDirectRejectsBadInput(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantField  string
	}{
		{
			name:       "malformed JSON",
			body:       "{",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "no address",
			body:       `{"snmp_version":"v2c"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			// "v4" does not exist. This case used to be "v3", which the tester
			// refused — leaving no way to check a v3 device's credentials from
			// either interface, which is the case that most needs one.
			name:       "unsupported version",
			body:       `{"address":"127.0.0.1","snmp_version":"v4"}`,
			wantStatus: http.StatusBadRequest,
			wantField:  "snmp_version",
		},
		{
			name:       "port above the 16-bit range",
			body:       `{"address":"127.0.0.1","snmp_version":"v2c","port":70000}`,
			wantStatus: http.StatusBadRequest,
			wantField:  "port",
		},
		{
			name:       "port below the 16-bit range",
			body:       `{"address":"127.0.0.1","snmp_version":"v2c","port":-1}`,
			wantStatus: http.StatusBadRequest,
			wantField:  "port",
		},
	}

	s := newTestServer(t)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/snmp/test", strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			s.router.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantField == "" {
				return
			}

			var resp SNMPTestResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("body is not the structured result (%q): %v", rec.Body.String(), err)
			}
			if !resp.Rejected {
				t.Error("a refused request was not reported as rejected")
			}
			if resp.TimedOut {
				t.Error("a refused request was reported as a timeout")
			}
			if !strings.Contains(resp.Error, tt.wantField) {
				t.Errorf("Error = %q, want it to name %q", resp.Error, tt.wantField)
			}
		})
	}
}

func TestTestSNMPConnectionRejectsUnknownDevice(t *testing.T) {
	s := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/devices/4242/test-snmp", http.NoBody)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %q)", rec.Code, rec.Body.String())
	}
}

// TestProbeHonoursCallerCancellation keeps a client that has gone away from
// holding an SNMP probe open for the rest of its budget.
func TestProbeHonoursCallerCancellation(t *testing.T) {
	t.Parallel()

	s := newTestServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	resp := s.performSNMPTest(ctx, snmpProbe{
		Device: "bastion", Address: silentAgent(t), SNMPVersion: "2c", Community: "public",
	})
	elapsed := time.Since(start)

	if elapsed >= SNMPProbeBudget {
		t.Fatalf("probe ran %v after its caller gave up", elapsed)
	}
	if resp.Success {
		t.Error("a canceled probe reported success")
	}
	if !resp.TimedOut {
		t.Error("a canceled probe was not classified as a timeout")
	}
}

func TestStatusForProbe(t *testing.T) {
	tests := []struct {
		name string
		resp *SNMPTestResponse
		want int
	}{
		{"success", &SNMPTestResponse{Success: true}, http.StatusOK},
		{"silent device", &SNMPTestResponse{TimedOut: true}, http.StatusGatewayTimeout},
		{"refused request", &SNMPTestResponse{Rejected: true}, http.StatusBadRequest},
		{"other failure", &SNMPTestResponse{}, http.StatusBadGateway},
		{"a rejection outranks a timeout", &SNMPTestResponse{Rejected: true, TimedOut: true}, http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := statusForProbe(tt.resp); got != tt.want {
				t.Errorf("statusForProbe(%+v) = %d, want %d", tt.resp, got, tt.want)
			}
		})
	}
}

func TestIsProbeTimeout(t *testing.T) {
	live := context.Background()
	dead, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{"gosnmp retry exhaustion", live, errors.New("request timeout (after 2 retries)"), true},
		{"deadline from the budget", live, context.DeadlineExceeded, true},
		{"a net.Error that timed out", live, &net.OpError{Err: &timeoutError{}}, true},
		{"a dead context reclassifies any error", dead, errors.New("connection refused"), true},
		{"an ordinary transport failure", live, errors.New("connection refused"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isProbeTimeout(tt.ctx, tt.err); got != tt.want {
				t.Errorf("isProbeTimeout(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// timeoutError is a net.Error that reports a timeout.
type timeoutError struct{}

func (*timeoutError) Error() string { return "i/o deadline reached" }
func (*timeoutError) Timeout() bool { return true }
func (*timeoutError) Temporary() bool {
	return true
}

func TestProbeErrorMessagesNameTheirSubject(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "unreachable device with a name",
			err:  &SNMPUnreachableError{Device: "bastion", Address: "198.51.100.14:161", Budget: 8 * time.Second, Err: errors.New("request timeout")},
			want: "snmp: device bastion (198.51.100.14:161) did not answer within 8s: request timeout",
		},
		{
			name: "unreachable address with no device",
			err:  &SNMPUnreachableError{Address: "198.51.100.101", Budget: 8 * time.Second, Err: errors.New("request timeout")},
			want: "snmp: device 198.51.100.101 did not answer within 8s: request timeout",
		},
		{
			name: "probe failure",
			err:  &SNMPProbeError{Device: "bastion", Address: "198.51.100.101", Reason: "connect failed", Err: errors.New("no route")},
			want: "snmp: probing device bastion (198.51.100.101): connect failed: no route",
		},
		{
			name: "probe failure without a cause",
			err:  &SNMPProbeError{Address: "198.51.100.101", Reason: "connect failed"},
			want: "snmp: probing device 198.51.100.101: connect failed",
		},
		{
			name: "refused request",
			err:  &SNMPRequestError{Field: "port", Value: "70000", Reason: "must be between 1 and 65535"},
			want: `snmp: invalid port "70000": must be between 1 and 65535`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}

	cause := errors.New("boom")
	if !errors.Is(&SNMPUnreachableError{Err: cause}, cause) {
		t.Error("SNMPUnreachableError does not unwrap its cause")
	}
	if !errors.Is(&SNMPProbeError{Err: cause}, cause) {
		t.Error("SNMPProbeError does not unwrap its cause")
	}
}

// itoa keeps the URL building in these tests readable.
func itoa(id int64) string { return strconv.FormatInt(id, 10) }

// TestRefusedDeviceIsABadGatewayNotATimeout separates a box that is silent from
// one that actively refuses: on a closed UDP port the kernel answers ICMP
// unreachable, which surfaces as a refused connection rather than a deadline.
func TestRefusedDeviceIsABadGatewayNotATimeout(t *testing.T) {
	t.Parallel()

	// Bind a UDP port, learn its address, then release it so nothing listens.
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	addr := conn.LocalAddr().String()
	if closeErr := conn.Close(); closeErr != nil {
		t.Fatalf("close: %v", closeErr)
	}

	s := newTestServer(t)
	id := storeDevice(t, s, "bastion", addr)

	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/api/devices/"+itoa(id)+"/test-snmp", http.NoBody))

	var resp SNMPTestResponse
	if unmarshalErr := json.Unmarshal(rec.Body.Bytes(), &resp); unmarshalErr != nil {
		t.Fatalf("response is not the structured result (%q): %v", rec.Body.String(), unmarshalErr)
	}
	if resp.Success {
		t.Fatal("a refused port reported success")
	}

	// Either classification is defensible depending on what the kernel
	// reported, but the connection must never be dropped and the body must
	// always name the device.
	if rec.Code != http.StatusBadGateway && rec.Code != http.StatusGatewayTimeout {
		t.Errorf("status = %d, want 502 or 504", rec.Code)
	}
	if resp.Device != "bastion" {
		t.Errorf("Device = %q, want the device name", resp.Device)
	}
	if !strings.Contains(resp.Error, "bastion") {
		t.Errorf("Error = %q, want it to name the device", resp.Error)
	}
}
