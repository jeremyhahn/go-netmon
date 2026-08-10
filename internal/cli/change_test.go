package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// TestBulkNamesTheFailingReference checks that a batch which fails part way
// through says which reference it failed on: the earlier ones are already
// applied, so "which one" is the only thing that tells an operator where the
// command stopped.
func TestBulkNamesTheFailingReference(t *testing.T) {
	var applied []string

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/devices", deviceListHandler(t, testDeviceFixture()))
	mux.HandleFunc("GET /api/mibs", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"mibs": testMIBs()})
	})
	mux.HandleFunc("POST /api/devices/7/mibs", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MIBID int64 `json:"mib_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode assignment request: %v", err)
		}
		if body.MIBID == 8 {
			http.Error(w, "MIB is already assigned", http.StatusConflict)
			return
		}
		applied = append(applied, "ok")
		w.WriteHeader(http.StatusNoContent)
	})

	env, out := newFakeEnv(t, mux)
	err := runCommand(t, newDeviceCommand(env),
		"mib", "add", "core-01", "IF-MIB", "CISCO-ENVMON-MIB")

	var change *ChangeError
	if !errors.As(err, &change) {
		t.Fatalf("error is %T (%v), want *ChangeError", err, err)
	}
	if change.Ref != "CISCO-ENVMON-MIB" {
		t.Errorf("Ref = %q, want the module that failed", change.Ref)
	}

	// The cause must survive wrapping, or callers cannot tell a conflict from
	// an outage.
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusConflict {
		t.Errorf("wrapped cause = %v, want a 409 *client.APIError", err)
	}

	if len(applied) != 1 {
		t.Errorf("%d assignments applied, want the first one only", len(applied))
	}
	if !strings.Contains(out.String(), "Assigned IF-MIB") {
		t.Errorf("the applied assignment should still be reported:\n%s", out.String())
	}
}

// TestBulkStopsOnAnUnknownReference checks that a reference that resolves to
// nothing fails before any request is sent for it.
func TestBulkStopsOnAnUnknownReference(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/devices", deviceListHandler(t, testDeviceFixture()))
	mux.HandleFunc("GET /api/mibs", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"mibs": testMIBs()})
	})
	mux.HandleFunc("POST /api/devices/7/mibs", func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("no assignment must be attempted for an unknown module")
	})

	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newDeviceCommand(env), "mib", "add", "core-01", "NO-SUCH-MIB")

	var notFound *client.NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("error is %T (%v), want *client.NotFoundError", err, err)
	}
}

func TestChangeErrorMessage(t *testing.T) {
	err := &ChangeError{Ref: "IF-MIB", Err: errors.New("already assigned")}
	if got := err.Error(); got != "IF-MIB: already assigned" {
		t.Errorf("Error() = %q, want the reference and the cause", got)
	}
}
