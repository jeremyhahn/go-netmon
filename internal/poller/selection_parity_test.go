package poller

import (
	"fmt"
	"sort"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/mib/selector"
	webuiapi "github.com/jeremyhahn/go-netmon/internal/webui/api"
	webuistorage "github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// This file exists to make one class of bug impossible: the poller polling one
// set of objects while the web UI tells the operator it is polling another.
//
// Both go through internal/mib/selector — the poller via selector.Polled, the
// API via selector.Plan — so there is only ever one rule. These tests fail if
// anyone reintroduces a second copy of it on either side, which is why the test
// on the API side lives here, in the package whose behavior must not drift.

// selectionScenario is one device configuration to compare the two sides on.
type selectionScenario struct {
	name    string
	objects []*mib.OIDMetadata
	device  *config.Device
}

// selectionScenarios covers every branch of the rule: covered columns, absent
// tables, each kind of filter, a cap and a paused module.
func selectionScenarios() []selectionScenario {
	return []selectionScenario{
		{
			"unfiltered with the table present",
			withIfTable(),
			&config.Device{Name: "core-01", Modules: []string{"IF-MIB", "CISCO-PROCESS-MIB"}},
		},
		{
			"unfiltered with no table node",
			selectionFixture(),
			&config.Device{Name: "core-01", Modules: []string{"IF-MIB"}},
		},
		{
			"include names",
			withIfTable(),
			&config.Device{
				Name:    "core-01",
				Modules: []string{"IF-MIB"},
				ModuleFilters: map[string]config.ModuleFilter{
					"IF-MIB": {IncludeNames: []string{"Octets$"}},
				},
			},
		},
		{
			"exclude the table so its columns stand alone",
			withIfTable(),
			&config.Device{
				Name:    "core-01",
				Modules: []string{"IF-MIB"},
				ModuleFilters: map[string]config.ModuleFilter{
					"IF-MIB": {ExcludeNames: []string{"^ifTable$"}},
				},
			},
		},
		{
			"include OIDs",
			withIfTable(),
			&config.Device{
				Name:    "core-01",
				Modules: []string{"IF-MIB"},
				ModuleFilters: map[string]config.ModuleFilter{
					"IF-MIB": {IncludeOIDs: []string{`^1\.3\.6\.1\.2\.1\.2\.2\.1\.10$`}},
				},
			},
		},
		{
			"capped",
			selectionFixture(),
			&config.Device{
				Name:          "core-01",
				Modules:       []string{"IF-MIB"},
				ModuleFilters: map[string]config.ModuleFilter{"IF-MIB": {MaxOIDs: 2}},
			},
		},
		{
			"module paused",
			withIfTable(),
			&config.Device{Name: "core-01"},
		},
	}
}

// TestSelectModuleOIDsIsTheSelectorsPlan pins the poller to the shared rule. It
// fails the moment selectModuleOIDs grows logic of its own.
func TestSelectModuleOIDsIsTheSelectorsPlan(t *testing.T) {
	for _, tt := range selectionScenarios() {
		t.Run(tt.name, func(t *testing.T) {
			var want []string
			for _, decision := range selector.Plan(tt.objects, tt.device.Modules, tt.device.ModuleFilters) {
				if decision.Polled {
					want = append(want, decision.Object.Name)
				}
			}

			var got []string
			for _, oid := range selectModuleOIDs(tt.objects, tt.device) {
				got = append(got, oid.Name)
			}

			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("the poller selects %v; the plan says %v", got, want)
			}
		})
	}
}

// TestSelectModuleOIDsMatchesTheAPIObjectPlan is the anti-divergence test that
// spans the two sides: whatever GET /api/devices/{id}/mibs/{mibId}/objects
// reports as polled must be exactly what the poller fetches.
//
// It compares per module because the API answers about one MIB at a time, which
// is also why the comparison is meaningful: a module's tables, columns and cap
// are self-contained, so asking about one module in isolation has to give the
// same answer as asking about it alongside the others.
func TestSelectModuleOIDsMatchesTheAPIObjectPlan(t *testing.T) {
	for _, tt := range selectionScenarios() {
		t.Run(tt.name, func(t *testing.T) {
			pollerNames := namesByModule(selectModuleOIDs(tt.objects, tt.device))

			for _, module := range modulesIn(tt.objects) {
				assignment := apiAssignment(tt.device, module)
				plan := webuiapi.PlanMIBObjects(objectsOfModule(tt.objects, module), module, assignment, 1)

				var apiNames []string
				for _, object := range plan {
					if object.Polled {
						apiNames = append(apiNames, object.Name)
					}
				}

				if fmt.Sprint(apiNames) != fmt.Sprint(pollerNames[module]) {
					t.Errorf("%s: the API reports %v polled; the poller fetches %v",
						module, apiNames, pollerNames[module])
				}
			}
		})
	}
}

