package api

import (
	"context"
	"log"
	"sync"
	"time"
)

// The metric catalog is cached between reads; it changes only when MIBs change, readings are never cached.

// CatalogRefreshInterval is the background re-read interval; imports refresh immediately.
const CatalogRefreshInterval = 5 * time.Minute

// catalogCache holds the metric catalog; readers wait on Prometheus only for the first fill.
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

// Catalog returns the held catalog, filling it synchronously the first time.
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

// Refresh re-reads the catalog; a failed read keeps the previous one.
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

	// An unreadable catalog is not an empty one: keep what is held.
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
