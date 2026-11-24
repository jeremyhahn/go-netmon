package metrics

import (
	"time"

	"github.com/jeremyhahn/go-netmon/internal/snmp"
)

// The one path that builds every sample's labels: structural labels plus the device's configured tags.

// Structural label names shared across construction sites. Index label names
// are not listed here because they come from the MIBs, not from this package.
const (
	labelDevice = "device"
	labelOID    = "oid"
	labelValue  = "value"
)

// systemInfoMetricName is the metric carrying a device's sysName, sysDescr and
// the rest of the system group as labels.
const systemInfoMetricName = "snmp_system_info"

// infoMetricValue is the constant every info metric carries. Info metrics say
// nothing with their value; the information is in the labels.
const infoMetricValue = 1.0

// newSampleLabels returns the labels every sample of deviceName starts from.
func newSampleLabels(deviceName string) map[string]string {
	return map[string]string{labelDevice: deviceName}
}

// newInstanceLabels returns the structural labels shared by every sample that
// reads a single MIB instance: the device polled and the instance OID.
func newInstanceLabels(deviceName, instanceOID string) map[string]string {
	labels := newSampleLabels(deviceName)
	labels[labelOID] = instanceOID
	return labels
}

// mergeExtraLabels adds a device's tags; a tag colliding with a structural label is dropped and counted in sampleLabelConflicts.
func mergeExtraLabels(deviceName string, labels, extraLabels map[string]string) {
	for name, value := range extraLabels {
		if _, structural := labels[name]; structural {
			sampleLabelConflicts.WithLabelValues(deviceName, name).Inc()
			continue
		}
		labels[name] = value
	}
}

// recordSystemInfo files a well-known system scalar under its label name; empty values are dropped.
func recordSystemInfo(systemInfo map[string]string, result *snmp.Result) {
	labelName, wellKnown := systemInfoOIDs[result.OID]
	if !wellKnown {
		return
	}

	value, err := result.GetStringValue()
	if err != nil || value == "" {
		return
	}

	systemInfo[labelName] = value
}

// newSystemInfoSample builds deviceName's snmp_system_info sample; system values outrank colliding tags.
func newSystemInfoSample(deviceName string, systemInfo, extraLabels map[string]string, timestamp time.Time) *Sample {
	labels := newSampleLabels(deviceName)
	for name, value := range systemInfo {
		labels[name] = value
	}
	mergeExtraLabels(deviceName, labels, extraLabels)

	return &Sample{
		Name:      systemInfoMetricName,
		Labels:    labels,
		Value:     infoMetricValue,
		Timestamp: timestamp,
	}
}

// pollTimestamp returns the poll's first reading time for synthesized samples.
func pollTimestamp(results []*snmp.Result) time.Time {
	if len(results) > 0 {
		return results[0].Timestamp
	}
	return time.Now()
}
