package metrics

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/mib/storage"
	"github.com/jeremyhahn/go-netmon/internal/snmp"
)

// These tests cover the defect that made a working on-demand MIB loader look
// broken. The poller now parses a device's assigned modules into the MIB store
// when the device's worker starts, but the exporter's MIB-derived index was
// built once, in NewPrometheusExporter, from whatever the store held at that
// moment -- the three auto-loaded modules. Every object of the fourteen modules
// loaded afterwards therefore matched no MIB definition, and its samples fell
// back to a raw-OID metric name with no index labels: on the live install, 121
// friendly names and 22,162 raw-OID fallbacks where there had been 120 and 0.
//
// The fallback is the designed behavior for an OID no loaded MIB describes.
// The defect was that the index never learned the MIBs had been loaded.

// lateModuleObjects is CISCO-MEMORY-POOL-MIB's ciscoMemoryPoolTable, standing
// for any of the fourteen modules the poller loads on demand: a table with one
// integer index, one numeric column and one status column, which is everything
// naming and labeling have to get right.
func lateModuleObjects() []*mib.OIDMetadata {
	const entryOID = "1.3.6.1.4.1.9.9.48.1.1.1"

	return []*mib.OIDMetadata{
		{
			OID: "1.3.6.1.4.1.9.9.48.1.1", Name: "ciscoMemoryPoolTable",
			Module: "CISCO-MEMORY-POOL-MIB", IsTable: true, Parent: "1.3.6.1.4.1.9.9.48.1",
		},
		{
			OID: entryOID, Name: "ciscoMemoryPoolEntry", Module: "CISCO-MEMORY-POOL-MIB",
			IsTableEntry: true, Parent: "1.3.6.1.4.1.9.9.48.1.1",
			Index: []string{"ciscoMemoryPoolType"},
			IndexColumns: []mib.IndexColumn{{
				Name: "ciscoMemoryPoolType", OID: entryOID + ".1",
				Type: "Integer32", BaseType: "Integer32",
			}},
		},
		{
			OID: entryOID + ".1", Name: "ciscoMemoryPoolType", Module: "CISCO-MEMORY-POOL-MIB",
			Type: "Integer32", IsColumn: true, Parent: entryOID,
			ObjectClass: mib.ObjectClassIndexColumn,
		},
		{
			OID: entryOID + ".2", Name: "ciscoMemoryPoolName", Module: "CISCO-MEMORY-POOL-MIB",
			Type: "DisplayString", IsColumn: true, Parent: entryOID,
			ObjectClass: mib.ObjectClassStatusColumn,
		},
		{
			OID: entryOID + ".5", Name: "ciscoMemoryPoolUsed", Module: "CISCO-MEMORY-POOL-MIB",
			Type: "Gauge32", IsColumn: true, Parent: entryOID,
			ObjectClass: mib.ObjectClassMetricColumn,
		},
	}
}

// memoryPoolUsedInstance is one instance of the late module's numeric column.
const memoryPoolUsedInstance = ".1.3.6.1.4.1.9.9.48.1.1.1.5.1"

// rawNameOfMemoryPoolUsed is the raw-OID metric name that instance falls back
// to while no loaded MIB describes it.
const rawNameOfMemoryPoolUsed = "snmp__1_3_6_1_4_1_9_9_48_1_1_1_5_1"

// storeObjects writes objects into an already-open MIB store, which is what the
// MIB loader does when it parses a module on demand.
func storeObjects(t *testing.T, store *storage.Storage, objects []*mib.OIDMetadata) {
	t.Helper()

	for _, object := range objects {
		if err := store.StoreOID(object); err != nil {
			t.Fatalf("store %s: %v", object.Name, err)
		}
	}
}

// convertOne converts a single numeric result through the full poll path.
func convertOne(cache *TableIndexCache, deviceName, oid string) *Sample {
	results := []*snmp.Result{{
		OID:       oid,
		Type:      gosnmp.Gauge32,
		Value:     uint(4096),
		Timestamp: time.Unix(1700000000, 0),
	}}
	samples := ConvertSNMPResultsWithTableIndexes(deviceName, results, nil, cache, nil)
	if len(samples) == 0 {
		return nil
	}
	return samples[0]
}

