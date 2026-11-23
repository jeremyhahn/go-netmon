package metrics

import (
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/mib/storage"
)

// newCacheWithOIDs builds a TableIndexCache backed by a temporary store holding
// the given metadata.
func newCacheWithOIDs(t *testing.T, oids ...*mib.OIDMetadata) *TableIndexCache {
	t.Helper()

	store, err := storage.New(t.TempDir() + "/mib.db")
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	for _, oid := range oids {
		if err := store.StoreOID(oid); err != nil {
			t.Fatalf("store %s: %v", oid.Name, err)
		}
	}
	return NewTableIndexCache(store)
}

// TestExtractIndexLabelsImpliedStringIndex proves the fix for an IMPLIED
// string index. snmpTargetAddrEntry is indexed by IMPLIED snmpTargetAddrName,
// so the whole OID remainder is the name with no length prefix. The old
// name-substring heuristic classified "snmpTargetAddrName" as an integer index
// and returned the first octet of the name.
func TestExtractIndexLabelsImpliedStringIndex(t *testing.T) {
	entryOID := "1.3.6.1.6.3.12.1.2.1"

	cache := newCacheWithOIDs(t,
		&mib.OIDMetadata{
			OID:          entryOID,
			Name:         "snmpTargetAddrEntry",
			Module:       "SNMP-TARGET-MIB",
			Parent:       "1.3.6.1.6.3.12.1.2",
			IsTableEntry: true,
			Index:        []string{"snmpTargetAddrName"},
			IndexColumns: []mib.IndexColumn{{
				Name:     "snmpTargetAddrName",
				OID:      entryOID + ".1",
				Type:     "SnmpAdminString",
				BaseType: "OctetString",
				Implied:  true,
			}},
		},
		&mib.OIDMetadata{
			OID:      entryOID + ".4",
			Name:     "snmpTargetAddrTimeout",
			Module:   "SNMP-TARGET-MIB",
			Parent:   entryOID,
			IsColumn: true,
		},
	)

	// snmpTargetAddrTimeout for target "test" (0x74 0x65 0x73 0x74).
	labels := cache.ExtractIndexLabels("router1", entryOID+".4.116.101.115.116")

	if got := labels["snmptargetaddrname"]; got != "test" {
		t.Errorf("snmptargetaddrname = %q, want %q", got, "test")
	}

	// Document what the pre-metadata heuristic produced for the same OID.
	if legacy, _ := parseIndexValue([]string{"116", "101", "115", "116"},
		inferTypeFromName("snmpTargetAddrName")); legacy != "116" {
		t.Errorf("name heuristic produced %q, expected the old wrong value %q", legacy, "116")
	}
}

// TestExtractIndexLabelsCompositeIndex proves the fix for a composite index
// whose second column is a length-prefixed octet string. Getting the string
// wrong also misaligns everything after it, so this covers both the value and
// the consumed width.
func TestExtractIndexLabelsCompositeIndex(t *testing.T) {
	entryOID := "1.3.6.1.2.1.31.1.4.1"

	cache := newCacheWithOIDs(t,
		&mib.OIDMetadata{
			OID:          entryOID,
			Name:         "ifRcvAddressEntry",
			Module:       "IF-MIB",
			Parent:       "1.3.6.1.2.1.31.1.4",
			IsTableEntry: true,
			Index:        []string{"ifIndex", "ifRcvAddressAddress"},
			IndexColumns: []mib.IndexColumn{
				{Name: "ifIndex", OID: "1.3.6.1.2.1.2.2.1.1", Type: "InterfaceIndex", BaseType: "Integer32"},
				{Name: "ifRcvAddressAddress", OID: entryOID + ".1", Type: "PhysAddress", BaseType: "OctetString"},
			},
		},
		&mib.OIDMetadata{
			OID:      entryOID + ".2",
			Name:     "ifRcvAddressStatus",
			Module:   "IF-MIB",
			Parent:   entryOID,
			IsColumn: true,
		},
	)

	// ifRcvAddressStatus for interface 3, MAC 00:1a:2b:3c:4d:5e.
	labels := cache.ExtractIndexLabels("router1", entryOID+".2.3.6.0.26.43.60.77.94")

	want := map[string]string{
		"ifindex":             "3",
		"ifrcvaddressaddress": "00:1a:2b:3c:4d:5e",
	}
	for name, value := range want {
		if labels[name] != value {
			t.Errorf("%s = %q, want %q", name, labels[name], value)
		}
	}
}

