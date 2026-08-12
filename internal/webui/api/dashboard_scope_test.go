package api

import (
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/webui/dashboard"
)

// Narrowing a MIB's dashboard to what one device reports.
//
// The specification is generated from a MIB module: it describes what a device
// of that kind could report. The web UI rendered it whole, so every object the
// module defines became a card, and a card for an object this device does not
// implement sat on "Waiting for data…" forever. Measured in a browser across
// five devices: 507 such cards, including 175 of lab-node-1-bmc's 257.

func scalarWidget(name, metric string) dashboard.ScalarWidget {
	return dashboard.ScalarWidget{
		Type: "chart", Name: name, Metric: metric,
		OID: "1.3.6.1.2.1.1.1", ChartType: "line",
	}
}

// TestAWidgetTheDeviceDoesNotReportIsDropped is the case that filled the
// dashboard with cards that could never draw.
func TestAWidgetTheDeviceDoesNotReportIsDropped(t *testing.T) {
	spec := &dashboard.Spec{
		MIBName: "HOST-RESOURCES-MIB",
		ScalarWidgets: []dashboard.ScalarWidget{
			scalarWidget("hrSystemUptime", "snmp_hrSystemUptime"),
			scalarWidget("hrSWInstalledLastChange", "snmp_hrSWInstalledLastChange"),
		},
	}
	reported := map[string]bool{"snmp_hrSystemUptime": true}

	scoped := scopeSpecToDevice(spec, reported)

	if len(scoped.ScalarWidgets) != 1 {
		t.Fatalf("kept %d widgets, want the 1 the device reports", len(scoped.ScalarWidgets))
	}
	if scoped.ScalarWidgets[0].Name != "hrSystemUptime" {
		t.Errorf("kept %q, want hrSystemUptime", scoped.ScalarWidgets[0].Name)
	}
	if scoped.MIBName != "HOST-RESOURCES-MIB" {
		t.Errorf("MIBName = %q, want it carried through", scoped.MIBName)
	}
}

// TestATableKeepsOnlyTheColumnsTheDeviceReports covers the other half. Table
// widgets carry most of the cards on a real dashboard, so scoping only the
// scalars would leave the problem in place.
func TestATableKeepsOnlyTheColumnsTheDeviceReports(t *testing.T) {
	spec := &dashboard.Spec{
		MIBName: "IF-MIB",
		TableWidgets: []dashboard.TableWidget{{
			TableName:        "ifTable",
			InstanceSelector: dashboard.InstanceSelector{LabelName: "ifindex"},
			StatusFields: []dashboard.StatusField{
				{Name: "ifOperStatus", Metric: "snmp_ifOperStatus"},
				{Name: "ifAlias", Metric: "snmp_ifalias_info"},
			},
			MetricCharts: []dashboard.MetricChart{
				{Name: "ifHCInOctets", Metric: "snmp_ifHCInOctets"},
				{Name: "ifOutQLen", Metric: "snmp_ifOutQLen"},
			},
		}},
	}
	reported := map[string]bool{
		"snmp_ifOperStatus": true,
		"snmp_ifHCInOctets": true,
	}

	scoped := scopeSpecToDevice(spec, reported)

	if len(scoped.TableWidgets) != 1 {
		t.Fatalf("kept %d tables, want 1", len(scoped.TableWidgets))
	}
	table := scoped.TableWidgets[0]
	if len(table.StatusFields) != 1 || table.StatusFields[0].Name != "ifOperStatus" {
		t.Errorf("status fields = %v, want only ifOperStatus", table.StatusFields)
	}
	if len(table.MetricCharts) != 1 || table.MetricCharts[0].Name != "ifHCInOctets" {
		t.Errorf("metric charts = %v, want only ifHCInOctets", table.MetricCharts)
	}
	// The row picker has to survive, or the columns that remain cannot be read.
	if table.InstanceSelector.LabelName != "ifindex" {
		t.Errorf("the instance selector was lost: %+v", table.InstanceSelector)
	}
}

