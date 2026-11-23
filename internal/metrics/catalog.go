package metrics

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/mib"
)

// The metric catalog is how a consumer learns what a series means without
// knowing anything about MIBs.
//
// Remote write cannot carry metric metadata. The v1 protocol this exporter
// speaks has a Metadata field on the request, but Prometheus's v1 receive path
// never reads it -- only the v2 path calls UpdateMetadata, and only when the
// receiver runs the experimental metadata-wal-records feature. monitor-01's
// Prometheus 3.14.0 runs neither. So HELP and TYPE cannot reach a consumer at
// all, and everything a renderer needs has to travel as ordinary series.
//
// That is what this is: two families of constant-1 info series describing the
// metrics this collector publishes. A dashboard reads them and nothing else --
// no MIB files, no parser, no search path, no coupling to how collection
// works. Which is the point: the UI was reaching into the MIB store to render,
// and a UI that parses MIBs is a UI that breaks when the MIB store moves.
//
// The catalog is deliberately global. It carries no device label, so its size
// is the number of distinct metric names published across the fleet and not a
// multiple of the fleet: 20,390 series measured over 198 modules, identical at
// five devices and at a hundred thousand. A device dimension here would make
// the description of a metric cost more than the metric.

// Catalog family metric names.
const (
	// catalogMetricName describes one published metric per series.
	catalogMetricName = "snmp_metric_catalog"

	// catalogEnumMetricName maps one enumeration value per series, kept
	// apart from the catalog so that a metric with fifteen enum values does
	// not need fifteen labels on one row.
	catalogEnumMetricName = "snmp_metric_enum"
)

// Catalog label names.
const (
	labelCatalogMetric       = "metric"
	labelCatalogModule       = "module"
	labelCatalogObject       = "object"
	labelCatalogUnit         = "unit"
	labelCatalogKind         = "kind"
	labelCatalogDescription  = "description"
	labelCatalogIdentity     = "identity_label"
	labelCatalogIdentityInfo = "identity_info_metric"
	labelCatalogIndexLabels  = "index_labels"
	labelCatalogEnumValue    = "value"
	labelCatalogEnumLabel    = "label"
)

// MetricKind is how a renderer should draw a metric. It is the one field a
// renderer may rely on: unit is declared by fewer than a fifth of MIB objects,
// but kind is derivable for every object whose type resolved.
type MetricKind string

// The kinds a published metric can have.
const (
	// MetricKindCounter is monotonically increasing, to be drawn as a rate.
	MetricKindCounter MetricKind = "counter"

	// MetricKindGauge is a value that moves in both directions.
	MetricKindGauge MetricKind = "gauge"

	// MetricKindEnum is an integer standing for a named state. Its meanings
	// are published as snmp_metric_enum rows.
	MetricKindEnum MetricKind = "enum"

	// MetricKindString is text, published as an info metric carrying the
	// text in a label rather than as a number.
	MetricKindString MetricKind = "string"

	// MetricKindUnknown is a metric whose type could not be determined.
	//
	// This is a diagnosis, not a default. An object gets it when its MIB
	// module parsed but gosmi resolved no type node -- which happens when a
	// module the MIB IMPORTS from is absent, and which silently costs the
	// object its units, display hint and enumeration in the same stroke
	// (internal/mib/parser/parser.go:314). Measured at 730 of 9,638 pollable
	// objects across 198 modules, entirely from modules with unresolved
	// imports. Publishing it as a countable value is how that becomes
	// visible rather than being papered over with a guess.
	MetricKindUnknown MetricKind = "unknown"
)

