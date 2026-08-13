package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"github.com/jeremyhahn/go-netmon/internal/webui/client"
	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// newServerWithPoller builds a server on a temporary database with its real
// routes registered, so tests exercise the paths clients call rather than a
// handler in isolation.
func newServerWithPoller(t *testing.T, poller PollerClient) *Server {
	t.Helper()

	store, err := storage.New(filepath.Join(t.TempDir(), "webui.db"))
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	server := &Server{
		router:       mux.NewRouter(),
		storage:      store,
		pollerClient: poller,
	}
	server.routes()

	return server
}

// tablePoller answers like a poller holding exactly these devices.
//
// The fleet view reads the poller's whole listing in one call, so a device is
// either in that listing or it is not: one in statuses appears with it, one in
// failures appears without it -- the poller knows the device but has nothing to
// say about it -- and one in neither is absent, which is what "not registered"
// means. The per-device accessor answers from the same tables so the two can
// never disagree.
func tablePoller(statuses map[string]*client.DeviceStatus, failures map[string]error) *mockPollerClient {
	return &mockPollerClient{
		getDeviceStatusFunc: func(_ context.Context, name string) (*client.DeviceStatus, error) {
			if err, ok := failures[name]; ok {
				return nil, err
			}
			if status, ok := statuses[name]; ok {
				return status, nil
			}
			return nil, errors.New("device not found")
		},
		listDevicesFunc: func(context.Context) ([]client.DeviceInfo, error) {
			infos := make([]client.DeviceInfo, 0, len(statuses)+len(failures))
			for name, status := range statuses {
				infos = append(infos, client.DeviceInfo{Name: name, Status: status})
			}
			for name := range failures {
				infos = append(infos, client.DeviceInfo{Name: name})
			}
			return infos, nil
		},
	}
}

// requestFleetStatus calls the bulk endpoint through the router and decodes it.
func requestFleetStatus(ctx context.Context, t *testing.T, server *Server) (*httptest.ResponseRecorder, FleetStatusResponse) {
	t.Helper()

	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/devices/status", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	var fleet FleetStatusResponse
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &fleet); err != nil {
			t.Fatalf("decoding fleet status: %v (body %q)", err, w.Body.String())
		}
	}

	return w, fleet
}

// deviceNames lists the names in a fleet response, in the order returned.
func deviceNames(fleet FleetStatusResponse) []string {
	names := make([]string, len(fleet.Devices))
	for i, entry := range fleet.Devices {
		names[i] = entry.Name
	}
	return names
}

// entryFor finds a device's row, or fails: an absent device is the defect the
// bulk endpoint exists to avoid.
func entryFor(t *testing.T, fleet FleetStatusResponse, name string) FleetDeviceStatus {
	t.Helper()

	for _, entry := range fleet.Devices {
		if entry.Name == name {
			return entry
		}
	}

	t.Fatalf("device %q missing from fleet status; got %v", name, deviceNames(fleet))
	return FleetDeviceStatus{}
}