// TestExtractIndexLabelsFallsBackWithoutIndexColumns covers metadata stored
// before index column types were recorded: no IndexColumns, and the index
// column itself is not in the store either, so only the name heuristic is left.
func TestExtractIndexLabelsFallsBackWithoutIndexColumns(t *testing.T) {
	entryOID := "1.3.6.1.2.1.2.2.1"

	cache := newCacheWithOIDs(t,
		&mib.OIDMetadata{
			OID:          entryOID,
			Name:         "ifEntry",
			Module:       "IF-MIB",
			Parent:       "1.3.6.1.2.1.2.2",
			IsTableEntry: true,
			Index:        []string{"ifIndex"},
		},
		&mib.OIDMetadata{
			OID:      entryOID + ".10",
			Name:     "ifInOctets",
			Module:   "IF-MIB",
			Parent:   entryOID,
			IsColumn: true,
		},
	)

	labels := cache.ExtractIndexLabels("router1", entryOID+".10.5")

	if got := labels["ifindex"]; got != "5" {
		t.Errorf("ifindex = %q, want %q", got, "5")
	}
}

// TestDecodeIndexColumn covers each base type the decoder understands.
func TestDecodeIndexColumn(t *testing.T) {
	tests := []struct {
		name         string
		parts        []string
		column       mib.IndexColumn
		wantValue    string
		wantConsumed int
	}{
		{
			name:         "integer index",
			parts:        []string{"7", "9"},
			column:       mib.IndexColumn{Name: "ifIndex", Type: "InterfaceIndex", BaseType: "Integer32"},
			wantValue:    "7",
			wantConsumed: 1,
		},
		{
			name:         "unsigned index",
			parts:        []string{"42"},
			column:       mib.IndexColumn{Name: "ipNetToMediaIfIndex", Type: "Unsigned32", BaseType: "Unsigned32"},
			wantValue:    "42",
			wantConsumed: 1,
		},
		{
			name:         "enum index",
			parts:        []string{"2", "8"},
			column:       mib.IndexColumn{Name: "ipAddressAddrType", Type: "InetAddressType", BaseType: "Enum"},
			wantValue:    "2",
			wantConsumed: 1,
		},
		{
			name:         "ipaddress renders dotted quad",
			parts:        []string{"198", "51", "100", "101", "22"},
			column:       mib.IndexColumn{Name: "tcpConnLocalAddress", Type: "IpAddress", BaseType: "OctetString", FixedLength: 4},
			wantValue:    "198.51.100.101",
			wantConsumed: 4,
		},
		{
			name:         "inetaddress keeps type and length prefix handling",
			parts:        []string{"1", "4", "203", "0", "113", "1"},
			column:       mib.IndexColumn{Name: "cbgpPeer2RemoteAddr", Type: "InetAddress", BaseType: "OctetString"},
			wantValue:    "203.0.113.1",
			wantConsumed: 6,
		},
		{
			name:         "length prefixed printable string",
			parts:        []string{"3", "97", "98", "99", "5"},
			column:       mib.IndexColumn{Name: "hrFSMountPoint", Type: "InternationalDisplayString", BaseType: "OctetString"},
			wantValue:    "abc",
			wantConsumed: 4,
		},
		{
			name:         "implied string consumes the remainder",
			parts:        []string{"97", "98", "99", "100"},
			column:       mib.IndexColumn{Name: "snmpTargetAddrName", Type: "SnmpAdminString", BaseType: "OctetString", Implied: true},
			wantValue:    "abcd",
			wantConsumed: 4,
		},
		{
			name:         "fixed length string carries no prefix",
			parts:        []string{"0", "26", "43", "60", "77", "94", "1"},
			column:       mib.IndexColumn{Name: "docsIfCmtsCmStatusMacAddress", Type: "MacAddress", BaseType: "OctetString", FixedLength: 6},
			wantValue:    "00:1a:2b:3c:4d:5e",
			wantConsumed: 6,
		},
		{
			name:         "length prefixed object identifier",
			parts:        []string{"3", "1", "3", "6", "9"},
			column:       mib.IndexColumn{Name: "sysORID", Type: "ObjectIdentifier", BaseType: "ObjectIdentifier"},
			wantValue:    "1.3.6",
			wantConsumed: 4,
		},
		{
			name:         "implied object identifier",
			parts:        []string{"1", "3", "6", "1"},
			column:       mib.IndexColumn{Name: "sysORID", Type: "ObjectIdentifier", BaseType: "ObjectIdentifier", Implied: true},
			wantValue:    "1.3.6.1",
			wantConsumed: 4,
		},
		{
			name:         "empty string index",
			parts:        []string{"0", "5"},
			column:       mib.IndexColumn{Name: "snmpTargetAddrName", Type: "SnmpAdminString", BaseType: "OctetString"},
			wantValue:    "",
			wantConsumed: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, consumed := decodeIndexColumn(tt.parts, tt.column)
			if value != tt.wantValue {
				t.Errorf("value = %q, want %q", value, tt.wantValue)
			}
			if consumed != tt.wantConsumed {
				t.Errorf("consumed = %d, want %d", consumed, tt.wantConsumed)
			}
		})
	}
}

