package metrics

import (
	"time"

	"github.com/jeremyhahn/go-netmon/internal/snmp"
)

// This file holds the one path by which every sample's labels are built.
//
// A sample's labels fall into two groups. Structural labels come from the poll
// itself and establish the series' identity: the device that was polled, the
// instance OID that was read, the MIB-derived index labels of the row it came
// from, and — for an info metric — the value being described. Extra labels are
// the device's configured tags, supplied by the caller and attached to every
// sample of that device so they can be filtered, federated and joined on.
//
// Both groups have to be applied identically everywhere, because a device's
// tags are only useful if they appear on all of that device's series. An info
// metric that misses them is the worst case: an info metric exists to be
// joined against, so a missing tag there breaks the joins rather than just the
// series carrying it.

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

// mergeExtraLabels adds a device's configured tags to labels, which must
// already hold the sample's structural labels.
//
// Structural labels win: a tag whose name collides with one is dropped, not
// merged. Letting a tag through would let configuration rewrite a series'
// identity — a tag named "device" would file one router's readings under
// another's name, a tag named "ifindex" would move a sample to a different
// interface's series, and a tag named "value" would make an info metric
// describe something it never measured. Dropping is also the only outcome that
// does not depend on map iteration order, so the same configuration produces
// the same series on every poll and every process.
//
// The drop is deliberate but not silent: each one increments
// sampleLabelConflicts, so a tag that never reaches Prometheus is visible as a
// counter rather than as an unexplained missing label.
func mergeExtraLabels(deviceName string, labels, extraLabels map[string]string) {
	for name, value := range extraLabels {
		if _, structural := labels[name]; structural {
			sampleLabelConflicts.WithLabelValues(deviceName, name).Inc()
			continue
		}
		labels[name] = value
	}
}

// recordSystemInfo files result under its system-info label name when result
// reads one of the well-known system scalars, and ignores it otherwise. Empty
// values are dropped: an empty Prometheus label is indistinguishable from an
// absent one, so recording it would only widen the series for nothing.
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

// newSystemInfoSample builds the snmp_system_info sample describing
// deviceName. The system group values are structural: they are what the metric
// exists to report, so they outrank a colliding tag for the same reason an
// index label does.
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

// pollTimestamp returns the timestamp for samples synthesized for a poll as a
// whole rather than read from one result. The poll's own first reading is
// preferred so the synthesized samples land in the same scrape as the readings
// that produced them.
func pollTimestamp(results []*snmp.Result) time.Time {
	if len(results) > 0 {
		return results[0].Timestamp
	}
	return time.Now()
}
