package parser

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sleepinggenius2/gosmi"
	"github.com/sleepinggenius2/gosmi/types"

	"github.com/jeremyhahn/go-netmon/internal/testsupport"
)

// newLoadedParser returns a parser with dirs on its search path and module
// loaded, ready for the gosmi-level lookups the tests below need.
func newLoadedParser(t *testing.T, module string, dirs ...string) (*Parser, gosmi.SmiModule) {
	t.Helper()

	p := New(dirs...)
	if err := p.Init(); err != nil {
		t.Fatalf("init parser: %v", err)
	}
	t.Cleanup(p.Close)

	if _, err := gosmi.LoadModule(module); err != nil {
		t.Fatalf("load %s: %v", module, err)
	}
	loaded, err := gosmi.GetModule(module)
	if err != nil {
		t.Fatalf("get %s: %v", module, err)
	}
	return p, loaded
}

// TestGetOIDFromFirstChild covers the fallback that recovers a table row's OID
// when gosmi renders the row node itself as empty. Without it a row's columns
// are stored with no parent OID, and the table cannot be assembled at all.
//
// The rule is that a column's OID minus its final sub-identifier is the row's,
// so it is checked against rows whose OID gosmi does render, where the answer
// is independently known.
func TestGetOIDFromFirstChild(t *testing.T) {
	p, module := newLoadedParser(t, "IF-MIB", testsupport.MIBDir("standard"))

	rows := module.GetNodes(types.NodeRow)
	if len(rows) == 0 {
		t.Fatal("IF-MIB yielded no table rows")
	}

	checked := 0
	for _, row := range rows {
		rendered := strings.TrimPrefix(row.RenderNumeric(), ".")
		if rendered == "" {
			continue // nothing to compare the fallback against
		}

		derived := p.getOIDFromFirstChild(row, "IF-MIB")
		if derived != rendered {
			t.Errorf("%s: derived OID %q, want %q", row.GetRaw().Name, derived, rendered)
		}
		checked++
	}

	if checked == 0 {
		t.Fatal("no row had a rendered OID to check the fallback against")
	}
}

// TestGetOIDFromFirstChildWithoutColumns covers the case the fallback cannot
// resolve. Returning an empty string rather than a truncated or invented OID is
// what keeps a bad OID out of the store.
func TestGetOIDFromFirstChildWithoutColumns(t *testing.T) {
	p, module := newLoadedParser(t, "IF-MIB", testsupport.MIBDir("standard"))

	// A leaf column has no columns of its own.
	leaf, err := gosmi.GetNode("ifDescr", module)
	if err != nil {
		t.Fatalf("get ifDescr: %v", err)
	}

	if got := p.getOIDFromFirstChild(leaf, "IF-MIB"); got != "" {
		t.Errorf("getOIDFromFirstChild on a node with no columns = %q, want empty", got)
	}
}

// TestGetTypeName covers the whole gosmi base-type mapping. These names are not
// cosmetic: IsCounter, IsGauge and the chart-type decision compare against them
// exactly, so a wrong or missing name silently reclassifies a metric.
func TestGetTypeName(t *testing.T) {
	p := New()

	tests := map[types.BaseType]string{
		types.BaseTypeInteger32:        "Integer32",
		types.BaseTypeInteger64:        "Integer64",
		types.BaseTypeUnsigned32:       "Unsigned32",
		types.BaseTypeUnsigned64:       "Unsigned64",
		types.BaseTypeOctetString:      "OctetString",
		types.BaseTypeObjectIdentifier: "ObjectIdentifier",
		types.BaseTypeEnum:             "Integer",
		types.BaseTypeBits:             "Bits",
		types.BaseTypeUnknown:          "Unknown",
		types.BaseTypeFloat32:          "Unknown",
	}

	for baseType, want := range tests {
		if got := p.getTypeName(baseType); got != want {
			t.Errorf("getTypeName(%v) = %q, want %q", baseType, got, want)
		}
	}
}

// TestGetAccessString covers the access mapping that IsPollable gates on. An
// access string this does not produce makes every object of that access
// unpollable, which is a silent, whole-module outage.
func TestGetAccessString(t *testing.T) {
	p := New()

	tests := map[types.Access]string{
		types.AccessNotAccessible: "not-accessible",
		types.AccessReadOnly:      "read-only",
		types.AccessReadWrite:     "read-write",
		types.AccessNotify:        "accessible-for-notify",
		types.AccessUnknown:       "unknown",
	}

	for access, want := range tests {
		if got := p.getAccessString(access); got != want {
			t.Errorf("getAccessString(%v) = %q, want %q", access, got, want)
		}
	}
}

