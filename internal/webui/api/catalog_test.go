package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/promname"
	webuistorage "github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// catalogFixture is what each of the catalog's four reads returns.
//
// Two families, and two reads of each: the wide retained window, and the plain
// selector that answers only for rows still being written. Splitting them is
// the point -- a superseded row is one that appears in the retained read and
// not in the current one, and a fixture that could not express that could not
// test the rule that decides between them.
type catalogFixture struct {
	// Retained and RetainedEnums are the wide window: current rows plus
	// anything superseded within the lookback.
	Retained      []map[string]interface{}
	RetainedEnums []map[string]interface{}

	// Current and CurrentEnums are what the emitter is writing now.
	Current      []map[string]interface{}
	CurrentEnums []map[string]interface{}

	// Device is what the device reports, for the "which panels?" question.
	Device []string

	// Values is what each metric currently reads, one entry per ROW, for the
	// "which columns differentiate anything?" question. A metric absent from
	// here reports no rows, which is what a device that does not carry it looks
	// like -- so a test that cares about column defaults has to say so, and one
	// that does not is unaffected.
	Values map[string][]float64

	// Unreadable is how many EXTRA rows a metric returns that carry no usable
	// value -- Prometheus's "NaN", which is how it encodes an undefined result.
	// The decoder drops those, so a fixture that cannot emit them cannot state
	// the case where a column's rows are partly unreadable.
	Unreadable map[string]int

	// Labels is what an INFO metric's rows read. An info metric's sample is
	// always 1 and its words are in the "value" label, so a fixture that can
	// only set samples cannot express two rows that differ -- which is exactly
	// the case a column rule reading samples gets wrong.
	Labels map[string][]string

	// Fail names the queries to answer with a server error, or holds "*" for
	// all of them. Each read degrades on its own, and there is no other way to
	// exercise one failing while the rest answer.
	Fail []string
}

// serveCatalog answers the catalog reads from a fixture.
//
// It dispatches on the query text rather than on call order, because asserting
// on order would pass just as happily if two reads were swapped.
func serveCatalog(t *testing.T, f catalogFixture) *Server {
	t.Helper()

	return &Server{promClient: catalogClient(t, f)}
}

