package cli

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// scheduledDevice is testDeviceFixture with a schedule of its own.
func scheduledDevice(seconds int) client.Device {
	device := testDeviceFixture()
	client.SetPollInterval(&device, seconds)
	device.EffectivePollInterval = seconds
	return device
}

// inheritingDevice is testDeviceFixture as it reads before anyone sets an
// interval: no schedule of its own, and the server's default reported alongside.
func inheritingDevice(defaultSeconds int) client.Device {
	device := testDeviceFixture()
	device.EffectivePollInterval = defaultSeconds
	return device
}

func TestDescribePollInterval(t *testing.T) {
	tests := []struct {
		name   string
		device client.Device
		want   string
	}{
		{"a device with its own schedule", scheduledDevice(120), "2m0s"},
		{"a device that inherits", inheritingDevice(30), "30s (default)"},
		{
			"a device explicitly set to the same number as the default",
			scheduledDevice(30),
			"30s",
		},
		{
			"a server that reported no default at all",
			client.Device{},
			"default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := describePollInterval(tt.device); got != tt.want {
				t.Errorf("describePollInterval = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestDeviceListShowsTheInterval checks the column an operator scans to see
// which devices are on their own schedule.
func TestDeviceListShowsTheInterval(t *testing.T) {
	own := scheduledDevice(120)
	inherited := inheritingDevice(30)
	inherited.ID = 9
	inherited.Name = "core-02"

	mux := http.NewServeMux()
	mux.HandleFunc("/api/devices", deviceListHandler(t, own, inherited))

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newDeviceCommand(env), "list"); err != nil {
		t.Fatalf("device list: %v", err)
	}

	got := out.String()
	for _, want := range []string{"INTERVAL", "2m0s", "30s (default)"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

// TestDeviceShowExplainsTheInterval covers the detail view, including the
// warning the server attaches to a short interval.
func TestDeviceShowExplainsTheInterval(t *testing.T) {
	device := scheduledDevice(5)
	device.Warnings = []string{"poll interval 5s is shorter than this device's SNMP timeout budget of 40s"}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/devices", deviceListHandler(t, device))
	mux.HandleFunc("/api/devices/7", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, device)
	})
	mux.HandleFunc("/api/devices/7/mibs", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"direct_mibs": []any{}, "all_mibs": []any{}})
	})
	mux.HandleFunc("/api/devices/7/profiles", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"profiles": []any{}})
	})

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newDeviceCommand(env), "show", "core-01"); err != nil {
		t.Fatalf("device show: %v", err)
	}

	got := out.String()
	for _, want := range []string{"Poll interval", "5s", "Warning:", "SNMP timeout budget"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

// TestDeviceAddSendsTheInterval is the create half of the flag: what --poll-interval
// puts on the wire, and what the command reports back.
func TestDeviceAddSendsTheInterval(t *testing.T) {
	var body map[string]any

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/devices", func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("reading body: %v", err)
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decoding body %q: %v", raw, err)
		}
		w.WriteHeader(http.StatusCreated)
		respondJSON(t, w, scheduledDevice(90))
	})

	env, out := newFakeEnv(t, mux)
	err := runCommand(t, newDeviceCommand(env), "add",
		"--name", "core-01", "--address", "198.51.100.252", "--poll-interval", "90")
	if err != nil {
		t.Fatalf("device add: %v", err)
	}

	if got, ok := body["poll_interval"]; !ok {
		t.Fatalf("the request carried no poll_interval: %v", body)
	} else if got != float64(90) {
		t.Errorf("poll_interval = %v, want 90", got)
	}
	if !strings.Contains(out.String(), "1m30s") {
		t.Errorf("output does not report the schedule:\n%s", out)
	}
}

