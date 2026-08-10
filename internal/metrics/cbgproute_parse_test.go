package metrics

import (
	"strings"
	"testing"
)

// Test parseIndexValue with consecutive InetAddress indexes as in cbgpRouteTable
func TestParseIndexValueConsecutiveInetAddress(t *testing.T) {
	// Simulate parsing cbgpRouteTable OID suffix: 1.1.1.4.0.0.0.0.4.23.143.36.0.24
	// After skipping the column ID, the index parts are:
	indexParts := []string{"1", "1", "1", "4", "0", "0", "0", "0", "4", "23", "143", "36", "0", "24"}

	// Expected results:
	// afi = 1, safi = 1, peerType = 1
	// peer (InetAddress) = 0.0.0.0 (consumes: 4.0.0.0.0 = 5 parts)
	// prefix (InetAddress) = 23.143.36.0 (consumes: 4.23.143.36.0 = 5 parts)
	// prefixLen = 24

	pos := 0

	// Parse afi (Integer32)
	afiValue, consumed := parseIndexValue(indexParts[pos:], "Integer32")
	t.Logf("afi: value=%q, consumed=%d", afiValue, consumed)
	if afiValue != "1" {
		t.Errorf("Expected afi=1, got %q", afiValue)
	}
	if consumed != 1 {
		t.Errorf("Expected afi consumed=1, got %d", consumed)
	}
	pos += consumed

	// Parse safi (Integer32)
	safiValue, consumed := parseIndexValue(indexParts[pos:], "Integer32")
	t.Logf("safi: value=%q, consumed=%d", safiValue, consumed)
	if safiValue != "1" {
		t.Errorf("Expected safi=1, got %q", safiValue)
	}
	if consumed != 1 {
		t.Errorf("Expected safi consumed=1, got %d", consumed)
	}
	pos += consumed

	// Parse peerType (Integer32)
	peerTypeValue, consumed := parseIndexValue(indexParts[pos:], "Integer32")
	t.Logf("peerType: value=%q, consumed=%d", peerTypeValue, consumed)
	if peerTypeValue != "1" {
		t.Errorf("Expected peerType=1, got %q", peerTypeValue)
	}
	if consumed != 1 {
		t.Errorf("Expected peerType consumed=1, got %d", consumed)
	}
	pos += consumed

	// Parse peer (InetAddress) - first InetAddress
	t.Logf("Before parsing peer: pos=%d, remaining parts=%v", pos, indexParts[pos:])
	peerValue, consumed := parseIndexValue(indexParts[pos:], "InetAddress")
	t.Logf("peer: value=%q, consumed=%d", peerValue, consumed)
	if peerValue != "0.0.0.0" {
		t.Errorf("Expected peer=0.0.0.0, got %q", peerValue)
	}
	if consumed != 5 {
		t.Errorf("Expected peer consumed=5, got %d", consumed)
	}
	pos += consumed

	// Parse prefix (InetAddress) - second consecutive InetAddress (THIS IS THE BUG!)
	t.Logf("Before parsing prefix: pos=%d, remaining parts=%v", pos, indexParts[pos:])
	prefixValue, consumed := parseIndexValue(indexParts[pos:], "InetAddress")
	t.Logf("prefix: value=%q, consumed=%d", prefixValue, consumed)
	if prefixValue != "23.143.36.0" {
		t.Errorf("Expected prefix=23.143.36.0, got %q (BUG: This is the issue we're fixing!)", prefixValue)
	}
	if consumed != 5 {
		t.Errorf("Expected prefix consumed=5, got %d", consumed)
	}
	pos += consumed

	// Parse prefixLen (Integer32)
	t.Logf("Before parsing prefixLen: pos=%d, remaining parts=%v", pos, indexParts[pos:])
	prefixLenValue, consumed := parseIndexValue(indexParts[pos:], "Integer32")
	t.Logf("prefixLen: value=%q, consumed=%d", prefixLenValue, consumed)
	if prefixLenValue != "24" {
		t.Errorf("Expected prefixLen=24, got %q", prefixLenValue)
	}
	if consumed != 1 {
		t.Errorf("Expected prefixLen consumed=1, got %d", consumed)
	}
	pos += consumed

	// Verify we consumed all parts
	if pos != len(indexParts) {
		t.Errorf("Did not consume all index parts: consumed %d of %d", pos, len(indexParts))
	}
}

// Test the actual ExtractIndexLabels function with a full cbgpRouteTable OID
func TestExtractIndexLabelsForCbgpRouteTable(t *testing.T) {
	// Create a minimal table index cache
	entryOID := "1.3.6.1.4.1.9.9.187.1.1.1.1"
	tableInfo := &TableIndexInfo{
		EntryOID: entryOID,
		IndexNames: []string{
			"cbgpRouteAfi",
			"cbgpRouteSafi",
			"cbgpRoutePeerType",
			"cbgpRoutePeer",
			"cbgpRouteAddrPrefix",
			"cbgpRouteAddrPrefixLen",
		},
		IndexTypes: []string{
			"Integer32",
			"Integer32",
			"Integer32",
			"InetAddress",
			"InetAddress",
			"Integer32",
		},
	}

	// Map column OIDs to table
	columnOIDs := make([]string, 0, 19)
	for i := 1; i <= 19; i++ {
		columnOIDs = append(columnOIDs, strings.TrimSuffix(entryOID, "1")+string(rune('0'+i)))
	}

	cache := newTestCache([]*TableIndexInfo{tableInfo}, map[string][]string{entryOID: columnOIDs})

	// Test with a sample OID: cbgpRouteOrigin (column 7) with index 1.1.1.4.0.0.0.0.4.23.143.36.0.24
	testOID := "1.3.6.1.4.1.9.9.187.1.1.1.1.7.1.1.1.4.0.0.0.0.4.23.143.36.0.24"

	labels := cache.ExtractIndexLabels("test-device", testOID)

	t.Logf("Extracted labels: %+v", labels)

	// Verify expected labels
	expectedLabels := map[string]string{
		"cbgprouteafi":           "1",
		"cbgproutesafi":          "1",
		"cbgproutepeertype":      "1",
		"cbgproutepeer":          "0.0.0.0",
		"cbgprouteaddrprefix":    "23.143.36.0", // THIS IS THE BUG - currently returns "4"
		"cbgprouteaddrprefixlen": "24",
	}

	for key, expectedValue := range expectedLabels {
		actualValue, exists := labels[key]
		if !exists {
			t.Errorf("Expected label %s not found in result", key)
			continue
		}
		if actualValue != expectedValue {
			if key == "cbgprouteaddrprefix" {
				t.Errorf("BUG CONFIRMED: label %s = %q, expected %q", key, actualValue, expectedValue)
			} else {
				t.Errorf("label %s = %q, expected %q", key, actualValue, expectedValue)
			}
		}
	}
}