// catalogClient is the same fixture as a Prometheus client, for tests that need
// it on a server with real storage behind it.
func catalogClient(t *testing.T, f catalogFixture) *PrometheusClient {
	t.Helper()

	write := func(w http.ResponseWriter, rows []map[string]interface{}) {
		result := make([]map[string]interface{}, 0, len(rows))
		for _, r := range rows {
			result = append(result, map[string]interface{}{
				"metric": r,
				// Every row carries the query's evaluation time, which is what
				// Prometheus actually returns for these reads and why nothing
				// here can be decided by comparing timestamps.
				"value": []interface{}{1756607200.0, "1"},
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "success",
			"data":   map[string]interface{}{"resultType": "vector", "result": result},
		})
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		if q == "" {
			_ = r.ParseForm()
			q = r.Form.Get("query")
		}
		retained := strings.Contains(q, "last_over_time")
		w.Header().Set("Content-Type", "application/json")

		for _, failing := range f.Fail {
			if failing == "*" || failing == q {
				http.Error(w, "prometheus is unwell", http.StatusServiceUnavailable)
				return
			}
		}

		switch {
		case strings.Contains(q, catalogEnumMetric) && retained:
			write(w, f.RetainedEnums)
		case strings.Contains(q, catalogEnumMetric):
			write(w, f.CurrentEnums)
		case strings.Contains(q, catalogMetric) && retained:
			write(w, f.Retained)
		case strings.Contains(q, catalogMetric):
			write(w, f.Current)
		case strings.Contains(q, "count by ("):
			// Prometheus answers the selector it was given, so the stub has to
			// as well. A stub that returned every name whatever was asked
			// would hide a caller that forgot to constrain the question --
			// which is exactly how the poller's own instrumentation came to be
			// drawn as SNMP panels on the live fleet.
			prefixOnly := strings.Contains(q, promname.NameLabel+`=~"`+promname.Prefix)
			rows := make([]map[string]interface{}, 0, len(f.Device))
			for _, m := range f.Device {
				if prefixOnly && !strings.HasPrefix(m, promname.Prefix) {
					continue
				}
				rows = append(rows, map[string]interface{}{"__name__": m})
			}
			write(w, rows)
		case strings.Contains(q, promname.NameLabel+`=~"`):
			// The column-value read: one query carrying every column of a
			// table as a __name__ alternation, answered with one series per
			// row per metric. Values, not names, so this cannot reuse write().
			result := []map[string]interface{}{}
			for _, metric := range f.Device {
				if !strings.Contains(q, metric) {
					continue
				}
				for row, value := range f.Values[metric] {
					result = append(result, map[string]interface{}{
						"metric": map[string]interface{}{
							"__name__": metric,
							"ifindex":  strconv.Itoa(row + 1),
						},
						"value": []interface{}{1756607200.0, strconv.FormatFloat(value, 'g', -1, 64)},
					})
				}
				// Rows Prometheus returns with no usable value. Stamped after
				// the readable ones so their row indices do not collide.
				for n := 0; n < f.Unreadable[metric]; n++ {
					result = append(result, map[string]interface{}{
						"metric": map[string]interface{}{
							"__name__": metric,
							"ifindex":  strconv.Itoa(len(f.Values[metric]) + n + 1),
						},
						"value": []interface{}{1756607200.0, "NaN"},
					})
				}
				// Info rows: the sample is 1 on every one of them, as
				// Prometheus publishes them, and the reading is in the label.
				for row, text := range f.Labels[metric] {
					result = append(result, map[string]interface{}{
						"metric": map[string]interface{}{
							"__name__": metric,
							"ifindex":  strconv.Itoa(row + 1),
							"value":    text,
						},
						"value": []interface{}{1756607200.0, "1"},
					})
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"status": "success",
				"data":   map[string]interface{}{"resultType": "vector", "result": result},
			})
		default:
			write(w, nil)
		}
	}))
	t.Cleanup(srv.Close)

	return NewPrometheusClient(srv.URL)
}

// namedDevice is a device record for the generator, which needs a poll interval
// as well as a name to size the rate() window.
func namedDevice(name string) *webuistorage.Device {
	return &webuistorage.Device{Name: name}
}

// catalogStub is the healthy emitter: everything it has published is also
// what it is publishing now, so both reads return the same rows.
func catalogStub(t *testing.T, catalog, enums []map[string]interface{}, deviceMetrics ...string) *Server {
	t.Helper()

	return serveCatalog(t, catalogFixture{
		Retained: catalog, Current: catalog,
		RetainedEnums: enums, CurrentEnums: enums,
		Device: deviceMetrics,
	})
}

// TestCatalogReachesTheUnitNamedFamilies is the case the whole design exists
// for, expressed from the UI's side.
//
// Optical power is published under a name derived from its UNIT, not its MIB
// object: there is no MIB object called entity_sensor_dbm. A dashboard that
// enumerated MIB objects could never reach it. Reading the catalog does,
// because the catalog is keyed on the name the poller actually published.
func TestCatalogReachesTheUnitNamedFamilies(t *testing.T) {
	s := catalogStub(t, []map[string]interface{}{
		{catMetric: "snmp_entity_sensor_dbm", catModule: "CISCO-ENTITY-SENSOR-MIB",
			catUnit: "dBm", catKind: "gauge", catIdentity: "entphysicalname"},
	}, nil)

	cat := s.metricCatalog(context.Background()).entries

	entry, ok := cat["snmp_entity_sensor_dbm"]
	if !ok {
		t.Fatal("the catalog has no row for the optical reading")
	}
	if entry.Unit != "dBm" {
		t.Errorf("unit = %q, want dBm", entry.Unit)
	}
	if entry.Identity != "entphysicalname" {
		t.Errorf("identity = %q, want the name to be on the series itself", entry.Identity)
	}
	if entry.Object != "" {
		t.Errorf("object = %q, want empty: this family is named for its unit, "+
			"and claiming a MIB object would invite an object-keyed lookup back in",
			entry.Object)
	}
}

