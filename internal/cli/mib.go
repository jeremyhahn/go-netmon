package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-netmon/internal/mib/search"
	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// newMIBCommand builds the "netmon mib" subtree.
func newMIBCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "mib",
		Aliases: []string{"mibs"},
		Short:   "Import and explore MIB modules",
		Long: "Import MIB modules and browse what they define. These commands show\n" +
			"definitions, not live values; use \"netmon metrics\" for readings.\n\n" +
			"Commands that take a MIB accept either its module name or its\n" +
			"numeric ID.",
	}

	cmd.AddCommand(
		newMIBListCommand(env),
		newMIBShowCommand(env),
		newMIBObjectsCommand(env),
		newMIBTablesCommand(env),
		newMIBImportCommand(env),
		newMIBTagsCommand(env),
		newMIBDeleteCommand(env),
	)
	return cmd
}

func newMIBListCommand(env *Env) *cobra.Command {
	var (
		available bool
		tag       string
	)
	filter := newMIBListFilter()

	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List imported MIBs",
		Long: "List imported MIB modules.\n\n" +
			"--available asks the server for only the modules that have data\n" +
			"flowing, which is the useful view on a server with hundreds of\n" +
			"imported modules.\n\n" +
			"--search matches a module's name, description or tags, ignoring\n" +
			"case, and is run by the server: only the matching modules cross\n" +
			"the wire. The listing is capped at " + strconv.Itoa(defaultMIBLimit) + " modules by default because\n" +
			"an established server has hundreds; the output always says when\n" +
			"rows were held back, and --offset pages through the rest.\n\n" +
			"Examples:\n" +
			"  netmon mib list --search cisco\n" +
			"  netmon mib list --limit 20 --offset 20\n" +
			"  netmon mib list --limit 0",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			// Validate before the first request so a typo fails immediately
			// instead of once per --refresh frame.
			if err := filter.validate(); err != nil {
				return err
			}

			return env.runWithRefresh(func(ctx context.Context, out io.Writer) error {
				api, err := env.Client()
				if err != nil {
					return err
				}
				mibs, counts, err := api.SearchMIBs(ctx, client.MIBQuery{
					ListQuery:     filter.listQuery(),
					AvailableOnly: available,
					Tag:           tag,
				})
				if err != nil {
					return err
				}
				page := narrowedPage(mibs, counts, filter.noun, filter.search, filter.limit,
					func(all []client.MIB) listPage[client.MIB] {
						return filter.apply(filterMIBsByTag(all, tag))
					})

				if env.Opts.JSON {
					return writeJSON(out, page)
				}
				return renderMIBTable(out, page)
			})
		},
	}

	f := cmd.Flags()
	f.BoolVar(&available, "available", false, "only modules the server reports as having data")
	f.StringVar(&tag, "tag", "", "only modules carrying this tag")
	filter.bind(cmd)
	return cmd
}

// defaultMIBLimit caps the module catalog. A server accumulates modules with
// every vendor bundle imported — this one is already at 170 — so the listing
// has to be bounded by default, unlike the device and profile listings whose
// size an operator controls directly.
const defaultMIBLimit = 50

// newMIBListFilter builds the search-and-cap filter for the module catalog.
// Tags are searched alongside name and description because tagging is how
// modules are grouped by vendor, and "find me the Cisco modules" is the search
// an operator actually runs.
func newMIBListFilter() *listFilter[client.MIB] {
	return &listFilter[client.MIB]{
		noun:         "MIBs",
		fields:       "name, description or tags",
		defaultLimit: defaultMIBLimit,
		paged:        true,
		matches: func(m client.MIB, needle string) bool {
			return search.Module(needle, m.Name, m.Description, m.Tags)
		},
	}
}

// filterMIBsByTag keeps the modules carrying tag, for the answer that arrived
// without the server having applied it. Tags are matched case-insensitively; an
// empty tag keeps everything.
func filterMIBsByTag(mibs []client.MIB, tag string) []client.MIB {
	if tag == "" {
		return mibs
	}
	matched := make([]client.MIB, 0, len(mibs))
	for _, m := range mibs {
		if search.HasTag(m.Tags, tag) {
			matched = append(matched, m)
		}
	}
	return matched
}

func renderMIBTable(out io.Writer, page listPage[client.MIB]) error {
	if page.writeEmpty(out, "", "No MIBs match. Import one with: netmon mib import <file>") {
		return nil
	}

	t := newTable(out, "ID", "NAME", "OBJECTS", "TAGS", "IMPORTED")
	for _, m := range page.Items {
		t.Row(m.ID, m.Name, m.ObjectCount, dash(strings.Join(m.Tags, ",")), relativeTime(m.ImportedAt))
	}
	if err := t.Flush(); err != nil {
		return err
	}

	page.note(out, "")
	return nil
}

