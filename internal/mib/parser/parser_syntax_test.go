package parser

import (
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/testsupport"

	"github.com/jeremyhahn/go-netmon/internal/mib"
)

// parseModuleOIDs loads a module from the standard MIB directory and returns
// its OIDs keyed by object name.
func parseModuleOIDs(t *testing.T, module string) map[string]*mib.OIDMetadata {
	t.Helper()

	p := New(testsupport.MIBDir("standard"))
	if err := p.Init(); err != nil {
		t.Fatalf("init parser: %v", err)
	}
	t.Cleanup(p.Close)

	oids, err := p.ParseModule(module)
	if err != nil {
		t.Fatalf("parse %s: %v", module, err)
	}

	byName := make(map[string]*mib.OIDMetadata, len(oids))
	for _, oid := range oids {
		byName[oid.Name] = oid
	}
	return byName
}

// TestUnitsFromObjectTypeClause verifies that the UNITS clause declared on an
// OBJECT-TYPE is captured. gosmi exposes this on the resolved node type
// (SmiType.Units); reading it from SmiType.GetRaw() silently drops it because
// the raw type is the unresolved syntax, which carries no UNITS.
func TestUnitsFromObjectTypeClause(t *testing.T) {
	tests := []struct {
		module string
		object string
		want   string
	}{
		{"TCP-MIB", "tcpRtoMin", "milliseconds"},
		{"TCP-MIB", "tcpRtoMax", "milliseconds"},
		{"IP-MIB", "ipReasmTimeout", "seconds"},
	}

	for _, tt := range tests {
		t.Run(tt.module+"::"+tt.object, func(t *testing.T) {
			oids := parseModuleOIDs(t, tt.module)
			oid, ok := oids[tt.object]
			if !ok {
				t.Fatalf("%s not found in %s", tt.object, tt.module)
			}
			if oid.Units != tt.want {
				t.Errorf("Units = %q, want %q", oid.Units, tt.want)
			}
		})
	}
}

// TestUnitsAbsentWhenNotDeclared guards against the formatter inventing units
// for objects that declare none.
func TestUnitsAbsentWhenNotDeclared(t *testing.T) {
	oids := parseModuleOIDs(t, "IF-MIB")

	oid, ok := oids["ifDescr"]
	if !ok {
		t.Fatal("ifDescr not found in IF-MIB")
	}
	if oid.Units != "" {
		t.Errorf("ifDescr Units = %q, want empty", oid.Units)
	}
}

// TestDisplayHintResolvedThroughTextualConvention verifies that DISPLAY-HINT
// survives for both a bare textual convention and a subtyped one. For
// "SYNTAX DisplayString (SIZE (0..255))" the raw type is an anonymous subtype
// with no name and no format, so the hint is only reachable via the resolved
// type that gosmi builds by walking to the parent.
func TestDisplayHintResolvedThroughTextualConvention(t *testing.T) {
	oids := parseModuleOIDs(t, "IF-MIB")

	tests := []struct {
		object          string
		wantType        string
		wantDisplayHint string
	}{
		// Bare textual convention.
		{"ifPhysAddress", "PhysAddress", "1x:"},
		// Subtyped textual convention: the anonymous subtype must resolve
		// back to DisplayString rather than degrading to the base type.
		{"ifDescr", "DisplayString", "255a"},
	}

	for _, tt := range tests {
		t.Run(tt.object, func(t *testing.T) {
			oid, ok := oids[tt.object]
			if !ok {
				t.Fatalf("%s not found in IF-MIB", tt.object)
			}
			if oid.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", oid.Type, tt.wantType)
			}
			if oid.DisplayHint != tt.wantDisplayHint {
				t.Errorf("DisplayHint = %q, want %q", oid.DisplayHint, tt.wantDisplayHint)
			}
		})
	}
}

// TestSyntaxPreservesConcreteTypeNames verifies the common SNMP types are not
// flattened to their base types, since downstream code keys counter-vs-gauge
// behavior off these names.
func TestSyntaxPreservesConcreteTypeNames(t *testing.T) {
	oids := parseModuleOIDs(t, "IF-MIB")

	tests := []struct {
		object string
		want   string
	}{
		{"ifInOctets", "Counter32"},
		{"ifHCInOctets", "Counter64"},
		{"ifSpeed", "Gauge32"},
		{"ifLastChange", "TimeTicks"},
		{"ifIndex", "InterfaceIndex"},
	}

	for _, tt := range tests {
		t.Run(tt.object, func(t *testing.T) {
			oid, ok := oids[tt.object]
			if !ok {
				t.Fatalf("%s not found in IF-MIB", tt.object)
			}
			if oid.Type != tt.want {
				t.Errorf("Type = %q, want %q", oid.Type, tt.want)
			}
			if oid.Syntax != oid.Type {
				t.Errorf("Syntax = %q, want it to match Type %q", oid.Syntax, oid.Type)
			}
		})
	}
}

// TestEnumsResolveThroughTextualConvention verifies enum extraction works for
// a value defined via a textual convention rather than an inline INTEGER
// enumeration. TruthValue is a TC, so the raw anonymous type carries no named
// numbers and only the resolved type has them.
func TestEnumsResolveThroughTextualConvention(t *testing.T) {
	oids := parseModuleOIDs(t, "IF-MIB")

	// Inline enumeration: the baseline case that already worked.
	operStatus, ok := oids["ifOperStatus"]
	if !ok {
		t.Fatal("ifOperStatus not found in IF-MIB")
	}
	wantOper := map[int64]string{
		1: "up", 2: "down", 3: "testing", 4: "unknown",
		5: "dormant", 6: "notPresent", 7: "lowerLayerDown",
	}
	for value, name := range wantOper {
		if got := operStatus.EnumValues[value]; got != name {
			t.Errorf("ifOperStatus[%d] = %q, want %q", value, got, name)
		}
	}

	// Textual convention: ifConnectorPresent is SYNTAX TruthValue.
	connector, ok := oids["ifConnectorPresent"]
	if !ok {
		t.Fatal("ifConnectorPresent not found in IF-MIB")
	}
	if connector.Type != "TruthValue" {
		t.Errorf("ifConnectorPresent Type = %q, want TruthValue", connector.Type)
	}
	if got := connector.EnumValues[1]; got != "true" {
		t.Errorf("ifConnectorPresent[1] = %q, want %q", got, "true")
	}
	if got := connector.EnumValues[2]; got != "false" {
		t.Errorf("ifConnectorPresent[2] = %q, want %q", got, "false")
	}
}