// TestFleetStatusClassifiesEveryConfiguredDevice is the endpoint's central
// contract: one row per configured device, whatever the poller says about it.
//
// The five devices cover every answer the poller can give, including the two
// that a fleet view must never confuse — a device that has never been polled
// and a device whose polls fail — and one the poller has no record of at all,
// which must appear as a row rather than 404 the whole request.
func TestFleetStatusClassifiesEveryConfiguredDevice(t *testing.T) {
	polled := time.Date(2026, 8, 10, 4, 5, 26, 0, time.UTC)

	statuses := map[string]*client.DeviceStatus{
		"core-01": {
			Name:             "core-01",
			Connected:        true,
			LastPollSuccess:  true,
			LastPollTime:     polled,
			MetricsCollected: 15973,
			PollInterval:     "30s",
		},
		"core-02": {
			Name:            "core-02",
			Connected:       false,
			LastPollSuccess: false,
			LastPollTime:    polled,
			LastError:       "poller: 3 of 44 SNMP operations failed",
			PollInterval:    "30s",
		},
		"bastion": {
			Name:                "bastion",
			Connected:           false,
			LastPollSuccess:     false,
			LastPollTime:        polled,
			LastError:           "snmp: request timeout (after 3 retries)",
			Unreachable:         true,
			ConsecutiveFailures: 8,
			PollInterval:        "30s",
		},
		// Registered with the poller, no poll result yet: the zero LastPollTime
		// is what says so.
		"pdu-03": {
			Name:         "pdu-03",
			PollInterval: "30s",
		},
	}
	failures := map[string]error{
		"switch-07": errors.New("connection refused"),
	}

	server := newServerWithPoller(t, tablePoller(statuses, failures))
	// Created out of alphabetical order so the response order cannot be an
	// accident of insertion order.
	for _, name := range []string{"pdu-03", "switch-07", "bastion", "core-02", "core-01", "unregistered-99"} {
		mustCreateDevice(t, server.storage, name)
	}

	w, fleet := requestFleetStatus(t.Context(), t, server)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %q)", w.Code, http.StatusOK, w.Body.String())
	}

	wantOrder := []string{"bastion", "core-01", "core-02", "pdu-03", "switch-07", "unregistered-99"}
	if got := deviceNames(fleet); !equalStrings(got, wantOrder) {
		t.Fatalf("device order = %v, want %v", got, wantOrder)
	}

	wantStates := map[string]DeviceState{
		"core-01":      StateOK,
		"core-02":      StateFailing,
		"bastion":            StateUnreachable,
		"pdu-03":      StateNeverPolled,
		"unregistered-99": StateNotRegistered,
		"switch-07":       StateUnknown,
	}
	for name, want := range wantStates {
		if got := entryFor(t, fleet, name).State; got != want {
			t.Errorf("%s state = %q, want %q", name, got, want)
		}
	}

	// "never polled" and "polled and failed" must be tellable apart from the
	// payload itself, not only from the summary state.
	never := entryFor(t, fleet, "pdu-03")
	failed := entryFor(t, fleet, "core-02")
	if never.Status == nil || !never.Status.LastPollTime.IsZero() {
		t.Errorf("pdu-03 last_poll_time = %v, want the zero time for a device never polled", never.Status)
	}
	if failed.Status == nil || failed.Status.LastPollTime.IsZero() {
		t.Errorf("core-02 last_poll_time is zero; a failed poll is still a poll and must be stamped")
	}
}

// TestFleetStatusReportsUnreachableDeviceWithoutFailing pins the live stack's
// hard case: bastion answers no SNMP at all, and that fact must arrive as its own
// row's state rather than as an error on the request.
func TestFleetStatusReportsUnreachableDeviceWithoutFailing(t *testing.T) {
	statuses := map[string]*client.DeviceStatus{
		"bastion": {
			Name:                "bastion",
			LastPollTime:        time.Date(2026, 8, 10, 3, 56, 52, 0, time.UTC),
			LastError:           "snmp: GET of 20 OIDs failed: request timeout (after 3 retries)",
			Unreachable:         true,
			ConsecutiveFailures: 8,
		},
		"core-01": {Name: "core-01", Connected: true, LastPollSuccess: true, LastPollTime: time.Now()},
	}

	server := newServerWithPoller(t, tablePoller(statuses, nil))
	mustCreateDevice(t, server.storage, "bastion")
	mustCreateDevice(t, server.storage, "core-01")

	w, fleet := requestFleetStatus(t.Context(), t, server)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	bastion := entryFor(t, fleet, "bastion")
	if bastion.State != StateUnreachable {
		t.Errorf("bastion state = %q, want %q", bastion.State, StateUnreachable)
	}
	if bastion.Status == nil || !bastion.Status.Unreachable {
		t.Errorf("bastion status did not carry the poller's unreachable flag: %+v", bastion.Status)
	}
	if bastion.Status != nil && bastion.Status.ConsecutiveFailures != 8 {
		t.Errorf("bastion consecutive_failures = %d, want 8", bastion.Status.ConsecutiveFailures)
	}
	if entryFor(t, fleet, "core-01").State != StateOK {
		t.Errorf("a healthy device was dragged down by an unreachable one")
	}
}

