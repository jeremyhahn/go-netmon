package api

import (
	"context"
	"log"
	"strconv"
	"strings"
	"sync"
)

// The dashboard reads what every metric MEANS from Prometheus, not from MIBs.
//
// The poller parses the MIBs, and it is the only thing that does. It publishes
// what it learned as snmp_metric_catalog -- one constant-1 series per metric,
// carrying the unit, the kind, and where the row's human-readable name lives.
// The UI joins that against the metrics a device is actually reporting.
//
// This is what makes the dashboard work for any device and any MIB: nothing
// here knows what SNMP is. A renderer that enumerated MIB OBJECTS instead
// could not reach the readings at all, because the poller publishes some
// families under a name derived from their UNIT -- optical power arrives as
// snmp_entity_sensor_dbm, and no MIB object has that name.

// Catalog metric and label names. These mirror internal/metrics and are
// duplicated deliberately: they are a wire contract between two processes, not
// a shared type. Importing the exporter's package here would let a refactor
// there silently change what the UI reads, which is exactly the coupling this
// design removes.
const (
	catalogMetric     = "snmp_metric_catalog"
	catalogEnumMetric = "snmp_metric_enum"

	catMetric       = "metric"
	catModule       = "module"
	catObject       = "object"
	catOID          = "oid"
	catUnit         = "unit"
	catKind         = "kind"
	catDescription  = "description"
	catIdentity     = "identity_label"
	catIdentityInfo = "identity_info_metric"
	catIndexLabels  = "index_labels"
	catEnumValue    = "value"
	catEnumLabel    = "label"

	// catValueLabel is where an info metric's text actually is. The sample is
	// the constant 1, so a renderer given only the metric name has nothing to
	// show; the reading is in this label. Same wire contract as the rest, and
	// duplicated here for the same reason.
	catValueLabel = "value"
)

// CatalogLookback is how far back the RETAINED catalog read reaches.
//
// It buys availability and nothing else. The emitter republishes every two
// minutes, so a window this wide keeps every description on screen through a
// restart or a spell of refused remote writes -- and every panel depends on
// the catalog, so its failure mode is a fleet-wide loss of every label at once
// while the metrics themselves look perfectly healthy.
//
// The same width is what makes a SUPERSEDED row visible: a MIB reload that
// changes a description writes a different series, the emitter stops writing
// the old one, and the window keeps it queryable for the rest of the hour. See
// currentCatalogQuery for how that is resolved. Widening this without that
// second read would surface two rows describing one metric differently.
const CatalogLookback = "1h"

// The two reads, and why deciding between them cannot be done on timestamps.
//
// A range-vector function stamps its output at the QUERY EVALUATION TIME, not
// at the time of the sample it selected. Measured against monitor-01's Prometheus
// 3.14.0: the two live rows for snmp_chassisType, one last written at
// 1788303900 and the other at 1788304140, both came back stamped 1788304293 --
// the instant the query ran. So every row in a last_over_time read carries an
// identical timestamp, and a consumer comparing them is comparing a constant.
// docs/metrics/metric-catalog.md prescribed exactly that comparison and it
// resolved nothing; whichever row Prometheus returned first won.
//
// What Prometheus can answer cheaply is the question the rule was always about
// -- which row is STILL BEING WRITTEN. A plain instant selector answers only
// for series with a sample inside the staleness window, and the emitter's
// interval (2 minutes) is well inside it, so a superseded row drops out on its
// own. Measured on monitor-01: the retained read returns 12,324 rows, the current
// read 12,323, and the one it sheds is the stale chassisType row.
//
// The subquery that WOULD carry true sample times,
// last_over_time(timestamp(snmp_metric_catalog)[1h:1m]), is correct and was
// rejected on cost: 3.4-5.2s against 0.5-0.7s for the plain read, per family,
// on every dashboard load.
func currentCatalogQuery(family string) string {
	return family
}

// retainedCatalogQuery reads the wide window, which still holds superseded rows.
func retainedCatalogQuery(family string) string {
	return "last_over_time(" + family + "[" + CatalogLookback + "])"
}

// CatalogEntry is what the catalog says about one published metric.
type CatalogEntry struct {
	Metric      string
	Module      string
	Object      string
	OID         string
	Unit        string
	Kind        string
	Description string

	// Identity and IdentityInfo are mutually exclusive and answer "what names
	// each line in this panel?". Identity is a label on the series itself
	// (entphysicalname on an optical reading). IdentityInfo names a separate
	// info metric holding the name one join away (snmp_ifname_info for an
	// ordinary interface column, which carries only ifindex). Which one is set
	// is not a preference -- it is where the name physically is.
	Identity     string
	IdentityInfo string

	IndexLabels []string

	// Enum maps a numeric sample to its label, so a renderer can show "up"
	// rather than 1 without knowing what ifOperStatus is.
	Enum map[string]string
}

