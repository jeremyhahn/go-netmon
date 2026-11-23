package api

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/mib/search"
	webuistorage "github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// Page describes which slice of a list a response carries, and how big the list
// it was cut from was.
//
// It exists because a paged answer is dishonest without it. A client that
// receives fifty objects cannot tell whether the module defines fifty or ten
// thousand, and one that receives fifty matches cannot tell whether its search
// found fifty or three hundred. Both numbers are reported, so "51-100 of 293
// matching, 10,539 in the module" is answerable without the client counting
// anything itself.
type Page struct {
	// Total is how many records the endpoint holds before any narrowing: the
	// module's whole object count, or the whole MIB catalog. It does not move
	// when a search does.
	Total int `json:"total"`

	// Matched is how many records the query selected, before offset and limit.
	// It is the denominator of "51-100 of 293".
	Matched int `json:"matched"`

	// Count is how many records this response carries.
	Count int `json:"count"`

	// Offset and Limit echo the request, so a stored response says which page
	// it is without the request beside it.
	Offset int `json:"offset"`
	Limit  int `json:"limit"`
}

// listQuery is the narrowing every list endpoint accepts in its query string.
//
// Paging is offset and limit rather than a cursor because the web UI needs to
// jump to an arbitrary page, not only to step forward: page N is offset
// (N-1)*limit, which a cursor cannot express. The cost of that choice is that
// the list has to be ordered the same way on every request, which is why every
// endpoint that takes a listQuery pages an already-ordered list and never
// re-orders it here.
type listQuery struct {
	// Search is the folded search term, "" for no search.
	Search string

	// Limit caps how many records the response carries. Zero means no cap,
	// which is what a request with no limit at all asks for.
	Limit int

	// Offset is how many matching records to skip.
	Offset int

	// narrowed reports whether the request carried any narrowing parameter at
	// all. A request that carried none is answered exactly as it was before
	// this existed, with no page block, so an existing consumer sees no change.
	narrowed bool
}

// parseListQuery reads the search and paging parameters from a query string.
//
// It runs before the handler touches the MIB store, so a malformed limit costs
// a parse rather than a walk of ten thousand objects.
func parseListQuery(values url.Values) (listQuery, error) {
	var q listQuery

	if values.Has("search") {
		q.Search = search.Fold(values.Get("search"))
		q.narrowed = true
	}

	limit, present, err := queryCount(values, "limit")
	if err != nil {
		return listQuery{}, err
	}
	q.Limit = limit
	q.narrowed = q.narrowed || present

	offset, present, err := queryCount(values, "offset")
	if err != nil {
		return listQuery{}, err
	}
	q.Offset = offset
	q.narrowed = q.narrowed || present

	return q, nil
}

// queryCount reads a non-negative whole number parameter, reporting whether it
// was supplied at all.
func queryCount(values url.Values, name string) (int, bool, error) {
	if !values.Has(name) {
		return 0, false, nil
	}

	raw := values.Get(name)
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, true, &QueryParameterError{
			Parameter: name, Value: raw, Reason: "must be a whole number",
		}
	}
	if n < 0 {
		return 0, true, &QueryParameterError{
			Parameter: name, Value: raw, Reason: "must be 0 or greater",
		}
	}
	return n, true, nil
}

// queryFlag reads a boolean parameter, reporting whether it was supplied.
//
// A value that is not a boolean is refused rather than read as false: a client
// that sent "pollable=yes" asked a question this server did not answer, and
// silently returning everything would look like an answer.
func queryFlag(values url.Values, name string) (bool, bool, error) {
	if !values.Has(name) {
		return false, false, nil
	}

	raw := values.Get(name)
	flag, err := strconv.ParseBool(raw)
	if err != nil {
		return false, true, &QueryParameterError{
			Parameter: name, Value: raw, Reason: "must be true or false",
		}
	}
	return flag, true, nil
}

// selectPage narrows an ordered list to the slice a query asks for, and reports
// the counts that keep that slice honest.
//
// The order it is given is the order it returns: paging is only meaningful over
// a list whose order does not change between requests, and each endpoint has
// already decided what that order is. A nil keep means the query narrows
// nothing, in which case the list is passed through untouched — the same slice,
// so a request with no parameters is answered with exactly what it was answered
// with before.
func selectPage[T any](items []T, q listQuery, keep func(T) bool) ([]T, *Page, error) {
	matched := items
	if keep != nil {
		matched = make([]T, 0, len(items))
		for _, item := range items {
			if keep(item) {
				matched = append(matched, item)
			}
		}
	}

	// An offset past the end is a page that does not exist. Answering it with
	// an empty list would show an operator a blank table and let them believe
	// the module is empty; saying so is the only useful answer. Offset zero is
	// never out of range: page one of an empty result is a legitimate request.
	if q.Offset > 0 && q.Offset >= len(matched) {
		return nil, nil, &PageRangeError{
			Offset: q.Offset, Limit: q.Limit, Matched: len(matched),
		}
	}

	selected := matched[min(q.Offset, len(matched)):]
	if q.Limit > 0 && len(selected) > q.Limit {
		selected = selected[:q.Limit]
	}

	return selected, &Page{
		Total:   len(items),
		Matched: len(matched),
		Count:   len(selected),
		Offset:  q.Offset,
		Limit:   q.Limit,
	}, nil
}

