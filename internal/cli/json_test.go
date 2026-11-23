package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// --json is a global flag, so a command that quietly ignores it hands a script
// prose where it asked for a document. These tests cover the commands that
// change something; the read commands are covered alongside their tables.

// decodeJSON parses a command's output, failing the test with the raw text when
// it is not the document it claims to be.
func decodeJSON[T any](t *testing.T, text string) T {
	t.Helper()

	var decoded T
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, text)
	}
	return decoded
}

// runJSON runs a command with --json in force and returns what it wrote.
func runJSON(t *testing.T, env *Env, out *bytes.Buffer, cmd *cobra.Command, args ...string) string {
	t.Helper()

	env.Opts.JSON = true
	if err := runCommand(t, cmd, args...); err != nil {
		t.Fatalf("%s: %v", strings.Join(args, " "), err)
	}
	return out.String()
}

func TestDeviceDeleteJSON(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/devices", deviceListHandler(t, testDeviceFixture()))
	mux.HandleFunc("GET /api/devices/7", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, testDeviceFixture())
	})
	mux.HandleFunc("DELETE /api/devices/7", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	env, out := newFakeEnv(t, mux)
	got := decodeJSON[changeReport](t,
		runJSON(t, env, out, newDeviceCommand(env), "delete", "core-01", "--force"))

	want := changeReport{Action: "deleted", Kind: "device", Ref: "core-01", ID: 7}
	if got != want {
		t.Errorf("report = %+v, want %+v", got, want)
	}
}

func TestProfileDeleteJSON(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/profiles", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"profiles": []client.Profile{testProfile()}})
	})
	mux.HandleFunc("GET /api/profiles/1", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, testProfile())
	})
	mux.HandleFunc("DELETE /api/profiles/1", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	env, out := newFakeEnv(t, mux)
	got := decodeJSON[changeReport](t,
		runJSON(t, env, out, newProfileCommand(env), "delete", "switch", "--force"))

	want := changeReport{Action: "deleted", Kind: "profile", Ref: "switch", ID: 1}
	if got != want {
		t.Errorf("report = %+v, want %+v", got, want)
	}
}

func TestMIBDeleteJSON(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/mibs", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"mibs": testMIBs()})
	})
	mux.HandleFunc("GET /api/mibs/7", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, testMIBDetail(1))
	})
	mux.HandleFunc("DELETE /api/mibs/7", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	env, out := newFakeEnv(t, mux)
	got := decodeJSON[changeReport](t,
		runJSON(t, env, out, newMIBCommand(env), "delete", "IF-MIB", "--force"))

	if got.Action != "deleted" || got.Kind != "mib" || got.ID != 7 {
		t.Errorf("report = %+v, want a deleted mib with id 7", got)
	}
}

func TestMIBTagsJSON(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/mibs", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"mibs": testMIBs()})
	})
	mux.HandleFunc("PUT /api/mibs/7/tags", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	env, out := newFakeEnv(t, mux)
	got := decodeJSON[tagReport](t,
		runJSON(t, env, out, newMIBCommand(env), "tags", "IF-MIB", "--set", "cisco,core"))

	if got.Action != "tagged" || got.Ref != "IF-MIB" || got.ID != 7 {
		t.Errorf("report = %+v, want IF-MIB tagged", got)
	}
	if strings.Join(got.Tags, ",") != "cisco,core" {
		t.Errorf("tags = %v, want [cisco core]", got.Tags)
	}
}

// TestMIBImportJSON checks that a batch reports every file, including the ones
// that failed: the point of the import loop is that one bad file does not
// abandon the rest, and a script needs to see which was which.
func TestMIBImportJSON(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/mibs/import", func(w http.ResponseWriter, r *http.Request) {
		_, header, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("read upload: %v", err)
		}
		if strings.Contains(header.Filename, "bad") {
			http.Error(w, "parse error: unexpected token", http.StatusBadRequest)
			return
		}
		respondJSON(t, w, map[string]any{"mib": client.MIB{
			ID: 9, Name: strings.TrimSuffix(header.Filename, ".mib"), ObjectCount: 12,
		}})
	})

	env, out := newFakeEnv(t, mux)
	env.Opts.JSON = true

	paths := writeMIBFiles(t, "GOOD-MIB.mib", "bad-one.mib")
	args := append([]string{"import"}, paths...)

	err := runCommand(t, newMIBCommand(env), args...)
	var importErr *ImportError
	if !errors.As(err, &importErr) {
		t.Fatalf("error is %T (%v), want *ImportError", err, err)
	}

	reports := decodeJSON[[]importReport](t, out.String())
	if len(reports) != 2 {
		t.Fatalf("got %d reports, want one per file:\n%s", len(reports), out.String())
	}
	if reports[0].MIB == nil || reports[0].MIB.Name != "GOOD-MIB" {
		t.Errorf("first report = %+v, want the imported module", reports[0])
	}
	if reports[1].Error == "" || reports[1].File != filepath.Clean(paths[1]) {
		t.Errorf("second report = %+v, want the failing file and its cause", reports[1])
	}
}

