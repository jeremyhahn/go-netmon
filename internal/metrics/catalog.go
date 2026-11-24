package metrics

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/mib"
)

// The metric catalog: global constant-1 info series describing every published metric, since remote write v1 carries no metadata.

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

	// MetricKindUnknown marks an object whose type did not resolve, usually from missing IMPORTS.
	MetricKindUnknown MetricKind = "unknown"
)

// CatalogEntry describes one published metric, keyed by published name rather than MIB object.
type CatalogEntry struct {
	// Metric is the published metric name, and the key consumers join on.
	Metric string

	// Module and Object are provenance, empty for derived metrics.
	Module string
	Object string
	OID    string

	// Unit is decorative; most objects declare no UNITS.
	Unit string

	// Kind is what a renderer keys on.
	Kind MetricKind

	// Description is the MIB DESCRIPTION, carried in full.
	Description string

	// IdentityLabel is a name label on the series itself; IdentityInfoMetric is the info metric to join on the index labels for the name.
	IdentityLabel      string
	IdentityInfoMetric string

	// IndexLabels are the row's index labels, in INDEX-clause order.
	IndexLabels []string

	// EnumValues are the enumeration's meanings, published as separate
	// snmp_metric_enum rows.
	EnumValues map[int64]string
}

// classifyKind decides how to draw an object: unresolved, enum, counter (mib.IsCounter), text, else gauge.
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

// IndexLabelsValue renders the index tuple as one label value in INDEX-clause order.
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

// catalogIndex describes every metric the loaded MIBs could produce, keyed by published name; immutable once built.
type catalogIndex struct {
	entries map[string]*CatalogEntry

	// collisions counts objects dropped because an earlier object claimed their metric name.
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

// newCatalogIndex builds the catalog from a MIB store sweep: first definition wins, as in objectNameIndex; non-instantiable objects are skipped.
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

// publishedNamesFor returns every name an object is published under: snmp_<Name> for numbers, snmp_<name>_info for text, both for numeric status columns.
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

	// An info metric is a string series; its unit and enumeration belong to the numeric family.
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

// indexLabelNames returns a table's index labels in INDEX-clause order, as the exporter names them.
func indexLabelNames(table *TableIndexInfo) []string {
	names := make([]string, 0, len(table.IndexNames))
	for _, name := range table.IndexNames {
		names = append(names, strings.ToLower(name))
	}
	return names
}

// identityInfoMetric picks the family that names a row: *Name, then description, then alias; enumerated columns never qualify.
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

	// No name column: no identity, so a renderer falls back to the row index.
	return ""
}

// derivedCatalogEntries describes the metrics this collector produces rather than reads, such as unit-named entity sensor readings.
func derivedCatalogEntries() []*CatalogEntry {
	entries := make([]*CatalogEntry, 0, len(sensorUnitSuffixes)+2)

	// The device identity family snmp_system_info is derived, so it is described here.
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

// sensorCatalogDescription says what an entity sensor metric holds.
func sensorCatalogDescription(sensorType SensorType) string {
	if unit := sensorCatalogUnits[sensorType]; unit != "" {
		return "Physical sensor readings in " + unit +
			", scaled from ENTITY-SENSOR-MIB or CISCO-ENTITY-SENSOR-MIB by the sensor's" +
			" own scale and precision. Identified by entphysicalname."
	}
	return "Physical sensor readings whose EntitySensorDataType names no unit," +
		" scaled by the sensor's own scale and precision."
}

// catalogSamples renders entries as constant-1 series stamped with the caller's timestamp.
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
