package metrics

import (
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/mib/storage"
)

// OIDLearner discovers table structures from SNMP responses
type OIDLearner struct {
	storage *storage.Storage

	// Learned table entry OIDs: entryOID -> LearnedTableInfo
	learnedTables map[string]*LearnedTableInfo
	mu            sync.RWMutex

	// OID pattern tracking for discovery
	oidPatterns map[string]*OIDPattern
}

// LearnedTableInfo contains information about a discovered table
type LearnedTableInfo struct {
	EntryOID   string   `json:"entry_oid"`
	TableName  string   `json:"table_name,omitempty"`
	IndexNames []string `json:"index_names,omitempty"`
	IndexTypes []string `json:"index_types,omitempty"`
	ColumnOIDs []string `json:"column_oids"`

	// SampleCount is how many distinct instances the table has been observed
	// to hold: its distinct index values times its columns. It describes what
	// the table contains, not how often it has been walked, so it is stable
	// across poll cycles and SampleCount/len(ColumnOIDs) is the table's row
	// count. Counting every response instead made this instances x poll
	// cycles, a number that grew without bound and meant nothing.
	SampleCount int `json:"sample_count"`

	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	Source    string    `json:"source"` // "mib" or "learned"
}

// OIDPattern tracks OID patterns for table discovery
type OIDPattern struct {
	Prefix string
	// ColumnIDs is the set of column sub-identifiers seen below the entry.
	ColumnIDs map[string]bool
	// IndexValues is the set of index suffixes seen below those columns: one
	// entry per table row, however many times the row has been polled.
	IndexValues map[string]bool
	ExampleOIDs []string
}

// sampleCount returns the number of distinct instances the pattern describes,
// one per column per row.
func (p *OIDPattern) sampleCount() int {
	return len(p.IndexValues) * len(p.ColumnIDs)
}

// NewOIDLearner creates a new OID learning system
func NewOIDLearner(store *storage.Storage) *OIDLearner {
	return &OIDLearner{
		storage:       store,
		learnedTables: make(map[string]*LearnedTableInfo),
		oidPatterns:   make(map[string]*OIDPattern),
	}
}

// LearnFromResponses analyzes SNMP responses to discover table structures
func (l *OIDLearner) LearnFromResponses(oids []string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Group OIDs by potential table entry prefix
	// For example: 1.3.6.1.4.1.9.9.187.1.2.8.1.1.1.4.203.0.113.218.1.1
	// Should detect: 1.3.6.1.4.1.9.9.187.1.2.8.1 as entry OID

	for _, oid := range oids {
		l.analyzeOID(oid)
	}

	// After analyzing all OIDs, identify tables
	l.identifyTables()
}

// analyzeOID extracts patterns from a single OID
func (l *OIDLearner) analyzeOID(oid string) {
	// Normalize OID
	oid = strings.TrimPrefix(oid, ".")
	parts := strings.Split(oid, ".")

	// Tables typically have at least: base + entry + column + index
	// Minimum length: 10 parts (conservative)
	if len(parts) < 10 {
		return
	}

	// Try different prefix lengths to find table boundaries
	// Typical table structure: ...table.1.column.index
	// So we look for patterns where the 2nd-to-last level is "1"
	for i := len(parts) - 3; i >= 8; i-- {
		// Check if this could be an entry OID (typically ends with .1)
		if i+1 >= len(parts) || parts[i] != "1" {
			continue
		}

		// Potential entry OID
		entryOID := strings.Join(parts[:i+1], ".")
		columnID := parts[i+1]
		// Everything below the column identifies the row.
		indexValue := strings.Join(parts[i+2:], ".")

		// Track this pattern
		pattern, exists := l.oidPatterns[entryOID]
		if !exists {
			pattern = &OIDPattern{
				Prefix:      entryOID,
				ColumnIDs:   make(map[string]bool),
				IndexValues: make(map[string]bool),
				ExampleOIDs: make([]string, 0, 5),
			}
			l.oidPatterns[entryOID] = pattern
		}

		pattern.ColumnIDs[columnID] = true
		pattern.IndexValues[indexValue] = true

		if len(pattern.ExampleOIDs) < 5 {
			pattern.ExampleOIDs = append(pattern.ExampleOIDs, oid)
		}
	}
}