// TestDecodeIndexColumnBadInput covers truncated and unusable input: the
// decoder must consume what is there and report an empty value rather than
// index out of range or claim to have consumed more than it saw.
func TestDecodeIndexColumnBadInput(t *testing.T) {
	tests := []struct {
		name         string
		parts        []string
		column       mib.IndexColumn
		wantValue    string
		wantConsumed int
	}{
		{
			name:         "no parts",
			parts:        nil,
			column:       mib.IndexColumn{Name: "ifIndex", BaseType: "Integer32"},
			wantValue:    "",
			wantConsumed: 0,
		},
		{
			name:         "string truncated after length prefix",
			parts:        []string{"6", "97", "98"},
			column:       mib.IndexColumn{Name: "snmpTargetAddrName", Type: "SnmpAdminString", BaseType: "OctetString"},
			wantValue:    "",
			wantConsumed: 3,
		},
		{
			name:         "fixed length longer than remaining parts",
			parts:        []string{"10", "0"},
			column:       mib.IndexColumn{Name: "tcpConnLocalAddress", Type: "IpAddress", BaseType: "OctetString", FixedLength: 4},
			wantValue:    "",
			wantConsumed: 2,
		},
		{
			name:         "non numeric length prefix",
			parts:        []string{"x", "97"},
			column:       mib.IndexColumn{Name: "snmpTargetAddrName", Type: "SnmpAdminString", BaseType: "OctetString"},
			wantValue:    "",
			wantConsumed: 2,
		},
		{
			name:         "unknown base type falls back to the name heuristic",
			parts:        []string{"12", "13"},
			column:       mib.IndexColumn{Name: "someIndex"},
			wantValue:    "12",
			wantConsumed: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, consumed := decodeIndexColumn(tt.parts, tt.column)
			if value != tt.wantValue {
				t.Errorf("value = %q, want %q", value, tt.wantValue)
			}
			if consumed != tt.wantConsumed {
				t.Errorf("consumed = %d, want %d", consumed, tt.wantConsumed)
			}
		})
	}
}

// TestRenderOctets checks the two renderings of a string index: readable text
// when every octet is printable, hex otherwise.
func TestRenderOctets(t *testing.T) {
	tests := []struct {
		name  string
		parts []string
		want  string
	}{
		{name: "printable", parts: []string{"101", "116", "104", "48"}, want: "eth0"},
		{name: "binary", parts: []string{"0", "26", "255"}, want: "00:1a:ff"},
		{name: "empty", parts: nil, want: ""},
		// The same text-or-hex rule mib.FormatOctets applies to values, so a
		// padded index name reads as its name and an all-NUL one still reads
		// as something rather than vanishing.
		{name: "NUL padded text", parts: []string{"101", "116", "104", "48", "0", "0"}, want: "eth0"},
		{name: "all NUL", parts: []string{"0", "0"}, want: "00:00"},
		{name: "embedded NUL is binary", parts: []string{"97", "0", "98"}, want: "61:00:62"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := renderOctets(tt.parts); got != tt.want {
				t.Errorf("renderOctets(%v) = %q, want %q", tt.parts, got, tt.want)
			}
		})
	}
}

// TestRenderOctetsBadInput verifies that sub-identifiers which are not octets
// are left as they came off the wire instead of being silently truncated.
func TestRenderOctetsBadInput(t *testing.T) {
	if got := renderOctets([]string{"1", "300"}); got != "1.300" {
		t.Errorf("out of range octet: got %q, want %q", got, "1.300")
	}
	if got := renderOctets([]string{"1", "abc"}); got != "1.abc" {
		t.Errorf("non numeric octet: got %q, want %q", got, "1.abc")
	}
}
