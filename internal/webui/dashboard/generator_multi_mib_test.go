package dashboard

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-netmon/internal/mib/parser"
	mibstorage "github.com/jeremyhahn/go-netmon/internal/mib/storage"
	"github.com/jeremyhahn/go-netmon/internal/testsupport"
)

// loadTestMIBFromPath loads a MIB from a specific directory
func loadTestMIBFromPath(t *testing.T, storage *mibstorage.Storage, mibPath string, moduleName string) {
	t.Helper()

	p := parser.New(mibPath)
	err := p.Init()
	require.NoError(t, err, "Failed to init parser")
	defer p.Close()

	moduleInfo, err := p.LoadModule(moduleName)
	require.NoError(t, err, "Failed to load module %s", moduleName)

	metadata, err := p.ParseModule(moduleName)
	require.NoError(t, err, "Failed to parse module %s", moduleName)

	for _, meta := range metadata {
		storeErr := storage.StoreOID(meta)
		require.NoError(t, storeErr, "Failed to store OID %s", meta.Name)
	}

	if moduleInfo != nil {
		err = storage.StoreModule(moduleInfo)
		require.NoError(t, err, "Failed to store module info")
	}
}

// TestGenerateDashboard_MultiMIBSeparation verifies that when multiple MIBs are loaded,
// each MIB's dashboard only contains widgets from that specific MIB
func TestGenerateDashboard_MultiMIBSeparation(t *testing.T) {
	storage, cleanup := setupTestStorage(t)
	defer cleanup()

	// Load IF-MIB from standard directory
	t.Log("Loading IF-MIB...")
	loadTestMIBFromPath(t, storage, testsupport.MIBDir("standard"), "IF-MIB")

	// Load CISCO-BGP4-MIB from asr1001 directory
	t.Log("Loading CISCO-BGP4-MIB...")
	loadTestMIBFromPath(t, storage, testsupport.MIBDir("asr1001"), "CISCO-BGP4-MIB")

	// Verify both modules are in storage
	allOIDs, err := storage.ListOIDs("")
	require.NoError(t, err)

	moduleCounts := make(map[string]int)
	for _, oid := range allOIDs {
		moduleCounts[oid.Module]++
	}

	t.Logf("OIDs by module: %+v", moduleCounts)
	assert.Greater(t, moduleCounts["IF-MIB"], 0, "IF-MIB should have OIDs")
	assert.Greater(t, moduleCounts["CISCO-BGP4-MIB"], 0, "CISCO-BGP4-MIB should have OIDs")

	// Generate dashboard for IF-MIB
	t.Log("Generating IF-MIB dashboard...")
	ifDashboard, err := GenerateDashboard(storage, "IF-MIB")
	require.NoError(t, err)
	require.NotNil(t, ifDashboard)

	// Verify IF-MIB dashboard
	assert.Equal(t, "IF-MIB", ifDashboard.MIBName)
	t.Logf("IF-MIB Dashboard: %d scalars, %d tables", len(ifDashboard.ScalarWidgets), len(ifDashboard.TableWidgets))

	// Log first few widgets for debugging
	if len(ifDashboard.ScalarWidgets) > 0 {
		t.Logf("IF-MIB first scalar: %s (OID: %s)", ifDashboard.ScalarWidgets[0].Name, ifDashboard.ScalarWidgets[0].OID)
	}
	if len(ifDashboard.TableWidgets) > 0 {
		t.Logf("IF-MIB first table: %s", ifDashboard.TableWidgets[0].TableName)
	}

	// Generate dashboard for CISCO-BGP4-MIB
	t.Log("Generating CISCO-BGP4-MIB dashboard...")
	bgpDashboard, err := GenerateDashboard(storage, "CISCO-BGP4-MIB")
	require.NoError(t, err)
	require.NotNil(t, bgpDashboard)

	// Verify CISCO-BGP4-MIB dashboard
	assert.Equal(t, "CISCO-BGP4-MIB", bgpDashboard.MIBName)
	t.Logf("CISCO-BGP4-MIB Dashboard: %d scalars, %d tables", len(bgpDashboard.ScalarWidgets), len(bgpDashboard.TableWidgets))

	// Log first few widgets for debugging
	if len(bgpDashboard.ScalarWidgets) > 0 {
		t.Logf("CISCO-BGP4-MIB first scalar: %s (OID: %s)", bgpDashboard.ScalarWidgets[0].Name, bgpDashboard.ScalarWidgets[0].OID)
	}
	if len(bgpDashboard.TableWidgets) > 0 {
		t.Logf("CISCO-BGP4-MIB first table: %s", bgpDashboard.TableWidgets[0].TableName)
	}

	// CRITICAL: Verify no cross-contamination in scalars
	for _, widget := range ifDashboard.ScalarWidgets {
		// Check if this widget contains BGP-related names
		lowerName := strings.ToLower(widget.Name)
		if strings.Contains(lowerName, "bgp") || strings.Contains(lowerName, "cbgp") {
			t.Errorf("IF-MIB dashboard contains BGP scalar: %s (OID: %s)", widget.Name, widget.OID)
		}

		// Verify OID doesn't start with CISCO-BGP4-MIB's root (1.3.6.1.4.1.9.9.187)
		if strings.HasPrefix(widget.OID, "1.3.6.1.4.1.9.9.187") {
			t.Errorf("IF-MIB dashboard contains CISCO-BGP4-MIB OID: %s (%s)", widget.Name, widget.OID)
		}
	}

	for _, widget := range bgpDashboard.ScalarWidgets {
		// Check if this widget is from IF-MIB namespace (1.3.6.1.2.1.2.* or 1.3.6.1.2.1.31.*)
		if strings.HasPrefix(widget.OID, "1.3.6.1.2.1.2.") || strings.HasPrefix(widget.OID, "1.3.6.1.2.1.31.") {
			t.Errorf("CISCO-BGP4-MIB dashboard contains IF-MIB OID: %s (%s)", widget.Name, widget.OID)
		}

		// Check for typical IF-MIB names
		lowerName := strings.ToLower(widget.Name)
		if strings.HasPrefix(lowerName, "if") && (strings.Contains(widget.OID, "1.3.6.1.2.1.2") || strings.Contains(widget.OID, "1.3.6.1.2.1.31")) {
			t.Errorf("CISCO-BGP4-MIB dashboard contains IF-MIB scalar: %s (OID: %s)", widget.Name, widget.OID)
		}
	}

	// CRITICAL: Verify no cross-contamination in tables
	for _, widget := range ifDashboard.TableWidgets {
		lowerName := strings.ToLower(widget.TableName)
		if strings.Contains(lowerName, "bgp") || strings.Contains(lowerName, "cbgp") {
			t.Errorf("IF-MIB dashboard contains BGP table: %s", widget.TableName)
		}

		// Check all fields in the table
		for _, field := range widget.StatusFields {
			if strings.HasPrefix(field.OID, "1.3.6.1.4.1.9.9.187") {
				t.Errorf("IF-MIB table contains CISCO-BGP4-MIB status field: %s (OID: %s)", field.Name, field.OID)
			}
		}

		for _, chart := range widget.MetricCharts {
			if strings.HasPrefix(chart.OID, "1.3.6.1.4.1.9.9.187") {
				t.Errorf("IF-MIB table contains CISCO-BGP4-MIB metric: %s (OID: %s)", chart.Name, chart.OID)
			}
		}
	}

	for _, widget := range bgpDashboard.TableWidgets {
		// Check for IF-MIB table names
		if widget.TableName == "ifTable" || widget.TableName == "ifXTable" {
			t.Errorf("CISCO-BGP4-MIB dashboard contains IF-MIB table: %s", widget.TableName)
		}

		// Check all fields in the table
		for _, field := range widget.StatusFields {
			if strings.HasPrefix(field.OID, "1.3.6.1.2.1.2.") || strings.HasPrefix(field.OID, "1.3.6.1.2.1.31.") {
				t.Errorf("CISCO-BGP4-MIB table contains IF-MIB status field: %s (OID: %s)", field.Name, field.OID)
			}
		}

		for _, chart := range widget.MetricCharts {
			if strings.HasPrefix(chart.OID, "1.3.6.1.2.1.2.") || strings.HasPrefix(chart.OID, "1.3.6.1.2.1.31.") {
				t.Errorf("CISCO-BGP4-MIB table contains IF-MIB metric: %s (OID: %s)", chart.Name, chart.OID)
			}
		}
	}

	// Verify dashboards have content
	assert.NotEmpty(t, ifDashboard.ScalarWidgets, "IF-MIB should have scalar widgets")
	// Note: CISCO-BGP4-MIB might not have scalars, so we don't assert on that
}

