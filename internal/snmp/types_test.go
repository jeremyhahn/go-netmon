package snmp

import (
	"testing"

	"github.com/gosnmp/gosnmp"
)

// An SNMP response carries one varbind per requested OID whether or not the
// agent implements it. The ones it does not implement come back as a
// well-formed "not here", and counting those as readings is how a device that
// implements almost nothing reports more metrics than it produces series.

func TestHasValueRejectsAnAbsentObject(t *testing.T) {
	absent := []struct {
		name string
		typ  gosnmp.Asn1BER
	}{
		{"NoSuchObject", gosnmp.NoSuchObject},
		{"NoSuchInstance", gosnmp.NoSuchInstance},
		{"EndOfMibView", gosnmp.EndOfMibView},
		{"Null", gosnmp.Null},
	}

	for _, tt := range absent {
		t.Run(tt.name, func(t *testing.T) {
			result := &Result{OID: "1.3.6.1.2.1.4.3.0", Type: tt.typ}
			if result.HasValue() {
				t.Errorf("%s counted as a value; it is the agent saying it has no such object", tt.name)
			}
		})
	}
}

func TestHasValueAcceptsAReading(t *testing.T) {
	readings := []struct {
		name  string
		typ   gosnmp.Asn1BER
		value interface{}
	}{
		{"Integer", gosnmp.Integer, 42},
		{"Counter32", gosnmp.Counter32, uint(1000)},
		{"Counter64", gosnmp.Counter64, uint64(1000)},
		{"Gauge32", gosnmp.Gauge32, uint(7)},
		{"TimeTicks", gosnmp.TimeTicks, uint32(12345)},
		{"OctetString", gosnmp.OctetString, []byte("KN4116VA")},
		// Zero is a reading. A device reporting no errors reports zero of them.
		{"a zero counter", gosnmp.Counter32, uint(0)},
		// So is an empty string: sysLocation is often set to nothing.
		{"an empty string", gosnmp.OctetString, []byte("")},
	}

	for _, tt := range readings {
		t.Run(tt.name, func(t *testing.T) {
			result := &Result{OID: "1.3.6.1.2.1.1.3.0", Type: tt.typ, Value: tt.value}
			if !result.HasValue() {
				t.Errorf("%s was not counted as a value", tt.name)
			}
		})
	}
}

// TestCountValuesMatchesWhatTheDeviceAnswered is the case this exists for: a
// poll of a device that implements a fraction of what it was asked about.
func TestCountValuesMatchesWhatTheDeviceAnswered(t *testing.T) {
	results := []*Result{
		{OID: "1.3.6.1.2.1.1.3.0", Type: gosnmp.TimeTicks, Value: uint32(1)},
		{OID: "1.3.6.1.2.1.1.7.0", Type: gosnmp.Integer, Value: 72},
		// The KVM advertises IP, TCP and UDP and implements none of them.
		{OID: "1.3.6.1.2.1.4.3.0", Type: gosnmp.NoSuchObject},
		{OID: "1.3.6.1.2.1.6.5.0", Type: gosnmp.NoSuchObject},
		{OID: "1.3.6.1.2.1.7.1.0", Type: gosnmp.NoSuchObject},
		nil, // a defensive gap in the slice must not panic or count
	}

	if got := CountValues(results); got != 2 {
		t.Errorf("CountValues = %d, want 2: only two of six varbinds carried a value", got)
	}
	if got := CountValues(nil); got != 0 {
		t.Errorf("CountValues(nil) = %d, want 0", got)
	}
}
