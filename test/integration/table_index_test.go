package integration

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang/snappy"
	"github.com/gosnmp/gosnmp"
	"github.com/prometheus/prometheus/prompb"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/metrics"
	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/mib/loader"
	"github.com/jeremyhahn/go-netmon/internal/mib/storage"
	"github.com/jeremyhahn/go-netmon/internal/snmp"
	"github.com/jeremyhahn/go-netmon/internal/testsupport"
)

// pipelineModules are the MIB modules every test in this file needs: IF-MIB for
// a simple single-integer row index, CISCO-BGP4-MIB for a four-column composite
// index carrying a variable-length InetAddress, and SNMPv2-MIB for the system
// scalars.
var pipelineModules = []string{"IF-MIB", "CISCO-BGP4-MIB", "SNMPv2-MIB"}

// loadPipelineMIBs parses the repository's MIB files into a Pebble store at
// storagePath and returns the store, which is closed when the test ends.
//
// Paths are resolved from the repository root so this runs both on a
// developer's machine and inside the test container, which mounts the repo at a
// different location.
//
// mibs/cisco is required because CISCO-BGP4-MIB imports ciscoMgmt FROM
// CISCO-SMI, which lives there. Without it the parser cannot resolve the
// absolute OID of any CISCO-BGP4-MIB object — every OID in the module comes
// back empty — so the loader silently produces unusable table metadata instead
// of failing loudly.
func loadPipelineMIBs(t *testing.T, storagePath string) *storage.Storage {
	t.Helper()

	cfg := &config.MIBConfig{
		StoragePath: storagePath,
		Paths:       testsupport.MIBDirs("standard", "asr1001", "cisco"),
		AutoLoad:    pipelineModules,
	}

	mibLoader, err := loader.New(cfg)
	if err != nil {
		t.Fatalf("Failed to create MIB loader: %v", err)
	}
	t.Cleanup(func() { _ = mibLoader.Close() })

	if err := mibLoader.Load(); err != nil {
		t.Fatalf("Failed to load MIBs: %v", err)
	}

	mibStorage, ok := mibLoader.GetStorage().(*storage.Storage)
	if !ok {
		t.Fatal("Failed to get MIB storage")
	}

	return mibStorage
}