// TestTheRowStillBeingWrittenWinsAfterAReload is the consumer half of the read
// contract, and the version of it that can actually fail.
//
// The lookback is deliberately wide enough to survive a missed emission, which
// means a row superseded by a MIB reload stays queryable for the rest of the
// window. Both rows are real; only one is still being written.
//
// The earlier test for this asserted a rule the code could not apply. It gave
// the two rows different sample timestamps and checked that the newer won --
// but a last_over_time read stamps every row with the query's evaluation time,
// so in production both timestamps were identical and arrival order decided.
// The fixture manufactured the only conditions under which the code worked.
func TestTheRowStillBeingWrittenWinsAfterAReload(t *testing.T) {
	// The superseded row sorts FIRST, so a read that resolved the tie by
	// ordering alone -- rather than by which row is still being written --
	// answers with it and fails.
	old := map[string]interface{}{catMetric: "snmp_ifInOctets",
		catUnit: "bytes", catDescription: "counted in bytes"}
	current := map[string]interface{}{catMetric: "snmp_ifInOctets",
		catUnit: "octets", catDescription: "octets received"}

	for _, order := range []struct {
		name     string
		retained []map[string]interface{}
	}{
		// Reversed in the second case, because a read that simply takes the
		// last row it sees passes the first by luck.
		{"superseded row first", []map[string]interface{}{old, current}},
		{"superseded row last", []map[string]interface{}{current, old}},
	} {
		t.Run(order.name, func(t *testing.T) {
			s := serveCatalog(t, catalogFixture{
				Retained: order.retained,
				// Only one of them is still being written.
				Current: []map[string]interface{}{current},
			})

			entry := s.metricCatalog(context.Background()).entries["snmp_ifInOctets"]
			if entry.Description != "octets received" || entry.Unit != "octets" {
				t.Errorf("got %q/%q, want the row still being written -- a superseded "+
					"row lingers for the whole lookback window",
					entry.Description, entry.Unit)
			}
		})
	}
}

// TestTheWideWindowStillCarriesTheCatalogWhenNothingIsBeingWritten is the
// other half, and the reason the retained read exists at all.
//
// When the emitter stops -- a restart, or remote writes being refused -- the
// current read goes empty. Falling back to it would blank every description on
// every panel across the fleet at once, while the metrics themselves kept
// arriving and looked perfectly healthy.
func TestTheWideWindowStillCarriesTheCatalogWhenNothingIsBeingWritten(t *testing.T) {
	s := serveCatalog(t, catalogFixture{
		Retained: []map[string]interface{}{
			{catMetric: "snmp_ifInOctets", catUnit: "bytes", catKind: "counter"},
		},
		RetainedEnums: []map[string]interface{}{
			{catMetric: "snmp_ifInOctets", catEnumValue: "1", catEnumLabel: "up"},
		},
		// The emitter is publishing nothing at this instant.
	})

	entry, ok := s.metricCatalog(context.Background()).entries["snmp_ifInOctets"]
	if !ok {
		t.Fatal("a stalled emitter emptied the catalog; every panel loses its labels at once")
	}
	if entry.Unit != "bytes" {
		t.Errorf("unit = %q, want bytes from the retained window", entry.Unit)
	}
	if entry.Enum["1"] != "up" {
		t.Errorf("enum = %v, want the retained decode: a state panel falls back to "+
			"raw numbers without it", entry.Enum)
	}
}

