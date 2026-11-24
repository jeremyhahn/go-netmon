// Package promname is the single derivation of the Prometheus names the SNMP exporter emits; contract_test.go pins it to the exporter.
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

// OIDLabel carries a sample's full instance OID.
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

// InfoMetricName returns a status column's info metric name: the column name lowercased, nothing else changed.
func InfoMetricName(columnName string) string {
	return Prefix + strings.ToLower(columnName) + "_info"
}

// IndexLabel returns the label for an index column's value: the column name lowercased.
func IndexLabel(columnName string) string {
	return strings.ToLower(columnName)
}

// IsInfoMetric reports whether a published name is an info metric: constant 1 with its text in a label.
func IsInfoMetric(metric string) bool {
	return strings.HasPrefix(metric, Prefix) && strings.HasSuffix(metric, "_info")
}

// IdentityLabel returns the label on a table's readings that also carries an info metric's value.
func IdentityLabel(infoMetric string) string {
	// Both prefix and suffix must be present.
	if !strings.HasPrefix(infoMetric, Prefix) || !strings.HasSuffix(infoMetric, "_info") {
		return ""
	}
	return strings.TrimSuffix(strings.TrimPrefix(infoMetric, Prefix), "_info")
}

// RawOIDMetricName returns the exporter's fallback name for an instance OID no loaded MIB describes.
func RawOIDMetricName(instanceOID string) string {
	return metrics.SanitizeMetricName(Prefix + instanceOID)
}

// EntitySensorMetricName returns the unit-derived metric name for an entity sensor of the given EntitySensorDataType.
func EntitySensorMetricName(sensorType int32) string {
	return metrics.SensorType(sensorType).MetricName()
}

// Labels an entity sensor sample carries beyond DeviceLabel and OIDLabel.
const (
	// EntityIndexLabel carries the entPhysicalIndex the reading was read at.
	EntityIndexLabel = "entphysicalindex"

	// EntityNameLabel carries entPhysicalName, else entPhysicalDescr; absent without ENTITY-MIB.
	EntityNameLabel = "entphysicalname"

	// SensorTypeLabel carries the EntitySensorDataType label, e.g. "dBm".
	SensorTypeLabel = "sensor_type"
)