// TestDeviceAddWithoutTheFlagInherits is the other half: a device added with no
// --poll-interval must not pin an interval, so it follows the server's default
// and keeps following it when the default changes.
func TestDeviceAddWithoutTheFlagInherits(t *testing.T) {
	var body map[string]any

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/devices", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decoding body %q: %v", raw, err)
		}
		w.WriteHeader(http.StatusCreated)
		respondJSON(t, w, inheritingDevice(30))
	})

	env, out := newFakeEnv(t, mux)
	err := runCommand(t, newDeviceCommand(env), "add",
		"--name", "core-01", "--address", "198.51.100.252")
	if err != nil {
		t.Fatalf("device add: %v", err)
	}

	if _, ok := body["poll_interval"]; ok {
		t.Errorf("the request pinned poll_interval = %v; it should have been omitted", body["poll_interval"])
	}
	if !strings.Contains(out.String(), "30s (default)") {
		t.Errorf("output does not report the inherited schedule:\n%s", out)
	}
}

// TestDeviceAddRejectsANegativeInterval checks the value never leaves the
// process, which is what keeps a typo from reaching the poller.
func TestDeviceAddRejectsANegativeInterval(t *testing.T) {
	var requests int

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusCreated)
	})

	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newDeviceCommand(env), "add",
		"--name", "bastion", "--address", "192.0.2.14", "--poll-interval", "-30")

	var invalid *client.PollIntervalError
	if !errors.As(err, &invalid) {
		t.Fatalf("device add = %v (%T), want *client.PollIntervalError", err, err)
	}
	if invalid.Interval != -30 {
		t.Errorf("Interval = %d, want -30", invalid.Interval)
	}
	if requests != 0 {
		t.Errorf("the server was called %d times; a rejected interval must not be sent", requests)
	}
}

// TestDeviceUpdateChangesOnlyTheInterval is the regression for the class of bug
// where changing one field silently resets the rest. The fake API answers the
// GET with a fully-populated device; the PUT must carry all of it back.
func TestDeviceUpdateChangesOnlyTheInterval(t *testing.T) {
	current := client.Device{
		ID: 7, Name: "core-01", Address: "198.51.100.252",
		SNMPVersion: "3", Community: "secret", Port: 1161,
		EffectivePollInterval: 30,
	}

	var body map[string]any

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/devices", deviceListHandler(t, current))
	mux.HandleFunc("GET /api/devices/7", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, current)
	})
	mux.HandleFunc("PUT /api/devices/7", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decoding body %q: %v", raw, err)
		}
		respondJSON(t, w, scheduledDevice(600))
	})

	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newDeviceCommand(env), "update", "core-01", "--poll-interval", "600")
	if err != nil {
		t.Fatalf("device update: %v", err)
	}

	want := map[string]any{
		"name":          "core-01",
		"address":       "198.51.100.252",
		"snmp_version":  "3",
		"community":     "secret",
		"port":          float64(1161),
		"poll_interval": float64(600),
	}
	for key, value := range want {
		if body[key] != value {
			t.Errorf("%s = %v, want %v (whole body: %v)", key, body[key], value, body)
		}
	}
}

// TestDeviceUpdateZeroRestoresTheDefault covers the documented meaning of
// --poll-interval 0: hand the device back to the global default rather than ask
// for a zero-second schedule.
func TestDeviceUpdateZeroRestoresTheDefault(t *testing.T) {
	current := scheduledDevice(600)

	var body map[string]any

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/devices", deviceListHandler(t, current))
	mux.HandleFunc("GET /api/devices/7", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, current)
	})
	mux.HandleFunc("PUT /api/devices/7", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decoding body %q: %v", raw, err)
		}
		respondJSON(t, w, inheritingDevice(30))
	})

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newDeviceCommand(env), "update", "core-01", "--poll-interval", "0"); err != nil {
		t.Fatalf("device update: %v", err)
	}

	if _, ok := body["poll_interval"]; ok {
		t.Errorf("the request still carries poll_interval = %v; 0 must clear it", body["poll_interval"])
	}
	if !strings.Contains(out.String(), "30s (default)") {
		t.Errorf("output does not report the restored default:\n%s", out)
	}
}