// TestFleetStatusOneDeviceErrorLeavesTheOthersIntact checks the isolation the
// endpoint promises: a device the poller cannot answer for costs that device
// its status and nothing else.
func TestFleetStatusOneDeviceErrorLeavesTheOthersIntact(t *testing.T) {
	statuses := map[string]*client.DeviceStatus{
		"alpha": {Name: "alpha", Connected: true, LastPollSuccess: true, LastPollTime: time.Now(), MetricsCollected: 11},
		"gamma": {Name: "gamma", Connected: true, LastPollSuccess: true, LastPollTime: time.Now(), MetricsCollected: 22},
	}
	failures := map[string]error{"beta": errors.New("poller: connection reset by peer")}

	server := newServerWithPoller(t, tablePoller(statuses, failures))
	for _, name := range []string{"alpha", "beta", "gamma"} {
		mustCreateDevice(t, server.storage, name)
	}

	_, fleet := requestFleetStatus(t.Context(), t, server)

	if len(fleet.Devices) != 3 {
		t.Fatalf("got %d rows, want 3: an erroring device must not remove others", len(fleet.Devices))
	}

	beta := entryFor(t, fleet, "beta")
	if beta.State != StateUnknown {
		t.Errorf("beta state = %q, want %q", beta.State, StateUnknown)
	}
	if beta.Status != nil {
		t.Errorf("beta carried a status it could not have: %+v", beta.Status)
	}
	if beta.Error == "" {
		t.Error("beta state is unknown but no reason was reported")
	}

	for name, want := range map[string]int{"alpha": 11, "gamma": 22} {
		entry := entryFor(t, fleet, name)
		if entry.State != StateOK {
			t.Errorf("%s state = %q, want %q", name, entry.State, StateOK)
		}
		if entry.Status == nil || entry.Status.MetricsCollected != want {
			t.Errorf("%s metrics_collected = %+v, want %d", name, entry.Status, want)
		}
		if entry.Error != "" {
			t.Errorf("%s reported error %q from a neighbor's failure", name, entry.Error)
		}
	}
}

// TestFleetStatusOrderIsStableAcrossCalls guards the defect this class of
// endpoint already shipped once: a list assembled in Go map order reshuffles on
// every request and the UI rebuilds itself around a different first row.
func TestFleetStatusOrderIsStableAcrossCalls(t *testing.T) {
	names := []string{"zulu", "alpha", "mike", "bravo", "yankee", "charlie"}

	statuses := make(map[string]*client.DeviceStatus, len(names))
	for _, name := range names {
		statuses[name] = &client.DeviceStatus{Name: name, Connected: true, LastPollSuccess: true, LastPollTime: time.Now()}
	}

	server := newServerWithPoller(t, tablePoller(statuses, nil))
	for _, name := range names {
		mustCreateDevice(t, server.storage, name)
	}

	want := []string{"alpha", "bravo", "charlie", "mike", "yankee", "zulu"}
	for call := range 12 {
		_, fleet := requestFleetStatus(t.Context(), t, server)
		if got := deviceNames(fleet); !equalStrings(got, want) {
			t.Fatalf("call %d order = %v, want %v", call, got, want)
		}
	}
}

// TestFleetStatusWithNoDevicesReturnsEmptyList pins the wire form for an empty
// fleet: a client that iterates the array must not have to defend against null.
func TestFleetStatusWithNoDevicesReturnsEmptyList(t *testing.T) {
	server := newServerWithPoller(t, tablePoller(nil, nil))

	w, fleet := requestFleetStatus(t.Context(), t, server)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: an empty fleet is not an error", w.Code, http.StatusOK)
	}
	if fleet.Devices == nil {
		t.Error("devices decoded as nil; the endpoint must emit an empty array")
	}
	if len(fleet.Devices) != 0 {
		t.Errorf("got %d rows for an empty fleet", len(fleet.Devices))
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if got := string(raw["devices"]); got != "[]" {
		t.Errorf("devices encoded as %s, want []", got)
	}
}

