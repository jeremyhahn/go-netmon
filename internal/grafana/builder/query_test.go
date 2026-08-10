package builder

import (
	"strings"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/promname"
)

func TestNewQueryBuilder(t *testing.T) {
	tests := []struct {
		name         string
		metricPrefix string
		wantPrefix   string
	}{
		{
			name:         "with custom prefix",
			metricPrefix: "custom_",
			wantPrefix:   "custom_",
		},
		{
			name:         "with empty prefix",
			metricPrefix: "",
			wantPrefix:   promname.Prefix,
		},
		{
			name:         "with default prefix",
			metricPrefix: promname.Prefix,
			wantPrefix:   promname.Prefix,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qb := NewQueryBuilder(tt.metricPrefix)
			if qb.metricPrefix != tt.wantPrefix {
				t.Errorf("NewQueryBuilder() metricPrefix = %v, want %v", qb.metricPrefix, tt.wantPrefix)
			}
		})
	}
}

func TestQueryBuilder_BuildScalarQuery(t *testing.T) {
	qb := NewQueryBuilder("snmp___")

	tests := []struct {
		name               string
		oid                *mib.OIDMetadata
		useDeviceVariable  bool
		wantExprContains   string
		wantLegendContains string
	}{
		{
			name: "scalar with device variable",
			oid: &mib.OIDMetadata{
				OID:      "1.3.6.1.2.1.1.1.0",
				Name:     "sysDescr",
				Type:     "OctetString",
				IsScalar: true,
			},
			useDeviceVariable:  true,
			wantExprContains:   `snmp___sysDescr{device=~"$device"}`,
			wantLegendContains: "{{device}}",
		},
		{
			name: "scalar without device variable",
			oid: &mib.OIDMetadata{
				OID:      "1.3.6.1.2.1.1.3.0",
				Name:     "sysUpTime",
				Type:     "TimeTicks",
				IsScalar: true,
			},
			useDeviceVariable:  false,
			wantExprContains:   "snmp___sysUpTime{}",
			wantLegendContains: "sysUpTime",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := qb.BuildScalarQuery(tt.oid, tt.useDeviceVariable)

			if target.RefID != "A" {
				t.Errorf("BuildScalarQuery() RefID = %v, want A", target.RefID)
			}

			if !strings.Contains(target.Expr, tt.wantExprContains) {
				t.Errorf("BuildScalarQuery() Expr = %v, want to contain %v", target.Expr, tt.wantExprContains)
			}

			if !strings.Contains(target.LegendFormat, tt.wantLegendContains) {
				t.Errorf("BuildScalarQuery() LegendFormat = %v, want to contain %v", target.LegendFormat, tt.wantLegendContains)
			}
		})
	}
}

func TestQueryBuilder_BuildCounterQuery(t *testing.T) {
	qb := NewQueryBuilder("snmp___")

	tests := []struct {
		name             string
		oid              *mib.OIDMetadata
		useDeviceVar     bool
		rateInterval     string
		wantExprContains []string
	}{
		{
			name: "counter with default rate",
			oid: &mib.OIDMetadata{
				OID:  "1.3.6.1.2.1.2.2.1.10",
				Name: "ifInOctets",
				Type: "Counter32",
			},
			useDeviceVar: true,
			rateInterval: "",
			wantExprContains: []string{
				"rate(",
				"[5m]",
				"* 8", // Octets should be multiplied by 8
			},
		},
		{
			name: "counter with custom rate interval",
			oid: &mib.OIDMetadata{
				OID:  "1.3.6.1.2.1.2.2.1.11",
				Name: "ifInUcastPkts",
				Type: "Counter32",
			},
			useDeviceVar: true,
			rateInterval: "1m",
			wantExprContains: []string{
				"rate(",
				"[1m]",
			},
		},
		{
			name: "counter64 with octets",
			oid: &mib.OIDMetadata{
				OID:  "1.3.6.1.2.1.31.1.1.1.6",
				Name: "ifHCInOctets",
				Type: "Counter64",
			},
			useDeviceVar: true,
			rateInterval: "5m",
			wantExprContains: []string{
				"rate(",
				"* 8",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := qb.BuildCounterQuery(tt.oid, tt.useDeviceVar, tt.rateInterval)

			for _, want := range tt.wantExprContains {
				if !strings.Contains(target.Expr, want) {
					t.Errorf("BuildCounterQuery() Expr = %v, want to contain %v", target.Expr, want)
				}
			}

			expectedInterval := tt.rateInterval
			if expectedInterval == "" {
				expectedInterval = "5m"
			}
			if target.Interval != expectedInterval {
				t.Errorf("BuildCounterQuery() Interval = %v, want %v", target.Interval, expectedInterval)
			}
		})
	}
}