// TestRefreshNamesObjectsOfAModuleLoadedAfterConstruction is the regression
// test. It states the whole defect in three assertions: before the module is
// loaded the sample is named after its raw OID, after it is loaded but before
// the index is rebuilt it is *still* named after its raw OID, and only the
// rebuild restores the MIB name.
//
// The middle assertion is the one that matters: it is the live symptom, and it
// is what a fix that only reruns the sweep at construction would leave in
// place.
func TestRefreshNamesObjectsOfAModuleLoadedAfterConstruction(t *testing.T) {
	store := newNamingTestStore(t)
	cache := NewTableIndexCache(store)

	before := convertOne(cache, "core-01", memoryPoolUsedInstance)
	if before.Name != rawNameOfMemoryPoolUsed {
		t.Fatalf("with the module unloaded, name = %q, want the raw-OID name %q",
			before.Name, rawNameOfMemoryPoolUsed)
	}

	storeObjects(t, store, lateModuleObjects())

	stale := convertOne(cache, "core-01", memoryPoolUsedInstance)
	if stale.Name != rawNameOfMemoryPoolUsed {
		t.Fatalf("the index cannot resolve a module it has not been told about; name = %q", stale.Name)
	}

	if err := cache.Refresh(); err != nil {
		t.Fatalf("Refresh() after the module was loaded: %v", err)
	}

	after := convertOne(cache, "core-01", memoryPoolUsedInstance)
	if after.Name != "snmp_ciscoMemoryPoolUsed" {
		t.Errorf("after Refresh() name = %q, want %q", after.Name, "snmp_ciscoMemoryPoolUsed")
	}
}

// TestRefreshLabelsTablesOfAModuleLoadedAfterConstruction covers the other half
// of a usable series. A name alone is not enough: the instance sub-identifiers
// have to become labels, and those come from the table index metadata, which
// went stale for exactly the same reason the names did.
func TestRefreshLabelsTablesOfAModuleLoadedAfterConstruction(t *testing.T) {
	store := newNamingTestStore(t)
	cache := NewTableIndexCache(store)

	if labels := cache.ExtractIndexLabels("core-01", memoryPoolUsedInstance); labels != nil {
		t.Fatalf("with the module unloaded, ExtractIndexLabels() = %v, want nil", labels)
	}

	storeObjects(t, store, lateModuleObjects())
	if err := cache.Refresh(); err != nil {
		t.Fatalf("Refresh(): %v", err)
	}

	labels := cache.ExtractIndexLabels("core-01", memoryPoolUsedInstance)
	if got := labels["ciscomemorypooltype"]; got != "1" {
		t.Errorf("ciscomemorypooltype = %q, want %q (labels: %v)", got, "1", labels)
	}

	// The status column of a late-loaded table must supply friendly names too,
	// which requires the status column map to have been rebuilt as well.
	named := []*snmp.Result{{
		OID:       ".1.3.6.1.4.1.9.9.48.1.1.1.2.1",
		Type:      gosnmp.OctetString,
		Value:     []byte("Processor"),
		Timestamp: time.Unix(1700000000, 0),
	}}
	cache.CacheFriendlyNames("core-01", named)

	labels = cache.ExtractIndexLabels("core-01", memoryPoolUsedInstance)
	if got := labels["ciscomemorypoolname"]; got != "Processor" {
		t.Errorf("ciscomemorypoolname = %q, want %q (labels: %v)", got, "Processor", labels)
	}
}

// TestRefreshWithoutStorageReportsTheMissingStore is the bad-input case: a
// cache built without a MIB store has nothing to sweep, and says so rather than
// publishing an empty index over a good one.
func TestRefreshWithoutStorageReportsTheMissingStore(t *testing.T) {
	cache := NewTableIndexCache(nil)

	err := cache.Refresh()
	if err == nil {
		t.Fatal("Refresh() with no storage returned nil error")
	}
	if _, ok := err.(*StorageUnavailableError); !ok {
		t.Errorf("Refresh() error = %T (%v), want *StorageUnavailableError", err, err)
	}

	if names := cache.objectNames(); names != nil {
		t.Errorf("objectNames() = %v, want nil", names)
	}
	if labels := cache.ExtractIndexLabels("core-01", memoryPoolUsedInstance); labels != nil {
		t.Errorf("ExtractIndexLabels() = %v, want nil", labels)
	}
}

