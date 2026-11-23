package builder_test

import (
	"fmt"

	"github.com/jeremyhahn/go-netmon/internal/grafana"
	"github.com/jeremyhahn/go-netmon/internal/grafana/builder"
	"github.com/jeremyhahn/go-netmon/internal/mib"
)

func ExampleQueryBuilder_BuildQuery() {
	qb := builder.NewQueryBuilder("")

	// Counter OID
	counterOID := &mib.OIDMetadata{
		OID:  "1.3.6.1.2.1.2.2.1.10",
		Name: "ifInOctets",
		Type: "Counter32",
	}
	fmt.Println(qb.BuildQuery(counterOID))

	// Gauge OID
	gaugeOID := &mib.OIDMetadata{
		OID:  "1.3.6.1.2.1.2.2.1.8",
		Name: "ifOperStatus",
		Type: "Integer32",
	}
	fmt.Println(qb.BuildQuery(gaugeOID))

	// Output:
	// (rate(snmp_ifInOctets{device=~"$device"}[5m])) * 8
	// snmp_ifOperStatus{device=~"$device"}
}

func ExampleQueryBuilder_BuildTableQuery() {
	qb := builder.NewQueryBuilder("")

	tableOID := "1.3.6.1.2.1.2.2"
	columns := []*mib.OIDMetadata{
		{OID: "1.3.6.1.2.1.2.2.1.10", Name: "ifInOctets", IsColumn: true},
		{OID: "1.3.6.1.2.1.2.2.1.16", Name: "ifOutOctets", IsColumn: true},
	}

	query := qb.BuildTableQuery(tableOID, columns)
	fmt.Println(query)

	// Output:
	// {__name__=~"snmp_ifInOctets|snmp_ifOutOctets",device=~"$device"}
}

func ExampleQueryBuilder_BuildLegendFormat() {
	qb := builder.NewQueryBuilder("")

	// Scalar OID
	scalarOID := &mib.OIDMetadata{
		OID:      "1.3.6.1.2.1.1.3.0",
		Name:     "sysUpTime",
		IsScalar: true,
	}
	fmt.Println(qb.BuildLegendFormat(scalarOID))

	// Table column
	columnOID := &mib.OIDMetadata{
		OID:      "1.3.6.1.2.1.2.2.1.10",
		Name:     "ifInOctets",
		IsColumn: true,
	}
	fmt.Println(qb.BuildLegendFormat(columnOID))

	// Output:
	// {{device}} - sysUpTime
	// {{device}} - ifInOctets - {{oid}}
}

func ExampleQueryBuilder_BuildDeviceSelectorQuery() {
	qb := builder.NewQueryBuilder("")

	query := qb.BuildDeviceSelectorQuery()
	fmt.Println(query)

	// Output:
	// label_values(device)
}

func ExampleObjectMetricName() {
	// The exporter's own prefix.
	fmt.Println(builder.ObjectMetricName("sysUpTime", ""))

	// A caller-chosen prefix, and a name that has to be sanitized.
	fmt.Println(builder.ObjectMetricName("cpmCPUTotal-5sec", "netmon_"))

	// Output:
	// snmp_sysUpTime
	// netmon_cpmCPUTotal_5sec
}

func ExampleNewDashboard() {
	// Create a new dashboard
	dashboard := grafana.NewDashboard("snmp-overview", "SNMP Overview")
	dashboard.Description = "Overview of SNMP metrics"
	dashboard.Tags = []string{"snmp", "network"}

	// Add a device selector variable
	deviceVar := grafana.NewQueryVariable("device", "Device", "label_values(device)")
	dashboard.AddVariable(deviceVar)

	// Create a time series panel
	panel := grafana.NewTimeSeriesPanel(1, "Interface Traffic", grafana.GridPos{
		X: 0,
		Y: 0,
		W: 12,
		H: 8,
	})
	panel.SetUnit("bps")

	// Add a query target
	target := grafana.NewTarget("A", "rate(snmp_ifInOctets{device=~\"$device\"}[5m]) * 8", "{{device}} - In")
	panel.AddTarget(target)

	dashboard.AddPanel(panel)

	fmt.Printf("Dashboard: %s (%d panels)\n", dashboard.Title, len(dashboard.Panels))
	fmt.Printf("Variables: %d\n", len(dashboard.Templating.List))

	// Output:
	// Dashboard: SNMP Overview (1 panels)
	// Variables: 1
}
