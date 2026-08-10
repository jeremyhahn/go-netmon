package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// TestMIBAssignmentValidate covers the patterns the SDK accepts and the ones it
// refuses before a request is built.
func TestMIBAssignmentValidate(t *testing.T) {
	tests := []struct {
		name       string
		assignment MIBAssignment
		wantField  string // "" when the assignment is valid
	}{
		{
			name:       "unfiltered",
			assignment: MIBAssignment{Enabled: true},
		},
		{
			name: "valid patterns on every field",
			assignment: MIBAssignment{
				Enabled:      true,
				IncludeNames: []string{"^ifHC.*Octets$"},
				ExcludeNames: []string{"Discards$"},
				IncludeOIDs:  []string{`^1\.3\.6\.1\.2\.1\.2\.2\.1\.`},
				ExcludeOIDs:  []string{`\.16$`},
				MaxOIDs:      50,
			},
		},
		{
			name:       "bad include names",
			assignment: MIBAssignment{IncludeNames: []string{"if["}},
			wantField:  "include_names",
		},
		{
			name:       "bad exclude names",
			assignment: MIBAssignment{ExcludeNames: []string{"*bad"}},
			wantField:  "exclude_names",
		},
		{
			name:       "bad include oids",
			assignment: MIBAssignment{IncludeOIDs: []string{"("}},
			wantField:  "include_oids",
		},
		{
			name:       "bad exclude oids",
			assignment: MIBAssignment{ExcludeOIDs: []string{"a{2,1}"}},
			wantField:  "exclude_oids",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.assignment.Validate()
			if tt.wantField == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}

			var patternErr *PatternError
			if !errors.As(err, &patternErr) {
				t.Fatalf("Validate() = %v, want *PatternError", err)
			}
			if patternErr.Field != tt.wantField {
				t.Errorf("field = %q, want %q", patternErr.Field, tt.wantField)
			}
			if patternErr.Pattern == "" {
				t.Error("the error must name the offending pattern")
			}
			if !strings.Contains(patternErr.Error(), patternErr.Pattern) {
				t.Errorf("message %q does not quote the pattern", patternErr.Error())
			}
			if patternErr.Unwrap() == nil {
				t.Error("the compile failure should stay reachable through Unwrap")
			}
		})
	}
}

func TestMIBAssignmentValidateRejectsNegativeMaxOIDs(t *testing.T) {
	err := MIBAssignment{MaxOIDs: -1}.Validate()

	var configErr *ConfigError
	if !errors.As(err, &configErr) {
		t.Fatalf("Validate() = %v, want *ConfigError", err)
	}
	if configErr.Field != "max_oids" {
		t.Errorf("field = %q, want max_oids", configErr.Field)
	}
}

// TestSetAssignmentRejectsBadPatternWithoutRequest proves the SDK refuses a
// malformed pattern in-process: a bad regexp must never reach the server.
func TestSetAssignmentRejectsBadPatternWithoutRequest(t *testing.T) {
	setters := map[string]func(*Client) error{
		"device": func(c *Client) error {
			_, err := c.SetDeviceMIBAssignment(context.Background(), 1, 2,
				MIBAssignment{Enabled: true, IncludeNames: []string{"if("}})
			return err
		},
		"profile": func(c *Client) error {
			_, err := c.SetProfileMIBAssignment(context.Background(), 1, 2,
				MIBAssignment{Enabled: true, ExcludeOIDs: []string{"["}})
			return err
		},
	}

	for name, set := range setters {
		t.Run(name, func(t *testing.T) {
			called := false
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.Write([]byte(`{"enabled":true}`))
			})

			var patternErr *PatternError
			if err := set(c); !errors.As(err, &patternErr) {
				t.Fatalf("expected *PatternError, got %v", err)
			}
			if called {
				t.Error("a malformed pattern reached the server")
			}
		})
	}
}

