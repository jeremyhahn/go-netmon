package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// newFakeEnv builds an Env wired to a fake management API. Output is captured
// in the returned buffer so tests can assert on what an operator would see.
func newFakeEnv(t *testing.T, handler http.Handler) (*Env, *bytes.Buffer) {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	out := &bytes.Buffer{}
	env := &Env{
		Opts:   &Options{Server: server.URL, Timeout: 5 * time.Second},
		Out:    out,
		ErrOut: out,
	}
	return env, out
}

// runCommand executes a command tree with args, discarding cobra's own output
// so only what the command wrote to Env.Out is under test.
func runCommand(t *testing.T, cmd *cobra.Command, args ...string) error {
	t.Helper()

	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	return cmd.Execute()
}

// respondJSON writes v as the fake API's response body.
func respondJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

// testProfile is the profile the fake API serves in most cases.
func testProfile() client.Profile {
	return client.Profile{
		ID:          1,
		Name:        "switch",
		Description: "Access switches",
		CreatedAt:   time.Now().Add(-2 * time.Hour),
	}
}

func TestProfileListRendersMIBCounts(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/profiles", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"profiles": []client.Profile{testProfile()}})
	})
	mux.HandleFunc("GET /api/profiles/1/mibs", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"mibs": []client.MIB{
			{ID: 7, Name: "IF-MIB", ObjectCount: 120},
			{ID: 8, Name: "BRIDGE-MIB", ObjectCount: 40},
		}})
	})

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newProfileCommand(env), "list"); err != nil {
		t.Fatalf("list: %v", err)
	}

	text := out.String()
	for _, want := range []string{"MIBS", "switch", "Access switches", "2"} {
		if !strings.Contains(text, want) {
			t.Errorf("listing missing %q:\n%s", want, text)
		}
	}
}

func TestProfileListEmptyAndServerError(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /api/profiles", func(w http.ResponseWriter, _ *http.Request) {
			respondJSON(t, w, map[string]any{"profiles": []client.Profile{}})
		})

		env, out := newFakeEnv(t, mux)
		if err := runCommand(t, newProfileCommand(env), "list"); err != nil {
			t.Fatalf("list: %v", err)
		}
		if !strings.Contains(out.String(), "No profiles defined") {
			t.Errorf("expected guidance for an empty list, got: %q", out.String())
		}
	})

	t.Run("server error", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /api/profiles", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "database unavailable", http.StatusInternalServerError)
		})

		env, _ := newFakeEnv(t, mux)
		err := runCommand(t, newProfileCommand(env), "list")

		var apiErr *client.APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("expected *client.APIError, got %v", err)
		}
		if apiErr.StatusCode != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", apiErr.StatusCode)
		}
	})
}

func TestProfileShowIncludesMIBsAndDevices(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/profiles/1", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, testProfile())
	})
	mux.HandleFunc("GET /api/profiles/1/mibs", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"mibs": []client.MIB{{ID: 7, Name: "IF-MIB", ObjectCount: 120}}})
	})
	mux.HandleFunc("GET /api/profiles/1/devices", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"devices": []client.Device{
			{ID: 3, Name: "core-01", Address: "198.51.100.252"},
		}})
	})

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newProfileCommand(env), "show", "1"); err != nil {
		t.Fatalf("show: %v", err)
	}

	text := out.String()
	for _, want := range []string{"switch", "IF-MIB", "120", "core-01", "198.51.100.252"} {
		if !strings.Contains(text, want) {
			t.Errorf("detail missing %q:\n%s", want, text)
		}
	}
}

func TestProfileShowUnknownName(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/profiles", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"profiles": []client.Profile{testProfile()}})
	})

	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newProfileCommand(env), "show", "ghost")

	var notFound *client.NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected *client.NotFoundError, got %v", err)
	}
	if notFound.Kind != "profile" {
		t.Errorf("kind = %q, want profile", notFound.Kind)
	}
}

func TestProfileCreateSendsFlags(t *testing.T) {
	var received client.Profile

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/profiles", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		received.ID = 9
		respondJSON(t, w, received)
	})

	env, out := newFakeEnv(t, mux)
	err := runCommand(t, newProfileCommand(env),
		"create", "--name", "router", "--description", "Edge routers")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if received.Name != "router" || received.Description != "Edge routers" {
		t.Errorf("server received %+v", received)
	}
	if !strings.Contains(out.String(), "Created profile router (id 9)") {
		t.Errorf("unexpected output: %q", out.String())
	}
}

