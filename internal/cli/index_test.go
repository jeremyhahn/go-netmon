package cli

import (
	"strings"
	"testing"

	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// The tests here cover how a series' row is named. The exporter decodes a
// table's index into labels and attaches enrichment labels beside them —
// ifname, ifdescr, ifalias, ifadminstatus, ifoperstatus, iftype — and the CLI
// has to render the first and ignore the second without keeping a list of the
// second. It does that from the MIB's INDEX clause, which is what these tests
// pin.

const (
	ifEntryOID     = ".1.3.6.1.2.1.2.2.1"
	ifInOctetsOID  = ".1.3.6.1.2.1.2.2.1.10"
	bgpEntryOID    = ".1.3.6.1.4.1.9.9.187.1.2.8.1"
	bgpPrefixesOID = ".1.3.6.1.4.1.9.9.187.1.2.8.1.1"
	ipAddrEntryOID = ".1.3.6.1.2.1.4.34.1"
	ipAddrIfOID    = ".1.3.6.1.2.1.4.34.1.3"
	orphanOID      = ".1.3.6.1.4.1.9.9.109.1.1.1.1.8"
)

// indexedObjects are the definitions the index tests resolve against: an
// interface table indexed by one integer, a BGP prefix table indexed by four
// columns including an InetAddress, an IP address table indexed by an address
// type and an InetAddress, and one column whose conceptual row no loaded MIB
// describes.
func indexedObjects() []client.MIBObject {
	return []client.MIBObject{
		{OID: ifEntryOID, Name: "ifEntry", Index: []string{"ifIndex"}},
		{OID: ifInOctetsOID, Name: "ifInOctets", Type: "Counter32", IsColumn: true},

		{OID: bgpEntryOID, Name: "cbgpPeer2AddrFamilyPrefixEntry", Index: []string{
			"cbgpPeer2Type", "cbgpPeer2RemoteAddr",
			"cbgpPeer2AddrFamilyAfi", "cbgpPeer2AddrFamilySafi",
		}},
		{OID: bgpPrefixesOID, Name: "cbgpPeer2AcceptedPrefixes", Type: "Gauge32", IsColumn: true},

		{OID: ipAddrEntryOID, Name: "ipAddressEntry", Index: []string{
			"ipAddressAddrType", "ipAddressAddr",
		}},
		{OID: ipAddrIfOID, Name: "ipAddressIfIndex", Type: "InterfaceIndex", IsColumn: true},

		// A column whose row node was never imported: nothing says what its
		// index columns are, so it has to fall back to the raw sub-identifiers.
		{OID: orphanOID, Name: "cpmCPUTotal5minRev", Type: "Gauge32", IsColumn: true},
	}
}

// TestDescribeRendersDecodedIndexes covers each index shape the CLI has to
// render, and the fallback for the one it cannot.
func TestDescribeRendersDecodedIndexes(t *testing.T) {
	index := newOIDIndex(indexedObjects())

	tests := []struct {
		name   string
		labels map[string]string
		want   string
	}{
		{
			name: "a single-column index renders as the bare value",
			labels: series("snmp_ifInOctets", ifInOctetsOID+".10", map[string]string{
				"ifindex": "10",
				// Enrichment, all of it. None of these is in ifEntry's INDEX
				// clause, so none of them may reach the rendered name.
				"ifname": "GigabitEthernet0/0", "ifdescr": "GigabitEthernet0/0",
				"ifalias": "uplink", "ifadminstatus": "up", "ifoperstatus": "up",
				"iftype": "ethernetCsmacd",
			}),
			want: "ifInOctets[10]",
		},
		{
			name: "a composite index names each column",
			labels: series("snmp_cbgpPeer2AcceptedPrefixes",
				bgpPrefixesOID+".1.4.203.0.113.218.1.1", map[string]string{
					"cbgppeer2type": "1", "cbgppeer2remoteaddr": "203.0.113.218",
					"cbgppeer2addrfamilyafi": "1", "cbgppeer2addrfamilysafi": "1",
				}),
			want: "cbgpPeer2AcceptedPrefixes[cbgpPeer2Type=1,cbgpPeer2RemoteAddr=203.0.113.218," +
				"cbgpPeer2AddrFamilyAfi=1,cbgpPeer2AddrFamilySafi=1]",
		},
		{
			name: "an InetAddress index renders as the address, not its encoding",
			labels: series("snmp_ipAddressIfIndex", ipAddrIfOID+".1.4.203.0.113.218",
				map[string]string{
					"ipaddressaddrtype": "1", "ipaddressaddr": "203.0.113.218",
				}),
			want: "ipAddressIfIndex[ipAddressAddrType=1,ipAddressAddr=203.0.113.218]",
		},
		{
			name: "a row no MIB describes keeps the raw sub-identifiers",
			labels: series("snmp_cpmCPUTotal5minRev", orphanOID+".7.3623.688",
				map[string]string{"cpmcputotalindex": "7"}),
			want: "cpmCPUTotal5minRev[7.3623.688]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, meta, ok := index.Describe(tt.labels)
			if !ok {
				t.Fatalf("Describe did not resolve the series: %q", got)
			}
			if got != tt.want {
				t.Errorf("Describe = %q, want %q", got, tt.want)
			}
			if meta == nil {
				t.Error("a resolved series must carry metadata for formatting")
			}
		})
	}
}