// TestAssignmentRoundTrip walks every assignment endpoint against a recording
// server, asserting the method, path, request body and decoded response.
func TestAssignmentRoundTrip(t *testing.T) {
	ctx := context.Background()

	filtered := MIBAssignment{
		Enabled:      true,
		IncludeNames: []string{"^ifHC"},
		ExcludeOIDs:  []string{`\.16$`},
		MaxOIDs:      50,
	}

	tests := []struct {
		name       string
		wantMethod string
		wantPath   string
		response   string
		call       func(*Client) (any, error)
	}{
		{
			name: "DeviceMIBAssignment", wantMethod: http.MethodGet,
			wantPath: "/api/devices/1/mibs/2/assignment",
			response: `{"enabled":true,"include_names":["^ifHC"],"exclude_oids":["\\.16$"],"max_oids":50}`,
			call: func(c *Client) (any, error) {
				return c.DeviceMIBAssignment(ctx, 1, 2)
			},
		},
		{
			name: "SetDeviceMIBAssignment", wantMethod: http.MethodPut,
			wantPath: "/api/devices/1/mibs/2/assignment",
			response: `{"enabled":true,"include_names":["^ifHC"],"exclude_oids":["\\.16$"],"max_oids":50}`,
			call: func(c *Client) (any, error) {
				return c.SetDeviceMIBAssignment(ctx, 1, 2, filtered)
			},
		},
		{
			name: "ProfileMIBAssignment", wantMethod: http.MethodGet,
			wantPath: "/api/profiles/1/mibs/2/assignment",
			response: `{"enabled":true,"include_names":["^ifHC"],"exclude_oids":["\\.16$"],"max_oids":50}`,
			call: func(c *Client) (any, error) {
				return c.ProfileMIBAssignment(ctx, 1, 2)
			},
		},
		{
			name: "SetProfileMIBAssignment", wantMethod: http.MethodPut,
			wantPath: "/api/profiles/1/mibs/2/assignment",
			response: `{"enabled":true,"include_names":["^ifHC"],"exclude_oids":["\\.16$"],"max_oids":50}`,
			call: func(c *Client) (any, error) {
				return c.SetProfileMIBAssignment(ctx, 1, 2, filtered)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotMethod, gotPath string

			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath = r.Method, r.URL.Path
				w.Write([]byte(tt.response))
			})

			result, err := tt.call(c)
			if err != nil {
				t.Fatalf("%s: %v", tt.name, err)
			}
			if gotMethod != tt.wantMethod {
				t.Errorf("method = %s, want %s", gotMethod, tt.wantMethod)
			}
			if gotPath != tt.wantPath {
				t.Errorf("path = %s, want %s", gotPath, tt.wantPath)
			}

			got, ok := result.(*MIBAssignment)
			if !ok {
				t.Fatalf("result type = %T, want *MIBAssignment", result)
			}
			if !got.Enabled || got.MaxOIDs != 50 {
				t.Errorf("decoded assignment = %+v", got)
			}
			if len(got.IncludeNames) != 1 || got.IncludeNames[0] != "^ifHC" {
				t.Errorf("include names = %v", got.IncludeNames)
			}
			if len(got.ExcludeOIDs) != 1 || got.ExcludeOIDs[0] != `\.16$` {
				t.Errorf("exclude oids = %v", got.ExcludeOIDs)
			}
		})
	}
}

// TestSetAssignmentSendsWholeAssignment verifies the PUT body carries every
// field, since the server replaces the stored assignment outright.
func TestSetAssignmentSendsWholeAssignment(t *testing.T) {
	var got MIBAssignment

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Write([]byte(`{"enabled":false}`))
	})

	want := MIBAssignment{
		Enabled:      true,
		IncludeOIDs:  []string{"^1.3.6"},
		ExcludeNames: []string{"Errors$"},
		MaxOIDs:      12,
	}
	if _, err := c.SetDeviceMIBAssignment(context.Background(), 3, 4, want); err != nil {
		t.Fatalf("set: %v", err)
	}

	if !got.Enabled || got.MaxOIDs != 12 {
		t.Errorf("server received %+v", got)
	}
	if len(got.IncludeOIDs) != 1 || len(got.ExcludeNames) != 1 {
		t.Errorf("server received %+v, want both pattern lists", got)
	}
}