// TestGenerateDashboard_SNMPv2AndIFMIBSeparation tests with two standard MIBs
func TestGenerateDashboard_SNMPv2AndIFMIBSeparation(t *testing.T) {
	storage, cleanup := setupTestStorage(t)
	defer cleanup()

	// Load both MIBs from standard directory
	loadTestMIBFromPath(t, storage, testsupport.MIBDir("standard"), "SNMPv2-MIB")
	loadTestMIBFromPath(t, storage, testsupport.MIBDir("standard"), "IF-MIB")

	// Generate dashboards
	snmpDashboard, err := GenerateDashboard(storage, "SNMPv2-MIB")
	require.NoError(t, err)

	ifDashboard, err := GenerateDashboard(storage, "IF-MIB")
	require.NoError(t, err)

	// Verify names
	assert.Equal(t, "SNMPv2-MIB", snmpDashboard.MIBName)
	assert.Equal(t, "IF-MIB", ifDashboard.MIBName)

	// Verify SNMPv2-MIB doesn't contain IF-MIB objects
	for _, widget := range snmpDashboard.ScalarWidgets {
		lowerName := strings.ToLower(widget.Name)
		if strings.HasPrefix(lowerName, "if") && (strings.Contains(widget.OID, "1.3.6.1.2.1.2") || strings.Contains(widget.OID, "1.3.6.1.2.1.31")) {
			t.Errorf("SNMPv2-MIB dashboard contains IF-MIB object: %s (OID: %s)", widget.Name, widget.OID)
		}
	}

	// Verify IF-MIB doesn't contain SNMPv2-MIB objects
	for _, widget := range ifDashboard.ScalarWidgets {
		// SNMPv2-MIB has sysDescr (1.3.6.1.2.1.1.*), snmp* objects (1.3.6.1.2.1.11.*)
		if strings.HasPrefix(widget.OID, "1.3.6.1.2.1.1.") || strings.HasPrefix(widget.OID, "1.3.6.1.2.1.11.") {
			// These could be in either MIB, need to check module specifically
			// Let's verify using the actual OID metadata from storage
			allOIDs, _ := storage.ListOIDs("")
			for _, oid := range allOIDs {
				if oid.OID == widget.OID && oid.Module == "SNMPv2-MIB" {
					t.Errorf("IF-MIB dashboard contains SNMPv2-MIB object: %s (OID: %s)", widget.Name, widget.OID)
				}
			}
		}
	}
}