// TestTableIndexExtraction tests that the generic table index extraction works for real MIBs
// This test runs in Docker and uses actual MIB files on disk
func TestTableIndexExtraction(t *testing.T) {
	mibStorage := loadPipelineMIBs(t, filepath.Join(t.TempDir(), "mib-storage"))

	// Create table index cache and OID learner
	indexCache := metrics.NewTableIndexCache(mibStorage)
	oidLearner := metrics.NewOIDLearner(mibStorage)

	// Test IF-MIB simple numeric index extraction
	t.Run("IF-MIB_Simple_Index", func(t *testing.T) {
		// Interface counter OID with index 1
		oid := ".1.3.6.1.2.1.2.2.1.10.1" // ifInOctets.1
		labels := indexCache.ExtractIndexLabels("test-device", oid)

		if len(labels) == 0 {
			t.Error("Expected labels to be extracted")
		}

		if labels["ifindex"] != "1" {
			t.Errorf("Expected ifindex=1, got %s", labels["ifindex"])
		}

		t.Logf("✓ IF-MIB index extraction: %v", labels)
	})

	// Test BGP composite index extraction with FIXED InetAddress parsing
	t.Run("BGP_Composite_Index", func(t *testing.T) {
		// BGP peer prefix OID with composite index:
		// Format: .1.3.6.1.4.1.9.9.187.1.2.8.1.1.TYPE.LENGTH.IP_OCTETS.AFI.SAFI
		// type=1 (IPv4), length=4, addr=203.0.113.218 (4 octets), afi=1, safi=1
		oid := ".1.3.6.1.4.1.9.9.187.1.2.8.1.1.1.4.203.0.113.218.1.1"
		labels := indexCache.ExtractIndexLabels("test-device", oid)

		if len(labels) == 0 {
			t.Error("Expected labels to be extracted")
		}

		if labels["cbgppeer2type"] == "" {
			t.Error("Expected cbgppeer2type label")
		}

		// CRITICAL: Verify IP address is correctly parsed
		if labels["cbgppeer2remoteaddr"] != "203.0.113.218" {
			t.Errorf("Expected cbgppeer2remoteaddr=203.0.113.218, got %s", labels["cbgppeer2remoteaddr"])
		}

		if labels["cbgppeer2addrfamilyafi"] == "" {
			t.Error("Expected cbgppeer2addrfamilyafi label")
		}

		if labels["cbgppeer2addrfamilysafi"] == "" {
			t.Error("Expected cbgppeer2addrfamilysafi label")
		}

		t.Logf("✓ BGP composite index extraction: %v", labels)
	})

	// Test that samples get created with labels
	t.Run("Sample_Creation_With_Labels", func(t *testing.T) {
		// Create mock SNMP results
		results := []*snmp.Result{
			{
				OID:       ".1.3.6.1.2.1.2.2.1.10.5", // ifInOctets.5
				Value:     uint64(12345678),
				Timestamp: time.Now(),
			},
			{
				OID:       ".1.3.6.1.4.1.9.9.187.1.2.8.1.1.1.4.203.0.113.218.1.1",
				Value:     uint64(100),
				Timestamp: time.Now(),
			},
		}

		samples := metrics.ConvertSNMPResultsWithTableIndexes(
			"test-device",
			results,
			map[string]string{"environment": "test"},
			indexCache,
			oidLearner,
		)

		if len(samples) != 2 {
			t.Fatalf("Expected 2 samples, got %d", len(samples))
		}

		// Check interface sample has ifindex label
		var ifSample *metrics.Sample
		for _, s := range samples {
			if s.Labels["oid"] == ".1.3.6.1.2.1.2.2.1.10.5" {
				ifSample = s
				break
			}
		}

		if ifSample == nil {
			t.Fatal("Interface sample not found")
		}

		if ifSample.Labels["ifindex"] != "5" {
			t.Errorf("Expected ifindex=5, got %s", ifSample.Labels["ifindex"])
		}

		// Check BGP sample has composite labels with CORRECT IP
		var bgpSample *metrics.Sample
		for _, s := range samples {
			if s.Labels["cbgppeer2type"] != "" {
				bgpSample = s
				break
			}
		}

		if bgpSample == nil {
			t.Fatal("BGP sample not found")
		}

		if bgpSample.Labels["cbgppeer2addrfamilyafi"] == "" {
			t.Error("BGP sample missing cbgppeer2addrfamilyafi label")
		}

		// CRITICAL: Verify IP address in sample
		if bgpSample.Labels["cbgppeer2remoteaddr"] != "203.0.113.218" {
			t.Errorf("BGP sample has incorrect IP: expected 203.0.113.218, got %s",
				bgpSample.Labels["cbgppeer2remoteaddr"])
		}

		t.Logf("✓ Interface sample labels: %v", ifSample.Labels)
		t.Logf("✓ BGP sample labels: %v", bgpSample.Labels)
	})

	// Test all loaded tables have index information.
	//
	// "Usable" index information means three things, because anything less
	// leaves a row of the table either unlabeled or mislabeled in Prometheus:
	//
	//  1. the row's INDEX clause survived the parse as a non-empty list of
	//     columns, since a table with no index columns cannot have its rows
	//     told apart at all;
	//  2. every one of those columns carries the name the clause lists, the
	//     OID of the object it names and that object's resolved type, since
	//     the decoder splits the OID suffix by each column's type and consumes
	//     the columns left to right -- one column of the wrong width misaligns
	//     every column after it;
	//  3. the production index cache actually turns an instance OID of the
	//     table into one label per index column, which is the only thing that
	//     proves the metadata reached the component that uses it rather than
	//     merely being stored.
	//
	// Every table of every loaded module is checked rather than a sample, and
	// the set is asserted exactly: a table that stops being stored fails, and
	// so does a table that appears without an expectation written for it.
	t.Run("All_Tables_Have_Index_Info", func(t *testing.T) {
		stored := make(map[string]*mib.TableInfo)
		for _, module := range pipelineModules {
			tables, err := mibStorage.ListTables(module)
			if err != nil {
				t.Fatalf("list tables of %s: %v", module, err)
			}
			for _, table := range tables {
				stored[module+"::"+table.Name] = table
			}
		}

		described := make(map[string]struct{}, len(pipelineIndexedTables))
		for _, want := range pipelineIndexedTables {
			described[want.module+"::"+want.name] = struct{}{}
		}
		for key := range stored {
			if _, ok := described[key]; !ok {
				t.Errorf("table %s was stored but no expectation is written for it; every table of a loaded module must have its index metadata asserted", key)
			}
		}

		for _, want := range pipelineIndexedTables {
			table, ok := stored[want.module+"::"+want.name]
			if !ok {
				t.Errorf("table %s::%s was not stored by the loader", want.module, want.name)
				continue
			}

			t.Run(want.name, func(t *testing.T) {
				assertTableIsIndexed(t, indexCache, table, want)
			})
		}
	})

	// Test OID learner with BGP OIDs
	t.Run("OID_Learner_Discovers_BGP_Table", func(t *testing.T) {
		// Create a fresh learner
		learner := metrics.NewOIDLearner(mibStorage)

		// Simulate BGP OIDs coming from SNMP responses
		bgpOIDs := []string{
			".1.3.6.1.4.1.9.9.187.1.2.8.1.1.1.4.203.0.113.218.1.1",
			".1.3.6.1.4.1.9.9.187.1.2.8.1.2.1.4.203.0.113.218.1.1",
			".1.3.6.1.4.1.9.9.187.1.2.8.1.3.1.4.203.0.113.218.1.1",
			".1.3.6.1.4.1.9.9.187.1.2.8.1.1.1.4.203.0.113.202.1.1",
			".1.3.6.1.4.1.9.9.187.1.2.8.1.2.1.4.203.0.113.202.1.1",
		}

		learner.LearnFromResponses(bgpOIDs)

		// The five OIDs are three columns of one table, so exactly one table
		// is there to be learned. More would mean the learner split one
		// conceptual row across several tables, which is how a row's columns
		// end up labeled inconsistently.
		expectedEntry := "1.3.6.1.4.1.9.9.187.1.2.8.1"
		learned := learner.GetAllLearnedTables()
		if len(learned) != 1 {
			t.Errorf("OID learner discovered %d tables, want 1 (%s)", len(learned), expectedEntry)
		}

		table := learner.GetLearnedTable(expectedEntry)
		if table == nil {
			t.Fatalf("Expected to learn BGP table entry %s", expectedEntry)
		}

		wantColumns := []string{
			expectedEntry + ".1",
			expectedEntry + ".2",
			expectedEntry + ".3",
		}
		gotColumns := slices.Clone(table.ColumnOIDs)
		slices.Sort(gotColumns)
		if !slices.Equal(gotColumns, wantColumns) {
			t.Errorf("learned columns %v, want %v; the two OIDs that differ only in their row index are one column, not two",
				gotColumns, wantColumns)
		}

		// Test enrichment with MIB metadata
		learner.MergeWithMIBMetadata(indexCache)

		// Enrichment has to replace what the learner knows -- that a row index
		// exists -- with what the MIB says it is made of. Without the types,
		// the variable-length address in the middle of this index is decoded
		// by guessing from the column name.
		enriched := learner.GetLearnedTable(expectedEntry)
		if enriched == nil {
			t.Fatalf("learned table %s disappeared when MIB metadata was merged", expectedEntry)
		}
		wantIndexNames := []string{"cbgpPeer2Type", "cbgpPeer2RemoteAddr", "cbgpPeer2AddrFamilyAfi", "cbgpPeer2AddrFamilySafi"}
		if !slices.Equal(enriched.IndexNames, wantIndexNames) {
			t.Errorf("enriched index names are %v, want %v", enriched.IndexNames, wantIndexNames)
		}
		wantIndexTypes := []string{"InetAddressType", "InetAddress", "InetAddressType", "CbgpSafi"}
		if !slices.Equal(enriched.IndexTypes, wantIndexTypes) {
			t.Errorf("enriched index types are %v, want %v", enriched.IndexTypes, wantIndexTypes)
		}

		// CISCO-BGP4-MIB already describes this table, so exporting the learned
		// copy must add nothing: a learned table, whose index types are
		// inferred from column names, may never displace the MIB's own.
		if exported := learner.ExportToIndexCache(indexCache); exported != 0 {
			t.Errorf("exporting learned tables added %d of them to the index cache, want 0; %s is described by CISCO-BGP4-MIB",
				exported, expectedEntry)
		}

		// And the cache still decodes the row the MIB's way afterwards.
		testOID := ".1.3.6.1.4.1.9.9.187.1.2.8.1.1.1.4.203.0.113.218.1.1"
		wantLabels := map[string]string{
			"cbgppeer2type":           "1",
			"cbgppeer2remoteaddr":     "203.0.113.218",
			"cbgppeer2addrfamilyafi":  "1",
			"cbgppeer2addrfamilysafi": "1",
		}
		if labels := indexCache.ExtractIndexLabels("learner-audit-device", testOID); !maps.Equal(labels, wantLabels) {
			t.Errorf("after exporting learned tables, %s decoded to %v, want %v", testOID, labels, wantLabels)
		}
	})
}