// Chartable reports whether a panel drawn from this entry can show anything.
//
// A string family is a constant 1 carrying its value in a label: plotting it
// draws a flat line that says nothing. It belongs in a table, and the renderer
// is told so rather than left to infer it from the metric name.
func (e CatalogEntry) Chartable() bool {
	return e.Kind != string(kindString)
}

type metricKind string

const (
	kindCounter metricKind = "counter"
	kindGauge   metricKind = "gauge"
	kindEnum    metricKind = "enum"
	kindString  metricKind = "string"
	kindUnknown metricKind = "unknown"
)

// ChartType is the widget this entry should be drawn with.
//
// It is chosen from the DECLARED semantics -- kind, and enum presence -- never
// from a metric name match. That is what lets a MIB nobody has seen render
// correctly: an unrecognized counter is still a counter.
func (e CatalogEntry) ChartType() string {
	switch metricKind(e.Kind) {
	case kindString:
		return "text"
	case kindEnum:
		return "state"
	case kindCounter:
		return "rate"
	default:
		return "line"
	}
}

// metricCatalog reads the catalog from Prometheus, keyed by metric name.
//
// Returns an empty map rather than an error when the catalog is absent: a
// collector that is not the designated emitter publishes none, and a dashboard
// should then degrade to unlabeled panels rather than refuse to render. The
// caller distinguishes the two by the count.
// readable reports whether at least one of the two catalog reads answered.
//
// An empty catalog and an UNREADABLE one look identical from the map, and they
// mean opposite things. Empty is a collector that is not the designated
// emitter: nothing describes these metrics and nothing is going to. Unreadable
// is Prometheus refusing the query, and every metric on the device is then
// undescribed, unattributed to any module, and reported as a MIB that stopped
// resolving. Observed on monitor-01 in the seconds after a restart: 363 panels on one
// device, none of them labeled, under a warning naming a fault that did not
// exist.
type metricCatalogResult struct {
	entries  map[string]CatalogEntry
	readable bool
}

func (s *Server) metricCatalog(ctx context.Context) metricCatalogResult {
	// Four independent reads, issued together. They share nothing, so the
	// wall-clock cost is the slowest of them rather than their sum -- which
	// matters most in the case that is already going badly: a Prometheus that
	// is timing out costs one client timeout here, not four in a row.
	var (
		wait                    sync.WaitGroup
		retained, current       map[string]CatalogEntry
		retainedOK, currentOK   bool
		retainedEnums, curEnums map[string]map[string]string
	)
	read := func(f func()) {
		wait.Add(1)
		go func() {
			defer wait.Done()
			f()
		}()
	}
	read(func() { retained, retainedOK = s.catalogRows(ctx, retainedCatalogQuery(catalogMetric)) })
	read(func() { current, currentOK = s.catalogRows(ctx, currentCatalogQuery(catalogMetric)) })
	read(func() { retainedEnums = s.enumRows(ctx, retainedCatalogQuery(catalogEnumMetric)) })
	read(func() { curEnums = s.enumRows(ctx, currentCatalogQuery(catalogEnumMetric)) })
	wait.Wait()

	// The wide window first, then the rows still being written laid over it.
	// Which read answered is the whole tie-break: a current row replaces
	// whatever the window remembered for that metric, and the window is left
	// carrying only the metrics nothing is publishing right now -- which is
	// exactly what it is for.
	entries := retained
	for name, entry := range current {
		entries[name] = entry
	}

	attachEnums(entries, retainedEnums, curEnums)
	return metricCatalogResult{entries: entries, readable: retainedOK || currentOK}
}

// catalogRows reads one catalog query into entries keyed by metric name.
func (s *Server) catalogRows(ctx context.Context, query string) (map[string]CatalogEntry, bool) {
	rows, err := s.promClient.Query(ctx, query)
	if err != nil {
		log.Printf("WARNING: could not read the metric catalog (%s): %v", query, err)
		return map[string]CatalogEntry{}, false
	}

	entries := make(map[string]CatalogEntry, len(rows))
	for _, row := range rows {
		name := row.Metric[catMetric]
		if name == "" {
			continue
		}
		entry := CatalogEntry{
			Metric:       name,
			Module:       row.Metric[catModule],
			Object:       row.Metric[catObject],
			OID:          row.Metric[catOID],
			Unit:         row.Metric[catUnit],
			Kind:         row.Metric[catKind],
			Description:  row.Metric[catDescription],
			Identity:     row.Metric[catIdentity],
			IdentityInfo: row.Metric[catIdentityInfo],
			IndexLabels:  splitLabels(row.Metric[catIndexLabels]),
		}
		if prev, seen := entries[name]; seen && rowOrder(prev) <= rowOrder(entry) {
			continue
		}
		entries[name] = entry
	}
	return entries, true
}

