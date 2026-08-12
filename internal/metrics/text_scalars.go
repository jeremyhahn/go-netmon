package metrics

import (
	"strings"

	"github.com/jeremyhahn/go-netmon/internal/snmp"
)

// Scalars whose value is text rather than a number.
//
// A reading that does not parse as a float had nowhere to go. The conversion
// offered it to snmp_system_info, which recognizes five well-known system-group
// OIDs, and discarded everything else. Table columns were spared -- a status
// column becomes an info metric -- but a scalar was not, so a device whose
// interesting objects are all scalars reported nothing at all.
//
// That is not a corner case. The ATEN KN4116VA states its whole condition in
// DisplayString scalars:
//
//	PSU1Status   "PSU1 is on"
//	PSU2Status   "PSU2 is abnormal, please check the device"
//	fanSpeed     "Fan speed detection is disabled"
//	temperature  "Temperature detection is disabled"
//	LAN1Status   "LAN1 is connected"
//
// The poller asked for all of them, the device answered all of them, and the
// stack stored none of them -- including a power supply reporting a fault.
//
// They are emitted the same way a status column is: snmp_<object>_info carrying
// the text in a "value" label, constant 1. Alerting reads the label, which is
// the only sensible thing to do with a sentence, and one series per object holds
// however the vendor words it.

// textScalarSample builds the info metric for a scalar whose value is text, or
// returns nil when the result is not one.
//
// index names the object. An OID no MIB describes still gets a sample, under the
// raw-OID name the numeric path falls back to, because losing a device's only
// health signal to a missing MIB is worse than an ugly metric name.
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

// textScalarMetricName is the info metric naming a text scalar.
//
// It reuses the numeric path's resolution so both name the same object the same
// way, then applies the _info suffix that marks a value carried in a label
// rather than in the sample.
func textScalarMetricName(index *mibIndex, oid string) string {
	name := SanitizeMetricName(metricNamePrefix + oid)
	if index != nil {
		if resolved, ok := resolveMetricName(index.names, oid); ok {
			name = resolved
		}
	}
	return infoMetricName(strings.TrimPrefix(name, metricNamePrefix))
}

// infoMetricName is the metric carrying objectName's value in a label.
//
// promname.InfoMetricName computes the same name for the query side to read
// back; this package cannot import that one, since it imports this one. The
// tests pin the two together.
func infoMetricName(objectName string) string {
	return metricNamePrefix + strings.ToLower(objectName) + "_info"
}