// indexedTable is the index metadata one table of a loaded module has to
// carry, together with one row of that table and the labels that row must
// decode into.
//
// The expectations are written out from the module's own INDEX clause and from
// the SMI index encoding rules (RFC 2578 section 7.7) rather than read back
// from the parser, so a parser that resolves an index column to the wrong type
// or the wrong width fails here instead of agreeing with itself.
type indexedTable struct {
	module string
	name   string

	// oid and entryOID are stored without a leading dot, which is the form the
	// MIB store keeps and not the form the SNMP fixtures in this file use.
	oid      string
	entryOID string

	// columns is the INDEX clause with every column resolved: the name the
	// clause lists, the OID of the object it names, and the type and SMI base
	// type that object is declared with.
	columns []mib.IndexColumn

	// rowSuffix is the sub-identifiers encoding one row's index, appended to a
	// column OID to form an instance OID of that row.
	rowSuffix string

	// labels is exactly what rowSuffix has to decode into: one label per index
	// column, named after the lowercased column name.
	labels map[string]string
}

// The index columns the tables below share. The same object indexes several
// tables -- ifIndex indexes four of IF-MIB's five, bgpPeerRemoteAddr indexes
// four CISCO-BGP4-MIB tables from a different module entirely -- so each is
// spelled out once.
//
// Two resolved types differ from the word in the MIB's SYNTAX clause because
// libsmi reduces anonymous subtypes to the base type it derives: sysORIndex is
// declared INTEGER (1..2147483647) and resolves to Unsigned32 because its range
// excludes negatives, and the inline INTEGER { ... } enumerations of the
// capability codes resolve to Enumeration. Both decode as a single
// sub-identifier either way.
var (
	ifIndexColumn             = mib.IndexColumn{Name: "ifIndex", OID: "1.3.6.1.2.1.2.2.1.1", Type: "InterfaceIndex", BaseType: "Integer32"}
	ifStackHigherLayerColumn  = mib.IndexColumn{Name: "ifStackHigherLayer", OID: "1.3.6.1.2.1.31.1.2.1.1", Type: "InterfaceIndexOrZero", BaseType: "Integer32"}
	ifStackLowerLayerColumn   = mib.IndexColumn{Name: "ifStackLowerLayer", OID: "1.3.6.1.2.1.31.1.2.1.2", Type: "InterfaceIndexOrZero", BaseType: "Integer32"}
	ifRcvAddressAddressColumn = mib.IndexColumn{Name: "ifRcvAddressAddress", OID: "1.3.6.1.2.1.31.1.4.1.1", Type: "PhysAddress", BaseType: "OctetString"}
	sysORIndexColumn          = mib.IndexColumn{Name: "sysORIndex", OID: "1.3.6.1.2.1.1.9.1.1", Type: "Unsigned32", BaseType: "Unsigned32"}
	bgpPeerRemoteAddrColumn   = mib.IndexColumn{Name: "bgpPeerRemoteAddr", OID: "1.3.6.1.2.1.15.3.1.7", Type: "IpAddress", BaseType: "OctetString", FixedLength: 4}
	cbgpPeerCapCodeColumn     = mib.IndexColumn{Name: "cbgpPeerCapCode", OID: "1.3.6.1.4.1.9.9.187.1.2.2.1.1", Type: "Enumeration", BaseType: "Enum"}
	cbgpPeerCapIndexColumn    = mib.IndexColumn{Name: "cbgpPeerCapIndex", OID: "1.3.6.1.4.1.9.9.187.1.2.2.1.2", Type: "Unsigned32", BaseType: "Unsigned32"}
	cbgpPeerAfiColumn         = mib.IndexColumn{Name: "cbgpPeerAddrFamilyAfi", OID: "1.3.6.1.4.1.9.9.187.1.2.3.1.1", Type: "InetAddressType", BaseType: "Enum"}
	cbgpPeerSafiColumn        = mib.IndexColumn{Name: "cbgpPeerAddrFamilySafi", OID: "1.3.6.1.4.1.9.9.187.1.2.3.1.2", Type: "CbgpSafi", BaseType: "Enum"}
	cbgpPeer2TypeColumn       = mib.IndexColumn{Name: "cbgpPeer2Type", OID: "1.3.6.1.4.1.9.9.187.1.2.5.1.1", Type: "InetAddressType", BaseType: "Enum"}
	cbgpPeer2RemoteAddrColumn = mib.IndexColumn{Name: "cbgpPeer2RemoteAddr", OID: "1.3.6.1.4.1.9.9.187.1.2.5.1.2", Type: "InetAddress", BaseType: "OctetString"}
	cbgpPeer2CapCodeColumn    = mib.IndexColumn{Name: "cbgpPeer2CapCode", OID: "1.3.6.1.4.1.9.9.187.1.2.6.1.1", Type: "Enumeration", BaseType: "Enum"}
	cbgpPeer2CapIndexColumn   = mib.IndexColumn{Name: "cbgpPeer2CapIndex", OID: "1.3.6.1.4.1.9.9.187.1.2.6.1.2", Type: "Unsigned32", BaseType: "Unsigned32"}
	cbgpPeer2AfiColumn        = mib.IndexColumn{Name: "cbgpPeer2AddrFamilyAfi", OID: "1.3.6.1.4.1.9.9.187.1.2.7.1.1", Type: "InetAddressType", BaseType: "Enum"}
	cbgpPeer2SafiColumn       = mib.IndexColumn{Name: "cbgpPeer2AddrFamilySafi", OID: "1.3.6.1.4.1.9.9.187.1.2.7.1.2", Type: "CbgpSafi", BaseType: "Enum"}
	cbgpPeer3VrfIDColumn      = mib.IndexColumn{Name: "cbgpPeer3VrfId", OID: "1.3.6.1.4.1.9.9.187.1.2.9.1.1", Type: "Unsigned32", BaseType: "Unsigned32"}
	cbgpPeer3TypeColumn       = mib.IndexColumn{Name: "cbgpPeer3Type", OID: "1.3.6.1.4.1.9.9.187.1.2.9.1.2", Type: "InetAddressType", BaseType: "Enum"}
	cbgpPeer3RemoteAddrColumn = mib.IndexColumn{Name: "cbgpPeer3RemoteAddr", OID: "1.3.6.1.4.1.9.9.187.1.2.9.1.3", Type: "InetAddress", BaseType: "OctetString"}
)

// The row index encodings reused below. An InetAddress preceded by its own
// address-type column is encoded as a length octet and the address octets
// (RFC 4001), while an IpAddress is four octets with no length because its
// width is fixed.
const (
	peerInetAddrSuffix = "4.203.0.113.218" // InetAddress 203.0.113.218
	peerIPv4Suffix     = "203.0.113.218"   // IpAddress 203.0.113.218
	peerAddrValue      = "203.0.113.218"
)

