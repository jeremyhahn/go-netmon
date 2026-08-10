package metrics

import (
	"fmt"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/mib/storage"
	"github.com/jeremyhahn/go-netmon/internal/snmp"
)

// newNamingTestCache builds a cache over a MIB store holding one scalar, one
// single-index table and one two-index table, which is everything the metric
// naming rules have to distinguish.
func newNamingTestCache(t *testing.T) *TableIndexCache {
	t.Helper()
	return NewTableIndexCache(newNamingTestStore(t))
}

// newNamingTestStore populates a MIB store with the naming fixture.
func newNamingTestStore(t *testing.T) *storage.Storage {
	t.Helper()

	store, err := storage.New(t.TempDir() + "/naming.db")
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close storage: %v", err)
		}
	})

	objects := []*mib.OIDMetadata{
		// Scalar: ifNumber.
		{OID: "1.3.6.1.2.1.2.1", Name: "ifNumber", Module: "IF-MIB", Type: "Integer32", IsScalar: true},

		// Single-index table: ifTable.
		{OID: "1.3.6.1.2.1.2.2", Name: "ifTable", Module: "IF-MIB", IsTable: true, Parent: "1.3.6.1.2.1.2"},
		{OID: "1.3.6.1.2.1.2.2.1", Name: "ifEntry", Module: "IF-MIB", IsTableEntry: true,
			Parent: "1.3.6.1.2.1.2.2", Index: []string{"ifIndex"}},
		{OID: "1.3.6.1.2.1.2.2.1.1", Name: "ifIndex", Module: "IF-MIB", Type: "Integer32",
			IsColumn: true, Parent: "1.3.6.1.2.1.2.2.1", ObjectClass: mib.ObjectClassIndexColumn},
		{OID: "1.3.6.1.2.1.2.2.1.10", Name: "ifInOctets", Module: "IF-MIB", Type: "Counter32",
			IsColumn: true, Parent: "1.3.6.1.2.1.2.2.1", ObjectClass: mib.ObjectClassMetricColumn},

		// Two-index table: CISCO-PROCESS-MIB, indexed by CPU and process ID.
		{OID: "1.3.6.1.4.1.9.9.109.1.1.1.1.2", Name: "cpmCPUTotalIndex", Module: "CISCO-PROCESS-MIB",
			Type: "Integer32", IsColumn: true, ObjectClass: mib.ObjectClassIndexColumn},
		{OID: "1.3.6.1.4.1.9.9.109.1.2.3.1.1", Name: "cpmProcessPID", Module: "CISCO-PROCESS-MIB",
			Type: "Integer32", IsColumn: true, Parent: "1.3.6.1.4.1.9.9.109.1.2.3.1",
			ObjectClass: mib.ObjectClassIndexColumn},
		{OID: "1.3.6.1.4.1.9.9.109.1.2.3.1", Name: "cpmProcessExtRevEntry", Module: "CISCO-PROCESS-MIB",
			IsTableEntry: true, Parent: "1.3.6.1.4.1.9.9.109.1.2.3",
			Index: []string{"cpmCPUTotalIndex", "cpmProcessPID"}},
		{OID: "1.3.6.1.4.1.9.9.109.1.2.3.1.17", Name: "cpmProcessMemoryCore", Module: "CISCO-PROCESS-MIB",
			Type: "Gauge32", IsColumn: true, Parent: "1.3.6.1.4.1.9.9.109.1.2.3.1",
			ObjectClass: mib.ObjectClassMetricColumn},
	}

	for _, object := range objects {
		if err := store.StoreOID(object); err != nil {
			t.Fatalf("store %s: %v", object.Name, err)
		}
	}

	return store
}

func counterResult(oid string, value uint) *snmp.Result {
	return &snmp.Result{
		OID:       oid,
		Type:      gosnmp.Counter32,
		Value:     value,
		Timestamp: time.Unix(1700000000, 0),
	}
}

// sampleByName returns the single sample carrying name, failing if there is
// not exactly one.
func sampleByName(t *testing.T, samples []*Sample, name string) *Sample {
	t.Helper()

	var found *Sample
	for _, sample := range samples {
		if sample.Name != name {
			continue
		}
		if found != nil {
			t.Fatalf("expected exactly one %q sample, got at least two", name)
		}
		found = sample
	}
	if found == nil {
		names := make([]string, 0, len(samples))
		for _, sample := range samples {
			names = append(names, sample.Name)
		}
		t.Fatalf("no sample named %q; got %v", name, names)
	}
	return found
}

