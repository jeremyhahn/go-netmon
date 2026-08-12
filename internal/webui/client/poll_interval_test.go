package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// seconds returns a pointer to an interval, which is how a device that has one
// of its own carries it.
func seconds(n int) *int { return &n }

// TestDeviceToRequestCarriesTheDevicesInterval is the regression for the defect
// this field exists to fix.
//
// deviceToRequest used to compute a poll interval of 30 down both arms of a
// branch on the device's port, so every device was pushed to the poller at 30
// seconds no matter what was configured. Each case below asserts the actual
// number, and the interesting ones are deliberately not 30: a test that accepted
// 30 would have passed against the bug.
func TestDeviceToRequestCarriesTheDevicesInterval(t *testing.T) {
	c := NewPollerClient("http://poller.invalid", time.Second)

	tests := []struct {
		name   string
		device storage.Device
		want   int
	}{
		{
			name: "an explicit schedule is pushed unchanged",
			device: storage.Device{
				Name: "core-01", Address: "192.0.2.1", SNMPVersion: "v2c",
				Port: 161, PollInterval: seconds(120),
			},
			want: 120,
		},
		{
			name: "a schedule shorter than the default is pushed unchanged",
			device: storage.Device{
				Name: "bastion", Address: "192.0.2.14", SNMPVersion: "v2c",
				Port: 161, PollInterval: seconds(5),
			},
			want: 5,
		},
		{
			name: "a custom port does not decide the schedule",
			device: storage.Device{
				Name: "pdu-03", Address: "192.0.2.3", SNMPVersion: "v2c",
				Port: 1161, PollInterval: seconds(900),
			},
			want: 900,
		},
		{
			name: "a device with no schedule is pushed as inheriting",
			device: storage.Device{
				Name: "core-02", Address: "192.0.2.2", SNMPVersion: "v2c", Port: 161,
			},
			want: 0,
		},
		{
			name: "a device with no schedule and a custom port also inherits",
			device: storage.Device{
				Name: "core-03", Address: "192.0.2.4", SNMPVersion: "v2c", Port: 1161,
			},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := c.deviceToRequest(&tt.device, nil, nil)

			if req.PollInterval != tt.want {
				t.Errorf("PollInterval = %d, want %d", req.PollInterval, tt.want)
			}

			// Nothing else about the device may move with the interval.
			if req.Name != tt.device.Name {
				t.Errorf("Name = %q, want %q", req.Name, tt.device.Name)
			}
			if !req.Enabled {
				t.Error("the device was pushed disabled")
			}
		})
	}
}

// TestPushedIntervalMeansWhatThePollerReads closes the loop on the wire format:
// the number deviceToRequest puts on the wire, read by the poller's own rule,
// is the interval the operator asked for.
func TestPushedIntervalMeansWhatThePollerReads(t *testing.T) {
	c := NewPollerClient("http://poller.invalid", time.Second)
	const globalDefault = 30

	tests := []struct {
		name   string
		device storage.Device
		want   time.Duration
	}{
		{"an explicit schedule", storage.Device{Name: "a", PollInterval: seconds(120)}, 2 * time.Minute},
		{"a device that inherits", storage.Device{Name: "b"}, globalDefault * time.Second},
		{
			"a device explicitly set to today's default",
			storage.Device{Name: "c", PollInterval: seconds(globalDefault)},
			globalDefault * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := c.deviceToRequest(&tt.device, nil, nil)

			// What the poller makes of the request it receives.
			received := config.Device{PollInterval: req.PollInterval}
			if got := received.GetPollInterval(globalDefault); got != tt.want {
				t.Errorf("the poller would poll every %v, want %v", got, tt.want)
			}
		})
	}
}

// TestRegisterAndUpdateSendTheInterval checks the interval survives being
// encoded onto the wire, on both write paths.
func TestRegisterAndUpdateSendTheInterval(t *testing.T) {
	bodies := make(chan PollerDeviceRequest, 2)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		bodies <- req
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c := NewPollerClient(server.URL, time.Second)
	device := &storage.Device{
		Name: "core-01", Address: "192.0.2.1", SNMPVersion: "v2c",
		Community: "public", Port: 161, PollInterval: seconds(45),
	}

	if err := c.RegisterDevice(context.Background(), device, []string{"IF-MIB"}, nil); err != nil {
		t.Fatalf("RegisterDevice: %v", err)
	}
	if got := (<-bodies).PollInterval; got != 45 {
		t.Errorf("registered poll_interval = %d, want 45", got)
	}

	device.PollInterval = seconds(300)
	if err := c.UpdateDevice(context.Background(), device, []string{"IF-MIB"}, nil); err != nil {
		t.Fatalf("UpdateDevice: %v", err)
	}
	if got := (<-bodies).PollInterval; got != 300 {
		t.Errorf("updated poll_interval = %d, want 300", got)
	}
}

// TestRegisterRejectedByThePollerIsReported is the bad-input half: an interval
// the poller refuses comes back as an error rather than being swallowed.
func TestRegisterRejectedByThePollerIsReported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"validation failed","detail":"poll_interval must be non-negative"}`,
			http.StatusBadRequest)
	}))
	defer server.Close()

	c := NewPollerClient(server.URL, time.Second)
	device := &storage.Device{Name: "bastion", Address: "192.0.2.14", SNMPVersion: "v2c"}

	err := c.RegisterDevice(context.Background(), device, nil, nil)
	if err == nil {
		t.Fatal("RegisterDevice = nil, want the poller's refusal")
	}
	if got := err.Error(); got == "" {
		t.Error("the error carries no reason")
	}
}