// CatalogEntry describes one published metric.
//
// The primary key is Metric -- the name as published -- and not the MIB object
// it came from. That distinction is load-bearing: entity sensor readings are
// published under a name derived from their unit (snmp_entity_sensor_dbm),
// because one metric holding dBm and celsius and volts cannot be aggregated.
// No MIB object is called entity_sensor_dbm, so a catalog keyed on object name
// could not describe the readings this collector exists to produce.
type CatalogEntry struct {
	// Metric is the published metric name, and the key consumers join on.
	Metric string

	// Module and Object are provenance. They are empty for a metric this
	// collector derives rather than reads, which is not a gap: a derived
	// metric's meaning is in Kind and Unit, and its MIB object would name a
	// raw column that is deliberately not published.
	Module string
	Object string
	OID    string

	// Unit is decorative and must never be load-bearing. Fewer than a fifth
	// of MIB objects declare UNITS (1,821 of 9,638 measured), so a renderer
	// that keys an axis on it is blank four times in five. An empty unit
	// renders a unitless axis; it never suppresses a panel.
	Unit string

	// Kind is what a renderer keys on.
	Kind MetricKind

	// Description is the MIB DESCRIPTION, carried in full.
	Description string

	// IdentityLabel names the label on the series itself that identifies one
	// line of a panel, when there is one. Entity sensor readings carry
	// entphysicalname directly, so theirs is set.
	//
	// IdentityInfoMetric is the other case, and the common one: for an
	// ordinary table column the series carries only its index labels, and
	// the human-readable name lives in a separate info metric that has to be
	// joined on those indexes. snmp_ifInOctets has ifindex and no ifname;
	// the name is on snmp_ifname_info.
	//
	// Both are published because both are real. Which of them a renderer
	// uses is not a preference: it is whether the name is on the series or
	// one join away.
	IdentityLabel      string
	IdentityInfoMetric string

	// IndexLabels are the row's index labels, in INDEX-clause order.
	IndexLabels []string

	// EnumValues are the enumeration's meanings, published as separate
	// snmp_metric_enum rows.
	EnumValues map[int64]string
}

// classifyKind decides how a renderer should draw an object.
//
// The order matters and each step is a fact rather than a guess:
//
//   - No resolved type at all means the module's imports did not resolve, and
//     nothing else about the object is trustworthy either. It is checked first
//     so that a later rule cannot make something up from the wreckage.
//   - An enumeration is an enumeration whatever its underlying integer type,
//     so the presence of enum values decides before the type name does.
//   - Counter-ness is the project's existing definition (mib.IsCounter), which
//     includes TimeTicks. Reusing it keeps the catalog from disagreeing with
//     the rest of the codebase about what a counter is.
//   - Text is text. The set here is wider than mib.IsString, which covers only
//     OctetString and DisplayString: a catalog that called SnmpAdminString a
//     gauge would have a renderer draw a line through a hostname.
//
// Everything remaining is a gauge, which is the correct default for a numeric
// SNMP object and the only one of these branches that is a default at all.
func classifyKind(object *mib.OIDMetadata) MetricKind {
	if object == nil || object.Type == "" {
		return MetricKindUnknown
	}
	if len(object.EnumValues) > 0 {
		return MetricKindEnum
	}
	if object.IsCounter() {
		return MetricKindCounter
	}
	if isTextualType(object.Type) {
		return MetricKindString
	}
	return MetricKindGauge
}

// textualTypes are the resolved type names whose values are text or an address
// rendered as text rather than a number to plot.
var textualTypes = map[string]bool{
	"OctetString":      true,
	"DisplayString":    true,
	"SnmpAdminString":  true,
	"PhysAddress":      true,
	"MacAddress":       true,
	"InetAddress":      true,
	"IpAddress":        true,
	"Ipv6Address":      true,
	"ObjectIdentifier": true,
	"Bits":             true,
	"Opaque":           true,
	"Utf8String":       true,
}

// isTextualType reports whether a resolved MIB type carries text.
func isTextualType(typeName string) bool {
	return textualTypes[typeName]
}

// IndexLabelsValue renders the index tuple as one label value, in INDEX-clause
// order, which is the order the sub-identifiers appear in the OID. Order is
// part of the meaning, so it is preserved rather than sorted.
func (e *CatalogEntry) IndexLabelsValue() string {
	return strings.Join(e.IndexLabels, ",")
}

