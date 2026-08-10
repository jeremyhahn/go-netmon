package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// healthServer builds a fake management API for the health command. Each
// handler is optional, so a test omits the one whose service it wants down.
type healthServer struct {
	devices    []client.Device
	status     *client.DeviceStatus // non-nil replaces the device's status
	statusCode int                  // non-zero replaces the device-status response
	promCode   int                  // non-zero replaces the instant-query response
	deviceCode int                  // non-zero replaces the device listing response
}

func (h healthServer) mux(t *testing.T) *http.ServeMux {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/devices", func(w http.ResponseWriter, _ *http.Request) {
		if h.deviceCode != 0 {
			http.Error(w, "storage unavailable", h.deviceCode)
			return
		}
		respondJSON(t, w, map[string]any{"devices": h.devices})
	})
	mux.HandleFunc("GET /api/devices/1/status", func(w http.ResponseWriter, _ *http.Request) {
		if h.statusCode != 0 {
			http.Error(w, "poller service unavailable", h.statusCode)
			return
		}
		if h.status != nil {
			respondJSON(t, w, *h.status)
			return
		}
		respondJSON(t, w, testDeviceStatus())
	})
	mux.HandleFunc("POST /api/metrics/instant", func(w http.ResponseWriter, _ *http.Request) {
		if h.promCode != 0 {
			http.Error(w, "failed to query metrics", h.promCode)
			return
		}
		respondJSON(t, w, testInstantValues(
			instantValue(map[string]string{"__name__": "up", "job": "go-netmon"}, 1),
		))
	})
	return mux
}

// oneDevice is the device every health probe hangs off.
func oneDevice() []client.Device {
	return []client.Device{{ID: 1, Name: "core-01", Address: "198.51.100.252"}}
}