// apiAssignment expresses a device's configuration for one module the way the
// web UI stores it, so the two sides are asked the same question.
func apiAssignment(device *config.Device, module string) webuistorage.MIBAssignment {
	enabled := false
	for _, assigned := range device.Modules {
		if assigned == module {
			enabled = true
			break
		}
	}

	filter := device.ModuleFilters[module]
	return webuistorage.MIBAssignment{
		Enabled:      enabled,
		IncludeOIDs:  filter.IncludeOIDs,
		ExcludeOIDs:  filter.ExcludeOIDs,
		IncludeNames: filter.IncludeNames,
		ExcludeNames: filter.ExcludeNames,
		MaxOIDs:      filter.MaxOIDs,
	}
}

// namesByModule groups selected object names by the module they came from.
func namesByModule(oids []*mib.OIDMetadata) map[string][]string {
	byModule := make(map[string][]string)
	for _, oid := range oids {
		byModule[oid.Module] = append(byModule[oid.Module], oid.Name)
	}
	return byModule
}

// modulesIn lists the modules the objects belong to, in a stable order.
func modulesIn(objects []*mib.OIDMetadata) []string {
	seen := make(map[string]bool)
	modules := make([]string, 0, 4)
	for _, object := range objects {
		if !seen[object.Module] {
			seen[object.Module] = true
			modules = append(modules, object.Module)
		}
	}
	sort.Strings(modules)
	return modules
}

// objectsOfModule returns one module's objects, in order.
func objectsOfModule(objects []*mib.OIDMetadata, module string) []*mib.OIDMetadata {
	matched := make([]*mib.OIDMetadata, 0, len(objects))
	for _, object := range objects {
		if object.Module == module {
			matched = append(matched, object)
		}
	}
	return matched
}

// moduleShape describes a module the way a real one is built: some conceptual
// tables, some free-standing scalars, and the deprecated and not-accessible
// objects every published MIB carries.
type moduleShape struct {
	name    string
	base    string
	tables  int
	scalars int

	// columns is the number of columns each table has beyond its
	// not-accessible index column.
	columns int

	// deprecated and notAccessible are readable-looking objects the poller
	// must still refuse, so a passing count cannot be reached by taking
	// everything.
	deprecated    int
	notAccessible int
}

// buildModule renders a shape into stored metadata: a table node, a row node, a
// not-accessible index column and readable metric columns per table, plus the
// scalars and the noise.
func buildModule(shape moduleShape) []*mib.OIDMetadata {
	objects := make([]*mib.OIDMetadata, 0,
		shape.tables*(3+shape.columns)+shape.scalars+shape.deprecated+shape.notAccessible)

	for table := 1; table <= shape.tables; table++ {
		tableOID := fmt.Sprintf("%s.%d", shape.base, table)
		entryOID := tableOID + ".1"

		objects = append(objects,
			&mib.OIDMetadata{
				OID: tableOID, Name: fmt.Sprintf("%sTable%d", shape.name, table),
				Module: shape.name, Access: "not-accessible", Status: "current", IsTable: true,
			},
			&mib.OIDMetadata{
				OID: entryOID, Name: fmt.Sprintf("%sEntry%d", shape.name, table),
				Module: shape.name, Access: "not-accessible", Status: "current",
				IsTableEntry: true, TableOID: tableOID,
			},
			&mib.OIDMetadata{
				OID: entryOID + ".1", Name: fmt.Sprintf("%sIndex%d", shape.name, table),
				Module: shape.name, Type: "Integer32", Access: "not-accessible", Status: "current",
				IsColumn: true, IsIndex: true, TableOID: tableOID, EntryOID: entryOID,
			})

		for column := 1; column <= shape.columns; column++ {
			objects = append(objects, &mib.OIDMetadata{
				OID:    fmt.Sprintf("%s.%d", entryOID, column+1),
				Name:   fmt.Sprintf("%sColumn%d_%d", shape.name, table, column),
				Module: shape.name, Type: "Counter32", Access: "read-only", Status: "current",
				IsColumn: true, TableOID: tableOID, EntryOID: entryOID,
			})
		}
	}

	for scalar := 1; scalar <= shape.scalars; scalar++ {
		objects = append(objects, &mib.OIDMetadata{
			OID:    fmt.Sprintf("%s.%d", shape.base, 100+scalar),
			Name:   fmt.Sprintf("%sScalar%d", shape.name, scalar),
			Module: shape.name, Type: "Gauge32", Access: "read-only", Status: "current",
			IsScalar: true,
		})
	}
	for old := 1; old <= shape.deprecated; old++ {
		objects = append(objects, &mib.OIDMetadata{
			OID:    fmt.Sprintf("%s.%d", shape.base, 200+old),
			Name:   fmt.Sprintf("%sDeprecated%d", shape.name, old),
			Module: shape.name, Type: "Gauge32", Access: "read-only", Status: "deprecated",
			IsScalar: true,
		})
	}
	for hidden := 1; hidden <= shape.notAccessible; hidden++ {
		objects = append(objects, &mib.OIDMetadata{
			OID:    fmt.Sprintf("%s.%d", shape.base, 300+hidden),
			Name:   fmt.Sprintf("%sHidden%d", shape.name, hidden),
			Module: shape.name, Type: "Gauge32", Access: "not-accessible", Status: "current",
			IsScalar: true,
		})
	}

	return objects
}

