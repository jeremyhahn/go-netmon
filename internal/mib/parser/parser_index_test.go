package parser

import (
	"testing"

	"github.com/sleepinggenius2/gosmi/models"
	"github.com/sleepinggenius2/gosmi/types"

	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/testsupport"
)

// parseModuleOIDsFrom loads a module from the given MIB directories and returns
// its OIDs keyed by object name. Tables whose index columns live in another
// module (or whose textual conventions do) need more than one directory on the
// search path.
func parseModuleOIDsFrom(t *testing.T, module string, dirs ...string) map[string]*mib.OIDMetadata {
	t.Helper()

	p := New(dirs...)
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

// TestIndexColumnsFromRealMIBs verifies that a table row records the resolved
// type of each of its index columns, not just the column names. The types come
// from gosmi's resolved node type; the raw SYNTAX is anonymous for subtyped
// textual conventions and would yield an empty name.
func TestIndexColumnsFromRealMIBs(t *testing.T) {
	tests := []struct {
		module string
		entry  string
		want   []mib.IndexColumn
		legacy []string
	}{
		{
			module: "IF-MIB",
			entry:  "ifEntry",
			want: []mib.IndexColumn{
				{Name: "ifIndex", OID: "1.3.6.1.2.1.2.2.1.1", Type: "InterfaceIndex", BaseType: "Integer32"},
			},
			legacy: []string{"ifIndex"},
		},
		{
			module: "IP-MIB",
			entry:  "ipAddrEntry",
			want: []mib.IndexColumn{
				{Name: "ipAdEntAddr", OID: "1.3.6.1.2.1.4.20.1.1", Type: "IpAddress", BaseType: "OctetString", FixedLength: 4},
			},
			legacy: []string{"ipAdEntAddr"},
		},
		{
			module: "TCP-MIB",
			entry:  "tcpConnEntry",
			want: []mib.IndexColumn{
				{Name: "tcpConnLocalAddress", OID: "1.3.6.1.2.1.6.13.1.2", Type: "IpAddress", BaseType: "OctetString", FixedLength: 4},
				{Name: "tcpConnLocalPort", OID: "1.3.6.1.2.1.6.13.1.3", Type: "Integer32", BaseType: "Integer32"},
				{Name: "tcpConnRemAddress", OID: "1.3.6.1.2.1.6.13.1.4", Type: "IpAddress", BaseType: "OctetString", FixedLength: 4},
				{Name: "tcpConnRemPort", OID: "1.3.6.1.2.1.6.13.1.5", Type: "Integer32", BaseType: "Integer32"},
			},
			legacy: []string{"tcpConnLocalAddress", "tcpConnLocalPort", "tcpConnRemAddress", "tcpConnRemPort"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.module+"::"+tt.entry, func(t *testing.T) {
			oids := parseModuleOIDsFrom(t, tt.module, testsupport.MIBDir("standard"))
			entry, ok := oids[tt.entry]
			if !ok {
				t.Fatalf("%s not found in %s", tt.entry, tt.module)
			}

			assertIndexColumns(t, entry.IndexColumns, tt.want)

			// The name-only list stays exactly as it was; the webui JSON and
			// older stored metadata depend on it.
			if len(entry.Index) != len(tt.legacy) {
				t.Fatalf("Index = %v, want %v", entry.Index, tt.legacy)
			}
			for i, name := range tt.legacy {
				if entry.Index[i] != name {
					t.Errorf("Index[%d] = %q, want %q", i, entry.Index[i], name)
				}
			}
		})
	}
}

// TestIndexColumnsImplied verifies that IMPLIED is recorded, and only on the
// final index column. Without it, a variable-length string index is decoded as
// if it carried a leading length sub-identifier.
func TestIndexColumnsImplied(t *testing.T) {
	oids := parseModuleOIDsFrom(t, "SNMP-TARGET-MIB", testsupport.MIBDirs("standard", "asr1001")...)

	entry, ok := oids["snmpTargetAddrEntry"]
	if !ok {
		t.Fatal("snmpTargetAddrEntry not found in SNMP-TARGET-MIB")
	}

	assertIndexColumns(t, entry.IndexColumns, []mib.IndexColumn{
		{
			Name:     "snmpTargetAddrName",
			OID:      "1.3.6.1.6.3.12.1.2.1.1",
			Type:     "SnmpAdminString",
			BaseType: "OctetString",
			Implied:  true,
		},
	})
}

// TestIndexColumnsNotImpliedForMultiIndexTable guards the "last column only"
// rule: a table without IMPLIED must not have the flag set anywhere.
func TestIndexColumnsNotImpliedForMultiIndexTable(t *testing.T) {
	oids := parseModuleOIDsFrom(t, "TCP-MIB", testsupport.MIBDir("standard"))

	entry, ok := oids["tcpConnEntry"]
	if !ok {
		t.Fatal("tcpConnEntry not found in TCP-MIB")
	}
	for _, col := range entry.IndexColumns {
		if col.Implied {
			t.Errorf("%s: Implied = true, want false", col.Name)
		}
	}
}

// TestIndexColumnsResolveThroughImportedModules covers an index whose types
// come from another module: ipAddressEntry indexes on InetAddressType and
// InetAddress, both defined in INET-ADDRESS-MIB. That module used to be absent
// from mibs/, which left gosmi with a nil node type and the base types empty —
// the index still decoded, but only by falling back to a name heuristic.
//
// With the module present the base types must resolve, because that is what
// lets an index value be decoded by its actual type rather than guessed at.
func TestIndexColumnsResolveThroughImportedModules(t *testing.T) {
	oids := parseModuleOIDsFrom(t, "IP-MIB", testsupport.MIBDir("standard"))

	entry, ok := oids["ipAddressEntry"]
	if !ok {
		t.Fatal("ipAddressEntry not found in IP-MIB")
	}

	if len(entry.IndexColumns) != len(entry.Index) {
		t.Fatalf("IndexColumns has %d entries, Index has %d", len(entry.IndexColumns), len(entry.Index))
	}
	if len(entry.IndexColumns) == 0 {
		t.Fatal("IndexColumns is empty")
	}
	for i, col := range entry.IndexColumns {
		if col.Name != entry.Index[i] {
			t.Errorf("IndexColumns[%d].Name = %q, want %q", i, col.Name, entry.Index[i])
		}
		if col.BaseType == "" {
			t.Errorf("IndexColumns[%d] (%s) has no base type: its defining module is not resolving", i, col.Name)
		}
	}
}

// TestIndexColumnsEmptyForNonRowNodes verifies that scalars and plain columns
// carry no index metadata at all.
func TestIndexColumnsEmptyForNonRowNodes(t *testing.T) {
	oids := parseModuleOIDsFrom(t, "IF-MIB", testsupport.MIBDir("standard"))

	for _, name := range []string{"ifNumber", "ifDescr", "ifTable"} {
		oid, ok := oids[name]
		if !ok {
			t.Fatalf("%s not found in IF-MIB", name)
		}
		if len(oid.IndexColumns) != 0 {
			t.Errorf("%s: IndexColumns = %v, want empty", name, oid.IndexColumns)
		}
	}
}

// TestGetTableInfoIndexColumns verifies that the table view exposes the same
// index metadata as the row view.
func TestGetTableInfoIndexColumns(t *testing.T) {
	p := New(testsupport.MIBDirs("standard", "asr1001")...)
	if err := p.Init(); err != nil {
		t.Fatalf("init parser: %v", err)
	}
	t.Cleanup(p.Close)

	if _, err := p.LoadModule("TCP-MIB"); err != nil {
		t.Fatalf("load TCP-MIB: %v", err)
	}
	if _, err := p.LoadModule("SNMP-TARGET-MIB"); err != nil {
		t.Fatalf("load SNMP-TARGET-MIB: %v", err)
	}

	tables := make(map[string]*mib.TableInfo)
	for _, module := range []string{"TCP-MIB", "SNMP-TARGET-MIB"} {
		infos, err := p.GetTableInfo(module)
		if err != nil {
			t.Fatalf("table info for %s: %v", module, err)
		}
		for _, info := range infos {
			tables[info.Name] = info
		}
	}

	conn, ok := tables["tcpConnTable"]
	if !ok {
		t.Fatal("tcpConnTable not found")
	}
	assertIndexColumns(t, conn.IndexColumns, []mib.IndexColumn{
		{Name: "tcpConnLocalAddress", OID: "1.3.6.1.2.1.6.13.1.2", Type: "IpAddress", BaseType: "OctetString", FixedLength: 4},
		{Name: "tcpConnLocalPort", OID: "1.3.6.1.2.1.6.13.1.3", Type: "Integer32", BaseType: "Integer32"},
		{Name: "tcpConnRemAddress", OID: "1.3.6.1.2.1.6.13.1.4", Type: "IpAddress", BaseType: "OctetString", FixedLength: 4},
		{Name: "tcpConnRemPort", OID: "1.3.6.1.2.1.6.13.1.5", Type: "Integer32", BaseType: "Integer32"},
	})
	if conn.Implied {
		t.Error("tcpConnTable Implied = true, want false")
	}
	if len(conn.Indexes) != 4 || conn.Indexes[0] != "tcpConnLocalAddress" {
		t.Errorf("Indexes = %v, want the four tcpConn columns", conn.Indexes)
	}

	target, ok := tables["snmpTargetAddrTable"]
	if !ok {
		t.Fatal("snmpTargetAddrTable not found")
	}
	if !target.Implied {
		t.Error("snmpTargetAddrTable Implied = false, want true")
	}
	assertIndexColumns(t, target.IndexColumns, []mib.IndexColumn{
		{Name: "snmpTargetAddrName", OID: "1.3.6.1.6.3.12.1.2.1.1", Type: "SnmpAdminString", BaseType: "OctetString", Implied: true},
	})
}

// TestGetTableInfoUnknownModule verifies the error path for a module that was
// never loaded.
func TestGetTableInfoUnknownModule(t *testing.T) {
	p := New(testsupport.MIBDir("standard"))
	if err := p.Init(); err != nil {
		t.Fatalf("init parser: %v", err)
	}
	t.Cleanup(p.Close)

	tables, err := p.GetTableInfo("NO-SUCH-MIB")
	if err == nil {
		t.Fatalf("GetTableInfo(NO-SUCH-MIB) = %v, want error", tables)
	}
	if tables != nil {
		t.Errorf("tables = %v, want nil on error", tables)
	}
}

// TestFixedOctetLength covers the SIZE-constraint reader used to size
// fixed-width string indexes.
func TestFixedOctetLength(t *testing.T) {
	tests := []struct {
		name string
		typ  *models.Type
		want int
	}{
		{
			name: "fixed size four",
			typ:  &models.Type{BaseType: types.BaseTypeOctetString, Ranges: []models.Range{{MinValue: 4, MaxValue: 4}}},
			want: 4,
		},
		{
			name: "variable size",
			typ:  &models.Type{BaseType: types.BaseTypeOctetString, Ranges: []models.Range{{MinValue: 1, MaxValue: 32}}},
			want: 0,
		},
		{
			name: "no size constraint",
			typ:  &models.Type{BaseType: types.BaseTypeOctetString},
			want: 0,
		},
		{
			name: "disjoint sizes",
			typ: &models.Type{BaseType: types.BaseTypeOctetString, Ranges: []models.Range{
				{MinValue: 4, MaxValue: 4}, {MinValue: 16, MaxValue: 16},
			}},
			want: 0,
		},
		{
			name: "integer range is not a size",
			typ:  &models.Type{BaseType: types.BaseTypeInteger32, Ranges: []models.Range{{MinValue: 7, MaxValue: 7}}},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fixedOctetLength(tt.typ); got != tt.want {
				t.Errorf("fixedOctetLength() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestFixedOctetLengthBadInput verifies the nil and negative-size cases return
// zero rather than panicking or reporting a nonsensical width.
func TestFixedOctetLengthBadInput(t *testing.T) {
	if got := fixedOctetLength(nil); got != 0 {
		t.Errorf("fixedOctetLength(nil) = %d, want 0", got)
	}

	negative := &models.Type{BaseType: types.BaseTypeOctetString, Ranges: []models.Range{{MinValue: -1, MaxValue: -1}}}
	if got := fixedOctetLength(negative); got != 0 {
		t.Errorf("fixedOctetLength(negative) = %d, want 0", got)
	}
}

// assertIndexColumns compares parsed index columns against expectations field
// by field so a failure names the offending column.
func assertIndexColumns(t *testing.T, got, want []mib.IndexColumn) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("got %d index columns %v, want %d", len(got), got, len(want))
	}
	for i, w := range want {
		g := got[i]
		if g.Name != w.Name {
			t.Errorf("column %d: Name = %q, want %q", i, g.Name, w.Name)
		}
		if g.OID != w.OID {
			t.Errorf("%s: OID = %q, want %q", w.Name, g.OID, w.OID)
		}
		if g.Type != w.Type {
			t.Errorf("%s: Type = %q, want %q", w.Name, g.Type, w.Type)
		}
		if g.BaseType != w.BaseType {
			t.Errorf("%s: BaseType = %q, want %q", w.Name, g.BaseType, w.BaseType)
		}
		if g.Implied != w.Implied {
			t.Errorf("%s: Implied = %v, want %v", w.Name, g.Implied, w.Implied)
		}
		if g.FixedLength != w.FixedLength {
			t.Errorf("%s: FixedLength = %d, want %d", w.Name, g.FixedLength, w.FixedLength)
		}
	}
}