// TestDeviceUpdateWithoutTheFlagKeepsTheSchedule is the complement: a command
// that changes something else must not disturb the interval.
func TestDeviceUpdateWithoutTheFlagKeepsTheSchedule(t *testing.T) {
	current := scheduledDevice(600)

	var body map[string]any

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/devices", deviceListHandler(t, current))
	mux.HandleFunc("GET /api/devices/7", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, current)
	})
	mux.HandleFunc("PUT /api/devices/7", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decoding body %q: %v", raw, err)
		}
		respondJSON(t, w, scheduledDevice(600))
	})

	env, _ := newFakeEnv(t, mux)
	if err := runCommand(t, newDeviceCommand(env), "update", "core-01",
		"--community", "private"); err != nil {
		t.Fatalf("device update: %v", err)
	}

	if body["poll_interval"] != float64(600) {
		t.Errorf("poll_interval = %v, want the stored 600 to survive an unrelated change",
			body["poll_interval"])
	}
	if body["community"] != "private" {
		t.Errorf("community = %v, want private", body["community"])
	}
}

// TestDeviceUpdateRejectsANegativeInterval is the bad-input case on the update
// path: the device is fetched, but nothing is written.
func TestDeviceUpdateRejectsANegativeInterval(t *testing.T) {
	current := scheduledDevice(600)
	var writes int

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/devices", deviceListHandler(t, current))
	mux.HandleFunc("GET /api/devices/7", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, current)
	})
	mux.HandleFunc("PUT /api/devices/7", func(w http.ResponseWriter, _ *http.Request) {
		writes++
		respondJSON(t, w, current)
	})

	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newDeviceCommand(env), "update", "core-01", "--poll-interval", "-1")

	var invalid *client.PollIntervalError
	if !errors.As(err, &invalid) {
		t.Fatalf("device update = %v (%T), want *client.PollIntervalError", err, err)
	}
	if writes != 0 {
		t.Errorf("the device was written %d times despite an invalid interval", writes)
	}
}

// TestDeviceListJSONCarriesTheInterval keeps the --json document in step with
// the table, since scripts read that and not the columns.
func TestDeviceListJSONCarriesTheInterval(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/devices", deviceListHandler(t, scheduledDevice(120), inheritingDevice(30)))

	env, out := newFakeEnv(t, mux)
	env.Opts.JSON = true
	if err := runCommand(t, newDeviceCommand(env), "list"); err != nil {
		t.Fatalf("device list: %v", err)
	}

	var page struct {
		Items []client.Device `json:"items"`
	}
	if err := json.Unmarshal(out.Bytes(), &page); err != nil {
		t.Fatalf("decoding %s: %v", out, err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("got %d items, want 2", len(page.Items))
	}

	if seconds, own := client.PollIntervalOf(page.Items[0]); !own || seconds != 120 {
		t.Errorf("first item = (%d, %v), want (120, true)", seconds, own)
	}
	if _, own := client.PollIntervalOf(page.Items[1]); own {
		t.Error("the second item reports a schedule of its own; it inherits")
	}
	if page.Items[1].EffectivePollInterval != 30 {
		t.Errorf("second item effective = %d, want 30", page.Items[1].EffectivePollInterval)
	}
}

// TestDeviceAddPrintsTheServersWarning makes sure a trade-off the server
// accepted is not silently swallowed by the CLI: the operator set a short
// interval and has to be told what it costs.
func TestDeviceAddPrintsTheServersWarning(t *testing.T) {
	created := scheduledDevice(5)
	created.Warnings = []string{
		"poll interval 5s is shorter than this device's SNMP timeout budget of 40s",
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/devices", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		respondJSON(t, w, created)
	})

	env, out := newFakeEnv(t, mux)
	err := runCommand(t, newDeviceCommand(env), "add",
		"--name", "bastion", "--address", "192.0.2.14", "--poll-interval", "5")
	if err != nil {
		t.Fatalf("device add: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "Warning:") || !strings.Contains(got, "SNMP timeout budget") {
		t.Errorf("output does not carry the server's warning:\n%s", got)
	}
}
