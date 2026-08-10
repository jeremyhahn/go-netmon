package metrics

import (
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// Freshness of the MIB-derived index is exported rather than inferred.
//
// The index is built from whatever the MIB store held when it was last swept,
// and an object the sweep did not see cannot be named after its MIB definition
// -- the sample keeps its raw-OID name and increments
// gonetmon_metric_name_unresolved_samples_total. That counter says samples are
// unnamed but not why; these say how much metadata the index actually holds and
// when it last changed, which is the difference between "the MIBs do not
// describe these OIDs" and "the index has not been rebuilt since the MIBs were
// loaded". Diagnosing the latter previously meant diffing metric names on the
// Prometheus side by hand.
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

// mibIndex is the complete MIB-derived view a poll is named and labeled
// against: the metric name of every instantiable object, the index structure of
// every table, and the status columns that supply friendly-name labels.
//
// It is immutable once published. A rebuild produces a whole new value that
// replaces the old one in a single atomic pointer swap, so a poll in progress
// keeps reading the view it started with and never observes a half-rebuilt one.
// That is what lets modules be loaded into the MIB store while devices are
// polling: the alternative -- taking the cache lock and mutating the maps in
// place -- would stop every poll for as long as it takes to sweep the store,
// which at production scale is tens of thousands of objects.
//
// Every OID key is normalized (leading dot) so that lookups never have to
// normalize the stored side, and EntryOID is normalized to match, which keeps
// the table map, the column map and the status column map mutually consistent.
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

	// tableForOID memoizes tableFor. The fallback in tableForUncached is a
	// prefix scan over every table, run once per SNMP result on every poll of
	// every device, and OIDs that resolve to no table at all would pay it
	// forever. The memo lives inside the index rather than beside it so that
	// publishing a new index discards the answers derived from the old one:
	// an invalidation that cannot be forgotten, and cannot race with a lookup
	// that is still writing back a result computed from the previous view.
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
}

// newMIBIndex merges a MIB store sweep with the tables the OID learner
// discovered into one immutable index.
//
// The sweep wins where both describe a table: a MIB definition carries index
// column types the learner can only guess at. Learned tables are held apart
// from the sweep rather than merged into it once, because a rebuild replaces
// the sweep wholesale and would otherwise drop everything learned since the
// process started.
func newMIBIndex(sweep *mibSweep, learned *tableSet) *mibIndex {
	index := &mibIndex{
		tables:        make(map[string]*TableIndexInfo),
		columnToTable: make(map[string]string),
	}

	if sweep != nil {
		index.names = sweep.names
		index.statusColumns = sweep.statusColumns
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
