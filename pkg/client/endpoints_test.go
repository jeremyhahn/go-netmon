package client

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// endpointCall exercises one SDK method against a recording server.
type endpointCall struct {
	name       string
	wantMethod string
	wantPath   string
	body       string // JSON the fake server returns
	call       func(*Client) error
}

// TestEndpoints walks every remaining SDK method, asserting each hits the right
// method and path and decodes the response without error. The individual
// methods are thin wrappers over one request helper, so covering the routing is
// what actually protects against a typo in a URL.
func TestEndpoints(t *testing.T) {
	ctx := context.Background()

	calls := []endpointCall{
		{
			name: "DeviceStatus", wantMethod: "GET", wantPath: "/api/devices/1/status",
			body: `{"name":"core-01","running":true}`,
			call: func(c *Client) error { _, err := c.DeviceStatus(ctx, 1); return err },
		},
		{
			name: "TestDeviceSNMP", wantMethod: "POST", wantPath: "/api/devices/1/test-snmp",
			body: `{"success":true}`,
			call: func(c *Client) error { _, err := c.TestDeviceSNMP(ctx, 1); return err },
		},
		{
			name: "UpdateDevice", wantMethod: "PUT", wantPath: "/api/devices/1",
			body: `{"id":1,"name":"core-01"}`,
			call: func(c *Client) error { _, err := c.UpdateDevice(ctx, 1, Device{}); return err },
		},
		{
			name: "DeviceMIBs", wantMethod: "GET", wantPath: "/api/devices/1/mibs",
			body: `{"direct_mibs":[],"all_mibs":[]}`,
			call: func(c *Client) error { _, err := c.DeviceMIBs(ctx, 1); return err },
		},
		{
			name: "AssignMIBToDevice", wantMethod: "POST", wantPath: "/api/devices/1/mibs",
			body: `{"mib":"IF-MIB","device":"bastion","objects":10,"pollable":8,"series":40}`,
			call: func(c *Client) error { _, err := c.AssignMIBToDevice(ctx, 1, 2); return err },
		},
		{
			name: "RemoveMIBFromDevice", wantMethod: "DELETE", wantPath: "/api/devices/1/mibs/2",
			call: func(c *Client) error { return c.RemoveMIBFromDevice(ctx, 1, 2) },
		},
		{
			name: "DeviceProfiles", wantMethod: "GET", wantPath: "/api/devices/1/profiles",
			body: `{"profiles":[]}`,
			call: func(c *Client) error { _, err := c.DeviceProfiles(ctx, 1); return err },
		},
		{
			name: "AssignProfileToDevice", wantMethod: "POST", wantPath: "/api/devices/1/profiles/2",
			call: func(c *Client) error { return c.AssignProfileToDevice(ctx, 1, 2) },
		},
		{
			name: "RemoveProfileFromDevice", wantMethod: "DELETE", wantPath: "/api/devices/1/profiles/2",
			call: func(c *Client) error { return c.RemoveProfileFromDevice(ctx, 1, 2) },
		},
		{
			name: "ListProfiles", wantMethod: "GET", wantPath: "/api/profiles",
			body: `{"profiles":[{"id":1,"name":"asr1000-core"}]}`,
			call: func(c *Client) error { _, err := c.ListProfiles(ctx); return err },
		},
		{
			name: "GetProfile", wantMethod: "GET", wantPath: "/api/profiles/1",
			body: `{"id":1,"name":"asr1000-core"}`,
			call: func(c *Client) error { _, err := c.GetProfile(ctx, 1); return err },
		},
		{
			name: "CreateProfile", wantMethod: "POST", wantPath: "/api/profiles",
			body: `{"id":1,"name":"asr1000-core"}`,
			call: func(c *Client) error { _, err := c.CreateProfile(ctx, Profile{}); return err },
		},
		{
			name: "UpdateProfile", wantMethod: "PUT", wantPath: "/api/profiles/1",
			body: `{"id":1}`,
			call: func(c *Client) error { _, err := c.UpdateProfile(ctx, 1, Profile{}); return err },
		},
		{
			name: "DeleteProfile", wantMethod: "DELETE", wantPath: "/api/profiles/1",
			call: func(c *Client) error { return c.DeleteProfile(ctx, 1) },
		},
		{
			name: "ProfileMIBs", wantMethod: "GET", wantPath: "/api/profiles/1/mibs",
			body: `{"mibs":[]}`,
			call: func(c *Client) error { _, err := c.ProfileMIBs(ctx, 1); return err },
		},
		{
			name: "AddMIBToProfile", wantMethod: "POST", wantPath: "/api/profiles/1/mibs",
			call: func(c *Client) error { return c.AddMIBToProfile(ctx, 1, 2) },
		},
		{
			name: "RemoveMIBFromProfile", wantMethod: "DELETE", wantPath: "/api/profiles/1/mibs/2",
			call: func(c *Client) error { return c.RemoveMIBFromProfile(ctx, 1, 2) },
		},
		{
			name: "ProfileDevices", wantMethod: "GET", wantPath: "/api/profiles/1/devices",
			body: `{"devices":[]}`,
			call: func(c *Client) error { _, err := c.ProfileDevices(ctx, 1); return err },
		},
		{
			name: "GetMIB", wantMethod: "GET", wantPath: "/api/mibs/1",
			body: `{"id":1,"name":"IF-MIB","objects":[]}`,
			call: func(c *Client) error { _, err := c.GetMIB(ctx, 1); return err },
		},
		{
			name: "MIBTables", wantMethod: "GET", wantPath: "/api/mibs/1/tables",
			body: `{"tables":[]}`,
			call: func(c *Client) error { _, err := c.MIBTables(ctx, 1); return err },
		},
		{
			name: "DeleteMIB", wantMethod: "DELETE", wantPath: "/api/mibs/1",
			call: func(c *Client) error { return c.DeleteMIB(ctx, 1) },
		},
		{
			name: "SetMIBTags", wantMethod: "PUT", wantPath: "/api/mibs/1/tags",
			call: func(c *Client) error { return c.SetMIBTags(ctx, 1, []string{"cisco"}) },
		},
		{
			name: "QueryRange", wantMethod: "POST", wantPath: "/api/metrics/query",
			body: `[]`,
			call: func(c *Client) error { _, err := c.QueryRange(ctx, "snmp_if_in_octets", "6h"); return err },
		},
		{
			name: "QueryInstant", wantMethod: "POST", wantPath: "/api/metrics/instant",
			body: `{"metrics":[]}`,
			call: func(c *Client) error { _, err := c.QueryInstant(ctx, "up"); return err },
		},
		{
			name: "SystemInfo", wantMethod: "POST", wantPath: "/api/metrics/system-info",
			body: `{"device":"core-01","timestamp":1,"values":{"sys_name":"core-01"}}`,
			call: func(c *Client) error { _, err := c.SystemInfo(ctx, "core-01", nil); return err },
		},
		{
			name: "PollerConfig", wantMethod: "GET", wantPath: "/api/poller/config",
			body: `{"devices":[],"mib_modules":[],"version":1}`,
			call: func(c *Client) error { _, err := c.PollerConfig(ctx); return err },
		},
		{
			name: "LearnedTables", wantMethod: "GET", wantPath: "/api/learned-tables",
			body: `{"tables":{}}`,
			call: func(c *Client) error { _, err := c.LearnedTables(ctx); return err },
		},
		{
			name: "Health", wantMethod: "GET", wantPath: "/api/devices",
			body: `{"devices":[]}`,
			call: func(c *Client) error { return c.Health(ctx) },
		},
	}

	for _, tt := range calls {
		t.Run(tt.name, func(t *testing.T) {
			var gotMethod, gotPath string

			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath = r.Method, r.URL.Path
				if tt.body == "" {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				w.Write([]byte(tt.body))
			})

			if err := tt.call(c); err != nil {
				t.Fatalf("%s: %v", tt.name, err)
			}
			if gotMethod != tt.wantMethod {
				t.Errorf("method = %s, want %s", gotMethod, tt.wantMethod)
			}
			if gotPath != tt.wantPath {
				t.Errorf("path = %s, want %s", gotPath, tt.wantPath)
			}
		})
	}
}

