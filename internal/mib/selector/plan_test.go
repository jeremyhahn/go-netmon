package selector

import (
	"fmt"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/mib"
)

const (
	ifTableOID = "1.3.6.1.2.1.2.2"
	ifEntryOID = "1.3.6.1.2.1.2.2.1"
)

// planFixture is a small IF-MIB/CISCO-PROCESS-MIB slice covering the shapes
// that matter: a table node, its entry, readable columns, a readable and a
// not-accessible index column, a deprecated object and a scalar.
func planFixture() []*mib.OIDMetadata {
	return []*mib.OIDMetadata{
		{OID: ifTableOID, Name: "ifTable", Module: "IF-MIB",
			Access: "not-accessible", Status: "current", IsTable: true},
		{OID: ifEntryOID, Name: "ifEntry", Module: "IF-MIB",
			Access: "not-accessible", Status: "current", IsTableEntry: true},
		{OID: "1.3.6.1.2.1.2.2.1.1", Name: "ifIndex", Module: "IF-MIB",
			Type: "InterfaceIndex", Access: "read-only", Status: "current",
			IsColumn: true, IsIndex: true, TableOID: ifTableOID},
		{OID: "1.3.6.1.2.1.2.2.1.10", Name: "ifInOctets", Module: "IF-MIB",
			Type: "Counter32", Access: "read-only", Status: "current",
			IsColumn: true, TableOID: ifTableOID},
		{OID: "1.3.6.1.2.1.2.2.1.16", Name: "ifOutOctets", Module: "IF-MIB",
			Type: "Counter32", Access: "read-only", Status: "current",
			IsColumn: true, TableOID: ifTableOID},
		{OID: "1.3.6.1.2.1.2.2.1.21", Name: "ifSpecific", Module: "IF-MIB",
			Type: "OBJECT IDENTIFIER", Access: "read-only", Status: "deprecated",
			IsColumn: true, TableOID: ifTableOID},
		{OID: "1.3.6.1.2.1.31.1.4.1.1", Name: "ifRcvAddressAddress", Module: "IF-MIB",
			Type: "PhysAddress", Access: "not-accessible", Status: "current",
			IsColumn: true, IsIndex: true},
		{OID: "1.3.6.1.2.1.1.3", Name: "sysUpTime", Module: "SNMPv2-MIB",
			Type: "TimeTicks", Access: "read-only", Status: "current", IsScalar: true},
	}
}

// reasons indexes a plan by object name.
func reasons(decisions []Decision) map[string]Decision {
	byName := make(map[string]Decision, len(decisions))
	for _, decision := range decisions {
		byName[decision.Object.Name] = decision
	}
	return byName
}

// assertDecision checks one object's verdict and reason.
func assertDecision(t *testing.T, byName map[string]Decision, name string, polled bool, reason Reason) {
	t.Helper()

	decision, ok := byName[name]
	if !ok {
		t.Fatalf("no decision for %s; a plan must account for every object", name)
	}
	if decision.Polled != polled {
		t.Errorf("%s: Polled = %v, want %v (reason %q)", name, decision.Polled, polled, decision.Reason)
	}
	if decision.Reason != reason {
		t.Errorf("%s: Reason = %q, want %q", name, decision.Reason, reason)
	}
}

// TestPlanUnfilteredNamesEveryReason is the specification of the unfiltered
// rule: one walk per table, nothing for the objects that walk already covers,
// and a distinct reason for each way an object can be left out.
func TestPlanUnfilteredNamesEveryReason(t *testing.T) {
	byName := reasons(Plan(planFixture(), []string{"IF-MIB"}, nil))

	assertDecision(t, byName, "ifTable", true, ReasonPolled)
	assertDecision(t, byName, "ifEntry", false, ReasonCoveredByTable)
	assertDecision(t, byName, "ifIndex", false, ReasonCoveredByTable)
	assertDecision(t, byName, "ifInOctets", false, ReasonCoveredByTable)
	assertDecision(t, byName, "ifOutOctets", false, ReasonCoveredByTable)
	assertDecision(t, byName, "ifSpecific", false, ReasonCoveredByTable)
	// A column with no table node of its own has to stand on its access.
	assertDecision(t, byName, "ifRcvAddressAddress", false, ReasonNotAccessible)
	// A module the device does not poll.
	assertDecision(t, byName, "sysUpTime", false, ReasonModuleDisabled)
}