// TestEnumerationsAreFoldedIntoTheirMetric checks the join, so a renderer can
// show "up" instead of 1 without knowing what ifOperStatus is.
func TestEnumerationsAreFoldedIntoTheirMetric(t *testing.T) {
	s := catalogStub(t,
		[]map[string]interface{}{{catMetric: "snmp_ifOperStatus", catKind: "enum"}},
		[]map[string]interface{}{
			{catMetric: "snmp_ifOperStatus", catEnumValue: "1", catEnumLabel: "up"},
			{catMetric: "snmp_ifOperStatus", catEnumValue: "2", catEnumLabel: "down"},
			// An enum row for a metric with no catalog entry must not create one.
			{catMetric: "snmp_ghost", catEnumValue: "1", catEnumLabel: "nowhere"},
		})

	cat := s.metricCatalog(context.Background()).entries

	entry := cat["snmp_ifOperStatus"]
	if entry.Enum["1"] != "up" || entry.Enum["2"] != "down" {
		t.Errorf("enum = %v, want 1=up 2=down", entry.Enum)
	}
	if _, ok := cat["snmp_ghost"]; ok {
		t.Error("an enumeration row invented a catalog entry for a metric with no row")
	}
}

// TestChartTypeComesFromDeclaredSemantics is what lets an unseen MIB render
// correctly: the widget follows the declared kind, never a name match.
func TestChartTypeComesFromDeclaredSemantics(t *testing.T) {
	for _, tc := range []struct {
		kind, want string
	}{
		{"counter", "rate"},
		{"gauge", "line"},
		{"enum", "state"},
		{"string", "text"},
		// An unresolved type still draws. Refusing to render would hide a
		// metric that has perfectly good data behind a MIB import failure.
		{"unknown", "line"},
		{"", "line"},
	} {
		if got := (CatalogEntry{Kind: tc.kind}).ChartType(); got != tc.want {
			t.Errorf("kind %q -> %q, want %q", tc.kind, got, tc.want)
		}
	}
}

// TestAnAbsentCatalogDegradesRatherThanFails covers the non-designated
// collector: it publishes no catalog, and the dashboard must still render
// unlabelled panels rather than refuse.
func TestAnAbsentCatalogDegradesRatherThanFails(t *testing.T) {
	s := catalogStub(t, nil, nil)

	cat := s.metricCatalog(context.Background()).entries
	if cat == nil {
		t.Fatal("an empty catalog came back nil; the caller ranges over it")
	}
	if len(cat) != 0 {
		t.Errorf("catalog = %v, want empty", cat)
	}
}

// TestAMIBFilterDoesNotDropTheDerivedFamilies is the regression test for a bug
// this design was specifically built to avoid, which I reintroduced one layer
// up anyway.
//
// The catalog is keyed on the PUBLISHED name so that families named for their
// unit are reachable. But those families have no `module` label -- there is no
// MIB object behind them -- so filtering panels by the device's assigned MIBs
// dropped every one of them. Found on monitor-01: 40 snmp_entity_sensor_dbm series
// present, zero of them on the dashboard, and the coverage table reporting
// CISCO-ENTITY-SENSOR-MIB as fully covered while the actual readings were
// missing. Exactly the invisible failure the whole change exists to remove.
//
// A metric with no module is not attributable to a MIB, so it belongs to the
// DEVICE: it appears whenever the question is about the device, and only there.
func TestAMIBFilterDoesNotDropTheDerivedFamilies(t *testing.T) {
	s := catalogStub(t,
		[]map[string]interface{}{
			{catMetric: "snmp_ifInOctets", catModule: "IF-MIB", catKind: "counter"},
			{catMetric: "snmp_entPhysicalClass", catModule: "ENTITY-MIB", catKind: "enum"},
			// No module: the poller derives this one, so no MIB object backs it.
			{catMetric: "snmp_entity_sensor_dbm", catUnit: "dBm", catKind: "gauge"},
		},
		nil,
		"snmp_ifInOctets", "snmp_entPhysicalClass", "snmp_entity_sensor_dbm",
		"snmp_1_3_6_1_4_1_9999")

	// The device's own view: every module it has.
	dash := s.generateDashboard(context.Background(), namedDevice("dist-01"),
		[]string{"IF-MIB", "ENTITY-MIB"}, "", scopeDevice)

	drawn := map[string]bool{}
	for _, p := range dash.Panels {
		drawn[p.Metric] = true
	}

	if !drawn["snmp_entity_sensor_dbm"] {
		t.Error("the optical reading was filtered out by a MIB selection; it has " +
			"no module, so no MIB filter can exclude it")
	}
	if !drawn["snmp_1_3_6_1_4_1_9999"] {
		t.Error("a raw-OID metric was filtered out; it has data and nothing describes it")
	}
	if !drawn["snmp_ifInOctets"] || !drawn["snmp_entPhysicalClass"] {
		t.Error("a selected module's metric was dropped")
	}
}

