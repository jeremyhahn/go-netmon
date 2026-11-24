package parser

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/testsupport"
)

// The parser against vendor MIB dialects (ATEN, Raritan, Eaton); object counts are lower bounds, OID resolution is exact.

// vendorModule is one dialect and what it is known to yield.
type vendorModule struct {
	// file is the MIB relative to mibs/, module the name it declares; gosmi loads by file name.
	file   string
	module string

	minObjects  int
	minPollable int
	minTables   int
	minUnits    int
}

var vendorDialects = []vendorModule{
	{
		// The ATEN registration tree, which ATEN-PRODUCTS-MIB imports.
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

// TestVendorDialectsParse is the regression net for those vendor dialects.
func TestVendorDialectsParse(t *testing.T) {
	// Stage every dialect together under its declared name, as the importer does.
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

// TestUPSMIBCarriesItsUnits checks the Eaton UPS MIB keeps its UNITS.
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

// TestAVendorFileUnderTheWrongNameIsRefused: gosmi identifies a module by file name, so a misnamed file fails to load.
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
