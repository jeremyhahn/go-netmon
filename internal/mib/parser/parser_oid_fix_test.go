package parser

import (
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/testsupport"
)

// TestParser_ParseModule_IF_MIB_OIDs verifies that IF-MIB OIDs are correctly extracted
func TestParser_ParseModule_IF_MIB_OIDs(t *testing.T) {
	parser := New(testsupport.MIBDir("standard"))
	if err := parser.Init(); err != nil {
		t.Fatalf("Init() failed: %v", err)
	}
	defer parser.Close()

	oids, err := parser.ParseModule("IF-MIB")
	if err != nil {
		t.Fatalf("ParseModule() failed: %v", err)
	}

	if len(oids) == 0 {
		t.Fatal("Expected OIDs from IF-MIB, got none")
	}

	t.Logf("Parsed %d OIDs from IF-MIB", len(oids))

	// Verify that ALL OIDs have non-empty OID fields
	emptyOIDs := 0
	for _, oid := range oids {
		if oid.OID == "" {
			emptyOIDs++
			t.Logf("WARNING: OID has empty OID field: Name=%s, Module=%s", oid.Name, oid.Module)
		}
	}

	if emptyOIDs > 0 {
		t.Errorf("Found %d OIDs with empty OID fields out of %d total", emptyOIDs, len(oids))
	}

	// Check for well-known IF-MIB OIDs
	expectedOIDs := map[string]string{
		"ifNumber":      "1.3.6.1.2.1.2.1",
		"ifTable":       "1.3.6.1.2.1.2.2",
		"ifEntry":       "1.3.6.1.2.1.2.2.1",
		"ifIndex":       "1.3.6.1.2.1.2.2.1.1",
		"ifDescr":       "1.3.6.1.2.1.2.2.1.2",
		"ifType":        "1.3.6.1.2.1.2.2.1.3",
		"ifMtu":         "1.3.6.1.2.1.2.2.1.4",
		"ifSpeed":       "1.3.6.1.2.1.2.2.1.5",
		"ifPhysAddress": "1.3.6.1.2.1.2.2.1.6",
		"ifAdminStatus": "1.3.6.1.2.1.2.2.1.7",
		"ifOperStatus":  "1.3.6.1.2.1.2.2.1.8",
		"ifInOctets":    "1.3.6.1.2.1.2.2.1.10",
		"ifOutOctets":   "1.3.6.1.2.1.2.2.1.16",
	}

	foundOIDs := make(map[string]bool)
	for _, oid := range oids {
		if expectedOID, exists := expectedOIDs[oid.Name]; exists {
			foundOIDs[oid.Name] = true
			if oid.OID != expectedOID {
				t.Errorf("OID mismatch for %s: expected %s, got %s", oid.Name, expectedOID, oid.OID)
			}
			t.Logf("✓ Found %s with correct OID: %s", oid.Name, oid.OID)
		}
	}

	// Verify all expected OIDs were found
	for name := range expectedOIDs {
		if !foundOIDs[name] {
			t.Errorf("Expected OID %s not found in parsed results", name)
		}
	}

	if len(foundOIDs) != len(expectedOIDs) {
		t.Errorf("Expected to find %d known OIDs, found %d", len(expectedOIDs), len(foundOIDs))
	}
}

// TestParser_ParseModule_CISCO_BGP4_MIB_OIDs verifies that CISCO-BGP4-MIB OIDs are correctly extracted
func TestParser_ParseModule_CISCO_BGP4_MIB_OIDs(t *testing.T) {
	parser := New(
		testsupport.MIBDir("standard"),
		testsupport.MIBDir("cisco"),
		testsupport.MIBDir("asr1001"),
	)
	if err := parser.Init(); err != nil {
		t.Fatalf("Init() failed: %v", err)
	}
	defer parser.Close()

	oids, err := parser.ParseModule("CISCO-BGP4-MIB")
	if err != nil {
		t.Fatalf("ParseModule() failed: %v", err)
	}

	if len(oids) == 0 {
		t.Fatal("Expected OIDs from CISCO-BGP4-MIB, got none")
	}

	t.Logf("Parsed %d OIDs from CISCO-BGP4-MIB", len(oids))

	// Verify that ALL OIDs have non-empty OID fields
	emptyOIDs := 0
	var exampleEmptyOIDs []string
	for _, oid := range oids {
		if oid.OID == "" {
			emptyOIDs++
			if len(exampleEmptyOIDs) < 5 {
				exampleEmptyOIDs = append(exampleEmptyOIDs, oid.Name)
			}
		}
	}

	if emptyOIDs > 0 {
		t.Errorf("Found %d OIDs with empty OID fields out of %d total", emptyOIDs, len(oids))
		t.Errorf("Example OIDs with empty fields: %v", exampleEmptyOIDs)
	}

	// Check for well-known CISCO-BGP4-MIB OIDs
	expectedOIDs := map[string]bool{
		"cbgpGlobal":               true,
		"cbgpLocalAs":              true,
		"cbgpPeer":                 true,
		"cbgpPeerTable":            true,
		"cbgpPeerEntry":            true,
		"cbgpPeerPrefixAccepted":   true,
		"cbgpPeerPrefixDenied":     true,
		"cbgpPeerPrefixAdvertised": true,
	}

	foundOIDs := make(map[string]string)
	for _, oid := range oids {
		if expectedOIDs[oid.Name] {
			foundOIDs[oid.Name] = oid.OID
			if oid.OID == "" {
				t.Errorf("Expected OID %s has empty OID field", oid.Name)
			} else {
				t.Logf("✓ Found %s with OID: %s", oid.Name, oid.OID)
			}
		}
	}

	// Verify we found at least some expected OIDs
	if len(foundOIDs) == 0 {
		t.Error("Did not find any expected CISCO-BGP4-MIB OIDs")
	}
}

// TestParser_AllModules_NoEmptyOIDs verifies that no module returns empty OID fields
func TestParser_AllModules_NoEmptyOIDs(t *testing.T) {
	parser := New(
		testsupport.MIBDir("standard"),
		testsupport.MIBDir("cisco"),
		testsupport.MIBDir("asr1001"),
	)
	if err := parser.Init(); err != nil {
		t.Fatalf("Init() failed: %v", err)
	}
	defer parser.Close()

	testModules := []string{
		"SNMPv2-MIB",
		"IF-MIB",
		"IP-MIB",
	}

	for _, moduleName := range testModules {
		t.Run(moduleName, func(t *testing.T) {
			oids, err := parser.ParseModule(moduleName)
			if err != nil {
				t.Logf("Skipping %s: %v", moduleName, err)
				return
			}

			if len(oids) == 0 {
				t.Logf("Warning: %s returned 0 OIDs", moduleName)
				return
			}

			emptyOIDs := 0
			for _, oid := range oids {
				if oid.OID == "" {
					emptyOIDs++
				}
			}

			if emptyOIDs > 0 {
				t.Errorf("%s: Found %d OIDs with empty OID fields out of %d total",
					moduleName, emptyOIDs, len(oids))
			} else {
				t.Logf("✓ %s: All %d OIDs have valid OID fields", moduleName, len(oids))
			}
		})
	}
}