func TestProfileCreateRejectsBadInput(t *testing.T) {
	t.Run("missing name", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("POST /api/profiles", func(_ http.ResponseWriter, _ *http.Request) {
			t.Fatal("server must not be called without --name")
		})

		env, _ := newFakeEnv(t, mux)
		if err := runCommand(t, newProfileCommand(env), "create"); err == nil {
			t.Fatal("expected an error when --name is omitted")
		}
	})

	t.Run("duplicate name", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("POST /api/profiles", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "profile exists", http.StatusConflict)
		})

		env, _ := newFakeEnv(t, mux)
		err := runCommand(t, newProfileCommand(env), "create", "--name", "switch")

		var apiErr *client.APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusConflict {
			t.Fatalf("expected a 409 APIError, got %v", err)
		}
	})
}

func TestProfileUpdateOverlaysOnlyChangedFlags(t *testing.T) {
	var received client.Profile

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/profiles/1", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, testProfile())
	})
	mux.HandleFunc("PUT /api/profiles/1", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		respondJSON(t, w, received)
	})

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newProfileCommand(env), "update", "1", "--name", "access-switch"); err != nil {
		t.Fatalf("update: %v", err)
	}

	if received.Name != "access-switch" {
		t.Errorf("name = %q, want access-switch", received.Name)
	}
	if received.Description != "Access switches" {
		t.Errorf("description = %q, want the stored value to survive", received.Description)
	}
	if !strings.Contains(out.String(), "Updated profile access-switch") {
		t.Errorf("unexpected output: %q", out.String())
	}
}

func TestProfileUpdateUnknownProfile(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/profiles/42", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	mux.HandleFunc("PUT /api/profiles/42", func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("must not update a profile that could not be read")
	})

	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newProfileCommand(env), "update", "42", "--name", "x")

	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || !apiErr.NotFound() {
		t.Fatalf("expected a 404 APIError, got %v", err)
	}
}

func TestProfileDeleteRequiresForce(t *testing.T) {
	deleted := false

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/profiles/1", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, testProfile())
	})
	mux.HandleFunc("DELETE /api/profiles/1", func(w http.ResponseWriter, _ *http.Request) {
		deleted = true
		w.WriteHeader(http.StatusNoContent)
	})

	env, out := newFakeEnv(t, mux)

	err := runCommand(t, newProfileCommand(env), "delete", "1")
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected a refusal mentioning --force, got %v", err)
	}
	if deleted {
		t.Fatal("profile was deleted without --force")
	}

	if err := runCommand(t, newProfileCommand(env), "delete", "1", "--force"); err != nil {
		t.Fatalf("forced delete: %v", err)
	}
	if !deleted {
		t.Fatal("forced delete did not reach the server")
	}
	if !strings.Contains(out.String(), "Deleted profile switch (id 1)") {
		t.Errorf("unexpected output: %q", out.String())
	}
}

func TestProfileMIBAddAndList(t *testing.T) {
	var added []int64

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/mibs", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"mibs": []client.MIB{
			{ID: 7, Name: "IF-MIB", ObjectCount: 120},
			{ID: 8, Name: "BRIDGE-MIB", ObjectCount: 40},
		}})
	})
	mux.HandleFunc("POST /api/profiles/1/mibs", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]int64
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		added = append(added, body["mib_id"])
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/profiles/1/mibs", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"mibs": []client.MIB{{ID: 7, Name: "IF-MIB", ObjectCount: 120}}})
	})

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newProfileCommand(env), "mib", "add", "1", "IF-MIB", "BRIDGE-MIB"); err != nil {
		t.Fatalf("mib add: %v", err)
	}
	if len(added) != 2 || added[0] != 7 || added[1] != 8 {
		t.Fatalf("server received mib ids %v, want [7 8]", added)
	}
	if !strings.Contains(out.String(), "Added IF-MIB") || !strings.Contains(out.String(), "Added BRIDGE-MIB") {
		t.Errorf("unexpected output: %q", out.String())
	}

	out.Reset()
	if err := runCommand(t, newProfileCommand(env), "mib", "list", "1"); err != nil {
		t.Fatalf("mib list: %v", err)
	}
	if !strings.Contains(out.String(), "IF-MIB") {
		t.Errorf("listing missing the MIB: %q", out.String())
	}
}