// rowOrder breaks a tie between two rows answering one read for one metric.
//
// The tie is real and bounded. A plain selector answers for any series with a
// sample inside Prometheus's lookback delta -- five minutes by default -- so
// for the few minutes after a reload BOTH the superseded row and its
// replacement are still current, and nothing in the data says which is which.
// (The emitter would have to write a stale marker for the old one to drop out
// at once, and remote write does not carry them.)
//
// So the tie-break makes no claim to be right; it only makes the answer the
// SAME every time. Taking whichever row Prometheus happened to return first
// let one dashboard describe snmp_chassisType as a Cisco chassis and the next
// reload of the same page describe it as a Dell iDRAC one. Ordering on the
// provenance labels means an operator sees one answer, consistently, until the
// window passes and the superseded row falls out on its own.
func rowOrder(e CatalogEntry) string {
	// Every field a catalog row carries, because every one of them is a LABEL
	// and so any of them changing is what made the second series in the first
	// place. Ordering on provenance alone left two rows that differ only in
	// their unit -- the exact shape of a reload that corrects one -- with equal
	// keys and the arrival-order tie-break back underneath.
	//
	// Length-prefixed so the encoding is injective: label values may contain
	// any byte, so no separator can be reserved, and "a" + "bc" would otherwise
	// order identically to "ab" + "c".
	var key strings.Builder
	for _, field := range []string{
		e.Module, e.Object, e.OID, e.Unit, e.Kind, e.Description,
		e.Identity, e.IdentityInfo, strings.Join(e.IndexLabels, ","),
	} {
		key.WriteString(strconv.Itoa(len(field)))
		key.WriteByte(':')
		key.WriteString(field)
	}
	return key.String()
}

// attachEnums folds the enumeration rows into their metrics.
//
// They are a separate family so that a metric with forty enum values costs
// forty small series rather than one series with a forty-value label, which
// would have to be re-sent whole on every emission.
//
// Enum rows need the same tie-break as the catalog rows and did not have it. A
// MIB reload that renames value 1 from "up" to "active" writes a different
// series -- the label is part of the identity -- so both sit in the wide
// window and arrival order decided which one a state panel showed. Laying the
// current read over the retained one resolves it per value: the renamed label
// is the one still being written.
//
// A value the reload REMOVED still lingers, supplied by the retained read
// until the window passes. That is the same bargain the lookback makes
// everywhere else, and it is the safe direction: a decode nobody sends any
// more is inert, where dropping the retained rows would blank a working
// enumeration whenever an emission was late.
func attachEnums(entries map[string]CatalogEntry, retained, current map[string]map[string]string) {
	for name, values := range current {
		if retained[name] == nil {
			retained[name] = values
			continue
		}
		for value, label := range values {
			retained[name][value] = label
		}
	}

	for name, values := range retained {
		// An enumeration row must not invent a metric. It describes how to
		// read one, and a metric with no catalog row has nothing to read.
		entry, ok := entries[name]
		if !ok {
			continue
		}
		entry.Enum = values
		entries[name] = entry
	}
}

// enumRows reads one enumeration query into metric -> value -> label.
func (s *Server) enumRows(ctx context.Context, query string) map[string]map[string]string {
	rows, err := s.promClient.Query(ctx, query)
	if err != nil {
		log.Printf("WARNING: could not read catalog enumerations (%s): %v", query, err)
		return map[string]map[string]string{}
	}

	decodes := make(map[string]map[string]string)
	for _, row := range rows {
		name := row.Metric[catMetric]
		value, label := row.Metric[catEnumValue], row.Metric[catEnumLabel]
		if name == "" || value == "" || label == "" {
			continue
		}
		values := decodes[name]
		if values == nil {
			values = map[string]string{}
			decodes[name] = values
		}
		// Ordered rather than first-seen, for the reason rowOrder gives.
		if prev, seen := values[value]; !seen || label < prev {
			values[value] = label
		}
	}
	return decodes
}

// splitLabels parses the comma-separated index label tuple.
func splitLabels(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