// TestOneModuleIsAskedAboutOneModule is the other half of the same rule.
//
// The browser renders one tab per module and asks once per tab. Carrying the
// unattributable families into every one of those answers put snmp_system_info
// and every optical reading on all ten of core-01's tabs, each with its own
// copy of the same "(uncataloged)" warning about the same metric.
func TestOneModuleIsAskedAboutOneModule(t *testing.T) {
	s := catalogStub(t,
		[]map[string]interface{}{
			{catMetric: "snmp_ifInOctets", catModule: "IF-MIB", catKind: "counter",
				catDescription: "octets in"},
			{catMetric: "snmp_entity_sensor_dbm", catUnit: "dBm", catKind: "gauge",
				catDescription: "optical power"},
		},
		nil,
		"snmp_ifInOctets", "snmp_entity_sensor_dbm")

	dash := s.generateDashboard(context.Background(), namedDevice("dist-01"), []string{"IF-MIB"}, "", scopeModule)

	for _, p := range dash.Panels {
		if p.Metric == "snmp_entity_sensor_dbm" {
			t.Error("a metric no MIB claims was drawn on a single module's tab, " +
				"where it will appear again on every other tab too")
		}
	}
	if len(dash.Warnings) != 0 {
		t.Errorf("warnings = %v, want none: the uncataloged bucket belongs to the "+
			"device's view, not to one module's tab", dash.Warnings)
	}
}

// TestTheEnumLabelStillBeingWrittenWinsAfterAReload is the enumeration half of
// the read contract, and it was missing entirely.
//
// A MIB reload that renames value 1 from "up" to "active" leaves both rows in
// the lookback window: the label is part of the series identity, so they are
// two series and only one is still being written. Folding them in arrival
// order made a state panel read "up" on a device whose current MIB calls it
// "active", with nothing on screen to say which.
func TestTheEnumLabelStillBeingWrittenWinsAfterAReload(t *testing.T) {
	// The superseded label sorts first, so ordering alone answers with it.
	renamed := map[string]interface{}{catMetric: "snmp_ifOperStatus",
		catEnumValue: "1", catEnumLabel: "up"}
	superseded := map[string]interface{}{catMetric: "snmp_ifOperStatus",
		catEnumValue: "1", catEnumLabel: "active"}

	for _, order := range []struct {
		name     string
		retained []map[string]interface{}
	}{
		{"superseded label first", []map[string]interface{}{superseded, renamed}},
		{"superseded label last", []map[string]interface{}{renamed, superseded}},
	} {
		t.Run(order.name, func(t *testing.T) {
			catalog := []map[string]interface{}{
				{catMetric: "snmp_ifOperStatus", catKind: "enum"},
			}
			s := serveCatalog(t, catalogFixture{
				Retained: catalog, Current: catalog,
				RetainedEnums: order.retained,
				CurrentEnums:  []map[string]interface{}{renamed},
			})

			if got := s.metricCatalog(context.Background()).entries["snmp_ifOperStatus"].Enum["1"]; got != "up" {
				t.Errorf("enum 1 = %q, want up: a superseded label lingers for "+
					"the whole lookback window and must not win", got)
			}
		})
	}
}

