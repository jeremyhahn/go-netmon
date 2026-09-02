package api

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

// Holding the catalog between reads.
//
// Every dashboard load used to re-read it whole: four Prometheus queries,
// 12,324 catalog rows and 13,534 enumeration rows, decoded from JSON, per
// click. Measured on monitor-01 at 4 to 6 seconds to open one device, most of it
// spent re-downloading a description of the fleet that had not changed since
// the last time anyone looked.

// countingRead is a catalog read that records how often it was asked.
type countingRead struct {
	calls  atomic.Int64
	result metricCatalogResult
}

func (c *countingRead) read(context.Context) metricCatalogResult {
	c.calls.Add(1)
	return c.result
}

func describing(metrics ...string) metricCatalogResult {
	entries := make(map[string]CatalogEntry, len(metrics))
	for _, m := range metrics {
		entries[m] = CatalogEntry{Metric: m, Unit: "octets"}
	}
	return metricCatalogResult{entries: entries, readable: true}
}

// TestTheCatalogIsReadOnceNotOncePerDashboard is the whole point.
func TestTheCatalogIsReadOnceNotOncePerDashboard(t *testing.T) {
	source := &countingRead{result: describing("snmp_ifInOctets")}
	cache := newCatalogCache(source.read)

	for i := 0; i < 10; i++ {
		held := cache.Catalog(context.Background())
		if len(held.entries) != 1 {
			t.Fatalf("read %d returned %d entries, want 1", i, len(held.entries))
		}
	}

	if got := source.calls.Load(); got != 1 {
		t.Errorf("Prometheus was asked %d times for ten dashboard loads, want 1", got)
	}
}

// TestABurstOnAColdCacheMakesOneRequest covers the thundering herd.
//
// A restart followed by several browsers opening dashboards at once would
// otherwise send one full catalog read per browser, at the moment Prometheus is
// least able to serve them.
func TestABurstOnAColdCacheMakesOneRequest(t *testing.T) {
	source := &countingRead{result: describing("snmp_ifInOctets")}
	cache := newCatalogCache(source.read)

	var wait sync.WaitGroup
	for i := 0; i < 20; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			cache.Catalog(context.Background())
		}()
	}
	wait.Wait()

	if got := source.calls.Load(); got != 1 {
		t.Errorf("twenty simultaneous loads made %d requests, want 1", got)
	}
}

// TestRefreshReplacesWhatIsHeld is what a MIB import triggers.
func TestRefreshReplacesWhatIsHeld(t *testing.T) {
	source := &countingRead{result: describing("snmp_ifInOctets")}
	cache := newCatalogCache(source.read)
	cache.Catalog(context.Background())

	source.result = describing("snmp_ifInOctets", "snmp_newlyImported")
	cache.Refresh(context.Background())

	held := cache.Catalog(context.Background())
	if _, described := held.entries["snmp_newlyImported"]; !described {
		t.Error("a refresh did not pick up the module that was just imported")
	}
}

// TestAFailedRefreshKeepsTheDescriptionsItHas is the degradation that matters.
//
// An unreadable catalog is not an empty one. Replacing what is held with
// nothing would strip every panel of its units, descriptions and decoded states
// because one query failed -- the same failure the two-read strategy avoids one
// layer down, and it would be reintroduced here.
func TestAFailedRefreshKeepsTheDescriptionsItHas(t *testing.T) {
	source := &countingRead{result: describing("snmp_ifInOctets")}
	cache := newCatalogCache(source.read)
	cache.Catalog(context.Background())

	source.result = metricCatalogResult{entries: map[string]CatalogEntry{}, readable: false}
	cache.Refresh(context.Background())

	held := cache.Catalog(context.Background())
	if len(held.entries) != 1 {
		t.Errorf("entries = %d, want the 1 it already had: a failed re-read must "+
			"not blank every panel's labels", len(held.entries))
	}
	if !held.readable {
		t.Error("the held catalog is reported unreadable; it was read successfully " +
			"and is being served")
	}
}

// TestAnUnreadableCatalogOnAColdCacheIsReportedAsSuch keeps the failed-read
// signal intact when there is nothing to fall back on.
func TestAnUnreadableCatalogOnAColdCacheIsReportedAsSuch(t *testing.T) {
	source := &countingRead{
		result: metricCatalogResult{entries: map[string]CatalogEntry{}, readable: false},
	}
	cache := newCatalogCache(source.read)

	held := cache.Catalog(context.Background())
	if held.readable {
		t.Error("a cold cache whose first read failed reported a readable catalog; " +
			"every panel would be filed as unattributed with no explanation")
	}
}

// TestTheCacheReportsWhetherItHasBeenRead keeps a diagnostic from guessing.
func TestTheCacheReportsWhetherItHasBeenRead(t *testing.T) {
	source := &countingRead{result: describing("snmp_ifInOctets")}
	cache := newCatalogCache(source.read)

	if _, read := cache.Age(); read {
		t.Error("an unread cache reported an age")
	}

	cache.Catalog(context.Background())
	if _, read := cache.Age(); !read {
		t.Error("a filled cache reported that it had never been read")
	}
}
