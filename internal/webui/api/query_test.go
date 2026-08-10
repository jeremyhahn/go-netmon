package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gorilla/mux"

	"github.com/jeremyhahn/go-netmon/internal/mib"
	mibstorage "github.com/jeremyhahn/go-netmon/internal/mib/storage"
	webuistorage "github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// ---------------------------------------------------------------- parsing

// TestParseListQuery reads the three parameters every list endpoint takes and
// checks the narrowed flag, which is what decides whether a response carries a
// page block at all.
func TestParseListQuery(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		want     listQuery
		narrowed bool
	}{
		{
			name: "no parameters narrows nothing",
			raw:  "",
			want: listQuery{},
		},
		{
			name:     "search is folded",
			raw:      "search=IfInOctets",
			want:     listQuery{Search: "ifinoctets", narrowed: true},
			narrowed: true,
		},
		{
			name:     "limit and offset",
			raw:      "limit=50&offset=100",
			want:     listQuery{Limit: 50, Offset: 100, narrowed: true},
			narrowed: true,
		},
		{
			name:     "an explicit zero limit still narrows the answer's shape",
			raw:      "limit=0",
			want:     listQuery{narrowed: true},
			narrowed: true,
		},
		{
			name: "an unrelated parameter is not a narrowing",
			raw:  "filter=available",
			want: listQuery{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values, err := url.ParseQuery(tt.raw)
			if err != nil {
				t.Fatalf("parse fixture %q: %v", tt.raw, err)
			}

			got, err := parseListQuery(values)
			if err != nil {
				t.Fatalf("parseListQuery(%q): %v", tt.raw, err)
			}
			if got != tt.want {
				t.Errorf("parseListQuery(%q) = %+v, want %+v", tt.raw, got, tt.want)
			}
			if got.narrowed != tt.narrowed {
				t.Errorf("narrowed = %v, want %v", got.narrowed, tt.narrowed)
			}
		})
	}
}

// TestParseListQuery_BadInput checks every malformed parameter is refused with
// a typed error naming the parameter, rather than being read as a default that
// silently answers a different question.
func TestParseListQuery_BadInput(t *testing.T) {
	tests := []struct {
		raw       string
		parameter string
	}{
		{"limit=-1", "limit"},
		{"limit=abc", "limit"},
		{"limit=", "limit"},
		{"limit=1.5", "limit"},
		{"offset=-3", "offset"},
		{"offset=none", "offset"},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			values, err := url.ParseQuery(tt.raw)
			if err != nil {
				t.Fatalf("parse fixture: %v", err)
			}

			_, err = parseListQuery(values)

			var parameterErr *QueryParameterError
			if !errors.As(err, &parameterErr) {
				t.Fatalf("err = %v, want a *QueryParameterError", err)
			}
			if parameterErr.Parameter != tt.parameter {
				t.Errorf("parameter = %q, want %q", parameterErr.Parameter, tt.parameter)
			}
			if !strings.Contains(parameterErr.Error(), tt.parameter) {
				t.Errorf("message %q does not name the parameter", parameterErr.Error())
			}
		})
	}
}

// TestParseObjectAndPlanQueries checks the endpoint-specific flags, including
// that a flag which is not a boolean is refused rather than read as false.
func TestParseObjectAndPlanQueries(t *testing.T) {
	objects, err := parseObjectQuery(url.Values{"pollable": {"true"}})
	if err != nil {
		t.Fatalf("parseObjectQuery: %v", err)
	}
	if !objects.pollableOnly || !objects.narrowed {
		t.Errorf("objectQuery = %+v, want pollableOnly and narrowed", objects)
	}

	plan, err := parsePlanQuery(url.Values{"polled": {"1"}})
	if err != nil {
		t.Fatalf("parsePlanQuery: %v", err)
	}
	if !plan.polledOnly || !plan.narrowed {
		t.Errorf("planQuery = %+v, want polledOnly and narrowed", plan)
	}

	mibs, err := parseMIBQuery(url.Values{"tag": {"cisco"}})
	if err != nil {
		t.Fatalf("parseMIBQuery: %v", err)
	}
	if mibs.tag != "cisco" || !mibs.narrowed {
		t.Errorf("mibQuery = %+v, want tag cisco and narrowed", mibs)
	}
}

