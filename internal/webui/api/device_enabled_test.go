package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

type enabledView struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Enabled *bool  `json:"enabled"`
}

func decodeEnabledView(t *testing.T, body []byte) enabledView {
	t.Helper()
	var view enabledView
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	if view.Enabled == nil {
		t.Fatalf("response %s does not state enabled", body)
	}
	return view
}

func deviceBody(name string, extra map[string]any) map[string]any {
	body := map[string]any{
		"name": name, "address": "192.0.2.9", "snmp_version": "v2c",
		"community": "public", "port": 161,
	}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

// TestDeviceAPIEnabled covers the field through create, get, list and update:
// omitted means enabled, false is kept, and every read states it.
func TestDeviceAPIEnabled(t *testing.T) {
	server := newTestServer(t)

	rec := doRequest(t, server, http.MethodPost, "/api/devices", deviceBody("pdu-01", nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body)
	}
	if view := decodeEnabledView(t, rec.Body.Bytes()); !*view.Enabled {
		t.Error("a device created without enabled is disabled")
	}

	rec = doRequest(t, server, http.MethodPost, "/api/devices",
		deviceBody("pdu-02", map[string]any{"enabled": false}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create disabled = %d: %s", rec.Code, rec.Body)
	}
	disabled := decodeEnabledView(t, rec.Body.Bytes())
	if *disabled.Enabled {
		t.Fatal("a device created disabled came back enabled")
	}

	rec = doRequest(t, server, http.MethodGet, fmt.Sprintf("/api/devices/%d", disabled.ID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get = %d: %s", rec.Code, rec.Body)
	}
	if view := decodeEnabledView(t, rec.Body.Bytes()); *view.Enabled {
		t.Error("get reports a disabled device as enabled")
	}

	rec = doRequest(t, server, http.MethodGet, "/api/devices", nil)
	var list struct {
		Devices []json.RawMessage `json:"devices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decoding the list: %v", err)
	}
	listed := map[string]bool{}
	for _, raw := range list.Devices {
		view := decodeEnabledView(t, raw)
		listed[view.Name] = *view.Enabled
	}
	if !listed["pdu-01"] || listed["pdu-02"] {
		t.Errorf("listed enabled = %v, want pdu-01 enabled and pdu-02 disabled", listed)
	}

	// An update that omits enabled keeps the stored value, whichever it is; an
	// update that states it changes it.
	path := fmt.Sprintf("/api/devices/%d", disabled.ID)
	steps := []struct {
		name string
		body map[string]any
		want bool
	}{
		{"omitting enabled on a disabled device", deviceBody("pdu-02", map[string]any{"address": "192.0.2.19"}), false},
		{"enabling", deviceBody("pdu-02", map[string]any{"enabled": true}), true},
		{"omitting enabled on an enabled device", deviceBody("pdu-02", nil), true},
		{"disabling", deviceBody("pdu-02", map[string]any{"enabled": false}), false},
	}
	for _, step := range steps {
		rec = doRequest(t, server, http.MethodPut, path, step.body)
		if rec.Code != http.StatusOK {
			t.Fatalf("update %s = %d: %s", step.name, rec.Code, rec.Body)
		}
		if got := *decodeEnabledView(t, rec.Body.Bytes()).Enabled; got != step.want {
			t.Errorf("update %s: response enabled = %v, want %v", step.name, got, step.want)
		}
		stored, err := server.storage.GetDevice(context.Background(), disabled.ID)
		if err != nil {
			t.Fatalf("GetDevice: %v", err)
		}
		if got := storage.EnabledOf(stored); got != step.want {
			t.Errorf("update %s: stored enabled = %v, want %v", step.name, got, step.want)
		}
	}
}

// TestPollerConfigCarriesEnabled is the pull path: the poller's configuration
// sync skips a device it is told is disabled, so it must be told.
func TestPollerConfigCarriesEnabled(t *testing.T) {
	server := newTestServer(t)
	off := false
	if err := server.storage.CreateDevice(context.Background(), &storage.Device{
		Name: "pdu-01", Address: "192.0.2.9", SNMPVersion: "v2c",
		Community: "public", Port: 161, Enabled: &off,
	}); err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}
	storeDevice(t, server, "core-01", "192.0.2.1")

	rec := doRequest(t, server, http.MethodGet, "/api/poller/config", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("poller config = %d: %s", rec.Code, rec.Body)
	}
	var config struct {
		Devices []struct {
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &config); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	got := map[string]bool{}
	for _, d := range config.Devices {
		got[d.Name] = d.Enabled
	}
	if len(got) != 2 || got["pdu-01"] || !got["core-01"] {
		t.Errorf("poller config enabled = %v, want pdu-01 false and core-01 true", got)
	}
}
