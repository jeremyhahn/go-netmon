package parser

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/testsupport"
)

func scratchDirs(t *testing.T) []string {
	root, _ := testsupport.RepoRoot()
	var dirs []string
	err := filepath.WalkDir(filepath.Join(root, "mibs"), func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(dirs)
	return dirs
}

// TestScratchDump dumps a module's objects.
func TestScratchDump(t *testing.T) {
	for _, mod := range []string{"raritandsx.mib.txt", "RemoteKVMDevice-MIB.txt", "KNGeneric-MIB-20200526.MIB", "ATEN-PRODUCTS-MIB.mib", "10892.mib", "dcstorag.mib"} {
		t.Run(mod, func(t *testing.T) {
			p := New(scratchDirs(t)...)
			if err := p.Init(); err != nil {
				t.Fatal(err)
			}
			defer p.Close()

			info, err := p.LoadModule(mod)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			oids, err := p.ParseModule(info.Name)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			t.Logf("module=%s imports=%v nodes=%d", info.Name, info.Imports, info.OIDCount)
			untyped := 0
			for i, o := range oids {
				if o.Type == "" && (o.Access == "read-only" || o.Access == "read-write") {
					untyped++
				}
				if i < 25 {
					t.Logf("  %-34s oid=%-34s type=%-20s access=%-12s units=%q hint=%q enums=%d idx=%v",
						o.Name, o.OID, o.Type, o.Access, o.Units, o.DisplayHint, len(o.EnumValues), o.Index)
				}
			}
			t.Logf("  untyped readable: %d of %d", untyped, len(oids))
			tables, err := p.GetTableInfo(info.Name)
			if err != nil {
				t.Fatalf("tables: %v", err)
			}
			for _, tb := range tables {
				t.Logf("  TABLE %s oid=%s entry=%s idx=%v cols=%d idxcols=%+v", tb.Name, tb.OID, tb.EntryOID, tb.Indexes, len(tb.Columns), tb.IndexColumns)
			}
		})
	}
}