// TestConvertNamesMetricsAfterMIBObject is the core of the naming contract:
// the metric name comes from the MIB object, and every instance
// sub-identifier appears only in labels.
func TestConvertNamesMetricsAfterMIBObject(t *testing.T) {
	cache := newNamingTestCache(t)

	tests := []struct {
		name       string
		oid        string
		wantName   string
		wantLabels map[string]string
	}{
		{
			name:       "scalar instance",
			oid:        ".1.3.6.1.2.1.2.1.0",
			wantName:   "snmp_ifNumber",
			wantLabels: map[string]string{"device": "core-01", "oid": ".1.3.6.1.2.1.2.1.0"},
		},
		{
			name:     "single-index table row",
			oid:      ".1.3.6.1.2.1.2.2.1.10.5",
			wantName: "snmp_ifInOctets",
			wantLabels: map[string]string{
				"device":  "core-01",
				"oid":     ".1.3.6.1.2.1.2.2.1.10.5",
				"ifindex": "5",
			},
		},
		{
			name:     "two-index table row",
			oid:      ".1.3.6.1.4.1.9.9.109.1.2.3.1.17.7.5420",
			wantName: "snmp_cpmProcessMemoryCore",
			wantLabels: map[string]string{
				"device":           "core-01",
				"oid":              ".1.3.6.1.4.1.9.9.109.1.2.3.1.17.7.5420",
				"cpmcputotalindex": "7",
				"cpmprocesspid":    "5420",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			samples := ConvertSNMPResultsWithTableIndexes(
				"core-01", []*snmp.Result{counterResult(tt.oid, 42)}, nil, cache, nil)

			if len(samples) != 1 {
				t.Fatalf("expected 1 sample, got %d: %+v", len(samples), samples)
			}
			sample := samples[0]

			if sample.Name != tt.wantName {
				t.Errorf("metric name = %q, want %q", sample.Name, tt.wantName)
			}
			if len(sample.Labels) != len(tt.wantLabels) {
				t.Errorf("labels = %v, want %v", sample.Labels, tt.wantLabels)
			}
			for key, want := range tt.wantLabels {
				if got := sample.Labels[key]; got != want {
					t.Errorf("label %q = %q, want %q", key, got, want)
				}
			}
			if sample.Value != 42 {
				t.Errorf("value = %v, want 42", sample.Value)
			}
		})
	}
}

// TestConvertSharesOneMetricNameAcrossRows is the regression this change
// exists for: two rows of one column used to become two metric names, which
// is what put 36,654 distinct names into Prometheus for two routers.
func TestConvertSharesOneMetricNameAcrossRows(t *testing.T) {
	cache := newNamingTestCache(t)

	results := []*snmp.Result{
		counterResult(".1.3.6.1.4.1.9.9.109.1.2.3.1.17.7.5420", 100),
		counterResult(".1.3.6.1.4.1.9.9.109.1.2.3.1.17.7.20178", 200),
		counterResult(".1.3.6.1.2.1.2.2.1.10.1", 300),
		counterResult(".1.3.6.1.2.1.2.2.1.10.2", 400),
	}

	samples := ConvertSNMPResultsWithTableIndexes("core-01", results, nil, cache, nil)
	if len(samples) != len(results) {
		t.Fatalf("expected %d samples, got %d", len(results), len(samples))
	}

	byName := make(map[string][]*Sample)
	for _, sample := range samples {
		byName[sample.Name] = append(byName[sample.Name], sample)
	}
	if len(byName) != 2 {
		t.Fatalf("expected 2 distinct metric names for 4 rows, got %d: %v", len(byName), byName)
	}

	process := byName["snmp_cpmProcessMemoryCore"]
	if len(process) != 2 {
		t.Fatalf("expected 2 snmp_cpmProcessMemoryCore samples, got %d", len(process))
	}
	if process[0].Labels["cpmprocesspid"] == process[1].Labels["cpmprocesspid"] {
		t.Errorf("rows must be distinguished by labels, both have cpmprocesspid=%q",
			process[0].Labels["cpmprocesspid"])
	}

	interfaces := byName["snmp_ifInOctets"]
	if len(interfaces) != 2 {
		t.Fatalf("expected 2 snmp_ifInOctets samples, got %d", len(interfaces))
	}
	if interfaces[0].Labels["ifindex"] == interfaces[1].Labels["ifindex"] {
		t.Errorf("rows must be distinguished by labels, both have ifindex=%q",
			interfaces[0].Labels["ifindex"])
	}
}

