package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// seconds returns a pointer to an interval, which is how a device that has one
// of its own carries it.
func seconds(n int) *int { return &n }

// shippedPolling is the timing config.yaml ships: a 30s default against a 40s
// worst-case SNMP budget. It is the case that decides whether a short interval
// is an error or a warning, so it is the case the tests use.
var shippedPolling = PollingConfig{DefaultInterval: 30, SNMPTimeout: 10, SNMPRetries: 3}

// TestPollingDefaultsMatchConfigDefaults keeps the numbers the web UI assumes
// tied to the numbers the poller actually applies. If internal/config changes
// what an omitted setting means, a web UI that was not told the setting would
// start reporting an interval no device is polled at — so that drift fails here
// instead of in the field.
func TestPollingDefaultsMatchConfigDefaults(t *testing.T) {
	var cfg config.Config
	config.ApplyDefaults(&cfg)

	if cfg.Global.DefaultPollInterval != DefaultPollInterval {
		t.Errorf("DefaultPollInterval = %d, config applies %d",
			DefaultPollInterval, cfg.Global.DefaultPollInterval)
	}
	if cfg.Global.SNMPTimeout != DefaultSNMPTimeout {
		t.Errorf("DefaultSNMPTimeout = %d, config applies %d",
			DefaultSNMPTimeout, cfg.Global.SNMPTimeout)
	}
	if cfg.Global.SNMPRetries != DefaultSNMPRetries {
		t.Errorf("DefaultSNMPRetries = %d, config applies %d",
			DefaultSNMPRetries, cfg.Global.SNMPRetries)
	}
}

// TestPollingEffectiveAgreesWithThePoller proves the two ends read the same
// value the same way: what the web UI reports a device will be polled at is
// what config.Device.GetPollInterval resolves for the value the web UI pushes.
func TestPollingEffectiveAgreesWithThePoller(t *testing.T) {
	polling := shippedPolling

	tests := []struct {
		name   string
		device storage.Device
		want   int
	}{
		{"a device with its own schedule", storage.Device{PollInterval: seconds(120)}, 120},
		{"a device that inherits", storage.Device{}, 30},
		{"a device explicitly set to the default", storage.Device{PollInterval: seconds(30)}, 30},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := polling.Effective(tt.device.PollInterval)
			if got != tt.want {
				t.Errorf("Effective = %d, want %d", got, tt.want)
			}

			// The same device as the poller receives it, resolved by the
			// poller's own rule.
			pushed := config.Device{PollInterval: storage.PollIntervalOf(&tt.device)}
			resolved := pushed.GetPollInterval(polling.DefaultInterval)
			if resolved != time.Duration(tt.want)*time.Second {
				t.Errorf("the poller would use %v, the web UI reports %ds", resolved, tt.want)
			}
		})
	}
}

// TestPollingZeroValueIsUsable covers a Server built without polling settings,
// which is every Server assembled as a struct literal.
func TestPollingZeroValueIsUsable(t *testing.T) {
	var polling PollingConfig

	if got := polling.Effective(nil); got != DefaultPollInterval {
		t.Errorf("Effective(nil) = %d, want %d", got, DefaultPollInterval)
	}
	if got := polling.Effective(seconds(15)); got != 15 {
		t.Errorf("Effective(15) = %d, want 15", got)
	}

	want := time.Duration(DefaultSNMPTimeout*(DefaultSNMPRetries+1)) * time.Second
	if got := polling.SNMPBudget(); got != want {
		t.Errorf("SNMPBudget = %v, want %v", got, want)
	}
}

// TestPollingEffectiveIgnoresUnschedulableValues checks the reporting path
// against values the write path rejects, since a database written by an older
// or broken client must not make a status page render a negative schedule.
func TestPollingEffectiveIgnoresUnschedulableValues(t *testing.T) {
	polling := shippedPolling

	for _, interval := range []int{0, -1, -3600} {
		if got := polling.Effective(&interval); got != polling.DefaultInterval {
			t.Errorf("Effective(%d) = %d, want the default %d",
				interval, got, polling.DefaultInterval)
		}
	}
}

