package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"testing"

	webuiStorage "github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// The table is the answer to a usability report: hundreds of interfaces drawn
// as hundreds of series behind scroll arrows, which is unreadable and slow for
// the same reason. Rows are listed with their current values and the reader
// picks which to chart, so history is fetched for a handful rather than for all
// of them.
//
// These tests are about which columns a table offers, because that is where the
// design is decided. Everything else follows from the catalog.

// tableFixture builds a device with one MIB and a catalog, and returns the
// dashboard the browser draws that module's tab from.
func tableFixture(t *testing.T, f catalogFixture, module string) Dashboard {
	t.Helper()

	s := newTestServer(t)
	s.promClient = catalogClient(t, f)

	ctx := context.Background()
	device := storeDevice(t, s, "dist-01", "192.0.2.14")
	mib := &webuiStorage.MIB{Name: module, FilePath: "/mibs/" + module + ".mib"}
	if err := s.storage.CreateMIB(ctx, mib); err != nil {
		t.Fatalf("CreateMIB: %v", err)
	}
	if err := s.storage.AssignMIBToDevice(ctx, device, mib.ID); err != nil {
		t.Fatalf("AssignMIBToDevice: %v", err)
	}

	rec := do(t, s, http.MethodGet,
		"/api/mibs/"+itoa(mib.ID)+"/dashboard?device=dist-01", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	return decodeDashboard(t, rec.Body.String())
}

// defaultColumns returns the metrics a table shows before anyone chooses.
func defaultColumns(t *testing.T, dash Dashboard) []string {
	t.Helper()

	if len(dash.Tables) != 1 {
		t.Fatalf("tables = %d, want 1: every metric here shares one index", len(dash.Tables))
	}
	shown := []string{}
	for _, c := range dash.Tables[0].Columns {
		if c.Default {
			shown = append(shown, c.Metric)
		}
	}
	return shown
}

// columnFor finds one metric's column.
func columnFor(t *testing.T, dash Dashboard, metric string) TableColumn {
	t.Helper()

	for _, table := range dash.Tables {
		for _, c := range table.Columns {
			if c.Metric == metric {
				return c
			}
		}
	}
	t.Fatalf("no column for %s", metric)
	return TableColumn{}
}

// TestAUniformStateColumnMovesToTheSummaryRatherThanVanishing is the decision
// the whole column rule turns on.
//
// ifOperStatus reading "up" on every interface is uniform AND is the most
// valuable thing on the page: "everything is fine" is the answer the operator
// came for. Two wrong answers were considered and both are recorded here:
//
//   - HIDE it, because it differentiates no rows. Then the status column is
//     absent exactly when the network is healthy and appears only once
//     something breaks -- a dashboard that goes dark when it is working. Worse,
//     its absence would carry meaning with three possible causes: uniform,
//     deselected, or not reported at all. That is #73's shape, in a UI.
//   - EXEMPT enums from the rule and keep them as full columns. Then 72 rows
//     spend their width repeating one word, which is the crowding this
//     replaces.
//
// So a uniform column of ANY kind moves to the summary line, carrying the value
// every row reads. The answer stays on screen and costs one line. When a row
// disagrees the column migrates back into the table -- a visible transition,
// not a silent appearance.
func TestAUniformStateColumnMovesToTheSummaryRatherThanVanishing(t *testing.T) {
	catalog := []map[string]interface{}{
		{catMetric: "snmp_ifOperStatus", catModule: "IF-MIB", catObject: "ifOperStatus",
			catKind: "enum", catDescription: "operational state", catIndexLabels: "ifindex"},
		{catMetric: "snmp_ifMtu", catModule: "IF-MIB", catObject: "ifMtu",
			catKind: "gauge", catDescription: "MTU", catIndexLabels: "ifindex"},
	}
	dash := tableFixture(t, catalogFixture{
		Retained: catalog, Current: catalog,
		Device: []string{"snmp_ifOperStatus", "snmp_ifMtu"},
		Values: map[string][]float64{
			"snmp_ifOperStatus": {1, 1, 1},
			"snmp_ifMtu":        {1500, 1500, 1500},
		},
	}, "IF-MIB")

	if shown := defaultColumns(t, dash); len(shown) != 0 {
		t.Errorf("default columns = %v, want none: every row agrees about both, so "+
			"neither tells the reader anything the summary cannot say in one line",
			shown)
	}

	for _, want := range []struct{ metric, value string }{
		{"snmp_ifOperStatus", "1"},
		{"snmp_ifMtu", "1500"},
	} {
		column := columnFor(t, dash, want.metric)
		if column.Varies {
			t.Errorf("%s is marked as varying; all three rows read the same", want.metric)
		}
		if column.Value != want.value {
			t.Errorf("%s carries value %q, want %q: the summary line has nothing to "+
				"show without it, and a column that leaves the table without "+
				"leaving its value behind is the hiding this rule exists to avoid",
				want.metric, column.Value, want.value)
		}
	}
}

// TestAStateColumnTheRowsDisagreeAboutIsAColumn is the other half: the moment
// one interface goes down, the column migrates out of the summary and into the
// table, where it can be sorted and read per row.
func TestAStateColumnTheRowsDisagreeAboutIsAColumn(t *testing.T) {
	catalog := []map[string]interface{}{
		{catMetric: "snmp_ifOperStatus", catModule: "IF-MIB", catObject: "ifOperStatus",
			catKind: "enum", catDescription: "operational state", catIndexLabels: "ifindex"},
	}
	dash := tableFixture(t, catalogFixture{
		Retained: catalog, Current: catalog,
		Device: []string{"snmp_ifOperStatus"},
		Values: map[string][]float64{"snmp_ifOperStatus": {1, 1, 2}},
	}, "IF-MIB")

	if shown := defaultColumns(t, dash); !slices.Contains(shown, "snmp_ifOperStatus") {
		t.Errorf("default columns = %v, want snmp_ifOperStatus: one interface is "+
			"down and which one is the question being asked", shown)
	}
	if column := columnFor(t, dash, "snmp_ifOperStatus"); column.Value != "" {
		t.Errorf("a varying column carries value %q; there is no single value to "+
			"summarize and offering one would be a lie", column.Value)
	}
}

// TestAVaryingNumericColumnIsShown is the other half of the same rule: the
// filter is about whether the rows disagree, not about the column being numeric.
func TestAVaryingNumericColumnIsShown(t *testing.T) {
	catalog := []map[string]interface{}{
		{catMetric: "snmp_ifMtu", catModule: "IF-MIB", catObject: "ifMtu",
			catKind: "gauge", catDescription: "MTU", catIndexLabels: "ifindex"},
	}
	dash := tableFixture(t, catalogFixture{
		Retained: catalog, Current: catalog,
		Device: []string{"snmp_ifMtu"},
		Values: map[string][]float64{"snmp_ifMtu": {1500, 9000, 1500}},
	}, "IF-MIB")

	if shown := defaultColumns(t, dash); !slices.Contains(shown, "snmp_ifMtu") {
		t.Errorf("default columns = %v, want snmp_ifMtu: one row disagrees, so the "+
			"column is telling the reader something", shown)
	}
}

// TestASingleRowTableShowsItsColumns guards the degenerate case the uniformity
// rule gets wrong on its own.
//
// One row cannot disagree with itself, so every column is trivially uniform and
// a table of one interface would come back with nothing but its identity. A
// table too small to compare shows what it has.
func TestASingleRowTableShowsItsColumns(t *testing.T) {
	catalog := []map[string]interface{}{
		{catMetric: "snmp_ifMtu", catModule: "IF-MIB", catObject: "ifMtu",
			catKind: "gauge", catDescription: "MTU", catIndexLabels: "ifindex"},
	}
	dash := tableFixture(t, catalogFixture{
		Retained: catalog, Current: catalog,
		Device: []string{"snmp_ifMtu"},
		Values: map[string][]float64{"snmp_ifMtu": {1500}},
	}, "IF-MIB")

	if shown := defaultColumns(t, dash); !slices.Contains(shown, "snmp_ifMtu") {
		t.Errorf("default columns = %v, want snmp_ifMtu: with one row there is "+
			"nothing to compare it against, and a table showing only its own "+
			"index is worse than one showing a value that might be uniform", shown)
	}
}

// TestScalarsAreNotTableColumns is the rule that keeps this one page rather
// than two modes: scalars are cards, indexed metrics are tables, and a device
// gets whichever sections it has data for.
func TestScalarsAreNotTableColumns(t *testing.T) {
	catalog := []map[string]interface{}{
		{catMetric: "snmp_sysUpTime", catModule: "SNMPv2-MIB", catObject: "sysUpTime",
			catKind: "counter", catDescription: "uptime"},
		{catMetric: "snmp_ifMtu", catModule: "SNMPv2-MIB", catObject: "ifMtu",
			catKind: "gauge", catDescription: "MTU", catIndexLabels: "ifindex"},
	}
	dash := tableFixture(t, catalogFixture{
		Retained: catalog, Current: catalog,
		Device: []string{"snmp_sysUpTime", "snmp_ifMtu"},
		Values: map[string][]float64{"snmp_ifMtu": {1500, 9000}},
	}, "SNMPv2-MIB")

	if len(dash.Tables) != 1 {
		t.Fatalf("tables = %d, want 1: only the indexed metric has rows", len(dash.Tables))
	}
	for _, c := range dash.Tables[0].Columns {
		if c.Metric == "snmp_sysUpTime" {
			t.Error("the scalar became a table column; a scalar has no row and no " +
				"index, and a one-row table of it is worse than the card it was")
		}
	}
	if panelFor(t, dash, "snmp_sysUpTime").Metric == "" {
		t.Error("the scalar lost its panel; it should still be a card")
	}
}

// TestVaryingNumericColumnsAreBounded stops the table becoming the thing it
// replaced. IF-MIB contributes 42 columns for one device, and a table needing
// horizontal scrolling is the same failure as a legend needing scroll arrows.
func TestVaryingNumericColumnsAreBounded(t *testing.T) {
	names := []string{"a", "b", "c", "d", "e", "f"}
	catalog := make([]map[string]interface{}, 0, len(names))
	reported := make([]string, 0, len(names))
	values := map[string][]float64{}
	for _, name := range names {
		metric := "snmp_if" + name
		catalog = append(catalog, map[string]interface{}{
			catMetric: metric, catModule: "IF-MIB", catObject: "if" + name,
			catKind: "gauge", catDescription: "column " + name, catIndexLabels: "ifindex",
		})
		reported = append(reported, metric)
		values[metric] = []float64{1, 2, 3}
	}
	dash := tableFixture(t, catalogFixture{
		Retained: catalog, Current: catalog, Device: reported, Values: values,
	}, "IF-MIB")

	shown := defaultColumns(t, dash)
	if len(shown) != maxDefaultNumericColumns {
		t.Errorf("default columns = %v (%d), want %d: every one of these varies, so "+
			"the bound is the only thing keeping the table readable",
			shown, len(shown), maxDefaultNumericColumns)
	}
}

// TestAColumnWithTooFewRowsIsJudgedOnItsOwn is the case a table-wide row count
// gets wrong.
//
// Uniformity is a statement about a column's rows. Asking "does the TABLE have
// enough rows to compare?" and then applying that answer to every column marks
// a column uniform on the strength of its neighbours' data -- so a metric the
// device carries but has answered for once, or answered for on one row only,
// is left out of the table as though its rows had agreed. "Nothing to compare"
// and "everything agrees" are different answers and only the second is a reason
// to drop a column.
func TestAColumnWithTooFewRowsIsJudgedOnItsOwn(t *testing.T) {
	catalog := []map[string]interface{}{
		{catMetric: "snmp_ifSpeed", catModule: "IF-MIB", catObject: "ifSpeed",
			catKind: "gauge", catDescription: "speed", catIndexLabels: "ifindex"},
		{catMetric: "snmp_ifLastChange", catModule: "IF-MIB", catObject: "ifLastChange",
			catKind: "gauge", catDescription: "last change", catIndexLabels: "ifindex"},
	}
	dash := tableFixture(t, catalogFixture{
		Retained: catalog, Current: catalog,
		Device: []string{"snmp_ifSpeed", "snmp_ifLastChange"},
		Values: map[string][]float64{
			// Plenty of rows, and they agree: inventory, left out.
			"snmp_ifSpeed": {1000, 1000, 1000, 1000},
			// One row. Nothing to compare it against.
			"snmp_ifLastChange": {42},
		},
	}, "IF-MIB")

	shown := defaultColumns(t, dash)
	if !slices.Contains(shown, "snmp_ifLastChange") {
		t.Errorf("default columns = %v, want snmp_ifLastChange: it answered for one "+
			"row, so nothing about it is known to be uniform -- the four rows that "+
			"agree belong to a different column", shown)
	}
	if slices.Contains(shown, "snmp_ifSpeed") {
		t.Errorf("default columns = %v, want snmp_ifSpeed left out: four rows, one "+
			"value", shown)
	}
}

// TestATextColumnIsJudgedByItsWordsNotItsSample is the column whose loss would
// have been most obvious and which a sample-based rule drops first.
//
// An info metric's sample is 1 on every row and its reading is in a label:
//
//	snmp_ifalias_info{ifindex="1",value="uplink"} 1
//	snmp_ifalias_info{ifindex="2",value="server"} 1
//
// Comparing samples finds one distinct value across a hundred differently named
// interfaces, marks the column uniform, and summarizes it as "1". So the column
// carrying each row's NAME is the first one dropped from a table whose whole
// purpose is naming rows, and the summary line says something meaningless.
//
// A fixture that can only set samples cannot express this, which is why the
// stub grew a way to say what an info row reads.
func TestATextColumnIsJudgedByItsWordsNotItsSample(t *testing.T) {
	catalog := []map[string]interface{}{
		{catMetric: "snmp_ifalias_info", catModule: "IF-MIB", catObject: "ifAlias",
			catKind: "string", catDescription: "interface alias",
			catIndexLabels: "ifindex"},
	}
	dash := tableFixture(t, catalogFixture{
		Retained: catalog, Current: catalog,
		Device: []string{"snmp_ifalias_info"},
		Labels: map[string][]string{
			"snmp_ifalias_info": {"uplink to core", "server rack 3", "spare"},
		},
	}, "IF-MIB")

	column := columnFor(t, dash, "snmp_ifalias_info")
	if !column.Varies {
		t.Errorf("the alias column is marked uniform with value %q: every row's "+
			"sample is 1, so a rule reading samples finds one value and drops the "+
			"column that names the rows", column.Value)
	}
	if shown := defaultColumns(t, dash); !slices.Contains(shown, "snmp_ifalias_info") {
		t.Errorf("default columns = %v, want the alias column: it is what makes a "+
			"row recognizable as a port rather than as an index", shown)
	}
}

// TestATextColumnThatReallyIsUniformIsSummarized is the other side, and the
// reason the fix is "read the label" rather than "always show text columns".
//
// A description column reading the same words on every row is as much one fact
// as an MTU reading 1500 on every row, and the summary line says it once.
func TestATextColumnThatReallyIsUniformIsSummarized(t *testing.T) {
	catalog := []map[string]interface{}{
		{catMetric: "snmp_iftype_info", catModule: "IF-MIB", catObject: "ifType",
			catKind: "string", catDescription: "interface type",
			catIndexLabels: "ifindex"},
	}
	dash := tableFixture(t, catalogFixture{
		Retained: catalog, Current: catalog,
		Device: []string{"snmp_iftype_info"},
		Labels: map[string][]string{
			"snmp_iftype_info": {"ethernetCsmacd", "ethernetCsmacd", "ethernetCsmacd"},
		},
	}, "IF-MIB")

	column := columnFor(t, dash, "snmp_iftype_info")
	if column.Varies {
		t.Error("every row reads ethernetCsmacd; that is one fact and the summary " +
			"line can say it once")
	}
	if column.Value != "ethernetCsmacd" {
		t.Errorf("the summary carries %q, want the words the rows share -- not the "+
			"sample, which is 1 on every info row", column.Value)
	}
}

// TestAFailedColumnReadShowsMoreColumnsNotFewer pins the fail-open branch,
// which is the safety property of the whole column rule.
//
// Deciding which columns to hide requires knowing what every column currently
// reads. When that read fails, the zero value of the answer is "no distinct
// values anywhere", which reads as "every column is uniform" and would
// summarize the entire table away -- so a Prometheus outage would empty the
// dashboard, and it would look like the device had stopped reporting rather
// than like a failed query.
//
// A failed read must therefore err toward showing MORE. Same rule as an absent
// counter not reading as zero: an answer that was never obtained is not an
// answer.
//
// The fixture supplies values that WOULD make the column uniform, so the test
// distinguishes "the read failed" from "there was nothing to read". Without the
// failure this column is summarized away; the sibling test above is what pins
// that, and the two together are what make this one non-vacuous.
func TestAFailedColumnReadShowsMoreColumnsNotFewer(t *testing.T) {
	catalog := []map[string]interface{}{
		{catMetric: "snmp_ifMtu", catModule: "IF-MIB", catObject: "ifMtu",
			catKind: "gauge", catDescription: "MTU", catIndexLabels: "ifindex"},
	}

	dash := tableFixture(t, catalogFixture{
		Retained: catalog, Current: catalog,
		Device: []string{"snmp_ifMtu"},
		Values: map[string][]float64{"snmp_ifMtu": {1500, 1500, 1500}},
		Fail:   []string{`{__name__=~"snmp_ifMtu",device="dist-01"}`},
	}, "IF-MIB")

	column := columnFor(t, dash, "snmp_ifMtu")
	if !column.Varies {
		t.Error("the column-values read failed and the column was marked uniform " +
			"anyway: an answer that was never obtained is not an answer, and " +
			"treating the failure as 'all rows agree' empties the table during an " +
			"outage while looking like the device stopped reporting")
	}
	if shown := defaultColumns(t, dash); !slices.Contains(shown, "snmp_ifMtu") {
		t.Errorf("default columns = %v, want the column shown: a failed read errs "+
			"toward more columns, not fewer", shown)
	}
}

// TestAColumnWithAnUnreadableRowIsNotSummarizedAsUniform is the PARTIAL answer,
// which is a different failure from the total one above.
//
// TestAFailedColumnReadShowsMoreColumnsNotFewer pins the query FAILING. This
// pins the query SUCCEEDING and coming back quietly incomplete: Prometheus
// returns three rows, the decoder drops the one carrying NaN, and what reaches
// the rule is two rows with one distinct value. The column is then marked
// uniform, summarized as "1500", and the row whose data is unavailable is gone
// from the table -- with the summary line stating as fact that every row agrees.
//
// A fixture that can only fail the whole read cannot express this, so re-pinning
// total failure with a different shape would have looked like coverage and left
// the gap exactly where it was.
//
// NaN is not exotic: it is how Prometheus encodes an undefined result, and a
// division or a counter reset produces one on this fleet today.
func TestAColumnWithAnUnreadableRowIsNotSummarizedAsUniform(t *testing.T) {
	catalog := []map[string]interface{}{
		{catMetric: "snmp_ifMtu", catModule: "IF-MIB", catObject: "ifMtu",
			catKind: "gauge", catDescription: "MTU", catIndexLabels: "ifindex"},
	}
	dash := tableFixture(t, catalogFixture{
		Retained: catalog, Current: catalog,
		Device: []string{"snmp_ifMtu"},
		// Two rows that agree, and a third that cannot be read. Without the
		// third this column is summarized away -- which the sibling test pins,
		// and which is what makes this one non-vacuous.
		Values:     map[string][]float64{"snmp_ifMtu": {1500, 1500}},
		Unreadable: map[string]int{"snmp_ifMtu": 1},
	}, "IF-MIB")

	column := columnFor(t, dash, "snmp_ifMtu")
	if !column.Varies {
		t.Errorf("the column is marked uniform with value %q although one of its "+
			"rows could not be read: two rows agreeing is not three rows agreeing, "+
			"and summarizing it states as fact something the read never observed",
			column.Value)
	}
	if column.Value != "" {
		t.Errorf("the column carries summary value %q; there is no value every row "+
			"shares, because one row has no value at all", column.Value)
	}
	if shown := defaultColumns(t, dash); !slices.Contains(shown, "snmp_ifMtu") {
		t.Errorf("default columns = %v, want the column shown: the row that could "+
			"not be read is the one most worth looking at", shown)
	}
}

// TestTheEndpointTheBrowserRendersFromCarriesTables is the test whose absence
// let this feature ship inert.
//
// Every other table test here asks /api/mibs/{id}/dashboard, because that is
// where the tables were built. **The browser never calls it.** app.js fetches
// /api/devices/{id}/dashboard once and splits the panels per module itself, so
// tables were being built on an endpoint nothing draws from.
//
// Measured on dist-01 before the fix:
//
//	/api/devices/5/dashboard  ->  220 panels, no "tables" key
//	/api/mibs/56/dashboard    ->   11 panels, 2 tables
//
// The server emitted an arrangement, the browser received a payload without
// it, and nothing errored. The Playwright specs passed 9 of 9 against a mock
// that supplied tables on the device endpoint -- a shape the server did not
// produce. A mock is a claim about the server, and nothing compared them.
//
// So this test asks the endpoint the product asks, which is the only version
// that can fail the way production failed.
func TestTheEndpointTheBrowserRendersFromCarriesTables(t *testing.T) {
	catalog := []map[string]interface{}{
		{catMetric: "snmp_ifMtu", catModule: "IF-MIB", catObject: "ifMtu",
			catKind: "gauge", catDescription: "MTU", catIndexLabels: "ifindex"},
		{catMetric: "snmp_ifSpeed", catModule: "IF-MIB", catObject: "ifSpeed",
			catKind: "gauge", catDescription: "speed", catIndexLabels: "ifindex"},
	}
	s := newTestServer(t)
	s.promClient = catalogClient(t, catalogFixture{
		Retained: catalog, Current: catalog,
		Device: []string{"snmp_ifMtu", "snmp_ifSpeed"},
		Values: map[string][]float64{
			"snmp_ifMtu":   {1500, 9000, 1500},
			"snmp_ifSpeed": {1000, 1000, 1000},
		},
	})

	ctx := context.Background()
	device := storeDevice(t, s, "dist-01", "192.0.2.14")
	mib := &webuiStorage.MIB{Name: "IF-MIB", FilePath: "/mibs/IF-MIB.mib"}
	if err := s.storage.CreateMIB(ctx, mib); err != nil {
		t.Fatalf("CreateMIB: %v", err)
	}
	if err := s.storage.AssignMIBToDevice(ctx, device, mib.ID); err != nil {
		t.Fatalf("AssignMIBToDevice: %v", err)
	}

	// The URL app.js uses. Not the MIB-scoped one.
	rec := do(t, s, http.MethodGet, "/api/devices/"+itoa(device)+"/dashboard", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}

	// Decoded as the browser sees it -- a raw map, so an absent key is
	// distinguishable from an empty array. Decoding into Dashboard would give
	// a nil slice for both, and "the field is missing" is exactly the failure.
	var payload map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	raw, hasTables := payload["tables"]
	if !hasTables {
		present := make([]string, 0, len(payload))
		for k := range payload {
			present = append(present, k)
		}
		sort.Strings(present)
		t.Fatalf("the response has no \"tables\" key at all; the browser renders "+
			"from this endpoint and would draw no table. keys = %v", present)
	}
	tables, _ := raw.([]interface{})
	if len(tables) == 0 {
		t.Fatal("the response carries an empty tables array; the indexed metrics " +
			"of this device share an index and should group into one")
	}
}