func TestQueryBuilder_BuildTableQuery(t *testing.T) {
	qb := NewQueryBuilder("snmp___")

	tests := []struct {
		name             string
		oid              *mib.OIDMetadata
		useDeviceVar     bool
		useIndexVar      bool
		wantExprContains []string
	}{
		{
			name: "table column with both variables",
			oid: &mib.OIDMetadata{
				OID:      "1.3.6.1.2.1.2.2.1.2",
				Name:     "ifDescr",
				Type:     "DisplayString",
				IsColumn: true,
			},
			useDeviceVar: true,
			useIndexVar:  true,
			// BuildColumnQuery filters table columns by interface name
			// (ifname/$interface), not by raw row index.
			wantExprContains: []string{
				`device=~"$device"`,
				`ifname=~"$interface"`,
			},
		},
		{
			name: "table counter column",
			oid: &mib.OIDMetadata{
				OID:      "1.3.6.1.2.1.2.2.1.10",
				Name:     "ifInOctets",
				Type:     "Counter32",
				IsColumn: true,
			},
			useDeviceVar: true,
			useIndexVar:  true,
			wantExprContains: []string{
				"rate(",
				"* 8",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := qb.BuildColumnQuery(tt.oid, tt.useDeviceVar, tt.useIndexVar)

			for _, want := range tt.wantExprContains {
				if !strings.Contains(target.Expr, want) {
					t.Errorf("BuildColumnQuery() Expr = %v, want to contain %v", target.Expr, want)
				}
			}
		})
	}
}

func TestQueryBuilder_BuildAggregateQuery(t *testing.T) {
	qb := NewQueryBuilder("snmp___")

	tests := []struct {
		name             string
		oid              *mib.OIDMetadata
		aggregation      string
		groupBy          []string
		wantExprContains []string
	}{
		{
			name: "sum aggregation with grouping",
			oid: &mib.OIDMetadata{
				OID:  "1.3.6.1.2.1.2.2.1.10",
				Name: "ifInOctets",
				Type: "Counter32",
			},
			aggregation: "sum",
			groupBy:     []string{"device"},
			wantExprContains: []string{
				"sum by (",
				"rate(",
				"by (device)",
			},
		},
		{
			name: "avg aggregation without grouping",
			oid: &mib.OIDMetadata{
				OID:  "1.3.6.1.2.1.25.3.3.1.2",
				Name: "hrProcessorLoad",
				Type: "Integer32",
			},
			aggregation: "avg",
			groupBy:     []string{},
			wantExprContains: []string{
				"avg(",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := qb.BuildAggregateQuery(tt.oid, tt.aggregation, tt.groupBy)

			for _, want := range tt.wantExprContains {
				if !strings.Contains(target.Expr, want) {
					t.Errorf("BuildAggregateQuery() Expr = %v, want to contain %v", target.Expr, want)
				}
			}
		})
	}
}

