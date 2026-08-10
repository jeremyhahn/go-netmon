package client

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// TestListQueryValues checks the parameters a query renders, and above all that
// a zero query renders none: an unnarrowed call has to be the request it always
// was, or every existing consumer starts receiving a page block.
func TestListQueryValues(t *testing.T) {
	tests := []struct {
		name  string
		query ListQuery
		want  string
	}{
		{"zero query sends nothing", ListQuery{}, ""},
		{"search only", ListQuery{Search: "cevChassis"}, "search=cevChassis"},
		{"limit only", ListQuery{Limit: 3}, "limit=3"},
		{"a zero limit is no limit, so it is not sent", ListQuery{Limit: 0}, ""},
		{"a zero offset is the first page, so it is not sent", ListQuery{Offset: 0}, ""},
		{"page three", ListQuery{Limit: 50, Offset: 100}, "limit=50&offset=100"},
		{
			"everything at once",
			ListQuery{Search: "ip addr", Limit: 10, Offset: 20},
			"limit=10&offset=20&search=ip+addr",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.query.values().Encode(); got != tt.want {
				t.Errorf("values = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestListQueryValidate checks a query the server would refuse is refused here
// first, with a typed error naming the field.
func TestListQueryValidate(t *testing.T) {
	if err := (ListQuery{Limit: 50, Offset: 100}).validate(); err != nil {
		t.Errorf("a valid query was refused: %v", err)
	}

	tests := map[string]ListQuery{
		"limit":  {Limit: -1},
		"offset": {Offset: -5},
	}
	for field, query := range tests {
		t.Run(field, func(t *testing.T) {
			err := query.validate()

			var configErr *ConfigError
			if !errors.As(err, &configErr) {
				t.Fatalf("err = %v, want a *ConfigError", err)
			}
			if configErr.Field != field {
				t.Errorf("field = %q, want %q", configErr.Field, field)
			}
		})
	}
}

// TestEndpointQueryValues checks each endpoint's own parameters ride alongside
// the shared ones.
func TestEndpointQueryValues(t *testing.T) {
	objects := ObjectQuery{ListQuery: ListQuery{Search: "octets"}, PollableOnly: true}
	if got := objects.values().Encode(); got != "pollable=true&search=octets" {
		t.Errorf("object query = %q", got)
	}
	if got := (ObjectQuery{}).values().Encode(); got != "" {
		t.Errorf("an empty object query rendered %q, want nothing", got)
	}

	plan := PlanQuery{ListQuery: ListQuery{Limit: 5}, PolledOnly: true}
	if got := plan.values().Encode(); got != "limit=5&polled=true" {
		t.Errorf("plan query = %q", got)
	}
	if got := (PlanQuery{}).values().Encode(); got != "" {
		t.Errorf("an empty plan query rendered %q, want nothing", got)
	}

	mibs := MIBQuery{ListQuery: ListQuery{Offset: 50}, AvailableOnly: true, Tag: "cisco"}
	if got := mibs.values().Encode(); got != "filter=available&offset=50&tag=cisco" {
		t.Errorf("MIB query = %q", got)
	}
	if got := (MIBQuery{}).values().Encode(); got != "" {
		t.Errorf("an empty MIB query rendered %q, want nothing", got)
	}
}

// TestWithQuery checks a path with no parameters is left exactly as it was.
func TestWithQuery(t *testing.T) {
	if got := withQuery("/api/mibs", ListQuery{}.values()); got != "/api/mibs" {
		t.Errorf("withQuery = %q, want the bare path", got)
	}
	if got := withQuery("/api/mibs", ListQuery{Limit: 2}.values()); got != "/api/mibs?limit=2" {
		t.Errorf("withQuery = %q", got)
	}
}

// TestPageMore checks the question a paging control asks: is there anything
// after this page?
func TestPageMore(t *testing.T) {
	tests := []struct {
		name string
		page *Page
		want bool
	}{
		{"first of several", &Page{Matched: 293, Count: 50, Offset: 0}, true},
		{"middle", &Page{Matched: 293, Count: 50, Offset: 50}, true},
		{"last", &Page{Matched: 120, Count: 20, Offset: 100}, false},
		{"everything on one page", &Page{Matched: 3, Count: 3}, false},
		{"nothing matched", &Page{Matched: 0, Count: 0}, false},
		{"absent page block", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.page.More(); got != tt.want {
				t.Errorf("More = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestSearchMethodsSendTheirQuery checks each narrowed method reaches the right
// URL with the right parameters and decodes the page block.
func TestSearchMethodsSendTheirQuery(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name     string
		wantPath string
		wantRaw  string
		body     string
		call     func(*Client) (*Page, error)
	}{
		{
			name:     "SearchMIBObjects",
			wantPath: "/api/mibs/57",
			wantRaw:  "limit=3&search=cevChassis",
			body: `{"id":57,"name":"CISCO-ENTITY-VENDORTYPE-OID-MIB","object_count":10539,
			        "objects":[{"oid":"1.3","name":"cevChassis"}],
			        "page":{"total":10539,"matched":42,"count":1,"offset":0,"limit":3}}`,
			call: func(c *Client) (*Page, error) {
				detail, err := c.SearchMIBObjects(ctx, 57, ObjectQuery{
					ListQuery: ListQuery{Search: "cevChassis", Limit: 3},
				})
				if err != nil {
					return nil, err
				}
				if detail.ObjectCount != 10539 {
					t.Errorf("object_count = %d, want the module's own count", detail.ObjectCount)
				}
				return detail.Page, nil
			},
		},
		{
			name:     "SearchDeviceMIBObjects",
			wantPath: "/api/devices/1/mibs/5/objects",
			wantRaw:  "offset=10&polled=true",
			body: `{"device":"core-01","total":293,"polled":12,"objects":[],
			        "page":{"total":293,"matched":12,"count":2,"offset":10,"limit":0}}`,
			call: func(c *Client) (*Page, error) {
				plan, err := c.SearchDeviceMIBObjects(ctx, 1, 5, PlanQuery{
					ListQuery: ListQuery{Offset: 10}, PolledOnly: true,
				})
				if err != nil {
					return nil, err
				}
				if plan.Total != 293 || plan.Polled != 12 {
					t.Errorf("plan totals = %d polled of %d, want the whole module's",
						plan.Polled, plan.Total)
				}
				return plan.Page, nil
			},
		},
		{
			name:     "SearchMIBs",
			wantPath: "/api/mibs",
			wantRaw:  "limit=2&search=cisco",
			body: `{"mibs":[{"id":1,"name":"CISCO-BGP4-MIB"}],
			        "page":{"total":170,"matched":37,"count":1,"offset":0,"limit":2}}`,
			call: func(c *Client) (*Page, error) {
				mibs, page, err := c.SearchMIBs(ctx, MIBQuery{
					ListQuery: ListQuery{Search: "cisco", Limit: 2},
				})
				if err != nil {
					return nil, err
				}
				if len(mibs) != 1 {
					t.Errorf("mibs = %d, want the one the server sent", len(mibs))
				}
				return page, nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath, gotRaw string
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				gotPath, gotRaw = r.URL.Path, r.URL.RawQuery
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			})

			page, err := tt.call(c)
			if err != nil {
				t.Fatalf("call: %v", err)
			}
			if gotPath != tt.wantPath {
				t.Errorf("path = %q, want %q", gotPath, tt.wantPath)
			}
			if gotRaw != tt.wantRaw {
				t.Errorf("query = %q, want %q", gotRaw, tt.wantRaw)
			}
			if page == nil {
				t.Fatal("a narrowed response must carry a page block")
			}
			if page.Total == 0 || page.Matched == 0 {
				t.Errorf("page = %+v, want the server's counts", page)
			}
		})
	}
}

// TestUnnarrowedCallsSendNoQuery pins the backward-compatible path: the methods
// that existed before narrowing still make the request they always made.
func TestUnnarrowedCallsSendNoQuery(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name string
		body string
		call func(*Client) error
	}{
		{
			name: "GetMIB",
			body: `{"id":5,"name":"IP-MIB","objects":[]}`,
			call: func(c *Client) error { _, err := c.GetMIB(ctx, 5); return err },
		},
		{
			name: "DeviceMIBObjects",
			body: `{"device":"core-01","objects":[]}`,
			call: func(c *Client) error { _, err := c.DeviceMIBObjects(ctx, 1, 5); return err },
		},
		{
			name: "ListMIBs",
			body: `{"mibs":[]}`,
			call: func(c *Client) error { _, err := c.ListMIBs(ctx, false); return err },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var raw string
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				raw = r.URL.RawQuery
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			})

			if err := tt.call(c); err != nil {
				t.Fatalf("call: %v", err)
			}
			if raw != "" {
				t.Errorf("query = %q, want none", raw)
			}
		})
	}

	// The one parameter that predates narrowing is still the only one --available
	// sends.
	var raw string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"mibs":[]}`))
	})
	if _, err := c.ListMIBs(ctx, true); err != nil {
		t.Fatalf("ListMIBs: %v", err)
	}
	if raw != "filter=available" {
		t.Errorf("query = %q, want filter=available", raw)
	}
}

// TestSearchMethodsRefuseABadQueryBeforeRequesting checks a query the server
// would reject never reaches it: a typo costs no round trip, and under a
// refreshing view it fails once rather than once per frame.
func TestSearchMethodsRefuseABadQueryBeforeRequesting(t *testing.T) {
	ctx := context.Background()
	requests := 0

	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})

	bad := ListQuery{Limit: -1}
	if _, err := c.SearchMIBObjects(ctx, 1, ObjectQuery{ListQuery: bad}); err == nil {
		t.Error("SearchMIBObjects accepted a negative limit")
	}
	if _, err := c.SearchDeviceMIBObjects(ctx, 1, 2, PlanQuery{ListQuery: bad}); err == nil {
		t.Error("SearchDeviceMIBObjects accepted a negative limit")
	}
	if _, _, err := c.SearchMIBs(ctx, MIBQuery{ListQuery: bad}); err == nil {
		t.Error("SearchMIBs accepted a negative limit")
	}

	if requests != 0 {
		t.Errorf("%d requests were made; a refused query must cost none", requests)
	}
}