func TestEstimateMIBSeries(t *testing.T) {
	var gotMethod, gotPath string

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Write([]byte(`{"mib":"IF-MIB","device":"core-01","series":960,` +
			`"scalars":0,"columns":40,"instances":24,"exact":false,` +
			`"explanation":"estimated from 24 instances observed on this device"}`))
	})

	estimate, err := c.EstimateMIBSeries(context.Background(), 1, 2)
	if err != nil {
		t.Fatalf("estimate: %v", err)
	}
	if gotMethod != http.MethodGet || gotPath != "/api/devices/1/mibs/2/estimate" {
		t.Errorf("request = %s %s", gotMethod, gotPath)
	}
	if estimate.MIB != "IF-MIB" || estimate.Series != 960 || estimate.Instances != 24 {
		t.Errorf("decoded estimate = %+v", estimate)
	}
	if estimate.Exact {
		t.Error("exact = true, want false for an estimated count")
	}
	if estimate.Explanation == "" {
		t.Error("the estimate must carry an explanation a human can act on")
	}
}

// TestDeviceMIBObjects checks the object plan is fetched from the right URL and
// decoded whole: the per-object verdict is the payload the CLI and the device
// page both render, so a dropped field is a silent wrong answer.
func TestDeviceMIBObjects(t *testing.T) {
	var gotMethod, gotPath string

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Write([]byte(`{"device_id":1,"device":"core-01","mib_id":2,"mib":"IF-MIB",` +
			`"assignment":{"enabled":true},"source":"profile","total":3,"polled":1,` +
			`"estimate":{"mib":"IF-MIB","device":"core-01","series":48,"instances":24,` +
			`"explanation":"estimated from 24 instances"},` +
			`"objects":[` +
			`{"oid":"1.3.6.1.2.1.2.2","name":"ifTable","kind":"table","polled":true,` +
			`"reason":"polled","series":48},` +
			`{"oid":"1.3.6.1.2.1.2.2.1.10","name":"ifInOctets","kind":"column","polled":false,` +
			`"reason":"covered-by-table-walk","series":0},` +
			`{"oid":"1.3.6.1.2.1.2.2.1.1","name":"ifIndex","kind":"column","polled":false,` +
			`"reason":"not-accessible","series":0}]}`))
	})

	plan, err := c.DeviceMIBObjects(context.Background(), 1, 2)
	if err != nil {
		t.Fatalf("objects: %v", err)
	}
	if gotMethod != http.MethodGet || gotPath != "/api/devices/1/mibs/2/objects" {
		t.Errorf("request = %s %s", gotMethod, gotPath)
	}
	if plan.Device != "core-01" || plan.MIB != "IF-MIB" || plan.Source != "profile" {
		t.Errorf("decoded plan = %+v", plan)
	}
	if plan.Total != 3 || plan.Polled != 1 {
		t.Errorf("total/polled = %d/%d, want 3/1", plan.Total, plan.Polled)
	}
	if plan.Estimate == nil || plan.Estimate.Series != 48 {
		t.Errorf("estimate = %+v, want the module cost carried through", plan.Estimate)
	}
	if len(plan.Objects) != 3 {
		t.Fatalf("got %d objects, want 3", len(plan.Objects))
	}

	walked := plan.Objects[0]
	if !walked.Polled || walked.Kind != "table" || walked.Series != 48 {
		t.Errorf("first object = %+v, want the walked table priced at 48 series", walked)
	}
	covered := plan.Objects[1]
	if covered.Polled || covered.Reason != "covered-by-table-walk" {
		t.Errorf("second object = %+v, want a column covered by the walk", covered)
	}
	// The embedded MIB fields have to survive the flattening.
	if covered.Name != "ifInOctets" || covered.OID != "1.3.6.1.2.1.2.2.1.10" {
		t.Errorf("second object lost its MIB fields: %+v", covered)
	}
	if plan.Objects[2].Reason != "not-accessible" {
		t.Errorf("third object reason = %q, want not-accessible", plan.Objects[2].Reason)
	}
}

