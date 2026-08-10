//go:build integration

package parser

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/testsupport"
)

// asr1001Dir is the directory holding the MIB set shipped with the ASR1001.
// It is resolved at run time, so it is a var: a function call cannot appear in
// a const block, which is why this file did not build under the integration
// tag.
//
// It is a *fixture* directory, not a search path. The modules in it import
// SNMPv2-SMI, SNMPv2-TC and CISCO-SMI, none of which live here, so a parser
// configured with this directory alone resolves nothing: IF-MIB comes back as
// 62 objects with an empty numeric OID rather than 91 correctly typed ones, and
// no error is reported. Every test below therefore parses with allMIBDirs(),
// and uses this directory only to enumerate the module set under test.
var asr1001Dir = testsupport.MIBDir("asr1001")

const (
	// Object counts the complete search path yields today. They are lower
	// bounds so that adding objects to a MIB does not fail the suite, but
	// they are pinned to the current counts so that losing any does.
	minOIDsForCiscoBGP4 = 169
	minOIDsForIFMIB     = 91
	minOIDsForBGP4      = 57
	minOIDsForSNMPv2    = 70

	// minPollablePercentage is a sanity floor. A module of monitoring
	// objects whose objects are almost all unpollable indicates the parse
	// degraded rather than that the MIB is unusual.
	minPollablePercentage = 10.0

	// maxReportedOIDs caps the per-object failure messages emitted for a
	// module, so one broken parse does not bury the rest of the run.
	maxReportedOIDs = 10
)

// moduleHeaderPattern matches the "<name> DEFINITIONS ::= BEGIN" header that
// opens an SMI module.
var moduleHeaderPattern = regexp.MustCompile(`(?m)^[ \t]*([A-Za-z][A-Za-z0-9-]*)[ \t]+DEFINITIONS[ \t]+::=[ \t]*BEGIN`)

