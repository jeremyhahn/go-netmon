package cli

import (
	"math"
	"testing"

	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// ifMIBObjects are the IF-MIB definitions the resolution tests index. The two
// columns ending in .2 and .20 are the pair that catches a prefix match done
// on characters instead of sub-identifiers.
func ifMIBObjects() []client.MIBObject {
	return []client.MIBObject{
		{
			OID:      ".1.3.6.1.2.1.1.3",
			Name:     "sysUpTime",
			Type:     "TimeTicks",
			IsScalar: true,
		},
		{
			OID:      ".1.3.6.1.2.1.2.2.1.2",
			Name:     "ifDescr",
			Type:     "OctetString",
			IsColumn: true,
			TableOID: ".1.3.6.1.2.1.2.2",
		},
		{
			OID:      ".1.3.6.1.2.1.2.2.1.8",
			Name:     "ifOperStatus",
			Type:     "Integer32",
			IsColumn: true,
			TableOID: ".1.3.6.1.2.1.2.2",
			EnumValues: map[int64]string{
				1: "up", 2: "down", 3: "testing",
			},
		},
		{
			OID:      ".1.3.6.1.2.1.2.2.1.10",
			Name:     "ifInOctets",
			Type:     "Counter32",
			Unit:     "octets",
			IsColumn: true,
			TableOID: ".1.3.6.1.2.1.2.2",
		},
		{
			OID:      ".1.3.6.1.2.1.2.2.1.20",
			Name:     "ifOutErrors",
			Type:     "Counter32",
			IsColumn: true,
			TableOID: ".1.3.6.1.2.1.2.2",
		},
	}
}

func TestOIDFromMetricName(t *testing.T) {
	tests := []struct {
		name   string
		metric string
		want   string
	}{
		{
			name:   "leading dot becomes a doubled underscore",
			metric: "snmp__1_3_6_1_2_1_2_2_1_10_10",
			want:   "1.3.6.1.2.1.2.2.1.10.10",
		},
		{
			name:   "oid written without a leading dot",
			metric: "snmp_1_3_6_1_2_1_1_3_0",
			want:   "1.3.6.1.2.1.1.3.0",
		},
		{
			name:   "multi-digit sub-identifiers survive",
			metric: "snmp__1_3_6_1_4_1_9999_100_1_1234",
			want:   "1.3.6.1.4.1.9999.100.1.1234",
		},
		{
			name:   "info metric is not an oid",
			metric: "snmp_system_info",
			want:   "",
		},
		{
			name:   "info metric with a digit-free name",
			metric: "snmp_ifname_info",
			want:   "",
		},
		{
			name:   "poller telemetry has no snmp prefix",
			metric: "gonetmon_polls_total",
			want:   "",
		},
		{
			name:   "prefix alone",
			metric: "snmp_",
			want:   "",
		},
		{
			name:   "prefix and leading dot alone",
			metric: "snmp__",
			want:   "",
		},
		{
			name:   "empty component from a doubled separator",
			metric: "snmp__1__3_6",
			want:   "",
		},
		{
			name:   "trailing separator",
			metric: "snmp__1_3_6_",
			want:   "",
		},
		{
			name:   "letters mixed into a component",
			metric: "snmp__1_3_6a_1",
			want:   "",
		},
		{
			name:   "empty metric name",
			metric: "",
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := oidFromMetricName(tt.metric); got != tt.want {
				t.Errorf("oidFromMetricName(%q) = %q, want %q", tt.metric, got, tt.want)
			}
		})
	}
}

// series builds the label set the exporter attaches to one sample: the object's
// metric name, the instance OID that identifies the row, and the enrichment
// labels a real interface series carries. Those extras are the reason Describe
// reads the instance from the "oid" label -- nothing about ifname, ifdescr or
// device says which of them, if any, is a table index.
func series(metricName, instanceOID string, extra map[string]string) map[string]string {
	labels := map[string]string{
		seriesNameLabel: metricName,
		"device":        "core-01",
	}
	if instanceOID != "" {
		labels["oid"] = instanceOID
	}
	for k, v := range extra {
		labels[k] = v
	}
	return labels
}