// TestParseQueries_BadFlag checks a flag that is not a boolean is a typed
// error on every endpoint that offers one.
func TestParseQueries_BadFlag(t *testing.T) {
	if _, err := parseObjectQuery(url.Values{"pollable": {"yes"}}); !isQueryParameterError(err, "pollable") {
		t.Errorf("pollable=yes gave %v, want a *QueryParameterError naming pollable", err)
	}
	if _, err := parsePlanQuery(url.Values{"polled": {"maybe"}}); !isQueryParameterError(err, "polled") {
		t.Errorf("polled=maybe gave %v, want a *QueryParameterError naming polled", err)
	}
	// A bad list parameter has to surface through every wrapper too.
	if _, err := parseObjectQuery(url.Values{"limit": {"-2"}}); !isQueryParameterError(err, "limit") {
		t.Errorf("limit=-2 gave %v, want a *QueryParameterError naming limit", err)
	}
	if _, err := parsePlanQuery(url.Values{"offset": {"-2"}}); !isQueryParameterError(err, "offset") {
		t.Errorf("offset=-2 gave %v, want a *QueryParameterError naming offset", err)
	}
	if _, err := parseMIBQuery(url.Values{"limit": {"x"}}); !isQueryParameterError(err, "limit") {
		t.Errorf("limit=x gave %v, want a *QueryParameterError naming limit", err)
	}
}

// isQueryParameterError reports whether err is a QueryParameterError about the
// named parameter.
func isQueryParameterError(err error, parameter string) bool {
	var parameterErr *QueryParameterError
	return errors.As(err, &parameterErr) && parameterErr.Parameter == parameter
}

// ---------------------------------------------------------------- selectPage

// numbers is an ordered list to page through.
func numbers(n int) []int {
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}
	return items
}

// TestSelectPage_PagesAreDisjointAndComplete walks a list one page at a time
// and checks the pages reassemble it exactly: no row seen twice, none missed,
// none reordered. Without that, paging is a way to lose data quietly.
func TestSelectPage_PagesAreDisjointAndComplete(t *testing.T) {
	const (
		total    = 250
		pageSize = 100
	)
	items := numbers(total)

	var walked []int
	seen := make(map[int]bool, total)

	for offset := 0; offset < total; offset += pageSize {
		selected, page, err := selectPage(items, listQuery{Limit: pageSize, Offset: offset}, nil)
		if err != nil {
			t.Fatalf("offset %d: %v", offset, err)
		}
		if page.Total != total || page.Matched != total {
			t.Errorf("offset %d: total = %d, matched = %d, want %d for both",
				offset, page.Total, page.Matched, total)
		}
		if page.Count != len(selected) {
			t.Errorf("offset %d: count = %d but %d rows were sent", offset, page.Count, len(selected))
		}
		for _, item := range selected {
			if seen[item] {
				t.Fatalf("item %d appeared on two pages", item)
			}
			seen[item] = true
		}
		walked = append(walked, selected...)
	}

	if len(walked) != total {
		t.Fatalf("walked %d items, want %d", len(walked), total)
	}
	for i, item := range walked {
		if item != items[i] {
			t.Fatalf("paging reordered the list at %d: got %d, want %d", i, item, items[i])
		}
	}

	// The specific claim the web UI depends on: page one followed by page two
	// is the first hundred rows of the list, in order.
	first, _, err := selectPage(items, listQuery{Limit: 50}, nil)
	if err != nil {
		t.Fatalf("page one: %v", err)
	}
	second, _, err := selectPage(items, listQuery{Limit: 50, Offset: 50}, nil)
	if err != nil {
		t.Fatalf("page two: %v", err)
	}
	joined := append(append([]int{}, first...), second...)
	for i, item := range joined {
		if item != items[i] {
			t.Fatalf("page one + page two differs from the first 100 at %d: %d vs %d",
				i, item, items[i])
		}
	}
}