// TestAssignmentEndpointsPropagateServerErrors verifies every assignment call
// surfaces a server failure rather than a zero value and a nil error.
func TestAssignmentEndpointsPropagateServerErrors(t *testing.T) {
	ctx := context.Background()

	calls := map[string]func(*Client) error{
		"DeviceMIBAssignment": func(c *Client) error {
			_, err := c.DeviceMIBAssignment(ctx, 1, 2)
			return err
		},
		"SetDeviceMIBAssignment": func(c *Client) error {
			_, err := c.SetDeviceMIBAssignment(ctx, 1, 2, MIBAssignment{Enabled: true})
			return err
		},
		"ProfileMIBAssignment": func(c *Client) error {
			_, err := c.ProfileMIBAssignment(ctx, 1, 2)
			return err
		},
		"SetProfileMIBAssignment": func(c *Client) error {
			_, err := c.SetProfileMIBAssignment(ctx, 1, 2, MIBAssignment{Enabled: true})
			return err
		},
		"EstimateMIBSeries": func(c *Client) error {
			_, err := c.EstimateMIBSeries(ctx, 1, 2)
			return err
		},
		"DeviceMIBObjects": func(c *Client) error {
			_, err := c.DeviceMIBObjects(ctx, 1, 2)
			return err
		},
	}

	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "MIB 2 is not assigned to device 1", http.StatusNotFound)
			})

			err := call(c)
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("%s: expected *APIError, got %v", name, err)
			}
			if !apiErr.NotFound() {
				t.Errorf("status = %d, want 404", apiErr.StatusCode)
			}
		})
	}
}

// TestMIBListingsCarryAssignments proves a listing exposes each module's
// polling state, which is what lets the CLI show it without a request per MIB.
func TestMIBListingsCarryAssignments(t *testing.T) {
	const deviceBody = `{"direct_mibs":[{"id":7,"name":"IF-MIB","object_count":120,` +
		`"assignment":{"enabled":false,"include_names":["^ifHC"]}}],` +
		`"all_mibs":[{"id":7,"name":"IF-MIB","object_count":120,` +
		`"assignment":{"enabled":false,"include_names":["^ifHC"]}},` +
		`{"id":8,"name":"SNMPv2-MIB","object_count":40,"assignment":{"enabled":true}}]}`

	t.Run("device", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte(deviceBody))
		})

		mibs, err := c.DeviceMIBs(context.Background(), 1)
		if err != nil {
			t.Fatalf("DeviceMIBs: %v", err)
		}
		if len(mibs.DirectMIBs) != 1 || len(mibs.AllMIBs) != 2 {
			t.Fatalf("unexpected listing: %+v", mibs)
		}
		// The MIB's own fields are flattened alongside the assignment.
		if mibs.AllMIBs[0].Name != "IF-MIB" || mibs.AllMIBs[0].ObjectCount != 120 {
			t.Errorf("MIB fields did not decode: %+v", mibs.AllMIBs[0])
		}
		if mibs.AllMIBs[0].Assignment.Enabled {
			t.Error("IF-MIB should decode as disabled")
		}
		if len(mibs.AllMIBs[0].Assignment.IncludeNames) != 1 {
			t.Errorf("filters did not decode: %+v", mibs.AllMIBs[0].Assignment)
		}
		if !mibs.AllMIBs[1].Assignment.Enabled {
			t.Error("SNMPv2-MIB should decode as enabled")
		}
	})

	t.Run("profile", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte(`{"mibs":[{"id":7,"name":"IF-MIB","object_count":120,` +
				`"assignment":{"enabled":true,"max_oids":50}}]}`))
		})

		mibs, err := c.ProfileMIBs(context.Background(), 1)
		if err != nil {
			t.Fatalf("ProfileMIBs: %v", err)
		}
		if len(mibs) != 1 {
			t.Fatalf("unexpected listing: %+v", mibs)
		}
		if mibs[0].Name != "IF-MIB" || mibs[0].ObjectCount != 120 {
			t.Errorf("MIB fields did not decode: %+v", mibs[0])
		}
		if !mibs[0].Assignment.Enabled || mibs[0].Assignment.MaxOIDs != 50 {
			t.Errorf("assignment did not decode: %+v", mibs[0].Assignment)
		}
	})
}

// TestMIBListingsWithoutAssignments covers a server that omits the field, which
// must leave the MIB itself readable rather than failing the decode.
func TestMIBListingsWithoutAssignments(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"mibs":[{"id":7,"name":"IF-MIB"}]}`))
	})

	mibs, err := c.ProfileMIBs(context.Background(), 1)
	if err != nil {
		t.Fatalf("ProfileMIBs: %v", err)
	}
	if len(mibs) != 1 || mibs[0].Name != "IF-MIB" {
		t.Fatalf("unexpected listing: %+v", mibs)
	}
	if mibs[0].Assignment.Enabled {
		t.Error("a missing assignment must decode to the zero value, not enabled")
	}
}
