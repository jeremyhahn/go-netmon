package metrics

// Helpers for building a TableIndexCache whose published index is stated
// directly, for tests about label extraction and naming that have no reason to
// stand up a MIB store.

// publishIndex installs sweep as the cache's published MIB-derived index,
// exactly as Refresh would after reading it from the store.
func (c *TableIndexCache) publishIndex(sweep *mibSweep) {
	c.buildMu.Lock()
	defer c.buildMu.Unlock()

	c.sweep = sweep
	c.publishLocked()
}

// newTestCache returns a cache with no MIB store whose published index
// describes tables, with columns mapping each table's entry OID to the column
// OIDs that belong to it.
func newTestCache(tables []*TableIndexInfo, columns map[string][]string) *TableIndexCache {
	cache := &TableIndexCache{}

	set := newTableSet()
	for _, tableInfo := range tables {
		// add normalizes EntryOID in place, so the caller's key has to be
		// read first.
		set.add(tableInfo, columns[tableInfo.EntryOID])
	}

	cache.publishIndex(&mibSweep{
		tables:        set,
		statusColumns: make(map[string][]statusColumnInfo),
	})

	return cache
}
