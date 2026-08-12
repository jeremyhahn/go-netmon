package parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An object with no OID is not an object.
//
// gosmi produces them in two situations and both used to be stored, which is
// what made an unusable module report a healthy object count:
//
//   - A module whose IMPORTS did not resolve parses into objects hanging off a
//     parent identifier that was never found. ATEN-PRODUCTS-MIB imported this
//     way reported "6 objects" and produced no metrics, for weeks, because
//     ATEN-SMI was not in the store when it was first imported.
//   - A module with a duplicate IMPORTS symbol can yield each object twice, once
//     resolved and once not. KNGeneric-MIB stored 159 records for the 80 objects
//     it declares.
//
// The first case is also cached, which is the harder half: re-importing the same
// file reads the same cached failure and fails identically, so the module stays
// broken until the process restarts.

// writeModule puts a MIB source file in dir, named for the module it declares.
func writeModule(t *testing.T, dir, module, body string) string {
	t.Helper()

	path := filepath.Join(dir, module+".mib")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", module, err)
	}
	return path
}

// The two halves of a dependency: a root that registers an enterprise subtree,
// and a module hanging objects off it.
const (
	rootModule = `TEST-ROOT-SMI DEFINITIONS ::= BEGIN
IMPORTS enterprises FROM SNMPv2-SMI;
testRoot OBJECT IDENTIFIER ::= { enterprises 65535 }
testProducts OBJECT IDENTIFIER ::= { testRoot 1 }
END
`

	leafModule = `TEST-LEAF-MIB DEFINITIONS ::= BEGIN
IMPORTS
    OBJECT-TYPE FROM SNMPv2-SMI
    testProducts FROM TEST-ROOT-SMI;

testGroup OBJECT IDENTIFIER ::= { testProducts 7 }

testReading OBJECT-TYPE
    SYNTAX INTEGER
    MAX-ACCESS read-only
    STATUS current
    DESCRIPTION "A reading."
    ::= { testGroup 1 }

testLabel OBJECT-TYPE
    SYNTAX OCTET STRING
    MAX-ACCESS read-only
    STATUS current
    DESCRIPTION "A label."
    ::= { testGroup 2 }

END
`
)

// standardMIBs is the repository's own MIB tree, which holds SNMPv2-SMI. Both
// fixtures import it, as every real module does.
func standardMIBs(t *testing.T) string {
	t.Helper()

	path, err := filepath.Abs(filepath.Join("..", "..", "..", "mibs", "standard"))
	if err != nil {
		t.Fatalf("resolve the standard MIB path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(path, "SNMPv2-SMI.txt")); err != nil {
		t.Skipf("the standard MIB tree is not present: %v", err)
	}
	return path
}

// TestAModuleParsedBeforeItsDependencyRecoversOnRetry is the cached-failure
// case, and the reason a MIB could be re-imported forever without improving.
//
// The leaf is parsed while its dependency is missing, which fails and is cached.
// The dependency then appears -- exactly what happens when both are uploaded in
// one run, the root a moment after the leaf -- and the leaf is parsed again.
func TestAModuleParsedBeforeItsDependencyRecoversOnRetry(t *testing.T) {
	dir := t.TempDir()
	p := New(standardMIBs(t), dir)
	if err := p.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(p.Close)

	// The leaf alone: its IMPORTS cannot resolve, and gosmi remembers that.
	writeModule(t, dir, "TEST-LEAF-MIB", leafModule)
	if objects, _, err := p.ParseModuleWithReport("TEST-LEAF-MIB"); err == nil && len(objects) > 0 {
		t.Fatalf("the leaf resolved %d objects without its dependency present", len(objects))
	}

	// The dependency arrives.
	writeModule(t, dir, "TEST-ROOT-SMI", rootModule)

	objects, report, err := p.ParseModuleWithReport("TEST-LEAF-MIB")
	if err != nil {
		t.Fatalf("parsing the leaf after its dependency arrived: %v", err)
	}
	if len(objects) == 0 {
		t.Fatalf("the leaf still resolved nothing; the cached failure was not cleared "+
			"(%d objects discarded)", report.UnresolvedCount())
	}

	byName := make(map[string]string, len(objects))
	for _, object := range objects {
		byName[object.Name] = object.OID
	}
	for _, want := range []string{"testReading", "testLabel"} {
		oid, found := byName[want]
		if !found {
			t.Errorf("%s is missing from the parsed module", want)
			continue
		}
		if !strings.HasPrefix(oid, "1.3.6.1.4.1.65535.1.7") {
			t.Errorf("%s resolved to %q, want it under the imported root", want, oid)
		}
	}
}

// TestAnObjectWithNoOIDIsNotStored is the other half: whatever the cause, a
// record the poller can never ask for must not be returned as an object.
func TestAnObjectWithNoOIDIsNotStored(t *testing.T) {
	dir := t.TempDir()
	p := New(standardMIBs(t), dir)
	if err := p.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(p.Close)

	// The leaf with no dependency anywhere: nothing it defines can resolve.
	writeModule(t, dir, "TEST-LEAF-MIB", leafModule)

	objects, report, err := p.ParseModuleWithReport("TEST-LEAF-MIB")
	if err != nil {
		// gosmi refusing the module outright is also an honest answer; what
		// must not happen is objects coming back with no OIDs.
		return
	}

	for _, object := range objects {
		if object.OID == "" {
			t.Errorf("%s was returned with no OID; nothing can poll it", object.Name)
		}
	}
	// Whatever was dropped has to be counted, or the drop is silent -- which is
	// the failure the stored phantoms were standing in for.
	if len(objects) == 0 && report.UnresolvedCount() == 0 {
		t.Error("the module produced nothing and reported nothing discarded")
	}
}

// TestParseReportCountsNothingWhenNothingIsWrong keeps the report from crying
// wolf on a module that parsed cleanly.
func TestParseReportCountsNothingWhenNothingIsWrong(t *testing.T) {
	p := New(standardMIBs(t))
	if err := p.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(p.Close)

	objects, report, err := p.ParseModuleWithReport("SNMPv2-MIB")
	if err != nil {
		t.Fatalf("parsing SNMPv2-MIB: %v", err)
	}
	if len(objects) == 0 {
		t.Fatal("SNMPv2-MIB parsed to nothing")
	}
	if report.UnresolvedCount() != 0 {
		t.Errorf("a clean module reported %d discarded objects: %v",
			report.UnresolvedCount(), report.Unresolved)
	}

	// And every object it did return is usable.
	for _, object := range objects {
		if object.OID == "" {
			t.Errorf("%s has no OID", object.Name)
		}
	}
}

// TestUnresolvedCountToleratesNoReport covers the nil case, since ParseModule
// discards the report and callers may hold one that was never produced.
func TestUnresolvedCountToleratesNoReport(t *testing.T) {
	var absent *ParseReport
	if absent.UnresolvedCount() != 0 {
		t.Error("a nil report counted something")
	}
}