// TestSelectPage_NoNarrowingPassesTheListThrough checks the backward-compatible
// path: a query that narrows nothing returns the very slice it was given, so a
// response cannot change shape for a client that sent no parameters.
func TestSelectPage_NoNarrowingPassesTheListThrough(t *testing.T) {
	items := numbers(5)

	selected, page, err := selectPage(items, listQuery{}, nil)
	if err != nil {
		t.Fatalf("selectPage: %v", err)
	}
	if len(selected) != len(items) {
		t.Fatalf("selected %d items, want all %d", len(selected), len(items))
	}
	if &selected[0] != &items[0] {
		t.Error("an unnarrowed page copied the list; it must pass the same slice through")
	}
	if page.Total != 5 || page.Matched != 5 || page.Count != 5 {
		t.Errorf("page = %+v, want 5 everywhere", page)
	}

	// A nil list stays nil, so a handler that has always answered null keeps
	// answering null.
	var empty []int
	selected, _, err = selectPage(empty, listQuery{}, nil)
	if err != nil {
		t.Fatalf("selectPage on nil: %v", err)
	}
	if selected != nil {
		t.Errorf("selected = %v, want nil", selected)
	}
}

// TestSelectPage_TotalsDistinguishFilteredFromWhole is the honesty test: a
// filtered page must report both how many records matched and how many exist.
func TestSelectPage_TotalsDistinguishFilteredFromWhole(t *testing.T) {
	items := numbers(100)
	even := func(n int) bool { return n%2 == 0 }

	selected, page, err := selectPage(items, listQuery{Limit: 10}, even)
	if err != nil {
		t.Fatalf("selectPage: %v", err)
	}

	if page.Total != 100 {
		t.Errorf("total = %d, want the 100 records that exist", page.Total)
	}
	if page.Matched != 50 {
		t.Errorf("matched = %d, want the 50 the filter kept", page.Matched)
	}
	if page.Count != 10 || len(selected) != 10 {
		t.Errorf("count = %d with %d rows, want 10", page.Count, len(selected))
	}
	for _, item := range selected {
		if !even(item) {
			t.Fatalf("filter let %d through", item)
		}
	}
}

// TestSelectPage_OutOfRangeOffset checks a page past the end is refused with a
// typed error carrying the counts, and that offset zero over an empty result is
// not treated as out of range.
func TestSelectPage_OutOfRangeOffset(t *testing.T) {
	items := numbers(10)

	_, _, err := selectPage(items, listQuery{Limit: 5, Offset: 10}, nil)

	var rangeErr *PageRangeError
	if !errors.As(err, &rangeErr) {
		t.Fatalf("err = %v, want a *PageRangeError", err)
	}
	if rangeErr.Offset != 10 || rangeErr.Matched != 10 {
		t.Errorf("error = %+v, want offset 10 of 10 matched", rangeErr)
	}
	if !strings.Contains(rangeErr.Error(), "10") {
		t.Errorf("message %q must carry the counts", rangeErr.Error())
	}

	// Nothing matched, but page one of nothing is a legitimate question.
	none := func(int) bool { return false }
	selected, page, err := selectPage(items, listQuery{Limit: 5}, none)
	if err != nil {
		t.Fatalf("offset 0 over an empty result: %v", err)
	}
	if len(selected) != 0 || page.Matched != 0 || page.Total != 10 {
		t.Errorf("selected %d rows, page %+v; want an empty page of 0 matched from 10",
			len(selected), page)
	}
}

// ---------------------------------------------------------------- predicates

// TestQueryKeepPredicates checks each endpoint's selection, including that a
// query which narrows nothing returns no predicate at all — the condition the
// pass-through path depends on.
func TestQueryKeepPredicates(t *testing.T) {
	object := &mib.OIDMetadata{
		OID: "1.3.6.1.2.1.2.2.1.10", Name: "ifInOctets",
		Description: "octets received", Pollable: true,
	}
	unpollable := &mib.OIDMetadata{OID: "1.3.6.1.2.1.2.2.1.1", Name: "ifIndex"}

	if keep := (objectQuery{}).keep(); keep != nil {
		t.Error("an empty object query must produce no predicate")
	}

	keep := objectQuery{listQuery: listQuery{Search: "octets"}}.keep()
	if !keep(object) || keep(unpollable) {
		t.Error("object search must match name, OID or description and nothing else")
	}

	keep = objectQuery{pollableOnly: true}.keep()
	if !keep(object) || keep(unpollable) {
		t.Error("pollable=true must keep exactly the pollable objects")
	}

	decision := MIBObjectDecision{MIBObject: MIBObject{Name: "ifInOctets"}, Polled: true}
	skipped := MIBObjectDecision{MIBObject: MIBObject{Name: "ifOutOctets"}}

	if keepPlan := (planQuery{}).keep(); keepPlan != nil {
		t.Error("an empty plan query must produce no predicate")
	}
	keepPlan := planQuery{polledOnly: true}.keep()
	if !keepPlan(decision) || keepPlan(skipped) {
		t.Error("polled=true must keep exactly the polled objects")
	}

	if keepMIB := (mibQuery{}).keep(); keepMIB != nil {
		t.Error("an empty MIB query must produce no predicate")
	}
	keepMIB := mibQuery{tag: "cisco"}.keep()
	tagged := webuistorage.MIB{Name: "CISCO-ENVMON-MIB", Tags: []string{"Cisco"}}
	untagged := webuistorage.MIB{Name: "IF-MIB"}
	if !keepMIB(tagged) || keepMIB(untagged) {
		t.Error("tag selection must ignore case and drop untagged modules")
	}
}