// TestASupersededLabelDoesNotEvictItsNeighbours keeps the rule scoped to the
// value it describes.
//
// The current read only carries the value the reload touched. Laying it over
// the retained decode wholesale -- rather than per value -- would hide the
// untouched labels for 2 and 3, blanking most of an enumeration to correct one
// of it.
func TestASupersededLabelDoesNotEvictItsNeighbours(t *testing.T) {
	catalog := []map[string]interface{}{{catMetric: "snmp_ifOperStatus", catKind: "enum"}}
	s := serveCatalog(t, catalogFixture{
		Retained: catalog, Current: catalog,
		RetainedEnums: []map[string]interface{}{
			{catMetric: "snmp_ifOperStatus", catEnumValue: "1", catEnumLabel: "up"},
			{catMetric: "snmp_ifOperStatus", catEnumValue: "2", catEnumLabel: "down"},
			{catMetric: "snmp_ifOperStatus", catEnumValue: "3", catEnumLabel: "testing"},
		},
		CurrentEnums: []map[string]interface{}{
			{catMetric: "snmp_ifOperStatus", catEnumValue: "1", catEnumLabel: "active"},
		},
	})

	enum := s.metricCatalog(context.Background()).entries["snmp_ifOperStatus"].Enum
	if len(enum) != 3 || enum["1"] != "active" || enum["2"] != "down" || enum["3"] != "testing" {
		t.Errorf("enum = %v, want 1=active alongside the untouched 2 and 3", enum)
	}
}

// TestATextPanelIsToldWhereItsReadingIs closes the gap that made text widgets
// useless.
//
// An info metric's sample is the constant 1 -- the reading is in the "value"
// label. A widget carrying only the metric name had literally nothing to show,
// and the contract exposed identity and index labels but never this one.
func TestATextPanelIsToldWhereItsReadingIs(t *testing.T) {
	s := catalogStub(t,
		[]map[string]interface{}{
			{catMetric: "snmp_ifname_info", catModule: "IF-MIB", catObject: "ifName",
				catKind: "string"},
			{catMetric: "snmp_ifInOctets", catModule: "IF-MIB", catKind: "counter"},
		},
		nil,
		"snmp_ifname_info", "snmp_ifInOctets")

	byMetric := map[string]Panel{}
	for _, p := range s.generateDashboard(context.Background(), namedDevice("dist-01"), []string{"IF-MIB"}, "", scopeDevice).Panels {
		byMetric[p.Metric] = p
	}

	text := byMetric["snmp_ifname_info"]
	if text.Type != panelText {
		t.Fatalf("type = %q, want text", text.Type)
	}
	if text.ValueLabel != "value" {
		t.Errorf("value_label = %q, want \"value\": without it the widget knows "+
			"the metric name and nothing it could display", text.ValueLabel)
	}

	// And only there. A counter's reading IS its sample, so pointing a
	// renderer at a label that does not exist would draw nothing.
	if byMetric["snmp_ifInOctets"].ValueLabel != "" {
		t.Error("a chartable metric was given a value label; its value is the sample")
	}
}

// TestTheOverlapAfterAReloadIsStableRatherThanArbitrary is the case the two
// reads do NOT resolve, pinned so nobody mistakes it for one that they do.
//
// Prometheus answers a plain selector for any series with a sample inside its
// lookback delta -- five minutes by default -- and remote write carries no
// stale markers, so for those few minutes after a reload the superseded row is
// still current too. Both rows come back from the current read and the data
// says nothing about which is which.
//
// What is guaranteed is only that the answer does not depend on the order
// Prometheus returned them in. Without that, one load of a dashboard called
// snmp_chassisType a Cisco chassis and the next called it a Dell one.
func TestTheOverlapAfterAReloadIsStableRatherThanArbitrary(t *testing.T) {
	cisco := map[string]interface{}{catMetric: "snmp_chassisType", catKind: "enum",
		catModule: "OLD-CISCO-CHASSIS-MIB", catObject: "chassisType"}
	idrac := map[string]interface{}{catMetric: "snmp_chassisType", catKind: "enum",
		catModule: "IDRAC-MIB-SMIv2", catObject: "chassisType"}

	answers := map[string]bool{}
	for _, rows := range [][]map[string]interface{}{{cisco, idrac}, {idrac, cisco}} {
		s := serveCatalog(t, catalogFixture{Retained: rows, Current: rows})
		answers[s.metricCatalog(context.Background()).entries["snmp_chassisType"].Module] = true
	}

	if len(answers) != 1 {
		t.Errorf("the same overlap answered %v on different orderings; an operator "+
			"reloading the page would see the description change under them", answers)
	}
}

