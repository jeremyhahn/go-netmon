package api

import (
	"context"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jeremyhahn/go-netmon/internal/promname"
)

// maxDefaultNumericColumns bounds how many varying numeric columns a table
// offers before the reader has to ask for more.
//
// Chosen, not derived. IF-MIB contributes 42 columns for one device and no
// number of them is obviously right; four fits beside the identity and state
// columns without the table needing horizontal scrolling, which is the failure
// being fixed. The chooser is the escape hatch, and it is the part that has to
// be discoverable.
const maxDefaultNumericColumns = 4

// Table is one indexed group of a device's metrics: the rows share an index,
// and each metric over that index is a column.
//
// A table exists because the metrics agree on their INDEX LABELS, not because
// anyone declared one. That is what lets a MIB nobody has seen render as a
// table: the catalog says snmp_ifOperStatus and snmp_ifHCInOctets are both
// indexed by ifindex, so they are two columns of the same rows.
type Table struct {
	// ID is stable across regenerations so a renderer can key selection and
	// column choices on it.
	ID string `json:"id"`

	Module string `json:"module,omitempty"`

	// IndexLabels are the labels whose distinct tuples are the rows, in
	// INDEX-clause order. Order is part of the meaning.
	IndexLabels []string `json:"index_labels"`

	Columns []TableColumn `json:"columns"`
}

// TableColumn is one metric drawn as a column of a table.
//
// It carries no metadata of its own beyond what the column decision needs: the
// panel it refers to already holds the title, unit, enum decode and query, and
// duplicating those here would give them two homes and one of them would go
// stale.
type TableColumn struct {
	// PanelID is the panel this column draws, and where its title, unit and
	// enum decode live.
	PanelID string `json:"panel_id"`
	Metric  string `json:"metric"`

	// Kind is the catalog's, repeated here because the column decision is made
	// from it and a reader of this struct should not have to resolve the panel
	// to understand why a column is shown.
	Kind string `json:"kind,omitempty"`

	// ValueLabel is where this column's reading is when it is not the sample.
	//
	// An info metric's sample is ALWAYS 1 and its words are in a label:
	// snmp_ifalias_info{ifindex="1",value="uplink"} 1. Judging such a column by
	// its sample finds one distinct value on every row, marks it uniform, and
	// summarizes it as "1" -- so the column carrying each row's NAME is the
	// first one dropped from a table whose whole purpose is naming rows.
	ValueLabel string `json:"value_label,omitempty"`

	// Varies says whether the rows disagree about this column's value.
	//
	// A column identical on every row differentiates nothing between them, so
	// it is not a column. It is NOT hidden either: it moves to the table's
	// SUMMARY LINE, carrying Value, and that distinction is the whole design.
	//
	// Hiding would make the layout depend on the data, and then a column's
	// absence would carry meaning while having three possible causes: uniform,
	// deselected, or not reported at all. That is the shape that cost us #73,
	// where an absent series read as "the old build is running" while the
	// binary hash said otherwise.
	//
	// Summarizing costs nothing and keeps the answer on screen. ifOperStatus
	// reading "up" on every interface is uniform AND is the most valuable
	// thing on the page -- "everything is fine" is what the operator came for
	// -- and it says exactly that, in one line, without spending 72 rows of
	// width to repeat one word. When a value stops being uniform the column
	// MIGRATES from the summary into the table: a visible transition rather
	// than a silent appearance.
	Varies bool `json:"varies"`

	// Value is what every row reads, when they all read the same. Empty when
	// the column varies. It is what the summary line shows, and it is on the
	// column rather than in a separate list so that a column moving between
	// the two places is one field changing, not a row moving between arrays.
	Value string `json:"value,omitempty"`

	// Default says the column is shown before anyone chooses. Identity and
	// every enum always are; varying numerics are, up to a bound.
	Default bool `json:"default"`
}