// ---------------------------------------------------------- module endpoint

// pagingModule is the module the endpoint tests page through: 120 objects,
// every third one pollable, and a recognizable name to search for.
func pagingModule() []*mib.OIDMetadata {
	objects := make([]*mib.OIDMetadata, 0, 120)
	for i := 0; i < 120; i++ {
		objects = append(objects, &mib.OIDMetadata{
			OID:         fmt.Sprintf("1.3.6.1.2.1.99.1.1.1.%03d", i),
			Name:        fmt.Sprintf("pageObject%03d", i),
			Module:      "PAGE-MIB",
			Type:        "Counter32",
			Access:      "read-only",
			Status:      "current",
			Description: fmt.Sprintf("object number %d", i),
			Pollable:    i%3 == 0,
		})
	}
	return objects
}

// newPagingServer builds a server holding one module of 120 objects.
func newPagingServer(t *testing.T) *Server {
	t.Helper()

	webuiStore, err := webuistorage.New(t.TempDir())
	if err != nil {
		t.Fatalf("create webui storage: %v", err)
	}
	t.Cleanup(func() { webuiStore.Close() })

	mibStore, err := mibstorage.New(t.TempDir())
	if err != nil {
		t.Fatalf("create MIB storage: %v", err)
	}
	t.Cleanup(func() { mibStore.Close() })

	if err := webuiStore.CreateMIB(context.Background(), &webuistorage.MIB{
		Name: "PAGE-MIB", FilePath: "/mibs/PAGE-MIB.txt", ObjectCount: 120,
		Description: "a module with enough objects to page through",
		Tags:        []string{"synthetic"},
	}); err != nil {
		t.Fatalf("create MIB record: %v", err)
	}
	for _, object := range pagingModule() {
		if err := mibStore.StoreOID(object); err != nil {
			t.Fatalf("store %s: %v", object.Name, err)
		}
	}

	return &Server{
		router:    mux.NewRouter(),
		storage:   webuiStore,
		mibLoader: &mockMIBLoaderForDashboard{storage: mibStore},
	}
}

// moduleResponse is the module document with the fields these tests read. Page
// is a pointer so its absence can be told from a zeroed block.
type moduleResponse struct {
	Name        string      `json:"name"`
	ObjectCount int         `json:"object_count"`
	Objects     []MIBObject `json:"objects"`
	Page        *Page       `json:"page"`
}

// getModule issues a narrowed request for MIB 1 and decodes the answer.
func getModule(t *testing.T, server *Server, query string) (*httptest.ResponseRecorder, *moduleResponse) {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/api/mibs/1?"+query, nil)
	req = mux.SetURLVars(req, map[string]string{"id": "1"})
	rec := httptest.NewRecorder()
	server.handleGetStoredMIB(rec, req)

	if rec.Code != http.StatusOK {
		return rec, nil
	}
	var body moduleResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode module response: %v", err)
	}
	return rec, &body
}

// TestModuleObjects_NoParametersUnchanged pins the backward-compatible answer:
// every object, in the store's order, and no page block at all. Existing
// consumers — the deployed web UI, mib_cost.go, the dashboard generator — send
// no parameters and must see exactly what they saw before.
func TestModuleObjects_NoParametersUnchanged(t *testing.T) {
	server := newPagingServer(t)

	_, body := getModule(t, server, "")
	if body == nil {
		t.Fatal("unnarrowed request failed")
	}

	if body.Page != nil {
		t.Errorf("an unnarrowed request must carry no page block, got %+v", body.Page)
	}
	if len(body.Objects) != 120 {
		t.Fatalf("objects = %d, want all 120", len(body.Objects))
	}
	if body.ObjectCount != 120 {
		t.Errorf("object_count = %d, want 120", body.ObjectCount)
	}

	// The order is the store's, and the objects are the store's.
	store, err := server.mibStore()
	if err != nil {
		t.Fatalf("mib store: %v", err)
	}
	stored, err := moduleObjects(store, "PAGE-MIB")
	if err != nil {
		t.Fatalf("list module objects: %v", err)
	}
	for i, object := range body.Objects {
		if object.Name != stored[i].Name {
			t.Fatalf("object %d = %q, want %q: the endpoint reordered the module",
				i, object.Name, stored[i].Name)
		}
	}
}