func TestPollingSNMPBudget(t *testing.T) {
	tests := []struct {
		name    string
		polling PollingConfig
		want    time.Duration
	}{
		{"the shipped configuration", shippedPolling, 40 * time.Second},
		{"a slower agent with fewer retries", PollingConfig{SNMPTimeout: 20, SNMPRetries: 1}, 40 * time.Second},
		{"a partly-specified configuration fills the rest from the defaults",
			PollingConfig{SNMPTimeout: 5}, 20 * time.Second},
		{"unset falls back to the config defaults", PollingConfig{}, 40 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.polling.SNMPBudget(); got != tt.want {
				t.Errorf("SNMPBudget = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestPollingWarnsBelowTheSNMPBudget pins the decision that a short interval is
// accepted with a warning rather than refused, and that the warning explains
// the arithmetic behind it.
func TestPollingWarnsBelowTheSNMPBudget(t *testing.T) {
	polling := shippedPolling

	warning := polling.Warning(seconds(10))
	if warning == "" {
		t.Fatal("a 10s interval against a 40s SNMP budget produced no warning")
	}
	for _, want := range []string{"10s", "40s", "snmp_timeout 10s x 4 attempts"} {
		if !strings.Contains(warning, want) {
			t.Errorf("warning %q does not mention %q", warning, want)
		}
	}
}

func TestPollingDoesNotWarnWhenTheIntervalFits(t *testing.T) {
	polling := shippedPolling

	tests := []struct {
		name     string
		interval *int
	}{
		{"exactly the budget", seconds(40)},
		{"longer than the budget", seconds(300)},
		{"inherited, which is the poller's problem to schedule", nil},
		{"unschedulable values are the validator's business, not the warner's", seconds(0)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if warning := polling.Warning(tt.interval); warning != "" {
				t.Errorf("Warning = %q, want none", warning)
			}
		})
	}
}

func TestNewDeviceView(t *testing.T) {
	t.Run("a device with its own schedule", func(t *testing.T) {
		view := newDeviceView(storage.Device{ID: 1, Name: "core-01", PollInterval: seconds(120)},
			shippedPolling)

		if view.EffectivePollInterval != 120 {
			t.Errorf("effective = %d, want 120", view.EffectivePollInterval)
		}
		if view.PollInterval == nil || *view.PollInterval != 120 {
			t.Errorf("poll_interval = %v, want 120", view.PollInterval)
		}
		if len(view.Warnings) != 0 {
			t.Errorf("warnings = %v, want none", view.Warnings)
		}
		if view.Name != "core-01" {
			t.Errorf("the stored record did not come through: %+v", view)
		}
	})

	t.Run("a device that inherits", func(t *testing.T) {
		view := newDeviceView(storage.Device{ID: 2, Name: "pdu-03"}, shippedPolling)

		if view.PollInterval != nil {
			t.Errorf("poll_interval = %d, want absent", *view.PollInterval)
		}
		if view.EffectivePollInterval != 30 {
			t.Errorf("effective = %d, want the global default 30", view.EffectivePollInterval)
		}
	})

	t.Run("a device whose schedule undercuts the SNMP budget", func(t *testing.T) {
		view := newDeviceView(storage.Device{ID: 3, Name: "bastion", PollInterval: seconds(5)},
			shippedPolling)

		if len(view.Warnings) != 1 {
			t.Fatalf("warnings = %v, want exactly one", view.Warnings)
		}
		if view.EffectivePollInterval != 5 {
			t.Errorf("effective = %d, want 5; a warned interval is still in force",
				view.EffectivePollInterval)
		}
	})
}

// TestDeviceViewIsAdditive is the backward-compatibility guard for the existing
// consumers of GET /api/devices/{id}: every field the response has always had
// must still be there, at the same name and the same level.
func TestDeviceViewIsAdditive(t *testing.T) {
	created := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	view := newDeviceView(storage.Device{
		ID: 7, Name: "core-01", Address: "192.0.2.1", SNMPVersion: "v2c",
		Community: "public", Port: 161, CreatedAt: created,
	}, shippedPolling)

	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(encoded, &body); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	for _, key := range []string{"id", "name", "address", "snmp_version", "community",
		"port", "created_at", "effective_poll_interval"} {
		if _, ok := body[key]; !ok {
			t.Errorf("the response lost %q: %s", key, encoded)
		}
	}
	// A device that inherits carries no poll_interval, which is how a client
	// tells "inherits" from "explicitly 30".
	if _, ok := body["poll_interval"]; ok {
		t.Errorf("a device that inherits reported poll_interval: %s", encoded)
	}

	// And the old type still decodes it, which is what the existing web UI
	// and CLI do.
	var device storage.Device
	if err := json.Unmarshal(encoded, &device); err != nil {
		t.Fatalf("an existing consumer could not decode the response: %v", err)
	}
	if device.ID != 7 || device.Name != "core-01" || !device.CreatedAt.Equal(created) {
		t.Errorf("decoded %+v, want the stored record unchanged", device)
	}
}

func TestNewDeviceViewsPreservesOrder(t *testing.T) {
	views := newDeviceViews([]storage.Device{
		{ID: 1, Name: "a", PollInterval: seconds(60)},
		{ID: 2, Name: "b"},
		{ID: 3, Name: "c", PollInterval: seconds(5)},
	}, shippedPolling)

	if len(views) != 3 {
		t.Fatalf("got %d views, want 3", len(views))
	}
	if views[0].Name != "a" || views[1].Name != "b" || views[2].Name != "c" {
		t.Errorf("order changed: %v %v %v", views[0].Name, views[1].Name, views[2].Name)
	}
	if views[1].EffectivePollInterval != 30 {
		t.Errorf("the inheriting device reports %d, want 30", views[1].EffectivePollInterval)
	}
	if len(views[2].Warnings) == 0 {
		t.Error("the 5s device carries no warning")
	}
}

func TestNewDeviceViewsOfNothing(t *testing.T) {
	views := newDeviceViews(nil, shippedPolling)
	if views == nil {
		t.Fatal("newDeviceViews(nil) = nil; an empty listing must still encode as []")
	}
	if len(views) != 0 {
		t.Errorf("got %d views, want 0", len(views))
	}
}

// TestDevicePollIntervalOverHTTP walks the interval through the endpoints the
// web UI and the CLI actually call.
func TestDevicePollIntervalOverHTTP(t *testing.T) {
	s := newTestServer(t)
	s.pollingConfig = shippedPolling
	poller := &recordingPoller{}
	s.pollerClient = poller

	rec := do(t, s, http.MethodPost, "/api/devices",
		`{"name":"core-01","address":"192.0.2.1","snmp_version":"v2c","community":"public","poll_interval":120}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201 (body %q)", rec.Code, rec.Body.String())
	}

	var created DeviceView
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("create response: %v", err)
	}
	if created.PollInterval == nil || *created.PollInterval != 120 {
		t.Fatalf("created poll_interval = %v, want 120", created.PollInterval)
	}
	if created.EffectivePollInterval != 120 {
		t.Errorf("created effective_poll_interval = %d, want 120", created.EffectivePollInterval)
	}

	rec = do(t, s, http.MethodGet, "/api/devices/"+itoa(created.ID), "")
	var fetched DeviceView
	if err := json.Unmarshal(rec.Body.Bytes(), &fetched); err != nil {
		t.Fatalf("get response: %v", err)
	}
	if fetched.PollInterval == nil || *fetched.PollInterval != 120 {
		t.Errorf("fetched poll_interval = %v, want 120", fetched.PollInterval)
	}

	// Clearing it hands the device back to the global default.
	rec = do(t, s, http.MethodPut, "/api/devices/"+itoa(created.ID),
		`{"name":"core-01","address":"192.0.2.1","snmp_version":"v2c","community":"public"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}

	var updated DeviceView
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("update response: %v", err)
	}
	if updated.PollInterval != nil {
		t.Errorf("poll_interval = %d, want absent after being cleared", *updated.PollInterval)
	}
	if updated.EffectivePollInterval != 30 {
		t.Errorf("effective_poll_interval = %d, want the global default 30", updated.EffectivePollInterval)
	}

	// A listing reports the same thing.
	rec = do(t, s, http.MethodGet, "/api/devices", "")
	var list struct {
		Devices []DeviceView `json:"devices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("list response: %v", err)
	}
	if len(list.Devices) != 1 {
		t.Fatalf("listed %d devices, want 1", len(list.Devices))
	}
	if list.Devices[0].EffectivePollInterval != 30 {
		t.Errorf("listed effective_poll_interval = %d, want 30", list.Devices[0].EffectivePollInterval)
	}
}

// TestDeviceWriteRejectsAnUnschedulableInterval checks that a bad interval is
// refused with a 400 that says what to do, and that nothing was stored and
// nothing was pushed to the poller.
func TestDeviceWriteRejectsAnUnschedulableInterval(t *testing.T) {
	s := newTestServer(t)
	poller := &recordingPoller{}
	s.pollerClient = poller

	existing := storeDevice(t, s, "core-01", "192.0.2.1")

	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{
			"create with a zero interval", http.MethodPost, "/api/devices",
			`{"name":"bastion","address":"192.0.2.14","snmp_version":"v2c","poll_interval":0}`,
		},
		{
			"create with a negative interval", http.MethodPost, "/api/devices",
			`{"name":"bastion","address":"192.0.2.14","snmp_version":"v2c","poll_interval":-30}`,
		},
		{
			"update with a zero interval", http.MethodPut, "/api/devices/" + itoa(existing),
			`{"name":"core-01","address":"192.0.2.1","snmp_version":"v2c","poll_interval":0}`,
		},
		{
			"update with a negative interval", http.MethodPut, "/api/devices/" + itoa(existing),
			`{"name":"core-01","address":"192.0.2.1","snmp_version":"v2c","poll_interval":-1}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, s, tt.method, tt.path, tt.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %q)", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "inherit the global default") {
				t.Errorf("body %q does not say how to inherit the default", rec.Body.String())
			}
		})
	}

	if got := poller.calls(&poller.registered); len(got) != 0 {
		t.Errorf("the poller was told about %v; a rejected device must never reach it", got)
	}
	if got := poller.calls(&poller.updated); len(got) != 0 {
		t.Errorf("the poller was told about %v; a rejected update must never reach it", got)
	}
	if rec := do(t, s, http.MethodGet, "/api/devices/9999", ""); rec.Code != http.StatusNotFound {
		t.Errorf("a rejected device was stored: %d", rec.Code)
	}
}