// TestConvertLongestPrefixWins covers an object OID that is a strict prefix
// of another object OID: the instance must resolve to the more specific one.
func TestConvertLongestPrefixWins(t *testing.T) {
	store, err := storage.New(t.TempDir() + "/prefix.db")
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Errorf("close storage: %v", err)
		}
	}()

	for _, object := range []*mib.OIDMetadata{
		{OID: "1.3.6.1.4.1.99.1", Name: "outerScalar", Module: "TEST-MIB", Type: "Integer32", IsScalar: true},
		{OID: "1.3.6.1.4.1.99.1.2", Name: "innerScalar", Module: "TEST-MIB", Type: "Integer32", IsScalar: true},
	} {
		if err := store.StoreOID(object); err != nil {
			t.Fatalf("store %s: %v", object.Name, err)
		}
	}

	cache := NewTableIndexCache(store)
	results := []*snmp.Result{
		counterResult(".1.3.6.1.4.1.99.1.0", 1),
		counterResult(".1.3.6.1.4.1.99.1.2.0", 2),
	}

	samples := ConvertSNMPResultsWithTableIndexes("core-01", results, nil, cache, nil)
	if len(samples) != 2 {
		t.Fatalf("expected 2 samples, got %d", len(samples))
	}

	if outer := sampleByName(t, samples, "snmp_outerScalar"); outer.Value != 1 {
		t.Errorf("snmp_outerScalar value = %v, want 1", outer.Value)
	}
	if inner := sampleByName(t, samples, "snmp_innerScalar"); inner.Value != 2 {
		t.Errorf("snmp_innerScalar value = %v, want 2", inner.Value)
	}
}

// TestConvertUnresolvedOIDKeepsRawNameAndCounts covers the only permitted
// fallback: an OID no loaded MIB describes keeps its raw-OID name so the
// sample is not lost, and is counted so the gap is visible.
func TestConvertUnresolvedOIDKeepsRawNameAndCounts(t *testing.T) {
	cache := newNamingTestCache(t)

	const device = "unresolved-device"
	before := unresolvedSamplesFor(t, device)

	results := []*snmp.Result{
		counterResult(".1.3.6.1.4.1.99999.7.1.0", 1),
		counterResult(".1.3.6.1.4.1.99999.7.2.1.4.9", 2),
		counterResult(".1.3.6.1.2.1.2.2.1.10.5", 3), // resolvable, must not be counted
	}

	samples := ConvertSNMPResultsWithTableIndexes(device, results, nil, cache, nil)
	if len(samples) != 3 {
		t.Fatalf("expected 3 samples, got %d", len(samples))
	}

	sampleByName(t, samples, "snmp__1_3_6_1_4_1_99999_7_1_0")
	sampleByName(t, samples, "snmp__1_3_6_1_4_1_99999_7_2_1_4_9")
	sampleByName(t, samples, "snmp_ifInOctets")

	if got := unresolvedSamplesFor(t, device) - before; got != 2 {
		t.Errorf("unresolved sample counter rose by %v, want 2", got)
	}
}

// TestConvertWithoutMIBMetadata covers the degraded constructions: no cache
// at all, and a cache built without MIB storage. Both must name every sample
// from its raw OID rather than panicking.
func TestConvertWithoutMIBMetadata(t *testing.T) {
	tests := []struct {
		name  string
		cache *TableIndexCache
	}{
		{name: "nil cache", cache: nil},
		{name: "cache with nil storage", cache: NewTableIndexCache(nil)},
		{name: "zero-value cache", cache: &TableIndexCache{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.cache != nil && tt.cache.objectNames() != nil {
				t.Errorf("expected no object name index, got one")
			}

			results := []*snmp.Result{counterResult(".1.3.6.1.2.1.2.2.1.10.5", 7)}
			samples := ConvertSNMPResultsWithTableIndexes("degraded-device", results, nil, tt.cache, nil)

			if len(samples) != 1 {
				t.Fatalf("expected 1 sample, got %d", len(samples))
			}
			if samples[0].Name != "snmp__1_3_6_1_2_1_2_2_1_10_5" {
				t.Errorf("metric name = %q, want the raw-OID name", samples[0].Name)
			}
		})
	}
}

// TestConvertEmptyResults covers a poll that returned nothing.
func TestConvertEmptyResults(t *testing.T) {
	cache := newNamingTestCache(t)

	for _, results := range [][]*snmp.Result{nil, {}} {
		samples := ConvertSNMPResultsWithTableIndexes("core-01", results, nil, cache, nil)
		if len(samples) != 0 {
			t.Errorf("expected no samples for %v results, got %d", results, len(samples))
		}
	}
}