func TestOIDIndexLookupTakesTheLongestPrefix(t *testing.T) {
	index := newOIDIndex(ifMIBObjects())

	tests := []struct {
		name         string
		oid          string
		wantName     string
		wantInstance string
	}{
		{
			name:         "column with a row index",
			oid:          "1.3.6.1.2.1.2.2.1.10.10",
			wantName:     "ifInOctets",
			wantInstance: "10",
		},
		{
			name:         "scalar instance",
			oid:          "1.3.6.1.2.1.1.3.0",
			wantName:     "sysUpTime",
			wantInstance: "0",
		},
		{
			name:         "leading dot is accepted",
			oid:          ".1.3.6.1.2.1.2.2.1.8.3",
			wantName:     "ifOperStatus",
			wantInstance: "3",
		},
		{
			name:         "object oid with no instance at all",
			oid:          "1.3.6.1.2.1.2.2.1.2",
			wantName:     "ifDescr",
			wantInstance: "",
		},
		{
			// The decisive case: ".2" is a character prefix of ".20", so a
			// string comparison would file this sample under ifDescr.
			name:         "sub-identifier boundary is respected",
			oid:          "1.3.6.1.2.1.2.2.1.20.7",
			wantName:     "ifOutErrors",
			wantInstance: "7",
		},
		{
			name:         "multi-part index",
			oid:          "1.3.6.1.2.1.2.2.1.10.1.2.3",
			wantName:     "ifInOctets",
			wantInstance: "1.2.3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj, instance, ok := index.lookup(tt.oid)
			if !ok {
				t.Fatalf("lookup(%q) found nothing", tt.oid)
			}
			if obj.Name != tt.wantName || instance != tt.wantInstance {
				t.Errorf("lookup(%q) = %s[%s], want %s[%s]",
					tt.oid, obj.Name, instance, tt.wantName, tt.wantInstance)
			}
		})
	}
}

func TestOIDIndexLookupRejectsUnknownOIDs(t *testing.T) {
	// ifOutErrors is deliberately left out, so 1.3.6.1.2.1.2.2.1.20.7 has no
	// legitimate match: anything reported here came from a character prefix.
	index := newOIDIndex([]client.MIBObject{
		{OID: ".1.3.6.1.2.1.2.2.1.2", Name: "ifDescr", IsColumn: true},
	})

	for _, oid := range []string{
		"1.3.6.1.2.1.2.2.1.20.7", // .2 must not swallow .20
		"1.3.6.1.4.1.9.9.109.1",  // a vendor tree nothing loaded defines
		"",
		".",
	} {
		if obj, instance, ok := index.lookup(oid); ok {
			t.Errorf("lookup(%q) = %s[%s], want no match", oid, obj.Name, instance)
		}
	}
}

func TestOIDIndexSkipsObjectsWithoutAnOID(t *testing.T) {
	index := newOIDIndex([]client.MIBObject{
		{OID: "", Name: "nameless"},
		{OID: ".", Name: "rootish"},
		{OID: ".1.3.6.1.2.1.1.3", Name: "sysUpTime", IsScalar: true},
	})

	if len(index.objects) != 1 {
		t.Fatalf("index holds %d objects, want only the one with a usable OID", len(index.objects))
	}
	if _, _, ok := index.lookup("1.3.6.1.2.1.1.3.0"); !ok {
		t.Error("the usable object must still resolve")
	}
}

func TestOIDIndexDescribeNamesSeries(t *testing.T) {
	index := newOIDIndex(ifMIBObjects())

	tests := []struct {
		name      string
		labels    map[string]string
		wantLabel string
		wantType  string
		wantOK    bool
	}{
		{
			name: "column carries its index, read from the oid label",
			labels: series("snmp_ifInOctets", ".1.3.6.1.2.1.2.2.1.10.10", map[string]string{
				// Every one of these is enrichment the exporter adds. The
				// row is identified by the oid label, not by guessing which
				// of them is the table's INDEX.
				"ifindex": "10", "ifname": "GigabitEthernet0/0",
				"ifdescr": "GigabitEthernet0/0", "ifoperstatus": "up",
			}),
			wantLabel: "ifInOctets[10]",
			wantType:  "Counter32",
			wantOK:    true,
		},
		{
			name:      "one metric name, another row",
			labels:    series("snmp_ifInOctets", ".1.3.6.1.2.1.2.2.1.10.20178", map[string]string{"ifindex": "20178"}),
			wantLabel: "ifInOctets[20178]",
			wantType:  "Counter32",
			wantOK:    true,
		},
		{
			name:      "scalar drops its .0 instance",
			labels:    series("snmp_sysUpTime", ".1.3.6.1.2.1.1.3.0", nil),
			wantLabel: "sysUpTime",
			wantType:  "TimeTicks",
			wantOK:    true,
		},
		{
			name:      "column named without an instance",
			labels:    series("snmp_ifDescr", ".1.3.6.1.2.1.2.2.1.2", nil),
			wantLabel: "ifDescr",
			wantType:  "OctetString",
			wantOK:    true,
		},
		{
			name:      "an OID no loaded MIB defines keeps its PromQL identity",
			labels:    series("snmp__1_3_6_1_4_1_9_2_1_58_0", ".1.3.6.1.4.1.9.2.1.58.0", nil),
			wantLabel: `snmp__1_3_6_1_4_1_9_2_1_58_0{device="core-01",oid=".1.3.6.1.4.1.9.2.1.58.0"}`,
			wantOK:    false,
		},
		{
			name:      "a raw-OID name with no oid label still resolves",
			labels:    map[string]string{seriesNameLabel: "snmp__1_3_6_1_2_1_2_2_1_10_10"},
			wantLabel: "ifInOctets[10]",
			wantType:  "Counter32",
			wantOK:    true,
		},
		{
			name:      "non-oid metric keeps its name",
			labels:    map[string]string{seriesNameLabel: "snmp_system_info"},
			wantLabel: "snmp_system_info",
			wantOK:    false,
		},
		{
			name:      "foreign metric keeps its name",
			labels:    map[string]string{seriesNameLabel: "gonetmon_polls_total"},
			wantLabel: "gonetmon_polls_total",
			wantOK:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			label, meta, ok := index.Describe(tt.labels)
			if label != tt.wantLabel || ok != tt.wantOK {
				t.Fatalf("Describe(%v) = %q, %v, want %q, %v",
					tt.labels, label, ok, tt.wantLabel, tt.wantOK)
			}
			if !tt.wantOK {
				if meta != nil {
					t.Errorf("unresolved series must carry no metadata, got %+v", meta)
				}
				return
			}
			if meta == nil {
				t.Fatal("a resolved series must carry metadata for formatting")
			}
			if meta.Type != tt.wantType {
				t.Errorf("meta.Type = %q, want %q", meta.Type, tt.wantType)
			}
		})
	}
}