func TestProfileMIBAddUnknownMIB(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/mibs", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"mibs": []client.MIB{}})
	})
	mux.HandleFunc("POST /api/profiles/1/mibs", func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("must not assign a MIB that does not resolve")
	})

	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newProfileCommand(env), "mib", "add", "1", "NO-SUCH-MIB")

	var notFound *client.NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected *client.NotFoundError, got %v", err)
	}
}

func TestProfileMIBRemove(t *testing.T) {
	removed := false

	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /api/profiles/1/mibs/7", func(w http.ResponseWriter, _ *http.Request) {
		removed = true
		w.WriteHeader(http.StatusNoContent)
	})

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newProfileCommand(env), "mib", "remove", "1", "7"); err != nil {
		t.Fatalf("mib remove: %v", err)
	}
	if !removed {
		t.Fatal("removal did not reach the server")
	}
	if !strings.Contains(out.String(), "Removed 7") {
		t.Errorf("unexpected output: %q", out.String())
	}
}

func TestProfileDevicesReusesDeviceTable(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/profiles/1/devices", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"devices": []client.Device{
			{ID: 3, Name: "core-01", Address: "198.51.100.252", Port: 161, SNMPVersion: "2c"},
		}})
	})

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newProfileCommand(env), "devices", "1"); err != nil {
		t.Fatalf("devices: %v", err)
	}

	text := out.String()
	for _, want := range []string{"ADDRESS", "core-01", "198.51.100.252", "2c"} {
		if !strings.Contains(text, want) {
			t.Errorf("device table missing %q:\n%s", want, text)
		}
	}
}

func TestProfileDevicesServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/profiles/1/devices", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})

	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newProfileCommand(env), "devices", "1")

	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected a 500 APIError, got %v", err)
	}
}

func TestProfileListJSON(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/profiles", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"profiles": []client.Profile{testProfile()}})
	})

	env, out := newFakeEnv(t, mux)
	env.Opts.JSON = true
	if err := runCommand(t, newProfileCommand(env), "list"); err != nil {
		t.Fatalf("list --json: %v", err)
	}

	page := decodePage[client.Profile](t, out.Bytes())
	if len(page.Items) != 1 || page.Items[0].Name != "switch" {
		t.Errorf("unexpected JSON payload: %+v", page.Items)
	}
	if page.Total != 1 || page.Truncated {
		t.Errorf("counts = %+v, want a complete 1-row listing", page.listCounts)
	}
}

func TestRenderProfileTableWithoutCounts(t *testing.T) {
	out := &bytes.Buffer{}
	if err := renderProfileTable(out, wholeList([]client.Profile{testProfile()}, "profiles")); err != nil {
		t.Fatalf("render: %v", err)
	}

	text := out.String()
	if strings.Contains(text, "MIBS") {
		t.Errorf("call sites without counts must not claim a MIBS column:\n%s", text)
	}
	if !strings.Contains(text, "switch") {
		t.Errorf("table missing the profile:\n%s", text)
	}
}

