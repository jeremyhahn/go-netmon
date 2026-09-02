package api

import (
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/metrics"
	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/promname"
)

// Which modules hold data, and for whom.
//
// The dashboard asks this every refresh to notice a newly assigned MIB that has
// started producing. It used to ask the fleet-wide question and compare the
// answer with the modules drawn for one device: 13 against 2 on a four-device
// stack. The sets differ by construction, so it found "new" data on every tick
// and tore the dashboard down and rebuilt it every thirty seconds, discarding
// the streamed history each time.
//
// Two things had to be true for the fix. The question has to be answerable per
// device, and the evidence used to answer it has to be per device too.

// objects is a module's worth of metadata. scalar and column come from
// mib_cost_test.go, which describes the same objects for the cost estimator.
func objects(metas ...*mib.OIDMetadata) []*mib.OIDMetadata { return metas }

func tableEntry(entryOID string) *mib.OIDMetadata {
	return &mib.OIDMetadata{
		OID: entryOID, Name: "ifEntry", EntryOID: entryOID,
		Access: "not-accessible", Status: "current", IsTableEntry: true,
	}
}

// TestAModuleWithAMatchingMetricHasData is the ordinary case.
func TestAModuleWithAMatchingMetricHasData(t *testing.T) {
	present := map[string]bool{promname.MetricName("sysUpTime"): true}

	if !moduleHasData(objects(scalar(sysUpTimeOID, "sysUpTime")), nil, present) {
		t.Error("a module whose scalar is in Prometheus was reported as having no data")
	}
	if moduleHasData(objects(scalar("1.3.6.1.4.1.21317.1.3.1.13.1.9.4", "temperature")), nil, present) {
		t.Error("a module with no matching metric was reported as having data")
	}
}

// TestATableColumnCountsAsData is what made per-device scoping possible.
//
// The check used to consider only scalars, so a module that is all table columns
// — IF-MIB, most of them — could never answer yes on metric names alone. That is
// why the fleet-wide learned tables were consulted, and why the answer could not
// be narrowed to one device.
func TestATableColumnCountsAsData(t *testing.T) {
	present := map[string]bool{promname.MetricName("ifInOctets"): true}

	if !moduleHasData(objects(column(ifInOctets, "ifInOctets")), nil, present) {
		t.Error("a table column in Prometheus was not counted as data")
	}
}

// TestATextReadingCountsAsData covers the other metric shape: a reading whose
// value is words arrives as an info metric, and a device reporting only those
// still has data.
func TestATextReadingCountsAsData(t *testing.T) {
	present := map[string]bool{promname.InfoMetricName("PSU2Status"): true}

	if !moduleHasData(objects(scalar("1.3.6.1.4.1.21317.1.3.1.13.1.9.2", "PSU2Status")), nil, present) {
		t.Error("a module whose only reading is text was reported as having no data")
	}
}

// TestLearnedTablesAnswerOnlyTheFleetWideQuestion is the bug itself.
//
// The OID learner discovers tables across every device, so a table it has walked
// says nothing about whether a particular device has data. Passing those as
// evidence for a device-scoped question is what made 13 modules look present for
// a device with 2.
func TestLearnedTablesAnswerOnlyTheFleetWideQuestion(t *testing.T) {
	const entryOID = ifEntryOID
	walked := map[string]*metrics.LearnedTableInfo{entryOID: {}}
	module := objects(tableEntry(entryOID), column(ifInOctets, "ifInOctets"))

	// Fleet-wide: the caller supplies the learned tables, and a table anyone has
	// walked is evidence the module produces data somewhere.
	if !moduleHasData(module, walked, nil) {
		t.Error("a walked table was not counted for the fleet-wide question")
	}

	// Per device: the caller supplies no learned tables, because they are not
	// evidence about one device. With no metric for this device, the answer is
	// no -- which is what stops the dashboard reloading forever.
	if moduleHasData(module, nil, map[string]bool{promname.MetricName("somethingElse"): true}) {
		t.Error("a module with no metric for this device was reported as having data for it; " +
			"the dashboard will reload on every refresh")
	}
}

// TestAnUnpollableObjectIsNotEvidence keeps a module from claiming data on the
// strength of an object the poller never asks for, including one left without an
// OID by an unresolved import.
func TestAnUnpollableObjectIsNotEvidence(t *testing.T) {
	unresolved := &mib.OIDMetadata{
		Name: "kvmPortCount", Access: "read-only", Status: "current", IsScalar: true,
	}
	// Pollable is the stored flag; IsPollable() is the rule. Both say no.
	if unresolved.IsPollable() {
		t.Fatal("an object with no OID reported itself pollable")
	}

	present := map[string]bool{promname.MetricName("kvmPortCount"): true}
	if moduleHasData(objects(unresolved), nil, present) {
		t.Error("an object the poller can never ask for was counted as evidence of data")
	}
}

// TestNoEvidenceMeansNoData covers the empty case in both directions: nothing
// collected, and nothing defined.
func TestNoEvidenceMeansNoData(t *testing.T) {
	if moduleHasData(objects(scalar(sysUpTimeOID, "sysUpTime")), nil, nil) {
		t.Error("a module was reported as having data with nothing collected")
	}
	if moduleHasData(nil, map[string]*metrics.LearnedTableInfo{"1.3.6.1": {}},
		map[string]bool{"snmp_anything": true}) {
		t.Error("a module defining no objects was reported as having data")
	}
}
