package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	webuiStorage "github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

///api/mibs/{id}/dashboard, the endpoint the UI draws its panels from.

// dashboardFixture builds a server with a device, a MIB assigned to it, and a
// catalog describing what the device reports.
func dashboardFixture(t *testing.T, f catalogFixture, mibName string) (*Server, int64, int64) {
	t.Helper()

	s := newTestServer(t)
	s.promClient = catalogClient(t, f)

	ctx := context.Background()
	device := storeDevice(t, s, "dist-01", "192.0.2.14")

	mib := &webuiStorage.MIB{Name: mibName, FilePath: "/mibs/" + mibName + ".mib"}
	if err := s.storage.CreateMIB(ctx, mib); err != nil {
		t.Fatalf("CreateMIB: %v", err)
	}
	if err := s.storage.AssignMIBToDevice(ctx, device, mib.ID); err != nil {
		t.Fatalf("AssignMIBToDevice: %v", err)
	}
	return s, device, mib.ID
}

// decodeDashboard reads the payload the browser receives.
func decodeDashboard(t *testing.T, body string) Dashboard {
	t.Helper()

	var dash Dashboard
	if err := json.Unmarshal([]byte(body), &dash); err != nil {
		t.Fatalf("decoding the dashboard: %v (body %q)", err, body)
	}
	return dash
}

// panelFor finds one metric's panel.
func panelFor(t *testing.T, dash Dashboard, metric string) Panel {
	t.Helper()

	for _, p := range dash.Panels {
		if p.Metric == metric {
			return p
		}
	}

	drawn := make([]string, 0, len(dash.Panels))
	for _, p := range dash.Panels {
		drawn = append(drawn, p.Metric)
	}
	t.Fatalf("no panel for %s; the dashboard draws %v", metric, drawn)
	return Panel{}
}

// TestTheRenderedDashboardIsBuiltFromTheCatalog checks the endpoint carries catalog facts.
func TestTheRenderedDashboardIsBuiltFromTheCatalog(t *testing.T) {
	catalog := []map[string]interface{}{
		{catMetric: "snmp_ifInOctets", catModule: "IF-MIB", catObject: "ifInOctets",
			catKind: "counter", catUnit: "octets", catDescription: "octets received",
			catIdentityInfo: "snmp_ifdescr_info", catIndexLabels: "ifindex"},
		{catMetric: "snmp_ifOperStatus", catModule: "IF-MIB", catObject: "ifOperStatus",
			catKind: "enum", catDescription: "operational state",
			catIndexLabels: "ifindex"},
	}
	enums := []map[string]interface{}{
		{catMetric: "snmp_ifOperStatus", catEnumValue: "1", catEnumLabel: "up"},
		{catMetric: "snmp_ifOperStatus", catEnumValue: "2", catEnumLabel: "down"},
	}
	s, _, mibID := dashboardFixture(t, catalogFixture{
		Retained: catalog, Current: catalog,
		RetainedEnums: enums, CurrentEnums: enums,
		Device: []string{"snmp_ifInOctets", "snmp_ifOperStatus"},
	}, "IF-MIB")

	rec := do(t, s, http.MethodGet,
		"/api/mibs/"+itoa(mibID)+"/dashboard?device=dist-01", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}

	dash := decodeDashboard(t, rec.Body.String())
	if dash.MIB != "IF-MIB" {
		t.Errorf("mib = %q, want IF-MIB: the UI renders one tab per module and "+
			"has to know the answer is the tab it asked for", dash.MIB)
	}

	counter := panelFor(t, dash, "snmp_ifInOctets")
	if counter.Type != panelRate {
		t.Errorf("type = %q, want rate: a counter drawn as itself only ever goes up",
			counter.Type)
	}
	if counter.Unit != "octets" {
		t.Errorf("unit = %q, want octets on the axis", counter.Unit)
	}
	if !strings.Contains(counter.Query, "rate(") {
		t.Errorf("query = %q, want it wrapped in rate()", counter.Query)
	}

	state := panelFor(t, dash, "snmp_ifOperStatus")
	if state.Type != panelState {
		t.Errorf("type = %q, want state", state.Type)
	}
	if state.Enum["1"] != "up" || state.Enum["2"] != "down" {
		t.Errorf("enum = %v, want the decode, or a panel shows bare numbers", state.Enum)
	}
}

