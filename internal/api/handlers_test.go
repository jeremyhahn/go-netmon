package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/poller"
)

// The poller's control API is what the web UI drives the poller through, so
// every rejection below is a message that ends up in front of an operator: a
// bare 500 or a silently ignored field is a support call.

// newTestHandler builds a handler over a poller that polls nothing off the
// machine.
func newTestHandler(t *testing.T) *PollerControlHandler {
	t.Helper()

	cfg := fastFailConfig()
	p := poller.New(cfg, nil)
	t.Cleanup(func() { _ = p.Stop() })

	return NewPollerControlHandler(p, cfg)
}

// registerBody is a valid registration for a device that answers nothing.
func registerBody(t *testing.T, name string) RegisterDeviceRequest {
	t.Helper()

	return RegisterDeviceRequest{
		Name:         name,
		Address:      deadAddress(t),
		SNMPVersion:  "2c",
		Community:    "public",
		PollInterval: 60,
		Enabled:      true,
	}
}

func request(t *testing.T, method, path string, body any) *http.Request {
	t.Helper()

	if body == nil {
		return httptest.NewRequest(method, path, nil)
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encoding the request body: %v", err)
	}
	return httptest.NewRequest(method, path, bytes.NewReader(encoded))
}

// TestHandleUpdateDeviceReplacesTheDevice covers the path the web UI takes every
// time a device's MIB assignment changes.
func TestHandleUpdateDeviceReplacesTheDevice(t *testing.T) {
	t.Parallel()

	handler := newTestHandler(t)
	body := registerBody(t, "bastion")

	w := httptest.NewRecorder()
	handler.HandleRegisterDevice(w, request(t, http.MethodPost, "/api/poller/devices", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("register: status = %d, want 201. Body: %s", w.Code, w.Body.String())
	}

	body.PollInterval = 120
	body.Tags = map[string]string{"site": "hq"}

	w = httptest.NewRecorder()
	handler.HandleUpdateDevice(w, request(t, http.MethodPut, "/api/poller/devices/bastion", body))
	if w.Code != http.StatusOK {
		t.Fatalf("update: status = %d, want 200. Body: %s", w.Code, w.Body.String())
	}

	var resp DeviceResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding the update response: %v", err)
	}
	if resp.PollInterval != 120 {
		t.Errorf("poll_interval = %d, want the updated 120", resp.PollInterval)
	}

	status, polling := handler.poller.WorkerStatus("bastion")
	if !polling {
		t.Fatal("the updated device has no worker")
	}
	if status.Interval.Seconds() != 120 {
		t.Errorf("the worker still polls every %s, want 120s", status.Interval)
	}
}

// TestHandleUpdateDeviceRejectsBadRequests covers everything the handler must
// refuse before it touches the poller, since each one would otherwise remove a
// running device and fail to put it back.
func TestHandleUpdateDeviceRejectsBadRequests(t *testing.T) {
	t.Parallel()

	handler := newTestHandler(t)

	tests := []struct {
		name   string
		method string
		path   string
		body   any
		want   int
	}{
		{"the wrong method", http.MethodPost, "/api/poller/devices/bastion", registerBody(t, "bastion"), http.StatusMethodNotAllowed},
		{"no device name in the path", http.MethodPut, "/api/poller/devices/", registerBody(t, "bastion"), http.StatusBadRequest},
		{"a body that is not JSON", http.MethodPut, "/api/poller/devices/bastion", nil, http.StatusBadRequest},
		{"a name that contradicts the path", http.MethodPut, "/api/poller/devices/bastion", registerBody(t, "someone-else"), http.StatusBadRequest},
		{"a device that fails validation", http.MethodPut, "/api/poller/devices/bastion", RegisterDeviceRequest{Name: "bastion"}, http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := request(t, tt.method, tt.path, tt.body)
			if tt.body == nil && tt.method == http.MethodPut {
				req = httptest.NewRequest(tt.method, tt.path, strings.NewReader("{not json"))
			}

			w := httptest.NewRecorder()
			handler.HandleUpdateDevice(w, req)

			if w.Code != tt.want {
				t.Errorf("status = %d, want %d. Body: %s", w.Code, tt.want, w.Body.String())
			}
		})
	}
}

// TestHandlersRefuseTheWrongMethod keeps a mistyped verb from being answered as
// though it had worked.
func TestHandlersRefuseTheWrongMethod(t *testing.T) {
	t.Parallel()

	handler := newTestHandler(t)

	tests := []struct {
		name   string
		handle func(http.ResponseWriter, *http.Request)
		method string
	}{
		{"register", handler.HandleRegisterDevice, http.MethodGet},
		{"remove", handler.HandleRemoveDevice, http.MethodGet},
		{"list", handler.HandleListDevices, http.MethodDelete},
		{"status", handler.HandleGetDeviceStatus, http.MethodPost},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			tt.handle(w, httptest.NewRequest(tt.method, "/api/poller/devices/bastion/status", nil))

			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("status = %d, want 405", w.Code)
			}
		})
	}
}