// newAssignedMIBFilter builds the search-and-cap filter for the MIBs attached
// to a device or a profile.
//
// It is uncapped by default. These lists are the assignments an operator made,
// and the question they answer — "what is this device polling?" — needs the
// whole answer. --limit is there for the deployment whose profiles carry
// hundreds of modules.
func newAssignedMIBFilter() *listFilter[client.AssignedMIB] {
	return &listFilter[client.AssignedMIB]{
		noun:   "MIBs",
		fields: "name, description or tags",
		matches: func(m client.AssignedMIB, needle string) bool {
			return containsFold(needle, m.Name, m.Description) ||
				containsFold(needle, m.Tags...)
		},
	}
}

// objectFilter narrows a module's object list for display. A large MIB has
// thousands of objects — CISCO-ENTITY-VENDORTYPE-OID-MIB has 10,539 — so the
// default view is a searched, bounded slice.
type objectFilter struct {
	listFilter[client.MIBObject]

	pollableOnly bool
}

// newObjectFilter builds the filter the commands that list a module's objects
// share.
func newObjectFilter() *objectFilter {
	return &objectFilter{
		listFilter: listFilter[client.MIBObject]{
			noun:         "objects",
			fields:       "name, OID or description",
			defaultLimit: defaultObjectLimit,
			paged:        true,
			matches:      objectMatches,
		},
	}
}

// objectMatches reports whether an object's name, OID or description contains
// the already-lowercased needle. It is internal/mib/search's definition, which
// is also the one the API applies.
func objectMatches(o client.MIBObject, needle string) bool {
	return search.Object(needle, o.Name, o.OID, o.Description)
}

// apply drops what --pollable-only excludes, then searches and pages what is
// left. The order matters for the counts: with --pollable-only in force,
// "showing 50 of 91" has to mean 91 pollable objects, which is the list the
// reader asked for, not the module's full object count. It is the same order
// the server applies them in.
func (f *objectFilter) apply(objects []client.MIBObject) listPage[client.MIBObject] {
	if f.pollableOnly {
		pollable := make([]client.MIBObject, 0, len(objects))
		for _, o := range objects {
			if o.Pollable {
				pollable = append(pollable, o)
			}
		}
		objects = pollable
	}
	return f.listFilter.apply(objects)
}

// query is the narrowing to ask the server for. Sending it is the whole point:
// "mib objects CISCO-ENTITY-VENDORTYPE-OID-MIB --search cevChassis --limit 3"
// used to transfer 1.6 MiB to print three rows.
func (f *objectFilter) query() client.ObjectQuery {
	return client.ObjectQuery{
		ListQuery:    f.listQuery(),
		PollableOnly: f.pollableOnly,
	}
}

// bind declares the flags that drive an objectFilter.
func (f *objectFilter) bind(cmd *cobra.Command) {
	f.listFilter.bind(cmd)
	cmd.Flags().BoolVar(&f.pollableOnly, "pollable-only", false,
		"only objects the poller can collect")
}

// defaultObjectLimit keeps "netmon mib show" from filling a terminal with a
// 10,000-object module when the user only wanted to see what is in it.
const defaultObjectLimit = 50

func newMIBShowCommand(env *Env) *cobra.Command {
	filter := newObjectFilter()

	cmd := &cobra.Command{
		Use:   "show <mib>",
		Short: "Show a MIB module and its objects",
		Long: "Print a module's summary followed by its objects.\n\n" +
			"--search matches an object's name, OID or description, ignoring\n" +
			"case, and is applied before --limit: the truncation notice counts\n" +
			"what matched, not what the module defines. The server applies\n" +
			"both, so only the objects printed cross the wire.\n\n" +
			"Example:\n" +
			"  netmon mib show IF-MIB --search octets --pollable-only",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: env.completeNames(mibNames),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := filter.validate(); err != nil {
				return err
			}
			return env.showMIBObjects(args[0], filter, true)
		},
	}

	filter.bind(cmd)
	return cmd
}