// TestFleetStatusMatchesSingleDeviceEndpoint pins the two endpoints together.
// The bulk row's status object must be byte-for-byte what GET
// /api/devices/{id}/status returns, so the fleet view and the device view can
// never disagree about a device.
func TestFleetStatusMatchesSingleDeviceEndpoint(t *testing.T) {
	statuses := map[string]*client.DeviceStatus{
		"alpha": {
			Name:             "alpha",
			Connected:        true,
			LastPollSuccess:  true,
			LastPollTime:     time.Date(2026, 8, 10, 4, 5, 26, 269641018, time.UTC),
			NextPollTime:     time.Date(2026, 8, 10, 4, 5, 56, 269625739, time.UTC),
			MetricsCollected: 15973,
			PollInterval:     "30s",
			Tags:             map[string]string{"source": "webui"},
		},
		"bravo": {
			Name:                "bravo",
			LastPollTime:        time.Date(2026, 8, 10, 3, 56, 52, 550541906, time.UTC),
			LastError:           "snmp: request timeout (after 3 retries)",
			Unreachable:         true,
			ConsecutiveFailures: 8,
		},
	}

	server := newServerWithPoller(t, tablePoller(statuses, nil))
	ids := map[string]int64{
		"alpha": mustCreateDevice(t, server.storage, "alpha"),
		"bravo": mustCreateDevice(t, server.storage, "bravo"),
	}

	_, fleet := requestFleetStatus(t.Context(), t, server)

	for name, id := range ids {
		single := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet,
			"/api/devices/"+formatDeviceID(id)+"/status", nil)
		server.router.ServeHTTP(single, req)

		if single.Code != http.StatusOK {
			t.Fatalf("%s single status = %d, want %d", name, single.Code, http.StatusOK)
		}

		bulk, err := json.Marshal(entryFor(t, fleet, name).Status)
		if err != nil {
			t.Fatalf("re-encoding %s bulk status: %v", name, err)
		}

		if got, want := canonicalJSON(t, bulk), canonicalJSON(t, single.Body.Bytes()); got != want {
			t.Errorf("%s bulk status drifted from the single-device endpoint:\n bulk   = %s\n single = %s", name, got, want)
		}
	}
}

// TestFleetStatusWithoutPollerClientStillListsEveryDevice covers the degraded
// deployment: with no poller wired up nothing can be said about any device, but
// the fleet is still the fleet.
func TestFleetStatusWithoutPollerClientStillListsEveryDevice(t *testing.T) {
	server := newServerWithPoller(t, nil)
	mustCreateDevice(t, server.storage, "alpha")
	mustCreateDevice(t, server.storage, "bravo")

	w, fleet := requestFleetStatus(t.Context(), t, server)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if len(fleet.Devices) != 2 {
		t.Fatalf("got %d rows, want 2", len(fleet.Devices))
	}
	for _, entry := range fleet.Devices {
		if entry.State != StateUnknown {
			t.Errorf("%s state = %q, want %q", entry.Name, entry.State, StateUnknown)
		}
		if entry.Error == "" {
			t.Errorf("%s state is unknown but no reason was reported", entry.Name)
		}
	}
}

// TestFleetStatusCarriesDeviceIdentity checks the fields a fleet card needs
// beyond the poller's status: the row must be navigable and labeled without a
// second request.
func TestFleetStatusCarriesDeviceIdentity(t *testing.T) {
	server := newServerWithPoller(t, tablePoller(nil, nil))
	id := mustCreateDevice(t, server.storage, "alpha")

	_, fleet := requestFleetStatus(t.Context(), t, server)

	entry := entryFor(t, fleet, "alpha")
	if entry.ID != id {
		t.Errorf("id = %d, want %d", entry.ID, id)
	}
	if entry.Address != "192.0.2.1" {
		t.Errorf("address = %q, want the device's configured address", entry.Address)
	}
}