// TestModuleObjects_SearchMatchesTheDocumentedFields pins the server's search
// to what the CLI's --search has always promised: an object's name, OID or
// description, ignoring case. The expectation is recomputed here from those
// three fields alone, so narrowing or widening the definition on either side
// fails this test.
func TestModuleObjects_SearchMatchesTheDocumentedFields(t *testing.T) {
	server := newPagingServer(t)
	all := pagingModule()

	for _, term := range []string{"pageobject01", "PAGEOBJECT01", "99.1.1.1.005", "number 7", "zzz"} {
		t.Run(term, func(t *testing.T) {
			_, body := getModule(t, server, "search="+url.QueryEscape(term)+"&limit=0")
			if body == nil {
				t.Fatal("search request failed")
			}

			// The same question, answered independently from the three fields
			// the CLI documents.
			needle := strings.ToLower(term)
			want := make([]string, 0, len(all))
			for _, object := range all {
				if strings.Contains(strings.ToLower(object.Name), needle) ||
					strings.Contains(strings.ToLower(object.OID), needle) ||
					strings.Contains(strings.ToLower(object.Description), needle) {
					want = append(want, object.Name)
				}
			}

			got := make([]string, 0, len(body.Objects))
			for _, object := range body.Objects {
				got = append(got, object.Name)
			}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("search %q selected %v, want %v", term, got, want)
			}
			if body.Page == nil || body.Page.Matched != len(want) {
				t.Errorf("page = %+v, want matched = %d", body.Page, len(want))
			}
		})
	}
}

// TestModuleObjects_PagesReassembleTheModule checks the paged answers are
// disjoint and complete against the unpaged one.
func TestModuleObjects_PagesReassembleTheModule(t *testing.T) {
	server := newPagingServer(t)

	_, whole := getModule(t, server, "")
	if whole == nil {
		t.Fatal("unnarrowed request failed")
	}

	var walked []string
	for offset := 0; offset < 120; offset += 50 {
		_, body := getModule(t, server, fmt.Sprintf("limit=50&offset=%d", offset))
		if body == nil {
			t.Fatalf("offset %d failed", offset)
		}
		if body.Page.Total != 120 || body.Page.Matched != 120 {
			t.Errorf("offset %d: page = %+v, want 120 total and matched", offset, body.Page)
		}
		if body.Page.Offset != offset || body.Page.Limit != 50 {
			t.Errorf("offset %d: page must echo the request, got %+v", offset, body.Page)
		}
		for _, object := range body.Objects {
			walked = append(walked, object.Name)
		}
	}

	if len(walked) != 120 {
		t.Fatalf("paging returned %d objects, want the module's 120", len(walked))
	}
	for i, name := range walked {
		if name != whole.Objects[i].Name {
			t.Fatalf("page walk differs from the whole module at %d: %q vs %q",
				i, name, whole.Objects[i].Name)
		}
	}
}

// TestModuleObjects_TotalsStayHonest checks a searched, capped page still says
// how big the module is and how many objects matched.
func TestModuleObjects_TotalsStayHonest(t *testing.T) {
	server := newPagingServer(t)

	// "pageObject01" matches pageObject010 through pageObject019: ten objects.
	_, body := getModule(t, server, "search=pageObject01&limit=3")
	if body == nil {
		t.Fatal("request failed")
	}

	switch {
	case body.Page == nil:
		t.Fatal("a narrowed request must carry a page block")
	case body.Page.Total != 120:
		t.Errorf("total = %d, want the module's 120", body.Page.Total)
	case body.Page.Matched != 10:
		t.Errorf("matched = %d, want the 10 the search found", body.Page.Matched)
	case body.Page.Count != 3:
		t.Errorf("count = %d, want the 3 the limit allowed", body.Page.Count)
	}
	if body.ObjectCount != 120 {
		t.Errorf("object_count = %d: the module's own count must not follow the page",
			body.ObjectCount)
	}
}

