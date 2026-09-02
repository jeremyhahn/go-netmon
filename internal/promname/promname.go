// Package promname builds the Prometheus identifiers the SNMP exporter emits.
//
// The exporter names a sample after the MIB object it instantiates and puts the
// row index in labels (internal/metrics/name_index.go). Every consumer that has
// to name a series it did not produce — a dashboard generator, a query builder,
// a CLI renderer — has to arrive at exactly the same string, and each one
// deriving it independently is what let 36,654 raw-OID metric names survive in
// four packages after the exporter stopped emitting them.
//
// This package is that single derivation. It is deliberately thin: it composes
// the exporter's own metrics.SanitizeMetricName so the character rules cannot
// drift, and internal/promname/contract_test.go pins its output to what the
// exporter actually emits for a live sample, so a change on either side fails
// the build rather than silently emptying every dashboard panel.
package promname

import (
	"strings"

	"github.com/jeremyhahn/go-netmon/internal/metrics"
)

// Prefix is prepended to the name of every metric derived from an SNMP object.
// It matches metrics.metricNamePrefix.
const Prefix = "snmp_"

// DeviceLabel carries the configured name of the polled device.
const DeviceLabel = "device"

// OIDLabel carries the full instance OID a sample was read from: the object's
// OID followed by ".0" for a scalar or by the row index for a table column. It
// is the only label that identifies a sample's instance exactly, which is what
// makes it the right thing to resolve a series back to a MIB object with.
const OIDLabel = "oid"

// NameLabel is the label Prometheus keeps a metric's name in.
const NameLabel = "__name__"

// MetricName returns the metric name the exporter gives every instance of the
// MIB object called objectName. The row index is not part of it: it is carried
// in labels, so one object is one metric name however many rows its table has.
func MetricName(objectName string) string {
	return NameWithPrefix(Prefix, objectName)
}

// NameWithPrefix is MetricName with a caller-chosen prefix, for the dashboard
// builder's configurable MetricPrefix. An empty prefix means the default.
func NameWithPrefix(prefix, objectName string) string {
	if prefix == "" {
		prefix = Prefix
	}
	return prefix + metrics.SanitizeMetricName(objectName)
}

// InfoMetricName returns the name of the info metric the exporter emits for a
// status column: the column's value is not a number, so it is published as a
// label on a constant-1 series named after the column, lowercased
// (internal/metrics/table_index_cache.go).
//
// The exporter lowercases the column name and nothing else, so this does the
// same. Matching it exactly is the whole point -- a consumer that sanitized
// where the exporter does not would query a name no series has.
func InfoMetricName(columnName string) string {
	return Prefix + strings.ToLower(columnName) + "_info"
}

// IndexLabel returns the label the exporter puts an index column's decoded
// value in. Index and status column names reach Prometheus lowercased and
// otherwise untouched, so ifIndex is read back as "ifindex" and
// cbgpPeer2RemoteAddr as "cbgppeer2remoteaddr".
func IndexLabel(columnName string) string {
	return strings.ToLower(columnName)
}

// IsInfoMetric reports whether a published name is an info metric: a constant 1
// whose reading is text carried in a label.
//
// This reads the exporter's naming CONTRACT rather than guessing from a name.
// InfoMetricName is the only thing that produces this shape, and it produces it
// for exactly one reason -- the value is text and cannot be a sample -- so the
// suffix is a fact about how the family is published, not a hint.
//
// It is what lets a consumer draw an UNDESCRIBED info metric correctly. A
// metric with no catalog row has no declared kind, and the honest default for
// an unknown kind is a line; but a line through a family that is always 1 is a
// flat line that says nothing, and the reading is sitting in a label beside it.
// Measured on ci-node-1: thirteen of them, including the device's own name and
// firmware version, drawn as flat lines.
func IsInfoMetric(metric string) bool {
	return strings.HasPrefix(metric, Prefix) && strings.HasSuffix(metric, "_info")
}

// IdentityLabel returns the label an info metric's value ALSO appears in, on
// the readings of the table it belongs to.
//
// The exporter promotes a table's status columns onto its readings as
// friendly-name labels, so snmp_ifInOctets arrives already carrying
// ifdescr="GigabitEthernet0/0/0" alongside its ifindex. The same value is
// published separately as snmp_ifdescr_info, and the catalog names that family
// as where a row's identity lives -- true, but not the cheapest place to get it
// from, because a consumer holding the reading already has the label.
//
// The two names differ only by the prefix and the suffix, since both are the
// column name lowercased: see InfoMetricName and IndexLabel. That is what makes
// this derivation exact rather than a guess, and
// TestIdentityLabelRoundTripsWithTheExporter pins it.
func IdentityLabel(infoMetric string) string {
	// Both ends have to be there. Trimming whichever one is present would turn
	// snmp_ifInOctets -- a reading, not an info metric -- into "ifInOctets",
	// and hand a renderer a label no series carries while looking like an
	// answer.
	if !strings.HasPrefix(infoMetric, Prefix) || !strings.HasSuffix(infoMetric, "_info") {
		return ""
	}
	return strings.TrimSuffix(strings.TrimPrefix(infoMetric, Prefix), "_info")
}

// RawOIDMetricName returns the name the exporter falls back to for an instance
// OID that no loaded MIB describes. It is the only case in which a metric name
// still carries an OID, and the only case in which one metric name means one
// table row.
func RawOIDMetricName(instanceOID string) string {
	return metrics.SanitizeMetricName(Prefix + instanceOID)
}

// EntitySensorMetricName returns the metric name the exporter publishes
// readings of an entity sensor under, given the sensor's EntitySensorDataType.
//
// Entity sensor readings are the one family whose name is not derived from the
// MIB object that produced it. entPhySensorValue and entSensorValue each carry
// every sensor a device has -- optical power, temperature, voltage, current,
// fan speed -- as bare integers in whatever scale each sensor chose, so naming
// their samples after the column would put five units in one metric and no
// unit in its name. The exporter scales each reading into its base unit and
// names it after that unit instead (internal/metrics/entity_sensor.go), which
// is why a consumer needs this rather than MetricName.
func EntitySensorMetricName(sensorType int32) string {
	return metrics.SensorType(sensorType).MetricName()
}

// The labels an entity sensor sample carries beyond DeviceLabel and OIDLabel.
//
// EntityIndexLabel and EntityNameLabel follow the index label convention --
// the MIB object's own name, lowercased -- so they are what IndexLabel would
// return for entPhysicalIndex and entPhysicalName. SensorTypeLabel does not
// name a MIB object, because the object is entPhySensorType in
// ENTITY-SENSOR-MIB and entSensorType in CISCO-ENTITY-SENSOR-MIB and a device
// may answer either.
const (
	// EntityIndexLabel carries the entPhysicalIndex the reading was read at.
	EntityIndexLabel = "entphysicalindex"

	// EntityNameLabel carries the sensor's entPhysicalName, falling back to
	// its entPhysicalDescr. It is absent when the device was polled without
	// ENTITY-MIB, since an empty label and a missing one are the same series.
	EntityNameLabel = "entphysicalname"

	// SensorTypeLabel carries the EntitySensorDataType label, e.g. "dBm".
	SensorTypeLabel = "sensor_type"
)