// TestEndpointsPropagateServerErrors verifies that every method surfaces a
// server-side failure rather than returning a zero value and a nil error.
func TestEndpointsPropagateServerErrors(t *testing.T) {
	ctx := context.Background()

	failures := map[string]func(*Client) error{
		"GetProfile":    func(c *Client) error { _, err := c.GetProfile(ctx, 1); return err },
		"GetMIB":        func(c *Client) error { _, err := c.GetMIB(ctx, 1); return err },
		"MIBTables":     func(c *Client) error { _, err := c.MIBTables(ctx, 1); return err },
		"QueryRange":    func(c *Client) error { _, err := c.QueryRange(ctx, "m", "6h"); return err },
		"QueryInstant":  func(c *Client) error { _, err := c.QueryInstant(ctx, "up"); return err },
		"SystemInfo":    func(c *Client) error { _, err := c.SystemInfo(ctx, "d", nil); return err },
		"PollerConfig":  func(c *Client) error { _, err := c.PollerConfig(ctx); return err },
		"LearnedTables": func(c *Client) error { _, err := c.LearnedTables(ctx); return err },
		"DeleteMIB":     func(c *Client) error { return c.DeleteMIB(ctx, 1) },
		"SetMIBTags":    func(c *Client) error { return c.SetMIBTags(ctx, 1, nil) },
		"DeleteProfile": func(c *Client) error { return c.DeleteProfile(ctx, 1) },
	}

	for name, call := range failures {
		t.Run(name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "upstream unavailable", http.StatusBadGateway)
			})
			if err := call(c); err == nil {
				t.Errorf("%s returned nil error for a 502 response", name)
			}
		})
	}
}