func newMIBObjectsCommand(env *Env) *cobra.Command {
	filter := newObjectFilter()

	cmd := &cobra.Command{
		Use:     "objects <mib>",
		Aliases: []string{"oids"},
		Short:   "List a MIB's objects only",
		Long: "Print just the object table, without the module summary, so the\n" +
			"output can be piped into grep, awk or a spreadsheet.\n\n" +
			"--search matches an object's name, OID or description, ignoring\n" +
			"case. Large modules make the cap matter here: IP-MIB defines 293\n" +
			"objects and CISCO-ENTITY-VENDORTYPE-OID-MIB defines 10,539. The\n" +
			"server applies both, and --offset pages through what matched.\n\n" +
			"Examples:\n" +
			"  netmon mib objects IP-MIB --search ipAddress\n" +
			"  netmon mib objects IP-MIB --limit 50 --offset 50\n" +
			"  netmon mib objects IP-MIB --limit 0",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: env.completeNames(mibNames),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := filter.validate(); err != nil {
				return err
			}
			return env.showMIBObjects(args[0], filter, false)
		},
	}

	filter.bind(cmd)
	return cmd
}

// mibShowReport is the --json form of "mib show": the module exactly as the
// server described it, its objects narrowed to what the filter selected, and a
// listing block saying what was held back. The counts are additive, so a
// consumer that only reads the module's own fields is unaffected.
type mibShowReport struct {
	client.MIBDetail

	Listing listCounts `json:"listing"`
}

// showMIBObjects renders a module's objects, optionally preceded by the module
// summary. "show" and "objects" differ only in that summary, so they share it.
func (e *Env) showMIBObjects(ref string, filter *objectFilter, summary bool) error {
	return e.runWithRefresh(func(ctx context.Context, out io.Writer) error {
		api, err := e.Client()
		if err != nil {
			return err
		}
		id, err := api.ResolveMIB(ctx, ref)
		if err != nil {
			return err
		}
		detail, err := api.SearchMIBObjects(ctx, id, filter.query())
		if err != nil {
			return err
		}
		page := narrowedPage(detail.Objects, detail.Page,
			filter.noun, filter.search, filter.limit, filter.apply)

		if e.Opts.JSON {
			if !summary {
				return writeJSON(out, page)
			}
			// Emit the module with its selected objects rather than both the
			// full and the filtered set, which would only confuse a consumer.
			report := mibShowReport{MIBDetail: *detail, Listing: page.counts()}
			report.Objects = page.Items
			return writeJSON(out, &report)
		}

		indent := ""
		if summary {
			if err := renderMIBSummary(out, detail, page); err != nil {
				return err
			}
			fmt.Fprintln(out, "\nObjects:")
			indent = "  "
		}
		return renderObjectTable(out, page, indent)
	})
}

func renderMIBSummary(out io.Writer, detail *client.MIBDetail, page listPage[client.MIBObject]) error {
	return keyValues(out,
		[2]string{"ID", fmt.Sprint(detail.ID)},
		[2]string{"Name", detail.Name},
		[2]string{"Description", dash(truncate(detail.Description, 100))},
		[2]string{"File", dash(detail.FilePath)},
		[2]string{"Objects", fmt.Sprintf("%d (%d shown by filter)", detail.ObjectCount, page.Shown)},
		[2]string{"Imported", detail.ImportedAt.Format("2006-01-02 15:04:05")},
	)
}

// renderObjectTable prints the objects a filter selected. When --limit hid rows
// it says so: a silently truncated OID list sends an operator hunting for an
// object that was there all along.
func renderObjectTable(out io.Writer, page listPage[client.MIBObject], indent string) error {
	if page.writeEmpty(out, indent, "(no matching objects)") {
		return nil
	}

	t := newTable(out, indent+"OID", "NAME", "TYPE", "ACCESS", "UNIT", "POLLABLE")
	for _, o := range page.Items {
		t.Row(indent+o.OID, o.Name, dash(o.Type), dash(o.Access), dash(o.Unit), yesNo(o.Pollable))
	}
	if err := t.Flush(); err != nil {
		return err
	}

	page.note(out, indent)
	return nil
}