// TestDescribeIgnoresEnrichmentLabels is the point of deriving index columns
// from the MIB: a series whose only labels are enrichment must fall back to the
// raw index rather than inventing a row identity out of ifName.
func TestDescribeIgnoresEnrichmentLabels(t *testing.T) {
	index := newOIDIndex(indexedObjects())

	labels := series("snmp_ifInOctets", ifInOctetsOID+".10", map[string]string{
		"ifname": "GigabitEthernet0/0", "ifdescr": "uplink to core",
		"ifalias": "uplink", "ifoperstatus": "up",
	})

	got, _, ok := index.Describe(labels)
	if !ok {
		t.Fatalf("Describe did not resolve the series: %q", got)
	}
	if got != "ifInOctets[10]" {
		t.Errorf("Describe = %q, want the raw index when no INDEX label is present", got)
	}
	for _, enrichment := range []string{"GigabitEthernet0/0", "uplink", "up"} {
		if strings.Contains(got, enrichment) {
			t.Errorf("enrichment label value %q reached the rendered name %q", enrichment, got)
		}
	}
}

// TestDescribeFallsBackOnAPartialIndex checks that half a row identity is not
// offered as a whole one. The raw sub-identifiers name the row exactly; two of
// its four index columns do not.
func TestDescribeFallsBackOnAPartialIndex(t *testing.T) {
	index := newOIDIndex(indexedObjects())

	labels := series("snmp_cbgpPeer2AcceptedPrefixes",
		bgpPrefixesOID+".1.4.203.0.113.218.1.1", map[string]string{
			"cbgppeer2type": "1", "cbgppeer2remoteaddr": "203.0.113.218",
			// The last two index columns were not decoded.
		})

	got, _, ok := index.Describe(labels)
	if !ok {
		t.Fatalf("Describe did not resolve the series: %q", got)
	}
	if got != "cbgpPeer2AcceptedPrefixes[1.4.203.0.113.218.1.1]" {
		t.Errorf("Describe = %q, want the raw index when a column is missing", got)
	}
}

// TestRowIndexColumns checks the lookup that decides which labels are the row,
// including the cases where there is no row to look up.
func TestRowIndexColumns(t *testing.T) {
	index := newOIDIndex(indexedObjects())

	column := client.MIBObject{OID: ifInOctetsOID, IsColumn: true}
	if got := index.rowIndexColumns(column); len(got) != 1 || got[0] != "ifIndex" {
		t.Errorf("rowIndexColumns = %v, want [ifIndex]", got)
	}

	// A scalar has no row, so asking about one must not reach into the table
	// index and find a neighbour's.
	scalar := client.MIBObject{OID: ".1.3.6.1.2.1.1.3", IsScalar: true}
	if got := index.rowIndexColumns(scalar); got != nil {
		t.Errorf("rowIndexColumns on a scalar = %v, want nil", got)
	}

	orphan := client.MIBObject{OID: orphanOID, IsColumn: true}
	if got := index.rowIndexColumns(orphan); got != nil {
		t.Errorf("rowIndexColumns on an undescribed row = %v, want nil", got)
	}

	// A degenerate OID must not panic or match anything.
	if got := index.rowIndexColumns(client.MIBObject{OID: "1", IsColumn: true}); got != nil {
		t.Errorf("rowIndexColumns on a one-component OID = %v, want nil", got)
	}
}

// TestParentOID covers the arithmetic that turns a column into its row.
func TestParentOID(t *testing.T) {
	tests := map[string]string{
		"1.3.6.1.2.1.2.2.1.10": "1.3.6.1.2.1.2.2.1",
		"1.3":                  "1",
		"1":                    "",
		"":                     "",
		".1":                   "",
	}
	for oid, want := range tests {
		if got := parentOID(oid); got != want {
			t.Errorf("parentOID(%q) = %q, want %q", oid, got, want)
		}
	}
}

// TestDecodedIndex covers the renderer directly, including every way it can
// decline to render.
func TestDecodedIndex(t *testing.T) {
	labels := map[string]string{
		"ifindex": "10", "cbgppeer2type": "1", "cbgppeer2remoteaddr": "203.0.113.218",
	}

	if got := decodedIndex([]string{"ifIndex"}, labels); got != "10" {
		t.Errorf("single column = %q, want 10", got)
	}
	want := "cbgpPeer2Type=1,cbgpPeer2RemoteAddr=203.0.113.218"
	if got := decodedIndex([]string{"cbgpPeer2Type", "cbgpPeer2RemoteAddr"}, labels); got != want {
		t.Errorf("composite = %q, want %q", got, want)
	}

	if got := decodedIndex(nil, labels); got != "" {
		t.Errorf("no columns = %q, want an empty rendering", got)
	}
	if got := decodedIndex([]string{"ifIndex", "ifMissing"}, labels); got != "" {
		t.Errorf("a missing column gave %q, want an empty rendering", got)
	}
	if got := decodedIndex([]string{"ifIndex"}, map[string]string{"ifindex": ""}); got != "" {
		t.Errorf("an empty label value gave %q, want an empty rendering", got)
	}
	if got := decodedIndex([]string{"ifIndex"}, nil); got != "" {
		t.Errorf("no labels at all gave %q, want an empty rendering", got)
	}
}

// TestNewOIDIndexKeepsTheFirstRowDefinition checks that two modules defining
// the same row do not make the rendering depend on map iteration.
func TestNewOIDIndexKeepsTheFirstRowDefinition(t *testing.T) {
	index := newOIDIndex([]client.MIBObject{
		{OID: ifEntryOID, Name: "ifEntry", Index: []string{"ifIndex"}},
		{OID: ifEntryOID, Name: "ifEntryClone", Index: []string{"somethingElse"}},
	})

	got := index.indexColumns[strings.TrimPrefix(ifEntryOID, ".")]
	if len(got) != 1 || got[0] != "ifIndex" {
		t.Errorf("index columns = %v, want the first definition [ifIndex]", got)
	}
}