// TestRequestBodiesAreWellFormed verifies the methods that send a body encode
// what the server expects.
func TestRequestBodiesAreWellFormed(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name string
		call func(*Client) error
		want map[string]any
	}{
		{
			name: "AssignMIBToDevice sends mib_id",
			call: func(c *Client) error { _, err := c.AssignMIBToDevice(ctx, 1, 42); return err },
			want: map[string]any{"mib_id": float64(42)},
		},
		{
			name: "AddMIBToProfile sends mib_id",
			call: func(c *Client) error { return c.AddMIBToProfile(ctx, 1, 7) },
			want: map[string]any{"mib_id": float64(7)},
		},
		{
			name: "SetMIBTags sends tags",
			call: func(c *Client) error { return c.SetMIBTags(ctx, 1, []string{"cisco", "asr1000"}) },
			want: map[string]any{"tags": []any{"cisco", "asr1000"}},
		},
		{
			name: "QueryRange sends metric and range",
			call: func(c *Client) error { _, err := c.QueryRange(ctx, "snmp_if_in_octets", "1d"); return err },
			want: map[string]any{"metric": "snmp_if_in_octets", "time_range": "1d"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got map[string]any

			// This test is about what the client sends, so the reply says
			// nothing at all: 204 short-circuits decoding, and a stub body
			// would otherwise have to match each method's response shape.
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				json.NewDecoder(r.Body).Decode(&got)
				w.WriteHeader(http.StatusNoContent)
			})

			if err := tt.call(c); err != nil {
				t.Fatalf("call: %v", err)
			}
			for key, want := range tt.want {
				gotVal, ok := got[key]
				if !ok {
					t.Errorf("request body missing %q; got %v", key, got)
					continue
				}
				if !jsonEqual(gotVal, want) {
					t.Errorf("body[%q] = %v, want %v", key, gotVal, want)
				}
			}
		})
	}
}

// jsonEqual compares two decoded JSON values.
func jsonEqual(a, b any) bool {
	aJSON, errA := json.Marshal(a)
	bJSON, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return string(aJSON) == string(bJSON)
}