// TestOIDIndexDescribeOnNilIndex covers the --raw path, where no index is
// built at all and every series must still render under its full identity.
func TestOIDIndexDescribeOnNilIndex(t *testing.T) {
	var index *oidIndex

	labels := series("snmp_ifInOctets", ".1.3.6.1.2.1.2.2.1.10.10", nil)
	want := formatSeries(labels)

	label, meta, ok := index.Describe(labels)
	if ok || meta != nil || label != want {
		t.Errorf("Describe on a nil index = %q, %+v, %v; want %q", label, meta, ok, want)
	}

	if label, _, ok := newOIDIndex(nil).Describe(labels); ok || label != want {
		t.Errorf("Describe against an empty index = %q, %v; want %q", label, ok, want)
	}
}

// TestDescribeFeedsTheValueFormatter checks the whole point of returning
// metadata: the value renders the way a MIB browser would show it.
func TestDescribeFeedsTheValueFormatter(t *testing.T) {
	index := newOIDIndex(ifMIBObjects())

	tests := []struct {
		name   string
		labels map[string]string
		value  float64
		want   string
	}{
		{
			name:   "counter gains its units",
			labels: series("snmp_ifInOctets", ".1.3.6.1.2.1.2.2.1.10.10", nil),
			value:  1107275976,
			want:   "1107275976 octets",
		},
		{
			name:   "enumeration becomes a label",
			labels: series("snmp_ifOperStatus", ".1.3.6.1.2.1.2.2.1.8.10", nil),
			value:  1,
			want:   "up(1)",
		},
		{
			name:   "value outside the enumeration is called out",
			labels: series("snmp_ifOperStatus", ".1.3.6.1.2.1.2.2.1.8.10", nil),
			value:  9,
			want:   "unknown(9)",
		},
		{
			name:   "timeticks become a duration",
			labels: series("snmp_sysUpTime", ".1.3.6.1.2.1.1.3.0", nil),
			value:  8640000,
			want:   "1d 0h 0m 0s",
		},
		{
			name:   "unresolved series keeps the plain number",
			labels: series("snmp__1_3_6_1_4_1_9_2_1_58_0", ".1.3.6.1.4.1.9.2.1.58.0", nil),
			value:  42,
			want:   "42",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, meta, _ := index.Describe(tt.labels)
			if got := formatSeriesValue(meta, tt.value); got != tt.want {
				t.Errorf("formatSeriesValue(%v, %v) = %q, want %q",
					tt.labels, tt.value, got, tt.want)
			}
		})
	}
}

// TestFormatSeriesValueRejectsUnusableNumbers documents the boundary between
// the MIB formatter, which speaks in whole SNMP integers, and the numeric
// rendering the metrics commands use for everything else.
func TestFormatSeriesValueRejectsUnusableNumbers(t *testing.T) {
	index := newOIDIndex(ifMIBObjects())
	_, counter, _ := index.Describe(series("snmp_ifInOctets", ".1.3.6.1.2.1.2.2.1.10.10", nil))

	tests := []struct {
		name  string
		value float64
		want  string
	}{
		{name: "whole number is formatted", value: 7, want: "7 octets"},
		{name: "a Counter64-sized value still fits", value: 1e18, want: "1000000000000000000 octets"},
		{name: "fraction is not truncated", value: 1.5, want: formatValue(1.5)},
		{name: "beyond int64 range", value: 1e19, want: formatValue(1e19)},
		{name: "not a number", value: math.NaN(), want: formatValue(math.NaN())},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatSeriesValue(counter, tt.value); got != tt.want {
				t.Errorf("formatSeriesValue(%v) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}

	if got := formatSeriesValue(nil, 3); got != "3" {
		t.Errorf("formatSeriesValue(nil, 3) = %q, want %q", got, "3")
	}
}