// TestModuleObjects_PollableOnly checks the flag that lets a client ask the
// same question --pollable-only does, and that its counts are of the pollable
// set rather than of the module.
func TestModuleObjects_PollableOnly(t *testing.T) {
	server := newPagingServer(t)

	_, body := getModule(t, server, "pollable=true&limit=0")
	if body == nil {
		t.Fatal("request failed")
	}

	if body.Page.Matched != 40 {
		t.Errorf("matched = %d, want the 40 pollable objects", body.Page.Matched)
	}
	if body.Page.Total != 120 {
		t.Errorf("total = %d, want the module's 120", body.Page.Total)
	}
	for _, object := range body.Objects {
		if !object.Pollable {
			t.Fatalf("%s is not pollable but survived pollable=true", object.Name)
		}
	}
}

// TestModuleObjects_RejectsBadQueryBeforeReadingAnything checks a malformed
// parameter is a 400 even for a MIB that does not exist, which is only possible
// if the query is parsed before the record and the module are read.
func TestModuleObjects_RejectsBadQueryBeforeReadingAnything(t *testing.T) {
	server := newPagingServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/mibs/999?limit=-1", nil)
	req = mux.SetURLVars(req, map[string]string{"id": "999"})
	rec := httptest.NewRecorder()
	server.handleGetStoredMIB(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 — the query must be refused before the store is read",
			rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "limit") {
		t.Errorf("body %q must name the parameter", rec.Body.String())
	}

	for _, query := range []string{"limit=abc", "offset=-1", "pollable=maybe"} {
		if rec, _ := getModule(t, server, query); rec.Code != http.StatusBadRequest {
			t.Errorf("%q gave status %d, want 400", query, rec.Code)
		}
	}
}

// TestModuleObjects_OutOfRangePage checks a page past the end is refused rather
// than answered with an empty list that reads as an empty module.
func TestModuleObjects_OutOfRangePage(t *testing.T) {
	server := newPagingServer(t)

	rec, _ := getModule(t, server, "limit=50&offset=500")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a page past the end", rec.Code)
	}

	// The last legitimate page is still served.
	rec, body := getModule(t, server, "limit=50&offset=100")
	if rec.Code != http.StatusOK || body == nil {
		t.Fatalf("status = %d, want the last page to be served", rec.Code)
	}
	if body.Page.Count != 20 {
		t.Errorf("count = %d, want the 20 objects left", body.Page.Count)
	}
}

// ---------------------------------------------------------- catalog endpoint

// catalogResponse is the module list with the fields these tests read.
type catalogResponse struct {
	MIBs []webuistorage.MIB `json:"mibs"`
	Page *Page              `json:"page"`
}

