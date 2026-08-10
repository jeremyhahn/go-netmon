package poller

import (
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/mib"
)

// ifTableOID is the table the IF-MIB columns below belong to.
const ifTableOID = "1.3.6.1.2.1.2.2"

// selectionFixture is a small IF-MIB/CISCO-PROCESS-MIB slice covering the
// shapes that matter: readable columns, an entry node, a readable and a
// not-accessible index column, and a deprecated object.
//
// It deliberately contains NO table node, so a test states explicitly whether
// the table is present — that choice decides whether the columns are walked
// individually or covered by one walk of the table.
func selectionFixture() []*mib.OIDMetadata {
	return []*mib.OIDMetadata{
		// Readable IF-MIB columns.
		{OID: "1.3.6.1.2.1.2.2.1.10", Name: "ifInOctets", Module: "IF-MIB",
			Type: "Counter32", Access: "read-only", Status: "current",
			IsColumn: true, TableOID: ifTableOID},
		{OID: "1.3.6.1.2.1.2.2.1.16", Name: "ifOutOctets", Module: "IF-MIB",
			Type: "Counter32", Access: "read-only", Status: "current",
			IsColumn: true, TableOID: ifTableOID},
		{OID: "1.3.6.1.2.1.2.2.1.8", Name: "ifOperStatus", Module: "IF-MIB",
			Type: "INTEGER", Access: "read-only", Status: "current",
			IsColumn: true, TableOID: ifTableOID},
		{OID: "1.3.6.1.2.1.2.2.1.2", Name: "ifDescr", Module: "IF-MIB",
			Type: "DisplayString", Access: "read-only", Status: "current",
			IsColumn: true, TableOID: ifTableOID},

		// The entry node describes structure and can never be fetched.
		{OID: "1.3.6.1.2.1.2.2.1", Name: "ifEntry", Module: "IF-MIB",
			Access: "not-accessible", Status: "current", IsTableEntry: true},

		// An index column that IS readable. IF-MIB declares ifIndex
		// MAX-ACCESS read-only, so it is fetched like any other column when
		// its table is not being walked — being an index does not make it free.
		{OID: "1.3.6.1.2.1.2.2.1.1", Name: "ifIndex", Module: "IF-MIB",
			Type: "InterfaceIndex", Access: "read-only", Status: "current",
			IsColumn: true, IsIndex: true, TableOID: ifTableOID},

		// An index column that is not readable, which many later MIBs use.
		// This one exists only to label rows and must never be fetched.
		{OID: "1.3.6.1.2.1.31.1.4.1.1", Name: "ifRcvAddressAddress", Module: "IF-MIB",
			Type: "PhysAddress", Access: "not-accessible", Status: "current",
			IsColumn: true, IsIndex: true},

		// A deprecated object the agent may still answer but we should not ask for.
		{OID: "1.3.6.1.2.1.2.2.1.21", Name: "ifSpecific", Module: "IF-MIB",
			Type: "OBJECT IDENTIFIER", Access: "read-only", Status: "deprecated",
			IsColumn: true, TableOID: ifTableOID},

		// A different module, to prove module scoping.
		{OID: "1.3.6.1.4.1.9.9.109.1.1.1.1.8", Name: "cpmCPUTotal5minRev",
			Module: "CISCO-PROCESS-MIB", Type: "Gauge32", Access: "read-only",
			Status: "current", IsColumn: true},
		{OID: "1.3.6.1.4.1.9.9.109.1.1.1.1.12", Name: "cpmCPUMemoryUsed",
			Module: "CISCO-PROCESS-MIB", Type: "Gauge32", Access: "read-only",
			Status: "current", IsColumn: true},

		// A module the device has not been assigned at all.
		{OID: "1.3.6.1.2.1.6.5", Name: "tcpActiveOpens", Module: "TCP-MIB",
			Type: "Counter32", Access: "read-only", Status: "current", IsScalar: true},
	}
}

// withIfTable returns the fixture plus the ifTable node, which is how real
// stored metadata looks and which lets one walk cover every column.
func withIfTable() []*mib.OIDMetadata {
	return append(selectionFixture(), &mib.OIDMetadata{
		OID: ifTableOID, Name: "ifTable", Module: "IF-MIB",
		Access: "not-accessible", Status: "current", IsTable: true,
	})
}

func selectedNames(oids []*mib.OIDMetadata) map[string]bool {
	names := make(map[string]bool, len(oids))
	for _, oid := range oids {
		names[oid.Name] = true
	}
	return names
}