// integrationParser returns a parser initialized with every MIB directory in
// the repository, which is the only configuration in which the modules under
// test resolve their imports. The parser is closed when the test ends: gosmi
// keeps its loaded modules in package state, so leaving one open would let a
// later parse with a different search path silently answer from this one's
// cache.
func integrationParser(t *testing.T) *Parser {
	t.Helper()

	p := New(allMIBDirs()...)
	if err := p.Init(); err != nil {
		t.Fatalf("init parser: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

// declaredModuleName returns the module name the MIB file at path declares in
// its header. SMI makes that name independent of the file name — for example
// mibs/asr1001/EVENT-MIB.my declares DISMAN-EVENT-MIB — so the file is the only
// authority on what the module is called.
func declaredModuleName(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	match := moduleHeaderPattern.FindSubmatch(data)
	if match == nil {
		t.Fatalf("%s has no \"<name> DEFINITIONS ::= BEGIN\" header", path)
	}
	return string(match[1])
}

// assertOIDsResolved fails for every object whose numeric OID came back empty.
//
// An empty OID is not a cosmetic defect: an object with no OID cannot be
// polled, and an object that is *also* marked pollable is worse than useless,
// because the poller will issue a request for an empty OID and the failure
// surfaces at run time rather than at import time.
func assertOIDsResolved(t *testing.T, module string, oids []*mib.OIDMetadata) {
	t.Helper()

	if len(oids) == 0 {
		t.Fatalf("%s parsed to no objects at all", module)
	}

	empty, pollable := 0, 0
	for _, oid := range oids {
		if oid.OID != "" {
			continue
		}
		empty++
		if oid.Pollable {
			pollable++
		}
		if empty <= maxReportedOIDs {
			t.Errorf("%s::%s has an empty numeric OID (type=%q class=%s column=%t scalar=%t pollable=%t)",
				module, oid.Name, oid.Type, oid.ObjectClass, oid.IsColumn, oid.IsScalar, oid.Pollable)
		}
	}

	if empty > maxReportedOIDs {
		t.Errorf("%s: and %d further objects with an empty numeric OID", module, empty-maxReportedOIDs)
	}
	if pollable > 0 {
		t.Errorf("%s: %d of the %d objects with an empty numeric OID are marked pollable, so they would be polled as an empty OID",
			module, pollable, empty)
	}
}

// assertModuleResolves parses module with the complete search path and asserts
// that it yields at least minOIDs objects, every one of them carrying a numeric
// OID.
func assertModuleResolves(t *testing.T, module string, minOIDs int) []*mib.OIDMetadata {
	t.Helper()

	oids, err := integrationParser(t).ParseModule(module)
	if err != nil {
		t.Fatalf("parse %s: %v", module, err)
	}

	assertOIDsResolved(t, module, oids)

	if len(oids) < minOIDs {
		t.Errorf("%s yielded %d objects, want at least %d", module, len(oids), minOIDs)
	}

	logClassificationBreakdown(t, module, oids)
	return oids
}

// TestIntegration_LoadAllASR1001MIBs loads every MIB shipped for the ASR1001.
//
// All of them must load and all of them must contribute at least one node: a
// module that loads but yields nothing is the signature of an unresolved
// import, which gosmi does not report as an error. CISCO-PRODUCTS-MIB is the
// clearest case — it is 2888 OBJECT IDENTIFIER assignments hanging off
// ciscoProducts, and without CISCO-SMI on the search path it loads "successfully"
// with zero nodes.
func TestIntegration_LoadAllASR1001MIBs(t *testing.T) {
	parser := integrationParser(t)

	files, err := filepath.Glob(filepath.Join(asr1001Dir, "*.my"))
	if err != nil {
		t.Fatalf("list MIB files in %s: %v", asr1001Dir, err)
	}
	if len(files) == 0 {
		t.Fatalf("no MIB files found in %s", asr1001Dir)
	}

	t.Logf("found %d MIB files in %s", len(files), asr1001Dir)

	for _, file := range files {
		fileName := filepath.Base(file)

		t.Run(strings.TrimSuffix(fileName, ".my"), func(t *testing.T) {
			// gosmi resolves a module by the name it is asked for, which
			// for these files is the file's base name.
			requested := strings.TrimSuffix(fileName, ".my")

			info, err := parser.LoadModule(requested)
			if err != nil {
				t.Fatalf("load %s: %v", requested, err)
			}
			if info == nil {
				t.Fatalf("load %s returned no module info", requested)
			}

			// The module's name comes from its header, not its file name.
			declared := declaredModuleName(t, file)
			if info.Name != declared {
				t.Errorf("module name = %q, want %q as declared by %s", info.Name, declared, fileName)
			}

			// A module name is resolved against the whole search path, so
			// the file that answered is not necessarily the one under
			// mibs/asr1001 — IF-MIB, for instance, is answered by
			// mibs/standard/IF-MIB.txt. That is only benign while the two
			// files declare the same module.
			if info.Path != "" && info.Path != file {
				if shadow := declaredModuleName(t, info.Path); shadow != declared {
					t.Errorf("%s declares %q but was answered by %s, which declares %q",
						fileName, declared, info.Path, shadow)
				}
			}

			if info.OIDCount == 0 {
				t.Errorf("module %s contributed no nodes, which means its imports did not resolve", info.Name)
			}

			t.Logf("%s: module %s, %d nodes, from %s", fileName, info.Name, info.OIDCount, info.Path)
		})
	}
}

// TestIntegration_CISCO_BGP4_MIB_NoEmptyOIDs guards the empty-OID regression on
// the Cisco BGP module, whose deep table nesting is where it first appeared.
func TestIntegration_CISCO_BGP4_MIB_NoEmptyOIDs(t *testing.T) {
	assertModuleResolves(t, "CISCO-BGP4-MIB", minOIDsForCiscoBGP4)
}

// TestIntegration_IF_MIB_NoEmptyOIDs guards the same regression on IF-MIB,
// which every device is polled for.
func TestIntegration_IF_MIB_NoEmptyOIDs(t *testing.T) {
	assertModuleResolves(t, "IF-MIB", minOIDsForIFMIB)
}

// TestIntegration_BGP4_MIB_NoEmptyOIDs guards it on the standard BGP module, so
// a regression cannot be mistaken for something specific to Cisco's MIBs.
func TestIntegration_BGP4_MIB_NoEmptyOIDs(t *testing.T) {
	assertModuleResolves(t, "BGP4-MIB", minOIDsForBGP4)
}

// TestIntegration_TableStructureParsing checks that every table in IF-MIB comes
// back fully described. A table missing its entry OID, its indexes or any of its
// column OIDs cannot be walked, so each of these is a hard failure rather than a
// count.
func TestIntegration_TableStructureParsing(t *testing.T) {
	parser := integrationParser(t)

	tables, err := parser.GetTableInfo("IF-MIB")
	if err != nil {
		t.Fatalf("table info for IF-MIB: %v", err)
	}

	// IF-MIB defines ifTable, ifXTable, ifStackTable, ifTestTable and
	// ifRcvAddressTable.
	if len(tables) != 5 {
		t.Errorf("IF-MIB yielded %d tables, want 5", len(tables))
	}

	for _, table := range tables {
		t.Run(table.Name, func(t *testing.T) {
			if table.OID == "" {
				t.Errorf("table %s has no OID", table.Name)
			}
			if table.EntryOID == "" {
				t.Errorf("table %s has no entry OID", table.Name)
			}
			if len(table.Indexes) == 0 {
				t.Errorf("table %s has no indexes, so its rows cannot be identified", table.Name)
			}
			if len(table.Columns) == 0 {
				t.Errorf("table %s has no columns", table.Name)
			}
			for i, column := range table.Columns {
				if column == "" {
					t.Errorf("table %s column %d has an empty OID", table.Name, i)
				}
			}

			t.Logf("table %s: OID=%s entry=%s indexes=%v columns=%d",
				table.Name, table.OID, table.EntryOID, table.Indexes, len(table.Columns))
		})
	}
}

// TestIntegration_OIDHierarchy checks that every column in IF-MIB is linked to
// its row and its table. Those links are what let a caller turn a column OID
// into a walkable table, so a column missing any of them is a defect, not a
// statistic.
func TestIntegration_OIDHierarchy(t *testing.T) {
	oids, err := integrationParser(t).ParseModule("IF-MIB")
	if err != nil {
		t.Fatalf("parse IF-MIB: %v", err)
	}

	isEntry := make(map[string]bool, len(oids))
	for _, oid := range oids {
		if oid.OID != "" {
			isEntry[oid.OID] = oid.IsTableEntry
		}
	}

	columns := 0
	for _, oid := range oids {
		if !oid.IsColumn {
			continue
		}
		columns++

		if oid.Parent == "" {
			t.Errorf("column %s has no parent OID", oid.Name)
			continue
		}
		if oid.EntryOID == "" {
			t.Errorf("column %s has no entry OID", oid.Name)
		}
		if oid.TableOID == "" {
			t.Errorf("column %s has no table OID", oid.Name)
		}
		if entry, ok := isEntry[oid.Parent]; ok && !entry {
			t.Errorf("column %s has parent %s, which is not a table entry", oid.Name, oid.Parent)
		}
	}

	if columns == 0 {
		t.Fatal("IF-MIB yielded no columns")
	}
	t.Logf("IF-MIB: %d columns, all linked to a row and a table", columns)
}

// TestIntegration_ObjectClassification checks that IF-MIB exercises every
// object class the rest of the project switches on, and that an index column is
// self-consistent: classified as an index, flagged as one, and pointing at the
// row it indexes.
func TestIntegration_ObjectClassification(t *testing.T) {
	oids, err := integrationParser(t).ParseModule("IF-MIB")
	if err != nil {
		t.Fatalf("parse IF-MIB: %v", err)
	}

	byClass := make(map[mib.ObjectClass]int)
	for _, oid := range oids {
		byClass[oid.ObjectClass]++
	}
	for class, count := range byClass {
		t.Logf("%s: %d", class, count)
	}

	for _, class := range []mib.ObjectClass{
		mib.ObjectClassScalar,
		mib.ObjectClassTableNode,
		mib.ObjectClassEntryNode,
		mib.ObjectClassIndexColumn,
		mib.ObjectClassMetricColumn,
	} {
		if byClass[class] == 0 {
			t.Errorf("IF-MIB yielded no objects of class %s", class)
		}
	}

	indexColumns := 0
	for _, oid := range oids {
		if oid.ObjectClass != mib.ObjectClassIndexColumn {
			continue
		}
		indexColumns++

		if !oid.IsIndex {
			t.Errorf("%s is classified as an index column but IsIndex is false", oid.Name)
		}
		if oid.IndexFor == "" {
			t.Errorf("index column %s does not say which row it indexes", oid.Name)
		}
	}

	if indexColumns == 0 {
		t.Error("IF-MIB yielded no index columns")
	}
	t.Logf("IF-MIB: %d index columns", indexColumns)
}

// TestIntegration_PollableOIDValidation checks the invariant the poller depends
// on across several modules: an object marked pollable carries a numeric OID,
// and that OID is rooted at 1 rather than being a name or a fragment.
func TestIntegration_PollableOIDValidation(t *testing.T) {
	parser := integrationParser(t)

	for _, module := range []string{"IF-MIB", "CISCO-BGP4-MIB", "BGP4-MIB"} {
		t.Run(module, func(t *testing.T) {
			oids, err := parser.ParseModule(module)
			if err != nil {
				t.Fatalf("parse %s: %v", module, err)
			}
			if len(oids) == 0 {
				t.Fatalf("%s parsed to no objects at all", module)
			}

			pollable := 0
			for _, oid := range oids {
				if !oid.Pollable {
					continue
				}
				pollable++

				if oid.OID == "" {
					t.Errorf("%s::%s is pollable but has an empty numeric OID (type=%q class=%s)",
						module, oid.Name, oid.Type, oid.ObjectClass)
					continue
				}
				if !strings.HasPrefix(oid.OID, "1.") && !strings.HasPrefix(oid.OID, ".1.") {
					t.Errorf("%s::%s has OID %q, which is not rooted at 1", module, oid.Name, oid.OID)
				}
			}

			percentage := float64(pollable) / float64(len(oids)) * 100
			t.Logf("%s: %d objects, %d pollable (%.1f%%)", module, len(oids), pollable, percentage)

			if percentage < minPollablePercentage {
				t.Errorf("%s: %.1f%% of objects are pollable, want at least %.1f%%; a monitoring MIB this far below the floor indicates a degraded parse",
					module, percentage, minPollablePercentage)
			}
		})
	}
}

// TestIntegration_ChartabilityClassification checks the rules the UI relies on:
// a chartable object carries a chart type, and an index column is never
// chartable, because charting a row identifier plots the key rather than a
// measurement.
func TestIntegration_ChartabilityClassification(t *testing.T) {
	oids, err := integrationParser(t).ParseModule("IF-MIB")
	if err != nil {
		t.Fatalf("parse IF-MIB: %v", err)
	}

	chartable, displayable, indexColumns := 0, 0, 0
	for _, oid := range oids {
		if oid.IsChartable {
			chartable++
			if oid.ChartType == "" {
				t.Errorf("%s is chartable but carries no chart type", oid.Name)
			}
		}
		if oid.IsDisplayable {
			displayable++
		}
		if oid.IsIndex {
			indexColumns++
			if oid.IsChartable {
				t.Errorf("index column %s is marked chartable", oid.Name)
			}
		}
	}

	t.Logf("IF-MIB: %d chartable, %d displayable, %d index columns", chartable, displayable, indexColumns)

	if chartable == 0 {
		t.Error("IF-MIB yielded no chartable objects, though it is almost entirely counters and gauges")
	}
	if indexColumns == 0 {
		t.Error("IF-MIB yielded no index columns")
	}
}

// TestIntegration_EnumValues pins the enumerations of the two IF-MIB objects
// whose labels the UI renders directly. Counting objects that carry an enum is
// not enough: the labels have to be the right ones and keyed by the right
// values, or a down interface is displayed as up.
func TestIntegration_EnumValues(t *testing.T) {
	oids, err := integrationParser(t).ParseModule("IF-MIB")
	if err != nil {
		t.Fatalf("parse IF-MIB: %v", err)
	}

	byName := make(map[string]*mib.OIDMetadata, len(oids))
	for _, oid := range oids {
		byName[oid.Name] = oid
	}

	tests := []struct {
		object string
		want   map[int64]string
	}{
		{
			object: "ifAdminStatus",
			want:   map[int64]string{1: "up", 2: "down", 3: "testing"},
		},
		{
			object: "ifOperStatus",
			want: map[int64]string{
				1: "up", 2: "down", 3: "testing", 4: "unknown",
				5: "dormant", 6: "notPresent", 7: "lowerLayerDown",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.object, func(t *testing.T) {
			oid, ok := byName[tt.object]
			if !ok {
				t.Fatalf("%s is missing from the parsed module", tt.object)
			}
			if len(oid.EnumValues) != len(tt.want) {
				t.Errorf("%s has %d enum values, want %d: %v", tt.object, len(oid.EnumValues), len(tt.want), oid.EnumValues)
			}
			for value, label := range tt.want {
				if got := oid.EnumValues[value]; got != label {
					t.Errorf("%s enum %d = %q, want %q", tt.object, value, got, label)
				}
			}
		})
	}

	withEnums := 0
	for _, oid := range oids {
		if len(oid.EnumValues) > 0 {
			withEnums++
		}
	}
	if withEnums == 0 {
		t.Error("IF-MIB yielded no objects with enum values")
	}
	t.Logf("IF-MIB: %d objects carry enum values", withEnums)
}

// TestIntegration_CriticalMIBs checks the module set the product cannot ship
// without. Every one of them is present in the repository, so a module that
// fails to parse, comes back short or comes back with an unusable object is a
// release blocker rather than an optional extra.
func TestIntegration_CriticalMIBs(t *testing.T) {
	parser := integrationParser(t)

	criticalMIBs := []struct {
		name    string
		minOIDs int
	}{
		{name: "IF-MIB", minOIDs: minOIDsForIFMIB},
		{name: "CISCO-BGP4-MIB", minOIDs: minOIDsForCiscoBGP4},
		{name: "BGP4-MIB", minOIDs: minOIDsForBGP4},
		{name: "CISCO-PROCESS-MIB", minOIDs: 190},
		{name: "CISCO-MEMORY-POOL-MIB", minOIDs: 33},
	}

	for _, critical := range criticalMIBs {
		t.Run(critical.name, func(t *testing.T) {
			oids, err := parser.ParseModule(critical.name)
			if err != nil {
				t.Fatalf("parse %s: %v", critical.name, err)
			}

			assertOIDsResolved(t, critical.name, oids)

			if len(oids) < critical.minOIDs {
				t.Errorf("%s yielded %d objects, want at least %d", critical.name, len(oids), critical.minOIDs)
			}

			pollable := 0
			for _, oid := range oids {
				if oid.Pollable {
					pollable++
				}
			}
			if pollable == 0 {
				t.Errorf("%s yielded no pollable objects", critical.name)
			}

			t.Logf("%s: %d objects, %d pollable", critical.name, len(oids), pollable)
		})
	}
}

// TestIntegration_DetailedOIDDump dumps the full metadata of the first objects
// of CISCO-BGP4-MIB. The dump is a diagnostic aid, but it asserts rather than
// annotates: an object with no numeric OID used to be flagged in the output and
// still let the test pass.
func TestIntegration_DetailedOIDDump(t *testing.T) {
	const dumpLimit = 20

	oids, err := integrationParser(t).ParseModule("CISCO-BGP4-MIB")
	if err != nil {
		t.Fatalf("parse CISCO-BGP4-MIB: %v", err)
	}
	if len(oids) == 0 {
		t.Fatal("CISCO-BGP4-MIB parsed to no objects at all")
	}

	for i, oid := range oids {
		if i >= dumpLimit {
			t.Logf("... and %d more objects", len(oids)-dumpLimit)
			break
		}

		if oid.OID == "" {
			t.Errorf("CISCO-BGP4-MIB::%s has an empty numeric OID", oid.Name)
		}

		t.Logf("[%d] %s OID=%s type=%s access=%s status=%s class=%s",
			i+1, oid.Name, oid.OID, oid.Type, oid.Access, oid.Status, oid.ObjectClass)
		t.Logf("    table=%t entry=%t column=%t scalar=%t index=%t pollable=%t chartable=%t(%s)",
			oid.IsTable, oid.IsTableEntry, oid.IsColumn, oid.IsScalar, oid.IsIndex,
			oid.Pollable, oid.IsChartable, oid.ChartType)
		if oid.Parent != "" {
			t.Logf("    parent=%s table=%s entry=%s", oid.Parent, oid.TableOID, oid.EntryOID)
		}
		if len(oid.EnumValues) > 0 {
			t.Logf("    %d enum values", len(oid.EnumValues))
		}
	}
}

// logClassificationBreakdown records how a module's objects were classified.
// It only describes; the assertions live in assertOIDsResolved and its callers,
// so that a problem cannot be reported as a note in passing output.
func logClassificationBreakdown(t *testing.T, moduleName string, oids []*mib.OIDMetadata) {
	t.Helper()

	if len(oids) == 0 {
		return
	}

	typeCount := make(map[string]int)
	accessCount := make(map[string]int)
	statusCount := make(map[string]int)
	classCount := make(map[mib.ObjectClass]int)

	pollable, chartable, displayable, withEnums := 0, 0, 0, 0

	for _, oid := range oids {
		typeCount[oid.Type]++
		accessCount[oid.Access]++
		statusCount[oid.Status]++
		classCount[oid.ObjectClass]++

		if oid.Pollable {
			pollable++
		}
		if oid.IsChartable {
			chartable++
		}
		if oid.IsDisplayable {
			displayable++
		}
		if len(oid.EnumValues) > 0 {
			withEnums++
		}
	}

	total := float64(len(oids))
	t.Logf("%s: %d objects, %d pollable (%.1f%%), %d chartable (%.1f%%), %d displayable (%.1f%%), %d with enums",
		moduleName, len(oids),
		pollable, float64(pollable)/total*100,
		chartable, float64(chartable)/total*100,
		displayable, float64(displayable)/total*100,
		withEnums)

	for typ, count := range typeCount {
		t.Logf("  type %s: %d", typ, count)
	}
	for access, count := range accessCount {
		t.Logf("  access %s: %d", access, count)
	}
	for status, count := range statusCount {
		t.Logf("  status %s: %d", status, count)
	}
	for class, count := range classCount {
		t.Logf("  class %s: %d", class, count)
	}
}

// BenchmarkIntegration_ParseCISCOBGP4MIB measures a repeat parse of the largest
// Cisco module in the critical set.
func BenchmarkIntegration_ParseCISCOBGP4MIB(b *testing.B) {
	benchmarkParseModule(b, "CISCO-BGP4-MIB")
}

// BenchmarkIntegration_ParseIFMIB measures a repeat parse of the module every
// device is polled for.
func BenchmarkIntegration_ParseIFMIB(b *testing.B) {
	benchmarkParseModule(b, "IF-MIB")
}

// benchmarkParseModule times ParseModule for one module. The first iteration
// loads the module and the rest read it back from gosmi's cache, which is what
// the import path does when it walks a directory of MIBs.
func benchmarkParseModule(b *testing.B, module string) {
	b.Helper()

	p := New(allMIBDirs()...)
	if err := p.Init(); err != nil {
		b.Fatalf("init parser: %v", err)
	}
	defer p.Close()

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if _, err := p.ParseModule(module); err != nil {
			b.Fatalf("parse %s: %v", module, err)
		}
	}
}
