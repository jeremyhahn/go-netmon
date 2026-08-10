package client

import (
	"net/url"
	"strconv"
)

// ListQuery narrows a list request on the server instead of in the caller.
//
// It exists because the alternative is to download a list to throw most of it
// away: asking for three of CISCO-ENTITY-VENDORTYPE-OID-MIB's 10,539 objects
// used to transfer 1.6 MiB. Every field is optional, and a zero ListQuery sends
// no parameters at all, so a caller that wants the whole list still gets
// exactly the request it made before this existed.
type ListQuery struct {
	// Search keeps only the records whose text contains it, ignoring case.
	// Which text is searched depends on the record: an object's name, OID or
	// description, a module's name, description or tags.
	Search string

	// Limit caps how many records the response carries. Zero asks for all of
	// them.
	Limit int

	// Offset is how many matching records to skip, which is how a page other
	// than the first is asked for: page N of size L is offset (N-1)*L.
	Offset int
}

// Page describes which slice of a list a response carried and how big the list
// it was cut from was, so a caller can render "51-100 of 293" without counting
// anything itself.
//
// The server sends it only for a request that narrowed something; see
// pageOrAll for what a caller should do when it is absent.
type Page struct {
	// Total is how many records exist before any narrowing: the module's whole
	// object count, or the whole MIB catalog. A search does not move it.
	Total int `json:"total"`

	// Matched is how many records the query selected, before offset and limit.
	Matched int `json:"matched"`

	// Count is how many records the response carried.
	Count int `json:"count"`

	// Offset and Limit echo the request.
	Offset int `json:"offset"`
	Limit  int `json:"limit"`
}

// More reports whether matching records were left beyond this page, which is
// what a caller needs to decide whether to offer a "next" or to warn that rows
// were held back.
func (p *Page) More() bool {
	if p == nil {
		return false
	}
	return p.Offset+p.Count < p.Matched
}

// validate rejects a query the server would refuse, before the request is made,
// so a typo costs nothing and — under a refreshing view — fails once rather
// than once per frame.
func (q ListQuery) validate() error {
	if q.Limit < 0 {
		return &ConfigError{Field: "limit", Reason: "must be 0 or greater, got " + strconv.Itoa(q.Limit)}
	}
	if q.Offset < 0 {
		return &ConfigError{Field: "offset", Reason: "must be 0 or greater, got " + strconv.Itoa(q.Offset)}
	}
	return nil
}

// values renders the query as URL parameters.
//
// Only what narrows is emitted. A zero query renders nothing, which is what
// keeps an unnarrowed call byte for byte the request it always was — and is
// what tells the server to answer without a page block.
func (q ListQuery) values() url.Values {
	values := url.Values{}
	if q.Search != "" {
		values.Set("search", q.Search)
	}
	if q.Limit > 0 {
		values.Set("limit", strconv.Itoa(q.Limit))
	}
	if q.Offset > 0 {
		values.Set("offset", strconv.Itoa(q.Offset))
	}
	return values
}

// ObjectQuery narrows a request for one module's objects.
type ObjectQuery struct {
	ListQuery

	// PollableOnly keeps only the objects the poller can collect. It is applied
	// before Search, so the counts report matches within the pollable set.
	PollableOnly bool
}

func (q ObjectQuery) values() url.Values {
	values := q.ListQuery.values()
	if q.PollableOnly {
		values.Set("pollable", "true")
	}
	return values
}

// PlanQuery narrows a request for a device's object plan.
type PlanQuery struct {
	ListQuery

	// PolledOnly keeps only the objects the device is actually polling.
	PolledOnly bool
}

func (q PlanQuery) values() url.Values {
	values := q.ListQuery.values()
	if q.PolledOnly {
		values.Set("polled", "true")
	}
	return values
}

// MIBQuery narrows a request for the module catalog.
type MIBQuery struct {
	ListQuery

	// AvailableOnly keeps only the modules the server reports as having data
	// flowing.
	AvailableOnly bool

	// Tag keeps only the modules carrying it, ignoring case.
	Tag string
}

func (q MIBQuery) values() url.Values {
	values := q.ListQuery.values()
	if q.AvailableOnly {
		values.Set("filter", "available")
	}
	if q.Tag != "" {
		values.Set("tag", q.Tag)
	}
	return values
}

// withQuery appends parameters to a path, leaving the path untouched when there
// are none.
func withQuery(path string, values url.Values) string {
	if len(values) == 0 {
		return path
	}
	return path + "?" + values.Encode()
}
