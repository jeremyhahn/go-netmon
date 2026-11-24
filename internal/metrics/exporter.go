// Package metrics turns SNMP readings into Prometheus series: MIB-derived names and index labels, scaled sensors, and the metric catalog.
package metrics

import (
	"context"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/snmp"
)

// Sample represents a single metric sample
type Sample struct {
	// Metric name
	Name string

	// Labels for this metric
	Labels map[string]string

	// Value of the metric
	Value float64

	// Timestamp when the metric was collected
	Timestamp time.Time
}

// Exporter defines the interface for metrics exporters
type Exporter interface {
	// Export sends metrics to the backend
	Export(ctx context.Context, samples []*Sample) error

	// ExportSNMPResults converts SNMP results to samples and exports them
	ExportSNMPResults(ctx context.Context, deviceName string, results []*snmp.Result, labels map[string]string) error

	// Flush ensures all buffered metrics are sent
	Flush(ctx context.Context) error

	// Close closes the exporter and releases resources
	Close() error
}

// MIBMetadataRefresher is an optional capability of exporters whose names derive from a changing MIB store.
type MIBMetadataRefresher interface {
	// RefreshMIBMetadata rebuilds the exporter's MIB-derived state from the
	// current contents of its MIB store.
	RefreshMIBMetadata() error
}

// DeviceForgetter is an optional capability of exporters that keep per-device state, called when a device is retired.
type DeviceForgetter interface {
	// ForgetDevice drops everything the exporter holds for the device. It is
	// called once nothing more can be exported for it.
	ForgetDevice(deviceName string)
}

// systemInfoOIDs maps well-known system OIDs to their label names for info metrics
var systemInfoOIDs = map[string]string{
	".1.3.6.1.2.1.1.1.0": "sys_descr",     // sysDescr
	"1.3.6.1.2.1.1.1.0":  "sys_descr",     // sysDescr (without leading dot)
	".1.3.6.1.2.1.1.2.0": "sys_object_id", // sysObjectID
	"1.3.6.1.2.1.1.2.0":  "sys_object_id", // sysObjectID (without leading dot)
	".1.3.6.1.2.1.1.4.0": "sys_contact",   // sysContact
	"1.3.6.1.2.1.1.4.0":  "sys_contact",   // sysContact (without leading dot)
	".1.3.6.1.2.1.1.5.0": "sys_name",      // sysName
	"1.3.6.1.2.1.1.5.0":  "sys_name",      // sysName (without leading dot)
	".1.3.6.1.2.1.1.6.0": "sys_location",  // sysLocation
	"1.3.6.1.2.1.1.6.0":  "sys_location",  // sysLocation (without leading dot)
}

// ConvertSNMPResultsToSamples converts SNMP results to raw-OID-named samples; use ConvertSNMPResultsWithTableIndexes for Prometheus.
func ConvertSNMPResultsToSamples(deviceName string, results []*snmp.Result, extraLabels map[string]string) []*Sample {
	samples := make([]*Sample, 0, len(results))
	systemInfo := make(map[string]string) // Collect system text values

	for _, result := range results {
		// Try numeric value first
		value, err := result.GetFloat64Value()
		if err != nil {
			// Not a reading. It may still be one of the system scalars that
			// snmp_system_info reports as labels.
			recordSystemInfo(systemInfo, result)
			continue
		}

		labels := newInstanceLabels(deviceName, result.OID)
		mergeExtraLabels(deviceName, labels, extraLabels)

		metricName, _ := resolveMetricName(nil, result.OID)

		samples = append(samples, &Sample{
			Name:      metricName,
			Labels:    labels,
			Value:     value,
			Timestamp: result.Timestamp,
		})
	}

	if len(systemInfo) > 0 {
		samples = append(samples, newSystemInfoSample(deviceName, systemInfo, extraLabels, pollTimestamp(results)))
	}

	return samples
}

// SanitizeMetricName converts an OID or name to a valid Prometheus metric name
// Prometheus metric names must match [a-zA-Z_:][a-zA-Z0-9_:]*
func SanitizeMetricName(name string) string {
	runes := []rune(name)
	result := make([]rune, 0, len(runes))

	for i, r := range runes {
		if i == 0 {
			// First character must be [a-zA-Z_:]
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' || r == ':' {
				result = append(result, r)
			} else {
				result = append(result, '_')
			}
		} else {
			// Other characters can be [a-zA-Z0-9_:]
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == ':' {
				result = append(result, r)
			} else {
				result = append(result, '_')
			}
		}
	}

	return string(result)
}
