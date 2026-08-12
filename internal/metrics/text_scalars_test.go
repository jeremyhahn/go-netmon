package metrics

import (
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/jeremyhahn/go-netmon/internal/snmp"
)

// A scalar whose value is text used to be polled, answered and then dropped.
//
// The conversion offered every non-numeric reading to snmp_system_info, which
// knows five system-group OIDs, and discarded the rest. Status columns became
// info metrics; scalars did not. A device that states its condition in
// DisplayString scalars therefore reported nothing at all -- including, on the
// KVM this was found with, a power supply saying it had failed.

// textResult is one OctetString reading.
func textResult(oid, value string) *snmp.Result {
	return &snmp.Result{
		OID: oid, Type: gosnmp.OctetString, Value: []byte(value),
		Timestamp: time.Unix(1_760_000_000, 0),
	}
}

// TestATextScalarBecomesAnInfoMetric is the reading that used to vanish.
func TestATextScalarBecomesAnInfoMetric(t *testing.T) {
	result := textResult("1.3.6.1.4.1.21317.1.3.1.13.1.9.2.0",
		"PSU2 is abnormal, please check the device")

	sample := textScalarSample("bastion", result, nil, nil)

	if sample == nil {
		t.Fatal("a text scalar produced no sample; a failed power supply would go unreported")
	}
	if sample.Value != infoMetricValue {
		t.Errorf("Value = %v, want %v: the reading is in a label, not the sample", sample.Value, infoMetricValue)
	}
	if got := sample.Labels[labelValue]; got != "PSU2 is abnormal, please check the device" {
		t.Errorf("value label = %q, want the device's own words", got)
	}
	if sample.Labels[labelDevice] != "bastion" {
		t.Errorf("device label = %q, want bastion", sample.Labels[labelDevice])
	}
	if !sample.Timestamp.Equal(result.Timestamp) {
		t.Errorf("Timestamp = %v, want the reading's own %v", sample.Timestamp, result.Timestamp)
	}
}

// TestATextScalarIsNamedInLowerCase pins the shape this package writes. That it
// matches what the query side looks for is asserted in internal/promname, which
// can import this package where this package cannot import that one.
func TestATextScalarIsNamedInLowerCase(t *testing.T) {
	cases := map[string]string{
		"PSU2Status":  "snmp_psu2status_info",
		"temperature": "snmp_temperature_info",
		"LAN1Status":  "snmp_lan1status_info",
	}
	for object, want := range cases {
		if got := infoMetricName(object); got != want {
			t.Errorf("infoMetricName(%q) = %q, want %q", object, got, want)
		}
	}
}

// TestAnUnansweredScalarIsNotAReading covers the bad-input case. A device that
// declines an OID still returns a varbind, and recording "NoSuchObject" as a
// health string would read as a reading.
func TestAnUnansweredScalarIsNotAReading(t *testing.T) {
	absent := []gosnmp.Asn1BER{gosnmp.NoSuchObject, gosnmp.NoSuchInstance, gosnmp.EndOfMibView, gosnmp.Null}

	for _, kind := range absent {
		result := &snmp.Result{OID: "1.3.6.1.4.1.21317.1.3.1.13.1.9.4.0", Type: kind}
		if sample := textScalarSample("bastion", result, nil, nil); sample != nil {
			t.Errorf("%v produced a sample labeled %q", kind, sample.Labels[labelValue])
		}
	}
}

// TestAnEmptyTextScalarIsDropped covers the other empty case: an OctetString
// that came back blank, or as the NUL padding some agents send. An empty
// Prometheus label cannot be told from an absent one, so the series would say
// nothing while still costing.
func TestAnEmptyTextScalarIsDropped(t *testing.T) {
	for _, blank := range []string{"", "   ", "\x00", "\x00\x00\x00\x00"} {
		result := textResult("1.3.6.1.4.1.21317.1.3.1.13.1.5.2.0", blank)
		if sample := textScalarSample("bastion", result, nil, nil); sample != nil {
			t.Errorf("%q produced a sample labeled %q", blank, sample.Labels[labelValue])
		}
	}

	// Padding around real text is trimmed, not treated as absent.
	padded := textResult("1.3.6.1.4.1.21317.1.3.1.13.1.8.1.0", "A1H21350020\x00\x00\x00")
	sample := textScalarSample("bastion", padded, nil, nil)
	if sample == nil {
		t.Fatal("a NUL-padded serial number was dropped")
	}
	if got := sample.Labels[labelValue]; got != "A1H21350020" {
		t.Errorf("value label = %q, want the padding stripped", got)
	}
}

// TestTheSystemGroupIsNotReportedTwice keeps this from duplicating
// snmp_system_info, which already carries sysDescr and friends as labels.
func TestTheSystemGroupIsNotReportedTwice(t *testing.T) {
	for oid := range systemInfoOIDs {
		result := textResult(oid, "some system value")
		if sample := textScalarSample("router", result, nil, nil); sample != nil {
			t.Errorf("%s produced %s as well as snmp_system_info", oid, sample.Name)
		}
	}
}

// TestTextScalarsSurviveTheWholeConversion is the regression at the level the
// bug actually bit: the reading has to come out of the function the poller
// calls, not merely out of the helper.
func TestTextScalarsSurviveTheWholeConversion(t *testing.T) {
	results := []*snmp.Result{
		textResult("1.3.6.1.4.1.21317.1.3.1.13.1.9.1.0", "PSU1 is on"),
		textResult("1.3.6.1.4.1.21317.1.3.1.13.1.9.2.0", "PSU2 is abnormal, please check the device"),
		textResult("1.3.6.1.4.1.21317.1.3.1.13.1.8.2.0", "V3.0.293"),
		// A numeric reading alongside them, which must stay a plain sample.
		{OID: "1.3.6.1.4.1.21317.1.3.1.13.1.2.3.0", Type: gosnmp.Integer, Value: 80,
			Timestamp: time.Unix(1_760_000_000, 0)},
	}

	samples := ConvertSNMPResultsWithTableIndexes("bastion", results, nil, nil, nil)

	var texts, numbers int
	for _, sample := range samples {
		switch sample.Value {
		case infoMetricValue:
			if sample.Labels[labelValue] != "" {
				texts++
			}
		default:
			numbers++
		}
	}

	if texts != 3 {
		t.Errorf("%d text readings survived, want 3:\n%s", texts, describeSamples(samples))
	}
	if numbers != 1 {
		t.Errorf("%d numeric readings survived, want 1:\n%s", numbers, describeSamples(samples))
	}
}

// describeSamples renders samples for a failure message.
func describeSamples(samples []*Sample) string {
	var out string
	for _, sample := range samples {
		out += "  " + sample.Name + " = " + sample.Labels[labelValue] + "\n"
	}
	if out == "" {
		return "  (no samples)"
	}
	return out
}
