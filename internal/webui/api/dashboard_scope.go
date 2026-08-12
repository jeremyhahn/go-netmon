package api

import (
	"github.com/jeremyhahn/go-netmon/internal/webui/dashboard"
)

// Narrowing a MIB's dashboard to one device.
//
// A dashboard specification is generated from a MIB module, which describes what
// a device of that kind *could* report. What a particular device does report is
// a smaller set: it may not implement every object, and a filter may narrow it
// further.
//
// The web UI renders the module's specification directly, so every object the
// module defines became a card, and a card whose metric the device never sends
// sits on "Waiting for data…" forever. On this fleet that was 507 cards across
// five devices -- 175 of lab-node-1-bmc's 257, and 160 of core-01's 456.
//
// Nothing about that state is recoverable by waiting, and nothing distinguishes
// it from a card whose first sample is merely slow. The cards are dropped
// instead, against what the device is actually reporting.

// scopeSpecToDevice returns the parts of spec that device reports.
//
// reported is the set of metric names Prometheus currently holds for the device.
// An empty set means the question could not be answered -- Prometheus is down,
// or the device has not been polled yet -- and the specification is returned
// whole rather than emptied, because showing every card is a better failure than
// showing none.
func scopeSpecToDevice(spec *dashboard.Spec, reported map[string]bool) *dashboard.Spec {
	if spec == nil || len(reported) == 0 {
		return spec
	}

	scoped := &dashboard.Spec{MIBName: spec.MIBName}

	for _, widget := range spec.ScalarWidgets {
		if reported[widget.Metric] {
			scoped.ScalarWidgets = append(scoped.ScalarWidgets, widget)
		}
	}

	for _, table := range spec.TableWidgets {
		if narrowed, keep := scopeTableToDevice(table, reported); keep {
			scoped.TableWidgets = append(scoped.TableWidgets, narrowed)
		}
	}

	return scoped
}

// scopeTableToDevice drops the columns of one table widget that the device does
// not report, and reports whether anything is left worth rendering.
//
// A table whose every column is gone takes its instance selector with it: the
// selector is a control for choosing a row, and a row picker over no data is
// worse than nothing, since it looks like a table that failed to load.
func scopeTableToDevice(
	table dashboard.TableWidget, reported map[string]bool,
) (dashboard.TableWidget, bool) {
	scoped := dashboard.TableWidget{
		TableName:        table.TableName,
		InstanceSelector: table.InstanceSelector,
	}

	for _, field := range table.StatusFields {
		if reported[field.Metric] {
			scoped.StatusFields = append(scoped.StatusFields, field)
		}
	}
	for _, chart := range table.MetricCharts {
		if reported[chart.Metric] {
			scoped.MetricCharts = append(scoped.MetricCharts, chart)
		}
	}

	return scoped, len(scoped.StatusFields) > 0 || len(scoped.MetricCharts) > 0
}
