package cli

import (
	"fmt"
	"io"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-netmon/internal/mib/search"
	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// LimitError reports a --limit value the CLI cannot honor.
//
// It is returned before the command makes any request, so a typo costs nothing
// and, under --refresh, fails once instead of once per frame.
type LimitError struct {
	Limit int
}

func (e *LimitError) Error() string {
	return "netmon: --limit must be 0 or greater, got " +
		strconv.Itoa(e.Limit) + "; use --limit 0 for no limit"
}

// OffsetError reports an --offset the CLI cannot honor. Like LimitError it is
// returned before the command makes any request.
type OffsetError struct {
	Offset int
}

func (e *OffsetError) Error() string {
	return "netmon: --offset must be 0 or greater, got " + strconv.Itoa(e.Offset)
}

// listFilter is the search-and-cap pair every listing command shares. One
// implementation keeps the flags, their semantics and their wording identical
// across the CLI, which is the only way the CLI and the web UI can stay at
// parity as either grows.
//
// The zero value lists everything: an unset --search matches every row and a
// defaultLimit of 0 prints them all.
type listFilter[T any] struct {
	// search, limit and offset are bound to the command's flags.
	search string
	limit  int
	offset int

	// paged marks a listing the server narrows. Such a command offers
	// --offset, sends the flags to the server as query parameters and builds
	// its page with serverPage; matches is then nil, because there is one
	// definition of what a search means and it is the server's.
	paged bool

	// noun names what is being listed, in the plural and in the words the
	// reader used to ask for it ("MIBs", "devices", "objects"). It appears in
	// the flag help, the truncation notice and the empty-result message.
	noun string

	// fields names the fields --search looks at, for the flag's help. It is the
	// promise the command makes about what a search can find.
	fields string

	// defaultLimit is the cap applied when --limit is not given. 0 prints
	// everything, which is right for any list an operator expects to read whole.
	defaultLimit int

	// matches reports whether item contains the already-lowercased needle. It
	// defers to internal/mib/search for the record kinds the API also
	// searches, so a paged listing narrowed here and the same listing narrowed
	// on the server select the same rows.
	matches func(item T, needle string) bool
}

// bind declares --search and --limit on cmd, and --offset where the server
// pages: an offset is only meaningful over a list whose order the server keeps
// the same between requests.
func (f *listFilter[T]) bind(cmd *cobra.Command) {
	flags := cmd.Flags()
	flags.StringVar(&f.search, "search", "",
		"only "+f.noun+" whose "+f.fields+" contains this")
	flags.IntVar(&f.limit, "limit", f.defaultLimit,
		"maximum "+f.noun+" to print; 0 prints all")
	if f.paged {
		flags.IntVar(&f.offset, "offset", 0,
			"skip this many matching "+f.noun+"; with --limit it pages through them")
	}
}

// validate rejects a --limit or --offset the command cannot honor. Callers run
// it at the top of RunE, before the first request.
func (f *listFilter[T]) validate() error {
	if f.limit < 0 {
		return &LimitError{Limit: f.limit}
	}
	if f.offset < 0 {
		return &OffsetError{Offset: f.offset}
	}
	return nil
}

// listQuery is the narrowing to send to the server, for a paged filter.
func (f *listFilter[T]) listQuery() client.ListQuery {
	return client.ListQuery{Search: f.search, Limit: f.limit, Offset: f.offset}
}

// apply searches items, then takes the page --offset and --limit select, in
// that order, so the counts a truncation notice reports are the searched totals
// rather than the whole set: "showing 50 of 293" has to mean 293 matches, not
// 293 rows the server sent.
//
// This is the client-side path, for the listings a server returns whole. It
// runs the same search the server would — both go through internal/mib/search —
// so which rows it selects does not depend on where the narrowing happened.
func (f *listFilter[T]) apply(items []T) listPage[T] {
	matched := items
	if needle := search.Fold(f.search); needle != "" && f.matches != nil {
		matched = make([]T, 0, len(items))
		for _, item := range items {
			if f.matches(item, needle) {
				matched = append(matched, item)
			}
		}
	}

	page := listPage[T]{
		listCounts: listCounts{
			Total:   len(items),
			Matched: len(matched),
			Offset:  min(f.offset, len(matched)),
			Limit:   f.limit,
			Search:  f.search,
		},
		Items: matched[min(f.offset, len(matched)):],
		noun:  f.noun,
	}
	if f.limit > 0 && len(page.Items) > f.limit {
		page.Items = page.Items[:f.limit]
	}
	page.Truncated = page.Offset+len(page.Items) < len(matched)
	// An empty listing must still encode as [], not null: a consumer that
	// ranges over .items should not have to special-case "nothing matched".
	if page.Items == nil {
		page.Items = []T{}
	}
	page.Shown = len(page.Items)
	return page
}

// listCounts says what a --search and --limit selected. It rides along with
// every list document the CLI emits under --json so a program can always tell a
// complete answer from a subset of one.
type listCounts struct {
	// Total is how many rows the server returned, before --search.
	Total int `json:"total"`

	// Matched is how many --search kept, before --limit.
	Matched int `json:"matched"`

	// Shown is how many are in this document.
	Shown int `json:"shown"`

	// Offset is how many matching rows were skipped to reach this page. It is
	// absent from an unpaged document, which starts at the first row.
	Offset int `json:"offset,omitempty"`

	// Truncated reports that --limit held rows back, so a consumer does not
	// have to compare counts to notice.
	Truncated bool `json:"truncated"`

	// Limit and Search echo the flags that produced these counts, so a stored
	// document explains itself.
	Limit  int    `json:"limit"`
	Search string `json:"search,omitempty"`
}

// listPage is what a listFilter selected from a list, together with the counts
// that keep a capped view honest. It is also the --json document for every
// command whose output is a bare list.
type listPage[T any] struct {
	listCounts

	// Items are the rows to print: the searched set, capped by --limit.
	Items []T `json:"items"`

	// noun names what was listed, for the messages this page prints.
	noun string
}

// counts returns the block a --json document carries to describe this page.
func (p listPage[T]) counts() listCounts { return p.listCounts }

// wholeList wraps an unfiltered slice as a page, for the nested lists a command
// deliberately never caps — a conceptual table's columns are the reason its
// table is being shown, so hiding them would defeat the command.
func wholeList[T any](items []T, noun string) listPage[T] {
	if items == nil {
		items = []T{}
	}
	return listPage[T]{
		listCounts: listCounts{Total: len(items), Matched: len(items), Shown: len(items)},
		Items:      items,
		noun:       noun,
	}
}

// writeEmpty explains an empty listing and reports whether it wrote anything.
//
// A list that is empty because nothing is configured needs different words from
// one that is empty because --search matched nothing: the first is a state to
// fix, the second is a query to widen. Printing a bare table header for either
// tells the reader neither.
func (p listPage[T]) writeEmpty(out io.Writer, indent, unfiltered string) bool {
	if p.Shown > 0 {
		return false
	}
	if p.Search != "" && p.Total > 0 {
		fmt.Fprintf(out, "%sNo %s match --search %q; searched %d.\n",
			indent, p.noun, p.Search, p.Total)
		return true
	}
	fmt.Fprintln(out, indent+unfiltered)
	return true
}

// note says how much of the list is on screen when --limit held rows back.
//
// Silent truncation is the failure that makes an operator distrust a tool: it
// sends them hunting for a row that was there all along. The count it reports
// is the searched total, which is what "of" has to mean once --search is in
// play.
func (p listPage[T]) note(out io.Writer, indent string) {
	if p.Offset > 0 {
		// A page that starts partway through has to say where: "showing 51-100
		// of 293" is the only form that tells the reader both what they are
		// looking at and where it sits in the whole.
		fmt.Fprintf(out, "%sshowing %d-%d of %d %s\n",
			indent, p.Offset+1, p.Offset+p.Shown, p.Matched, p.noun)
		return
	}
	if !p.Truncated {
		return
	}
	fmt.Fprintf(out, "%sshowing %d of %d %s; use --limit 0 for all\n",
		indent, p.Shown, p.Matched, p.noun)
}

// containsFold reports whether any of values contains needle, which the caller
// has already lowercased.
//
// It defers to internal/mib/search, which is where the CLI and the API agree on
// what "search" means: a case-insensitive substring, everywhere and on both
// sides of the wire.
func containsFold(needle string, values ...string) bool {
	return search.Contains(needle, values...)
}

// narrowedPage presents a listing whose narrowing may have happened on either
// side of the wire.
//
// A page block is the server saying "I narrowed this"; its absence says the
// whole list arrived. That is the protocol, and it decides which of the two
// paths is right: adopt the server's counts, or narrow here and count what is
// in hand. Both run the same search, so the rows are the same either way and
// only the bytes on the wire differ.
//
// Its absence is also what a server that predates these parameters answers with
// — it returned everything because it ignored the query — so a --limit stays a
// promise to the terminal rather than to a particular server build.
func narrowedPage[T any](
	items []T,
	page *client.Page,
	noun, term string,
	limit int,
	narrow func([]T) listPage[T],
) listPage[T] {
	if page == nil {
		return narrow(items)
	}
	return serverPage(items, page, noun, term, limit)
}

// serverPage presents a response the server has already narrowed as a listPage.
//
// The rows arrive searched and sliced, so the counts cannot be recovered by
// counting them: fifty objects say nothing about whether the module defines
// fifty or ten thousand. They come from the page block the server sends beside
// them.
func serverPage[T any](items []T, page *client.Page, noun, term string, limit int) listPage[T] {
	// An empty listing must still encode as [], not null.
	if items == nil {
		items = []T{}
	}

	counts := listCounts{
		Total:   len(items),
		Matched: len(items),
		Shown:   len(items),
		Limit:   limit,
		Search:  term,
	}
	if page != nil {
		counts.Total = page.Total
		counts.Matched = page.Matched
		counts.Shown = page.Count
		counts.Offset = page.Offset
		counts.Truncated = page.More()
	}
	return listPage[T]{listCounts: counts, Items: items, noun: noun}
}