// newCatalogServer builds a server holding a handful of modules.
func newCatalogServer(t *testing.T) *Server {
	t.Helper()

	store, err := webuistorage.New(t.TempDir())
	if err != nil {
		t.Fatalf("create webui storage: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	ctx := context.Background()
	records := []webuistorage.MIB{
		{Name: "IF-MIB", Description: "Interfaces group", Tags: []string{"standard"}},
		{Name: "IP-MIB", Description: "IP group", Tags: []string{"standard"}},
		{Name: "CISCO-BGP4-MIB", Description: "Border Gateway Protocol", Tags: []string{"cisco"}},
		{Name: "CISCO-ENVMON-MIB", Description: "Environmental monitor", Tags: []string{"cisco"}},
	}
	for i := range records {
		if err := store.CreateMIB(ctx, &records[i]); err != nil {
			t.Fatalf("create %s: %v", records[i].Name, err)
		}
	}

	return &Server{router: mux.NewRouter(), storage: store}
}

// getCatalog issues a narrowed catalog request and decodes the answer.
func getCatalog(t *testing.T, server *Server, query string) (*httptest.ResponseRecorder, *catalogResponse) {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/api/mibs?"+query, nil)
	rec := httptest.NewRecorder()
	server.handleListStoredMIBs(rec, req)

	if rec.Code != http.StatusOK {
		return rec, nil
	}
	var body catalogResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	return rec, &body
}

// TestCatalog_NoParametersUnchanged checks the catalog answers as it always
// has: every module, in name order, with no page block.
func TestCatalog_NoParametersUnchanged(t *testing.T) {
	server := newCatalogServer(t)

	_, body := getCatalog(t, server, "")
	if body == nil {
		t.Fatal("request failed")
	}
	if body.Page != nil {
		t.Errorf("an unnarrowed catalog must carry no page block, got %+v", body.Page)
	}

	want := []string{"CISCO-BGP4-MIB", "CISCO-ENVMON-MIB", "IF-MIB", "IP-MIB"}
	got := make([]string, 0, len(body.MIBs))
	for _, m := range body.MIBs {
		got = append(got, m.Name)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("catalog = %v, want %v in name order", got, want)
	}
}

// TestCatalog_SearchAndTag checks the two ways the catalog is narrowed, and
// that the counts distinguish what matched from what exists.
func TestCatalog_SearchAndTag(t *testing.T) {
	server := newCatalogServer(t)

	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{"by name", "search=cisco-bgp", []string{"CISCO-BGP4-MIB"}},
		{"by description", "search=INTERFACES", []string{"IF-MIB"}},
		{"by tag through search", "search=standard", []string{"IF-MIB", "IP-MIB"}},
		{"by tag parameter", "tag=CISCO", []string{"CISCO-BGP4-MIB", "CISCO-ENVMON-MIB"}},
		{"tag and search together", "tag=cisco&search=envmon", []string{"CISCO-ENVMON-MIB"}},
		{"nothing matches", "search=juniper", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, body := getCatalog(t, server, tt.query)
			if body == nil {
				t.Fatal("request failed")
			}

			got := make([]string, 0, len(body.MIBs))
			for _, m := range body.MIBs {
				got = append(got, m.Name)
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("%q selected %v, want %v", tt.query, got, tt.want)
			}
			if body.Page.Total != 4 {
				t.Errorf("total = %d, want the catalog's 4 modules", body.Page.Total)
			}
			if body.Page.Matched != len(tt.want) {
				t.Errorf("matched = %d, want %d", body.Page.Matched, len(tt.want))
			}
		})
	}
}

// TestCatalog_Paging checks the catalog pages in name order and refuses a page
// past the end.
func TestCatalog_Paging(t *testing.T) {
	server := newCatalogServer(t)

	_, first := getCatalog(t, server, "limit=2")
	_, second := getCatalog(t, server, "limit=2&offset=2")
	if first == nil || second == nil {
		t.Fatal("paged requests failed")
	}

	if first.MIBs[0].Name != "CISCO-BGP4-MIB" || first.MIBs[1].Name != "CISCO-ENVMON-MIB" {
		t.Errorf("page one = %v, want the first two in name order", first.MIBs)
	}
	if second.MIBs[0].Name != "IF-MIB" || second.MIBs[1].Name != "IP-MIB" {
		t.Errorf("page two = %v, want the second two in name order", second.MIBs)
	}
	if second.Page.Offset != 2 || second.Page.Count != 2 || second.Page.Total != 4 {
		t.Errorf("page two counts = %+v", second.Page)
	}

	if rec, _ := getCatalog(t, server, "limit=2&offset=99"); rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a page past the end", rec.Code)
	}
}

// TestCatalog_BadQuery checks a malformed parameter is a 400 and not a 500.
func TestCatalog_BadQuery(t *testing.T) {
	server := newCatalogServer(t)

	for _, query := range []string{"limit=-1", "offset=x", "limit=nine"} {
		if rec, _ := getCatalog(t, server, query); rec.Code != http.StatusBadRequest {
			t.Errorf("%q gave status %d, want 400", query, rec.Code)
		}
	}
}

// ------------------------------------------------------------- plan endpoint

// getPlan issues a narrowed object-plan request and decodes the answer.
func getPlan(t *testing.T, server *Server, query string) (*httptest.ResponseRecorder, *MIBObjectPlan) {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/api/devices/1/mibs/1/objects?"+query, nil)
	req = mux.SetURLVars(req, map[string]string{"id": "1", "mibId": "1"})
	rec := httptest.NewRecorder()
	server.handleGetDeviceMIBObjects(rec, req)

	if rec.Code != http.StatusOK {
		return rec, nil
	}
	var plan MIBObjectPlan
	if err := json.NewDecoder(rec.Body).Decode(&plan); err != nil {
		t.Fatalf("decode plan: %v", err)
	}
	return rec, &plan
}