func TestDeviceMIBAddJSON(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/devices", deviceListHandler(t, testDeviceFixture()))
	mux.HandleFunc("GET /api/mibs", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"mibs": testMIBs()})
	})
	mux.HandleFunc("POST /api/devices/7/mibs", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	env, out := newFakeEnv(t, mux)
	reports := decodeJSON[[]changeReport](t,
		runJSON(t, env, out, newDeviceCommand(env), "mib", "add", "core-01", "IF-MIB", "CISCO-ENVMON-MIB"))

	if len(reports) != 2 {
		t.Fatalf("got %d reports, want one per MIB", len(reports))
	}
	for _, report := range reports {
		if report.Action != "assigned" || report.Kind != "mib" || report.ID == 0 {
			t.Errorf("report = %+v, want an assigned mib with an ID", report)
		}
	}
}

// TestDeviceProfileList covers the listing of a device's profiles in both
// forms: it is a read command, so --json is not optional.
func TestDeviceProfileList(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/devices", deviceListHandler(t, testDeviceFixture()))
	mux.HandleFunc("GET /api/devices/7/profiles", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"profiles": []client.Profile{testProfile()}})
	})

	t.Run("table", func(t *testing.T) {
		env, out := newFakeEnv(t, mux)
		if err := runCommand(t, newDeviceCommand(env), "profile", "list", "core-01"); err != nil {
			t.Fatalf("device profile list: %v", err)
		}
		if !strings.Contains(out.String(), "switch") {
			t.Errorf("listing missing the profile:\n%s", out.String())
		}
	})

	t.Run("json", func(t *testing.T) {
		env, out := newFakeEnv(t, mux)
		page := decodeJSON[listPage[client.Profile]](t,
			runJSON(t, env, out, newDeviceCommand(env), "profile", "list", "core-01"))

		if len(page.Items) != 1 || page.Items[0].Name != "switch" {
			t.Errorf("profiles = %+v, want the assigned profile", page.Items)
		}
	})
}

// TestMIBEnableJSON also covers the series-cost preview, which is prose: under
// --json it must be withheld rather than written into the document.
func TestMIBEnableJSON(t *testing.T) {
	api := newFakeAssignmentAPI(t)
	env, out := newFakeEnv(t, api.handler())

	reports := decodeJSON[[]changeReport](t,
		runJSON(t, env, out, newDeviceCommand(env), "mib", "enable", "core-01", "CISCO-PROCESS-MIB"))

	if len(reports) != 1 {
		t.Fatalf("got %d reports, want 1", len(reports))
	}
	if reports[0].Action != "enabled" || reports[0].Ref != "CISCO-PROCESS-MIB" {
		t.Errorf("report = %+v, want CISCO-PROCESS-MIB enabled", reports[0])
	}
	if !api.assignments[2].Enabled {
		t.Error("the assignment was not enabled on the server")
	}
}

// TestMIBEnableJSONStillGuardsSeriesCost checks that suppressing the preview
// does not suppress the guard it exists to enforce.
func TestMIBEnableJSONStillGuardsSeriesCost(t *testing.T) {
	api := newFakeAssignmentAPI(t)
	api.estimates[2] = client.MIBSeriesEstimate{
		MIB: "CISCO-PROCESS-MIB", Device: "core-01",
		Series: seriesConfirmThreshold + 1, Explanation: "a runaway table",
	}

	env, out := newFakeEnv(t, api.handler())
	env.Opts.JSON = true

	err := runCommand(t, newDeviceCommand(env),
		"mib", "enable", "core-01", "CISCO-PROCESS-MIB")

	var budget *SeriesBudgetError
	if !errors.As(err, &budget) {
		t.Fatalf("error is %T (%v), want *SeriesBudgetError", err, err)
	}
	if api.assignments[2].Enabled {
		t.Error("a refused enable must change nothing")
	}
	if out.Len() != 0 {
		t.Errorf("a refused command must not emit a partial document:\n%s", out.String())
	}
}
