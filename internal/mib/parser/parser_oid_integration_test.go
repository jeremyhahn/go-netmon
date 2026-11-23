//go:build integration

package parser

import (
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/mib/storage"
)

// TestIntegration_MIBParsing_NoEmptyOIDs verifies that real MIB files parse to
// objects that all carry a numeric OID, that the well-known OIDs land where the
// standards say they do, and that the result survives a round trip through
// storage.
//
// It parses with every MIB directory in the repository. A narrower search path
// does not fail — it silently yields fewer objects, most of them with an empty
// OID — so the search path is part of what this test fixes in place.
func TestIntegration_MIBParsing_NoEmptyOIDs(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "mib.db"))
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}
	defer store.Close()

	parser := New(allMIBDirs()...)
	if err := parser.Init(); err != nil {
		t.Fatalf("init parser: %v", err)
	}
	defer parser.Close()

	testCases := []struct {
		moduleName   string
		expectedOIDs map[string]string // name -> OID
		minTotalOIDs int
	}{
		{
			moduleName: "IF-MIB",
			expectedOIDs: map[string]string{
				"ifNumber":    "1.3.6.1.2.1.2.1",
				"ifTable":     "1.3.6.1.2.1.2.2",
				"ifIndex":     "1.3.6.1.2.1.2.2.1.1",
				"ifInOctets":  "1.3.6.1.2.1.2.2.1.10",
				"ifOutOctets": "1.3.6.1.2.1.2.2.1.16",
			},
			minTotalOIDs: minOIDsForIFMIB,
		},
		{
			moduleName: "CISCO-BGP4-MIB",
			expectedOIDs: map[string]string{
				"ciscoBgp4MIB": "1.3.6.1.4.1.9.9.187",
				// ciscoBgp4MIBObjects = { ciscoBgp4MIB 1 }, cbgpPeer =
				// { ciscoBgp4MIBObjects 2 }, cbgpPeerTable = { cbgpPeer 1 }.
				// This previously read ...1.2.3, which is cbgpPeerAddrFamilyTable;
				// a skip guard on this case kept the mismatch hidden.
				"cbgpPeerTable":           "1.3.6.1.4.1.9.9.187.1.2.1",
				"cbgpPeerAddrFamilyTable": "1.3.6.1.4.1.9.9.187.1.2.3",
			},
			minTotalOIDs: minOIDsForCiscoBGP4,
		},
		{
			moduleName: "SNMPv2-MIB",
			expectedOIDs: map[string]string{
				"system":    "1.3.6.1.2.1.1",
				"sysDescr":  "1.3.6.1.2.1.1.1",
				"sysUpTime": "1.3.6.1.2.1.1.3",
			},
			minTotalOIDs: minOIDsForSNMPv2,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.moduleName, func(t *testing.T) {
			moduleInfo, err := parser.LoadModule(tc.moduleName)
			if err != nil {
				t.Fatalf("load module %s: %v", tc.moduleName, err)
			}

			t.Logf("loaded %s with %d nodes", tc.moduleName, moduleInfo.OIDCount)

			oids, err := parser.ParseModule(tc.moduleName)
			if err != nil {
				t.Fatalf("parse module %s: %v", tc.moduleName, err)
			}

			if len(oids) < tc.minTotalOIDs {
				t.Errorf("%s yielded %d objects, want at least %d", tc.moduleName, len(oids), tc.minTotalOIDs)
			}

			assertOIDsResolved(t, tc.moduleName, oids)

			// Verify the well-known OIDs are present and correct.
			found := make(map[string]string, len(tc.expectedOIDs))
			for _, oid := range oids {
				expected, wanted := tc.expectedOIDs[oid.Name]
				if !wanted {
					continue
				}
				found[oid.Name] = oid.OID
				if oid.OID != expected {
					t.Errorf("%s::%s = %s, want %s", tc.moduleName, oid.Name, oid.OID, expected)
				}
			}
			for name, expected := range tc.expectedOIDs {
				if _, ok := found[name]; !ok {
					t.Errorf("%s::%s (%s) is missing from the parsed module", tc.moduleName, name, expected)
				}
			}

			// Round trip through storage. Every object must store: an
			// object the parser produces but the store rejects is one the
			// product silently drops at import time.
			if err = store.StoreModule(moduleInfo); err != nil {
				t.Fatalf("store module %s: %v", tc.moduleName, err)
			}

			stored := 0
			for _, oid := range oids {
				if err = store.StoreOID(oid); err != nil {
					t.Errorf("store %s::%s (%s): %v", tc.moduleName, oid.Name, oid.OID, err)
					continue
				}
				stored++
			}

			if stored != len(oids) {
				t.Errorf("stored %d of %d objects from %s", stored, len(oids), tc.moduleName)
			}

			allStored, listErr := store.ListOIDs("")
			if listErr != nil {
				t.Fatalf("list stored OIDs: %v", listErr)
			}

			retrieved := 0
			for _, oid := range allStored {
				if oid.Module != tc.moduleName {
					continue
				}
				retrieved++
				if oid.OID == "" {
					t.Errorf("stored object %s::%s came back with an empty OID", tc.moduleName, oid.Name)
				}
			}

			if retrieved != stored {
				t.Errorf("stored %d objects from %s but read back %d", stored, tc.moduleName, retrieved)
			}

			t.Logf("%s: %d objects parsed and stored, all with a numeric OID", tc.moduleName, len(oids))
		})
	}
}

// TestIntegration_StandardModulesParseCompletely checks the three standard
// modules the product depends on for interface, IP and system data.
//
// Every accessible object must carry both a numeric OID and a resolved type.
// No exceptions remain: IP-MIB's twelve InetZoneIndex objects used to resolve
// untyped because INET-ADDRESS-MIB (RFC 4001) was missing from mibs/ even
// though IP-MIB imports it. The module has since been added, so the expected
// count is zero everywhere — an untyped readable object now means an import
// went unresolved again.
func TestIntegration_StandardModulesParseCompletely(t *testing.T) {
	tests := []struct {
		module string
		minOID int
		// untypedAccessible is the number of readable objects expected to
		// come back with no type because a module they import is absent.
		// Zero is the only acceptable value; the field remains so that a
		// future known gap can be recorded explicitly rather than silently.
		untypedAccessible int
	}{
		{module: "SNMPv2-MIB", minOID: minOIDsForSNMPv2},
		{module: "IF-MIB", minOID: minOIDsForIFMIB},
		{module: "IP-MIB", minOID: 293},
	}

	for _, tt := range tests {
		t.Run(tt.module, func(t *testing.T) {
			oids := parseWith(t, tt.module, allMIBDirs()...)

			if len(oids) < tt.minOID {
				t.Errorf("%s yielded %d objects, want at least %d", tt.module, len(oids), tt.minOID)
			}

			assertOIDsResolved(t, tt.module, oids)

			if untyped := untypedAccessible(oids); len(untyped) != tt.untypedAccessible {
				t.Errorf("%s left %d readable objects untyped (%v), want %d; a readable object with no type is one whose IMPORT did not resolve, and it is still marked pollable",
					tt.module, len(untyped), untyped, tt.untypedAccessible)
			}

			t.Logf("%s: %d objects", tt.module, len(oids))
		})
	}
}
