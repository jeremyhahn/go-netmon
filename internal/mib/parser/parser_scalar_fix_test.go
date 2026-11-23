package parser

import (
	"testing"

	"github.com/sleepinggenius2/gosmi"

	"github.com/jeremyhahn/go-netmon/internal/mib"
)

// TestOrganizationalNodesNotScalar verifies that organizational/parent nodes
// like "system" and "snmp" are NOT marked as IsScalar=true
func TestOrganizationalNodesNotScalar(t *testing.T) {
	// Initialize gosmi with standard MIB path
	gosmi.Init()
	defer gosmi.Exit()

	// Set paths
	gosmi.AppendPath("../../../mibs/standard")

	// Create parser
	parser := New("../../../mibs/standard")
	err := parser.Init()
	if err != nil {
		t.Fatalf("Failed to initialize parser: %v", err)
	}
	defer parser.Close()

	// Parse SNMPv2-MIB which contains the "system" and "snmp" organizational nodes
	oids, err := parser.ParseModule("SNMPv2-MIB")
	if err != nil {
		t.Fatalf("Failed to parse SNMPv2-MIB: %v", err)
	}

	t.Logf("Parsed %d OIDs from SNMPv2-MIB", len(oids))

	// Create a map for easier lookup
	oidMap := make(map[string]*mib.OIDMetadata)
	for _, oid := range oids {
		oidMap[oid.Name] = oid
	}

	// Test cases: organizational nodes that should NOT be marked as scalars
	organizationalNodes := []struct {
		name string
		oid  string
	}{
		{"system", "1.3.6.1.2.1.1"},
		{"snmp", "1.3.6.1.2.1.11"},
	}

	for _, tc := range organizationalNodes {
		t.Run(tc.name, func(t *testing.T) {
			oid, found := oidMap[tc.name]
			if !found {
				t.Errorf("Expected to find organizational node '%s'", tc.name)
				return
			}

			if oid.OID != tc.oid {
				t.Errorf("Expected OID '%s' for '%s', got '%s'", tc.oid, tc.name, oid.OID)
			}

			if oid.IsScalar {
				t.Errorf("Organizational node '%s' should NOT be marked as IsScalar, but it is", tc.name)
			}

			if oid.Pollable {
				t.Errorf("Organizational node '%s' should NOT be pollable, but it is", tc.name)
			}

			t.Logf("✓ Organizational node '%s' (%s) correctly NOT marked as scalar (IsScalar=%v, Pollable=%v)",
				tc.name, oid.OID, oid.IsScalar, oid.Pollable)
		})
	}

	// Test cases: actual scalar nodes that SHOULD be marked as scalars
	scalarNodes := []struct {
		name string
		oid  string
	}{
		{"sysDescr", "1.3.6.1.2.1.1.1"},
		{"sysObjectID", "1.3.6.1.2.1.1.2"},
		{"sysUpTime", "1.3.6.1.2.1.1.3"},
		{"sysContact", "1.3.6.1.2.1.1.4"},
		{"sysName", "1.3.6.1.2.1.1.5"},
		{"sysLocation", "1.3.6.1.2.1.1.6"},
		{"sysServices", "1.3.6.1.2.1.1.7"},
		{"snmpInPkts", "1.3.6.1.2.1.11.1"},
		// Note: snmpOutPkts is deprecated in SNMPv2-MIB, so we don't test it here
	}

	for _, tc := range scalarNodes {
		t.Run(tc.name, func(t *testing.T) {
			oid, found := oidMap[tc.name]
			if !found {
				t.Errorf("Expected to find scalar node '%s'", tc.name)
				return
			}

			if oid.OID != tc.oid {
				t.Errorf("Expected OID '%s' for '%s', got '%s'", tc.oid, tc.name, oid.OID)
			}

			if !oid.IsScalar {
				t.Errorf("Scalar node '%s' should be marked as IsScalar, but it's not", tc.name)
			}

			if !oid.Pollable {
				t.Errorf("Scalar node '%s' should be pollable, but it's not (Status=%s, Access=%s)",
					tc.name, oid.Status, oid.Access)
			}

			t.Logf("✓ Scalar node '%s' (%s) correctly marked as scalar (IsScalar=%v, Pollable=%v)",
				tc.name, oid.OID, oid.IsScalar, oid.Pollable)
		})
	}
}

// TestScalarDetectionLogic specifically tests the logic used to detect scalars
func TestScalarDetectionLogic(t *testing.T) {
	// Initialize gosmi with standard MIB path
	gosmi.Init()
	defer gosmi.Exit()

	// Set paths
	gosmi.AppendPath("../../../mibs/standard")

	// Load SNMPv2-MIB
	_, err := gosmi.LoadModule("SNMPv2-MIB")
	if err != nil {
		t.Fatalf("Failed to load SNMPv2-MIB: %v", err)
	}

	// Test the "system" node - should NOT be scalar
	systemNode, err := gosmi.GetNode("system")
	if err != nil {
		t.Fatalf("Failed to get system node: %v", err)
	}

	raw := systemNode.GetRaw()
	t.Logf("system node: NodeKind=%v, HasType=%v", raw.NodeKind, systemNode.SmiType != nil)

	if systemNode.SmiType != nil {
		t.Errorf("Expected 'system' to have NO type (SmiType == nil), but it has one")
	}

	// Test the "sysDescr" node - SHOULD be scalar
	sysDescrNode, err := gosmi.GetNode("sysDescr")
	if err != nil {
		t.Fatalf("Failed to get sysDescr node: %v", err)
	}

	raw = sysDescrNode.GetRaw()
	t.Logf("sysDescr node: NodeKind=%v, HasType=%v", raw.NodeKind, sysDescrNode.SmiType != nil)

	if sysDescrNode.SmiType == nil {
		t.Errorf("Expected 'sysDescr' to have a type (SmiType != nil), but it doesn't")
	}
}