func TestQueryBuilder_metricName(t *testing.T) {
	qb := NewQueryBuilder("snmp___")

	tests := []struct {
		name   string
		object *mib.OIDMetadata
		want   string
	}{
		{
			name:   "scalar",
			object: &mib.OIDMetadata{OID: ".1.3.6.1.2.1.1.1.0", Name: "sysDescr"},
			want:   "snmp___sysDescr",
		},
		{
			name:   "table column names the object, not the row",
			object: &mib.OIDMetadata{OID: ".1.3.6.1.2.1.2.2.1.10", Name: "ifInOctets", IsColumn: true},
			want:   "snmp___ifInOctets",
		},
		{
			name:   "a name needing sanitizing",
			object: &mib.OIDMetadata{OID: ".1.3.6.1.4.1.9.9.109.1.1.1.1.5", Name: "cpmCPUTotal-5sec"},
			want:   "snmp___cpmCPUTotal_5sec",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := qb.metricName(tt.object)
			if got != tt.want {
				t.Errorf("metricName() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestQueryBuilder_metricNameDefaultPrefix checks the empty-prefix default,
// which is the exporter's own prefix rather than a literal in this package.
func TestQueryBuilder_metricNameDefaultPrefix(t *testing.T) {
	got := NewQueryBuilder("").metricName(&mib.OIDMetadata{Name: "ifInOctets"})
	if want := promname.MetricName("ifInOctets"); got != want {
		t.Errorf("metricName() = %v, want %v", got, want)
	}
}

func TestQueryBuilder_isOctetCounter(t *testing.T) {
	qb := NewQueryBuilder("snmp___")

	tests := []struct {
		name string
		oid  *mib.OIDMetadata
		want bool
	}{
		{
			name: "ifInOctets",
			oid: &mib.OIDMetadata{
				Name: "ifInOctets",
				Type: "Counter32",
			},
			want: true,
		},
		{
			name: "ifHCInOctets",
			oid: &mib.OIDMetadata{
				Name: "ifHCInOctets",
				Type: "Counter64",
			},
			want: true,
		},
		{
			name: "ifInBytes",
			oid: &mib.OIDMetadata{
				Name: "ifInBytes",
				Type: "Counter32",
			},
			want: true,
		},
		{
			name: "ifInUcastPkts",
			oid: &mib.OIDMetadata{
				Name: "ifInUcastPkts",
				Type: "Counter32",
			},
			want: false,
		},
		{
			name: "non-counter with octets in name",
			oid: &mib.OIDMetadata{
				Name: "someOctets",
				Type: "Gauge32",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := qb.isOctetCounter(tt.oid)
			if got != tt.want {
				t.Errorf("isOctetCounter() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestQueryBuilder_buildLegendFormat(t *testing.T) {
	qb := NewQueryBuilder("snmp___")

	tests := []struct {
		name            string
		oid             *mib.OIDMetadata
		includeIndex    bool
		wantContains    []string
		wantNotContains []string
	}{
		{
			name: "with index",
			oid: &mib.OIDMetadata{
				Name: "ifInOctets",
			},
			includeIndex: true,
			wantContains: []string{"{{device}}", "{{index}}", "ifInOctets"},
		},
		{
			name: "without index",
			oid: &mib.OIDMetadata{
				Name: "sysDescr",
			},
			includeIndex:    false,
			wantContains:    []string{"{{device}}", "sysDescr"},
			wantNotContains: []string{"{{index}}"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := qb.buildLegendFormat(tt.oid, tt.includeIndex)

			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("buildLegendFormat() = %v, want to contain %v", got, want)
				}
			}

			for _, notWant := range tt.wantNotContains {
				if strings.Contains(got, notWant) {
					t.Errorf("buildLegendFormat() = %v, want to NOT contain %v", got, notWant)
				}
			}
		})
	}
}

// Benchmark tests
func BenchmarkQueryBuilder_BuildScalarQuery(b *testing.B) {
	qb := NewQueryBuilder("snmp___")
	oid := &mib.OIDMetadata{
		OID:      "1.3.6.1.2.1.1.1.0",
		Name:     "sysDescr",
		Type:     "OctetString",
		IsScalar: true,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = qb.BuildScalarQuery(oid, true)
	}
}

func BenchmarkQueryBuilder_BuildCounterQuery(b *testing.B) {
	qb := NewQueryBuilder("snmp___")
	oid := &mib.OIDMetadata{
		OID:  "1.3.6.1.2.1.2.2.1.10",
		Name: "ifInOctets",
		Type: "Counter32",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = qb.BuildCounterQuery(oid, true, "5m")
	}
}

func BenchmarkQueryBuilder_metricName(b *testing.B) {
	qb := NewQueryBuilder("snmp___")
	oid := &mib.OIDMetadata{OID: ".1.3.6.1.2.1.2.2.1.10", Name: "ifInOctets", IsColumn: true}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = qb.metricName(oid)
	}
}