// buildTables groups panels into tables by the index they share.
//
// Panels with no index labels are scalars and are not table columns: they stay
// cards. That separation is the catalog's own -- "a scalar has no row and no
// index" -- and it is what keeps this one rule rather than two modes. Every
// device gets both sections; a device with nothing indexed simply has no table.
func buildTables(panels []Panel) []Table {
	byIndex := map[string]*Table{}
	order := []string{}

	for _, p := range panels {
		if len(p.IndexLabels) == 0 {
			continue
		}

		key := p.Module + "\x1f" + strings.Join(p.IndexLabels, ",")
		table, seen := byIndex[key]
		if !seen {
			table = &Table{
				ID:          key,
				Module:      p.Module,
				IndexLabels: append([]string(nil), p.IndexLabels...),
			}
			byIndex[key] = table
			order = append(order, key)
		}
		table.Columns = append(table.Columns, TableColumn{
			PanelID:    p.ID,
			Metric:     p.Metric,
			Kind:       p.Kind,
			ValueLabel: p.ValueLabel,
		})
	}

	tables := make([]Table, 0, len(order))
	for _, key := range order {
		tables = append(tables, *byIndex[key])
	}
	return tables
}

// markVaryingColumns asks Prometheus what each column currently reads on every
// row, and records which columns the rows disagree about.
//
// ONE query for the whole set, not one per column. Every column is a metric
// name, so a __name__ alternation fetches all of them at once: 42 columns of
// IF-MIB on dist-01 is one request rather than 42, and the samples returned
// are the same ones the table needs for its first paint.
//
// A read that fails is not fatal. The column decision degrades to "everything
// varies", which shows more columns than necessary -- the pre-existing
// behavior -- rather than fewer, because a failed read must never be
// indistinguishable from "these columns are all the same".
func (s *Server) markVaryingColumns(ctx context.Context, device string, tables []Table) {
	for i := range tables {
		table := &tables[i]
		values, err := s.columnValues(ctx, device, table.Columns)
		if err != nil {
			log.Printf("[dashboard] column values for %s/%s: %v; every column is "+
				"treated as varying, which shows more of them rather than fewer",
				device, table.ID, err)
			for j := range table.Columns {
				table.Columns[j].Varies = true
			}
			continue
		}

		// Decided PER COLUMN, from that column's own rows.
		//
		// Counted as ROWS, not as distinct values: a column of 72 interfaces
		// all reading 1500 has 72 rows and one value, and reading the second
		// number as the first makes every uniform column look like a column
		// too small to judge -- the rule, inverted.
		//
		// And counted from the column rather than from the table. A table-wide
		// row count says nothing about a column that reported one row or none
		// -- a metric the device carries but has not answered for yet, or a
		// column populated on some rows only -- and applying the table's count
		// to it marks it uniform on the strength of its neighbours' data.
		// "Nothing to compare" and "everything agrees" are different answers
		// and only the second is a reason to leave a column out.
		//
		// Fewer than two rows therefore means shown, which errs toward more
		// columns rather than fewer, the same way a failed read does.
		for j := range table.Columns {
			reading := values[table.Columns[j].Metric]
			table.Columns[j].Varies = reading.rows < 2 ||
				reading.unreadable > 0 ||
				len(reading.distinct) > 1
			if !table.Columns[j].Varies {
				for value := range reading.distinct {
					table.Columns[j].Value = value
				}
			}
		}
	}
}

// columnReading is what one column currently reads across its rows: how many
// rows answered, and how many different things they said.
//
// Both numbers are needed and neither substitutes for the other. The distinct
// count decides whether a column differentiates anything; the row count decides
// whether that question is worth asking at all.
type columnReading struct {
	rows     int
	distinct map[string]struct{}

	// unreadable counts rows Prometheus returned that carried no usable value.
	// They are rows -- so they count toward whether there is anything to
	// compare -- and they are not values, so they cannot be part of an
	// agreement.
	unreadable int
}