// TestFailedRefreshKeepsThePublishedIndex covers a sweep that fails after an
// index has already been published. Polling has to keep the names it had:
// publishing an empty index over a good one would turn a transient store error
// into a device-wide loss of metric names.
func TestFailedRefreshKeepsThePublishedIndex(t *testing.T) {
	store := newNamingTestStore(t)
	storeObjects(t, store, lateModuleObjects())
	cache := NewTableIndexCache(store)

	if _, found := cache.objectNames().metricNameFor(memoryPoolUsedInstance); !found {
		t.Fatal("the freshly built index does not resolve the fixture")
	}

	// Take the store away, which is the one sweep failure that can be induced
	// without relying on how the storage layer behaves once closed.
	cache.storage = nil

	if err := cache.Refresh(); err == nil {
		t.Error("Refresh() with the store gone returned nil error")
	}

	if _, found := cache.objectNames().metricNameFor(memoryPoolUsedInstance); !found {
		t.Error("a failed rebuild discarded the previously published index")
	}
	if labels := cache.ExtractIndexLabels("core-01", memoryPoolUsedInstance); labels["ciscomemorypooltype"] != "1" {
		t.Errorf("a failed rebuild discarded the table metadata; labels: %v", labels)
	}
}

// TestUnresolvedOIDsFallBackAndAreCounted pins the behavior the fallback is
// there for. An OID no loaded MIB describes keeps a raw-OID name -- dropping
// the sample would lose data silently -- and every such sample is counted, so
// the gap is measurable instead of invisible.
func TestUnresolvedOIDsFallBackAndAreCounted(t *testing.T) {
	cache := NewTableIndexCache(newNamingTestStore(t))

	const device = "unresolved-device"
	before := testutil.ToFloat64(metricNameUnresolvedSamples.WithLabelValues(device))

	results := []*snmp.Result{
		counterResult(".1.3.6.1.4.1.99999.1.2.3.4", 1),
		counterResult(".1.3.6.1.4.1.99999.1.2.3.5", 2),
		counterResult(".1.3.6.1.2.1.2.2.1.10.5", 3), // resolvable: ifInOctets
	}
	samples := ConvertSNMPResultsWithTableIndexes(device, results, nil, cache, nil)

	if len(samples) != 3 {
		t.Fatalf("got %d samples, want 3", len(samples))
	}
	for _, sample := range samples[:2] {
		if !strings.HasPrefix(sample.Name, "snmp__1_3_6_1_4_1_99999") {
			t.Errorf("unresolvable OID produced name %q, want the raw-OID name", sample.Name)
		}
	}
	if samples[2].Name != "snmp_ifInOctets" {
		t.Errorf("resolvable OID produced name %q, want %q", samples[2].Name, "snmp_ifInOctets")
	}

	after := testutil.ToFloat64(metricNameUnresolvedSamples.WithLabelValues(device))
	if after-before != 2 {
		t.Errorf("unresolved sample counter rose by %v, want 2", after-before)
	}
}

// TestRefreshTracksIndexSizeMetrics covers the observability the defect needed
// and did not have: how much MIB metadata the published index actually holds,
// so that "the index is stale" is a number rather than an inference from
// Prometheus label values.
func TestRefreshTracksIndexSizeMetrics(t *testing.T) {
	store := newNamingTestStore(t)
	cache := NewTableIndexCache(store)

	objectsBefore := testutil.ToFloat64(mibIndexObjects)
	tablesBefore := testutil.ToFloat64(mibIndexTables)
	rebuildsBefore := testutil.ToFloat64(mibIndexRebuilds.WithLabelValues(rebuildOutcomeSuccess))

	storeObjects(t, store, lateModuleObjects())
	if err := cache.Refresh(); err != nil {
		t.Fatalf("Refresh(): %v", err)
	}

	// The fixture adds three instantiable objects (two columns plus the index
	// column) and one table.
	if got := testutil.ToFloat64(mibIndexObjects) - objectsBefore; got != 3 {
		t.Errorf("indexed objects rose by %v, want 3", got)
	}
	if got := testutil.ToFloat64(mibIndexTables) - tablesBefore; got != 1 {
		t.Errorf("indexed tables rose by %v, want 1", got)
	}
	if got := testutil.ToFloat64(mibIndexRebuilds.WithLabelValues(rebuildOutcomeSuccess)) - rebuildsBefore; got != 1 {
		t.Errorf("successful rebuilds rose by %v, want 1", got)
	}

	failuresBefore := testutil.ToFloat64(mibIndexRebuilds.WithLabelValues(rebuildOutcomeError))
	if err := NewTableIndexCache(nil).Refresh(); err == nil {
		t.Fatal("Refresh() with no storage returned nil error")
	}
	if got := testutil.ToFloat64(mibIndexRebuilds.WithLabelValues(rebuildOutcomeError)) - failuresBefore; got < 1 {
		t.Errorf("failed rebuilds rose by %v, want at least 1", got)
	}
}

