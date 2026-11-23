package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// fakePollerAPI is the poller's control API as far as registration goes: it
// holds the devices it was given, and like the real one it polls every device
// it holds, whatever the request's enabled field says.
type fakePollerAPI struct {
	mu       sync.Mutex
	polled   map[string]PollerDeviceRequest
	requests []string
}

func newFakePollerAPI(t *testing.T) (*fakePollerAPI, *httptest.Server) {
	t.Helper()
	fake := &fakePollerAPI{polled: map[string]PollerDeviceRequest{}}
	server := httptest.NewServer(http.HandlerFunc(fake.serve(t)))
	t.Cleanup(server.Close)
	return fake, server
}

func (f *fakePollerAPI) serve(t *testing.T) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)

		const prefix = "/api/poller/devices"
		name := ""
		if len(r.URL.Path) > len(prefix)+1 {
			name = r.URL.Path[len(prefix)+1:]
		}

		switch r.Method {
		case http.MethodPost, http.MethodPut:
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("reading request body: %v", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			var req PollerDeviceRequest
			if err := json.Unmarshal(raw, &req); err != nil {
				t.Errorf("decoding request body %q: %v", raw, err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if r.Method == http.MethodPost {
				if _, held := f.polled[req.Name]; held {
					w.WriteHeader(http.StatusConflict)
					return
				}
				name = req.Name
			}
			f.polled[name] = req
			w.WriteHeader(http.StatusOK)
		case http.MethodDelete:
			if _, held := f.polled[name]; !held {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			delete(f.polled, name)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

func (f *fakePollerAPI) polling(name string) (PollerDeviceRequest, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	req, held := f.polled[name]
	return req, held
}

func enabledFlag(v bool) *bool { return &v }

// TestADisabledDeviceIsNotPolled proves the registration path respects the
// stored flag end to end: a disabled device never reaches the poller, an
// update that disables a polled device takes it away, and enabling it again
// brings it back.
func TestADisabledDeviceIsNotPolled(t *testing.T) {
	fake, server := newFakePollerAPI(t)
	c := NewPollerClient(server.URL, time.Second)
	ctx := context.Background()

	device := &storage.Device{
		Name: "pdu-01", Address: "192.0.2.9", SNMPVersion: "v2c",
		Community: "public", Port: 161, Enabled: enabledFlag(false),
	}

	if err := c.RegisterDevice(ctx, device, []string{"IF-MIB"}, nil); err != nil {
		t.Fatalf("RegisterDevice(disabled): %v", err)
	}
	if _, held := fake.polling(device.Name); held {
		t.Fatal("a disabled device was registered with the poller")
	}
	if len(fake.requests) != 0 {
		t.Errorf("registering a disabled device sent %v, want nothing", fake.requests)
	}

	// Enabling it is an update, and the poller adds what it does not hold.
	device.Enabled = enabledFlag(true)
	if err := c.UpdateDevice(ctx, device, []string{"IF-MIB"}, nil); err != nil {
		t.Fatalf("UpdateDevice(enabled): %v", err)
	}
	req, held := fake.polling(device.Name)
	if !held {
		t.Fatal("an enabled device is not polled after its update")
	}
	if !req.Enabled {
		t.Error("the request for an enabled device says enabled=false")
	}

	// Disabling it takes it off the poller.
	device.Enabled = enabledFlag(false)
	if err := c.UpdateDevice(ctx, device, []string{"IF-MIB"}, nil); err != nil {
		t.Fatalf("UpdateDevice(disabled): %v", err)
	}
	if _, held := fake.polling(device.Name); held {
		t.Error("a device disabled by an update is still polled")
	}

	// And disabling it again, with the poller already rid of it, is not an error.
	if err := c.UpdateDevice(ctx, device, []string{"IF-MIB"}, nil); err != nil {
		t.Errorf("UpdateDevice(disabled, already absent): %v", err)
	}
}

// TestAStoredDeviceThatNeverSaidIsPolled covers every device stored before the
// flag existed: it registers, and says enabled.
func TestAStoredDeviceThatNeverSaidIsPolled(t *testing.T) {
	fake, server := newFakePollerAPI(t)
	c := NewPollerClient(server.URL, time.Second)

	device := &storage.Device{
		Name: "core-01", Address: "192.0.2.1", SNMPVersion: "v2c",
		Community: "public", Port: 161,
	}
	if err := c.RegisterDevice(context.Background(), device, nil, nil); err != nil {
		t.Fatalf("RegisterDevice: %v", err)
	}
	req, held := fake.polling(device.Name)
	if !held || !req.Enabled {
		t.Errorf("held = %v, enabled = %v; want a polled, enabled device", held, req.Enabled)
	}
}