// pipelineIndexedTables describes every table the three modules of
// pipelineModules define: five in IF-MIB, ten in CISCO-BGP4-MIB and one in
// SNMPv2-MIB. All sixteen have complete index metadata, so there is no table
// here that the MIB set leaves without it -- including the three rows that
// carry no INDEX clause of their own (ifXEntry and ifTestEntry augment
// ifEntry, cbgpPeerEntry augments BGP4-MIB's bgpPeerEntry) and so have to
// inherit the augmented row's index, and the four rows indexed by
// bgpPeerRemoteAddr, an object of BGP4-MIB rather than of CISCO-BGP4-MIB.
// Those seven are the cases where index metadata would go missing first, which
// is why they are asserted by name rather than covered by a count.
var pipelineIndexedTables = []indexedTable{
	{
		module:    "IF-MIB",
		name:      "ifTable",
		oid:       "1.3.6.1.2.1.2.2",
		entryOID:  "1.3.6.1.2.1.2.2.1",
		columns:   []mib.IndexColumn{ifIndexColumn},
		rowSuffix: "7",
		labels:    map[string]string{"ifindex": "7"},
	},
	{
		// AUGMENTS ifEntry: the extended interface row is indexed by the
		// interface it extends.
		module:    "IF-MIB",
		name:      "ifXTable",
		oid:       "1.3.6.1.2.1.31.1.1",
		entryOID:  "1.3.6.1.2.1.31.1.1.1",
		columns:   []mib.IndexColumn{ifIndexColumn},
		rowSuffix: "7",
		labels:    map[string]string{"ifindex": "7"},
	},
	{
		module:    "IF-MIB",
		name:      "ifStackTable",
		oid:       "1.3.6.1.2.1.31.1.2",
		entryOID:  "1.3.6.1.2.1.31.1.2.1",
		columns:   []mib.IndexColumn{ifStackHigherLayerColumn, ifStackLowerLayerColumn},
		rowSuffix: "3.9",
		labels:    map[string]string{"ifstackhigherlayer": "3", "ifstacklowerlayer": "9"},
	},
	{
		// AUGMENTS ifEntry.
		module:    "IF-MIB",
		name:      "ifTestTable",
		oid:       "1.3.6.1.2.1.31.1.3",
		entryOID:  "1.3.6.1.2.1.31.1.3.1",
		columns:   []mib.IndexColumn{ifIndexColumn},
		rowSuffix: "7",
		labels:    map[string]string{"ifindex": "7"},
	},
	{
		// The second index column is a variable-length PhysAddress, so the row
		// index carries its length (6) ahead of the address octets, and the
		// decoder has to render those octets as an address rather than as six
		// separate sub-identifiers.
		module:    "IF-MIB",
		name:      "ifRcvAddressTable",
		oid:       "1.3.6.1.2.1.31.1.4",
		entryOID:  "1.3.6.1.2.1.31.1.4.1",
		columns:   []mib.IndexColumn{ifIndexColumn, ifRcvAddressAddressColumn},
		rowSuffix: "7.6.0.27.33.60.77.94",
		labels:    map[string]string{"ifindex": "7", "ifrcvaddressaddress": "00:1b:21:3c:4d:5e"},
	},
	{
		module:    "SNMPv2-MIB",
		name:      "sysORTable",
		oid:       "1.3.6.1.2.1.1.9",
		entryOID:  "1.3.6.1.2.1.1.9.1",
		columns:   []mib.IndexColumn{sysORIndexColumn},
		rowSuffix: "2",
		labels:    map[string]string{"sysorindex": "2"},
	},
	{
		// Six index columns, two of them variable-length strings. The prefix is
		// a CbgpNetworkAddress, which the MIB gives no DISPLAY-HINT, so its
		// octets render as hex rather than as dotted quad.
		module:   "CISCO-BGP4-MIB",
		name:     "cbgpRouteTable",
		oid:      "1.3.6.1.4.1.9.9.187.1.1.1",
		entryOID: "1.3.6.1.4.1.9.9.187.1.1.1.1",
		columns: []mib.IndexColumn{
			{Name: "cbgpRouteAfi", OID: "1.3.6.1.4.1.9.9.187.1.1.1.1.1", Type: "InetAddressType", BaseType: "Enum"},
			{Name: "cbgpRouteSafi", OID: "1.3.6.1.4.1.9.9.187.1.1.1.1.2", Type: "CbgpSafi", BaseType: "Enum"},
			{Name: "cbgpRoutePeerType", OID: "1.3.6.1.4.1.9.9.187.1.1.1.1.3", Type: "InetAddressType", BaseType: "Enum"},
			{Name: "cbgpRoutePeer", OID: "1.3.6.1.4.1.9.9.187.1.1.1.1.4", Type: "InetAddress", BaseType: "OctetString"},
			{Name: "cbgpRouteAddrPrefix", OID: "1.3.6.1.4.1.9.9.187.1.1.1.1.5", Type: "CbgpNetworkAddress", BaseType: "OctetString"},
			{Name: "cbgpRouteAddrPrefixLen", OID: "1.3.6.1.4.1.9.9.187.1.1.1.1.6", Type: "Unsigned32", BaseType: "Unsigned32"},
		},
		rowSuffix: "1.1.1." + peerInetAddrSuffix + ".4.203.0.113.0.24",
		labels: map[string]string{
			"cbgprouteafi":           "1",
			"cbgproutesafi":          "1",
			"cbgproutepeertype":      "1",
			"cbgproutepeer":          peerAddrValue,
			"cbgprouteaddrprefix":    "c0:a8:01:00",
			"cbgprouteaddrprefixlen": "24",
		},
	},
	{
		// AUGMENTS BGP4-MIB's bgpPeerEntry, so both the index column and the
		// row it is inherited from come from a module this one only imports.
		module:    "CISCO-BGP4-MIB",
		name:      "cbgpPeerTable",
		oid:       "1.3.6.1.4.1.9.9.187.1.2.1",
		entryOID:  "1.3.6.1.4.1.9.9.187.1.2.1.1",
		columns:   []mib.IndexColumn{bgpPeerRemoteAddrColumn},
		rowSuffix: peerIPv4Suffix,
		labels:    map[string]string{"bgppeerremoteaddr": peerAddrValue},
	},
	{
		module:    "CISCO-BGP4-MIB",
		name:      "cbgpPeerCapsTable",
		oid:       "1.3.6.1.4.1.9.9.187.1.2.2",
		entryOID:  "1.3.6.1.4.1.9.9.187.1.2.2.1",
		columns:   []mib.IndexColumn{bgpPeerRemoteAddrColumn, cbgpPeerCapCodeColumn, cbgpPeerCapIndexColumn},
		rowSuffix: peerIPv4Suffix + ".2.1",
		labels: map[string]string{
			"bgppeerremoteaddr": peerAddrValue,
			"cbgppeercapcode":   "2",
			"cbgppeercapindex":  "1",
		},
	},
	{
		module:    "CISCO-BGP4-MIB",
		name:      "cbgpPeerAddrFamilyTable",
		oid:       "1.3.6.1.4.1.9.9.187.1.2.3",
		entryOID:  "1.3.6.1.4.1.9.9.187.1.2.3.1",
		columns:   []mib.IndexColumn{bgpPeerRemoteAddrColumn, cbgpPeerAfiColumn, cbgpPeerSafiColumn},
		rowSuffix: peerIPv4Suffix + ".1.1",
		labels: map[string]string{
			"bgppeerremoteaddr":      peerAddrValue,
			"cbgppeeraddrfamilyafi":  "1",
			"cbgppeeraddrfamilysafi": "1",
		},
	},
	{
		module:    "CISCO-BGP4-MIB",
		name:      "cbgpPeerAddrFamilyPrefixTable",
		oid:       "1.3.6.1.4.1.9.9.187.1.2.4",
		entryOID:  "1.3.6.1.4.1.9.9.187.1.2.4.1",
		columns:   []mib.IndexColumn{bgpPeerRemoteAddrColumn, cbgpPeerAfiColumn, cbgpPeerSafiColumn},
		rowSuffix: peerIPv4Suffix + ".1.1",
		labels: map[string]string{
			"bgppeerremoteaddr":      peerAddrValue,
			"cbgppeeraddrfamilyafi":  "1",
			"cbgppeeraddrfamilysafi": "1",
		},
	},
	{
		module:    "CISCO-BGP4-MIB",
		name:      "cbgpPeer2Table",
		oid:       "1.3.6.1.4.1.9.9.187.1.2.5",
		entryOID:  "1.3.6.1.4.1.9.9.187.1.2.5.1",
		columns:   []mib.IndexColumn{cbgpPeer2TypeColumn, cbgpPeer2RemoteAddrColumn},
		rowSuffix: "1." + peerInetAddrSuffix,
		labels: map[string]string{
			"cbgppeer2type":       "1",
			"cbgppeer2remoteaddr": peerAddrValue,
		},
	},
	{
		module:    "CISCO-BGP4-MIB",
		name:      "cbgpPeer2CapsTable",
		oid:       "1.3.6.1.4.1.9.9.187.1.2.6",
		entryOID:  "1.3.6.1.4.1.9.9.187.1.2.6.1",
		columns:   []mib.IndexColumn{cbgpPeer2TypeColumn, cbgpPeer2RemoteAddrColumn, cbgpPeer2CapCodeColumn, cbgpPeer2CapIndexColumn},
		rowSuffix: "1." + peerInetAddrSuffix + ".2.1",
		labels: map[string]string{
			"cbgppeer2type":       "1",
			"cbgppeer2remoteaddr": peerAddrValue,
			"cbgppeer2capcode":    "2",
			"cbgppeer2capindex":   "1",
		},
	},
	{
		module:    "CISCO-BGP4-MIB",
		name:      "cbgpPeer2AddrFamilyTable",
		oid:       "1.3.6.1.4.1.9.9.187.1.2.7",
		entryOID:  "1.3.6.1.4.1.9.9.187.1.2.7.1",
		columns:   []mib.IndexColumn{cbgpPeer2TypeColumn, cbgpPeer2RemoteAddrColumn, cbgpPeer2AfiColumn, cbgpPeer2SafiColumn},
		rowSuffix: "1." + peerInetAddrSuffix + ".1.1",
		labels: map[string]string{
			"cbgppeer2type":           "1",
			"cbgppeer2remoteaddr":     peerAddrValue,
			"cbgppeer2addrfamilyafi":  "1",
			"cbgppeer2addrfamilysafi": "1",
		},
	},
	{
		// The table TestEndToEndPipeline drives end to end, described here for
		// its metadata rather than for its samples.
		module:    "CISCO-BGP4-MIB",
		name:      "cbgpPeer2AddrFamilyPrefixTable",
		oid:       "1.3.6.1.4.1.9.9.187.1.2.8",
		entryOID:  "1.3.6.1.4.1.9.9.187.1.2.8.1",
		columns:   []mib.IndexColumn{cbgpPeer2TypeColumn, cbgpPeer2RemoteAddrColumn, cbgpPeer2AfiColumn, cbgpPeer2SafiColumn},
		rowSuffix: "1." + peerInetAddrSuffix + ".1.1",
		labels: map[string]string{
			"cbgppeer2type":           "1",
			"cbgppeer2remoteaddr":     peerAddrValue,
			"cbgppeer2addrfamilyafi":  "1",
			"cbgppeer2addrfamilysafi": "1",
		},
	},
	{
		module:    "CISCO-BGP4-MIB",
		name:      "cbgpPeer3Table",
		oid:       "1.3.6.1.4.1.9.9.187.1.2.9",
		entryOID:  "1.3.6.1.4.1.9.9.187.1.2.9.1",
		columns:   []mib.IndexColumn{cbgpPeer3VrfIDColumn, cbgpPeer3TypeColumn, cbgpPeer3RemoteAddrColumn},
		rowSuffix: "2.1." + peerInetAddrSuffix,
		labels: map[string]string{
			"cbgppeer3vrfid":      "2",
			"cbgppeer3type":       "1",
			"cbgppeer3remoteaddr": peerAddrValue,
		},
	},
}