// objectQuery narrows a module's objects: the listQuery every endpoint takes,
// plus the one selection that is specific to objects.
type objectQuery struct {
	listQuery

	// pollableOnly keeps only the objects the poller can collect, which is the
	// same set the "pollable" field reports on each object.
	pollableOnly bool
}

// parseObjectQuery reads the narrowing for a module's object list.
func parseObjectQuery(values url.Values) (objectQuery, error) {
	list, err := parseListQuery(values)
	if err != nil {
		return objectQuery{}, err
	}

	pollable, present, err := queryFlag(values, "pollable")
	if err != nil {
		return objectQuery{}, err
	}
	list.narrowed = list.narrowed || present

	return objectQuery{listQuery: list, pollableOnly: pollable}, nil
}

// keep returns the predicate that selects the objects this query asks for, or
// nil when it asks for all of them.
func (q objectQuery) keep() func(*mib.OIDMetadata) bool {
	if q.Search == "" && !q.pollableOnly {
		return nil
	}
	return func(o *mib.OIDMetadata) bool {
		if q.pollableOnly && !o.Pollable {
			return false
		}
		return search.Object(q.Search, o.Name, o.OID, o.Description)
	}
}

// planQuery narrows a device's object plan. The plan's objects carry a verdict
// as well as a definition, so the selection here is on the verdict.
type planQuery struct {
	listQuery

	// polledOnly keeps only the objects the device is actually polling.
	polledOnly bool
}

// parsePlanQuery reads the narrowing for a device's object plan.
func parsePlanQuery(values url.Values) (planQuery, error) {
	list, err := parseListQuery(values)
	if err != nil {
		return planQuery{}, err
	}

	polled, present, err := queryFlag(values, "polled")
	if err != nil {
		return planQuery{}, err
	}
	list.narrowed = list.narrowed || present

	return planQuery{listQuery: list, polledOnly: polled}, nil
}

// keep returns the predicate that selects the decisions this query asks for, or
// nil when it asks for all of them.
func (q planQuery) keep() func(MIBObjectDecision) bool {
	if q.Search == "" && !q.polledOnly {
		return nil
	}
	return func(d MIBObjectDecision) bool {
		if q.polledOnly && !d.Polled {
			return false
		}
		return search.Object(q.Search, d.Name, d.OID, d.Description)
	}
}

// mibQuery narrows the module catalog.
type mibQuery struct {
	listQuery

	// tag keeps only the modules carrying it, ignoring case.
	tag string
}

// parseMIBQuery reads the narrowing for the module catalog.
//
// The pre-existing "filter=available" parameter is deliberately not part of it:
// it is read where it always was, and does not by itself make a request a
// narrowed one, so the web UI and CLI calls that already use it are answered
// exactly as before.
func parseMIBQuery(values url.Values) (mibQuery, error) {
	list, err := parseListQuery(values)
	if err != nil {
		return mibQuery{}, err
	}

	tag := values.Get("tag")
	list.narrowed = list.narrowed || values.Has("tag")

	return mibQuery{listQuery: list, tag: tag}, nil
}

// keep returns the predicate that selects the modules this query asks for, or
// nil when it asks for all of them.
func (q mibQuery) keep() func(webuistorage.MIB) bool {
	if q.Search == "" && q.tag == "" {
		return nil
	}
	return func(m webuistorage.MIB) bool {
		if !search.HasTag(m.Tags, q.tag) {
			return false
		}
		return search.Module(q.Search, m.Name, m.Description, m.Tags)
	}
}

// respondQueryError answers a refused narrowing.
//
// Both faults it can report are the client's: it asked for something this
// server does not offer, or for a page that does not exist. Neither is a server
// fault and neither must be reported as one, because a 500 sends an operator to
// the server logs for a typo in a URL.
//
// Parsing a query and paging a list produce nothing else, so the handlers that
// only do those two things can call this with any error they get. A handler
// that also does other work asks isQueryFault first.
func respondQueryError(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusBadRequest)
}

// isQueryFault reports whether err is a fault in the request's query string, as
// opposed to anything the handler went on to do with it.
func isQueryFault(err error) bool {
	var (
		parameter *QueryParameterError
		pageRange *PageRangeError
	)
	return errors.As(err, &parameter) || errors.As(err, &pageRange)
}
