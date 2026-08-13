package parser

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/testsupport"
)

// The parser against the dialects this network actually runs.
//
// Everything else here parses Cisco and IETF modules, which are the best-formed
// MIBs in existence. The fleet does not consist of those: it is ATEN KVMs,
// Raritan KVMs and Eaton UPSs, and both parser defects found this year came
// from that direction -- a dependency cached as failed left ATEN-PRODUCTS-MIB
// with 35 objects that all had empty OIDs, and a malformed IMPORTS list made
// KNGeneric-MIB store 159 records for 80 objects.
//
// Those are fixed and 170 modules render on the deployed stack, but nothing in
// `make test` would have caught either one, and nothing would catch the next
// gosmi upgrade breaking a dialect. Each family below is pinned to the shape it
// is known to produce.
//
// The counts are lower bounds so that a richer MIB file does not fail the
// suite, while losing objects does. The OID assertion is exact and is the
// important one: an object with no numeric OID cannot be polled, and a module
// full of them is precisely what a silently degraded parse looks like.

// vendorModule is one dialect and what it is known to yield.
type vendorModule struct {
	// file is the MIB relative to mibs/, and module is the name it declares.
	//
	// They differ, which is the point of naming both: gosmi identifies a module
	// by its file name, so a vendor file shipped as
	// "KNGeneric-MIB-20200526.MIB" does not load as KNGeneric-MIB until it is
	// stored under its declared name. That is what the importer does, and this
	// mirrors it rather than pretending the raw file works.
	file   string
	module string

	minObjects  int
	minPollable int
	minTables   int
	minUnits    int
}

var vendorDialects = []vendorModule{
	{
		// The ATEN registration tree: all structure, nothing to poll. It is here
		// because it is what ATEN-PRODUCTS-MIB imports, and the defect it
		// exposed was a dependency that failed to resolve.
		file: "vendor/KN-MIB-File-v4.5.445/ATEN-SMI.mib", module: "ATEN-SMI",
		minObjects: 4,
	},
	{
		// 35 objects with resolved OIDs. Before the dependency-cache fix this
		// came back as 6 objects whose OIDs were all empty, reported as a
		// successful import.
		file: "vendor/KN-MIB-File-v4.5.445/ATEN-PRODUCTS-MIB.mib", module: "ATEN-PRODUCTS-MIB",
		minObjects: 35,
	},
	{
		// The KVM's own readings: PSU and fan condition, stated as sentences.
		file: "vendor/KN-MIB-File-v4.5.445/KNGeneric-MIB-20200526.MIB", module: "KNGeneric-MIB",
		minObjects: 80,
	},
	{
		file: "vendor/RARITAN-DX/RemoteKVMDevice-MIB.txt", module: "RemoteKVMDevice-MIB",
		minObjects: 143, minPollable: 9, minTables: 2,
	},
	{
		file: "vendor/RARITAN-SX/raritandsx.mib.txt", module: "RARITANSX-MIB",
		minObjects: 1,
	},
}

// storeUnderModuleName copies a vendor file into dir named for the module it
// declares, which is what makes it loadable.
func storeUnderModuleName(t *testing.T, dir string, spec vendorModule) {
	t.Helper()

	source := filepath.Join(testsupport.MIBDir(""), spec.file)
	content, err := os.ReadFile(source) // #nosec G304 -- a fixture path built from this repository's own MIB tree
	if err != nil {
		t.Fatalf("read %s: %v", spec.file, err)
	}
	if err := os.WriteFile(filepath.Join(dir, spec.module), content, 0o600); err != nil {
		t.Fatalf("stage %s: %v", spec.module, err)
	}
}

// TestVendorDialectsParse is the regression net for the MIBs this fleet runs on.
func TestVendorDialectsParse(t *testing.T) {
	// Every dialect is staged together, under its declared name, because that
	// is the state the importer leaves the store in and because these modules
	// import one another: ATEN-PRODUCTS-MIB staged without ATEN-SMI beside it
	// parses to nothing at all, which is the shape of the very defect this file
	// exists to catch.
	staged := t.TempDir()
	for _, spec := range vendorDialects {
		storeUnderModuleName(t, staged, spec)
	}

	for _, spec := range vendorDialects {
		t.Run(spec.module, func(t *testing.T) {
			p := New(append(allMIBDirs(), staged)...)
			if err := p.Init(); err != nil {
				t.Fatalf("init parser: %v", err)
			}
			defer p.Close()

			oids, err := p.ParseModule(spec.module)
			if err != nil {
				t.Fatalf("parse %s: %v", spec.module, err)
			}

			var pollable, tables, units, missingOID int
			for _, oid := range oids {
				if oid.OID == "" {
					missingOID++
				}
				if oid.IsPollable() {
					pollable++
				}
				if oid.IsTable {
					tables++
				}
				if oid.Units != "" {
					units++
				}
			}

			// The one that matters most: an object with no numeric OID cannot
			// be polled, and a module full of them is what a parse that
			// silently degraded looks like.
			if missingOID > 0 {
				t.Errorf("%d of %d objects resolved to no OID; they can never be polled and the import would still report success",
					missingOID, len(oids))
			}
			if len(oids) < spec.minObjects {
				t.Errorf("parsed %d objects, want at least %d", len(oids), spec.minObjects)
			}
			if pollable < spec.minPollable {
				t.Errorf("%d pollable objects, want at least %d", pollable, spec.minPollable)
			}
			if tables < spec.minTables {
				t.Errorf("%d tables, want at least %d", tables, spec.minTables)
			}
			if units < spec.minUnits {
				t.Errorf("%d objects carry UNITS, want at least %d", units, spec.minUnits)
			}
		})
	}
}

// TestUPSMIBCarriesItsUnits covers the Eaton UPSs, whose whole value is
// quantities: volts, minutes, degrees, percent. A parse that loses UNITS still
// produces numbers, and a dashboard then plots them without saying what they
// are -- which is a number, not a reading.
func TestUPSMIBCarriesItsUnits(t *testing.T) {
	oids := parseWith(t, "UPS-MIB", allMIBDirs()...)

	var units, pollable, tables int
	for _, oid := range oids {
		if oid.Units != "" {
			units++
		}
		if oid.IsPollable() {
			pollable++
		}
		if oid.IsTable {
			tables++
		}
	}

	if len(oids) < 148 {
		t.Errorf("parsed %d objects, want at least 148", len(oids))
	}
	if units < 31 {
		t.Errorf("%d objects carry UNITS, want at least 31: a reading without its unit is just a number", units)
	}
	if pollable < 55 {
		t.Errorf("%d pollable objects, want at least 55", pollable)
	}
	if tables < 4 {
		t.Errorf("%d tables, want at least 4", tables)
	}
}

// TestAVendorFileUnderTheWrongNameIsRefused pins the constraint the importer
// exists to satisfy, so it cannot be forgotten.
//
// gosmi identifies a module by its file name, not by the name inside it. A
// vendor file shipped as "KNGeneric-MIB-20200526.MIB" therefore does not load
// as KNGeneric-MIB, and the failure is a load error rather than a wrong parse.
func TestAVendorFileUnderTheWrongNameIsRefused(t *testing.T) {
	p := New(allMIBDirs()...)
	if err := p.Init(); err != nil {
		t.Fatalf("init parser: %v", err)
	}
	defer p.Close()

	if _, err := p.ParseModule("KNGeneric-MIB"); err == nil {
		t.Error("KNGeneric-MIB loaded straight from the vendor tree; if that now works, " +
			"the importer's rename is no longer what makes these modules usable and this test should say so")
	}
}