// TestEachLineIsNamedFromTheLabelsTheCatalogPointsAt checks panels name lines from the label the catalog points at.
func TestEachLineIsNamedFromTheLabelsTheCatalogPointsAt(t *testing.T) {
	catalog := []map[string]interface{}{
		{catMetric: "snmp_ifInOctets", catModule: "IF-MIB", catObject: "ifInOctets",
			catKind: "counter", catIdentityInfo: "snmp_ifdescr_info",
			catIndexLabels: "ifindex", catDescription: "octets received"},
	}
	s, _, mibID := dashboardFixture(t, catalogFixture{
		Retained: catalog, Current: catalog,
		Device: []string{"snmp_ifInOctets"},
	}, "IF-MIB")

	rec := do(t, s, http.MethodGet,
		"/api/mibs/"+itoa(mibID)+"/dashboard?device=dist-01", "")
	panel := panelFor(t, decodeDashboard(t, rec.Body.String()), "snmp_ifInOctets")

	if len(panel.SeriesLabels) != 1 || panel.SeriesLabels[0] != "ifdescr" {
		t.Fatalf("series_labels = %v, want [ifdescr]", panel.SeriesLabels)
	}
	// The index is carried apart from the name, because it is used differently:
	// a name is one label and the first present wins, an index is a tuple and
	// every part of it is needed to tell two rows apart.
	if len(panel.IndexLabels) != 1 || panel.IndexLabels[0] != "ifindex" {
		t.Errorf("index_labels = %v, want [ifindex] as the fallback", panel.IndexLabels)
	}

	// And no join. A group_left whose right-hand side ever has two series for
	// one row fails the whole query, which turns a missing line name into a
	// panel with no data at all.
	if strings.Contains(panel.Query, "group_left") || strings.Contains(panel.Query, "unless") {
		t.Errorf("query = %q, want no vector match: the label is already on the "+
			"reading", panel.Query)
	}
}

// TestAReadingThatCarriesItsOwnNameIsNotJoined covers the other identity case.
//
// An entity sensor reading carries entphysicalname on the series itself. Joining
// anything would be pointless work, and there is no info metric to join to.
func TestAReadingThatCarriesItsOwnNameIsNotJoined(t *testing.T) {
	catalog := []map[string]interface{}{
		{catMetric: "snmp_entity_sensor_dbm", catKind: "gauge", catUnit: "dBm",
			catIdentity: "entphysicalname", catIndexLabels: "entphysicalindex",
			catDescription: "optical power"},
	}
	s, device, _ := dashboardFixture(t, catalogFixture{
		Retained: catalog, Current: catalog,
		Device: []string{"snmp_entity_sensor_dbm"},
	}, "CISCO-ENTITY-SENSOR-MIB")

	// The device's own view. This family has no module, so it is not on any
	// single module's tab -- it belongs to the device.
	rec := do(t, s, http.MethodGet, "/api/devices/"+itoa(device)+"/dashboard", "")
	panel := panelFor(t, decodeDashboard(t, rec.Body.String()), "snmp_entity_sensor_dbm")

	if len(panel.SeriesLabels) != 1 || panel.SeriesLabels[0] != "entphysicalname" {
		t.Errorf("series_labels = %v, want entphysicalname first: it is on the "+
			"series itself", panel.SeriesLabels)
	}
	if panel.Type != panelLine {
		t.Errorf("type = %q, want line for a gauge", panel.Type)
	}
}

// TestTheRateWindowFollowsTheDevicePollInterval checks the rate() window scales with the device's poll interval.
func TestTheRateWindowFollowsTheDevicePollInterval(t *testing.T) {
	tests := []struct {
		name     string
		interval int
		want     string
	}{
		{"the default when a device sets none", 0, "4m"},
		{"a fast device", 30, "4m"},
		{"a slow device", 60, "8m"},
		{"an interval that is not whole minutes", 25, "200s"},
		// Never below a minute, whatever the interval: a window shorter than
		// the delivery spacing is empty however often the device is polled.
		{"a very fast device", 5, "1m"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rateWindow(tt.interval); got != tt.want {
				t.Errorf("rateWindow(%d) = %q, want %q", tt.interval, got, tt.want)
			}
		})
	}
}

