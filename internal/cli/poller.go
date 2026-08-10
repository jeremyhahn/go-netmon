package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// newPollerCommand builds the "netmon poller" subtree.
func newPollerCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "poller",
		Short: "Inspect the poller",
		Long: "Show what the poller is configured to collect and what it has\n" +
			"discovered while walking devices.",
	}

	cmd.AddCommand(
		newPollerConfigCommand(env),
		newPollerTablesCommand(env),
	)
	return cmd
}

func newPollerConfigCommand(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:     "config",
		Aliases: []string{"cfg"},
		Short:   "Show the configuration the poller consumes",
		Long: "Print the rendered configuration the poller pulls from the management\n" +
			"API: its version, the MIB modules it loads and every device it polls.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return env.runWithRefresh(func(ctx context.Context, out io.Writer) error {
				api, err := env.Client()
				if err != nil {
					return err
				}
				cfg, err := api.PollerConfig(ctx)
				if err != nil {
					return err
				}
				if env.Opts.JSON {
					return writeJSON(out, cfg)
				}
				return renderPollerConfig(out, cfg)
			})
		},
	}
}

func renderPollerConfig(out io.Writer, cfg *client.PollerConfig) error {
	if err := keyValues(out,
		[2]string{"Version", fmt.Sprint(cfg.Version)},
		[2]string{"Devices", fmt.Sprint(len(cfg.Devices))},
		[2]string{"MIB modules", fmt.Sprint(len(cfg.MIBModules))},
	); err != nil {
		return err
	}

	fmt.Fprintln(out, "\nMIB modules:")
	if len(cfg.MIBModules) == 0 {
		fmt.Fprintln(out, "  (none)")
	} else {
		// Copy before sorting: the config is the caller's, and a --json run of
		// the same command should show the server's order.
		modules := append([]client.PollerMIB(nil), cfg.MIBModules...)
		sort.Slice(modules, func(i, j int) bool { return modules[i].Name < modules[j].Name })
		for _, m := range modules {
			state := ""
			if !m.Enabled {
				state = " (disabled)"
			}
			fmt.Fprintf(out, "  %s%s\n", m.Name, state)
		}
	}

	fmt.Fprintln(out, "\nDevices:")
	if len(cfg.Devices) == 0 {
		fmt.Fprintln(out, "  (none)")
		return nil
	}

	t := newTable(out, "  NAME", "ADDRESS", "VERSION", "INTERVAL", "PROFILE", "MODULES", "ENABLED")
	for _, d := range cfg.Devices {
		t.Row(
			"  "+d.Name, d.Address, dash(d.SNMPVersion), formatInterval(d.PollInterval),
			dash(d.Profile), len(d.Modules), yesNo(d.Enabled),
		)
	}
	return t.Flush()
}

// formatInterval renders a poll interval given in seconds.
func formatInterval(seconds int) string {
	if seconds <= 0 {
		return "-"
	}
	return (time.Duration(seconds) * time.Second).String()
}

func newPollerTablesCommand(env *Env) *cobra.Command {
	filter := &listFilter[learnedTableRow]{
		noun:         "tables",
		fields:       "table name, entry OID, index names or source",
		defaultLimit: defaultLearnedTableLimit,
		matches: func(r learnedTableRow, needle string) bool {
			return containsFold(needle, r.displayName(), r.Key, r.EntryOID, r.Source) ||
				containsFold(needle, r.IndexNames...)
		},
	}

	cmd := &cobra.Command{
		Use:     "tables",
		Aliases: []string{"learned"},
		Short:   "Show the SNMP tables the poller has learned",
		Long: "List the tables the OID learner has identified from live walks,\n" +
			"including the index labels it will attach to their samples.\n\n" +
			"The learner accumulates a table for every entry OID it sees across\n" +
			"the whole fleet, so this list grows without bound. --search matches\n" +
			"a table's name, entry OID, index names or source, ignoring case.\n\n" +
			"Example:\n" +
			"  netmon poller tables --search ifTable",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := filter.validate(); err != nil {
				return err
			}

			return env.runWithRefresh(func(ctx context.Context, out io.Writer) error {
				api, err := env.Client()
				if err != nil {
					return err
				}
				raw, err := api.LearnedTables(ctx)
				if err != nil {
					return err
				}
				learned, err := decodeLearnedTables(raw)
				if err != nil {
					return err
				}
				page := filter.apply(learnedTableRows(learned.Tables))

				if env.Opts.JSON {
					return writeJSON(out, &learnedTablesReport{
						listPage: page,
						Stats:    learned.Stats,
					})
				}
				return renderLearnedTables(out, page, learned.Stats)
			})
		},
	}

	filter.bind(cmd)
	return cmd
}