// assertTableIsIndexed checks one stored table against what its module's INDEX
// clause requires of it, and then that the index cache can decode a row of that
// table into one label per index column.
func assertTableIsIndexed(t *testing.T, indexCache *metrics.TableIndexCache, table *mib.TableInfo, want indexedTable) {
	t.Helper()

	if table.OID != want.oid {
		t.Errorf("table OID is %q, want %q", table.OID, want.oid)
	}
	if table.EntryOID != want.entryOID {
		t.Errorf("conceptual row OID is %q, want %q", table.EntryOID, want.entryOID)
	}
	if table.Implied {
		t.Error("row reported as IMPLIED; none of these rows is, and an IMPLIED final column is encoded without the length prefix the sample row below carries")
	}

	// The name-only list is what the index cache turns into label names, so an
	// empty one leaves every row of the table unlabeled.
	if len(table.Indexes) == 0 {
		t.Fatal("table has no index columns; its rows cannot be told apart in Prometheus")
	}
	if wantNames := indexColumnNames(want.columns); !slices.Equal(table.Indexes, wantNames) {
		t.Errorf("INDEX clause parsed as %v, want %v", table.Indexes, wantNames)
	}

	// Names alone are not enough to decode a row: without each column's type
	// the decoder falls back to guessing one from the column's name.
	if len(table.IndexColumns) != len(want.columns) {
		t.Fatalf("table carries %d resolved index columns %v, want %d", len(table.IndexColumns), table.IndexColumns, len(want.columns))
	}
	for i, column := range want.columns {
		if table.IndexColumns[i] != column {
			t.Errorf("index column %d resolved to %+v, want %+v", i, table.IndexColumns[i], column)
		}
	}

	if len(table.Columns) == 0 || table.Columns[0] == "" {
		t.Fatalf("table has no column OIDs (%v); nothing of it can be polled", table.Columns)
	}

	// An instance OID of a real column of this table, carrying a real row
	// index. The device is one no other subtest polls, so the labels are the
	// decoded row index and nothing cached alongside it.
	instanceOID := table.Columns[0] + "." + want.rowSuffix
	labels := indexCache.ExtractIndexLabels("index-audit-device", instanceOID)
	if !maps.Equal(labels, want.labels) {
		t.Errorf("instance %s decoded to labels %v, want %v", instanceOID, labels, want.labels)
	}
}