func TestApplyProfileFlagOverridesIgnoresUnsetFlags(t *testing.T) {
	cmd := &cobra.Command{Use: "update", RunE: func(*cobra.Command, []string) error { return nil }}
	var flags client.Profile
	bindProfileFlags(cmd, &flags)

	if err := cmd.Flags().Parse([]string{"--description", "new text"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}

	current := testProfile()
	applyProfileFlagOverrides(cmd, &current, flags)

	if current.Name != "switch" {
		t.Errorf("name = %q, want the stored value to survive", current.Name)
	}
	if current.Description != "new text" {
		t.Errorf("description = %q, want the flag value", current.Description)
	}
}

func TestRenderProfileDetailReportsUnavailableSections(t *testing.T) {
	out := &bytes.Buffer{}
	profile := testProfile()

	err := renderProfileDetail(out, &profile,
		nil, errors.New("mib lookup failed"),
		nil, errors.New("device lookup failed"))
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	text := out.String()
	for _, want := range []string{"unavailable: mib lookup failed", "unavailable: device lookup failed"} {
		t.Run(want, func(t *testing.T) {
			if !strings.Contains(text, want) {
				t.Errorf("detail missing %q:\n%s", want, text)
			}
		})
	}
}

func TestRenderProfileDetailEmptySections(t *testing.T) {
	out := &bytes.Buffer{}
	profile := testProfile()

	if err := renderProfileDetail(out, &profile, nil, nil, nil, nil); err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Count(out.String(), "(none)") != 2 {
		t.Errorf("empty MIB and device sections should both say so:\n%s", out.String())
	}
}

func TestProfileListMarksUnreadableMIBCounts(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/profiles", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"profiles": []client.Profile{testProfile()}})
	})
	mux.HandleFunc("GET /api/profiles/1/mibs", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newProfileCommand(env), "list"); err != nil {
		t.Fatalf("an unreadable profile must not fail the listing: %v", err)
	}
	if !strings.Contains(out.String(), "?") {
		t.Errorf("expected an unknown MIB count marker:\n%s", out.String())
	}
}

func TestProfileMIBListEmpty(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/profiles/1/mibs", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"mibs": []client.MIB{}})
	})

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newProfileCommand(env), "mib", "list", "1"); err != nil {
		t.Fatalf("mib list: %v", err)
	}
	if !strings.Contains(out.String(), "No MIBs in this profile.") {
		t.Errorf("unexpected output: %q", out.String())
	}
}

func TestProfileMIBAddServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/profiles/1/mibs", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "already present", http.StatusConflict)
	})

	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newProfileCommand(env), "mib", "add", "1", "7")
	if err == nil || !strings.Contains(err.Error(), "7: ") {
		t.Fatalf("expected the failing MIB reference in the error, got %v", err)
	}

	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusConflict {
		t.Fatalf("expected a wrapped 409 APIError, got %v", err)
	}
}

func TestProfileShowJSONBundlesRelations(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/profiles/1", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, testProfile())
	})
	mux.HandleFunc("GET /api/profiles/1/mibs", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"mibs": []client.MIB{{ID: 7, Name: "IF-MIB"}}})
	})
	mux.HandleFunc("GET /api/profiles/1/devices", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"devices": []client.Device{{ID: 3, Name: "core-01"}}})
	})

	env, out := newFakeEnv(t, mux)
	env.Opts.JSON = true
	if err := runCommand(t, newProfileCommand(env), "show", "1"); err != nil {
		t.Fatalf("show --json: %v", err)
	}

	var payload struct {
		Profile client.Profile  `json:"profile"`
		MIBs    []client.MIB    `json:"mibs"`
		Devices []client.Device `json:"devices"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("output was not JSON: %v (%q)", err, out.String())
	}
	if payload.Profile.Name != "switch" || len(payload.MIBs) != 1 || len(payload.Devices) != 1 {
		t.Errorf("unexpected JSON payload: %+v", payload)
	}
}

func TestProfileDevicesAndMIBListJSON(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/profiles/1/devices", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"devices": []client.Device{{ID: 3, Name: "core-01"}}})
	})
	mux.HandleFunc("GET /api/profiles/1/mibs", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"mibs": []client.MIB{{ID: 7, Name: "IF-MIB"}}})
	})

	env, out := newFakeEnv(t, mux)
	env.Opts.JSON = true

	if err := runCommand(t, newProfileCommand(env), "devices", "1"); err != nil {
		t.Fatalf("devices --json: %v", err)
	}
	devices := decodePage[client.Device](t, out.Bytes())
	if len(devices.Items) != 1 || devices.Items[0].Name != "core-01" {
		t.Errorf("unexpected device payload: %+v", devices.Items)
	}

	out.Reset()
	if err := runCommand(t, newProfileCommand(env), "mib", "list", "1"); err != nil {
		t.Fatalf("mib list --json: %v", err)
	}
	mibs := decodePage[client.AssignedMIB](t, out.Bytes())
	if len(mibs.Items) != 1 || mibs.Items[0].Name != "IF-MIB" {
		t.Errorf("unexpected MIB payload: %+v", mibs.Items)
	}
}