// TestOverlapStabilityCoversEveryLabelAndNotJustProvenance is the same
// property for the rows that are hardest to tell apart.
//
// Every field on a catalog row is a label, so ANY of them changing is what
// minted the second series. A reload that corrects a unit and nothing else
// produces two rows identical in module, object and description -- and an
// ordering built from those three alone calls them equal and falls back to
// arrival order, which is the bug it exists to remove.
func TestOverlapStabilityCoversEveryLabelAndNotJustProvenance(t *testing.T) {
	octets := map[string]interface{}{catMetric: "snmp_ifInOctets",
		catModule: "IF-MIB", catObject: "ifInOctets", catKind: "counter",
		catDescription: "octets received", catUnit: "octets"}
	bytes := map[string]interface{}{catMetric: "snmp_ifInOctets",
		catModule: "IF-MIB", catObject: "ifInOctets", catKind: "counter",
		catDescription: "octets received", catUnit: "bytes"}

	answers := map[string]bool{}
	for _, rows := range [][]map[string]interface{}{{octets, bytes}, {bytes, octets}} {
		s := serveCatalog(t, catalogFixture{Retained: rows, Current: rows})
		answers[s.metricCatalog(context.Background()).entries["snmp_ifInOctets"].Unit] = true
	}

	if len(answers) != 1 {
		t.Errorf("two rows differing only in unit answered %v on different "+
			"orderings; the y axis would change label between page loads", answers)
	}
}

// TestAnEnumLabelInOverlapIsStableRatherThanArbitrary is the enumeration half
// of the overlap the two reads cannot resolve.
//
// For the few minutes after a reload, both the old and the renamed label for
// one value answer the current read. A state panel showing whichever arrived
// first would say "up" on one page load and "active" on the next, for the same
// interface in the same state -- which reads as the interface having changed.
func TestAnEnumLabelInOverlapIsStableRatherThanArbitrary(t *testing.T) {
	catalog := []map[string]interface{}{{catMetric: "snmp_ifOperStatus", catKind: "enum"}}
	both := []map[string]interface{}{
		{catMetric: "snmp_ifOperStatus", catEnumValue: "1", catEnumLabel: "up"},
		{catMetric: "snmp_ifOperStatus", catEnumValue: "1", catEnumLabel: "active"},
	}

	answers := map[string]bool{}
	for _, rows := range [][]map[string]interface{}{both, {both[1], both[0]}} {
		s := serveCatalog(t, catalogFixture{
			Retained: catalog, Current: catalog,
			RetainedEnums: rows, CurrentEnums: rows,
		})
		answers[s.metricCatalog(context.Background()).entries["snmp_ifOperStatus"].Enum["1"]] = true
	}

	if len(answers) != 1 {
		t.Errorf("the same overlap decoded state 1 as %v on different orderings; "+
			"a panel would report the interface changing when nothing did", answers)
	}
}