// identifyTables identifies likely table entry OIDs from patterns
func (l *OIDLearner) identifyTables() {
	// A table should have multiple columns (at least 2)
	// and multiple instances
	for entryOID, pattern := range l.oidPatterns {
		if len(pattern.ColumnIDs) >= 2 && pattern.sampleCount() >= 2 {
			// This looks like a table!
			if _, exists := l.learnedTables[entryOID]; !exists {
				// Check if we have MIB metadata for this entry
				indexNames, indexTypes := l.lookupIndexMetadata(entryOID)

				info := &LearnedTableInfo{
					EntryOID:   entryOID,
					IndexNames: indexNames,
					IndexTypes: indexTypes,
					ColumnOIDs: make([]string, 0, len(pattern.ColumnIDs)),
					FirstSeen:  time.Now(),
					Source:     "learned",
				}

				// If we found index metadata in MIB, mark as hybrid source
				if len(indexNames) > 0 {
					info.Source = "hybrid"
				}

				// Extract column OIDs
				for colID := range pattern.ColumnIDs {
					info.ColumnOIDs = append(info.ColumnOIDs, entryOID+"."+colID)
				}
				sort.Strings(info.ColumnOIDs)

				l.learnedTables[entryOID] = info
				log.Printf("[OID LEARNER] Discovered table entry: %s with %d columns (source: %s)",
					entryOID, len(pattern.ColumnIDs), info.Source)
			}

			// Update stats
			l.learnedTables[entryOID].SampleCount = pattern.sampleCount()
			l.learnedTables[entryOID].LastSeen = time.Now()
		}
	}
}

// lookupIndexMetadata tries to find index information from MIB storage
func (l *OIDLearner) lookupIndexMetadata(entryOID string) ([]string, []string) {
	if l.storage == nil {
		return nil, nil
	}

	// Try to get OID metadata
	oidMeta, err := l.storage.GetOID(entryOID)
	if err == nil && len(oidMeta.Index) > 0 {
		// Found it! Extract index types
		indexTypes := make([]string, len(oidMeta.Index))
		for i, indexName := range oidMeta.Index {
			// Try to look up index column type
			indexOID, lookupErr := l.storage.GetOIDByName(oidMeta.Module, indexName)
			if lookupErr == nil {
				indexTypes[i] = indexOID.Type
			}
		}
		return oidMeta.Index, indexTypes
	}

	// Also try module:name format for entries with empty OIDs
	allOIDs, err := l.storage.ListOIDs("")
	if err != nil {
		return nil, nil
	}

	for _, oid := range allOIDs {
		if oid.IsTableEntry && len(oid.Index) > 0 {
			// Check if this entry's OID matches (when normalized)
			normalizedStored := strings.TrimPrefix(oid.OID, ".")
			normalizedEntry := strings.TrimPrefix(entryOID, ".")

			if normalizedStored == normalizedEntry {
				// Extract types
				indexTypes := make([]string, len(oid.Index))
				for i, indexName := range oid.Index {
					indexOID, err := l.storage.GetOIDByName(oid.Module, indexName)
					if err == nil {
						indexTypes[i] = indexOID.Type
					}
				}
				return oid.Index, indexTypes
			}
		}
	}

	return nil, nil
}

// GetLearnedTable retrieves learned information for a table entry OID
func (l *OIDLearner) GetLearnedTable(entryOID string) *LearnedTableInfo {
	l.mu.RLock()
	defer l.mu.RUnlock()

	entryOID = strings.TrimPrefix(entryOID, ".")
	return l.learnedTables[entryOID]
}

// GetAllLearnedTables returns all discovered tables
func (l *OIDLearner) GetAllLearnedTables() map[string]*LearnedTableInfo {
	l.mu.RLock()
	defer l.mu.RUnlock()

	// Return a copy to avoid race conditions
	result := make(map[string]*LearnedTableInfo, len(l.learnedTables))
	for k, v := range l.learnedTables {
		result[k] = v
	}
	return result
}