// TestConcurrentPollAndRefreshIsRaceFree is the test that decides whether the
// design is sound: a rebuild happens while devices are polling, because that is
// exactly when modules are loaded on demand. Under -race this fails if the
// published index is ever mutated in place instead of replaced.
//
// It exercises every reader of the index at once -- naming, index labels,
// friendly-name caching, info metrics and the OID learner's export -- against a
// writer that republishes continuously.
func TestConcurrentPollAndRefreshIsRaceFree(t *testing.T) {
	store := newNamingTestStore(t)
	cache := NewTableIndexCache(store)
	learner := NewOIDLearner(store)

	results := []*snmp.Result{
		counterResult(".1.3.6.1.2.1.2.2.1.10.5", 1),
		counterResult(memoryPoolUsedInstance, 2),
		{
			OID:       ".1.3.6.1.4.1.9.9.48.1.1.1.2.1",
			Type:      gosnmp.OctetString,
			Value:     []byte("Processor"),
			Timestamp: time.Unix(1700000000, 0),
		},
	}

	const (
		pollers = 8
		rounds  = 40
	)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	for range pollers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				ConvertSNMPResultsWithTableIndexes("core-01", results, nil, cache, learner)
				ConvertStatusColumnsToInfoMetrics("core-01", results, nil, cache, time.Unix(1700000000, 0))
				cache.ExtractIndexLabels("core-01", memoryPoolUsedInstance)
			}
		}()
	}

	// The writer loads a module halfway through, so the readers span both the
	// stale and the refreshed index.
	for round := range rounds {
		if round == rounds/2 {
			storeObjects(t, store, lateModuleObjects())
		}
		if err := cache.Refresh(); err != nil {
			t.Errorf("Refresh() in round %d: %v", round, err)
			break
		}
	}

	close(stop)
	wg.Wait()

	name, found := cache.objectNames().metricNameFor(memoryPoolUsedInstance)
	if !found || name != "snmp_ciscoMemoryPoolUsed" {
		t.Errorf("after concurrent refreshes metricNameFor() = %q, %v; want %q, true",
			name, found, "snmp_ciscoMemoryPoolUsed")
	}
}

// TestLookupPathAllocatesNothing pins the property the name index exists for.
// Naming runs once per sample per poll -- tens of thousands of times a minute
// on the live install -- so an allocation on this path is a garbage collection
// bill paid forever.
func TestLookupPathAllocatesNothing(t *testing.T) {
	cache := NewTableIndexCache(newNamingTestStore(t))
	storeObjects(t, cache.storage, lateModuleObjects())
	if err := cache.Refresh(); err != nil {
		t.Fatalf("Refresh(): %v", err)
	}

	// Prime the table memo, which is populated on first use by design.
	cache.index().tableFor(memoryPoolUsedInstance)

	tests := []struct {
		name   string
		lookup func()
	}{
		{
			name: "metric name of a scalar",
			lookup: func() {
				cache.objectNames().metricNameFor("1.3.6.1.2.1.2.1.0")
			},
		},
		{
			name: "metric name of a table column instance",
			lookup: func() {
				cache.objectNames().metricNameFor(memoryPoolUsedInstance)
			},
		},
		{
			name: "table of a column instance",
			lookup: func() {
				cache.index().tableFor(memoryPoolUsedInstance)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if allocs := testing.AllocsPerRun(200, tt.lookup); allocs != 0 {
				t.Errorf("lookup allocated %v times per run, want 0", allocs)
			}
		})
	}
}