// indexColumnNames lists the names of index columns, in order.
func indexColumnNames(columns []mib.IndexColumn) []string {
	names := make([]string, len(columns))
	for i, column := range columns {
		names[i] = column.Name
	}
	return names
}

// The OIDs the in-process agent serves. They are spelled out rather than built
// from the MIB so that a mistake in the MIB-driven naming path cannot cancel
// out against the same mistake in the fixture.
const (
	ifNumberOID  = ".1.3.6.1.2.1.2.1.0" // scalar, IF-MIB
	sysUpTimeOID = ".1.3.6.1.2.1.1.3.0" // scalar, SNMPv2-MIB

	ifEntryOID        = ".1.3.6.1.2.1.2.2.1" // ifTable row, INDEX { ifIndex }
	ifIndexColOID     = ifEntryOID + ".1"
	ifDescrColOID     = ifEntryOID + ".2"
	ifInOctetsColOID  = ifEntryOID + ".10"
	ifOutOctetsColOID = ifEntryOID + ".16"

	// cbgpPeer2AddrFamilyPrefixEntry, INDEX { cbgpPeer2Type,
	// cbgpPeer2RemoteAddr, cbgpPeer2AddrFamilyAfi, cbgpPeer2AddrFamilySafi }.
	bgpEntryOID       = ".1.3.6.1.4.1.9.9.187.1.2.8.1"
	bgpAcceptedColOID = bgpEntryOID + ".1" // cbgpPeer2AcceptedPrefixes
	bgpDeniedColOID   = bgpEntryOID + ".2" // cbgpPeer2DeniedPrefixes

	// unknownOID is under an enterprise arc no loaded module describes. A
	// device that implements a MIB we have not been given still has to reach
	// Prometheus, under a raw-OID name, rather than being dropped.
	unknownOID = ".1.3.6.1.4.1.9.9.999.1.1.0"
)

// bgpRowSuffix encodes one cbgpPeer2AddrFamilyPrefixEntry row index:
// cbgpPeer2Type (InetAddressType, 1 = ipv4), cbgpPeer2RemoteAddr (InetAddress,
// a length octet followed by the address octets), then cbgpPeer2AddrFamilyAfi
// and cbgpPeer2AddrFamilySafi. The variable-length address in the middle is
// what makes this index impossible to decode without the MIB.
func bgpRowSuffix(ipv4 string, afi, safi int) string {
	return fmt.Sprintf(".1.4.%s.%d.%d", ipv4, afi, safi)
}