// TestTheRateWindowClearsTheMeasuredDeliveryGap checks the window is eight poll intervals.
func TestTheRateWindowClearsTheMeasuredDeliveryGap(t *testing.T) {
	for _, interval := range []int{30, 60, 120} {
		window := rateWindow(interval)

		var seconds int
		switch {
		case strings.HasSuffix(window, "m"):
			minutes, err := strconv.Atoi(strings.TrimSuffix(window, "m"))
			if err != nil {
				t.Fatalf("rateWindow(%d) = %q, which is not a duration", interval, window)
			}
			seconds = minutes * 60
		default:
			var err error
			seconds, err = strconv.Atoi(strings.TrimSuffix(window, "s"))
			if err != nil {
				t.Fatalf("rateWindow(%d) = %q, which is not a duration", interval, window)
			}
		}

		if seconds < interval*6 {
			t.Errorf("rateWindow(%d) = %q = %ds, which is under six poll intervals; "+
				"six is the measured minimum that filled every rate panel on the "+
				"fleet, and four left 36 of core-01's empty", interval, window, seconds)
		}
	}
}

// TestTheMIBDashboardRejectsBadInput covers the error paths; the device is required.
func TestTheMIBDashboardRejectsBadInput(t *testing.T) {
	s, _, mibID := dashboardFixture(t, catalogFixture{}, "IF-MIB")

	tests := []struct {
		name       string
		path       string
		wantStatus int
	}{
		{"a malformed MIB id", "/api/mibs/abc/dashboard?device=dist-01", http.StatusNotFound},
		{"an unknown MIB", "/api/mibs/4242/dashboard?device=dist-01", http.StatusNotFound},
		{"no device at all", "/api/mibs/" + itoa(mibID) + "/dashboard", http.StatusBadRequest},
		{"an unknown device", "/api/mibs/" + itoa(mibID) + "/dashboard?device=nowhere",
			http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, s, http.MethodGet, tt.path, "")
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (body %q)",
					rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}

// TestAModuleTabShowsOnlyItsOwnPanels keeps one tab from rendering another
// module's metrics.
func TestAModuleTabShowsOnlyItsOwnPanels(t *testing.T) {
	catalog := []map[string]interface{}{
		{catMetric: "snmp_ifInOctets", catModule: "IF-MIB", catKind: "counter",
			catDescription: "octets received"},
		{catMetric: "snmp_entPhysicalClass", catModule: "ENTITY-MIB", catKind: "enum",
			catDescription: "class"},
	}
	s, _, mibID := dashboardFixture(t, catalogFixture{
		Retained: catalog, Current: catalog,
		Device: []string{"snmp_ifInOctets", "snmp_entPhysicalClass"},
	}, "IF-MIB")

	rec := do(t, s, http.MethodGet,
		"/api/mibs/"+itoa(mibID)+"/dashboard?device=dist-01", "")
	dash := decodeDashboard(t, rec.Body.String())

	for _, p := range dash.Panels {
		if p.Metric == "snmp_entPhysicalClass" {
			t.Error("the IF-MIB tab drew an ENTITY-MIB panel")
		}
	}
	if len(dash.Panels) != 1 {
		t.Errorf("panels = %d, want 1", len(dash.Panels))
	}
}

// mockMIBLoaderForDashboard is a MIB loader for the browsing and cost endpoints.
type mockMIBLoaderForDashboard struct {
	storage interface{}
}

func (m *mockMIBLoaderForDashboard) LoadModule(string) error          { return nil }
func (m *mockMIBLoaderForDashboard) EnsureModuleCurrent(string) error { return nil }

func (m *mockMIBLoaderForDashboard) GetParser() interface{} { return nil }

func (m *mockMIBLoaderForDashboard) GetStorage() interface{} { return m.storage }

// TestThePollersOwnMetricsAreNotDrawnAsPanels checks gonetmon_* poller metrics are not panels.
func TestThePollersOwnMetricsAreNotDrawnAsPanels(t *testing.T) {
	catalog := []map[string]interface{}{
		{catMetric: "snmp_ifInOctets", catModule: "IF-MIB", catKind: "counter",
			catDescription: "octets received"},
	}
	s, _, mibID := dashboardFixture(t, catalogFixture{
		Retained: catalog, Current: catalog,
		Device: []string{"snmp_ifInOctets", "gonetmon_polls_total",
			"gonetmon_device_unreachable"},
	}, "IF-MIB")

	rec := do(t, s, http.MethodGet,
		"/api/mibs/"+itoa(mibID)+"/dashboard?device=dist-01", "")
	dash := decodeDashboard(t, rec.Body.String())

	for _, p := range dash.Panels {
		if strings.HasPrefix(p.Metric, "gonetmon_") {
			t.Errorf("the poller's own metric %s was drawn as a panel", p.Metric)
		}
	}
	// And it must not be counted as a MIB that stopped resolving, either.
	for _, cov := range dash.MIBs {
		if cov.MIB == unattributedModule {
			t.Errorf("the poller's own metrics were reported as uncataloged SNMP "+
				"readings: %+v", cov)
		}
	}
}

// TestAColumnIsNotNamedAfterItself checks a panel's lines are not named by the column it draws.
func TestAColumnIsNotNamedAfterItself(t *testing.T) {
	catalog := []map[string]interface{}{
		{catMetric: "snmp_cbgpPeer2AdminStatus", catModule: "CISCO-BGP4-MIB",
			catObject: "cbgpPeer2AdminStatus", catKind: "enum",
			catDescription:  "administrative status",
			catIdentityInfo: "snmp_cbgppeer2adminstatus_info",
			catIndexLabels:  "cbgppeer2type,cbgppeer2remoteaddr"},
	}
	s, _, mibID := dashboardFixture(t, catalogFixture{
		Retained: catalog, Current: catalog,
		Device: []string{"snmp_cbgpPeer2AdminStatus"},
	}, "CISCO-BGP4-MIB")

	rec := do(t, s, http.MethodGet,
		"/api/mibs/"+itoa(mibID)+"/dashboard?device=dist-01", "")
	panel := panelFor(t, decodeDashboard(t, rec.Body.String()), "snmp_cbgpPeer2AdminStatus")

	if len(panel.SeriesLabels) != 0 {
		t.Fatalf("series_labels = %v, want none: the identity is this column "+
			"itself, so it would name every line after its own reading",
			panel.SeriesLabels)
	}
	// Which leaves the index, and the whole of it: a table indexed by (type,
	// address) identified only by its type names eight BGP peers "1" and "2".
	want := []string{"cbgppeer2type", "cbgppeer2remoteaddr"}
	if len(panel.IndexLabels) != len(want) {
		t.Fatalf("index_labels = %v, want %v", panel.IndexLabels, want)
	}
	for i, label := range want {
		if panel.IndexLabels[i] != label {
			t.Errorf("index_labels[%d] = %q, want %q", i, panel.IndexLabels[i], label)
		}
	}
}

// TestAStatusColumnDrawsOnceNotTwice checks a status column and its info twin draw one panel.
func TestAStatusColumnDrawsOnceNotTwice(t *testing.T) {
	catalog := []map[string]interface{}{
		{catMetric: "snmp_ifOperStatus", catModule: "IF-MIB", catObject: "ifOperStatus",
			catKind: "enum", catDescription: "operational state", catIndexLabels: "ifindex"},
		{catMetric: "snmp_ifoperstatus_info", catModule: "IF-MIB", catObject: "ifOperStatus",
			catKind: "string", catDescription: "operational state", catIndexLabels: "ifindex"},
		// A text column with no number of its own is not a twin.
		{catMetric: "snmp_ifalias_info", catModule: "IF-MIB", catObject: "ifAlias",
			catKind: "string", catDescription: "the operator's own label", catIndexLabels: "ifindex"},
	}
	s, _, mibID := dashboardFixture(t, catalogFixture{
		Retained: catalog, Current: catalog,
		Device: []string{"snmp_ifOperStatus", "snmp_ifoperstatus_info", "snmp_ifalias_info"},
	}, "IF-MIB")

	rec := do(t, s, http.MethodGet,
		"/api/mibs/"+itoa(mibID)+"/dashboard?device=dist-01", "")
	dash := decodeDashboard(t, rec.Body.String())

	drawn := map[string]bool{}
	for _, p := range dash.Panels {
		drawn[p.Metric] = true
	}

	if drawn["snmp_ifoperstatus_info"] {
		t.Error("the info twin of ifOperStatus was drawn as well as its number; " +
			"they say the same thing and only the number carries the decode")
	}
	if !drawn["snmp_ifOperStatus"] {
		t.Error("the number was dropped instead of the twin")
	}
	if !drawn["snmp_ifalias_info"] {
		t.Error("a text column with no number of its own was dropped as a twin")
	}
	// And the coverage table has to agree with the panels it describes.
	cov := coverageFor(t, dash, "IF-MIB")
	if cov.Reporting != 2 || cov.Charts != 2 {
		t.Errorf("coverage = %+v, want 2 reporting and 2 charted", cov)
	}
}

// TestAnUndescribedInfoMetricIsTextNotAFlatLine checks an undescribed _info family renders as text.
func TestAnUndescribedInfoMetricIsTextNotAFlatLine(t *testing.T) {
	s, device, _ := dashboardFixture(t, catalogFixture{
		// Nothing describes these; the catalog has no row for either.
		Device: []string{"snmp_racname_info", "snmp_racfirmwareversion_info",
			"snmp_1_3_6_1_4_1_674_10892"},
	}, "IDRAC-MIB-SMIv2")

	rec := do(t, s, http.MethodGet, "/api/devices/"+itoa(device)+"/dashboard", "")
	dash := decodeDashboard(t, rec.Body.String())

	for _, metric := range []string{"snmp_racname_info", "snmp_racfirmwareversion_info"} {
		panel := panelFor(t, dash, metric)
		if panel.Type != panelText {
			t.Errorf("%s drawn as %q, want text: it is a constant 1 and its reading "+
				"is in a label", metric, panel.Type)
		}
		if panel.ValueLabel != "value" {
			t.Errorf("%s carries no value label, so the widget has nothing to show",
				metric)
		}
	}

	// And a metric that is NOT an info family keeps the honest default. The
	// contract says nothing about it, so neither does the panel.
	if got := panelFor(t, dash, "snmp_1_3_6_1_4_1_674_10892").Type; got != panelLine {
		t.Errorf("a non-info undescribed metric drawn as %q, want line", got)
	}
}

// TestNoTwoPanelsShareAQuery checks no two panels of a device share a query, even when two modules describe one metric.
func TestNoTwoPanelsShareAQuery(t *testing.T) {
	catalog := []map[string]interface{}{
		{catMetric: "snmp_ifInOctets", catModule: "IF-MIB", catKind: "counter",
			catDescription: "octets received"},
		{catMetric: "snmp_ifOperStatus", catModule: "IF-MIB", catKind: "enum",
			catDescription: "operational state"},
		{catMetric: "snmp_entPhysicalClass", catModule: "ENTITY-MIB", catKind: "enum",
			catDescription: "class"},
		{catMetric: "snmp_sysUpTime", catModule: "SNMPv2-MIB", catKind: "gauge",
			catDescription: "uptime"},
	}
	reported := []string{
		"snmp_ifInOctets", "snmp_ifOperStatus", "snmp_entPhysicalClass",
		"snmp_sysUpTime", "snmp_unCatalogedThing",
	}
	s, device, _ := dashboardFixture(t, catalogFixture{
		Retained: catalog, Current: catalog, Device: reported,
	}, "IF-MIB")

	rec := do(t, s, http.MethodGet, "/api/devices/"+itoa(device)+"/dashboard", "")
	dash := decodeDashboard(t, rec.Body.String())

	if len(dash.Panels) == 0 {
		t.Fatal("no panels: the fixture proves nothing")
	}
	seen := make(map[string]Panel, len(dash.Panels))
	for _, p := range dash.Panels {
		if first, dup := seen[p.Query]; dup {
			t.Errorf("query %q is drawn by two panels: %q (%s) and %q (%s)",
				p.Query, first.ID, first.Module, p.ID, p.Module)
		}
		seen[p.Query] = p
	}
}

// TestAPanelQueryIsDeterminedByItsMetricAndDevice checks the query depends only on metric and device.
func TestAPanelQueryIsDeterminedByItsMetricAndDevice(t *testing.T) {
	queryFor := func(t *testing.T, module string) string {
		t.Helper()
		catalog := []map[string]interface{}{
			{catMetric: "snmp_ifInOctets", catModule: module, catKind: "counter",
				catDescription: "octets received"},
		}
		s, device, _ := dashboardFixture(t, catalogFixture{
			Retained: catalog, Current: catalog, Device: []string{"snmp_ifInOctets"},
		}, module)

		rec := do(t, s, http.MethodGet, "/api/devices/"+itoa(device)+"/dashboard", "")
		dash := decodeDashboard(t, rec.Body.String())
		if len(dash.Panels) != 1 {
			t.Fatalf("panels = %d under %s, want 1", len(dash.Panels), module)
		}
		return dash.Panels[0].Query
	}

	underIF := queryFor(t, "IF-MIB")
	underEntity := queryFor(t, "ENTITY-MIB")

	if underIF != underEntity {
		t.Errorf("the same metric on the same device asks two different questions "+
			"depending on its module:\n  IF-MIB     %s\n  ENTITY-MIB %s", underIF, underEntity)
	}
	if !strings.Contains(underIF, "snmp_ifInOctets") {
		t.Errorf("query %q does not name its metric", underIF)
	}
	if !strings.Contains(underIF, `device="dist-01"`) {
		t.Errorf("query %q does not scope to a device, so two devices would share it", underIF)
	}
}