// coreRouterShapes reproduces the table and scalar counts of the seventeen
// modules the "Core Routers" profile assigns, measured from a live install.
// Selected = one walk per table + every pollable scalar, so the totals are
// 77 + 78 = 155.
func coreRouterShapes() []moduleShape {
	return []moduleShape{
		{name: "CISCO-BGP4-MIB", base: "1.3.6.1.4.1.9.9.187", tables: 10, scalars: 2, columns: 6, deprecated: 2, notAccessible: 3},
		{name: "CISCO-ENVMON-MIB", base: "1.3.6.1.4.1.9.9.13", tables: 4, scalars: 4, columns: 5, deprecated: 1, notAccessible: 2},
		{name: "CISCO-IF-EXTENSION-MIB", base: "1.3.6.1.4.1.9.9.276", tables: 9, scalars: 5, columns: 4, deprecated: 1, notAccessible: 2},
		{name: "CISCO-MEMORY-POOL-MIB", base: "1.3.6.1.4.1.9.9.48", tables: 2, scalars: 1, columns: 7, deprecated: 1, notAccessible: 1},
		{name: "CISCO-PROCESS-MIB", base: "1.3.6.1.4.1.9.9.109", tables: 10, scalars: 2, columns: 8, deprecated: 2, notAccessible: 3},
		{name: "ENTITY-MIB", base: "1.3.6.1.2.1.47", tables: 5, scalars: 1, columns: 6, deprecated: 1, notAccessible: 2},
		{name: "HOST-RESOURCES-MIB", base: "1.3.6.1.2.1.25", tables: 11, scalars: 11, columns: 4, deprecated: 1, notAccessible: 2},
		{name: "IANAifType-MIB", base: "1.3.6.1.2.1.30", tables: 0, scalars: 0, columns: 0},
		{name: "IF-INVERTED-STACK-MIB", base: "1.3.6.1.2.1.31.1.5", tables: 1, scalars: 0, columns: 3},
		{name: "IF-MIB", base: "1.3.6.1.2.1.2", tables: 5, scalars: 3, columns: 12, deprecated: 2, notAccessible: 3},
		{name: "IP-MIB", base: "1.3.6.1.2.1.4", tables: 14, scalars: 10, columns: 5, deprecated: 3, notAccessible: 4},
		{name: "SNMPv2-CONF", base: "1.3.6.1.6.3.8", tables: 0, scalars: 0, columns: 0},
		{name: "SNMPv2-MIB", base: "1.3.6.1.2.1.1", tables: 1, scalars: 17, columns: 4, deprecated: 2, notAccessible: 2},
		{name: "SNMPv2-SMI", base: "1.3.6.1.6.1", tables: 0, scalars: 0, columns: 0, notAccessible: 16},
		{name: "SNMPv2-TC", base: "1.3.6.1.6.2", tables: 0, scalars: 0, columns: 0},
		{name: "TCP-MIB", base: "1.3.6.1.2.1.6", tables: 3, scalars: 16, columns: 5, deprecated: 2, notAccessible: 2},
		{name: "UDP-MIB", base: "1.3.6.1.2.1.7", tables: 2, scalars: 6, columns: 4, deprecated: 1, notAccessible: 1},
	}
}

