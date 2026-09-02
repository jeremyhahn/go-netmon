package api

import (
	"context"
	"log"
	"sync"
	"time"
)

// The catalog is read once and kept, not fetched on every click.
//
// Every dashboard load re-read it whole: four Prometheus queries returning
// 12,324 catalog rows and 13,534 enumeration rows, decoded from JSON, on every
// request. Measured on monitor-01 before this existed, opening one device took 4 to 6
// seconds and most of that was re-downloading a description of the fleet's
// metrics that had not changed since the last time anyone clicked.
//
// It is cacheable because of what it is: the catalog describes what the loaded
// MIBs mean, and that changes when a MIB is imported or reloaded -- an
// operator action, minutes or days apart -- not when a device is polled. The
// readings on the dashboard are live and are not cached; only the words on the
// panels are.

// CatalogRefreshInterval is how often the held catalog is re-read in the
// background.
//
// It is not a staleness bound so much as a backstop: an import refreshes the
// catalog immediately through Refresh, so this only catches a change made by
// something other than this process -- another collector's MIB reload, or a
// direct edit to the poller's store. Five minutes is well inside the hour-long
// window a consumer reads with, so nothing can age out while a refresh is
// pending.
const CatalogRefreshInterval = 5 * time.Minute

// catalogCache holds the metric catalog between reads.
//
// Readers take a read lock and get whatever is currently held; they never wait
// on Prometheus except once, on a cache that has never been filled. That is
// the property that matters: a dashboard load must not be behind a five-second
// query, and a slow Prometheus must slow the refresh rather than the page.
type catalogCache struct {
	// read is what fills the cache. Injected so the cache can be tested
	// without a Prometheus.
	read func(context.Context) metricCatalogResult

	mu     sync.RWMutex
	held   metricCatalogResult
	at     time.Time
	primed bool

	// filling serializes fetches, so a burst of dashboard loads against a
	// cold cache makes one request to Prometheus rather than one each.
	filling sync.Mutex
}

func newCatalogCache(read func(context.Context) metricCatalogResult) *catalogCache {
	return &catalogCache{read: read}
}

// Catalog returns the held catalog, filling it first if it has never been read.
//
// The synchronous fill is deliberate and happens at most once per process: a
// dashboard opened in the seconds after a restart should wait and get labeled
// panels, not render unlabeled ones and leave the operator wondering why the
// units are missing.
func (c *catalogCache) Catalog(ctx context.Context) metricCatalogResult {
	c.mu.RLock()
	if c.primed {
		held := c.held
		c.mu.RUnlock()
		return held
	}
	c.mu.RUnlock()

	c.filling.Lock()
	defer c.filling.Unlock()

	// Another caller may have filled it while this one waited for the lock.
	c.mu.RLock()
	primed := c.primed
	held := c.held
	c.mu.RUnlock()
	if primed {
		return held
	}

	return c.fill(ctx)
}

// Refresh re-reads the catalog and replaces what is held.
//
// Called on a timer and whenever a MIB import changes what the catalog can
// describe. A read that fails leaves the previous catalog in place rather than
// blanking it: the descriptions it holds are still the best available answer,
// and replacing them with nothing would strip every panel of its units and
// enumerations because Prometheus was briefly unwell.
func (c *catalogCache) Refresh(ctx context.Context) {
	c.filling.Lock()
	defer c.filling.Unlock()

	c.fill(ctx)
}

// fill reads the catalog and stores it. The caller holds c.filling.
func (c *catalogCache) fill(ctx context.Context) metricCatalogResult {
	fresh := c.read(ctx)

	c.mu.RLock()
	primed, held := c.primed, c.held
	c.mu.RUnlock()

	// An unreadable catalog is not an empty one. Keeping what is held is the
	// same rule the two-read strategy follows one layer down, for the same
	// reason: losing every description because a query failed is worse than
	// serving descriptions that are a few minutes old.
	if !fresh.readable && primed {
		log.Printf("WARNING: the metric catalog could not be re-read; keeping %d entries",
			len(held.entries))
		return held
	}

	c.mu.Lock()
	c.held = fresh
	c.at = time.Now()
	c.primed = true
	c.mu.Unlock()

	return fresh
}

// Run refreshes the catalog until ctx is done.
func (c *catalogCache) Run(ctx context.Context) {
	ticker := time.NewTicker(CatalogRefreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.Refresh(ctx)
		}
	}
}

// Age is how long ago the held catalog was read, and whether it has been read
// at all. It is what a diagnostic endpoint reports rather than a guess.
func (c *catalogCache) Age() (time.Duration, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if !c.primed {
		return 0, false
	}
	return time.Since(c.at), true
}