// TestNewTableIndexCacheWithoutStorage checks the constructor tolerates a
// missing MIB store, since the exporter builds one only when MIB storage was
// configured.
func TestNewTableIndexCacheWithoutStorage(t *testing.T) {
	cache := NewTableIndexCache(nil)
	if cache == nil {
		t.Fatal("NewTableIndexCache(nil) returned nil")
	}
	if err := cache.Refresh(); err == nil {
		t.Error("Refresh() with no storage returned nil error")
	} else if _, ok := err.(*StorageUnavailableError); !ok {
		t.Errorf("Refresh() error = %T (%v), want *StorageUnavailableError", err, err)
	}
	if labels := cache.ExtractIndexLabels("core-01", ".1.3.6.1.2.1.2.2.1.10.5"); labels != nil {
		t.Errorf("ExtractIndexLabels() = %v, want nil", labels)
	}
}

// TestRefreshRebuildsNameIndex covers MIBs imported after the cache was built:
// refreshing must publish an index that resolves the new objects.
func TestRefreshRebuildsNameIndex(t *testing.T) {
	store, err := storage.New(t.TempDir() + "/reload.db")
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Errorf("close storage: %v", err)
		}
	}()

	cache := NewTableIndexCache(store)
	if _, found := cache.objectNames().metricNameFor("1.3.6.1.2.1.2.1.0"); found {
		t.Fatal("empty store resolved an OID")
	}

	imported := &mib.OIDMetadata{
		OID: "1.3.6.1.2.1.2.1", Name: "ifNumber", Module: "IF-MIB", Type: "Integer32", IsScalar: true,
	}
	if err := store.StoreOID(imported); err != nil {
		t.Fatalf("store ifNumber: %v", err)
	}

	if err := cache.Refresh(); err != nil {
		t.Fatalf("Refresh() after import: %v", err)
	}

	got, found := cache.objectNames().metricNameFor("1.3.6.1.2.1.2.1.0")
	if !found || got != "snmp_ifNumber" {
		t.Errorf("after reload metricNameFor() = %q, %v; want %q, true", got, found, "snmp_ifNumber")
	}
}

// TestConvertWithExtraLabelsSystemInfoAndLearner covers the rest of the
// conversion: caller-supplied labels reach every sample, textual system OIDs
// become the snmp_system_info metric rather than a named metric, and an OID
// learner may participate without disturbing naming.
func TestConvertWithExtraLabelsSystemInfoAndLearner(t *testing.T) {
	store := newNamingTestStore(t)
	cache := NewTableIndexCache(store)
	learner := NewOIDLearner(store)

	results := []*snmp.Result{
		counterResult(".1.3.6.1.2.1.2.2.1.10.5", 11),
		{
			OID:       "1.3.6.1.2.1.1.1.0",
			Type:      gosnmp.OctetString,
			Value:     []byte("Cisco IOS XE"),
			Timestamp: time.Unix(1700000000, 0),
		},
		{
			OID:       "1.3.6.1.2.1.1.5.0",
			Type:      gosnmp.OctetString,
			Value:     []byte("core-01"),
			Timestamp: time.Unix(1700000000, 0),
		},
	}
	extra := map[string]string{"site": "dc1"}

	samples := ConvertSNMPResultsWithTableIndexes("core-01", results, extra, cache, learner)

	counter := sampleByName(t, samples, "snmp_ifInOctets")
	if counter.Labels["site"] != "dc1" {
		t.Errorf("extra label site = %q, want %q", counter.Labels["site"], "dc1")
	}

	info := sampleByName(t, samples, "snmp_system_info")
	if info.Labels["sys_descr"] != "Cisco IOS XE" {
		t.Errorf("sys_descr = %q, want %q", info.Labels["sys_descr"], "Cisco IOS XE")
	}
	if info.Labels["sys_name"] != "core-01" {
		t.Errorf("sys_name = %q, want %q", info.Labels["sys_name"], "core-01")
	}
	if info.Labels["site"] != "dc1" {
		t.Errorf("extra label site = %q, want %q", info.Labels["site"], "dc1")
	}
	if info.Value != 1.0 {
		t.Errorf("snmp_system_info value = %v, want 1", info.Value)
	}
}

// BenchmarkConvertSNMPResultsWithTableIndexes measures the whole conversion
// path, naming included, over a poll-sized batch of table rows.
func BenchmarkConvertSNMPResultsWithTableIndexes(b *testing.B) {
	cache := newTestCache(nil, nil)
	cache.publishIndex(&mibSweep{names: benchmarkIndex(), tables: newTableSet()})

	results := make([]*snmp.Result, 0, 5000)
	for i := 0; i < 5000; i++ {
		results = append(results, &snmp.Result{
			OID:       fmt.Sprintf(".1.3.6.1.4.1.9.9.%d.1.1.1.%d.%d", i%400, i%40+1, i),
			Type:      gosnmp.Counter32,
			Value:     uint(i),
			Timestamp: time.Unix(1700000000, 0),
		})
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ConvertSNMPResultsWithTableIndexes("bench-device", results, nil, cache, nil)
	}
}
