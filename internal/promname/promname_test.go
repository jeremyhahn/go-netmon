package promname_test

import (
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/promname"
)

func TestMetricName(t *testing.T) {
	tests := []struct {
		name       string
		objectName string
		want       string
	}{
		{"a plain object name", "ifInOctets", "snmp_ifInOctets"},
		{"case is preserved", "cpmCPUTotal5sec", "snmp_cpmCPUTotal5sec"},
		{"a scalar", "sysUpTime", "snmp_sysUpTime"},
		{"underscores are kept", "sys_up_time", "snmp_sys_up_time"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := promname.MetricName(tt.objectName); got != tt.want {
				t.Errorf("MetricName(%q) = %q, want %q", tt.objectName, got, tt.want)
			}
		})
	}
}

// TestMetricNameSanitizesIllegalNames covers the inputs Prometheus' metric name
// grammar rejects. A name that cannot be sanitized into a legal identifier is
// still turned into one rather than being passed through to a query that would
// fail to parse.
func TestMetricNameSanitizesIllegalNames(t *testing.T) {
	tests := []struct {
		name       string
		objectName string
		want       string
	}{
		{"a hyphen", "cbgpPeer2AddrFamily-Prefix", "snmp_cbgpPeer2AddrFamily_Prefix"},
		// Sanitizing is applied to the name alone, so its "must not start
		// with a digit" rule fires even though the prefix already satisfies
		// it. This matches the exporter exactly, which is what matters; MIB
		// object names never start with a digit.
		{"a dotted OID, which is not an object name", "1.3.6.1.2.1", "snmp___3_6_1_2_1"},
		{"spaces", "if In Octets", "snmp_if_In_Octets"},
		{"non-ASCII", "ifInOctetsé", "snmp_ifInOctets_"},
		// The prefix already satisfies the "must not start with a digit" rule,
		// so an empty name yields the bare prefix rather than an illegal name.
		{"no name at all", "", "snmp_"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := promname.MetricName(tt.objectName); got != tt.want {
				t.Errorf("MetricName(%q) = %q, want %q", tt.objectName, got, tt.want)
			}
		})
	}
}

func TestNameWithPrefix(t *testing.T) {
	tests := []struct {
		name       string
		prefix     string
		objectName string
		want       string
	}{
		{"a caller-chosen prefix", "netmon_", "ifInOctets", "netmon_ifInOctets"},
		{"an empty prefix means the exporter's", "", "ifInOctets", "snmp_ifInOctets"},
		{"neither prefix nor name", "", "", "snmp_"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := promname.NameWithPrefix(tt.prefix, tt.objectName); got != tt.want {
				t.Errorf("NameWithPrefix(%q, %q) = %q, want %q",
					tt.prefix, tt.objectName, got, tt.want)
			}
		})
	}
}

func TestIndexLabel(t *testing.T) {
	tests := []struct {
		name       string
		columnName string
		want       string
	}{
		{"an interface index", "ifIndex", "ifindex"},
		{"a composite index column", "cbgpPeer2RemoteAddr", "cbgppeer2remoteaddr"},
		{"already lowercase", "ifindex", "ifindex"},
		{"no column name", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := promname.IndexLabel(tt.columnName); got != tt.want {
				t.Errorf("IndexLabel(%q) = %q, want %q", tt.columnName, got, tt.want)
			}
		})
	}
}

func TestInfoMetricName(t *testing.T) {
	tests := []struct {
		name       string
		columnName string
		want       string
	}{
		{"an interface description", "ifDescr", "snmp_ifdescr_info"},
		{"a BGP peer state", "cbgpPeer2State", "snmp_cbgppeer2state_info"},
		{"no column name", "", "snmp__info"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := promname.InfoMetricName(tt.columnName); got != tt.want {
				t.Errorf("InfoMetricName(%q) = %q, want %q", tt.columnName, got, tt.want)
			}
		})
	}
}

func TestRawOIDMetricName(t *testing.T) {
	tests := []struct {
		name     string
		instance string
		want     string
	}{
		{
			name:     "the leading dot the poller carries becomes a second underscore",
			instance: ".1.3.6.1.4.1.9.9.999.1.1",
			want:     "snmp__1_3_6_1_4_1_9_9_999_1_1",
		},
		{
			name:     "an OID written without a leading dot",
			instance: "1.3.6.1.4.1.9.9.999.1.1",
			want:     "snmp_1_3_6_1_4_1_9_9_999_1_1",
		},
		{
			name:     "no OID at all still yields a legal name",
			instance: "",
			want:     "snmp_",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := promname.RawOIDMetricName(tt.instance); got != tt.want {
				t.Errorf("RawOIDMetricName(%q) = %q, want %q", tt.instance, got, tt.want)
			}
		})
	}
}

func BenchmarkMetricName(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = promname.MetricName("cpmProcessMemoryCore")
	}
}

func BenchmarkIndexLabel(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = promname.IndexLabel("cbgpPeer2RemoteAddr")
	}
}