// TestEndToEndPipeline drives the whole metric pipeline against real
// components: an in-process SNMPv2c agent on loopback, the production SNMP
// client, MIB metadata parsed from the repository's MIB files, and the
// production Prometheus exporter writing to a remote-write endpoint that
// decodes exactly what went on the wire.
//
// What it is protecting is the naming path. A table column has to arrive at
// Prometheus under the MIB object's name with the row index in labels
// (snmp_ifInOctets{ifindex="1"}); naming it after the instance OID
// (snmp__1_3_6_1_2_1_2_2_1_10_1) produces one metric name per row, which
// cannot be aggregated or queried by hand.
func TestEndToEndPipeline(t *testing.T) {
	mibStorage := loadPipelineMIBs(t, filepath.Join(t.TempDir(), "mib-storage"))

	agent := pipelineAgent(t)
	recorder := newRemoteWriteRecorder(t)

	// A flush interval far longer than the test keeps the background flusher
	// out of the way: every sample this test produces is flushed by the
	// explicit Flush below, so nothing depends on timing.
	exporter, err := metrics.NewPrometheusExporter(recorder.url(), 1000, time.Hour, mibStorage)
	if err != nil {
		t.Fatalf("create Prometheus exporter: %v", err)
	}
	defer func() { _ = exporter.Close() }()

	results := pollAgent(t, agent)

	ctx := context.Background()
	if err := exporter.ExportSNMPResults(ctx, "e2e-router", results, map[string]string{"environment": "test"}); err != nil {
		t.Fatalf("export SNMP results: %v", err)
	}
	if err := exporter.Flush(ctx); err != nil {
		t.Fatalf("flush exporter: %v", err)
	}

	written := recorder.written()
	if len(written) == 0 {
		t.Fatal("remote write endpoint received no time series")
	}

	t.Run("TableColumnsAreNamedAfterTheMIBObject", func(t *testing.T) {
		for _, ifIndex := range []string{"1", "2"} {
			s := written.byOID(t, ifInOctetsColOID+"."+ifIndex)
			if s.name != "snmp_ifInOctets" {
				t.Errorf("ifInOctets.%s reached Prometheus as %q, want snmp_ifInOctets; a raw-OID name gives one metric name per row",
					ifIndex, s.name)
			}
		}

		// The raw-OID fallback name for a column that the MIBs do describe
		// must appear nowhere at all, not merely alongside the right name.
		for _, s := range written {
			if strings.HasPrefix(s.name, "snmp__1_3_6_1_2_1_2_2_1") || strings.HasPrefix(s.name, "snmp__1_3_6_1_4_1_9_9_187") {
				t.Errorf("series %s{oid=%q} fell back to a raw-OID name for an object IF-MIB/CISCO-BGP4-MIB describes",
					s.name, s.labels["oid"])
			}
		}
	})

	t.Run("RowIndexIsALabelNotPartOfTheName", func(t *testing.T) {
		first := written.byOID(t, ifInOctetsColOID+".1")
		second := written.byOID(t, ifInOctetsColOID+".2")

		if first.name != second.name {
			t.Fatalf("two rows of ifInOctets got different metric names: %q and %q", first.name, second.name)
		}

		if first.labels["ifindex"] != "1" {
			t.Errorf("ifInOctets.1 carries ifindex=%q, want 1", first.labels["ifindex"])
		}
		if second.labels["ifindex"] != "2" {
			t.Errorf("ifInOctets.2 carries ifindex=%q, want 2", second.labels["ifindex"])
		}

		// Same name, different labels is the whole point: the two rows must
		// be distinguishable series rather than one series overwriting the
		// other.
		if first.labels["ifindex"] == second.labels["ifindex"] {
			t.Fatal("both ifInOctets rows carry the same ifindex label; the rows are indistinguishable in Prometheus")
		}

		// The value has to survive the walk, the conversion and the
		// protobuf round trip, or the labels are decorating nothing.
		if first.value != 1000001 {
			t.Errorf("ifInOctets.1 arrived with value %v, want 1000001", first.value)
		}
		if second.value != 2000002 {
			t.Errorf("ifInOctets.2 arrived with value %v, want 2000002", second.value)
		}

		// A second column of the same table proves the index label is
		// derived from the row rather than from the column that happened to
		// be looked at first.
		out := written.byOID(t, ifOutOctetsColOID+".2")
		if out.name != "snmp_ifOutOctets" {
			t.Errorf("ifOutOctets.2 reached Prometheus as %q, want snmp_ifOutOctets", out.name)
		}
		if out.labels["ifindex"] != "2" {
			t.Errorf("ifOutOctets.2 carries ifindex=%q, want 2", out.labels["ifindex"])
		}
	})

	t.Run("CompositeIndexDecodesEachIndexColumn", func(t *testing.T) {
		peers := map[string]string{
			"203.0.113.218": bgpAcceptedColOID + bgpRowSuffix("203.0.113.218", 1, 1),
			"203.0.113.202":  bgpAcceptedColOID + bgpRowSuffix("203.0.113.202", 1, 1),
		}

		for peer, oid := range peers {
			s := written.byOID(t, oid)

			if s.name != "snmp_cbgpPeer2AcceptedPrefixes" {
				t.Errorf("%s reached Prometheus as %q, want snmp_cbgpPeer2AcceptedPrefixes", oid, s.name)
			}

			// Every column of the INDEX clause has to become its own label.
			// Collapsing the row index into one opaque label, or stopping at
			// the first index column, is the failure this catches.
			if s.labels["cbgppeer2type"] != "1" {
				t.Errorf("peer %s: cbgppeer2type=%q, want 1 (ipv4)", peer, s.labels["cbgppeer2type"])
			}
			if s.labels["cbgppeer2remoteaddr"] != peer {
				t.Errorf("peer %s: cbgppeer2remoteaddr=%q, want %s; the variable-length InetAddress in the middle of the index was mis-decoded",
					peer, s.labels["cbgppeer2remoteaddr"], peer)
			}
			if s.labels["cbgppeer2addrfamilyafi"] != "1" {
				t.Errorf("peer %s: cbgppeer2addrfamilyafi=%q, want 1", peer, s.labels["cbgppeer2addrfamilyafi"])
			}
			if s.labels["cbgppeer2addrfamilysafi"] != "1" {
				t.Errorf("peer %s: cbgppeer2addrfamilysafi=%q, want 1", peer, s.labels["cbgppeer2addrfamilysafi"])
			}
		}

		// A second column of the composite table, so the decode is not
		// specific to one column's OID.
		denied := written.byOID(t, bgpDeniedColOID+bgpRowSuffix("203.0.113.218", 1, 1))
		if denied.name != "snmp_cbgpPeer2DeniedPrefixes" {
			t.Errorf("cbgpPeer2DeniedPrefixes reached Prometheus as %q", denied.name)
		}
		if denied.labels["cbgppeer2remoteaddr"] != "203.0.113.218" {
			t.Errorf("cbgpPeer2DeniedPrefixes carries cbgppeer2remoteaddr=%q, want 203.0.113.218", denied.labels["cbgppeer2remoteaddr"])
		}
	})

	t.Run("ScalarsAreNamedAndCarryNoIndexLabel", func(t *testing.T) {
		ifNumber := written.byOID(t, ifNumberOID)
		if ifNumber.name != "snmp_ifNumber" {
			t.Errorf("ifNumber reached Prometheus as %q, want snmp_ifNumber", ifNumber.name)
		}
		if ifNumber.value != 2 {
			t.Errorf("ifNumber arrived with value %v, want 2", ifNumber.value)
		}
		if _, indexed := ifNumber.labels["ifindex"]; indexed {
			t.Errorf("scalar ifNumber carries a row index label: %v", ifNumber.labels)
		}

		upTime := written.byOID(t, sysUpTimeOID)
		if upTime.name != "snmp_sysUpTime" {
			t.Errorf("sysUpTime reached Prometheus as %q, want snmp_sysUpTime", upTime.name)
		}
		if upTime.value != 987654 {
			t.Errorf("sysUpTime arrived with value %v, want 987654", upTime.value)
		}
	})

	t.Run("UnknownOIDFallsBackToItsRawOIDName", func(t *testing.T) {
		s := written.byOID(t, unknownOID)

		if s.name != "snmp__1_3_6_1_4_1_9_9_999_1_1_0" {
			t.Errorf("unknown OID reached Prometheus as %q, want snmp__1_3_6_1_4_1_9_9_999_1_1_0", s.name)
		}
		if s.value != 77 {
			t.Errorf("unknown OID arrived with value %v, want 77", s.value)
		}
	})

	t.Run("StringColumnBecomesAnInfoMetricKeyedByTheSameRowIndex", func(t *testing.T) {
		// ifDescr is not a number, so it cannot be a sample value. It reaches
		// Prometheus as an info metric carrying the row index of the interface
		// it describes, which is what lets a dashboard join an interface name
		// onto snmp_ifInOctets{ifindex="1"}.
		descriptions := map[string]string{"1": "GigabitEthernet0/0/0", "2": "GigabitEthernet0/0/1"}

		for ifIndex, want := range descriptions {
			var found *writtenSeries
			for i, s := range written {
				if s.name == "snmp_ifdescr_info" && s.labels["ifindex"] == ifIndex {
					found = &written[i]
					break
				}
			}

			if found == nil {
				t.Fatalf("no snmp_ifdescr_info series for ifindex=%s; received %v", ifIndex, written.names())
			}
			if found.labels["value"] != want {
				t.Errorf("snmp_ifdescr_info{ifindex=%q} carries value=%q, want %q", ifIndex, found.labels["value"], want)
			}
		}
	})

	t.Run("DeviceAndCallerLabelsSurviveToTheWire", func(t *testing.T) {
		// Scoped to the samples converted from SNMP results, which are the
		// ones this test drives; each is identified by its oid label.
		//
		// The info metrics built alongside them (snmp_*_info) deliberately are
		// not checked here: internal/metrics builds their label set from the
		// device name and the row index only, so the caller's extra labels do
		// not reach them. That is a gap in the exporter, not in this test, and
		// asserting it here would be asserting the gap is correct.
		for _, s := range written {
			if s.labels["oid"] == "" {
				continue
			}
			if s.labels["device"] != "e2e-router" {
				t.Errorf("series %s lost its device label: %v", s.name, s.labels)
			}
			if s.labels["environment"] != "test" {
				t.Errorf("series %s lost the caller's extra labels: %v", s.name, s.labels)
			}
		}
	})
}

