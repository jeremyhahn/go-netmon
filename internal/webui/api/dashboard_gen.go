package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gorilla/mux"

	"github.com/jeremyhahn/go-netmon/internal/promname"
	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// DashboardRequest represents a dashboard generation request
type DashboardRequest struct {
	DeviceID int64   `json:"device_id"`
	MIBIDs   []int64 `json:"mib_ids"`
	Title    string  `json:"title,omitempty"`
}

// Dashboard is a device's panels, as the browser receives them.
//
// One shape, from one builder, whichever endpoint asked. The per-module
// endpoint the UI draws its tabs from and the whole-device endpoint differ only
// in how many modules they ask about.
type Dashboard struct {
	Device string `json:"device"`

	// MIB is the single module this dashboard was narrowed to, when it was.
	// The UI renders one tab per module and checks that what came back is the
	// tab it asked for.
	MIB string `json:"mib,omitempty"`

	Title    string        `json:"title"`
	Panels   []Panel       `json:"panels"`
	MIBs     []MIBCoverage `json:"mibs"`
	Warnings []string      `json:"warnings"`

	// CatalogUnavailable says the catalog could not be READ, which is not the
	// same as its being empty and means the opposite thing. Empty is a
	// collector that is not the designated emitter: nothing describes these
	// metrics and nothing will. Unavailable is Prometheus refusing the query,
	// and then every panel here is undescribed, attributed to no module, and
	// counted as a MIB that stopped resolving -- none of which is true. A
	// consumer is told so rather than left to draw that conclusion.
	CatalogUnavailable bool `json:"catalog_unavailable,omitempty"`
}

