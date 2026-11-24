package metrics

import (
	"strings"

	"github.com/jeremyhahn/go-netmon/internal/snmp"
)

// Text-valued scalars (e.g. PSU or fan status sentences) are emitted as snmp_<object>_info with the text in a value label.

// textScalarSample builds the info metric for a text scalar, using the raw-OID name when no MIB describes it, or returns nil.
func textScalarSample(
	deviceName string,
	result *snmp.Result,
	index *mibIndex,
	extraLabels map[string]string,
) *Sample {
	// A varbind the device declined to answer carries no value to report, and
	// recording "NoSuchObject" as a health string would read as a reading.
	if !result.HasValue() {
		return nil
	}

	// Columns are handled by ConvertStatusColumnsToInfoMetrics, which also has
	// the row index to label with. Emitting here as well would double them.
	if index != nil && index.tableFor(result.OID) != nil {
		return nil
	}

	// The system group is already reported as labels on snmp_system_info, so a
	// second copy per object would say the same thing at more cost.
	if _, wellKnown := systemInfoOIDs[result.OID]; wellKnown {
		return nil
	}

	text, err := result.GetStringValue()
	if err != nil {
		return nil
	}
	text = strings.TrimRight(text, "\x00")
	text = strings.TrimSpace(text)
	// An empty Prometheus label cannot be told from an absent one, so a blank
	// reading would create a series that says nothing.
	if text == "" {
		return nil
	}

	labels := newSampleLabels(deviceName)
	labels[labelValue] = text
	mergeExtraLabels(deviceName, labels, extraLabels)

	return &Sample{
		Name:      textScalarMetricName(index, result.OID),
		Labels:    labels,
		Value:     infoMetricValue,
		Timestamp: result.Timestamp,
	}
}

// textScalarMetricName names a text scalar's info metric using the numeric path's resolution plus _info.
func textScalarMetricName(index *mibIndex, oid string) string {
	name := SanitizeMetricName(metricNamePrefix + oid)
	if index != nil {
		if resolved, ok := resolveMetricName(index.names, oid); ok {
			name = resolved
		}
	}
	return infoMetricName(strings.TrimPrefix(name, metricNamePrefix))
}

// infoMetricName is the metric carrying objectName's value in a label; tests pin it to promname.InfoMetricName.
func infoMetricName(objectName string) string {
	return metricNamePrefix + strings.ToLower(objectName) + "_info"
}