// TestFleetStatusSilentDeviceIsReportedNotHidden covers the device the poller
// holds no status for. It is registered, so it is not "not registered"; nothing
// is known about it, so it is unknown -- and its neighbors are unaffected.
func TestFleetStatusSilentDeviceIsReportedNotHidden(t *testing.T) {
	healthy := &client.DeviceStatus{Connected: true, LastPollSuccess: true, LastPollTime: time.Now()}
	server := newServerWithPoller(t, tablePoller(
		map[string]*client.DeviceStatus{"alpha": healthy, "gamma": healthy},
		map[string]error{"blackhole": errors.New("no status held")},
	))
	for _, name := range []string{"alpha", "blackhole", "gamma"} {
		mustCreateDevice(t, server.storage, name)
	}

	w, fleet := requestFleetStatus(t.Context(), t, server)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: one silent device must not fail the request", w.Code, http.StatusOK)
	}
	if len(fleet.Devices) != 3 {
		t.Fatalf("got %d rows, want 3", len(fleet.Devices))
	}
	if got := entryFor(t, fleet, "blackhole").State; got != StateUnknown {
		t.Errorf("blackhole state = %q, want %q", got, StateUnknown)
	}
	for _, name := range []string{"alpha", "gamma"} {
		if got := entryFor(t, fleet, name).State; got != StateOK {
			t.Errorf("%s state = %q, want %q; a silent neighbor cost it its status", name, got, StateOK)
		}
	}
}

// TestFleetStatusAnswersWhenThePollerWillNot is the endpoint's hard deadline. A
// poller that never replies must not hold the request open: every configured
// device still gets a row, promptly, saying nothing is known.
func TestFleetStatusAnswersWhenThePollerWillNot(t *testing.T) {
	poller := &mockPollerClient{
		listDevicesFunc: func(ctx context.Context) ([]client.DeviceInfo, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}

	server := newServerWithPoller(t, poller)
	const total = 20
	for i := range total {
		mustCreateDevice(t, server.storage, "device-"+formatDeviceID(int64(i)))
	}

	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()

	w, fleet := requestFleetStatus(ctx, t, server)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if len(fleet.Devices) != total {
		t.Fatalf("got %d rows, want %d: devices were dropped when the poller went quiet", len(fleet.Devices), total)
	}
	for _, entry := range fleet.Devices {
		if entry.State != StateUnknown {
			t.Errorf("%s state = %q, want %q", entry.Name, entry.State, StateUnknown)
		}
	}
}

// TestFleetStatusIsGETOnly is the endpoint's bad-input case: it reads state and
// must not be reachable by a method that implies changing it.
func TestFleetStatusIsGETOnly(t *testing.T) {
	server := newServerWithPoller(t, tablePoller(nil, nil))
	mustCreateDevice(t, server.storage, "alpha")

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), method, "/api/devices/status", nil)
			w := httptest.NewRecorder()
			server.router.ServeHTTP(w, req)

			if w.Code == http.StatusOK {
				t.Fatalf("%s was served; the route is registered for GET only", method)
			}
			if strings.Contains(w.Body.String(), `"devices"`) {
				t.Errorf("%s reached the fleet handler: %s", method, w.Body.String())
			}
		})
	}
}

