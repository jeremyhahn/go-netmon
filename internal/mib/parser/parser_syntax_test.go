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

// TestUnitsFromObjectTypeClause checks UNITS is read from the resolved type (SmiType.Units), not GetRaw().
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

// TestDisplayHintResolvedThroughTextualConvention checks DISPLAY-HINT survives a bare and a subtyped TC.
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

// TestEnumsResolveThroughTextualConvention checks enums come from the resolved TC type.
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
