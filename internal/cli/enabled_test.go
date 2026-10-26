package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// TestDeviceUpdateKeepsADisabledDeviceDisabled is the read-modify-write the
// CLI does on every update: an unrelated change to a disabled device must carry
// enabled=false through, not drop it and switch polling back on.
func TestDeviceUpdateKeepsADisabledDeviceDisabled(t *testing.T) {
	current := testDeviceFixture()
	client.SetEnabled(&current, false)

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
		respondJSON(t, w, current)
	})

	env, _ := newFakeEnv(t, mux)
	if err := runCommand(t, newDeviceCommand(env), "update", "core-01",
		"--address", "192.0.2.77"); err != nil {
		t.Fatalf("device update: %v", err)
	}

	if body["enabled"] != false {
		t.Errorf("enabled = %v, want the stored false to survive an unrelated change", body["enabled"])
	}
	if body["address"] != "192.0.2.77" {
		t.Errorf("address = %v, want 192.0.2.77", body["address"])
	}
}