// TestSelectModuleOIDsExcludesUnpollableObjects is the important one: the
// previous implementation took every object whose module matched, including
// table and entry nodes that are not-accessible and index columns that only
// exist to label rows. Each one cost a wasted request on every poll.
func TestSelectModuleOIDsExcludesUnpollableObjects(t *testing.T) {
	device := &config.Device{Name: "core-01", Modules: []string{"IF-MIB"}}

	got := selectModuleOIDs(withIfTable(), device)
	names := selectedNames(got)

	// The table is the walk target. Its own MAX-ACCESS is not-accessible in
	// essentially every MIB, because that describes the node rather than its
	// subtree — one walk of it returns every column and row.
	if !names["ifTable"] {
		t.Error("ifTable is the walk target and must be selected")
	}

	// Columns of a walked table need no request of their own; asking for each
	// separately turns one walk into dozens.
	for _, redundant := range []string{
		"ifInOctets", "ifOutOctets", "ifOperStatus", "ifDescr", "ifIndex",
	} {
		if names[redundant] {
			t.Errorf("%s belongs to a walked table and must not be polled separately", redundant)
		}
	}

	// Entry nodes describe structure and add nothing over the table.
	if names["ifEntry"] {
		t.Error("ifEntry adds nothing over walking ifTable")
	}
	if names["ifSpecific"] {
		t.Error("a deprecated object must not be polled")
	}
	if names["cpmCPUTotal5minRev"] || names["tcpActiveOpens"] {
		t.Error("objects outside the device's assigned modules must not be selected")
	}
}

func TestSelectModuleOIDsAcrossSeveralModules(t *testing.T) {
	device := &config.Device{
		Name:    "core-01",
		Modules: []string{"IF-MIB", "CISCO-PROCESS-MIB"},
	}

	names := selectedNames(selectModuleOIDs(selectionFixture(), device))

	if !names["ifInOctets"] || !names["cpmCPUTotal5minRev"] {
		t.Error("both assigned modules should contribute objects")
	}
	if names["tcpActiveOpens"] {
		t.Error("TCP-MIB is not assigned and must not contribute")
	}
}

func TestSelectModuleOIDsNoModulesAssigned(t *testing.T) {
	device := &config.Device{Name: "core-01"}

	if got := selectModuleOIDs(selectionFixture(), device); len(got) != 0 {
		t.Errorf("got %d OIDs for a device with no modules, want 0", len(got))
	}
}

func TestSelectModuleOIDsIncludeNames(t *testing.T) {
	device := &config.Device{
		Name:    "core-01",
		Modules: []string{"IF-MIB"},
		ModuleFilters: map[string]config.ModuleFilter{
			"IF-MIB": {IncludeNames: []string{"^ifInOctets$", "^ifOutOctets$"}},
		},
	}

	names := selectedNames(selectModuleOIDs(selectionFixture(), device))

	if !names["ifInOctets"] || !names["ifOutOctets"] {
		t.Error("included objects should be selected")
	}
	if names["ifOperStatus"] || names["ifDescr"] {
		t.Error("an include list must exclude everything it does not name")
	}
}

func TestSelectModuleOIDsExcludeNames(t *testing.T) {
	device := &config.Device{
		Name:    "core-01",
		Modules: []string{"IF-MIB"},
		ModuleFilters: map[string]config.ModuleFilter{
			"IF-MIB": {ExcludeNames: []string{"^ifDescr$"}},
		},
	}

	names := selectedNames(selectModuleOIDs(selectionFixture(), device))

	if names["ifDescr"] {
		t.Error("ifDescr was excluded and must not be selected")
	}
	if !names["ifInOctets"] || !names["ifOperStatus"] {
		t.Error("an exclude list must leave everything else alone")
	}
}

// TestSelectModuleOIDsExcludeBeatsInclude pins the precedence rule, which is
// the one a user is most likely to get wrong.
func TestSelectModuleOIDsExcludeBeatsInclude(t *testing.T) {
	device := &config.Device{
		Name:    "core-01",
		Modules: []string{"IF-MIB"},
		ModuleFilters: map[string]config.ModuleFilter{
			"IF-MIB": {
				IncludeNames: []string{"^if.*Octets$"},
				ExcludeNames: []string{"^ifOutOctets$"},
			},
		},
	}

	names := selectedNames(selectModuleOIDs(selectionFixture(), device))

	if !names["ifInOctets"] {
		t.Error("ifInOctets matches the include and no exclude")
	}
	if names["ifOutOctets"] {
		t.Error("ifOutOctets matches both, and the exclude must win")
	}
}

func TestSelectModuleOIDsIncludeOIDs(t *testing.T) {
	device := &config.Device{
		Name:    "core-01",
		Modules: []string{"IF-MIB"},
		ModuleFilters: map[string]config.ModuleFilter{
			"IF-MIB": {IncludeOIDs: []string{`^1\.3\.6\.1\.2\.1\.2\.2\.1\.10$`}},
		},
	}

	names := selectedNames(selectModuleOIDs(selectionFixture(), device))

	if !names["ifInOctets"] || len(names) != 1 {
		t.Errorf("expected only ifInOctets, got %v", names)
	}
}