// TestPlanAccountsForEveryObject pins the contract the API depends on: a plan
// has exactly one decision per object, in the order given.
func TestPlanAccountsForEveryObject(t *testing.T) {
	objects := planFixture()

	decisions := Plan(objects, []string{"IF-MIB", "SNMPv2-MIB"}, nil)

	if len(decisions) != len(objects) {
		t.Fatalf("got %d decisions for %d objects", len(decisions), len(objects))
	}
	for i, decision := range decisions {
		if decision.Object != objects[i] {
			t.Fatalf("decision %d is about %s, want %s in input order",
				i, decision.Object.Name, objects[i].Name)
		}
		if decision.Polled && decision.Reason != ReasonPolled {
			t.Errorf("%s is polled but carries reason %q", decision.Object.Name, decision.Reason)
		}
		if !decision.Polled && decision.Reason == ReasonPolled {
			t.Errorf("%s is not polled but carries reason %q", decision.Object.Name, decision.Reason)
		}
	}
}

// TestPlanWalksColumnsWhenTheirTableIsAbsent covers metadata where the table
// node was never stored: the readable columns then have to be fetched
// individually, because nothing else will return them.
func TestPlanWalksColumnsWhenTheirTableIsAbsent(t *testing.T) {
	// Drop the table node, keeping everything hanging off it.
	objects := planFixture()[1:]

	byName := reasons(Plan(objects, []string{"IF-MIB"}, nil))

	assertDecision(t, byName, "ifInOctets", true, ReasonPolled)
	assertDecision(t, byName, "ifIndex", true, ReasonPolled)
	assertDecision(t, byName, "ifEntry", false, ReasonCoveredByTable)
	assertDecision(t, byName, "ifSpecific", false, ReasonNotCurrent)
	assertDecision(t, byName, "ifRcvAddressAddress", false, ReasonNotAccessible)
}

// TestPlanExcludingATableRestoresItsColumns verifies the two mechanisms
// compose: excluding the table means its columns are no longer covered, so the
// readable ones are fetched on their own.
func TestPlanExcludingATableRestoresItsColumns(t *testing.T) {
	filters := map[string]config.ModuleFilter{
		"IF-MIB": {ExcludeNames: []string{"^ifTable$"}},
	}

	byName := reasons(Plan(planFixture(), []string{"IF-MIB"}, filters))

	assertDecision(t, byName, "ifTable", false, ReasonExcluded)
	assertDecision(t, byName, "ifInOctets", true, ReasonPolled)
	assertDecision(t, byName, "ifSpecific", false, ReasonNotCurrent)
}

// TestPlanIncludeNamesReportsWhyEachObjectWentMissing checks the reason a
// checkbox list needs most: an object dropped by an include list must say the
// include list dropped it, not that it is unreadable.
func TestPlanIncludeNamesReportsWhyEachObjectWentMissing(t *testing.T) {
	filters := map[string]config.ModuleFilter{
		"IF-MIB": {IncludeNames: []string{"^ifInOctets$"}},
	}

	byName := reasons(Plan(planFixture(), []string{"IF-MIB"}, filters))

	// The table is not included either, so it is not walked and the column is
	// fetched on its own.
	assertDecision(t, byName, "ifTable", false, ReasonNotIncluded)
	assertDecision(t, byName, "ifInOctets", true, ReasonPolled)
	assertDecision(t, byName, "ifOutOctets", false, ReasonNotIncluded)
	// Access is checked before the filter, so an unreadable object reports the
	// reason that would apply whatever the filter said.
	assertDecision(t, byName, "ifRcvAddressAddress", false, ReasonNotAccessible)
}

// TestPlanExcludeBeatsInclude pins the precedence rule, which is the one a user
// is most likely to get wrong.
func TestPlanExcludeBeatsInclude(t *testing.T) {
	filters := map[string]config.ModuleFilter{
		"IF-MIB": {
			IncludeNames: []string{"^if.*Octets$", "^ifTable$"},
			ExcludeNames: []string{"^ifOutOctets$"},
		},
	}

	byName := reasons(Plan(planFixture(), []string{"IF-MIB"}, filters))

	assertDecision(t, byName, "ifTable", true, ReasonPolled)
	// The table is walked, so both columns are covered regardless of the
	// filter: covering is decided before the filter is consulted.
	assertDecision(t, byName, "ifInOctets", false, ReasonCoveredByTable)
	assertDecision(t, byName, "ifOutOctets", false, ReasonCoveredByTable)
}

