package parser

import (
	"errors"
	"slices"
	"testing"
)

// degradedModule takes its OIDs from SNMPv2-SMI, which is always present, and
// one type from TEST-ABSENT-TC-MIB, which exists nowhere. Every object
// resolves to an OID; testStatus cannot resolve its type.
const degradedModule = `TEST-DEGRADED-MIB DEFINITIONS ::= BEGIN
IMPORTS
    OBJECT-TYPE, enterprises FROM SNMPv2-SMI
    TestStatus FROM TEST-ABSENT-TC-MIB;

testDegraded OBJECT IDENTIFIER ::= { enterprises 99962 }

testStatus OBJECT-TYPE
    SYNTAX TestStatus
    MAX-ACCESS read-only
    STATUS current
    DESCRIPTION "Typed by a module that is not there."
    ::= { testDegraded 1 }

testReading OBJECT-TYPE
    SYNTAX INTEGER
    MAX-ACCESS read-only
    STATUS current
    DESCRIPTION "Typed by the language itself."
    ::= { testDegraded 2 }

END
`

// A module whose IMPORTS name an absent module still loads, but says so: the
// report names the missing module and the objects it left untyped, and the
// caller can tell by type (#62).
func TestAModuleWithAnAbsentImportReportsIt(t *testing.T) {
	dir := t.TempDir()
	p := New(standardMIBs(t), dir)
	if err := p.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(p.Close)

	writeModule(t, dir, "TEST-DEGRADED-MIB", degradedModule)

	objects, report, err := p.ParseModuleWithReport("TEST-DEGRADED-MIB")
	if err != nil {
		t.Fatalf("an unresolved import must not fail the parse: %v", err)
	}

	names := make([]string, 0, len(objects))
	for _, object := range objects {
		names = append(names, object.Name)
	}
	for _, want := range []string{"testStatus", "testReading"} {
		if !slices.Contains(names, want) {
			t.Errorf("%s was not returned; the module must still load (got %v)", want, names)
		}
	}

	if !slices.Equal(report.MissingImports, []string{"TEST-ABSENT-TC-MIB"}) {
		t.Errorf("MissingImports = %v, want [TEST-ABSENT-TC-MIB]", report.MissingImports)
	}
	if !slices.Contains(report.Untyped, "testStatus") {
		t.Errorf("Untyped = %v, want it to name testStatus", report.Untyped)
	}
	if slices.Contains(report.Untyped, "testReading") {
		t.Errorf("Untyped = %v names testReading, whose type resolved", report.Untyped)
	}

	degraded := report.Err()
	if !errors.Is(degraded, ErrUnresolvedImports) {
		t.Fatalf("report.Err() = %v, want ErrUnresolvedImports", degraded)
	}
	var typed *UnresolvedImportsError
	if !errors.As(degraded, &typed) {
		t.Fatalf("report.Err() is %T, want *UnresolvedImportsError", degraded)
	}
	if typed.Module != "TEST-DEGRADED-MIB" {
		t.Errorf("Module = %q, want TEST-DEGRADED-MIB", typed.Module)
	}
	if !slices.Equal(typed.Missing, report.MissingImports) ||
		!slices.Equal(typed.Untyped, report.Untyped) {
		t.Errorf("the error %+v does not carry the report %+v", typed, report)
	}
}

// A module whose imports all resolve reports none, and no error.
func TestAModuleWithEveryImportResolvedReportsNone(t *testing.T) {
	p := New(standardMIBs(t))
	if err := p.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(p.Close)

	_, report, err := p.ParseModuleWithReport("IF-MIB")
	if err != nil {
		t.Fatalf("parsing IF-MIB: %v", err)
	}
	if len(report.MissingImports) != 0 {
		t.Errorf("MissingImports = %v, want none", report.MissingImports)
	}
	if len(report.Untyped) != 0 {
		t.Errorf("Untyped = %v, want none", report.Untyped)
	}
	if err := report.Err(); err != nil {
		t.Errorf("report.Err() = %v, want nil", err)
	}
}

func TestErrToleratesNoReport(t *testing.T) {
	var absent *ParseReport
	if err := absent.Err(); err != nil {
		t.Errorf("a nil report returned %v", err)
	}
}

func TestUnresolvedImportsErrorNamesWhatIsMissing(t *testing.T) {
	err := &UnresolvedImportsError{
		Module:  "ATM-MIB",
		Missing: []string{"ATM-TC-MIB"},
		Untyped: []string{"atmVplOperStatus"},
	}
	want := "mib module ATM-MIB loaded degraded: unresolved imports ATM-TC-MIB; 1 objects have no type"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}