// TestThePanelsSurviveEachReadFailingOnItsOwn covers the degradation paths.
//
// Four reads, four independent ways for Prometheus to be unwell, and the rule
// is the same for all of them: a dashboard renders with less on it, never not
// at all. The catalog is decoration on panels whose data arrives by a
// different path entirely.
func TestThePanelsSurviveEachReadFailingOnItsOwn(t *testing.T) {
	rows := []map[string]interface{}{
		{catMetric: "snmp_ifOperStatus", catKind: "enum", catUnit: "state",
			catDescription: "operational state"},
	}
	enums := []map[string]interface{}{
		{catMetric: "snmp_ifOperStatus", catEnumValue: "1", catEnumLabel: "up"},
	}

	tests := []struct {
		name        string
		fail        string
		wantEntry   bool
		wantDecoded bool
	}{
		{"the wide catalog window fails", retainedCatalogQuery(catalogMetric), true, true},
		{"the current catalog read fails", currentCatalogQuery(catalogMetric), true, true},
		{"the wide enum window fails", retainedCatalogQuery(catalogEnumMetric), true, true},
		{"the current enum read fails", currentCatalogQuery(catalogEnumMetric), true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := serveCatalog(t, catalogFixture{
				Retained: rows, Current: rows,
				RetainedEnums: enums, CurrentEnums: enums,
				Fail: []string{tt.fail},
			})

			entry, ok := s.metricCatalog(context.Background()).entries["snmp_ifOperStatus"]
			if ok != tt.wantEntry {
				t.Fatalf("entry present = %v, want %v: one failed read must not "+
					"empty the catalog", ok, tt.wantEntry)
			}
			if entry.Unit != "state" {
				t.Errorf("unit = %q, want state from the read that did answer", entry.Unit)
			}
			if decoded := entry.Enum["1"] == "up"; decoded != tt.wantDecoded {
				t.Errorf("enum decoded = %v, want %v (enum = %v)",
					decoded, tt.wantDecoded, entry.Enum)
			}
		})
	}
}

// TestEverythingFailingIsAnEmptyCatalogAndNotAPanic is the floor.
func TestEverythingFailingIsAnEmptyCatalogAndNotAPanic(t *testing.T) {
	s := serveCatalog(t, catalogFixture{Fail: []string{"*"}})

	cat := s.metricCatalog(context.Background()).entries
	if cat == nil {
		t.Fatal("a total failure came back nil; the caller ranges over it")
	}
	if len(cat) != 0 {
		t.Errorf("catalog = %v, want empty", cat)
	}
}

// TestAnUnreadableCatalogSaysSoRatherThanLookingEmpty is the state that made a
// dashboard lie about itself.
//
// An empty catalog and an unreadable one produce the same map and mean opposite
// things. When Prometheus refused the read, every metric on the device came
// back undescribed, therefore attributed to no module, therefore counted in the
// bucket whose note announces MIBs that have stopped resolving. Observed on
// monitor-01 in the seconds after a restart: one device, 363 unlabeled panels, and a
// warning naming a fault that did not exist.
func TestAnUnreadableCatalogSaysSoRatherThanLookingEmpty(t *testing.T) {
	// Only the catalog reads fail. The device still answers what it is
	// reporting, which is what makes the panels real and their labels missing.
	unreadable := serveCatalog(t, catalogFixture{
		Device: []string{"snmp_ifInOctets"},
		Fail: []string{
			retainedCatalogQuery(catalogMetric),
			currentCatalogQuery(catalogMetric),
		},
	})
	dash := unreadable.generateDashboard(context.Background(), namedDevice("dist-01"), nil, "", scopeDevice)

	if !dash.CatalogUnavailable {
		t.Error("a failed read was reported as a dashboard with nothing to say")
	}
	if len(dash.Warnings) == 0 || !strings.Contains(dash.Warnings[0], "could not be read") {
		t.Errorf("warnings = %v, want the read failure explained first", dash.Warnings)
	}
	// The panels are still there: their data comes from a different query.
	if len(dash.Panels) != 1 {
		t.Errorf("panels = %d, want 1: the reading is real even when nothing "+
			"describes it", len(dash.Panels))
	}
}

// TestAnEmptyCatalogIsNotReportedAsAFailure is the other half.
//
// A collector that is not the designated emitter publishes no catalog at all.
// That is normal, permanent, and nothing to warn about -- treating it as a read
// failure would put a banner on every dashboard of every such deployment.
func TestAnEmptyCatalogIsNotReportedAsAFailure(t *testing.T) {
	empty := catalogStub(t, nil, nil, "snmp_ifInOctets")
	dash := empty.generateDashboard(context.Background(), namedDevice("dist-01"), nil, "", scopeDevice)

	if dash.CatalogUnavailable {
		t.Error("an emitter that publishes no catalog was reported as a read failure")
	}
	for _, w := range dash.Warnings {
		if strings.Contains(w, "could not be read") {
			t.Errorf("an empty catalog produced a read-failure warning: %q", w)
		}
	}
}