func newMIBTablesCommand(env *Env) *cobra.Command {
	filter := &listFilter[client.Table]{
		noun:         "tables",
		fields:       "table name, table OID, entry name or index column",
		defaultLimit: defaultTableLimit,
		matches: func(t client.Table, needle string) bool {
			return containsFold(needle,
				t.TableName, t.TableOID, t.EntryName, t.EntryOID, t.IndexColumn)
		},
	}

	cmd := &cobra.Command{
		Use:     "tables <mib>",
		Aliases: []string{"table"},
		Short:   "Show the conceptual tables a MIB defines",
		Long: "List a module's conceptual tables with the columns each one walks.\n\n" +
			"--search matches a table's name, OID, entry name or index column,\n" +
			"ignoring case, and --limit caps how many tables are printed. A\n" +
			"table's columns are the point of showing it, so they are never\n" +
			"capped.\n\n" +
			"Example:\n" +
			"  netmon mib tables IP-MIB --search ipAddress",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: env.completeNames(mibNames),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := filter.validate(); err != nil {
				return err
			}

			return env.runWithRefresh(func(ctx context.Context, out io.Writer) error {
				api, err := env.Client()
				if err != nil {
					return err
				}
				id, err := api.ResolveMIB(ctx, args[0])
				if err != nil {
					return err
				}
				tables, err := api.MIBTables(ctx, id)
				if err != nil {
					return err
				}
				page := filter.apply(tables)

				if env.Opts.JSON {
					return writeJSON(out, page)
				}
				return renderMIBTables(out, page)
			})
		},
	}

	filter.bind(cmd)
	return cmd
}

// defaultTableLimit caps the table listing. A table entry is many lines tall
// once its columns are printed, so even a few dozen tables fill a terminal.
const defaultTableLimit = 50

func renderMIBTables(out io.Writer, page listPage[client.Table]) error {
	if page.writeEmpty(out, "", "This MIB defines no tables.") {
		return nil
	}

	for i, table := range page.Items {
		if i > 0 {
			fmt.Fprintln(out)
		}
		if err := keyValues(out,
			[2]string{"Table", dash(table.TableName)},
			[2]string{"OID", dash(table.TableOID)},
			[2]string{"Entry", dash(table.EntryName)},
			[2]string{"Index", dash(table.IndexColumn)},
		); err != nil {
			return err
		}
		// Columns are the point of a table, so never cap them here.
		if err := renderObjectTable(out, wholeList(table.Columns, "columns"), "  "); err != nil {
			return err
		}
	}

	page.note(out, "")
	return nil
}

func newMIBImportCommand(env *Env) *cobra.Command {
	var (
		tags   []string
		update bool
	)

	cmd := &cobra.Command{
		Use:   "import <file>...",
		Short: "Import MIB files",
		Long: "Parse and store MIB modules.\n\n" +
			"Every file is attempted: a module that fails to parse is reported\n" +
			"and the import moves on, so one bad file in a vendor bundle does\n" +
			"not abandon the other hundred.\n\n" +
			"A module that is already imported is refused. Pass --update to\n" +
			"refresh it in place instead: its objects, object count and\n" +
			"description are re-parsed from the file, while its id, its tags and\n" +
			"every device and profile assignment — including each assignment's\n" +
			"enable state and filters — are kept. Objects the file no longer\n" +
			"defines are dropped. That is how to pick up a parser fix across a\n" +
			"whole MIB tree without detaching anything from polling, and it is\n" +
			"safe to run while the poller is collecting.\n\n" +
			"Examples:\n" +
			"  netmon mib import --tags cisco,switch mibs/CISCO-*.mib\n" +
			"  netmon mib import --update mibs/asr1001/*.my",
		Args: cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			api, err := env.Client()
			if err != nil {
				return err
			}

			opts := client.MIBImportOptions{Tags: tags, Replace: update}
			results := make([]importReport, 0, len(args))
			var tally importTally
			for _, path := range args {
				result, importErr := importMIBFile(env, api, path, opts)
				if importErr != nil {
					tally.failed++
					results = append(results, importReport{File: path, Error: importErr.Error()})
					if !env.Opts.JSON {
						fmt.Fprintf(env.Out, "FAILED   %s: %v\n", path, importErr)
					}
					continue
				}

				action := "imported"
				if result.Replaced {
					action = "updated"
					tally.updated++
				} else {
					tally.imported++
				}
				imported := result.MIB
				results = append(results, importReport{File: path, Action: action, MIB: &imported})
				if !env.Opts.JSON {
					fmt.Fprintf(env.Out, "%-8s %s as %s (id %d, %d objects)\n",
						action, path, imported.Name, imported.ID, imported.ObjectCount)
				}
			}

			if tally.failed < len(args) {
				// Something new is resolvable by name now.
				api.InvalidateMIBs()
			}

			if env.Opts.JSON {
				if err := writeJSON(env.Out, results); err != nil {
					return err
				}
			} else {
				fmt.Fprintf(env.Out, "%d imported, %d updated, %d failed\n",
					tally.imported, tally.updated, tally.failed)
			}
			if tally.failed > 0 {
				return &ImportError{Failed: tally.failed, Total: len(args)}
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringSliceVar(&tags, "tags", nil, "tags to apply to every newly imported module")
	f.BoolVar(&update, "update", false,
		"refresh modules that are already imported, keeping their assignments")
	return cmd
}

// importTally counts how a batch of files ended up, keeping "already stored,
// now refreshed" distinct from "newly stored" so a refresh run over a whole MIB
// tree shows how many modules it actually reached.
type importTally struct {
	imported int
	updated  int
	failed   int
}

// importMIBFile uploads one file under its own deadline. A batch may cover a
// whole vendor tree, so the timeout has to bound each upload rather than the
// run: one module that hangs must cost that module, not the hundred after it.
func importMIBFile(
	env *Env,
	api *client.Client,
	path string,
	opts client.MIBImportOptions,
) (*client.MIBImportResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), env.Opts.Timeout)
	defer cancel()

	return api.ImportMIBFile(ctx, path, opts)
}

