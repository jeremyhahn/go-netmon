package parser

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/sleepinggenius2/gosmi"

	"github.com/jeremyhahn/go-netmon/internal/testsupport"
)

const isolationMIB = `ISOLATION-MIB DEFINITIONS ::= BEGIN
IMPORTS
    MODULE-IDENTITY, OBJECT-TYPE, Integer32
        FROM SNMPv2-SMI;

isolationModule MODULE-IDENTITY
    LAST-UPDATED "202610210000Z"
    ORGANIZATION "test"
    CONTACT-INFO "test"
    DESCRIPTION  "A module with one object."
    ::= { 1 3 6 1 4 1 99963 }

isolationValue OBJECT-TYPE
    SYNTAX      Integer32
    MAX-ACCESS  read-only
    STATUS      current
    DESCRIPTION "One readable value."
    ::= { isolationModule 1 }

END
`

// isolationDir writes ISOLATION-MIB to a new directory, with SNMPv2-SMI beside
// it when withSMI is set.
func isolationDir(t *testing.T, withSMI bool) string {
	t.Helper()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ISOLATION-MIB.mib"), []byte(isolationMIB), 0o644); err != nil {
		t.Fatalf("write ISOLATION-MIB: %v", err)
	}
	if withSMI {
		smi, err := os.ReadFile(filepath.Join(testsupport.MIBDir("standard"), "SNMPv2-SMI.txt"))
		if err != nil {
			t.Fatalf("read SNMPv2-SMI: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SNMPv2-SMI.txt"), smi, 0o644); err != nil {
			t.Fatalf("write SNMPv2-SMI: %v", err)
		}
	}
	return dir
}

// parseIsolationMIB opens a parser on dir alone, parses ISOLATION-MIB and
// closes the parser, the way one `mib validate` run does.
func parseIsolationMIB(t *testing.T, dir string) *ParseReport {
	t.Helper()

	p := New(dir)
	if err := p.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer p.Close()

	if got, want := gosmi.GetPath(), dir; got != want {
		t.Errorf("search path = %q, want only %q", got, want)
	}
	info, err := p.LoadModuleFromFile(filepath.Join(dir, "ISOLATION-MIB.mib"))
	if err != nil {
		t.Fatalf("LoadModuleFromFile: %v", err)
	}
	_, report, err := p.ParseModuleWithReport(info.Name)
	if err != nil {
		t.Fatalf("ParseModuleWithReport: %v", err)
	}
	return report
}

// TestCloseLeavesNothingForTheNextParser checks that gosmi, which is one
// library per process, starts empty for each parser once the last one has
// closed: no modules and no search path survive Close. Every test that loads
// modules relies on this to be independent of the tests that ran before it.
func TestCloseLeavesNothingForTheNextParser(t *testing.T) {
	complete := isolationDir(t, true)
	incomplete := isolationDir(t, false)

	for i, run := range []struct {
		dir      string
		degraded bool
	}{
		{complete, false},
		{incomplete, true},
		{complete, false},
	} {
		report := parseIsolationMIB(t, run.dir)
		if got := report.Err() != nil; got != run.degraded {
			t.Errorf("run %d: degraded = %v, want %v (%v)", i, got, run.degraded, report.Err())
		}
	}
}

// TestInitSearchesOnlyTheGivenDirectories checks that the host's SMIPATH does
// not reach the search path. gosmi.Init seeds the path from every existing
// directory in SMIPATH; a parser given one directory must search that one only,
// or a module it was not given resolves on one host and not on another.
func TestInitSearchesOnlyTheGivenDirectories(t *testing.T) {
	t.Setenv("SMIPATH", testsupport.MIBDir("standard"))

	report := parseIsolationMIB(t, isolationDir(t, false))
	if report.Err() == nil {
		t.Error("SNMPv2-SMI resolved from SMIPATH, which the parser was not given")
	}
}

