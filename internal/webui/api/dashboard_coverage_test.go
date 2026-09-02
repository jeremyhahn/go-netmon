package api

import (
	"context"
	"strings"
	"testing"
)

// Explaining a dashboard that comes back without the MIB you just assigned.
//
// These notes changed shape when the dashboard stopped parsing MIBs, and one
// of the old outcomes is now unanswerable ON PURPOSE. The UI used to claim a
// module "has no pollable objects, so it will never add a panel" -- it could
// only know that by parsing the module itself, which is the coupling this
// design removes. Reading Prometheus can distinguish "reporting nothing" from
// "reporting things I cannot describe"; it cannot distinguish "will never
// report" from "has not reported yet", so it no longer pretends to.
//
// Every test here drives the real generator. An earlier version called
// coverageNote with structs assembled by hand, which proved the notes read
// well and nothing else: the generator could not actually produce the input
// that triggered the most important one, and the test could not notice.

// coverageFor finds one module's row in a generated dashboard.
func coverageFor(t *testing.T, dash Dashboard, module string) MIBCoverage {
	t.Helper()

	rows := dash.MIBs
	for _, row := range rows {
		if row.MIB == module {
			return row
		}
	}

	named := make([]string, 0, len(rows))
	for _, row := range rows {
		named = append(named, row.MIB)
	}
	t.Fatalf("no coverage row for %s; the table names %v", module, named)
	return MIBCoverage{}
}

// warningsOf returns the notes the dashboard would show an operator.
func warningsOf(t *testing.T, dash Dashboard) []string {
	t.Helper()

	if dash.Warnings == nil {
		t.Fatal("the dashboard carries no warnings list; the caller ranges over it")
	}
	return dash.Warnings
}

// TestAModuleReportingNothingTellsTheReaderToWait is the case that was once
// mistaken for a bug -- a MIB assigned a moment ago, before the first poll.
//
// It is also the case the generator could not produce. Coverage was built only
// while iterating the metrics a device REPORTS, so a module reporting nothing
// contributed no row and disappeared from the table instead of explaining
// itself. The note existed; nothing could reach it.
func TestAModuleReportingNothingTellsTheReaderToWait(t *testing.T) {
	// The device answers for IF-MIB and says nothing about the KVM. Both are
	// assigned.
	s := catalogStub(t,
		[]map[string]interface{}{
			{catMetric: "snmp_ifInOctets", catModule: "IF-MIB", catKind: "counter",
				catDescription: "octets in"},
		},
		nil,
		"snmp_ifInOctets")

	dash := s.generateDashboard(context.Background(), namedDevice("kvm-01"), []string{"IF-MIB", "RemoteKVMDevice-MIB"}, "", scopeDevice)

	cov := coverageFor(t, dash, "RemoteKVMDevice-MIB")
	if cov.Reporting != 0 || cov.Charts != 0 {
		t.Errorf("coverage = %+v, want nothing reported and nothing charted", cov)
	}
	// The actionable part: this resolves itself, and the reader should wait
	// rather than go hunting for a fault.
	if !strings.Contains(cov.Note, "poll interval") {
		t.Errorf("the note does not tell the reader to wait for a poll: %q", cov.Note)
	}
	// It must NOT claim permanence. That claim needed the MIB parsed, and
	// asserting it from Prometheus alone would be a guess presented as fact.
	if strings.Contains(cov.Note, "never") {
		t.Errorf("the note claims permanence it cannot know: %q", cov.Note)
	}
	// And it has to reach the operator, not just sit in the table.
	if got := warningsOf(t, dash); len(got) != 1 || !strings.Contains(got[0], "RemoteKVMDevice-MIB") {
		t.Errorf("warnings = %v, want the silent module explained", got)
	}
}

// TestUncatalogedMetricsExplainThemselvesWithoutReadingAsAFault covers the
// bucket that has no equivalent in the old model.
//
// A metric with no catalog row is still charted -- it has data by
// construction. The note has to say why it is unlabeled without implying
// breakage, because a raw-OID name is the ordinary outcome for an OID no
// loaded MIB describes.
func TestUncatalogedMetricsExplainThemselvesWithoutReadingAsAFault(t *testing.T) {
	s := catalogStub(t, nil, nil,
		"snmp_1_3_6_1_4_1_9999_1", "snmp_1_3_6_1_4_1_9999_2")

	dash := s.generateDashboard(context.Background(), namedDevice("dist-01"), nil, "", scopeDevice)

	cov := coverageFor(t, dash, unattributedModule)
	if cov.Reporting != 2 || cov.Charts != 2 {
		t.Errorf("coverage = %+v, want both metrics reported and both charted", cov)
	}
	if !strings.Contains(cov.Note, "raw-OID") {
		t.Errorf("the note does not name the expected cause: %q", cov.Note)
	}
	// It should also say what a CHANGE in the number would mean, since the
	// count is the coverage signal an operator watches.
	if !strings.Contains(cov.Note, "emitter") && !strings.Contains(cov.Note, "resolving") {
		t.Errorf("the note does not say what a jump in the count would mean: %q", cov.Note)
	}
}