// sortedEnumValues returns the enumeration in ascending numeric order, so that
// two collectors given the same MIBs publish the same rows in the same order.
func (e *CatalogEntry) sortedEnumValues() []int64 {
	values := make([]int64, 0, len(e.EnumValues))
	for value := range e.EnumValues {
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return values
}

// catalogIndex describes every metric the loaded MIBs could produce, keyed by
// published metric name.
//
// It is built from the same sweep of the MIB store that builds the metric name
// index, so it costs no extra scan, and it is immutable once built.
type catalogIndex struct {
	entries map[string]*CatalogEntry

	// collisions counts objects discarded because an earlier object already
	// claimed their metric name. 436 of 9,638 objects collide across 198
	// modules, so this is a routine condition rather than a fault, but a
	// catalog row describes one of them and an operator reading a
	// description for the wrong module has no way to tell.
	collisions int
}

// emptyCatalogIndex is what an unbuilt index reads as.
var emptyCatalogIndex = &catalogIndex{entries: map[string]*CatalogEntry{}}

// entryFor returns the catalog entry for a published metric name.
func (ix *catalogIndex) entryFor(metric string) (*CatalogEntry, bool) {
	if ix == nil {
		return nil, false
	}
	entry, found := ix.entries[metric]
	return entry, found
}

// newCatalogIndex builds the catalog from the objects a MIB store sweep found.
//
// First definition wins, which is not an arbitrary tie-break: objectNameIndex
// resolves an instance OID to a metric name by the same rule, so a catalog
// that chose differently would describe a metric under one module's
// DESCRIPTION while the series was named from another's. The two have to agree
// or the catalog documents a metric that is not there.
//
// Objects that are not instantiable are skipped for the same reason the name
// index skips them: a table node or a conceptual row is never published, so a
// catalog row for one describes nothing.
func newCatalogIndex(objects []*mib.OIDMetadata, tables *tableSet, statusColumns map[string][]statusColumnInfo) *catalogIndex {
	index := &catalogIndex{entries: make(map[string]*CatalogEntry, len(objects))}

	// The same objects the metric name index names samples after, filtered by
	// the same rule from the same slice, so the two cannot drift apart about
	// which objects exist.
	for _, object := range instantiableObjects(objects) {
		for _, published := range publishedNamesFor(object) {
			if _, taken := index.entries[published.metric]; taken {
				index.collisions++
				continue
			}
			index.entries[published.metric] = catalogEntryFor(published, object, tables, statusColumns)
		}
	}

	return index
}

// publishedName is one of the metric names an object reaches Prometheus under.
type publishedName struct {
	metric string

	// info is true when the series is an info metric: a constant 1 whose
	// value is carried in a "value" label rather than in the sample.
	info bool
}

// publishedNamesFor returns every metric name an object can be published
// under, which is not always one and is not always the obvious one.
//
// This is the difference between describing a metric and describing a MIB
// object, and getting it wrong left an entire category undescribed. The
// exporter publishes:
//
//   - a numeric object under snmp_<Name>, preserving case;
//   - a text object as an info metric under snmp_<name>_info, lowercased,
//     because its value is a string and cannot be a sample. It never reaches
//     the numeric path at all, so snmp_ifName is a name nothing ever writes;
//   - a status column under BOTH, when its value is numeric: the number is
//     published as a reading and the same column is published again as an info
//     metric carrying its rendered label, so ifOperStatus is both
//     snmp_ifOperStatus and snmp_ifoperstatus_info.
//
// Keying the catalog on snmp_<Name> alone therefore missed every _info family
// -- every text scalar and every status column -- while filing entries under
// names nothing publishes. Those entries were never emitted, because emission
// is driven by the published set, so the damage was one-directional: the
// families that exist had no description, and identity_info_metric pointed at
// a metric the catalog could not describe.
func publishedNamesFor(object *mib.OIDMetadata) []publishedName {
	numeric := publishedName{metric: metricNamePrefix + SanitizeMetricName(object.Name)}
	info := publishedName{metric: infoMetricName(object.Name), info: true}

	// A text value cannot be a sample, so the numeric name is never written.
	if classifyKind(object) == MetricKindString {
		return []publishedName{info}
	}

	// A status column is published twice: once as its number, once as the
	// label that number renders to.
	if object.ObjectClass == mib.ObjectClassStatusColumn {
		return []publishedName{numeric, info}
	}

	return []publishedName{numeric}
}

// catalogEntryFor describes one MIB object as one of the metrics it is
// published as.
func catalogEntryFor(
	published publishedName,
	object *mib.OIDMetadata,
	tables *tableSet,
	statusColumns map[string][]statusColumnInfo,
) *CatalogEntry {
	entry := &CatalogEntry{
		Metric:      published.metric,
		Module:      object.Module,
		Object:      object.Name,
		OID:         object.OID,
		Unit:        object.Units,
		Kind:        classifyKind(object),
		Description: object.Description,
		EnumValues:  object.EnumValues,
	}

	// An info metric carries its value in a label, so it is a string series
	// whatever the underlying object's type: a renderer drawing a line
	// through snmp_ifoperstatus_info would be plotting the constant 1. The
	// number, where there is one, is the object's other published name.
	//
	// Its unit goes with the number for the same reason -- "seconds" on a
	// series whose value is always 1 describes nothing -- and its
	// enumeration goes too, because the info metric has already rendered the
	// enum to text in its value label.
	if published.info {
		entry.Kind = MetricKindString
		entry.Unit = ""
		entry.EnumValues = nil
	}

	// A scalar has no row to identify and no index: it is one value for the
	// device, and the device label already says which.
	if !object.IsColumn {
		return entry
	}

	// tableSet and statusColumns are both keyed by the normalized entry OID,
	// which carries a leading dot. Keying on the raw EntryOID here would miss
	// every table and leave every column without an index or an identity.
	entryOID := normalizeOID(object.EntryOID)
	if tables != nil {
		if table := tables.tables[entryOID]; table != nil {
			entry.IndexLabels = indexLabelNames(table)
		}
	}
	entry.IdentityInfoMetric = identityInfoMetric(statusColumns[entryOID])

	return entry
}

// indexLabelNames returns a table's index labels in INDEX-clause order, named
// as the exporter labels them.
func indexLabelNames(table *TableIndexInfo) []string {
	names := make([]string, 0, len(table.IndexNames))
	for _, name := range table.IndexNames {
		names = append(names, strings.ToLower(name))
	}
	return names
}

// identityInfoMetric names the family a renderer gets a row's name from.
//
// A table's status columns are the candidates -- ifName, ifDescr, ifAlias --
// and the MIB does not declare which of them names the row. The preference
// below is a presentation convention and is documented as one: a column whose
// name ends in "Name" is what an operator calls the thing, a description is the
// fallback, and an alias is the operator's own label and so the least likely to
// be populated.
//
// ENUMERATED columns are excluded outright, and that is not a preference. An
// enumeration has a handful of values by construction, so it cannot tell two
// rows apart -- it is a state, not a name. cbgpPeer2Table has no Name, Descr or
// Alias column, so the sorted-order fallback elected cbgpPeer2AdminStatus, and
// on core-01 every row of every panel in that table was labeled "start":
// eight BGP peers, eight lines, one name between them. An unnamed row falls
// back to its index, which is the peer's address and tells them apart exactly.
//
// Where no candidate survives, the answer is no identity at all rather than a
// bad one.
func identityInfoMetric(columns []statusColumnInfo) string {
	names := make([]string, 0, len(columns))
	for _, column := range columns {
		if column.Meta != nil && len(column.Meta.EnumValues) > 0 {
			continue
		}
		names = append(names, column.Name)
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)

	for _, suffix := range []string{"Name", "Descr", "Description", "Alias"} {
		for _, name := range names {
			if strings.HasSuffix(name, suffix) {
				return infoMetricName(name)
			}
		}
	}

	// Nothing declared itself a name, so this table has no identity. There
	// used to be a fallback here -- the first status column in sorted order --
	// and it was a guess dressed as an answer. With enumerations excluded it
	// elected cbgpPeer2LastError on cbgpPeer2Table, so every BGP peer on
	// core-01's dashboard was called "04:00": the hex rendering of a last
	// error code, identical across all eight rows.
	//
	// Returning nothing sends a renderer to the row's index instead, which for
	// that table is the peer's address. An index is never friendly and never
	// wrong, and "never wrong" is the property a label has to have before
	// "friendly" is worth anything.
	return ""
}

// derivedCatalogEntries describes the metrics this collector produces rather
// than reads.
//
// These are the reason the catalog is keyed on the published name. An entity
// sensor reading is published as snmp_entity_sensor_dbm, named for the unit it
// was scaled into, because one metric holding dBm and celsius and volts cannot
// be aggregated (internal/metrics/entity_sensor.go). There is no MIB object
// called entity_sensor_dbm, so nothing derived from a sweep of the MIB store
// can describe it -- and it is precisely the reading this collector exists to
// produce.
//
// They carry no Module or Object, and that is honest rather than incomplete:
// their provenance is two different MIB objects on two different tables
// (entPhySensorValue and entSensorValue), and naming either would be wrong on
// half the devices.
//
// Unit and Kind here come from the collector's own semantics, which is the
// other half of why unit coverage is better than the MIBs' 19%: the MIB
// declares no UNITS on entSensorValue, and the collector knows the unit
// exactly because it scaled the reading into it.
func derivedCatalogEntries() []*CatalogEntry {
	entries := make([]*CatalogEntry, 0, len(sensorUnitSuffixes)+2)

	// The device's own identity. It is derived rather than read: the system
	// group's objects are collapsed into one info series whose labels are
	// sysDescr, sysContact and the rest, so no MIB object is published under
	// this name and a sweep of the store cannot describe it.
	//
	// Undescribed, a consumer sees an unknown kind and draws a line through
	// the constant 1 -- one flat, meaningless panel on every module tab of
	// every device, because the family carries no module either. Measured on
	// core-01: ten tabs, ten of them.
	entries = append(entries, &CatalogEntry{
		Metric: systemInfoMetricName,
		Kind:   MetricKindString,
		Description: "The device's system group -- sysDescr, sysObjectID, sysContact, " +
			"sysName, sysLocation and sysServices -- collapsed into one series " +
			"carrying each as a label. There is no number to plot: every value is text.",
	})

	seen := make(map[string]bool, len(sensorUnitSuffixes)+1)
	for _, sensorType := range sortedSensorTypes() {
		metric := sensorType.MetricName()
		if seen[metric] {
			continue
		}
		seen[metric] = true

		entries = append(entries, &CatalogEntry{
			Metric:        metric,
			Unit:          sensorCatalogUnit(sensorType),
			Kind:          MetricKindGauge,
			Description:   sensorCatalogDescription(sensorType),
			IdentityLabel: labelEntityName,
			IndexLabels:   []string{labelEntityIndex},
		})
	}

	return entries
}

// sortedSensorTypes returns the declared sensor types in ascending order, so
// the derived entries are identical on every collector.
func sortedSensorTypes() []SensorType {
	types := make([]SensorType, 0, len(sensorTypeNames))
	for sensorType := range sensorTypeNames {
		types = append(types, sensorType)
	}
	sort.Slice(types, func(i, j int) bool { return types[i] < types[j] })
	return types
}

// sensorCatalogUnit is the unit a sensor type's readings are published in.
// It is the collector's own answer rather than the MIB's, because the
// collector is what scaled the reading into that unit.
var sensorCatalogUnits = map[SensorType]string{
	SensorTypeVoltsAC:   "volts",
	SensorTypeVoltsDC:   "volts",
	SensorTypeAmperes:   "amperes",
	SensorTypeWatts:     "watts",
	SensorTypeHertz:     "hertz",
	SensorTypeCelsius:   "celsius",
	SensorTypePercentRH: "percent",
	SensorTypeRPM:       "rpm",
	SensorTypeCMM:       "cubic meters per minute",
	SensorTypeDBm:       "dBm",
	SensorTypeDB:        "dB",
}

func sensorCatalogUnit(sensorType SensorType) string {
	return sensorCatalogUnits[sensorType]
}

// sensorCatalogDescription says what an entity sensor metric holds, in the
// terms an operator reading a panel would want. There is no MIB DESCRIPTION to
// carry: the MIB describes entSensorValue, an integer that is not the reading.
func sensorCatalogDescription(sensorType SensorType) string {
	if unit := sensorCatalogUnits[sensorType]; unit != "" {
		return "Physical sensor readings in " + unit +
			", scaled from ENTITY-SENSOR-MIB or CISCO-ENTITY-SENSOR-MIB by the sensor's" +
			" own scale and precision. Identified by entphysicalname."
	}
	return "Physical sensor readings whose EntitySensorDataType names no unit," +
		" scaled by the sensor's own scale and precision."
}

// catalogSamples renders entries as the series a consumer reads.
//
// Every sample is a constant 1: an info metric says nothing with its value,
// and everything it carries is in its labels. The timestamp is the caller's,
// so one emission writes one timestamp across the whole catalog rather than
// smearing it across however long the render took.
func catalogSamples(entries []*CatalogEntry, timestamp time.Time) []*Sample {
	samples := make([]*Sample, 0, len(entries))

	for _, entry := range entries {
		labels := map[string]string{
			labelCatalogMetric: entry.Metric,
			labelCatalogKind:   string(entry.Kind),
		}
		setIfNotEmpty(labels, labelCatalogModule, entry.Module)
		setIfNotEmpty(labels, labelCatalogObject, entry.Object)
		setIfNotEmpty(labels, labelOID, entry.OID)
		setIfNotEmpty(labels, labelCatalogUnit, entry.Unit)
		setIfNotEmpty(labels, labelCatalogDescription, entry.Description)
		setIfNotEmpty(labels, labelCatalogIdentity, entry.IdentityLabel)
		setIfNotEmpty(labels, labelCatalogIdentityInfo, entry.IdentityInfoMetric)
		setIfNotEmpty(labels, labelCatalogIndexLabels, entry.IndexLabelsValue())

		samples = append(samples, &Sample{
			Name:      catalogMetricName,
			Labels:    labels,
			Value:     infoMetricValue,
			Timestamp: timestamp,
		})

		samples = append(samples, enumSamples(entry, timestamp)...)
	}

	return samples
}

// enumSamples renders one metric's enumeration as one series per value.
//
// They are a separate family rather than labels on the catalog row because an
// enumeration can carry dozens of values, and a row whose label set grows with
// the MIB is a row that eventually cannot be written.
func enumSamples(entry *CatalogEntry, timestamp time.Time) []*Sample {
	if len(entry.EnumValues) == 0 {
		return nil
	}

	samples := make([]*Sample, 0, len(entry.EnumValues))
	for _, value := range entry.sortedEnumValues() {
		samples = append(samples, &Sample{
			Name: catalogEnumMetricName,
			Labels: map[string]string{
				labelCatalogMetric:    entry.Metric,
				labelCatalogEnumValue: strconv.FormatInt(value, 10),
				labelCatalogEnumLabel: entry.EnumValues[value],
			},
			Value:     infoMetricValue,
			Timestamp: timestamp,
		})
	}
	return samples
}

// setIfNotEmpty adds a label only when it carries something. An empty
// Prometheus label cannot be told from an absent one, so writing it would
// widen the series for nothing.
func setIfNotEmpty(labels map[string]string, name, value string) {
	if value != "" {
		labels[name] = value
	}
}