// TestLearnedTablesSurviveARefresh states the rule that keeps the two sources
// of table metadata from destroying each other. Tables the OID learner
// discovered are not in the MIB store, so a rebuild that took only what it
// swept would drop them, and the vendor OIDs they label would lose their
// labels on the first module load after they were learned.
func TestLearnedTablesSurviveARefresh(t *testing.T) {
	store := newNamingTestStore(t)
	cache := NewTableIndexCache(store)

	const learnedEntry = ".1.3.6.1.4.1.9.9.9999.1.1"
	added := cache.addLearnedTables([]learnedTable{{
		info: &TableIndexInfo{
			EntryOID:   learnedEntry,
			TableOID:   ".1.3.6.1.4.1.9.9.9999.1",
			IndexNames: []string{"vendorIndex"},
			IndexTypes: []string{"Integer32"},
			IndexOIDs:  make([]string, 1),
		},
		columns: []string{learnedEntry + ".2"},
	}})
	if added != 1 {
		t.Fatalf("addLearnedTables() = %d, want 1", added)
	}

	labels := cache.ExtractIndexLabels("core-01", learnedEntry+".2.7")
	if labels["vendorindex"] != "7" {
		t.Fatalf("before refresh vendorindex = %q, want %q (labels: %v)",
			labels["vendorindex"], "7", labels)
	}

	storeObjects(t, store, lateModuleObjects())
	if err := cache.Refresh(); err != nil {
		t.Fatalf("Refresh(): %v", err)
	}

	labels = cache.ExtractIndexLabels("core-01", learnedEntry+".2.7")
	if labels["vendorindex"] != "7" {
		t.Errorf("after refresh vendorindex = %q, want %q (labels: %v)",
			labels["vendorindex"], "7", labels)
	}
}

// TestAddLearnedTablesRejectsWhatItMustNotOverwrite covers the bad inputs: an
// empty batch, a nil cache, and a table the MIBs already describe. The last
// matters most -- a learned table carries guessed index types, so letting it
// displace a MIB definition would replace good metadata with worse.
func TestAddLearnedTablesRejectsWhatItMustNotOverwrite(t *testing.T) {
	store := newNamingTestStore(t)
	storeObjects(t, store, lateModuleObjects())
	cache := NewTableIndexCache(store)

	if added := cache.addLearnedTables(nil); added != 0 {
		t.Errorf("addLearnedTables(nil) = %d, want 0", added)
	}

	var nilCache *TableIndexCache
	if added := nilCache.addLearnedTables([]learnedTable{{info: &TableIndexInfo{EntryOID: ".1.2.3"}}}); added != 0 {
		t.Errorf("addLearnedTables() on a nil cache = %d, want 0", added)
	}

	const mibEntry = "1.3.6.1.4.1.9.9.48.1.1.1"
	clash := []learnedTable{{
		info: &TableIndexInfo{
			EntryOID:   mibEntry,
			IndexNames: []string{"guessedIndex"},
			IndexTypes: []string{"Integer32"},
			IndexOIDs:  make([]string, 1),
		},
	}}
	if added := cache.addLearnedTables(clash); added != 0 {
		t.Errorf("addLearnedTables() over a MIB-described table = %d, want 0", added)
	}

	labels := cache.ExtractIndexLabels("core-01", memoryPoolUsedInstance)
	if _, guessed := labels["guessedindex"]; guessed {
		t.Errorf("a learned table displaced the MIB definition; labels: %v", labels)
	}
	if labels["ciscomemorypooltype"] != "1" {
		t.Errorf("ciscomemorypooltype = %q, want %q", labels["ciscomemorypooltype"], "1")
	}
}

// TestIndexOfAnUnbuiltCacheResolvesNothing covers the degraded constructions
// the exporter really produces: a nil cache when no MIB storage was configured,
// and a cache whose first sweep failed. Both must answer "nothing" rather than
// panic, since the poll path calls into them unconditionally.
func TestIndexOfAnUnbuiltCacheResolvesNothing(t *testing.T) {
	tests := []struct {
		name  string
		cache *TableIndexCache
	}{
		{name: "nil cache", cache: nil},
		{name: "cache with no storage", cache: NewTableIndexCache(nil)},
		{name: "zero-value cache", cache: &TableIndexCache{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			index := tt.cache.index()
			if index == nil {
				t.Fatal("index() returned nil")
			}
			if index.objectCount() != 0 {
				t.Errorf("objectCount() = %d, want 0", index.objectCount())
			}
			if table := index.tableFor(memoryPoolUsedInstance); table != nil {
				t.Errorf("tableFor() = %v, want nil", table)
			}
			if cols := index.statusColumnsFor(nil); cols != nil {
				t.Errorf("statusColumnsFor(nil) = %v, want nil", cols)
			}
		})
	}
}