// TestDeviceWriteWarnsBelowTheSNMPBudget pins the other half of the decision:
// a short interval is stored, pushed and reported, with the reason attached.
func TestDeviceWriteWarnsBelowTheSNMPBudget(t *testing.T) {
	s := newTestServer(t)
	s.pollingConfig = shippedPolling
	poller := &recordingPoller{}
	s.pollerClient = poller

	rec := do(t, s, http.MethodPost, "/api/devices",
		`{"name":"bastion","address":"192.0.2.14","snmp_version":"v2c","poll_interval":5}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; a short interval is a warning, not a refusal", rec.Code)
	}

	var created DeviceView
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("create response: %v", err)
	}
	if len(created.Warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one", created.Warnings)
	}
	if !strings.Contains(created.Warnings[0], "SNMP timeout budget") {
		t.Errorf("warning %q does not name the budget it undercuts", created.Warnings[0])
	}
	if got := poller.calls(&poller.registered); len(got) != 1 || got[0] != "bastion" {
		t.Errorf("poller registrations = %v, want [bastion]", got)
	}
}

func TestWriteDeviceWriteError(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{
			"a rejected interval is the caller's input",
			&storage.InvalidPollIntervalError{Device: "bastion", Interval: 0},
			http.StatusBadRequest,
		},
		{
			"anything else is the server's problem",
			errors.New("pebble: write failed"),
			http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeDeviceWriteError(rec, tt.err)
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if !strings.Contains(rec.Body.String(), tt.err.Error()) {
				t.Errorf("body %q does not carry the reason %q", rec.Body.String(), tt.err)
			}
		})
	}
}

// TestFleetStatusCarriesTheInterval covers the fleet overview's own copy of the
// schedule. It comes from the device record rather than the poller, so it is
// there even for a device the poller has never heard of — which is exactly the
// row an operator is trying to understand.
func TestFleetStatusCarriesTheInterval(t *testing.T) {
	s := newTestServer(t)
	s.pollingConfig = shippedPolling
	s.pollerClient = &recordingPoller{}

	scheduled := &storage.Device{
		Name: "core-01", Address: "192.0.2.1", SNMPVersion: "v2c",
		Community: "public", PollInterval: seconds(120),
	}
	if err := s.storage.CreateDevice(t.Context(), scheduled); err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}
	inheriting := &storage.Device{
		Name: "core-02", Address: "192.0.2.2", SNMPVersion: "v2c", Community: "public",
	}
	if err := s.storage.CreateDevice(t.Context(), inheriting); err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}

	rec := do(t, s, http.MethodGet, "/api/devices/status", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}

	var body FleetStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("fleet response: %v", err)
	}
	if len(body.Devices) != 2 {
		t.Fatalf("got %d rows, want 2", len(body.Devices))
	}

	rows := map[string]FleetDeviceStatus{}
	for _, row := range body.Devices {
		rows[row.Name] = row
	}

	own := rows["core-01"]
	if own.PollInterval == nil || *own.PollInterval != 120 {
		t.Errorf("core-01 poll_interval = %v, want 120", own.PollInterval)
	}
	if own.EffectivePollInterval != 120 {
		t.Errorf("core-01 effective = %d, want 120", own.EffectivePollInterval)
	}

	inherited := rows["core-02"]
	if inherited.PollInterval != nil {
		t.Errorf("core-02 poll_interval = %d, want absent", *inherited.PollInterval)
	}
	if inherited.EffectivePollInterval != 30 {
		t.Errorf("core-02 effective = %d, want the global default 30", inherited.EffectivePollInterval)
	}
}

// TestFleetStatusReportsTheIntervalOfAnUnknownDevice is the case the row's own
// copy exists for: the poller cannot be reached, so nothing is known about the
// device's health, but its configured schedule is still reportable.
func TestFleetStatusReportsTheIntervalOfAnUnknownDevice(t *testing.T) {
	s := newTestServer(t)
	s.pollingConfig = shippedPolling
	s.pollerClient = &recordingPoller{err: errors.New("connection refused")}

	device := &storage.Device{
		Name: "bastion", Address: "192.0.2.14", SNMPVersion: "v2c",
		Community: "public", PollInterval: seconds(300),
	}
	if err := s.storage.CreateDevice(t.Context(), device); err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}

	rec := do(t, s, http.MethodGet, "/api/devices/status", "")
	var body FleetStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("fleet response: %v", err)
	}
	if len(body.Devices) != 1 {
		t.Fatalf("got %d rows, want 1", len(body.Devices))
	}

	row := body.Devices[0]
	if row.State != StateUnknown {
		t.Errorf("state = %q, want %q", row.State, StateUnknown)
	}
	if row.EffectivePollInterval != 300 {
		t.Errorf("effective = %d, want 300 even with no status", row.EffectivePollInterval)
	}
}