// TestPlan_NoParametersUnchanged checks the plan endpoint's unnarrowed answer
// is what it was: every object, the poller's order, no page block.
func TestPlan_NoParametersUnchanged(t *testing.T) {
	server := newObjectPlanServer(t)

	_, plan := getPlan(t, server, "")
	if plan == nil {
		t.Fatal("request failed")
	}
	if plan.Page != nil {
		t.Errorf("an unnarrowed plan must carry no page block, got %+v", plan.Page)
	}
	if plan.Total != len(plan.Objects) || plan.Total != len(planObjects()) {
		t.Errorf("total = %d with %d objects, want the module's %d",
			plan.Total, len(plan.Objects), len(planObjects()))
	}
}

// TestPlan_PagingKeepsThePollerOrder checks a paged plan is a slice of the
// order the poller sees, not a re-sort of it. A max_oids cap is spent in that
// order, so reordering would change which objects report capped-by-max-oids.
func TestPlan_PagingKeepsThePollerOrder(t *testing.T) {
	server := newObjectPlanServer(t)

	_, whole := getPlan(t, server, "")
	if whole == nil {
		t.Fatal("unnarrowed request failed")
	}

	var walked []string
	for offset := 0; offset < len(whole.Objects); offset += 3 {
		_, page := getPlan(t, server, fmt.Sprintf("limit=3&offset=%d", offset))
		if page == nil {
			t.Fatalf("offset %d failed", offset)
		}
		// The whole-module counts must survive paging: they are what the
		// summary line above the table reports.
		if page.Total != whole.Total || page.Polled != whole.Polled {
			t.Errorf("offset %d: totals = %d polled of %d, want %d of %d",
				offset, page.Polled, page.Total, whole.Polled, whole.Total)
		}
		for _, object := range page.Objects {
			walked = append(walked, object.Name)
		}
	}

	if len(walked) != len(whole.Objects) {
		t.Fatalf("walked %d objects, want %d", len(walked), len(whole.Objects))
	}
	for i, name := range walked {
		if name != whole.Objects[i].Name {
			t.Fatalf("paging reordered the plan at %d: %q vs %q", i, name, whole.Objects[i].Name)
		}
	}
}

// TestPlan_SearchAndPolledOnly checks the two narrowings the CLI's flags map
// to, and that the reported totals stay the module's.
func TestPlan_SearchAndPolledOnly(t *testing.T) {
	server := newObjectPlanServer(t)

	_, searched := getPlan(t, server, "search=octets")
	if searched == nil {
		t.Fatal("search request failed")
	}
	for _, object := range searched.Objects {
		if !strings.Contains(strings.ToLower(object.Name), "octets") {
			t.Errorf("%s does not match the search", object.Name)
		}
	}
	if searched.Page.Total != searched.Total {
		t.Errorf("page total = %d, want the plan's %d", searched.Page.Total, searched.Total)
	}

	_, polled := getPlan(t, server, "polled=true")
	if polled == nil {
		t.Fatal("polled request failed")
	}
	if len(polled.Objects) != polled.Polled {
		t.Errorf("polled=true returned %d objects, want the %d the device polls",
			len(polled.Objects), polled.Polled)
	}
	for _, object := range polled.Objects {
		if !object.Polled {
			t.Errorf("%s is not polled but survived polled=true", object.Name)
		}
	}
}

// TestPlan_BadQueryIsRefusedBeforeTheDeviceIsRead checks a malformed parameter
// is a 400 even for a device that does not exist, and that a page past the end
// is refused too.
func TestPlan_BadQueryIsRefusedBeforeTheDeviceIsRead(t *testing.T) {
	server := newObjectPlanServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/devices/99/mibs/99/objects?limit=-4", nil)
	req = mux.SetURLVars(req, map[string]string{"id": "99", "mibId": "99"})
	rec := httptest.NewRecorder()
	server.handleGetDeviceMIBObjects(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 — the query must be refused before the device is read",
			rec.Code)
	}

	for _, query := range []string{"polled=perhaps", "offset=-1", "limit=2&offset=900"} {
		if rec, _ := getPlan(t, server, query); rec.Code != http.StatusBadRequest {
			t.Errorf("%q gave status %d, want 400", query, rec.Code)
		}
	}
}