// TestPlanMaxOIDsCapsInOrder verifies the cap is spent in the order the objects
// arrive and that everything past it says so.
func TestPlanMaxOIDsCapsInOrder(t *testing.T) {
	// Without the table node every readable column stands alone, which gives
	// the cap something to bite on.
	objects := planFixture()[1:]
	filters := map[string]config.ModuleFilter{"IF-MIB": {MaxOIDs: 1}}

	byName := reasons(Plan(objects, []string{"IF-MIB"}, filters))

	assertDecision(t, byName, "ifIndex", true, ReasonPolled)
	assertDecision(t, byName, "ifInOctets", false, ReasonMaxOIDs)
	assertDecision(t, byName, "ifOutOctets", false, ReasonMaxOIDs)
	// A cap does not turn an unreadable object into a capped one.
	assertDecision(t, byName, "ifRcvAddressAddress", false, ReasonNotAccessible)
}

// TestPlanMaxOIDsIsPerModule checks a cap on one module leaves another alone.
func TestPlanMaxOIDsIsPerModule(t *testing.T) {
	objects := planFixture()[1:]
	filters := map[string]config.ModuleFilter{"IF-MIB": {MaxOIDs: 1}}

	byName := reasons(Plan(objects, []string{"IF-MIB", "SNMPv2-MIB"}, filters))

	assertDecision(t, byName, "sysUpTime", true, ReasonPolled)
}

// TestPlanDisabledModulePollsNothing covers a paused MIB: every object comes
// back with the same reason, so a client can say "the module is off" rather
// than listing eight different excuses.
func TestPlanDisabledModulePollsNothing(t *testing.T) {
	decisions := Plan(planFixture(), nil, nil)

	for _, decision := range decisions {
		if decision.Polled {
			t.Errorf("%s is polled for a device with no modules", decision.Object.Name)
		}
		if decision.Reason != ReasonModuleDisabled {
			t.Errorf("%s: Reason = %q, want %q",
				decision.Object.Name, decision.Reason, ReasonModuleDisabled)
		}
	}
}

// TestPlanFilterOnlyAppliesToItsModule guards against a filter leaking across
// modules.
func TestPlanFilterOnlyAppliesToItsModule(t *testing.T) {
	filters := map[string]config.ModuleFilter{
		"IF-MIB": {ExcludeNames: []string{"."}},
	}

	byName := reasons(Plan(planFixture(), []string{"IF-MIB", "SNMPv2-MIB"}, filters))

	assertDecision(t, byName, "ifTable", false, ReasonExcluded)
	assertDecision(t, byName, "sysUpTime", true, ReasonPolled)
}

// TestPlanMalformedPatternIsIgnored verifies a bad pattern is dropped rather
// than taking the device's whole polling configuration down with it.
func TestPlanMalformedPatternIsIgnored(t *testing.T) {
	filters := map[string]config.ModuleFilter{
		"IF-MIB": {ExcludeNames: []string{"([unclosed", "^ifTable$"}},
	}

	byName := reasons(Plan(planFixture(), []string{"IF-MIB"}, filters))

	assertDecision(t, byName, "ifTable", false, ReasonExcluded)
	assertDecision(t, byName, "ifInOctets", true, ReasonPolled)
}

// TestPlanEmptyInput covers a module with no objects at all, which is what
// SNMPv2-TC and SNMPv2-CONF are: textual conventions and conformance
// statements define nothing to poll.
func TestPlanEmptyInput(t *testing.T) {
	if got := Plan(nil, []string{"SNMPv2-TC"}, nil); len(got) != 0 {
		t.Errorf("got %d decisions for a module with no objects, want 0", len(got))
	}
	if got := Polled(nil, []string{"SNMPv2-TC"}, nil); len(got) != 0 {
		t.Errorf("got %d polled objects for a module with no objects, want 0", len(got))
	}
}

// TestPolledIsThePlansPolledObjects is the anti-divergence guarantee inside the
// package: whatever Plan explains, Polled returns, in the same order.
func TestPolledIsThePlansPolledObjects(t *testing.T) {
	cases := []struct {
		name    string
		objects []*mib.OIDMetadata
		modules []string
		filters map[string]config.ModuleFilter
	}{
		{"unfiltered", planFixture(), []string{"IF-MIB", "SNMPv2-MIB"}, nil},
		{"no table node", planFixture()[1:], []string{"IF-MIB"}, nil},
		{"include names", planFixture(), []string{"IF-MIB"},
			map[string]config.ModuleFilter{"IF-MIB": {IncludeNames: []string{"Octets$"}}}},
		{"exclude table", planFixture(), []string{"IF-MIB"},
			map[string]config.ModuleFilter{"IF-MIB": {ExcludeNames: []string{"^ifTable$"}}}},
		{"capped", planFixture()[1:], []string{"IF-MIB"},
			map[string]config.ModuleFilter{"IF-MIB": {MaxOIDs: 1}}},
		{"disabled", planFixture(), nil, nil},
		{"empty", nil, []string{"IF-MIB"}, nil},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var want []*mib.OIDMetadata
			for _, decision := range Plan(tt.objects, tt.modules, tt.filters) {
				if decision.Polled {
					want = append(want, decision.Object)
				}
			}

			got := Polled(tt.objects, tt.modules, tt.filters)

			if len(got) != len(want) {
				t.Fatalf("Polled returned %d objects, the plan says %d", len(got), len(want))
			}
			for i := range got {
				if got[i] != want[i] {
					t.Errorf("object %d: Polled has %s, the plan has %s",
						i, got[i].Name, want[i].Name)
				}
			}
		})
	}
}