// TestAFullyDescribedModuleIsSilent guards the notes from firing on a working
// dashboard. A warning that always appears is a warning nobody reads.
func TestAFullyDescribedModuleIsSilent(t *testing.T) {
	s := catalogStub(t,
		[]map[string]interface{}{
			{catMetric: "snmp_ifInOctets", catModule: "IF-MIB", catKind: "counter",
				catDescription: "octets in"},
			{catMetric: "snmp_ifOutOctets", catModule: "IF-MIB", catKind: "counter",
				catDescription: "octets out"},
		},
		nil,
		"snmp_ifInOctets", "snmp_ifOutOctets")

	dash := s.generateDashboard(context.Background(), namedDevice("dist-01"), []string{"IF-MIB"}, "", scopeDevice)

	if cov := coverageFor(t, dash, "IF-MIB"); cov.Note != "" {
		t.Errorf("a fully described module was warned about: %q", cov.Note)
	}
	if got := warningsOf(t, dash); len(got) != 0 {
		t.Errorf("warnings = %v, want none on a healthy dashboard", got)
	}
}

// TestAModuleTheCatalogCannotLabelIsReportedWithBothCounts is the middle case:
// the panels exist, but some carry no words at all.
//
// This is what an unresolved MIB import looks like from the dashboard. gosmi
// resolves no type node for the object, so it reaches the catalog with a row
// but no description, unit or enumeration -- a panel with a line on it and
// nothing to say what the line is.
func TestAModuleTheCatalogCannotLabelIsReportedWithBothCounts(t *testing.T) {
	s := catalogStub(t,
		[]map[string]interface{}{
			{catMetric: "snmp_ifInOctets", catModule: "IF-MIB", catKind: "counter",
				catDescription: "octets in"},
			// Type never resolved: a row, and nothing on it.
			{catMetric: "snmp_ifMystery", catModule: "IF-MIB", catKind: "unknown"},
		},
		nil,
		"snmp_ifInOctets", "snmp_ifMystery")

	cov := coverageFor(t,
		s.generateDashboard(context.Background(), namedDevice("dist-01"), []string{"IF-MIB"}, "", scopeDevice), "IF-MIB")

	if cov.Reporting != 2 || cov.Undescribed != 1 {
		t.Fatalf("coverage = %+v, want 2 reporting and 1 of them unlabeled", cov)
	}
	if !strings.Contains(cov.Note, "2") || !strings.Contains(cov.Note, "1") {
		t.Errorf("the note does not carry both counts: %q", cov.Note)
	}
	if !strings.Contains(cov.Note, "description") {
		t.Errorf("the note does not say what is missing: %q", cov.Note)
	}
}

// TestDerivedFamiliesAreNotWarnedAbout keeps the unattributed bucket from
// crying wolf.
//
// It groups the metrics no MIB module CLAIMS, which is not the same as the
// metrics the catalog cannot describe. Optical power is published under a name
// derived from its unit, carries a unit and a description, and has no MIB
// object behind it -- perfectly healthy, and it landed in the bucket whose note
// announced it as a metric that had stopped resolving. On core-01 that note
// appeared on every tab, about snmp_system_info, which the catalog describes.
func TestDerivedFamiliesAreNotWarnedAbout(t *testing.T) {
	s := catalogStub(t,
		[]map[string]interface{}{
			{catMetric: "snmp_entity_sensor_dbm", catUnit: "dBm", catKind: "gauge",
				catDescription: "optical power"},
		},
		nil,
		"snmp_entity_sensor_dbm")

	dash := s.generateDashboard(context.Background(), namedDevice("dist-01"), nil, "", scopeDevice)

	if cov := coverageFor(t, dash, unattributedModule); cov.Note != "" {
		t.Errorf("a described family was warned about: %q", cov.Note)
	}
	if got := warningsOf(t, dash); len(got) != 0 {
		t.Errorf("warnings = %v, want none", got)
	}
}

// TestTheUnlabelableOnesAreStillCounted is the other half: a raw-OID metric
// really cannot be labeled, and the count is what an operator watches.
func TestTheUnlabelableOnesAreStillCounted(t *testing.T) {
	s := catalogStub(t,
		[]map[string]interface{}{
			{catMetric: "snmp_entity_sensor_dbm", catUnit: "dBm", catKind: "gauge",
				catDescription: "optical power"},
		},
		nil,
		"snmp_entity_sensor_dbm", "snmp_1_3_6_1_4_1_9999")

	cov := coverageFor(t,
		s.generateDashboard(context.Background(), namedDevice("dist-01"), nil, "", scopeDevice),
		unattributedModule)

	if cov.Reporting != 2 || cov.Undescribed != 1 {
		t.Fatalf("coverage = %+v, want 2 reporting and 1 of them unlabelable", cov)
	}
	if !strings.Contains(cov.Note, "1 of the 2") {
		t.Errorf("the note does not separate the two counts: %q", cov.Note)
	}
	if !strings.Contains(cov.Note, "raw-OID") {
		t.Errorf("the note does not name the expected cause: %q", cov.Note)
	}
}