// TestDeviceStateNamesEveryOutcome exercises the classifier directly, including
// the inputs no healthy poller produces.
func TestDeviceStateNamesEveryOutcome(t *testing.T) {
	polled := time.Date(2026, 8, 10, 4, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		status *client.DeviceStatus
		err    error
		want   DeviceState
	}{
		{
			name:   "successful poll",
			status: &client.DeviceStatus{LastPollSuccess: true, LastPollTime: polled},
			want:   StateOK,
		},
		{
			name:   "failed poll on a reachable device",
			status: &client.DeviceStatus{LastPollSuccess: false, LastPollTime: polled},
			want:   StateFailing,
		},
		{
			name:   "unreachable outranks a failed poll",
			status: &client.DeviceStatus{LastPollSuccess: false, LastPollTime: polled, Unreachable: true},
			want:   StateUnreachable,
		},
		{
			name:   "registered but never polled",
			status: &client.DeviceStatus{PollInterval: "30s"},
			want:   StateNeverPolled,
		},
		{
			name: "poller has no record of the device",
			err:  &DeviceNotPolledError{Device: "alpha"},
			want: StateNotRegistered,
		},
		{
			name: "poller could not be reached",
			err:  &PollerUnavailableError{Device: "alpha", Err: errors.New("connection refused")},
			want: StateUnknown,
		},
		{
			name: "no poller configured",
			err:  &PollerUnconfiguredError{},
			want: StateUnknown,
		},
		{
			name: "wrapped not-found is still not registered",
			err:  errors.Join(errors.New("context"), &DeviceNotPolledError{Device: "alpha"}),
			want: StateNotRegistered,
		},
		{
			name: "neither a status nor an error",
			want: StateUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deviceState(tt.status, tt.err); got != tt.want {
				t.Errorf("deviceState() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestNewFleetDeviceStatus checks the row builder in both directions: a device
// with a status keeps it, and a device without one explains why instead.
func TestNewFleetDeviceStatus(t *testing.T) {
	device := storage.Device{ID: 7, Name: "alpha", Address: "192.0.2.7"}

	t.Run("with a status", func(t *testing.T) {
		status := &client.DeviceStatus{Name: "alpha", LastPollSuccess: true, LastPollTime: time.Now()}

		entry := newFleetDeviceStatus(device, status, nil, PollingConfig{})

		if entry.ID != 7 || entry.Name != "alpha" || entry.Address != "192.0.2.7" {
			t.Errorf("identity not carried through: %+v", entry)
		}
		if entry.Status != status {
			t.Errorf("status = %+v, want the poller's own status", entry.Status)
		}
		if entry.Error != "" {
			t.Errorf("error = %q, want empty for a device that answered", entry.Error)
		}
	})

	t.Run("with an error", func(t *testing.T) {
		entry := newFleetDeviceStatus(device, nil,
			&PollerUnavailableError{Device: "alpha", Err: errors.New("connection refused")}, PollingConfig{})

		if entry.State != StateUnknown {
			t.Errorf("state = %q, want %q", entry.State, StateUnknown)
		}
		if entry.Status != nil {
			t.Errorf("status = %+v, want nil when none could be obtained", entry.Status)
		}
		if entry.Error == "" {
			t.Error("error is empty; an unknown state must carry its reason")
		}
	})

	t.Run("with a determinate absence", func(t *testing.T) {
		entry := newFleetDeviceStatus(device, nil, &DeviceNotPolledError{Device: "alpha"}, PollingConfig{})

		if entry.State != StateNotRegistered {
			t.Errorf("state = %q, want %q", entry.State, StateNotRegistered)
		}
		if entry.Error != "" {
			t.Errorf("error = %q; the poller answered definitively, so the state is the whole story", entry.Error)
		}
	})
}

// TestDeviceStatusAssembler covers the one implementation both endpoints use.
func TestDeviceStatusAssembler(t *testing.T) {
	t.Run("returns the poller's status", func(t *testing.T) {
		want := &client.DeviceStatus{Name: "alpha", Connected: true}
		server := &Server{pollerClient: tablePoller(map[string]*client.DeviceStatus{"alpha": want}, nil)}

		got, err := server.deviceStatus(t.Context(), "alpha")
		if err != nil {
			t.Fatalf("deviceStatus() error = %v", err)
		}
		if got != want {
			t.Errorf("deviceStatus() = %+v, want %+v", got, want)
		}
	})

	t.Run("rejects an empty device name", func(t *testing.T) {
		server := &Server{pollerClient: tablePoller(nil, nil)}

		_, err := server.deviceStatus(t.Context(), "")

		var nameRequired *DeviceNameRequiredError
		if !errors.As(err, &nameRequired) {
			t.Errorf("deviceStatus(\"\") error = %v, want a DeviceNameRequiredError", err)
		}
	})

	t.Run("reports a missing poller client", func(t *testing.T) {
		server := &Server{}

		_, err := server.deviceStatus(t.Context(), "alpha")

		var unconfigured *PollerUnconfiguredError
		if !errors.As(err, &unconfigured) {
			t.Errorf("deviceStatus() error = %v, want a PollerUnconfiguredError", err)
		}
	})

	t.Run("classifies a device the poller does not know", func(t *testing.T) {
		server := &Server{pollerClient: tablePoller(nil, nil)}

		_, err := server.deviceStatus(t.Context(), "alpha")

		var notPolled *DeviceNotPolledError
		if !errors.As(err, &notPolled) {
			t.Fatalf("deviceStatus() error = %v, want a DeviceNotPolledError", err)
		}
		if notPolled.Device != "alpha" {
			t.Errorf("DeviceNotPolledError.Device = %q, want alpha", notPolled.Device)
		}
	})

	t.Run("classifies a 404 from the poller", func(t *testing.T) {
		server := &Server{pollerClient: tablePoller(nil, map[string]error{"alpha": errors.New("HTTP 404: no such device")})}

		_, err := server.deviceStatus(t.Context(), "alpha")

		var notPolled *DeviceNotPolledError
		if !errors.As(err, &notPolled) {
			t.Errorf("deviceStatus() error = %v, want a DeviceNotPolledError", err)
		}
	})

	t.Run("classifies a transport failure as unavailable", func(t *testing.T) {
		cause := errors.New("connection refused")
		server := &Server{pollerClient: tablePoller(nil, map[string]error{"alpha": cause})}

		_, err := server.deviceStatus(t.Context(), "alpha")

		var unavailable *PollerUnavailableError
		if !errors.As(err, &unavailable) {
			t.Fatalf("deviceStatus() error = %v, want a PollerUnavailableError", err)
		}
		if !errors.Is(err, cause) {
			t.Errorf("deviceStatus() error does not unwrap to the transport failure")
		}
	})
}

// TestPollerStatusErrorMessages checks each error reads as a sentence and
// survives a nil cause, which is the shape a defensive caller can construct.
func TestPollerStatusErrorMessages(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "device name required",
			err:  &DeviceNameRequiredError{},
			want: "poller: device name is required",
		},
		{
			name: "poller unconfigured",
			err:  &PollerUnconfiguredError{},
			want: "poller: no poller client is configured",
		},
		{
			name: "device not polled",
			err:  &DeviceNotPolledError{Device: "bastion"},
			want: `poller: device "bastion" is not registered with the poller`,
		},
		{
			name: "poller unavailable",
			err:  &PollerUnavailableError{Device: "bastion", Err: errors.New("connection refused")},
			want: `poller: status for device "bastion" is unavailable: connection refused`,
		},
		{
			name: "poller unavailable without a cause",
			err:  &PollerUnavailableError{Device: "bastion"},
			want: `poller: status for device "bastion" is unavailable`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}

	if got := errors.Unwrap(&PollerUnavailableError{}); got != nil {
		t.Errorf("Unwrap() of a causeless error = %v, want nil", got)
	}
	if got := errors.Unwrap(&DeviceNotPolledError{}); got != nil {
		t.Errorf("Unwrap() of a causeless error = %v, want nil", got)
	}
}

// equalStrings compares two name lists.
func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// formatDeviceID renders an ID for a URL path.
func formatDeviceID(id int64) string {
	return strconv.FormatInt(id, 10)
}

// canonicalJSON re-encodes a document so two encodings of the same value
// compare equal regardless of whitespace.
func canonicalJSON(t *testing.T, data []byte) string {
	t.Helper()

	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("decoding %q: %v", data, err)
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("re-encoding %q: %v", data, err)
	}

	return string(encoded)
}
