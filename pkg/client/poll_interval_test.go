package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestPollIntervalAccessors(t *testing.T) {
	t.Run("a device that inherits", func(t *testing.T) {
		var device Device

		if seconds, own := PollIntervalOf(device); own || seconds != 0 {
			t.Errorf("PollIntervalOf = (%d, %v), want (0, false)", seconds, own)
		}
	})

	t.Run("setting and clearing a schedule", func(t *testing.T) {
		var device Device

		SetPollInterval(&device, 90)
		seconds, own := PollIntervalOf(device)
		if !own || seconds != 90 {
			t.Errorf("PollIntervalOf = (%d, %v), want (90, true)", seconds, own)
		}

		InheritPollInterval(&device)
		if _, own := PollIntervalOf(device); own {
			t.Error("the device still has its own schedule after being told to inherit")
		}
	})

	t.Run("a later set replaces the first", func(t *testing.T) {
		var device Device

		SetPollInterval(&device, 60)
		SetPollInterval(&device, 5)

		if seconds, own := PollIntervalOf(device); !own || seconds != 5 {
			t.Errorf("PollIntervalOf = (%d, %v), want (5, true)", seconds, own)
		}
	})
}

// TestDeviceWriteRejectsAnUnschedulableInterval keeps a value the poller cannot
// act on from costing a round trip: the SDK refuses before any request is made.
func TestDeviceWriteRejectsAnUnschedulableInterval(t *testing.T) {
	var requests int
	api := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusCreated)
	})

	for _, interval := range []int{0, -1, -300} {
		device := Device{Name: "bastion", Address: "192.0.2.14", SNMPVersion: "2c"}
		SetPollInterval(&device, interval)

		_, createErr := api.CreateDevice(context.Background(), device)
		var invalid *PollIntervalError
		if !errors.As(createErr, &invalid) {
			t.Fatalf("CreateDevice(%d) = %v (%T), want *PollIntervalError", interval, createErr, createErr)
		}
		if invalid.Interval != interval || invalid.Device != "bastion" {
			t.Errorf("error = %+v, want it to name the device and the value", invalid)
		}
		if !strings.Contains(invalid.Error(), "global default") {
			t.Errorf("message %q does not say how to inherit the default", invalid)
		}

		if _, updateErr := api.UpdateDevice(context.Background(), 1, device); !errors.As(updateErr, &invalid) {
			t.Fatalf("UpdateDevice(%d) = %v (%T), want *PollIntervalError", interval, updateErr, updateErr)
		}
	}

	if requests != 0 {
		t.Errorf("the server was called %d times; a rejected interval must never leave the client", requests)
	}
}

// TestDeviceWriteSendsTheInterval covers the accepted path in both directions:
// what goes on the wire and what comes back off it.
func TestDeviceWriteSendsTheInterval(t *testing.T) {
	var sent Device

	api := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading body: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if err := json.Unmarshal(raw, &sent); err != nil {
			t.Errorf("decoding body %q: %v", raw, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1,"name":"core-01","address":"192.0.2.1",` +
			`"snmp_version":"2c","port":161,"poll_interval":45,` +
			`"effective_poll_interval":45,"warnings":["short interval"]}`))
	})

	device := Device{Name: "core-01", Address: "192.0.2.1", SNMPVersion: "2c", Port: 161}
	SetPollInterval(&device, 45)

	created, err := api.CreateDevice(context.Background(), device)
	if err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}

	if seconds, own := PollIntervalOf(sent); !own || seconds != 45 {
		t.Errorf("the request carried (%d, %v), want (45, true)", seconds, own)
	}
	if seconds, own := PollIntervalOf(*created); !own || seconds != 45 {
		t.Errorf("the response decoded to (%d, %v), want (45, true)", seconds, own)
	}
	if created.EffectivePollInterval != 45 {
		t.Errorf("EffectivePollInterval = %d, want 45", created.EffectivePollInterval)
	}
	if len(created.Warnings) != 1 {
		t.Errorf("Warnings = %v, want the server's one warning", created.Warnings)
	}
}

// TestInheritingDeviceSendsNoInterval is the wire-format half of inherit: a
// device with no schedule must omit the field, so the server can tell it from a
// device explicitly set to whatever the default is today.
func TestInheritingDeviceSendsNoInterval(t *testing.T) {
	var body string

	api := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1,"name":"core-02","effective_poll_interval":30}`))
	})

	created, err := api.CreateDevice(context.Background(),
		Device{Name: "core-02", Address: "192.0.2.2", SNMPVersion: "2c"})
	if err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}

	if strings.Contains(body, "poll_interval") && !strings.Contains(body, "effective_poll_interval") {
		t.Errorf("request %s carries a poll_interval; a device that inherits must omit it", body)
	}
	if _, own := PollIntervalOf(*created); own {
		t.Error("the created device reports a schedule of its own")
	}
	if created.EffectivePollInterval != 30 {
		t.Errorf("EffectivePollInterval = %d, want the server's default 30", created.EffectivePollInterval)
	}
}

// TestListDevicesCarriesIntervals covers the field on the listing the CLI and
// the web UI both render.
func TestListDevicesCarriesIntervals(t *testing.T) {
	api := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"devices":[
			{"id":1,"name":"core-01","poll_interval":120,"effective_poll_interval":120},
			{"id":2,"name":"core-02","effective_poll_interval":30}
		]}`))
	})

	devices, err := api.ListDevices(context.Background())
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if len(devices) != 2 {
		t.Fatalf("got %d devices, want 2", len(devices))
	}

	if seconds, own := PollIntervalOf(devices[0]); !own || seconds != 120 {
		t.Errorf("core-01 = (%d, %v), want (120, true)", seconds, own)
	}
	if _, own := PollIntervalOf(devices[1]); own {
		t.Error("core-02 reports a schedule of its own; it inherits")
	}
	if devices[1].EffectivePollInterval != 30 {
		t.Errorf("core-02 effective = %d, want 30", devices[1].EffectivePollInterval)
	}
}
