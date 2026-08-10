package dashboard

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/testsupport"
)

// This file began as a debug scaffold: it opened the repository's live MIB
// store, printed a breakdown of CISCO-BGP4-MIB's objects and dumped the
// generated dashboard, under the title "helps debug why BGP dashboard is
// empty". It asserted nothing, it read host state outside the test's own
// temporary directory, and it was skipped, so it answered its question for
// nobody.
//
// The question does have an answer, and it is worth a regression test, so that
// is what this is now. A BGP dashboard comes back empty when the module is
// parsed without its imports on the search path.

const ciscoBGPModule = "CISCO-BGP4-MIB"

// TestBGPDashboardNeedsTheModulesImportsOnTheSearchPath is the answer to "why
// is the BGP dashboard empty".
//
// CISCO-BGP4-MIB lives in mibs/asr1001 but imports from mibs/standard and
// mibs/cisco. Parsed with only its own directory on the search path it returns
// no error: it returns objects with empty OIDs, no index columns and no table
// grouping, from which the generator can build no table widget at all. Parsed
// with the full path it yields the peer tables an operator expects.
//
// The two halves are asserted together because the failure is only recognizable
// as a failure next to the success: on its own, an empty dashboard looks like a
// device that has no BGP peers.
func TestBGPDashboardNeedsTheModulesImportsOnTheSearchPath(t *testing.T) {
	partialStore, closePartial := setupTestStorage(t)
	defer closePartial()
	loadTestMIB(t, partialStore, ciscoBGPModule, testsupport.MIBDir("asr1001"))

	partial, err := GenerateDashboard(partialStore, ciscoBGPModule)
	require.NoError(t, err, "an under-resolved parse still generates a dashboard, it is just empty")
	assert.Empty(t, partial.TableWidgets,
		"the fixture no longer demonstrates the hazard: an incomplete search path produced table widgets")

	fullStore, closeFull := setupTestStorage(t)
	defer closeFull()
	loadTestMIB(t, fullStore, ciscoBGPModule, testsupport.MIBDirs("standard", "cisco", "asr1001")...)

	full, err := GenerateDashboard(fullStore, ciscoBGPModule)
	require.NoError(t, err)
	require.NotEmpty(t, full.TableWidgets, "the complete search path produced no BGP tables")

	tables := make(map[string]*TableWidget, len(full.TableWidgets))
	for i := range full.TableWidgets {
		tables[full.TableWidgets[i].TableName] = &full.TableWidgets[i]
	}
	for _, name := range []string{"cbgpPeerTable", "cbgpPeer2Table", "cbgpRouteTable"} {
		assert.Contains(t, tables, name, "CISCO-BGP4-MIB table %s produced no widget", name)
	}

	// Every widget the complete parse produced must be addressable: a widget
	// with no OID is the shape the incomplete parse leaves behind, and it
	// renders as a panel that can never resolve a series.
	for name, table := range tables {
		assert.NotEmpty(t, table.InstanceSelector.LabelName, "table %s has no instance label", name)
		for _, field := range table.StatusFields {
			assert.NotEmpty(t, field.OID, "status field %s of %s has no OID", field.Name, name)
		}
		for _, chart := range table.MetricCharts {
			assert.NotEmpty(t, chart.OID, "metric chart %s of %s has no OID", chart.Name, name)
		}
	}
}

// TestBGPObjectsAreClassified is the metadata half of the same question: the
// generator only builds a widget for an object the parser has classified, so an
// unclassified object is silently dropped from the dashboard.
//
// Notification and conformance objects are legitimately unclassified — they are
// not pollable data — so the guarantee is about the module's readable objects.
func TestBGPObjectsAreClassified(t *testing.T) {
	store, cleanup := setupTestStorage(t)
	defer cleanup()

	loadTestMIB(t, store, ciscoBGPModule, testsupport.MIBDirs("standard", "cisco", "asr1001")...)

	oids, err := store.ListModuleOIDs(ciscoBGPModule)
	require.NoError(t, err)
	require.NotEmpty(t, oids, "CISCO-BGP4-MIB stored no objects")

	var unclassified []string
	classes := make(map[mib.ObjectClass]int)
	for _, oid := range oids {
		classes[oid.ObjectClass]++

		if !oid.Pollable {
			continue
		}
		if oid.ObjectClass == "" || oid.ObjectClass == mib.ObjectClassUnknown {
			unclassified = append(unclassified, oid.Name+" ("+oid.OID+")")
		}
	}

	assert.Empty(t, unclassified, "pollable BGP objects the generator will drop for want of a class")

	// The classes a dashboard is actually built from must all be represented,
	// or the module contributes only part of a dashboard.
	for _, class := range []mib.ObjectClass{
		mib.ObjectClassScalar,
		mib.ObjectClassIndexColumn,
		mib.ObjectClassStatusColumn,
		mib.ObjectClassMetricColumn,
	} {
		assert.NotZero(t, classes[class], "CISCO-BGP4-MIB yielded no %s objects", class)
	}
}