// catalog renders the named shapes into one store's worth of objects.
func catalog(shapes []moduleShape, only ...string) []*mib.OIDMetadata {
	wanted := make(map[string]bool, len(only))
	for _, name := range only {
		wanted[name] = true
	}

	objects := make([]*mib.OIDMetadata, 0, 512)
	for _, shape := range shapes {
		if len(wanted) > 0 && !wanted[shape.name] {
			continue
		}
		objects = append(objects, buildModule(shape)...)
	}
	return objects
}

// moduleNames lists every shape's module name.
func moduleNames(shapes []moduleShape) []string {
	names := make([]string, 0, len(shapes))
	for _, shape := range shapes {
		names = append(names, shape.name)
	}
	return names
}

// TestSelectModuleOIDsOnACoreRouterCatalog pins the count for a realistic
// seventeen-module profile: one walk per table plus every pollable scalar, and
// nothing else. 77 tables and 78 scalars make 155.
//
// The columns matter here even though none is selected. There are 631 of them,
// so a regression that stopped covering columns with their table's walk would
// turn 155 requests per poll into hundreds, and this count is what catches it.
func TestSelectModuleOIDsOnACoreRouterCatalog(t *testing.T) {
	shapes := coreRouterShapes()
	objects := catalog(shapes)
	device := &config.Device{Name: "core-01", Modules: moduleNames(shapes)}

	got := selectModuleOIDs(objects, device)

	wantTables, wantScalars := 0, 0
	for _, shape := range shapes {
		wantTables += shape.tables
		wantScalars += shape.scalars
	}
	if wantTables+wantScalars != 155 {
		t.Fatalf("the fixture describes %d selectable objects; it is meant to describe 155",
			wantTables+wantScalars)
	}

	tables, scalars, other := 0, 0, 0
	for _, oid := range got {
		switch selector.Classify(oid) {
		case selector.KindTable:
			tables++
		case selector.KindScalar:
			scalars++
		default:
			other++
		}
	}

	if len(got) != 155 {
		t.Errorf("selected %d objects, want 155 (%d table walks + %d scalars)",
			len(got), wantTables, wantScalars)
	}
	if tables != wantTables || scalars != wantScalars {
		t.Errorf("selected %d tables and %d scalars, want %d and %d",
			tables, scalars, wantTables, wantScalars)
	}
	if other != 0 {
		t.Errorf("selected %d columns or row nodes; a walked table covers all of them", other)
	}
}

// TestSelectModuleOIDsIgnoresModulesTheStoreDoesNotHold reproduces a live
// incident. A device was assigned seventeen modules and the poller reported
// selecting 38 objects rather than 155, which looked like a selection bug. It
// was not: the poller's MIB metadata store held only three of the seventeen
// modules, and a module with no stored objects can contribute nothing.
//
// The rule is therefore that assignment alone does not produce polling — the
// metadata has to be there too — and this test states that in code so the next
// person to see a short count looks at the store rather than at the rule.
func TestSelectModuleOIDsIgnoresModulesTheStoreDoesNotHold(t *testing.T) {
	shapes := coreRouterShapes()

	// What that poller's store actually held: the three auto-loaded modules.
	stored := catalog(shapes, "SNMPv2-MIB", "IF-MIB", "CISCO-BGP4-MIB")
	device := &config.Device{Name: "core-01", Modules: moduleNames(shapes)}

	got := selectModuleOIDs(stored, device)

	// SNMPv2-MIB 1+17, IF-MIB 5+3, CISCO-BGP4-MIB 10+2.
	if len(got) != 38 {
		t.Errorf("selected %d objects from a store holding 3 of the 17 assigned modules, want 38",
			len(got))
	}
	for _, oid := range got {
		switch oid.Module {
		case "SNMPv2-MIB", "IF-MIB", "CISCO-BGP4-MIB":
		default:
			t.Errorf("selected %s from %s, which is not in the store", oid.Name, oid.Module)
		}
	}
}
