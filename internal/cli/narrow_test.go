package cli

import (
	"net/http"
	"net/url"
	"strconv"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/mib/search"
	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// The helpers here make the fake API the CLI tests run against narrow its
// answers the way the real one does, now that search and paging happen on the
// server. Without them these tests would assert that the CLI filters lists it
// no longer filters.
//
// The search they apply is internal/mib/search's — the same definition the API
// applies — so a CLI test cannot pass against a search the server does not
// implement. What is left, the offset and limit arithmetic and the counts, is
// the wire contract these tests exist to pin.

// narrow applies a request's search, offset and limit to an ordered list and
// returns the slice to serve with the counts that describe it, or false when
// the request asked for a page that does not exist.
func narrow[T any](
	items []T,
	values url.Values,
	keep func(item T, needle string) bool,
) ([]T, *client.Page, bool) {
	needle := search.Fold(values.Get("search"))
	limit, _ := strconv.Atoi(values.Get("limit"))
	offset, _ := strconv.Atoi(values.Get("offset"))

	matched := make([]T, 0, len(items))
	for _, item := range items {
		if keep(item, needle) {
			matched = append(matched, item)
		}
	}

	if offset > 0 && offset >= len(matched) {
		return nil, nil, false
	}

	selected := matched[min(offset, len(matched)):]
	if limit > 0 && len(selected) > limit {
		selected = selected[:limit]
	}

	page := &client.Page{
		Total:   len(items),
		Matched: len(matched),
		Count:   len(selected),
		Offset:  offset,
		Limit:   limit,
	}
	return selected, page, true
}

// narrowed reports whether a request carried any narrowing parameter, which is
// what decides whether the real server answers with a page block.
func narrowed(values url.Values) bool {
	for _, name := range []string{"search", "limit", "offset", "pollable", "polled", "tag"} {
		if values.Has(name) {
			return true
		}
	}
	return false
}

// pageOrNil returns the page block a response should carry: none at all for a
// request that narrowed nothing.
func pageOrNil(values url.Values, page *client.Page) *client.Page {
	if !narrowed(values) {
		return nil
	}
	return page
}

// keepObject is the object selection the API applies: --pollable-only first,
// then the shared search.
func keepObject(values url.Values) func(client.MIBObject, string) bool {
	pollableOnly := values.Get("pollable") == "true"
	return func(o client.MIBObject, needle string) bool {
		if pollableOnly && !o.Pollable {
			return false
		}
		return search.Object(needle, o.Name, o.OID, o.Description)
	}
}

// keepDecision is the object selection the plan endpoint applies.
func keepDecision(values url.Values) func(client.MIBObjectDecision, string) bool {
	polledOnly := values.Get("polled") == "true"
	return func(d client.MIBObjectDecision, needle string) bool {
		if polledOnly && !d.Polled {
			return false
		}
		return search.Object(needle, d.Name, d.OID, d.Description)
	}
}

// keepMIB is the module selection the catalog endpoint applies.
func keepMIB(values url.Values) func(client.MIB, string) bool {
	tag := values.Get("tag")
	return func(m client.MIB, needle string) bool {
		if !search.HasTag(m.Tags, tag) {
			return false
		}
		return search.Module(needle, m.Name, m.Description, m.Tags)
	}
}

// rejectPage answers a page that does not exist the way the API does.
func rejectPage(w http.ResponseWriter) {
	http.Error(w, "offset is past the end of the matching records", http.StatusBadRequest)
}

// mibDetailHandler serves one module, narrowing its objects the way the API
// does and recording the query it was asked for.
func mibDetailHandler(t *testing.T, detail client.MIBDetail, query *string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		values := r.URL.Query()
		if query != nil {
			*query = r.URL.RawQuery
		}

		selected, page, ok := narrow(detail.Objects, values, keepObject(values))
		if !ok {
			rejectPage(w)
			return
		}

		answer := detail
		answer.Objects = selected
		answer.Page = pageOrNil(values, page)
		respondJSON(t, w, answer)
	}
}

// catalogHandler serves the module catalog, narrowing it the way the API
// does and recording the query it was asked for.
func catalogHandler(t *testing.T, mibs []client.MIB, query *string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		values := r.URL.Query()
		if query != nil {
			*query = r.URL.RawQuery
		}

		selected, page, ok := narrow(mibs, values, keepMIB(values))
		if !ok {
			rejectPage(w)
			return
		}
		respondJSON(t, w, map[string]any{
			"mibs": selected,
			"page": pageOrNil(values, page),
		})
	}
}

// planHandler serves a device's object plan, narrowing it the way the API does
// and recording the query it was asked for.
func planHandler(t *testing.T, plan client.MIBObjectPlan, query *string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		values := r.URL.Query()
		if query != nil {
			*query = r.URL.RawQuery
		}

		selected, page, ok := narrow(plan.Objects, values, keepDecision(values))
		if !ok {
			rejectPage(w)
			return
		}

		answer := plan
		answer.Objects = selected
		answer.Page = pageOrNil(values, page)
		respondJSON(t, w, answer)
	}
}