// TestATableWithNothingLeftIsDropped keeps a row picker from being rendered over
// no data, which reads as a table that failed to load rather than one that does
// not apply.
func TestATableWithNothingLeftIsDropped(t *testing.T) {
	spec := &dashboard.Spec{
		MIBName: "ENTITY-MIB",
		TableWidgets: []dashboard.TableWidget{{
			TableName:    "entLogicalTable",
			StatusFields: []dashboard.StatusField{{Name: "entLogicalDescr", Metric: "snmp_entlogicaldescr_info"}},
			MetricCharts: []dashboard.MetricChart{{Name: "entLogicalIndex", Metric: "snmp_entLogicalIndex"}},
		}},
	}

	scoped := scopeSpecToDevice(spec, map[string]bool{"snmp_somethingElse": true})

	if len(scoped.TableWidgets) != 0 {
		t.Errorf("a table with no reported column survived: %+v", scoped.TableWidgets)
	}
}

// TestNoEvidenceLeavesTheSpecWhole is the failure case, and it fails open.
//
// An empty set means the question could not be answered -- Prometheus is down,
// or the device has not been polled yet. Emptying the dashboard then would turn
// a monitoring outage into a dashboard that claims the device has nothing.
func TestNoEvidenceLeavesTheSpecWhole(t *testing.T) {
	spec := &dashboard.Spec{
		MIBName: "IF-MIB",
		ScalarWidgets: []dashboard.ScalarWidget{
			scalarWidget("ifNumber", "snmp_ifNumber"),
		},
		TableWidgets: []dashboard.TableWidget{{
			TableName:    "ifTable",
			MetricCharts: []dashboard.MetricChart{{Name: "ifHCInOctets", Metric: "snmp_ifHCInOctets"}},
		}},
	}

	for _, reported := range []map[string]bool{nil, {}} {
		scoped := scopeSpecToDevice(spec, reported)
		if len(scoped.ScalarWidgets) != 1 || len(scoped.TableWidgets) != 1 {
			t.Errorf("with no evidence the spec was narrowed to %d scalars and %d tables, want it whole",
				len(scoped.ScalarWidgets), len(scoped.TableWidgets))
		}
	}

	// And a nil spec is not a crash.
	if scopeSpecToDevice(nil, map[string]bool{"x": true}) != nil {
		t.Error("a nil spec came back as something")
	}
}

// TestScopingKeepsEverythingADeviceReports guards against the opposite failure:
// dropping cards that would have drawn. That would be worse than the bug, since
// the data exists and the operator simply never sees it.
func TestScopingKeepsEverythingADeviceReports(t *testing.T) {
	spec := &dashboard.Spec{
		MIBName: "SNMPv2-MIB",
		ScalarWidgets: []dashboard.ScalarWidget{
			scalarWidget("sysUpTime", "snmp_sysUpTime"),
			scalarWidget("sysDescr", "snmp_sysdescr_info"),
		},
		TableWidgets: []dashboard.TableWidget{{
			TableName:    "sysORTable",
			StatusFields: []dashboard.StatusField{{Name: "sysORDescr", Metric: "snmp_sysordescr_info"}},
			MetricCharts: []dashboard.MetricChart{{Name: "sysORUpTime", Metric: "snmp_sysORUpTime"}},
		}},
	}
	reported := map[string]bool{
		"snmp_sysUpTime": true, "snmp_sysdescr_info": true,
		"snmp_sysordescr_info": true, "snmp_sysORUpTime": true,
	}

	scoped := scopeSpecToDevice(spec, reported)

	if len(scoped.ScalarWidgets) != 2 {
		t.Errorf("kept %d scalars, want both", len(scoped.ScalarWidgets))
	}
	if len(scoped.TableWidgets) != 1 {
		t.Fatalf("kept %d tables, want 1", len(scoped.TableWidgets))
	}
	if len(scoped.TableWidgets[0].StatusFields) != 1 || len(scoped.TableWidgets[0].MetricCharts) != 1 {
		t.Error("a fully reported table lost a column")
	}
}
