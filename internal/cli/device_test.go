package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// deviceListHandler serves the device catalog, which the CLI reads whenever
// it has to turn a name into an ID.
func deviceListHandler(t *testing.T, devices ...client.Device) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"devices": devices})
	}
}

func TestDeviceListRendersTable(t *testing.T) {
	second := client.Device{
		ID: 9, Name: "core-02", Address: "198.51.100.253",
		SNMPVersion: "2c", Port: 161,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/devices", deviceListHandler(t, testDeviceFixture(), second))

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newDeviceCommand(env), "list"); err != nil {
		t.Fatalf("device list: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"ID", "NAME", "ADDRESS", "PORT", "VERSION", "ADDED",
		"core-01", "198.51.100.252", "core-02", "198.51.100.253", "161",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestDeviceListEmptySuggestsAdd(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/devices", deviceListHandler(t))

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newDeviceCommand(env), "list"); err != nil {
		t.Fatalf("device list: %v", err)
	}
	// An empty fleet is a normal first-run state, so point at the next step
	// rather than printing an empty table.
	if !strings.Contains(out.String(), "netmon device add") {
		t.Errorf("empty listing should suggest adding a device, got:\n%s", out)
	}
}

func TestDeviceListServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/devices", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "database unavailable", http.StatusInternalServerError)
	})

	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newDeviceCommand(env), "list")
	if err == nil {
		t.Fatal("expected an error from a 500 response")
	}

	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("got %v, want a 500 APIError", err)
	}
}

func TestDeviceListJSON(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/devices", deviceListHandler(t, testDeviceFixture()))

	env, out := newFakeEnv(t, mux)
	env.Opts.JSON = true
	if err := runCommand(t, newDeviceCommand(env), "list"); err != nil {
		t.Fatalf("device list --json: %v", err)
	}

	page := decodePage[client.Device](t, out.Bytes())
	if len(page.Items) != 1 || page.Items[0].Name != "core-01" {
		t.Errorf("unexpected payload: %+v", page.Items)
	}
	// An untruncated listing still reports its counts, so a consumer never has
	// to guess whether it received everything.
	if page.Total != 1 || page.Matched != 1 || page.Shown != 1 || page.Truncated {
		t.Errorf("counts = %+v, want a complete 1-row listing", page.listCounts)
	}
}

func TestDeviceShowIncludesMIBsAndProfiles(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/devices", deviceListHandler(t, testDeviceFixture()))
	mux.HandleFunc("/api/devices/7", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, testDeviceFixture())
	})
	mux.HandleFunc("/api/devices/7/mibs", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, client.DeviceMIBs{
			DirectMIBs: []client.AssignedMIB{enabledMIB(1, "IF-MIB", 91)},
			AllMIBs: []client.AssignedMIB{
				enabledMIB(1, "IF-MIB", 91),
				enabledMIB(2, "CISCO-PROCESS-MIB", 190),
			},
		})
	})
	mux.HandleFunc("/api/devices/7/profiles", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{
			"profiles": []client.Profile{{ID: 3, Name: "asr1000-core"}},
		})
	})

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newDeviceCommand(env), "show", "core-01"); err != nil {
		t.Fatalf("device show: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"core-01", "198.51.100.252", "asr1000-core",
		"IF-MIB", "CISCO-PROCESS-MIB",
		// A MIB reached through a profile must be distinguishable from one
		// attached directly, since only the latter can be removed here.
		"direct", "profile",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestDeviceShowSurvivesMissingMIBsAndProfiles(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/devices", deviceListHandler(t, testDeviceFixture()))
	mux.HandleFunc("/api/devices/7", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, testDeviceFixture())
	})
	mux.HandleFunc("/api/devices/7/mibs", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "mib store unavailable", http.StatusInternalServerError)
	})
	mux.HandleFunc("/api/devices/7/profiles", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "profile store unavailable", http.StatusInternalServerError)
	})

	env, out := newFakeEnv(t, mux)
	// The device's own configuration is still worth showing when its
	// associations cannot be read.
	if err := runCommand(t, newDeviceCommand(env), "show", "core-01"); err != nil {
		t.Fatalf("device show: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "core-01") {
		t.Errorf("device details should still print:\n%s", got)
	}
	if !strings.Contains(got, "unavailable") {
		t.Errorf("failed lookups should be reported inline:\n%s", got)
	}
}

