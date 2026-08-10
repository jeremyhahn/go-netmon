package api

import (
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/promname"
	"github.com/jeremyhahn/go-netmon/internal/webui/dashboard"
)

// TestModuleHasData covers the two independent kinds of evidence that a module
// is actually collecting: a table the poller has walked, and a scalar whose
// exported metric exists in Prometheus.
func TestModuleHasData(t *testing.T) {
	ifEntry := &mib.OIDMetadata{
		OID: "1.3.6.1.2.1.2.2.1", Name: "ifEntry", Module: "IF-MIB",
		IsTableEntry: true, EntryOID: "1.3.6.1.2.1.2.2.1",
	}
	sysUpTime := &mib.OIDMetadata{
		OID: "1.3.6.1.2.1.1.3", Name: "sysUpTime", Module: "SNMPv2-MIB",
		IsScalar: true, Pollable: true,
	}
	unpollableScalar := &mib.OIDMetadata{
		OID: "1.3.6.1.2.1.1.9", Name: "sysORTable", Module: "SNMPv2-MIB",
		IsScalar: true, Pollable: false,
	}

	learned := dashboard.LearnedTablesMap{
		"1.3.6.1.2.1.2.2.1": &dashboard.LearnedTable{EntryOID: "1.3.6.1.2.1.2.2.1"},
	}
	metrics := map[string]bool{
		promname.MetricName(sysUpTime.Name): true,
	}

	tests := []struct {
		name    string
		oids    []*mib.OIDMetadata
		learned dashboard.LearnedTablesMap
		metrics map[string]bool
		want    bool
	}{
		{
			name: "table entry the poller has learned",
			oids: []*mib.OIDMetadata{ifEntry}, learned: learned, want: true,
		},
		{
			name: "scalar present in Prometheus",
			oids: []*mib.OIDMetadata{sysUpTime}, metrics: metrics, want: true,
		},
		{
			name: "table entry with nothing learned",
			oids: []*mib.OIDMetadata{ifEntry}, learned: dashboard.LearnedTablesMap{}, want: false,
		},
		{
			name: "scalar absent from Prometheus",
			oids: []*mib.OIDMetadata{sysUpTime},
			// A different module's metric must not count as evidence.
			metrics: map[string]bool{promname.MetricName("cpmCPUTotal5minRev"): true},
			want:    false,
		},
		{
			name: "unpollable scalar is not evidence",
			oids: []*mib.OIDMetadata{unpollableScalar},
			metrics: map[string]bool{
				promname.MetricName(unpollableScalar.Name): true,
			},
			want: false,
		},
		{
			name: "module with no objects",
			oids: nil, learned: learned, metrics: metrics, want: false,
		},
		{
			name: "no evidence available at all",
			oids: []*mib.OIDMetadata{ifEntry, sysUpTime}, want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := moduleHasData(tt.oids, tt.learned, tt.metrics); got != tt.want {
				t.Errorf("moduleHasData = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestModuleHasDataUsesEntryOIDFallback verifies that a row node whose EntryOID
// was not populated still matches on its own OID, which is how older stored
// metadata is shaped.
func TestModuleHasDataUsesEntryOIDFallback(t *testing.T) {
	row := &mib.OIDMetadata{
		OID: "1.3.6.1.2.1.2.2.1", Name: "ifEntry", Module: "IF-MIB",
		IsTableEntry: true, // EntryOID deliberately left empty
	}
	learned := dashboard.LearnedTablesMap{
		"1.3.6.1.2.1.2.2.1": &dashboard.LearnedTable{EntryOID: "1.3.6.1.2.1.2.2.1"},
	}

	if !moduleHasData([]*mib.OIDMetadata{row}, learned, nil) {
		t.Error("a row node with no EntryOID should fall back to its own OID")
	}
}

// TestModuleHasDataNamesScalarsLikeTheExporter pins the name filterMIBsWithData
// looks a scalar up under to the exporter's naming rule. Getting it wrong hides
// every MIB from the web UI, which is indistinguishable from no data flowing.
func TestModuleHasDataNamesScalarsLikeTheExporter(t *testing.T) {
	scalar := &mib.OIDMetadata{
		OID: "1.3.6.1.4.1.9.9.109.1.1.1.1.8", Name: "cpmCPUTotal5minRev",
		IsScalar: true, Pollable: true,
	}

	tests := []struct {
		name        string
		metricNames map[string]bool
		want        bool
	}{
		{
			name:        "the object's own metric",
			metricNames: map[string]bool{promname.MetricName(scalar.Name): true},
			want:        true,
		},
		{
			name:        "the metric name the exporter no longer emits",
			metricNames: map[string]bool{"snmp__1_3_6_1_4_1_9_9_109_1_1_1_1_8_0": true},
			want:        false,
		},
		{
			name:        "a lowercased name, which the exporter does not emit",
			metricNames: map[string]bool{"snmp_cpmcputotal5minrev": true},
			want:        false,
		},
		{
			name:        "nothing in Prometheus",
			metricNames: nil,
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := moduleHasData([]*mib.OIDMetadata{scalar}, nil, tt.metricNames); got != tt.want {
				t.Errorf("moduleHasData() = %v, want %v", got, tt.want)
			}
		})
	}
}