func TestHealthAllServicesUp(t *testing.T) {
	env, out := newFakeEnv(t, healthServer{devices: oneDevice()}.mux(t))

	if err := runCommand(t, newHealthCommand(env)); err != nil {
		t.Fatalf("health: %v", err)
	}

	text := out.String()
	for _, want := range []string{managementAPIService, pollerService, prometheusService, "up"} {
		if !strings.Contains(text, want) {
			t.Errorf("report missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, string(serviceDown)) {
		t.Errorf("nothing is down, but the report says otherwise:\n%s", text)
	}
}

func TestHealthReportsDownServices(t *testing.T) {
	tests := []struct {
		name     string
		server   healthServer
		wantDown []string
		wantUp   []string
	}{
		{
			name:     "poller unreachable",
			server:   healthServer{devices: oneDevice(), statusCode: http.StatusServiceUnavailable},
			wantDown: []string{pollerService},
			wantUp:   []string{managementAPIService, prometheusService},
		},
		{
			name:     "prometheus unreachable",
			server:   healthServer{devices: oneDevice(), promCode: http.StatusInternalServerError},
			wantDown: []string{prometheusService},
			wantUp:   []string{managementAPIService, pollerService},
		},
		{
			name: "everything behind the API is down",
			server: healthServer{
				devices:    oneDevice(),
				statusCode: http.StatusServiceUnavailable,
				promCode:   http.StatusInternalServerError,
			},
			wantDown: []string{pollerService, prometheusService},
			wantUp:   []string{managementAPIService},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, out := newFakeEnv(t, tt.server.mux(t))

			err := runCommand(t, newHealthCommand(env))

			var unhealthy *UnhealthyError
			if !errors.As(err, &unhealthy) {
				t.Fatalf("error is %T (%v), want *UnhealthyError", err, err)
			}
			if strings.Join(unhealthy.Services, ",") != strings.Join(tt.wantDown, ",") {
				t.Errorf("down services = %v, want %v", unhealthy.Services, tt.wantDown)
			}
			// The message is what the shell prints; it has to name the
			// services rather than just saying something failed.
			for _, service := range tt.wantDown {
				if !strings.Contains(unhealthy.Error(), service) {
					t.Errorf("message %q does not name %s", unhealthy.Error(), service)
				}
			}

			// The detail must still be printed: an exit code alone does not
			// tell an operator which service to look at.
			states := healthStates(t, out.String())
			for _, service := range tt.wantDown {
				if states[service] != string(serviceDown) {
					t.Errorf("%s state = %q, want down", service, states[service])
				}
			}
			for _, service := range tt.wantUp {
				if states[service] != string(serviceUp) {
					t.Errorf("%s state = %q, want up", service, states[service])
				}
			}
		})
	}
}

// TestHealthManagementAPIDown checks the dependency order: when the API itself
// is unreachable the other services are unknown, not down, because nothing
// probed them.
func TestHealthManagementAPIDown(t *testing.T) {
	env, out := newFakeEnv(t, healthServer{deviceCode: http.StatusInternalServerError}.mux(t))

	err := runCommand(t, newHealthCommand(env))

	var unhealthy *UnhealthyError
	if !errors.As(err, &unhealthy) {
		t.Fatalf("error is %T (%v), want *UnhealthyError", err, err)
	}
	if len(unhealthy.Services) != 1 || unhealthy.Services[0] != managementAPIService {
		t.Errorf("down services = %v, want just the management API", unhealthy.Services)
	}

	states := healthStates(t, out.String())
	if states[pollerService] != string(serviceUnknown) {
		t.Errorf("poller state = %q, want unknown", states[pollerService])
	}
	if states[prometheusService] != string(serviceUnknown) {
		t.Errorf("prometheus state = %q, want unknown", states[prometheusService])
	}
}

// TestHealthWithoutDevices checks that an install with nothing configured is
// not reported as broken: the poller simply cannot be probed.
func TestHealthWithoutDevices(t *testing.T) {
	env, out := newFakeEnv(t, healthServer{}.mux(t))

	if err := runCommand(t, newHealthCommand(env)); err != nil {
		t.Fatalf("an empty install must not fail the health check: %v", err)
	}
	if state := healthStates(t, out.String())[pollerService]; state != string(serviceUnknown) {
		t.Errorf("poller state = %q, want unknown", state)
	}
}

// TestHealthPollerAnswers404 checks that the poller replying "I do not know
// that device" counts as reachable, which is what it is.
func TestHealthPollerAnswers404(t *testing.T) {
	env, out := newFakeEnv(t, healthServer{
		devices:    oneDevice(),
		statusCode: http.StatusNotFound,
	}.mux(t))

	if err := runCommand(t, newHealthCommand(env)); err != nil {
		t.Fatalf("a 404 from the poller is not an outage: %v", err)
	}
	if state := healthStates(t, out.String())[pollerService]; state != string(serviceUp) {
		t.Errorf("poller state = %q, want up", state)
	}
}

func TestHealthJSON(t *testing.T) {
	t.Run("healthy", func(t *testing.T) {
		env, out := newFakeEnv(t, healthServer{devices: oneDevice()}.mux(t))
		env.Opts.JSON = true

		if err := runCommand(t, newHealthCommand(env)); err != nil {
			t.Fatalf("health --json: %v", err)
		}

		var report healthReport
		if err := json.Unmarshal(out.Bytes(), &report); err != nil {
			t.Fatalf("output is not JSON: %v\n%s", err, out.String())
		}
		if !report.Healthy {
			t.Errorf("healthy = false, want true: %+v", report.Checks)
		}
		if len(report.Checks) != 3 {
			t.Fatalf("got %d checks, want 3", len(report.Checks))
		}
	})

	t.Run("unhealthy", func(t *testing.T) {
		env, out := newFakeEnv(t, healthServer{
			devices:  oneDevice(),
			promCode: http.StatusInternalServerError,
		}.mux(t))
		env.Opts.JSON = true

		err := runCommand(t, newHealthCommand(env))
		var unhealthy *UnhealthyError
		if !errors.As(err, &unhealthy) {
			t.Fatalf("error is %T (%v), want *UnhealthyError", err, err)
		}

		var report healthReport
		if err := json.Unmarshal(out.Bytes(), &report); err != nil {
			t.Fatalf("output is not JSON: %v\n%s", err, out.String())
		}
		if report.Healthy {
			t.Error("healthy = true although Prometheus is down")
		}
		for _, check := range report.Checks {
			if check.Service == prometheusService && check.State != serviceDown {
				t.Errorf("prometheus state = %q, want down", check.State)
			}
			if check.Detail == "" {
				t.Errorf("%s carries no detail", check.Service)
			}
		}
	})
}

// healthStates parses the rendered table into service→state, so assertions do
// not depend on column widths.
func healthStates(t *testing.T, text string) map[string]string {
	t.Helper()

	states := make(map[string]string)
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		states[fields[0]] = fields[1]
	}
	return states
}