// MergeWithMIBMetadata enriches learned tables with MIB metadata.
//
// This used to do, for every learned table still missing index info, a full
// linear scan of every table in indexCache -- an O(learnedTables x
// cacheTables) cost paid on every single poll of every device (this is
// called unconditionally from ConvertSNMPResultsWithTableIndexes). Tables
// discovered from vendor/enterprise OIDs with no corresponding MIB
// definition can never resolve, so they were rescanned against the entire
// cache forever. Both operands grow monotonically with process uptime and
// never shrink, so poll latency degraded a little more on every cycle,
// eventually starving the exporter of the CPU time it needed to flush on
// schedule -- with nothing in the pipeline able to detect or report it.
// Building the lookup once turns this into O(learnedTables + cacheTables).
func (l *OIDLearner) MergeWithMIBMetadata(indexCache *TableIndexCache) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Fast path: nothing to enrich.
	unresolved := false
	for _, learnedInfo := range l.learnedTables {
		if len(learnedInfo.IndexNames) == 0 {
			unresolved = true
			break
		}
	}
	if !unresolved {
		return
	}

	// The published index is already keyed by normalized entry OID, so the
	// per-poll rebuild of a normalized lookup this used to do -- O(tables in
	// the cache) on every poll of every device -- is now a plain map read.
	tables := indexCache.index().tables

	for entryOID, learnedInfo := range l.learnedTables {
		// Skip if we already have index info
		if len(learnedInfo.IndexNames) > 0 {
			continue
		}

		tableInfo, found := tables[normalizeOID(entryOID)]
		if !found {
			continue
		}

		learnedInfo.IndexNames = tableInfo.IndexNames
		learnedInfo.IndexTypes = tableInfo.IndexTypes
		learnedInfo.TableName = tableInfo.TableOID
		learnedInfo.Source = "hybrid"
		log.Printf("[OID LEARNER] Enriched %s with MIB metadata", entryOID)
	}
}

// ExportToIndexCache adds the tables the learner discovered, and no loaded MIB
// describes, to the index cache. It returns how many were added.
//
// The common case -- every learned table already known to the cache -- is
// answered from the published index alone, so a poll that discovered nothing
// new never blocks index construction.
func (l *OIDLearner) ExportToIndexCache(indexCache *TableIndexCache) int {
	if indexCache == nil {
		return 0
	}

	published := indexCache.index()

	l.mu.RLock()
	var discovered []learnedTable
	for entryOID, learnedInfo := range l.learnedTables {
		// A table whose index columns are still unknown cannot label
		// anything, so exporting it would only add a prefix match that
		// produces no labels.
		if len(learnedInfo.IndexNames) == 0 {
			continue
		}
		if _, known := published.tables[normalizeOID(entryOID)]; known {
			continue
		}

		discovered = append(discovered, learnedTable{
			info: &TableIndexInfo{
				EntryOID:   entryOID,
				TableOID:   learnedInfo.TableName,
				IndexNames: learnedInfo.IndexNames,
				IndexTypes: learnedInfo.IndexTypes,
				IndexOIDs:  make([]string, len(learnedInfo.IndexNames)),
			},
			columns: learnedInfo.ColumnOIDs,
		})
	}
	l.mu.RUnlock()

	// The caller logs the count; logging it here as well would put two lines
	// per poll in the log for the same event.
	return indexCache.addLearnedTables(discovered)
}

// GetStats returns statistics about learned tables
func (l *OIDLearner) GetStats() map[string]interface{} {
	l.mu.RLock()
	defer l.mu.RUnlock()

	stats := make(map[string]interface{})
	stats["total_tables"] = len(l.learnedTables)
	stats["total_patterns"] = len(l.oidPatterns)

	learnedCount := 0
	hybridCount := 0
	for _, info := range l.learnedTables {
		switch info.Source {
		case "learned":
			learnedCount++
		case "hybrid":
			hybridCount++
		}
	}

	stats["learned_only"] = learnedCount
	stats["hybrid"] = hybridCount

	return stats
}