// TestGetStatusString covers the status mapping, the other half of the
// IsPollable gate. Only "current" is pollable, so anything mapped wrongly to or
// away from it changes what gets polled.
func TestGetStatusString(t *testing.T) {
	p := New()

	tests := map[types.Status]string{
		types.StatusCurrent:    "current",
		types.StatusDeprecated: "deprecated",
		types.StatusObsolete:   "obsolete",
		types.StatusMandatory:  "mandatory",
		types.StatusOptional:   "optional",
		types.StatusUnknown:    "unknown",
	}

	for status, want := range tests {
		if got := p.getStatusString(status); got != want {
			t.Errorf("getStatusString(%v) = %q, want %q", status, got, want)
		}
	}
}

func TestGetModules(t *testing.T) {
	p, _ := newLoadedParser(t, "IF-MIB", testsupport.MIBDir("standard"))

	modules := p.GetModules()
	if len(modules) == 0 {
		t.Fatal("GetModules returned nothing after a module was loaded")
	}

	loaded := make(map[string]bool, len(modules))
	for _, name := range modules {
		loaded[name] = true
	}

	// IF-MIB and the modules it imports must all be listed.
	for _, want := range []string{"IF-MIB", "SNMPv2-SMI", "SNMPv2-TC"} {
		if !loaded[want] {
			t.Errorf("GetModules did not list %s; got %v", want, modules)
		}
	}
}

func TestLoadModuleFromFile(t *testing.T) {
	p := New()
	if err := p.Init(); err != nil {
		t.Fatalf("init parser: %v", err)
	}
	t.Cleanup(p.Close)

	// The standard directory has to be reachable for IF-MIB's imports, and
	// LoadModuleFromFile is expected to add the file's own directory itself.
	gosmi.AppendPath(testsupport.MIBDir("standard"))

	info, err := p.LoadModuleFromFile(filepath.Join(testsupport.MIBDir("standard"), "IF-MIB.txt"))
	if err != nil {
		t.Fatalf("LoadModuleFromFile: %v", err)
	}
	if info.Name != "IF-MIB" {
		t.Errorf("Name = %q, want %q", info.Name, "IF-MIB")
	}
	if info.OIDCount == 0 {
		t.Error("OIDCount = 0; the module was reported as holding no objects")
	}
	if len(info.Imports) == 0 {
		t.Error("Imports is empty; IF-MIB imports from SNMPv2-SMI and others")
	}
}

func TestLoadModuleFromFileRejectsUnknownFile(t *testing.T) {
	p := New()
	if err := p.Init(); err != nil {
		t.Fatalf("init parser: %v", err)
	}
	t.Cleanup(p.Close)

	_, err := p.LoadModuleFromFile(filepath.Join(t.TempDir(), "NO-SUCH-MIB.txt"))
	if err == nil {
		t.Fatal("LoadModuleFromFile succeeded for a file that does not exist")
	}
	if !strings.Contains(err.Error(), "NO-SUCH-MIB") {
		t.Errorf("error %q does not name the module it failed to load", err)
	}
}

func TestLoadModuleRejectsUnknownModule(t *testing.T) {
	p := New(testsupport.MIBDir("standard"))
	if err := p.Init(); err != nil {
		t.Fatalf("init parser: %v", err)
	}
	t.Cleanup(p.Close)

	if _, err := p.LoadModule("NO-SUCH-MIB"); err == nil {
		t.Fatal("LoadModule succeeded for a module that is not on the search path")
	}
}

func TestParseModuleRejectsUnknownModule(t *testing.T) {
	p := New(testsupport.MIBDir("standard"))
	if err := p.Init(); err != nil {
		t.Fatalf("init parser: %v", err)
	}
	t.Cleanup(p.Close)

	oids, err := p.ParseModule("NO-SUCH-MIB")
	if err == nil {
		t.Fatal("ParseModule succeeded for a module that is not on the search path")
	}
	if oids != nil {
		t.Error("ParseModule returned OIDs alongside an error")
	}
}

func TestGetTableInfoRejectsUnknownModule(t *testing.T) {
	p := New(testsupport.MIBDir("standard"))
	if err := p.Init(); err != nil {
		t.Fatalf("init parser: %v", err)
	}
	t.Cleanup(p.Close)

	if _, err := p.GetTableInfo("NO-SUCH-MIB"); err == nil {
		t.Fatal("GetTableInfo succeeded for a module that was never loaded")
	}
}