func TestDeviceShowUnresolvable(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/devices", deviceListHandler(t, testDeviceFixture()))

	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newDeviceCommand(env), "show", "ghost")
	if err == nil {
		t.Fatal("expected an error for an unknown device")
	}

	var notFound *client.NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("got %T, want *client.NotFoundError", err)
	}
}

func TestDeviceAddSendsConfiguredFields(t *testing.T) {
	var sent client.Device

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/devices", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		sent.ID = 12
		w.WriteHeader(http.StatusCreated)
		respondJSON(t, w, sent)
	})

	env, out := newFakeEnv(t, mux)
	err := runCommand(t, newDeviceCommand(env), "add",
		"--name", "core-01", "--address", "198.51.100.252",
		"--version", "2c", "--community", "public", "--port", "1161")
	if err != nil {
		t.Fatalf("device add: %v", err)
	}

	if sent.Name != "core-01" || sent.Address != "198.51.100.252" {
		t.Errorf("server received %+v", sent)
	}
	if sent.Community != "public" || sent.SNMPVersion != "2c" || sent.Port != 1161 {
		t.Errorf("credentials not forwarded: %+v", sent)
	}
	if !strings.Contains(out.String(), "id 12") {
		t.Errorf("output should confirm the new ID:\n%s", out)
	}
}

func TestDeviceAddRequiresNameAndAddress(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"no address", []string{"add", "--name", "core-01"}},
		{"no name", []string{"add", "--address", "198.51.100.252"}},
		{"neither", []string{"add"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/", func(_ http.ResponseWriter, r *http.Request) {
				t.Errorf("unexpected request %s %s for an incomplete device", r.Method, r.URL.Path)
			})

			env, _ := newFakeEnv(t, mux)
			if err := runCommand(t, newDeviceCommand(env), tt.args...); err == nil {
				t.Error("expected an error for a device missing required fields")
			}
		})
	}
}

func TestDeviceAddServerRejects(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/devices", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "device name already exists", http.StatusConflict)
	})

	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newDeviceCommand(env), "add",
		"--name", "core-01", "--address", "198.51.100.252")
	if err == nil {
		t.Fatal("expected an error for a duplicate name")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error lost the server's explanation: %v", err)
	}
}

// TestDeviceUpdateOnlyOverlaysChangedFlags is the important one: the API
// replaces the whole record, so an unset flag must not blank a stored value.
func TestDeviceUpdateOnlyOverlaysChangedFlags(t *testing.T) {
	var sent client.Device

	mux := http.NewServeMux()
	mux.HandleFunc("/api/devices", deviceListHandler(t, testDeviceFixture()))
	mux.HandleFunc("GET /api/devices/7", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, testDeviceFixture())
	})
	mux.HandleFunc("PUT /api/devices/7", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		respondJSON(t, w, sent)
	})

	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newDeviceCommand(env), "update", "core-01",
		"--community", "newsecret")
	if err != nil {
		t.Fatalf("device update: %v", err)
	}

	if sent.Community != "newsecret" {
		t.Errorf("community = %q, want newsecret", sent.Community)
	}
	// Everything the user did not pass must survive untouched.
	if sent.Name != "core-01" {
		t.Errorf("name = %q, want it preserved", sent.Name)
	}
	if sent.Address != "198.51.100.252" {
		t.Errorf("address = %q, want it preserved", sent.Address)
	}
	if sent.Port != 161 {
		t.Errorf("port = %d, want it preserved", sent.Port)
	}
	if sent.SNMPVersion != "2c" {
		t.Errorf("snmp version = %q, want it preserved", sent.SNMPVersion)
	}
}

func TestDeviceUpdateUnresolvable(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/devices", deviceListHandler(t))

	env, _ := newFakeEnv(t, mux)
	if err := runCommand(t, newDeviceCommand(env), "update", "ghost", "--port", "1161"); err == nil {
		t.Error("expected an error for an unknown device")
	}
}

// TestDeviceDeleteRequiresForce guards a destructive action: deleting stops
// collection and drops the device's assignments.
func TestDeviceDeleteRequiresForce(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/devices", deviceListHandler(t, testDeviceFixture()))
	mux.HandleFunc("GET /api/devices/7", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, testDeviceFixture())
	})
	mux.HandleFunc("DELETE /api/devices/7", func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("delete must not be issued without --force")
	})

	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newDeviceCommand(env), "delete", "core-01")
	if err == nil {
		t.Fatal("expected a refusal without --force")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("refusal should name the flag that unlocks it: %v", err)
	}
}