// leafMIB declares module name with one object under enterprises.n. It hangs
// off the imported enterprises, as real MIBs do: a second module rooted at a
// literal { 1 3 6 1 4 1 n } in one session resolves no OIDs, a separate defect.
func leafMIB(name, root string, n int) string {
	return fmt.Sprintf(`%[1]s DEFINITIONS ::= BEGIN
IMPORTS
    MODULE-IDENTITY, OBJECT-TYPE, Integer32, enterprises
        FROM SNMPv2-SMI;

%[2]s MODULE-IDENTITY
    LAST-UPDATED "202610210000Z"
    ORGANIZATION "test"
    CONTACT-INFO "test"
    DESCRIPTION  "A module with one object."
    ::= { enterprises %[3]d }

%[2]sValue OBJECT-TYPE
    SYNTAX      Integer32
    MAX-ACCESS  read-only
    STATUS      current
    DESCRIPTION "One readable value."
    ::= { %[2]s 1 }

END
`, name, root, n)
}

// writeModuleDir writes one module to a new directory and returns its path.
func writeModuleDir(t *testing.T, name, content string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, name+".mib")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// TestLoadModuleFromFileLeavesTheSearchPathAsConfigured checks that loading a
// file adds its directory for that load only. The web UI loads every upload
// through one long-lived parser; a directory left behind by each would grow the
// path for the life of the process.
func TestLoadModuleFromFileLeavesTheSearchPathAsConfigured(t *testing.T) {
	configured := isolationDir(t, true)
	p := New(configured)
	if err := p.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(p.Close)

	for n := 1; n <= 3; n++ {
		name := fmt.Sprintf("LEAF%d-MIB", n)
		path := writeModuleDir(t, name, leafMIB(name, fmt.Sprintf("leaf%d", n), n))

		info, err := p.LoadModuleFromFile(path)
		if err != nil {
			t.Fatalf("LoadModuleFromFile %s: %v", name, err)
		}
		if got := gosmi.GetPath(); got != configured {
			t.Fatalf("after loading %s the search path is %q, want %q", name, got, configured)
		}
		// Parsed by declared name once the file's directory is off the path.
		oids, report, err := p.ParseModuleWithReport(info.Name)
		if err != nil {
			t.Fatalf("ParseModuleWithReport %s: %v", info.Name, err)
		}
		if len(oids) == 0 || report.Err() != nil {
			t.Errorf("%s: %d objects, report %v; want objects and a clean report", info.Name, len(oids), report.Err())
		}
	}
}

// TestLoadModuleFromFileResolvesAModuleLoadedEarlier checks that taking a
// file's directory back off the path does not lose what was loaded from it: a
// module from another directory that IMPORTS it still resolves, because gosmi
// finds an import among the loaded modules before it searches the path.
func TestLoadModuleFromFileResolvesAModuleLoadedEarlier(t *testing.T) {
	p := New(isolationDir(t, true))
	if err := p.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(p.Close)

	if _, err := p.LoadModuleFromFile(writeModuleDir(t, "LEAF1-MIB", leafMIB("LEAF1-MIB", "leaf1", 1))); err != nil {
		t.Fatalf("LoadModuleFromFile LEAF1-MIB: %v", err)
	}

	dependent := writeModuleDir(t, "DEPENDENT-MIB", `DEPENDENT-MIB DEFINITIONS ::= BEGIN
IMPORTS
    OBJECT-TYPE, Integer32
        FROM SNMPv2-SMI
    leaf1
        FROM LEAF1-MIB;

dependentValue OBJECT-TYPE
    SYNTAX      Integer32
    MAX-ACCESS  read-only
    STATUS      current
    DESCRIPTION "Hangs off a module loaded from another directory."
    ::= { leaf1 2 }

END
`)
	info, err := p.LoadModuleFromFile(dependent)
	if err != nil {
		t.Fatalf("LoadModuleFromFile DEPENDENT-MIB: %v", err)
	}
	oids, report, err := p.ParseModuleWithReport(info.Name)
	if err != nil {
		t.Fatalf("ParseModuleWithReport: %v", err)
	}
	if len(oids) == 0 || report.Err() != nil {
		t.Errorf("DEPENDENT-MIB: %d objects, report %v; want LEAF1-MIB resolved", len(oids), report.Err())
	}
}