func TestSelectModuleOIDsMaxOIDs(t *testing.T) {
	device := &config.Device{
		Name:    "core-01",
		Modules: []string{"IF-MIB", "CISCO-PROCESS-MIB"},
		ModuleFilters: map[string]config.ModuleFilter{
			"IF-MIB": {MaxOIDs: 2},
		},
	}

	got := selectModuleOIDs(selectionFixture(), device)

	ifCount, ciscoCount := 0, 0
	for _, oid := range got {
		switch oid.Module {
		case "IF-MIB":
			ifCount++
		case "CISCO-PROCESS-MIB":
			ciscoCount++
		}
	}

	if ifCount != 2 {
		t.Errorf("IF-MIB contributed %d OIDs, want 2", ifCount)
	}
	// The cap is per module, so an unfiltered module is unaffected.
	if ciscoCount != 2 {
		t.Errorf("CISCO-PROCESS-MIB contributed %d OIDs, want 2", ciscoCount)
	}
}

// TestSelectModuleOIDsFilterOnlyAppliesToItsModule guards against a filter
// leaking across modules.
func TestSelectModuleOIDsFilterOnlyAppliesToItsModule(t *testing.T) {
	device := &config.Device{
		Name:    "core-01",
		Modules: []string{"IF-MIB", "CISCO-PROCESS-MIB"},
		ModuleFilters: map[string]config.ModuleFilter{
			"IF-MIB": {IncludeNames: []string{"^ifInOctets$"}},
		},
	}

	names := selectedNames(selectModuleOIDs(selectionFixture(), device))

	if !names["ifInOctets"] || names["ifOutOctets"] {
		t.Error("the IF-MIB filter should apply to IF-MIB")
	}
	if !names["cpmCPUTotal5minRev"] || !names["cpmCPUMemoryUsed"] {
		t.Error("CISCO-PROCESS-MIB has no filter and must be selected in full")
	}
}

// TestSelectModuleOIDsMalformedPattern verifies a bad pattern is ignored rather
// than taking the device's whole polling configuration down with it.
func TestSelectModuleOIDsMalformedPattern(t *testing.T) {
	device := &config.Device{
		Name:    "core-01",
		Modules: []string{"IF-MIB"},
		ModuleFilters: map[string]config.ModuleFilter{
			"IF-MIB": {
				ExcludeNames: []string{"([unclosed", "^ifDescr$"},
			},
		},
	}

	names := selectedNames(selectModuleOIDs(selectionFixture(), device))

	if names["ifDescr"] {
		t.Error("the valid exclude pattern should still apply")
	}
	if !names["ifInOctets"] {
		t.Error("a malformed pattern must not drop unrelated objects")
	}
}

func TestSelectModuleOIDsEmptyInput(t *testing.T) {
	device := &config.Device{Name: "core-01", Modules: []string{"IF-MIB"}}

	if got := selectModuleOIDs(nil, device); len(got) != 0 {
		t.Errorf("got %d OIDs from an empty store, want 0", len(got))
	}
}

// TestSelectModuleOIDsPrefersOneWalkPerTable is the regression for a change
// that quietly multiplied SNMP traffic. Filtering table nodes out because they
// are not-accessible left only their columns, turning a single walk that
// returns the whole table into one walk per column — 546 requests per poll on
// a real router instead of a handful.
func TestSelectModuleOIDsPrefersOneWalkPerTable(t *testing.T) {
	device := &config.Device{Name: "core-01", Modules: []string{"IF-MIB"}}

	got := selectModuleOIDs(withIfTable(), device)

	tables, columns := 0, 0
	for _, oid := range got {
		switch {
		case oid.IsTable:
			tables++
		case oid.IsColumn:
			columns++
		}
	}

	if tables != 1 {
		t.Errorf("selected %d tables, want the one table to be walked", tables)
	}
	if columns != 0 {
		t.Errorf("selected %d columns of a walked table, want 0", columns)
	}
}

// TestSelectModuleOIDsWalksColumnsWhenTheirTableIsAbsent covers metadata where
// the table node was never stored: the columns then have to be walked
// individually, because nothing else will return them.
func TestSelectModuleOIDsWalksColumnsWhenTheirTableIsAbsent(t *testing.T) {
	// The base fixture has no table node at all.
	device := &config.Device{Name: "core-01", Modules: []string{"IF-MIB"}}
	names := selectedNames(selectModuleOIDs(selectionFixture(), device))

	if !names["ifInOctets"] {
		t.Error("with no table to walk, readable columns must be polled individually")
	}
	if names["ifRcvAddressAddress"] {
		t.Error("a not-accessible column can never be fetched and must stay excluded")
	}
}

// TestSelectModuleOIDsExcludingATableRestoresItsColumns verifies the two
// mechanisms compose: excluding a table means its columns are no longer
// covered, so the readable ones are selected on their own.
func TestSelectModuleOIDsExcludingATableRestoresItsColumns(t *testing.T) {
	device := &config.Device{
		Name:    "core-01",
		Modules: []string{"IF-MIB"},
		ModuleFilters: map[string]config.ModuleFilter{
			"IF-MIB": {ExcludeNames: []string{"^ifTable$"}},
		},
	}

	names := selectedNames(selectModuleOIDs(withIfTable(), device))

	if names["ifTable"] {
		t.Error("ifTable was excluded and must not be walked")
	}
	if !names["ifInOctets"] {
		t.Error("with its table excluded, a readable column must be polled directly")
	}
}