// Panel is one published metric, drawn one way.
//
// Everything a renderer needs is on it, including the PromQL. The browser used
// to assemble queries itself from a metric name and a device, which meant the
// two rules that make a panel readable -- wrap a counter in rate() over a
// window the device's poll interval can fill, and join the identity metric that
// holds each row's name -- had to be reimplemented there against a MIB-shaped
// specification that did not carry the facts they need. The catalog carries
// them, and the catalog is read here.
type Panel struct {
	// ID is unique within a dashboard and stable across regenerations, so a
	// renderer can key a chart on it.
	ID string `json:"id"`

	Title string `json:"title"`

	// Type is the widget: line, rate, state or text. It comes from the
	// declared kind, never from a name match, which is what lets a MIB nobody
	// has seen render correctly.
	Type string `json:"type"`

	// Metric is the published metric name; Query is the PromQL to draw it.
	Metric string `json:"metric"`
	Query  string `json:"query"`

	Module      string `json:"module,omitempty"`
	Object      string `json:"object,omitempty"`
	OID         string `json:"oid,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Unit        string `json:"unit,omitempty"`
	Description string `json:"description,omitempty"`

	// SeriesLabels are the labels that could name a line, best first. A
	// renderer takes the first one a series actually carries.
	//
	// It is a list rather than one label because a name is not guaranteed to
	// be there: the exporter promotes a table's status columns onto its
	// readings only for the rows it polled the status column for. When the
	// name is missing the index labels still identify the row exactly, which
	// is less friendly and never wrong.
	SeriesLabels []string `json:"series_labels,omitempty"`

	// IndexLabels identify a row exactly when there is no name to show.
	IndexLabels []string `json:"index_labels,omitempty"`

	// Enum decodes a state panel's numbers.
	Enum map[string]string `json:"enum,omitempty"`

	// ValueLabel is where a text panel's reading is. An info metric's sample
	// is always 1; the words are in a label.
	ValueLabel string `json:"value_label,omitempty"`
}

// The widget kinds a panel can be.
const (
	panelLine  = "line"
	panelRate  = "rate"
	panelState = "state"
	panelText  = "text"
)

// handleGenerateDashboard generates a dashboard for a device and an explicit
// MIB set.
func (s *Server) handleGenerateDashboard(w http.ResponseWriter, r *http.Request) {
	var req DashboardRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.DeviceID == 0 {
		http.Error(w, "device_id is required", http.StatusBadRequest)
		return
	}

	if len(req.MIBIDs) == 0 {
		http.Error(w, "at least one mib_id is required", http.StatusBadRequest)
		return
	}

	device, err := s.storage.GetDevice(r.Context(), req.DeviceID)
	if err != nil {
		http.Error(w, "device not found", http.StatusNotFound)
		return
	}

	var mibNames []string
	for _, mibID := range req.MIBIDs {
		mib, getErr := s.storage.GetMIB(r.Context(), mibID)
		if getErr != nil {
			http.Error(w, fmt.Sprintf("MIB %d not found", mibID), http.StatusNotFound)
			return
		}
		mibNames = append(mibNames, mib.Name)
	}

	// Generating a dashboard cannot fail. It used to initialize a MIB parser,
	// which could; now it reads Prometheus, and every one of those reads
	// degrades to empty rather than erroring — a device reporting nothing gets
	// an empty dashboard, which is the honest answer, not a 500.
	respondJSON(w, http.StatusOK,
		s.generateDashboard(r.Context(), device, mibNames, req.Title, scopeDevice))
}

// handleGenerateDeviceDashboard generates a dashboard for a device using its
// assigned MIBs.
func (s *Server) handleGenerateDeviceDashboard(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	deviceID, err := strconv.ParseInt(vars["id"], 10, 64)
	if err != nil {
		http.Error(w, "invalid device ID", http.StatusBadRequest)
		return
	}

	device, err := s.storage.GetDevice(r.Context(), deviceID)
	if err != nil {
		http.Error(w, "device not found", http.StatusNotFound)
		return
	}

	mibs, err := s.storage.GetDeviceMIBs(r.Context(), deviceID)
	if err != nil {
		http.Error(w, "failed to get device MIBs", http.StatusInternalServerError)
		return
	}

	if len(mibs) == 0 {
		http.Error(w, "device has no assigned MIBs", http.StatusBadRequest)
		return
	}

	var mibNames []string
	for _, mib := range mibs {
		mibNames = append(mibNames, mib.Name)
	}

	respondJSON(w, http.StatusOK,
		s.generateDashboard(r.Context(), device, mibNames, "", scopeDevice))
}

// generateDashboard builds a dashboard from Prometheus alone.
//
// It parses no MIBs, and that is the point. The poller parses them, publishes
// what every metric MEANS as the metric catalog, and this joins that against
// the metrics the device is actually reporting. Import a MIB, add a device,
// and the dashboard follows -- for any MIB, including ones nobody has seen.
//
// The old version enumerated MIB OBJECTS and asked whether each had data. That
// could not work for the reading this project exists to show: the poller
// publishes optical power under a name derived from its UNIT
// (snmp_entity_sensor_dbm), and no MIB object has that name, so an
// object-driven walk rendered the threshold metadata columns and silently
// omitted the measurement. Enumerating what EXISTS finds everything the poller
// publishes, whatever it chose to call it.
// dashboardScope says whose question a dashboard is answering.
//
// It is passed rather than inferred, because the two cases are not
// distinguishable from the module list: a device with exactly one assigned MIB
// asks the same-looking question as the browser rendering one of ten tabs, and
// the right answer differs.
type dashboardScope bool

const (
	// scopeDevice is everything the device reports, including the families no
	// MIB claims -- the readings the poller derives, and the raw OIDs.
	scopeDevice dashboardScope = false

	// scopeModule is one module's tab. The browser asks once per tab, so the
	// unattributable families must not ride along: they would appear on every
	// tab, each with its own copy of the same coverage warning.
	scopeModule dashboardScope = true
)

func (s *Server) generateDashboard(ctx context.Context, device *storage.Device, mibNames []string, title string, scope dashboardScope) Dashboard {
	if title == "" {
		title = fmt.Sprintf("%s - %s", device.Name, strings.Join(mibNames, ", "))
	}

	// What this device is actually reporting. Device-scoped deliberately: the
	// fleet-wide question offers a panel whenever ANY device reports a metric,
	// which promised core-01 75 panels of data that could never arrive.
	available := s.prometheusMetricNames(ctx,
		map[string]string{promname.DeviceLabel: device.Name})

	// What those metrics mean. Held between reads: it describes what the
	// loaded MIBs mean, which changes when a MIB is imported, not when a
	// device is polled.
	read := s.catalogRead(ctx)
	catalog := read.entries
	log.Printf("INFO: %s reports %d metrics; the catalog describes %d",
		device.Name, len(available), len(catalog))

	// An empty module list means "everything this device reports", which is the
	// useful default for a device whose MIBs you have not decided about yet.
	wanted := make(map[string]bool, len(mibNames))
	for _, m := range mibNames {
		wanted[m] = true
	}

	// Sorted so construction is deterministic -- the same device produces the
	// same dashboard on every request. This is NOT the display order; see the
	// sort by title below.
	names := make([]string, 0, len(available))
	for name := range available {
		names = append(names, name)
	}
	sort.Strings(names)

	window := rateWindow(s.polling().Effective(device.PollInterval))

	panels := []Panel{}
	byModule := map[string]*MIBCoverage{}
	touch := func(module string) *MIBCoverage {
		if c, ok := byModule[module]; ok {
			return c
		}
		c := &MIBCoverage{MIB: module}
		byModule[module] = c
		return c
	}

	// Coverage is seeded from what was ASKED for, not from what answered.
	// Populating it only while iterating reported metrics made a module that
	// reports nothing vanish from the table altogether -- which is exactly the
	// case the coverage note exists to explain, so its most useful diagnostic
	// ("wait one poll interval") could never fire. A MIB assigned a moment ago
	// has to appear, saying nothing has arrived yet.
	for _, module := range mibNames {
		touch(module)
	}

	for _, name := range names {
		entry, described := catalog[name]

		// A metric the catalog cannot describe still gets a panel. It has data
		// by construction -- it came from what the device reports -- and a
		// nameless line is more use than a hidden one. Raw-OID names are the
		// expected case: no loaded MIB describes the OID, so nothing can.
		// A metric with no module is not attributable to a MIB, so a MIB
		// filter cannot meaningfully exclude it -- it is kept whatever was
		// selected. This is not a detail: the families the poller DERIVES
		// carry no module, because there is no MIB object behind them.
		// Filtering them out silently removed the optical readings, which are
		// the whole reason the catalog is keyed on the published name. Found
		// on monitor-01 with 40 dBm series present and zero of them on the
		// dashboard.
		module := entry.Module
		if module == "" {
			module = unattributedModule
			// A metric with no module is not attributable to a MIB, so no MIB
			// filter can meaningfully include or exclude it. It belongs to the
			// DEVICE, and it is shown when the question is about the device --
			// every module it has, or no module named at all.
			//
			// Keeping it on a module's tab put snmp_system_info and every
			// optical reading on all ten of core-01's tabs, and gave every
			// one of them the same "(uncataloged)" warning about the same
			// metric.
			if scope == scopeModule {
				continue
			}
		} else if len(wanted) > 0 && !wanted[module] {
			continue
		}

		// A status column is published twice: as its number, and again as an
		// info metric carrying the same value rendered to text. Both are real
		// families and both are described, so both became panels -- ifOperStatus
		// appeared as a decoded state list AND as a second card saying the same
		// thing, named by row index because an info series carries no friendly
		// label. Twenty-two tiles on core-01's IF-MIB tab where eleven say
		// everything.
		//
		// The number is the better of the two: the catalog decodes it, and the
		// exporter puts the row's name on it. So the info twin is dropped when
		// its own object also reports a number. A text object that has no
		// number -- a text scalar, ifDescr, ifAlias -- is not a twin and stays.
		if described && entry.Object != "" && !entry.Chartable() &&
			available[promname.MetricName(entry.Object)] {
			continue
		}

		cov := touch(module)
		cov.Reporting++
		// Unlabeled means the panel has no words to put on it: either no
		// catalog row at all, or a row whose DESCRIPTION is empty. The second
		// half is what makes this countable for a NAMED module -- a metric
		// with no row has no module either, so it lands in the uncataloged
		// bucket, and counting only those made this per-module number
		// permanently zero.
		if !described || entry.Description == "" {
			cov.Undescribed++
		}

		panels = append(panels, buildPanel(entry, name, module, device.Name, window))
		cov.Charts++
	}

	sortPanelsForDisplay(panels)

	coverage := make([]MIBCoverage, 0, len(byModule))
	for _, c := range byModule {
		c.Note = coverageNote(*c)
		coverage = append(coverage, *c)
	}
	sort.Slice(coverage, func(i, j int) bool { return coverage[i].MIB < coverage[j].MIB })

	warnings := coverageWarnings(coverage)
	if !read.readable {
		// First, because it explains every other line under it.
		warnings = append([]string{
			"The metric catalog could not be read from Prometheus, so nothing on " +
				"this page can be labeled or attributed to a MIB. The panels are " +
				"real -- their data comes from a different query -- but their units, " +
				"descriptions and decoded states are missing, and any coverage " +
				"figure below describes the failed read rather than the MIBs.",
		}, warnings...)
	}

	dash := Dashboard{
		Device:             device.Name,
		Title:              title,
		Panels:             panels,
		MIBs:               coverage,
		Warnings:           warnings,
		CatalogUnavailable: !read.readable,
	}
	if len(mibNames) == 1 {
		dash.MIB = mibNames[0]
	}
	return dash
}

// buildPanel describes one reported metric as the panel that draws it.
func buildPanel(e CatalogEntry, metric, module, device, window string) Panel {
	// A metric the catalog cannot describe still has one thing said about it
	// by the exporter's naming contract: whether it is an info metric. That is
	// not a guess from the name -- InfoMetricName produces that shape for
	// exactly one reason, that the value is text and cannot be a sample -- and
	// without it an undescribed info family is drawn as a line through the
	// constant 1. ci-node-1 had thirteen, including the device's own name and
	// firmware version, rendered as flat lines saying nothing.
	if e.Kind == "" && promname.IsInfoMetric(metric) {
		e.Kind = string(kindString)
	}

	panel := Panel{
		ID:          module + "_" + metric,
		Title:       panelTitle(e, metric),
		Type:        e.ChartType(),
		Metric:      metric,
		Module:      e.Module,
		Object:      e.Object,
		OID:         e.OID,
		Kind:        e.Kind,
		Unit:        e.Unit,
		Description: e.Description,
		IndexLabels: e.IndexLabels,
		Enum:        e.Enum,
	}

	panel.Query = panelQuery(e, metric, device, window)
	panel.SeriesLabels = seriesLabels(e)

	// A text family has no line to draw: it is a constant 1 carrying its
	// reading in a label, so a renderer given only the metric name would show
	// a widget with nothing in it. Say which label holds the text.
	if !e.Chartable() {
		panel.ValueLabel = catValueLabel
	}
	return panel
}

// panelQuery builds the PromQL a panel is drawn from.
//
// The one thing that happens here and cannot happen in the browser is the
// rate() wrapper, because the window has to be wide enough for the device's own
// poll interval to have put two samples in it, and only the server knows what
// that interval is. Drawing a counter unwrapped gives a line that only ever
// goes up, which says nothing about traffic.
//
// Nothing is joined. An earlier version of this pulled the row's name across
// from its info metric with a group_left join, which was unnecessary and worse
// than unnecessary: the exporter already promotes a table's status columns onto
// its readings, so snmp_ifInOctets arrives carrying ifdescr="Loopback0" beside
// its ifindex (measured on core-01). The join spent a vector match to
// recover a label that was already there, and a group_left whose right-hand
// side ever has two series for one row fails the WHOLE query -- turning a
// missing line name into a panel with no data at all.
func panelQuery(e CatalogEntry, metric, device, window string) string {
	selector := metric + `{` + promname.DeviceLabel + `="` + escapeLabelValue(device) + `"}`
	if e.ChartType() == panelRate {
		return "rate(" + selector + "[" + window + "])"
	}
	return selector
}

// seriesLabels are the labels that could NAME one line of a panel, best first.
//
// Only names. The index labels are carried separately, because they are used
// differently: a name is one label and the first one present wins, while an
// index is a TUPLE and every part of it is needed. cbgpPeer2Table is indexed by
// (type, address), so taking the first index label alone named eight BGP peers
// "1" and "2" -- observed on core-01's dashboard.
//
// The candidates, in order:
//
//   - The identity label, when the catalog declares one. An entity sensor
//     reading carries entphysicalname on the series itself.
//   - The label the identity info metric's value is also published in. The
//     catalog names snmp_ifdescr_info as where an interface row's name lives,
//     and the exporter puts that same value on the reading as ifdescr.
//
// Either may be missing from a given series -- the exporter promotes a status
// column onto the rows it polled it for -- which is why a renderer is handed
// the order rather than an answer, and why the index is there behind them.
func seriesLabels(e CatalogEntry) []string {
	labels := make([]string, 0, 2)

	if e.Identity != "" {
		labels = append(labels, e.Identity)
	}

	// A column can be its own table's identity, and then the label is
	// circular: it holds this metric's own reading. Naming a state panel's
	// lines after their own states gives two BGP peers both called "start".
	// Fall through to the index, which tells them apart.
	identity := promname.IdentityLabel(e.IdentityInfo)
	if identity != "" && identity != promname.IndexLabel(e.Object) {
		labels = append(labels, identity)
	}
	return labels
}

// rateWindow is the lookback rate() needs for a device's poll interval.
//
// rate() needs at least two samples inside its window, and the number of
// samples in a window is NOT the number of polls in it. A device is polled on
// schedule and an individual OID answers on only some of those polls. Measured
// on this fleet: core-01 is polled 110 times an hour and one of its counters
// lands 77 times; lab-node-1-bmc is polled 57 times and its counter lands 25. So
// the effective spacing of one metric's samples is 1.4 to 2.4 poll intervals,
// not one.
//
// Four intervals was the usual guidance and is what this used to be. It leaves
// between 1.7 and 2.9 real samples in the window -- either side of the two
// rate() requires -- which is why a panel drew on one device and not the next,
// and on the same device ten minutes apart. Measured at the moment this was
// written: 4 intervals left 36 of core-01's 147 rate panels empty, and had
// left ALL 104 of lab-node-3-bmc's empty a few minutes earlier. At 6 intervals
// every rate panel on every device drew.
//
// Eight is six plus one step of margin, and the margin is the point: six is the
// measured minimum, and choosing the minimum means panels blink empty whenever
// delivery jitters, which is what the fleet was already doing. The cost is a
// rate averaged over eight poll intervals rather than four -- three to five
// real samples -- and no resolution is lost that was ever visible, because a
// counter whose samples arrive every two minutes cannot show a one-minute
// spike however narrow the window.
func rateWindow(pollSeconds int) string {
	if pollSeconds <= 0 {
		pollSeconds = 30
	}
	window := pollSeconds * 8
	if window < 60 {
		window = 60
	}
	if window%60 == 0 {
		return strconv.Itoa(window/60) + "m"
	}
	return strconv.Itoa(window) + "s"
}

// escapeLabelValue makes a device name safe inside a PromQL string literal.
func escapeLabelValue(value string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(value)
}

// unattributedModule groups the metrics no MIB module claims. Named rather than
// left blank so it is visible in the UI instead of collapsing into a module
// with no name.
//
// It was called "(uncataloged)", which described only half of what lands in it.
// A raw-OID metric is uncataloged; a family the poller DERIVES is not -- optical
// power carries a unit and a description and simply has no MIB object behind it.
// The bucket is about attribution, not about description, and the note below
// now counts the two apart.
const unattributedModule = "(no module)"

// panelTitle prefers the MIB object name, which reads better than the metric.
// sortPanelsForDisplay orders panels by the words the reader can see.
//
// They used to arrive in published-metric-name order, which is the key the
// generator iterates and NOT the string on the card: the card reads
// "IF-MIB - ifHCInOctets" while the sort key was "snmp_ifHCInOctets". Those
// agree only when the metric name is derived from the object name, and the
// catalog deliberately does not guarantee that -- a derived family has no MIB
// object at all, so its title IS its metric name and it sorted among the
// module-prefixed titles rather than among them. Measured on dist-01: 220
// panels, 0 out-of-order transitions by metric name and 6 by title, e.g.
// "ENTITY-MIB - entPhysicalParentRelPos" followed by
// "CISCO-ENTITY-SENSOR-MIB - entSensorPrecision".
//
// A list sorted by an invisible key is indistinguishable from an unsorted one,
// which is how the owner read it (2026-09-02).
//
// Case-insensitive, because alphabetical to a reader is not byte order: every
// module-prefixed title is uppercase and every bare metric name is lowercase,
// so a byte-order sort puts all of the latter in a clump at the end rather
// than in their places. Ties fall back to the exact title and then the metric,
// so the order is total and two runs cannot differ.
func sortPanelsForDisplay(panels []Panel) {
	sort.SliceStable(panels, func(i, j int) bool {
		a, b := panels[i], panels[j]
		af, bf := strings.ToLower(a.Title), strings.ToLower(b.Title)
		if af != bf {
			return af < bf
		}
		if a.Title != b.Title {
			return a.Title < b.Title
		}
		return a.Metric < b.Metric
	})
}

func panelTitle(e CatalogEntry, metric string) string {
	if e.Module != "" && e.Object != "" {
		return e.Module + " - " + e.Object
	}
	if e.Object != "" {
		return e.Object
	}
	return metric
}

// MIBCoverage says what one of a device's MIBs contributed to its dashboard.
//
// The counts changed meaning when the dashboard stopped parsing MIBs. They used
// to compare panels against the module's POLLABLE OBJECTS, which the UI could
// only know by parsing the MIB itself. They now describe what the device is
// REPORTING, which is what Prometheus can answer:
//
//   - every module the caller asked for has a row, whether or not it answered
//   - Reporting counts the metrics of that module the device is sending
//   - Undescribed counts those the catalog could put no words on
//
// A freshly assigned MIB appears with Reporting at zero and a note saying so.
// It used to be absent instead -- the table was built only from the metrics
// that arrived -- which read as "this module is fine" for the one case an
// operator most needs explained.
type MIBCoverage struct {
	MIB string `json:"mib"`
	// Reporting is how many of this module's metrics the device is sending.
	Reporting int `json:"reporting"`
	// Charts is how many became a panel.
	Charts int `json:"charts"`
	// Undescribed counts metrics the catalog cannot put words on -- no row,
	// or a row with an empty DESCRIPTION. They are still charted; the count is
	// what says how much of this module the dashboard can label. A module
	// whose MIB imports did not resolve shows up here, because an object with
	// no resolved type carries no description either.
	Undescribed int `json:"undescribed,omitempty"`
	// Note explains a shortfall, and is empty when there is none.
	Note string `json:"note,omitempty"`
}

// coverageNote explains a shortfall in what a module contributed.
func coverageNote(c MIBCoverage) string {
	switch {
	case c.MIB == unattributedModule:
		// Silent when every one of them is described. A metric the poller
		// derives belongs here and is perfectly healthy -- warning about
		// optical power on every dashboard is how a warning stops being read.
		if c.Undescribed == 0 {
			return ""
		}
		return fmt.Sprintf(
			"%d of the %d metrics no MIB module claims cannot be labeled: the catalog "+
				"has no row for them, so they are charted without a unit, description "+
				"or enumeration. A raw-OID metric name is the expected case: no loaded "+
				"MIB describes that OID. A jump here means MIBs stopped resolving, or "+
				"the designated catalog emitter is not running.",
			c.Undescribed, c.Reporting)

	case c.Reporting == 0:
		return fmt.Sprintf(
			"%s is reporting nothing, so it adds no panels. A panel appears once the "+
				"device has been polled and answered — wait one poll interval and "+
				"generate again. If it stays empty, the device is not answering those "+
				"OIDs.", c.MIB)

	case c.Undescribed > 0:
		return fmt.Sprintf(
			"%s contributed %d panels, %d of them unlabeled because the catalog "+
				"carries no description for those metrics.", c.MIB, c.Charts, c.Undescribed)

	default:
		return ""
	}
}

// coverageWarnings is every note worth showing, in the order the MIBs were read.
func coverageWarnings(coverage []MIBCoverage) []string {
	warnings := []string{}
	for _, c := range coverage {
		if c.Note != "" {
			warnings = append(warnings, c.Note)
		}
	}
	return warnings
}

// catalogRead returns the held catalog, or reads it directly on a server built
// without a cache -- which is only a server assembled by hand in a test.
func (s *Server) catalogRead(ctx context.Context) metricCatalogResult {
	if s.catalog != nil {
		return s.catalog.Catalog(ctx)
	}
	return s.metricCatalog(ctx)
}