// TestHandlersRequireADeviceName covers the path with nothing where the name
// should be, which would otherwise address whichever device sorted first.
func TestHandlersRequireADeviceName(t *testing.T) {
	t.Parallel()

	handler := newTestHandler(t)

	tests := []struct {
		name   string
		handle func(http.ResponseWriter, *http.Request)
		method string
	}{
		{"remove", handler.HandleRemoveDevice, http.MethodDelete},
		{"status", handler.HandleGetDeviceStatus, http.MethodGet},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			tt.handle(w, httptest.NewRequest(tt.method, "/devices/", nil))

			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", w.Code)
			}
		})
	}
}

func TestHandleRegisterDeviceRejectsABodyThatIsNotJSON(t *testing.T) {
	t.Parallel()

	handler := newTestHandler(t)
	w := httptest.NewRecorder()

	handler.HandleRegisterDevice(w,
		httptest.NewRequest(http.MethodPost, "/api/poller/devices", strings.NewReader("{not json")))

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// TestHandleRegisterDeviceRejectsADuplicate covers the poller refusing a device
// it already runs: the failure belongs in the response, not only in the log.
func TestHandleRegisterDeviceRejectsADuplicate(t *testing.T) {
	t.Parallel()

	handler := newTestHandler(t)
	body := registerBody(t, "bastion")

	for _, want := range []int{http.StatusCreated, http.StatusInternalServerError} {
		w := httptest.NewRecorder()
		handler.HandleRegisterDevice(w, request(t, http.MethodPost, "/api/poller/devices", body))
		if w.Code != want {
			t.Fatalf("status = %d, want %d. Body: %s", w.Code, want, w.Body.String())
		}
	}
}

func TestValidateRegisterRequest(t *testing.T) {
	t.Parallel()

	handler := newTestHandler(t)

	valid := func(mutate func(*RegisterDeviceRequest)) RegisterDeviceRequest {
		req := RegisterDeviceRequest{
			Name: "bastion", Address: "127.0.0.1:161", SNMPVersion: "2c", Community: "public",
		}
		mutate(&req)
		return req
	}

	tests := []struct {
		name    string
		request RegisterDeviceRequest
		wantErr string
	}{
		{"a complete v2c device", valid(func(*RegisterDeviceRequest) {}), ""},
		{
			"a complete v3 device",
			valid(func(r *RegisterDeviceRequest) { r.SNMPVersion = "3"; r.Community = ""; r.Username = "netmon" }),
			"",
		},
		{"no name", valid(func(r *RegisterDeviceRequest) { r.Name = "" }), "name"},
		{"no address", valid(func(r *RegisterDeviceRequest) { r.Address = "" }), "address"},
		{"no version", valid(func(r *RegisterDeviceRequest) { r.SNMPVersion = "" }), "snmp_version"},
		{"a version we do not speak", valid(func(r *RegisterDeviceRequest) { r.SNMPVersion = "1" }), "snmp_version"},
		{"v2c without a community", valid(func(r *RegisterDeviceRequest) { r.Community = "" }), "community"},
		{
			"v3 without a username",
			valid(func(r *RegisterDeviceRequest) { r.SNMPVersion = "3"; r.Community = "" }),
			"username",
		},
		{"a negative poll interval", valid(func(r *RegisterDeviceRequest) { r.PollInterval = -1 }), "poll_interval"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := handler.validateRegisterRequest(&tt.request)

			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("validate = %v, want it accepted", err)
			case tt.wantErr != "" && err == nil:
				t.Errorf("validate accepted a request missing its %s", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Errorf("validate = %q, want it to name %q", err, tt.wantErr)
			}
		})
	}
}

// TestRequestToDeviceFillsInTheDefaults covers the two fields the web UI is
// allowed to omit, which the poller cannot run without.
func TestRequestToDeviceFillsInTheDefaults(t *testing.T) {
	t.Parallel()

	handler := newTestHandler(t)

	device := handler.requestToDevice(&RegisterDeviceRequest{
		Name: "bastion", Address: "127.0.0.1:161", SNMPVersion: "2c", Community: "public",
	})

	if device.Profile != "default" {
		t.Errorf("profile = %q, want %q", device.Profile, "default")
	}
	if device.PollInterval != handler.config.Global.DefaultPollInterval {
		t.Errorf("poll_interval = %d, want the global default %d",
			device.PollInterval, handler.config.Global.DefaultPollInterval)
	}
}

func TestExtractDeviceName(t *testing.T) {
	t.Parallel()

	handler := newTestHandler(t)

	tests := []struct {
		path string
		want string
	}{
		{"/api/poller/devices/bastion", "bastion"},
		{"/api/poller/devices/bastion/status", "bastion"},
		{"/api/poller/devices/bastion/", "bastion"},
		{"/api/poller/devices", ""},
		{"/api/poller/devices/", ""},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := handler.extractDeviceName(tt.path); got != tt.want {
				t.Errorf("extractDeviceName(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}
