package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/testsupport"
)

// The validator's whole value is the distinction between "parsed" and "would
// actually work". Both of the cases below were wrong in the first version and
// were caught by running it against real MIB trees, so they are pinned here.

// writeMIB puts a file in a directory and returns the directory.
func writeMIB(t *testing.T, dir, name, content string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// minimalMIB is a module that defines one object, so it is expected to
// contribute one.
const minimalMIB = `TEST-MIB DEFINITIONS ::= BEGIN
IMPORTS
    MODULE-IDENTITY, OBJECT-TYPE, Integer32
        FROM SNMPv2-SMI;

testModule MODULE-IDENTITY
    LAST-UPDATED "202608110000Z"
    ORGANIZATION "test"
    CONTACT-INFO "test"
    DESCRIPTION  "A module with one object."
    ::= { 1 3 6 1 4 1 99999 }

testValue OBJECT-TYPE
    SYNTAX      Integer32
    MAX-ACCESS  read-only
    STATUS      current
    DESCRIPTION "One readable value."
    ::= { testModule 1 }

END
`

// TestValidateReportsAModuleThatResolves is the working case.
func TestValidateReportsAModuleThatResolves(t *testing.T) {
	dir := t.TempDir()
	writeMIB(t, dir, "TEST-MIB.mib", minimalMIB)
	copyStandardMIB(t, dir, "SNMPv2-SMI.txt")

	var out bytes.Buffer
	if err := runMIBValidate(&out, []string{dir}, false); err != nil {
		t.Fatalf("validate: %v\n%s", err, out.String())
	}
	text := out.String()
	if !strings.Contains(text, "ok") || !strings.Contains(text, "TEST-MIB") {
		t.Errorf("report does not show the module as ok:\n%s", text)
	}
	if strings.Contains(text, "WARN") {
		t.Errorf("a module whose imports all resolve is reported as degraded:\n%s", text)
	}
}

// copyStandardMIB puts one of the repository's standard MIBs in dir. gosmi
// searches only the directories validate is given, so a module that imports
// SNMPv2-SMI resolves only when SNMPv2-SMI is in one of them.
func copyStandardMIB(t *testing.T, dir, name string) {
	t.Helper()

	content, err := os.ReadFile(filepath.Join(testsupport.MIBDir("standard"), name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	writeMIB(t, dir, name, string(content))
}

// TestValidateStartsFromAnEmptyLibrary checks that one run's modules and search
// path do not reach the next. gosmi is a process-wide library; each run
// initializes it and closes it, which discards both. If that ever stopped, a
// complete run would make a later incomplete one look complete, and an
// incomplete run would leave a later complete one degraded.
func TestValidateStartsFromAnEmptyLibrary(t *testing.T) {
	complete := t.TempDir()
	writeMIB(t, complete, "TEST-MIB.mib", minimalMIB)
	copyStandardMIB(t, complete, "SNMPv2-SMI.txt")

	incomplete := t.TempDir()
	writeMIB(t, incomplete, "TEST-MIB.mib", minimalMIB)

	for i, run := range []struct {
		dir      string
		degraded bool
	}{
		{complete, false},
		{incomplete, true},
		{complete, false},
	} {
		var out bytes.Buffer
		if err := runMIBValidate(&out, []string{run.dir}, false); err != nil {
			t.Fatalf("run %d: validate: %v\n%s", i, err, out.String())
		}
		text := out.String()
		if got := strings.Contains(text, "WARN"); got != run.degraded {
			t.Errorf("run %d: degraded = %v, want %v:\n%s", i, got, run.degraded, text)
		}
		if run.degraded && !strings.Contains(text, "SNMPv2-SMI") {
			t.Errorf("run %d: the warning does not name SNMPv2-SMI:\n%s", i, text)
		}
	}
}

// TestValidateIgnoresSMIPATH checks that validate searches exactly the
// directories it is given. A host whose SMIPATH holds SNMPv2-SMI must not make
// a directory without it look complete.
func TestValidateIgnoresSMIPATH(t *testing.T) {
	t.Setenv("SMIPATH", testsupport.MIBDir("standard"))

	dir := t.TempDir()
	writeMIB(t, dir, "TEST-MIB.mib", minimalMIB)

	var out bytes.Buffer
	if err := runMIBValidate(&out, []string{dir}, false); err != nil {
		t.Fatalf("validate: %v\n%s", err, out.String())
	}
	if text := out.String(); !strings.Contains(text, "WARN") || !strings.Contains(text, "SNMPv2-SMI") {
		t.Errorf("SNMPv2-SMI resolved from SMIPATH, which validate was not given:\n%s", text)
	}
}

// TestValidateFailsWhenAnImportIsMissing is the case the command exists for:
// gosmi reports a module whose IMPORTS did not resolve as loaded, and the
// deployment then produces no metrics for it with nothing in the log.
func TestValidateFailsWhenAnImportIsMissing(t *testing.T) {
	dir := t.TempDir()

	// The objects hang off an identifier defined in a vendor module that is not
	// present. Without SNMPv2-SMI gosmi still loads a module that imports only
	// the SMI base types, with those types unresolved (a WARN) - it is the
	// vendor root that has to be missing to reproduce the real failure, which is what
	// CISCO-PRODUCTS-MIB does without CISCO-SMI: it loads with zero nodes.
	writeMIB(t, dir, "ORPHAN-MIB.mib", `ORPHAN-MIB DEFINITIONS ::= BEGIN
IMPORTS
    OBJECT-TYPE, Integer32
        FROM SNMPv2-SMI
    absentVendorRoot
        FROM ABSENT-VENDOR-SMI;

orphanValue OBJECT-TYPE
    SYNTAX      Integer32
    MAX-ACCESS  read-only
    STATUS      current
    DESCRIPTION "Hangs off an identifier this directory does not define."
    ::= { absentVendorRoot 1 }

END
`)

	var out bytes.Buffer
	err := runMIBValidate(&out, []string{dir}, false)

	// SNMPv2-SMI is not in the directory, so the module cannot resolve. Whether
	// gosmi reports that as a load failure or as a module with no objects, the
	// command must not report success.
	if err == nil {
		t.Fatalf("a module whose imports are absent was reported as fine:\n%s", out.String())
	}
	var result *MIBValidateResultError
	if !errors.As(err, &result) {
		t.Fatalf("error = %T (%v), want a *MIBValidateResultError", err, err)
	}
}

// A module that resolves its OIDs but takes a type from an absent module is
// usable, so validation passes, but the report names what it could not
// import (#62).
func TestValidateWarnsWhenATypeImportIsMissing(t *testing.T) {
	dir := t.TempDir()
	writeMIB(t, dir, "DEGRADED-MIB.mib", `DEGRADED-MIB DEFINITIONS ::= BEGIN
IMPORTS
    MODULE-IDENTITY, OBJECT-TYPE
        FROM SNMPv2-SMI
    AbsentStatus
        FROM ABSENT-TC-MIB;

degradedModule MODULE-IDENTITY
    LAST-UPDATED "202610210000Z"
    ORGANIZATION "test"
    CONTACT-INFO "test"
    DESCRIPTION  "A module typed partly from an absent module."
    ::= { 1 3 6 1 4 1 99962 }

degradedStatus OBJECT-TYPE
    SYNTAX      AbsentStatus
    MAX-ACCESS  read-only
    STATUS      current
    DESCRIPTION "Typed by a module this directory does not define."
    ::= { degradedModule 1 }

END
`)

	var out bytes.Buffer
	if err := runMIBValidate(&out, []string{dir}, true); err != nil {
		t.Fatalf("a degraded module failed validation: %v\n%s", err, out.String())
	}
	text := out.String()
	for _, want := range []string{"WARN", "DEGRADED-MIB", "ABSENT-TC-MIB", "1 ok with unresolved imports"} {
		if !strings.Contains(text, want) {
			t.Errorf("report does not mention %q:\n%s", want, text)
		}
	}
}

// TestValidateSkipsFilesThatAreNotMIBs covers the first false positive: a MIB
// directory routinely holds a README, and calling those failures made the
// report cry wolf on both real trees in this repository.
func TestValidateSkipsFilesThatAreNotMIBs(t *testing.T) {
	dir := t.TempDir()
	writeMIB(t, dir, "TEST-MIB.mib", minimalMIB)
	writeMIB(t, dir, "readme_mibs.txt", "These MIBs came from the vendor's support site.\n")

	var out bytes.Buffer
	if err := runMIBValidate(&out, []string{dir}, false); err != nil {
		t.Fatalf("a README made validation fail: %v\n%s", err, out.String())
	}
	if text := out.String(); !strings.Contains(text, "1 non-MIB files skipped") {
		t.Errorf("the report does not account for the skipped file:\n%s", text)
	}
}

// TestValidateAcceptsADefinitionsOnlyModule covers the second false positive.
//
// A module that defines only textual conventions correctly produces no objects.
// SNMPv2-TC and SNMPv2-CONF are both like this and both ship in the standard
// set, so treating "no objects" as failure condemned two of the most common
// files there are. SNMPv2-CONF additionally mentions OBJECT-TYPE in a comment,
// which is why comments are stripped before deciding.
func TestValidateAcceptsADefinitionsOnlyModule(t *testing.T) {
	dir := t.TempDir()
	writeMIB(t, dir, "TC-ONLY-MIB.mib", `TC-ONLY-MIB DEFINITIONS ::= BEGIN
-- This module defines conventions only. Values are carried in an object's
-- OBJECT-TYPE macro elsewhere.
END
`)

	var out bytes.Buffer
	if err := runMIBValidate(&out, []string{dir}, false); err != nil {
		t.Fatalf("a definitions-only module was reported as broken: %v\n%s", err, out.String())
	}
	if text := out.String(); strings.Contains(text, "EMPTY") {
		t.Errorf("a module that defines no objects was flagged EMPTY:\n%s", text)
	}
}

// TestValidateRejectsABadDirectory is the bad-input case.
func TestValidateRejectsABadDirectory(t *testing.T) {
	var out bytes.Buffer

	if err := runMIBValidate(&out, []string{filepath.Join(t.TempDir(), "absent")}, false); err == nil {
		t.Error("a directory that does not exist was accepted")
	}

	empty := t.TempDir()
	if err := runMIBValidate(&out, []string{empty}, false); err == nil {
		t.Error("a directory holding no MIB files was accepted")
	}

	file := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := runMIBValidate(&out, []string{file}, false); err == nil {
		t.Error("a regular file was accepted as a directory")
	}
}

// TestStripMIBCommentsLeavesDefinitionsAlone pins the comment handling on its
// own, since it is what separates a real definition from a mention of one.
func TestStripMIBCommentsLeavesDefinitionsAlone(t *testing.T) {
	source := []byte("-- mentions OBJECT-TYPE in prose\nrealThing OBJECT-TYPE\n")

	stripped := stripMIBComments(source)
	if bytes.Contains(stripped, []byte("prose")) {
		t.Errorf("the comment survived stripping: %q", stripped)
	}
	if !objectDefinition.Match(stripped) {
		t.Errorf("the real definition was lost: %q", stripped)
	}
	if objectDefinition.Match(stripMIBComments([]byte("-- only OBJECT-TYPE in a comment\n"))) {
		t.Error("a construct named only in a comment was treated as a definition")
	}
}