func TestDeviceDeleteWithForce(t *testing.T) {
	deleted := false

	mux := http.NewServeMux()
	mux.HandleFunc("/api/devices", deviceListHandler(t, testDeviceFixture()))
	mux.HandleFunc("GET /api/devices/7", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, testDeviceFixture())
	})
	mux.HandleFunc("DELETE /api/devices/7", func(w http.ResponseWriter, _ *http.Request) {
		deleted = true
		w.WriteHeader(http.StatusNoContent)
	})

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newDeviceCommand(env), "delete", "core-01", "--force"); err != nil {
		t.Fatalf("device delete: %v", err)
	}
	if !deleted {
		t.Error("delete request was never issued")
	}
	if !strings.Contains(out.String(), "core-01") {
		t.Errorf("output should name what was deleted:\n%s", out)
	}
}

func TestDeviceTestUsesStoredCredentials(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/devices", deviceListHandler(t, testDeviceFixture()))
	mux.HandleFunc("POST /api/devices/7/test-snmp", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, client.SNMPTestResponse{
			Success: true, Duration: "4ms",
			SysName:  "core-01",
			SysDescr: "Cisco IOS Software, ASR1000",
			Results: []client.SNMPTestResult{
				{OID: "1.3.6.1.2.1.1.5.0", Name: "sysName", Type: "OctetString", Value: "core-01"},
			},
		})
	})

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newDeviceCommand(env), "test", "core-01"); err != nil {
		t.Fatalf("device test: %v", err)
	}

	got := out.String()
	for _, want := range []string{"OK", "4ms", "core-01", "Cisco IOS Software", "sysName"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestDeviceTestAdHocAddress(t *testing.T) {
	var sent client.SNMPTestRequest

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/snmp/test", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		respondJSON(t, w, client.SNMPTestResponse{Success: true, Duration: "3ms"})
	})

	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newDeviceCommand(env), "test",
		"--address", "198.51.100.254", "--community", "secret", "--port", "1161")
	if err != nil {
		t.Fatalf("device test --address: %v", err)
	}

	if sent.Address != "198.51.100.254" || sent.Community != "secret" || sent.Port != 1161 {
		t.Errorf("probe request = %+v", sent)
	}
}

func TestDeviceTestNeedsATarget(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s with no target", r.Method, r.URL.Path)
	})

	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newDeviceCommand(env), "test")
	if err == nil {
		t.Fatal("expected an error with neither a device nor --address")
	}
	if !strings.Contains(err.Error(), "--address") {
		t.Errorf("error should point at the alternative: %v", err)
	}
}

func TestDeviceTestReportsFailure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/snmp/test", func(w http.ResponseWriter, _ *http.Request) {
		// A failed probe is a successful API call reporting a bad result, so
		// the command must surface it rather than exiting silently.
		respondJSON(t, w, client.SNMPTestResponse{
			Success: false, Error: "request timeout after 3 retries",
		})
	})

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newDeviceCommand(env), "test", "--address", "198.51.100.99"); err != nil {
		t.Fatalf("device test: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "FAILED") || !strings.Contains(got, "request timeout") {
		t.Errorf("a failed probe must be reported:\n%s", got)
	}
}

func TestDeviceStatusSingleDeviceCarriesAddress(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/devices", deviceListHandler(t, testDeviceFixture()))
	mux.HandleFunc("GET /api/devices/7", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, testDeviceFixture())
	})
	mux.HandleFunc("/api/devices/7/status", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, testDeviceStatus())
	})

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newDeviceCommand(env), "status", "core-01"); err != nil {
		t.Fatalf("device status: %v", err)
	}

	got := out.String()
	// The poller's status carries no address; it must be filled in from the
	// device record so the column is not blank.
	if !strings.Contains(got, "198.51.100.252") {
		t.Errorf("status should show the device address:\n%s", got)
	}
	if !strings.Contains(got, "6610") {
		t.Errorf("status should show the metric count:\n%s", got)
	}
}