// TestClassifyMatchesTheRules pins the classification a client displays to the
// one the rules branch on, so "this is a column" and "this was skipped because
// its table is walked" can never describe different objects.
func TestClassifyMatchesTheRules(t *testing.T) {
	tests := []struct {
		name   string
		object *mib.OIDMetadata
		want   Kind
	}{
		{"table", &mib.OIDMetadata{IsTable: true}, KindTable},
		{"entry", &mib.OIDMetadata{IsTableEntry: true}, KindEntry},
		{"column", &mib.OIDMetadata{IsColumn: true}, KindColumn},
		{"scalar", &mib.OIDMetadata{IsScalar: true}, KindScalar},
		{"unflagged object counts as a scalar", &mib.OIDMetadata{}, KindScalar},
		// A table node that also carries the column flag is a table: the walk
		// is what the poller would issue, and the walk decides.
		{"table wins over column", &mib.OIDMetadata{IsTable: true, IsColumn: true}, KindTable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.object); got != tt.want {
				t.Errorf("Classify = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestFetchableRules covers the per-object rule directly, including the reason
// it gives, which the classification test cannot reach.
func TestFetchableRules(t *testing.T) {
	walked := map[string]bool{ifTableOID: true}

	tests := []struct {
		name       string
		object     *mib.OIDMetadata
		wantPolled bool
		wantReason Reason
	}{
		{
			"table is a walk target regardless of access",
			&mib.OIDMetadata{OID: "1.3.6.1.4.1.9.9.109.1.1.1", IsTable: true, Access: "not-accessible", Status: "current"},
			true, ReasonPolled,
		},
		{
			"entry adds nothing over the table",
			&mib.OIDMetadata{OID: "1.3.6.1.2.1.2.2.1", IsTableEntry: true, Access: "not-accessible", Status: "current"},
			false, ReasonCoveredByTable,
		},
		{
			"column of a walked table is redundant",
			&mib.OIDMetadata{OID: "1.3.6.1.2.1.2.2.1.10", IsColumn: true, TableOID: ifTableOID,
				Access: "read-only", Status: "current"},
			false, ReasonCoveredByTable,
		},
		{
			"column of an unwalked table is needed",
			&mib.OIDMetadata{OID: "1.3.6.1.4.1.9.9.109.1.1.1.3", IsColumn: true,
				TableOID: "1.3.6.1.4.1.9.9.109.1.1.1", Access: "read-only", Status: "current"},
			true, ReasonPolled,
		},
		{
			"not-accessible column of an unwalked table can never be fetched",
			&mib.OIDMetadata{OID: "1.3.6.1.4.1.9.9.109.1.1.1.1", IsColumn: true,
				TableOID: "1.3.6.1.4.1.9.9.109.1.1.1", Access: "not-accessible", Status: "current"},
			false, ReasonNotAccessible,
		},
		{
			"readable scalar",
			&mib.OIDMetadata{OID: "1.3.6.1.2.1.1.1", IsScalar: true, Access: "read-only", Status: "current"},
			true, ReasonPolled,
		},
		{
			"writable scalar is readable too",
			&mib.OIDMetadata{OID: "1.3.6.1.2.1.1.4", IsScalar: true, Access: "read-write", Status: "current"},
			true, ReasonPolled,
		},
		{
			"not-accessible scalar",
			&mib.OIDMetadata{OID: "1.3.6.1.2.1.1.9", IsScalar: true, Access: "not-accessible", Status: "current"},
			false, ReasonNotAccessible,
		},
		{
			"notify-only scalar",
			&mib.OIDMetadata{OID: "1.3.6.1.6.3.1.1.4.1", IsScalar: true, Access: "accessible-for-notify", Status: "current"},
			false, ReasonNotAccessible,
		},
		{
			"deprecated scalar is readable but stale",
			&mib.OIDMetadata{OID: "1.3.6.1.2.1.2.2.1.12", IsScalar: true, Access: "read-only", Status: "deprecated"},
			false, ReasonNotCurrent,
		},
		{
			// An object whose module was parsed without one of the modules its
			// IMPORTS names. It looks readable and current and has no OID, so
			// there is nothing to ask a device for; calling it deprecated would
			// send a reader after the wrong problem entirely.
			"readable and current but unresolved to any OID",
			&mib.OIDMetadata{IsScalar: true, Access: "read-only", Status: "current"},
			false, ReasonUnresolved,
		},
		{
			"obsolete scalar",
			&mib.OIDMetadata{OID: "1.3.6.1.2.1.4.21.1.10", IsScalar: true, Access: "read-only", Status: "obsolete"},
			false, ReasonNotCurrent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason, ok := fetchable(tt.object, Classify(tt.object), walked)
			if ok != tt.wantPolled {
				t.Errorf("fetchable = %v, want %v", ok, tt.wantPolled)
			}
			if reason != tt.wantReason {
				t.Errorf("reason = %q, want %q", reason, tt.wantReason)
			}
		})
	}
}

// TestModuleFilterAllows covers the filter clauses and the reason each gives.
func TestModuleFilterAllows(t *testing.T) {
	object := &mib.OIDMetadata{OID: "1.3.6.1.2.1.2.2.1.10", Name: "ifInOctets"}

	tests := []struct {
		name       string
		filter     config.ModuleFilter
		wantAllow  bool
		wantReason Reason
	}{
		{"no patterns allows everything", config.ModuleFilter{}, true, ReasonPolled},
		{"matching include", config.ModuleFilter{IncludeNames: []string{"^ifIn"}}, true, ReasonPolled},
		{"non-matching include", config.ModuleFilter{IncludeNames: []string{"^ifOut"}}, false, ReasonNotIncluded},
		{"matching exclude", config.ModuleFilter{ExcludeNames: []string{"^ifIn"}}, false, ReasonExcluded},
		{"non-matching exclude", config.ModuleFilter{ExcludeNames: []string{"^ifOut"}}, true, ReasonPolled},
		{"matching OID include", config.ModuleFilter{IncludeOIDs: []string{`\.10$`}}, true, ReasonPolled},
		{"non-matching OID include", config.ModuleFilter{IncludeOIDs: []string{`\.99$`}}, false, ReasonNotIncluded},
		{"matching OID exclude", config.ModuleFilter{ExcludeOIDs: []string{`\.10$`}}, false, ReasonExcluded},
		{
			"exclude wins over include",
			config.ModuleFilter{IncludeNames: []string{"^ifIn"}, ExcludeNames: []string{"Octets$"}},
			false, ReasonExcluded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason, ok := compileModuleFilter(tt.filter).allows(object)
			if ok != tt.wantAllow {
				t.Errorf("allows = %v, want %v", ok, tt.wantAllow)
			}
			if reason != tt.wantReason {
				t.Errorf("reason = %q, want %q", reason, tt.wantReason)
			}
		})
	}
}

// BenchmarkPlan measures the rule over a catalog the size a real device
// carries. The poller runs this every time a device's configuration changes, so
// it sits on the path that decides how quickly a MIB assignment takes effect.
func BenchmarkPlan(b *testing.B) {
	objects := make([]*mib.OIDMetadata, 0, 4096)
	for module := 0; module < 17; module++ {
		name := fmt.Sprintf("MODULE-%d-MIB", module)
		base := fmt.Sprintf("1.3.6.1.4.1.%d", module)

		for table := 0; table < 12; table++ {
			tableOID := fmt.Sprintf("%s.%d", base, table)
			objects = append(objects, &mib.OIDMetadata{
				OID: tableOID, Name: fmt.Sprintf("table%d", table), Module: name,
				Access: "not-accessible", Status: "current", IsTable: true,
			})
			for column := 0; column < 16; column++ {
				objects = append(objects, &mib.OIDMetadata{
					OID:    fmt.Sprintf("%s.1.%d", tableOID, column),
					Name:   fmt.Sprintf("column%d_%d", table, column),
					Module: name, Access: "read-only", Status: "current",
					IsColumn: true, TableOID: tableOID,
				})
			}
		}
	}

	modules := make([]string, 0, 17)
	for module := 0; module < 17; module++ {
		modules = append(modules, fmt.Sprintf("MODULE-%d-MIB", module))
	}
	filters := map[string]config.ModuleFilter{
		"MODULE-0-MIB": {ExcludeNames: []string{"^column1_"}, MaxOIDs: 100},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Plan(objects, modules, filters)
	}
}
