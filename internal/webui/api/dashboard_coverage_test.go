package api

import (
	"strings"
	"testing"
)

// Explaining a dashboard that comes back without the MIB you just assigned.
//
// A panel is generated only for an object Prometheus already holds data for. So
// a MIB assigned a moment ago contributes nothing until the poller has run and
// the device has answered — and that is indistinguishable, from the dashboard,
// from a MIB that will never contribute anything. Clicking "Generate dashboard"
// on a device and finding the new MIB absent was reported as a bug when it was
// the first case.
//
// These are the three outcomes, and each needs different advice.

// TestNotYetPolledIsDistinguishedFromNeverWillBe is the case that was mistaken
// for a bug: pollable objects, no data yet.
func TestNotYetPolledIsDistinguishedFromNeverWillBe(t *testing.T) {
	note := coverageNote(MIBCoverage{MIB: "RemoteKVMDevice-MIB", Pollable: 12, Charts: 0})

	if note == "" {
		t.Fatal("a MIB that contributed no panels produced no explanation")
	}
	if !strings.Contains(note, "12 pollable objects") {
		t.Errorf("the note does not say the MIB has pollable objects: %s", note)
	}
	// The actionable part: this resolves itself, and the reader should be told
	// to wait rather than go looking for a fault.
	if !strings.Contains(note, "poll interval") {
		t.Errorf("the note does not tell the reader to wait for a poll: %s", note)
	}
	if strings.Contains(note, "never") {
		t.Errorf("a MIB that will produce data once polled was described as never will: %s", note)
	}
}

// TestNothingPollableSaysItNeverWill is the other case, which no amount of
// waiting fixes.
func TestNothingPollableSaysItNeverWill(t *testing.T) {
	note := coverageNote(MIBCoverage{MIB: "ATEN-PRODUCTS-MIB", Pollable: 0, Charts: 0})

	if !strings.Contains(note, "never") {
		t.Errorf("a MIB with nothing pollable was not reported as permanent: %s", note)
	}
	if strings.Contains(note, "poll interval") {
		t.Errorf("a MIB that will never produce data was described as merely unpolled: %s", note)
	}
}

// TestAFullyCoveredMIBIsSilent guards the notes from firing on a working
// dashboard. A warning that always appears is a warning nobody reads.
func TestAFullyCoveredMIBIsSilent(t *testing.T) {
	if note := coverageNote(MIBCoverage{MIB: "IF-MIB", Pollable: 24, Charts: 24}); note != "" {
		t.Errorf("a fully covered MIB was warned about: %s", note)
	}
	// A MIB with nothing pollable and nothing charted is not "covered": that is
	// the registry case above, and it must not fall through to silence.
	if note := coverageNote(MIBCoverage{MIB: "EMPTY-MIB"}); note == "" {
		t.Error("a MIB with no pollable objects was passed over in silence")
	}
}

// TestPartialCoverageIsReportedWithBothCounts covers the common steady state: a
// device answers some of a module's objects and not others.
func TestPartialCoverageIsReportedWithBothCounts(t *testing.T) {
	note := coverageNote(MIBCoverage{MIB: "SNMPv2-MIB", Pollable: 41, Charts: 7})

	if !strings.Contains(note, "7 panels of 41") {
		t.Errorf("the note does not report both counts: %s", note)
	}
	// This one is ambiguous by nature — not yet polled, or not answered — and
	// saying so is more useful than picking one.
	if !strings.Contains(note, "not answered") {
		t.Errorf("the note does not admit the device may not answer those OIDs: %s", note)
	}
}

// TestCoverageWarningsCollectsOnlyTheNotes checks what reaches the client: the
// warnings list drives a notification per entry, so a silent MIB must not
// contribute an empty one.
func TestCoverageWarningsCollectsOnlyTheNotes(t *testing.T) {
	coverage := []MIBCoverage{
		{MIB: "IF-MIB", Pollable: 24, Charts: 24, Note: ""},
		{MIB: "ATEN-PRODUCTS-MIB", Pollable: 0, Charts: 0,
			Note: coverageNote(MIBCoverage{MIB: "ATEN-PRODUCTS-MIB"})},
		{MIB: "RemoteKVMDevice-MIB", Pollable: 12, Charts: 0,
			Note: coverageNote(MIBCoverage{MIB: "RemoteKVMDevice-MIB", Pollable: 12})},
	}

	warnings := coverageWarnings(coverage)

	if len(warnings) != 2 {
		t.Fatalf("collected %d warnings, want 2: %v", len(warnings), warnings)
	}
	for _, w := range warnings {
		if w == "" {
			t.Error("an empty warning would render as a blank notification")
		}
	}
	// Order follows the MIBs, so a reader can match a warning to the module.
	if !strings.Contains(warnings[0], "ATEN-PRODUCTS-MIB") {
		t.Errorf("warnings are not in MIB order: %v", warnings)
	}

	// No warnings must be an empty list rather than nil, so the JSON carries
	// "warnings": [] and the client's `.length` check does not have to guard
	// against a null.
	if empty := coverageWarnings(nil); empty == nil || len(empty) != 0 {
		t.Errorf("no warnings gave %#v, want an empty slice", empty)
	}
}