func TestDeviceMIBAssignAndRemove(t *testing.T) {
	var assigned, removed []int64

	mux := http.NewServeMux()
	mux.HandleFunc("/api/devices", deviceListHandler(t, testDeviceFixture()))
	mux.HandleFunc("/api/mibs", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"mibs": []client.MIB{
			{ID: 1, Name: "IF-MIB"},
			{ID: 2, Name: "CISCO-PROCESS-MIB"},
		}})
	})
	mux.HandleFunc("POST /api/devices/7/mibs", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MIBID int64 `json:"mib_id"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		assigned = append(assigned, body.MIBID)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /api/devices/7/mibs/1", func(w http.ResponseWriter, _ *http.Request) {
		removed = append(removed, 1)
		w.WriteHeader(http.StatusNoContent)
	})

	env, _ := newFakeEnv(t, mux)

	// Several MIBs in one command is the normal case when onboarding a device.
	if err := runCommand(t, newDeviceCommand(env),
		"mib", "add", "core-01", "IF-MIB", "CISCO-PROCESS-MIB"); err != nil {
		t.Fatalf("device mib add: %v", err)
	}
	if len(assigned) != 2 || assigned[0] != 1 || assigned[1] != 2 {
		t.Errorf("assigned = %v, want [1 2]", assigned)
	}

	if err := runCommand(t, newDeviceCommand(env),
		"mib", "remove", "core-01", "IF-MIB"); err != nil {
		t.Fatalf("device mib remove: %v", err)
	}
	if len(removed) != 1 {
		t.Errorf("removed = %v, want [1]", removed)
	}
}

func TestDeviceMIBAssignUnknownMIB(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/devices", deviceListHandler(t, testDeviceFixture()))
	mux.HandleFunc("/api/mibs", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"mibs": []client.MIB{{ID: 1, Name: "IF-MIB"}}})
	})
	mux.HandleFunc("POST /api/devices/7/mibs", func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("no assignment should be attempted for an unknown MIB")
	})

	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newDeviceCommand(env), "mib", "add", "core-01", "NO-SUCH-MIB")
	if err == nil {
		t.Fatal("expected an error for an unknown MIB")
	}

	var notFound *client.NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("got %T, want *client.NotFoundError", err)
	}
}

func TestDeviceProfileAssignAndRemove(t *testing.T) {
	assigned, removed := false, false

	mux := http.NewServeMux()
	mux.HandleFunc("/api/devices", deviceListHandler(t, testDeviceFixture()))
	mux.HandleFunc("/api/profiles", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"profiles": []client.Profile{
			{ID: 4, Name: "asr1000-core"},
		}})
	})
	mux.HandleFunc("POST /api/devices/7/profiles/4", func(w http.ResponseWriter, _ *http.Request) {
		assigned = true
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /api/devices/7/profiles/4", func(w http.ResponseWriter, _ *http.Request) {
		removed = true
		w.WriteHeader(http.StatusNoContent)
	})

	env, _ := newFakeEnv(t, mux)

	if err := runCommand(t, newDeviceCommand(env),
		"profile", "add", "core-01", "asr1000-core"); err != nil {
		t.Fatalf("device profile add: %v", err)
	}
	if !assigned {
		t.Error("assignment request was never issued")
	}

	if err := runCommand(t, newDeviceCommand(env),
		"profile", "remove", "core-01", "asr1000-core"); err != nil {
		t.Fatalf("device profile remove: %v", err)
	}
	if !removed {
		t.Error("removal request was never issued")
	}
}

func TestDeviceMIBListSeparatesDirectFromProfile(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/devices", deviceListHandler(t, testDeviceFixture()))
	mux.HandleFunc("/api/devices/7/mibs", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, client.DeviceMIBs{
			DirectMIBs: []client.AssignedMIB{enabledMIB(1, "IF-MIB", 91)},
			AllMIBs: []client.AssignedMIB{
				enabledMIB(1, "IF-MIB", 91),
				enabledMIB(5, "SNMPv2-MIB", 70),
			},
		})
	})

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newDeviceCommand(env), "mib", "list", "core-01"); err != nil {
		t.Fatalf("device mib list: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "direct") || !strings.Contains(got, "profile") {
		t.Errorf("listing must mark how each MIB was attached:\n%s", got)
	}
}

func TestDeviceMIBListEmpty(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/devices", deviceListHandler(t, testDeviceFixture()))
	mux.HandleFunc("/api/devices/7/mibs", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, client.DeviceMIBs{})
	})

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newDeviceCommand(env), "mib", "list", "core-01"); err != nil {
		t.Fatalf("device mib list: %v", err)
	}
	if !strings.Contains(out.String(), "No MIBs assigned") {
		t.Errorf("expected an empty-set notice, got:\n%s", out)
	}
}