// ImportError reports that some files in an import batch failed. The per-file
// causes are already printed, so this only carries the tally that decides the
// command's exit status.
type ImportError struct {
	Failed int
	Total  int
}

func (e *ImportError) Error() string {
	return strconv.Itoa(e.Failed) + " of " + strconv.Itoa(e.Total) + " MIB file(s) failed to import"
}

// importReport is one file's outcome under --json. Every file is attempted, so
// the document reports each one rather than only the batch's verdict.
type importReport struct {
	File string `json:"file"`

	// Action is "imported" for a module the server had not seen and "updated"
	// for one refreshed in place. It is empty on a file that failed.
	Action string      `json:"action,omitempty"`
	MIB    *client.MIB `json:"mib,omitempty"`
	Error  string      `json:"error,omitempty"`
}

// tagReport is the --json form of a tag change: the change itself, plus the
// tags the module now carries.
type tagReport struct {
	changeReport
	Tags []string `json:"tags"`
}

func newMIBTagsCommand(env *Env) *cobra.Command {
	var tags []string

	cmd := &cobra.Command{
		Use:   "tags <mib>",
		Short: "Set a MIB's tags",
		Long: "Replace a module's tags. Tags group modules so they can be listed\n" +
			"together, for example by vendor.\n\n" +
			"Example:\n" +
			"  netmon mib tags CISCO-ENVMON-MIB --set cisco,environment",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: env.completeNames(mibNames),
		RunE: func(_ *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), env.Opts.Timeout)
			defer cancel()

			api, err := env.Client()
			if err != nil {
				return err
			}
			id, err := api.ResolveMIB(ctx, args[0])
			if err != nil {
				return err
			}
			if err := api.SetMIBTags(ctx, id, tags); err != nil {
				return err
			}
			if env.Opts.JSON {
				return writeJSON(env.Out, tagReport{
					changeReport: changeReport{
						Action: "tagged", Kind: "mib", Ref: args[0], ID: id,
					},
					Tags: tags,
				})
			}
			fmt.Fprintf(env.Out, "Tagged %s: %s\n", args[0], dash(strings.Join(tags, ",")))
			return nil
		},
	}

	cmd.Flags().StringSliceVar(&tags, "set", nil, "replacement tag list, comma separated")
	mustMarkRequired(cmd, "set")
	return cmd
}

func newMIBDeleteCommand(env *Env) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:               "delete <mib>",
		Aliases:           []string{"rm", "remove"},
		Short:             "Remove an imported MIB",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: env.completeNames(mibNames),
		RunE: func(_ *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), env.Opts.Timeout)
			defer cancel()

			api, err := env.Client()
			if err != nil {
				return err
			}
			id, err := api.ResolveMIB(ctx, args[0])
			if err != nil {
				return err
			}
			mib, err := api.GetMIB(ctx, id)
			if err != nil {
				return err
			}

			// Deleting a module drops it from every profile and device that
			// polls it, so require an explicit confirmation.
			if !force {
				return fmt.Errorf(
					"refusing to delete MIB %s (id %d) without --force", mib.Name, mib.ID)
			}
			if err := api.DeleteMIB(ctx, id); err != nil {
				return err
			}
			// The module is gone; a later resolve must not answer from memory.
			api.InvalidateMIBs()

			if env.Opts.JSON {
				return writeJSON(env.Out, changeReport{
					Action: "deleted", Kind: "mib", Ref: mib.Name, ID: mib.ID,
				})
			}
			fmt.Fprintf(env.Out, "Deleted MIB %s (id %d)\n", mib.Name, mib.ID)
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "confirm removal")
	return cmd
}