// TestTableSetRejectsDuplicates covers the merge rule tableSet enforces: the
// first definition of a table wins, so a rebuild cannot silently swap the index
// structure of a table that two sources both describe.
func TestTableSetRejectsDuplicates(t *testing.T) {
	set := newTableSet()

	first := &TableIndexInfo{EntryOID: "1.3.6.1.2.1.2.2.1", IndexNames: []string{"ifIndex"}}
	if !set.add(first, []string{"1.3.6.1.2.1.2.2.1.10"}) {
		t.Fatal("add() of a new table returned false")
	}
	if first.EntryOID != ".1.3.6.1.2.1.2.2.1" {
		t.Errorf("add() left EntryOID = %q, want it normalized", first.EntryOID)
	}
	if got := set.columnToTable[".1.3.6.1.2.1.2.2.1.10"]; got != ".1.3.6.1.2.1.2.2.1" {
		t.Errorf("columnToTable entry = %q, want the normalized entry OID", got)
	}

	second := &TableIndexInfo{EntryOID: ".1.3.6.1.2.1.2.2.1", IndexNames: []string{"wrongIndex"}}
	if set.add(second, nil) {
		t.Error("add() of a table already in the set returned true")
	}
	if got := set.tables[".1.3.6.1.2.1.2.2.1"].IndexNames[0]; got != "ifIndex" {
		t.Errorf("duplicate add overwrote the table; IndexNames[0] = %q", got)
	}

	if !set.has("1.3.6.1.2.1.2.2.1") || !set.has(".1.3.6.1.2.1.2.2.1") {
		t.Error("has() does not match the table regardless of leading dot")
	}
	if set.has(".1.3.6.1.2.1.99.99.1") {
		t.Error("has() matched a table that was never added")
	}

	var missing *tableSet
	if missing.has(".1.3.6.1.2.1.2.2.1") {
		t.Error("has() on a nil set returned true")
	}
}

// BenchmarkTableIndexCacheRefresh measures a rebuild. It matters because the
// rebuild is no longer a startup cost: it runs whenever a device's modules are
// loaded on demand, so its shape in the size of the catalog is what decides
// whether hydrating a device is felt.
func BenchmarkTableIndexCacheRefresh(b *testing.B) {
	store, err := storage.New(b.TempDir() + "/bench.db")
	if err != nil {
		b.Fatalf("create storage: %v", err)
	}
	b.Cleanup(func() { _ = store.Close() })

	// 300 tables of 20 columns, the shape of a loaded vendor catalog.
	const (
		tables  = 300
		columns = 20
	)
	for table := range tables {
		entryOID := fmt.Sprintf("1.3.6.1.4.1.9.9.%d.1.1", table)
		objects := []*mib.OIDMetadata{{
			OID: entryOID, Name: fmt.Sprintf("benchEntry%d", table), Module: "BENCH-MIB",
			IsTableEntry: true, Parent: fmt.Sprintf("1.3.6.1.4.1.9.9.%d.1", table),
			Index: []string{fmt.Sprintf("benchIndex%d", table)},
			IndexColumns: []mib.IndexColumn{{
				Name: fmt.Sprintf("benchIndex%d", table), OID: entryOID + ".1",
				Type: "Integer32", BaseType: "Integer32",
			}},
		}}
		for column := 1; column <= columns; column++ {
			class := mib.ObjectClassMetricColumn
			if column == 2 {
				class = mib.ObjectClassStatusColumn
			}
			objects = append(objects, &mib.OIDMetadata{
				OID:    fmt.Sprintf("%s.%d", entryOID, column),
				Name:   fmt.Sprintf("benchColumn%d_%d", table, column),
				Module: "BENCH-MIB", Type: "Counter32", IsColumn: true,
				Parent: entryOID, ObjectClass: class,
			})
		}
		for _, object := range objects {
			if err := store.StoreOID(object); err != nil {
				b.Fatalf("store %s: %v", object.Name, err)
			}
		}
	}

	cache := NewTableIndexCache(store)

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := cache.Refresh(); err != nil {
			b.Fatalf("Refresh(): %v", err)
		}
	}
}