// defaultLearnedTableLimit caps the learned-table listing. The learner keeps an
// entry for every table OID it has ever walked on any device, so this list only
// ever grows.
const defaultLearnedTableLimit = 50

// learnedTablesReport is the --json form of "poller tables": the tables the
// filter selected, the counts describing that selection, and the learner's own
// statistics.
type learnedTablesReport struct {
	listPage[learnedTableRow]

	Stats map[string]any `json:"stats,omitempty"`
}

// learnedTables is the OID learner's response envelope. The SDK returns the
// document untyped because the management API proxies it verbatim from the
// poller, so decode it here rather than making the renderer walk a map.
type learnedTables struct {
	Tables map[string]learnedTable `json:"tables"`
	Stats  map[string]any          `json:"stats"`
}

// learnedTable mirrors metrics.LearnedTableInfo, keeping only the fields worth
// showing in a terminal.
type learnedTable struct {
	EntryOID    string    `json:"entry_oid"`
	TableName   string    `json:"table_name,omitempty"`
	IndexNames  []string  `json:"index_names,omitempty"`
	ColumnOIDs  []string  `json:"column_oids"`
	SampleCount int       `json:"sample_count"`
	LastSeen    time.Time `json:"last_seen"`
	Source      string    `json:"source"`
}

// LearnedTablesError reports a learned-tables payload the CLI could not read.
type LearnedTablesError struct {
	Err error
}

func (e *LearnedTablesError) Error() string {
	return "netmon: unreadable learned-tables response: " + e.Err.Error()
}

func (e *LearnedTablesError) Unwrap() error { return e.Err }

// decodeLearnedTables re-encodes the SDK's untyped document into the shape the
// learner actually emits.
func decodeLearnedTables(raw map[string]any) (*learnedTables, error) {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, &LearnedTablesError{Err: err}
	}
	var decoded learnedTables
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil, &LearnedTablesError{Err: err}
	}
	return &decoded, nil
}

// learnedTableRow pairs a learned table with the key it is stored under. That
// key is the entry OID for a table the learner has not matched to a MIB name,
// which is the only identity such a table has.
type learnedTableRow struct {
	Key string `json:"key"`

	learnedTable
}

// displayName is the label to show for a table, which is its MIB name once the
// learner has matched one and its entry OID until then.
func (r learnedTableRow) displayName() string {
	return firstNonEmpty(r.TableName, r.Key, r.EntryOID)
}

// learnedTableRows flattens the learner's map into the order the reader sees,
// which is by displayed name. Sorting before the filter runs keeps --limit on
// the same tables between refresh frames instead of whichever ones the map
// iteration happened to yield.
func learnedTableRows(tables map[string]learnedTable) []learnedTableRow {
	rows := make([]learnedTableRow, 0, len(tables))
	for key, table := range tables {
		rows = append(rows, learnedTableRow{Key: key, learnedTable: table})
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].displayName() < rows[j].displayName()
	})
	return rows
}

func renderLearnedTables(out io.Writer, page listPage[learnedTableRow], stats map[string]any) error {
	if err := renderLearnedTableRows(out, page); err != nil {
		return err
	}

	if len(stats) == 0 {
		return nil
	}
	fmt.Fprintln(out, "\nStats:")
	keys := make([]string, 0, len(stats))
	for k := range stats {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	pairs := make([][2]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, [2]string{"  " + k, fmt.Sprintf("%v", stats[k])})
	}
	return keyValues(out, pairs...)
}

func renderLearnedTableRows(out io.Writer, page listPage[learnedTableRow]) error {
	if page.writeEmpty(out, "",
		"No learned tables. The poller discovers them as it walks devices.") {
		return nil
	}

	t := newTable(out, "TABLE", "ENTRY OID", "INDEXES", "COLUMNS", "SAMPLES", "SOURCE", "LAST SEEN")
	for _, row := range page.Items {
		t.Row(
			row.displayName(),
			dash(firstNonEmpty(row.EntryOID, row.Key)),
			dash(strings.Join(row.IndexNames, ",")),
			len(row.ColumnOIDs),
			row.SampleCount,
			dash(row.Source),
			relativeTime(row.LastSeen),
		)
	}
	if err := t.Flush(); err != nil {
		return err
	}

	page.note(out, "")
	return nil
}
