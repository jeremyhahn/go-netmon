package metrics

import (
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// Gauges for how much metadata the MIB index holds and when it last changed.
var (
	mibIndexObjects = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "gonetmon_mib_index_objects",
			Help: "Number of instantiable MIB objects (scalars and columns) in the published metric name index.",
		},
	)
	mibIndexTables = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "gonetmon_mib_index_tables",
			Help: "Number of tables with usable index metadata in the published MIB index.",
		},
	)
	mibIndexRebuilds = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gonetmon_mib_index_rebuilds_total",
			Help: "Total number of MIB index rebuild attempts, by outcome (success, error).",
		},
		[]string{"outcome"},
	)
	mibIndexLastRebuildTimestamp = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "gonetmon_mib_index_last_rebuild_timestamp_seconds",
			Help: "Unix timestamp of the last successful MIB index rebuild. 0 if never rebuilt.",
		},
	)
)

func init() {
	prometheus.MustRegister(
		mibIndexObjects,
		mibIndexTables,
		mibIndexRebuilds,
		mibIndexLastRebuildTimestamp,
	)
}

// Rebuild outcomes reported by gonetmon_mib_index_rebuilds_total.
const (
	rebuildOutcomeSuccess = "success"
	rebuildOutcomeError   = "error"
)

// mibIndex is the immutable MIB-derived naming view a poll uses, replaced whole by atomic pointer swap; OID keys carry a leading dot.
type mibIndex struct {
	// names resolves an instance OID to a metric name.
	names *objectNameIndex

	// tables maps a normalized table entry OID to its index structure.
	tables map[string]*TableIndexInfo

	// columnToTable maps a normalized column OID to the normalized entry OID
	// of the table that column belongs to.
	columnToTable map[string]string

	// statusColumns maps a normalized table entry OID to the columns of that
	// table whose values become friendly-name labels.
	statusColumns map[string][]statusColumnInfo

	// catalog describes every metric the loaded MIBs could publish, keyed by
	// published metric name. It is built from the same sweep, so it costs no
	// extra pass over the store.
	catalog *catalogIndex

	// tableForOID memoizes tableFor inside the index, so a new index discards old answers.
	tableForOIDMu sync.RWMutex
	tableForOID   map[string]*TableIndexInfo
}

// emptyMIBIndex is the view of a cache that has no MIB metadata: it resolves no
// name and finds no table. Sharing one value lets every reader work against a
// non-nil index without a nil check on the poll path.
var emptyMIBIndex = &mibIndex{}

// objectCount returns how many instantiable MIB objects the index can name.
func (ix *mibIndex) objectCount() int {
	if ix == nil || ix.names == nil {
		return 0
	}
	return len(ix.names.metricNames)
}

// tableFor returns the table that oid belongs to, or nil if no loaded MIB
// describes one.
func (ix *mibIndex) tableFor(oid string) *TableIndexInfo {
	// An index with no tables can only answer nil, and answering it here keeps
	// the shared empty index free of a memo map it would otherwise need.
	if ix == nil || len(ix.tables) == 0 {
		return nil
	}

	normalized := normalizeOID(oid)

	ix.tableForOIDMu.RLock()
	cached, memoized := ix.tableForOID[normalized]
	ix.tableForOIDMu.RUnlock()
	if memoized {
		return cached
	}

	found := ix.tableForUncached(normalized)

	ix.tableForOIDMu.Lock()
	ix.tableForOID[normalized] = found
	ix.tableForOIDMu.Unlock()

	return found
}

// tableForUncached resolves a normalized OID to its table without consulting
// the memo.
func (ix *mibIndex) tableForUncached(normalized string) *TableIndexInfo {
	if entryOID, isColumn := ix.columnToTable[normalized]; isColumn {
		return ix.tables[entryOID]
	}

	// An instance OID of a column the MIBs do not describe still belongs to a
	// table whose entry OID prefixes it, which is enough to label its index.
	for entryOID, tableInfo := range ix.tables {
		if strings.HasPrefix(normalized, entryOID+".") {
			return tableInfo
		}
	}

	return nil
}

// statusColumnsFor returns the status columns of the table tableInfo describes.
func (ix *mibIndex) statusColumnsFor(tableInfo *TableIndexInfo) []statusColumnInfo {
	if ix == nil || tableInfo == nil {
		return nil
	}
	return ix.statusColumns[normalizeOID(tableInfo.EntryOID)]
}

// tableSet is a group of tables together with the column OIDs that map into
// them. Both the MIB store sweep and the OID learner produce one.
type tableSet struct {
	tables        map[string]*TableIndexInfo
	columnToTable map[string]string
}

// newTableSet returns an empty tableSet.
func newTableSet() *tableSet {
	return &tableSet{
		tables:        make(map[string]*TableIndexInfo),
		columnToTable: make(map[string]string),
	}
}

// add records a table and the columns that belong to it, keyed by normalized
// OID. It reports whether the table was new.
func (s *tableSet) add(tableInfo *TableIndexInfo, columnOIDs []string) bool {
	entryOID := normalizeOID(tableInfo.EntryOID)
	if _, exists := s.tables[entryOID]; exists {
		return false
	}

	tableInfo.EntryOID = entryOID
	s.tables[entryOID] = tableInfo
	for _, columnOID := range columnOIDs {
		s.columnToTable[normalizeOID(columnOID)] = entryOID
	}

	return true
}

// has reports whether the set already describes the table entered at oid.
func (s *tableSet) has(oid string) bool {
	if s == nil {
		return false
	}
	_, exists := s.tables[normalizeOID(oid)]
	return exists
}

// mibSweep is everything one pass over the MIB store yields. It is kept so that
// a table discovered by the OID learner can be published without re-reading the
// store, which at production scale costs tens of thousands of object reads.
type mibSweep struct {
	names         *objectNameIndex
	tables        *tableSet
	statusColumns map[string][]statusColumnInfo
	catalog       *catalogIndex
}

// newMIBIndex merges a MIB store sweep with learned tables; the sweep wins on conflict.
func newMIBIndex(sweep *mibSweep, learned *tableSet) *mibIndex {
	index := &mibIndex{
		tables:        make(map[string]*TableIndexInfo),
		columnToTable: make(map[string]string),
	}

	if sweep != nil {
		index.names = sweep.names
		index.statusColumns = sweep.statusColumns
		index.catalog = sweep.catalog
		copyTableSet(index, sweep.tables)
	}
	copyTableSet(index, learned)

	if len(index.tables) > 0 {
		index.tableForOID = make(map[string]*TableIndexInfo)
	}

	return index
}

// copyTableSet adds every table of set to index, leaving tables index already
// holds alone.
func copyTableSet(index *mibIndex, set *tableSet) {
	if set == nil {
		return
	}

	for entryOID, tableInfo := range set.tables {
		if _, exists := index.tables[entryOID]; !exists {
			index.tables[entryOID] = tableInfo
		}
	}
	for columnOID, entryOID := range set.columnToTable {
		if _, exists := index.columnToTable[columnOID]; !exists {
			index.columnToTable[columnOID] = entryOID
		}
	}
}
