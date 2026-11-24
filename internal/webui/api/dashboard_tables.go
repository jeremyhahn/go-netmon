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

// maxDefaultNumericColumns bounds the varying numeric columns a table shows by default.
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
	// A column identical on every row is not hidden: it moves to the table's
	// summary line, carrying Value. Hiding would make a column's absence ambiguous
	// (uniform, deselected, or not reported). When a value stops being uniform the
	// column moves from the summary into the table.
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

// buildTables groups indexed panels into tables; scalars stay cards.
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

// markVaryingColumns reads every column in one query and records which vary; a failed read marks all varying.
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

		// Decided per column from its own row count: fewer than two rows means shown.
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

// columnReading is how many rows a column has and how many distinct values they hold.
type columnReading struct {
	rows     int
	distinct map[string]struct{}

	// unreadable counts rows with no usable value: rows, but not values.
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

	// An unreadable row (NaN, infinity, malformed) counts as a row that does not agree, per metric.
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

// chooseDefaultColumns: uniform columns go to the summary line; varying enums and text always show; varying numerics are bounded.
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