// pipelineAgent starts an in-process SNMPv2c agent serving two ifTable rows, two
// rows of a composite-index BGP table, two scalars and one object no loaded MIB
// describes.
func pipelineAgent(t *testing.T) *testsupport.SNMPAgent {
	t.Helper()

	peerA := bgpRowSuffix("203.0.113.218", 1, 1)
	peerB := bgpRowSuffix("203.0.113.202", 1, 1)

	agent, err := testsupport.NewSNMPAgent(
		testsupport.Integer(ifNumberOID, 2),
		testsupport.TimeTicks(sysUpTimeOID, 987654),

		testsupport.Integer(ifIndexColOID+".1", 1),
		testsupport.Integer(ifIndexColOID+".2", 2),
		testsupport.OctetString(ifDescrColOID+".1", "GigabitEthernet0/0/0"),
		testsupport.OctetString(ifDescrColOID+".2", "GigabitEthernet0/0/1"),
		testsupport.Counter32(ifInOctetsColOID+".1", 1000001),
		testsupport.Counter32(ifInOctetsColOID+".2", 2000002),
		testsupport.Counter32(ifOutOctetsColOID+".1", 3000003),
		testsupport.Counter32(ifOutOctetsColOID+".2", 4000004),

		testsupport.Counter32(bgpAcceptedColOID+peerA, 41),
		testsupport.Counter32(bgpAcceptedColOID+peerB, 7),
		gauge32(bgpDeniedColOID+peerA, 3),
		gauge32(bgpDeniedColOID+peerB, 0),

		testsupport.Counter32(unknownOID, 77),
	)
	if err != nil {
		t.Fatalf("start in-process SNMP agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })

	return agent
}

// gauge32 builds a Gauge32 object, the syntax cbgpPeer2DeniedPrefixes is
// declared with. testsupport has no constructor for it and this file must not
// change one, so the PDU is spelled out here.
func gauge32(oid string, value uint) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Name: oid, Type: gosnmp.Gauge32, Value: value}
}

// pollAgent reads the agent the way the poller does: a bulk walk of each table
// and a single GET for the scalars, over the real SNMP client and a real UDP
// socket.
func pollAgent(t *testing.T, agent *testsupport.SNMPAgent) []*snmp.Result {
	t.Helper()

	device := &config.Device{
		Name:        "e2e-router",
		Address:     agent.Addr(),
		SNMPVersion: "2c",
		Community:   "public",
		Enabled:     true,
	}

	client, newErr := snmp.NewClient(device, 2*time.Second, 1)
	if newErr != nil {
		t.Fatalf("create SNMP client: %v", newErr)
	}
	if connectErr := client.Connect(); connectErr != nil {
		t.Fatalf("connect to in-process agent: %v", connectErr)
	}
	t.Cleanup(func() { _ = client.Close() })

	var results []*snmp.Result
	for _, root := range []string{ifEntryOID, bgpEntryOID} {
		walked, walkErr := client.BulkWalkAll(root)
		if walkErr != nil {
			t.Fatalf("bulk walk %s: %v", root, walkErr)
		}
		if len(walked) == 0 {
			t.Fatalf("bulk walk %s returned nothing; the agent served no rows", root)
		}
		results = append(results, walked...)
	}

	scalars, getErr := client.GetMultiple([]string{ifNumberOID, sysUpTimeOID, unknownOID})
	if getErr != nil {
		t.Fatalf("get scalars: %v", getErr)
	}
	results = append(results, scalars...)

	return results
}

// writtenSeries is one time series as it arrived at the remote-write endpoint,
// decoded from the protobuf the exporter actually sent.
type writtenSeries struct {
	name   string
	labels map[string]string
	value  float64
}

// writtenSeriesSet is everything one remote-write endpoint received.
type writtenSeriesSet []writtenSeries

// byOID returns the series whose oid label is exactly oid, failing the test if
// there is not exactly one. Every sample carries its instance OID as a label,
// which is what makes a series identifiable independently of the metric name
// the pipeline chose for it -- the thing under test.
func (set writtenSeriesSet) byOID(t *testing.T, oid string) writtenSeries {
	t.Helper()

	var matches []writtenSeries
	for _, s := range set {
		if s.labels["oid"] == oid || s.labels["oid"] == strings.TrimPrefix(oid, ".") {
			matches = append(matches, s)
		}
	}

	switch len(matches) {
	case 1:
		return matches[0]
	case 0:
		t.Fatalf("no series reached Prometheus for OID %s; the endpoint received %d series: %v", oid, len(set), set.names())
	default:
		t.Fatalf("OID %s produced %d series, want 1", oid, len(matches))
	}

	return writtenSeries{}
}

// names lists the distinct metric names received, sorted, for failure messages.
func (set writtenSeriesSet) names() []string {
	seen := make(map[string]struct{}, len(set))
	for _, s := range set {
		seen[s.name] = struct{}{}
	}

	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)

	return names
}

// remoteWriteRecorder is a Prometheus remote-write endpoint that keeps what it
// was sent. It decodes the snappy-compressed protobuf body exactly as
// Prometheus does, so the assertions are made against the bytes that left the
// exporter rather than against its internal state.
type remoteWriteRecorder struct {
	server *httptest.Server

	mu     sync.Mutex
	series writtenSeriesSet
}

// newRemoteWriteRecorder starts a recorder on loopback and stops it when the
// test ends.
func newRemoteWriteRecorder(t *testing.T) *remoteWriteRecorder {
	t.Helper()

	recorder := &remoteWriteRecorder{}
	recorder.server = httptest.NewServer(http.HandlerFunc(recorder.serveHTTP))
	t.Cleanup(recorder.server.Close)

	return recorder
}

// url is the remote-write URL to configure an exporter with.
func (r *remoteWriteRecorder) url() string {
	return r.server.URL + "/api/v1/write"
}

func (r *remoteWriteRecorder) serveHTTP(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	raw, err := snappy.Decode(nil, body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var write prompb.WriteRequest
	if err := write.Unmarshal(raw); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for _, ts := range write.Timeseries {
		labels := make(map[string]string, len(ts.Labels))
		name := ""
		for _, label := range ts.Labels {
			if label.Name == "__name__" {
				name = label.Value
				continue
			}
			labels[label.Name] = label.Value
		}
		for _, sample := range ts.Samples {
			r.series = append(r.series, writtenSeries{name: name, labels: labels, value: sample.Value})
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// written returns everything received so far.
func (r *remoteWriteRecorder) written() writtenSeriesSet {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append(writtenSeriesSet(nil), r.series...)
}