// columnValues returns what each column metric currently reads across its rows.
func (s *Server) columnValues(ctx context.Context, device string, columns []TableColumn) (map[string]columnReading, error) {
	names := make([]string, 0, len(columns))
	// Where each column's reading actually is. Empty means the sample.
	readingLabel := make(map[string]string, len(columns))
	for _, c := range columns {
		names = append(names, regexp.QuoteMeta(c.Metric))
		readingLabel[c.Metric] = c.ValueLabel
	}

	// Prometheus anchors =~ on its own, so the alternation needs no ^ or $.
	query := `{__name__=~"` + strings.Join(names, "|") + `",` +
		promname.DeviceLabel + `="` + escapeLabelValue(device) + `"}`

	rows, discarded, err := s.promClient.QueryReportingDiscards(ctx, query)
	if err != nil {
		return nil, err
	}

	values := map[string]columnReading{}

	// A row Prometheus returned but the decoder could not read -- a NaN, an
	// infinity, a malformed sample -- is not a row that agrees with the others.
	// Counting only the survivors turns "three rows, one unreadable" into "two
	// rows that agree", which is a different claim and a false one: the column
	// would be summarized as though every row read the same, and the row whose
	// data is unavailable would vanish from the table.
	//
	// NaN is not exotic here. It is how Prometheus encodes an undefined result,
	// and a division or a counter reset produces one on this fleet today.
	//
	// Recorded per metric rather than for the whole read, so one unreadable row
	// costs its own column and not the other forty-one.
	for _, labels := range discarded {
		name := labels[promname.NameLabel]
		if name == "" {
			continue
		}
		reading := values[name]
		if reading.distinct == nil {
			reading.distinct = map[string]struct{}{}
		}
		reading.rows++
		reading.unreadable++
		values[name] = reading
	}

	for _, row := range rows {
		name := row.Metric[promname.NameLabel]
		if name == "" {
			continue
		}
		reading, seen := values[name]
		if !seen {
			reading.distinct = map[string]struct{}{}
		}
		reading.rows++

		// An info metric's sample is 1 on every row; its reading is in a
		// label. Comparing the samples of snmp_ifalias_info would find one
		// distinct value across a hundred differently named interfaces.
		observed := strconv.FormatFloat(row.Value, 'g', -1, 64)
		if label := readingLabel[name]; label != "" {
			observed = row.Metric[label]
		}
		reading.distinct[observed] = struct{}{}
		values[name] = reading
	}
	return values, nil
}

// chooseDefaultColumns marks the columns a table shows before anyone chooses.
//
// A column that does not vary is not a column: it goes to the summary line,
// whatever its kind. That is the same rule for a state reading "up" on every
// interface and an MTU reading 1500 on every interface -- both are one fact,
// and one fact does not need 72 rows of width to say it.
//
// Among the columns that DO vary, kind decides how many are shown. Enums and
// text are always shown: a state column where some rows disagree is the thing
// the operator is looking for, and there are only ever a few. Numerics are
// bounded, because IF-MIB alone contributes 42 and a table needing horizontal
// scrolling is the failure this replaces.
//
// Nothing here can be proved right for a MIB nobody has seen, so the chooser is
// the escape hatch and the first counter-example refines the rule.
func chooseDefaultColumns(tables []Table, panels []Panel) {
	byID := make(map[string]Panel, len(panels))
	for _, p := range panels {
		byID[p.ID] = p
	}

	for i := range tables {
		table := &tables[i]
		numerics := []int{}

		for j := range table.Columns {
			column := &table.Columns[j]
			panel := byID[column.PanelID]

			switch {
			case !column.Varies:
				// One fact. The summary line says it once.
			case panel.Type == panelState, column.Kind == string(kindEnum),
				panel.Type == panelText:
				// A state the rows disagree about, or a row's name.
				column.Default = true
			default:
				numerics = append(numerics, j)
			}
		}

		// Stable by title, so two runs of one input agree. The bound is on how
		// many are shown, not on which vary.
		sort.SliceStable(numerics, func(a, b int) bool {
			return byID[table.Columns[numerics[a]].PanelID].Title <
				byID[table.Columns[numerics[b]].PanelID].Title
		})
		for n, j := range numerics {
			if n >= maxDefaultNumericColumns {
				break
			}
			table.Columns[j].Default = true
		}
	}
}
