package storage

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/cockroachdb/pebble"

	"github.com/jeremyhahn/go-netmon/internal/mib"
)

// targetModule is the module the per-module benchmarks look up. Its size is
// held constant while the rest of the store grows, so the benchmarks isolate
// the effect of total store size on a single-module lookup.
const (
	targetModule     = "TARGET-MIB"
	targetOIDs       = 200
	fillerModuleSize = 200
)

// benchStore returns a store holding totalOIDs objects, of which targetOIDs
// belong to targetModule and the rest are spread over filler modules.
//
// Records are written through one unsynced batch rather than StoreOID: a
// benchmark that spends its setup fsyncing 20,000 times measures the disk, not
// the lookup. The keys written are built by the same helpers production uses.
func benchStore(b *testing.B, totalOIDs int) *Storage {
	b.Helper()

	store, err := New(b.TempDir())
	if err != nil {
		b.Fatalf("open store: %v", err)
	}
	b.Cleanup(func() { store.Close() })

	batch := store.db.NewBatch()
	defer batch.Close()

	write := func(meta *mib.OIDMetadata) {
		data, err := json.Marshal(meta)
		if err != nil {
			b.Fatalf("marshal %s: %v", meta.Name, err)
		}
		key := oidKeyValue(meta)
		if err := batch.Set([]byte(PrefixOID+key), data, nil); err != nil {
			b.Fatalf("write %s: %v", meta.Name, err)
		}
		if err := batch.Set(moduleOIDIndexKey(meta.Module, key), []byte(key), nil); err != nil {
			b.Fatalf("index %s: %v", meta.Name, err)
		}
	}

	for i := range totalOIDs {
		module := targetModule
		if i >= targetOIDs {
			module = fmt.Sprintf("FILLER-%03d-MIB", (i-targetOIDs)/fillerModuleSize)
		}
		write(&mib.OIDMetadata{
			OID:         fmt.Sprintf("1.3.6.1.4.1.%d.%d", i/256, i%256),
			Name:        fmt.Sprintf("object%06d", i),
			Module:      module,
			Type:        "Counter32",
			Access:      "read-only",
			Status:      "current",
			Description: "a description of roughly the length MIB objects carry in practice",
			IsColumn:    true,
			Pollable:    true,
			ObjectClass: mib.ObjectClassMetricColumn,
		})
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		b.Fatalf("commit: %v", err)
	}

	return store
}

// scanAndFilter reproduces the pre-index lookup: read every OID in the store
// and keep the ones belonging to the module.
func scanAndFilter(b *testing.B, store *Storage, module string) []*mib.OIDMetadata {
	b.Helper()

	all, err := store.ListOIDs("")
	if err != nil {
		b.Fatalf("ListOIDs: %v", err)
	}

	matched := make([]*mib.OIDMetadata, 0, targetOIDs)
	for _, oid := range all {
		if oid.Module == module {
			matched = append(matched, oid)
		}
	}
	return matched
}

// BenchmarkModuleLookup contrasts the indexed lookup with the full scan it
// replaces, at store sizes an order of magnitude apart. The indexed variant
// should be flat across sizes; the scan should track the store.
func BenchmarkModuleLookup(b *testing.B) {
	for _, total := range []int{2000, 29000} {
		store := benchStore(b, total)

		b.Run(fmt.Sprintf("store=%d/index", total), func(b *testing.B) {
			for b.Loop() {
				got, err := store.ListModuleOIDs(targetModule)
				if err != nil {
					b.Fatalf("ListModuleOIDs: %v", err)
				}
				if len(got) != targetOIDs {
					b.Fatalf("got %d objects, want %d", len(got), targetOIDs)
				}
			}
		})

		b.Run(fmt.Sprintf("store=%d/scan", total), func(b *testing.B) {
			for b.Loop() {
				if got := scanAndFilter(b, store, targetModule); len(got) != targetOIDs {
					b.Fatalf("got %d objects, want %d", len(got), targetOIDs)
				}
			}
		})
	}
}

// BenchmarkGetPollableOIDs measures the call the poller makes per module.
func BenchmarkGetPollableOIDs(b *testing.B) {
	for _, total := range []int{2000, 29000} {
		store := benchStore(b, total)

		b.Run(fmt.Sprintf("store=%d", total), func(b *testing.B) {
			for b.Loop() {
				got, err := store.GetPollableOIDs(targetModule)
				if err != nil {
					b.Fatalf("GetPollableOIDs: %v", err)
				}
				if len(got) != targetOIDs {
					b.Fatalf("got %d objects, want %d", len(got), targetOIDs)
				}
			}
		})
	}
}
