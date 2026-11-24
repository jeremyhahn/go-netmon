package metrics

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/jeremyhahn/go-netmon/internal/mib"
)

// captureExporter records catalog deliveries; it implements catalogSink only.
type captureExporter struct {
	mu      sync.Mutex
	batches [][]*Sample
	err     error
}

func (c *captureExporter) SendCatalog(_ context.Context, samples []*Sample) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	c.batches = append(c.batches, samples)
	return nil
}

func (c *captureExporter) samples() []*Sample {
	c.mu.Lock()
	defer c.mu.Unlock()
	var all []*Sample
	for _, batch := range c.batches {
		all = append(all, batch...)
	}
	return all
}

// catalogTestCache returns an index cache whose catalog describes objects.
func catalogTestCache(t *testing.T, objects []*mib.OIDMetadata) *TableIndexCache {
	t.Helper()

	cache := &TableIndexCache{}
	cache.publishIndex(&mibSweep{
		names:         newObjectNameIndex(objects),
		tables:        newTableSet(),
		statusColumns: make(map[string][]statusColumnInfo),
		catalog:       newCatalogIndex(objects, newTableSet(), nil),
	})
	return cache
}

// TestPublishedMetricsRecordsWhatWasSent checks the set the catalog describes.
func TestPublishedMetricsRecordsWhatWasSent(t *testing.T) {
	published := newPublishedMetrics()

	published.record([]*Sample{
		{Name: "snmp_ifInOctets"},
		{Name: "snmp_ifOutOctets"},
		{Name: "snmp_ifInOctets"},
		nil,
	})

	names := published.snapshot()
	if len(names) != 2 {
		t.Fatalf("snapshot = %v, want two distinct names", names)
	}
	// Sorted, so two emissions of an unchanged fleet render identically.
	if names[0] != "snmp_ifInOctets" || names[1] != "snmp_ifOutOctets" {
		t.Errorf("snapshot = %v, want sorted", names)
	}
}

// TestPublishedMetricsIsBoundedByMetricsNotDevices is the scaling invariant.
//
// The same metric published by a hundred thousand devices is one entry. If
// this set grew per device it would be the same bug as a device label on the
// catalog, one layer down.
func TestPublishedMetricsIsBoundedByMetricsNotDevices(t *testing.T) {
	published := newPublishedMetrics()

	const devices = 10000
	for i := 0; i < devices; i++ {
		// Every device publishes the same three metrics, with different
		// label values -- which is what a fleet does.
		published.record([]*Sample{
			{Name: "snmp_ifInOctets", Labels: map[string]string{labelDevice: "d" + strconv.Itoa(i)}},
			{Name: "snmp_ifOutOctets", Labels: map[string]string{labelDevice: "d" + strconv.Itoa(i)}},
			{Name: "snmp_entity_sensor_dbm", Labels: map[string]string{labelDevice: "d" + strconv.Itoa(i)}},
		})
	}

	if got := published.len(); got != 3 {
		t.Errorf("published set holds %d names after %d devices, want 3", got, devices)
	}
}

// TestPublishedMetricsIsSafeUnderConcurrentPolls checks the write path, which
// every device's poll goes through at once.
func TestPublishedMetricsIsSafeUnderConcurrentPolls(t *testing.T) {
	published := newPublishedMetrics()

	var start, done sync.WaitGroup
	start.Add(1)
	for i := 0; i < 32; i++ {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			start.Wait()
			published.record([]*Sample{
				{Name: "snmp_shared"},
				{Name: "snmp_own_" + strconv.Itoa(i)},
			})
		}(i)
	}
	start.Done()
	done.Wait()

	if got := published.len(); got != 33 {
		t.Errorf("published set holds %d names, want 33", got)
	}
}

// TestCatalogDescribesFamiliesThisProcessNeverPublished checks the emitter describes the whole fleet's families.
func TestCatalogDescribesFamiliesThisProcessNeverPublished(t *testing.T) {
	cache := catalogTestCache(t, []*mib.OIDMetadata{
		{OID: ".1.3.6.1.2.1.2.2.1.10", Name: "ifInOctets", Module: "IF-MIB", Type: "Counter32", IsColumn: true},
		{OID: ".1.3.6.1.2.1.2.2.1.16", Name: "ifOutOctets", Module: "IF-MIB", Type: "Counter32", IsColumn: true},
		{OID: ".1.3.6.1.2.1.1.1", Name: "sysDescr", Module: "SNMPv2-MIB", Type: "DisplayString", IsScalar: true},
	})

	// This shard polls exactly one of them. Another shard polls the rest.
	published := newPublishedMetrics()
	published.record([]*Sample{{Name: "snmp_ifInOctets"}})

	exporter := &captureExporter{}
	emitter := newCatalogEmitter(exporter, cache, published)
	emitter.now = func() time.Time { return time.Unix(1756600000, 0) }

	emitter.emit(context.Background())

	got := catalogedMetrics(exporter.samples())
	for _, want := range []string{"snmp_ifInOctets", "snmp_ifOutOctets"} {
		if !got[want] {
			t.Errorf("catalog has no row for %s; a shard that does not poll a "+
				"family must still describe it, or the fleet cannot render it", want)
		}
	}
}

// catalogedMetrics indexes an emission by the metric each row describes.
func catalogedMetrics(samples []*Sample) map[string]bool {
	described := make(map[string]bool, len(samples))
	for _, s := range samples {
		if m := s.Labels[labelCatalogMetric]; m != "" {
			described[m] = true
		}
	}
	return described
}

// TestCatalogEmitterDescribesDerivedMetrics checks that a metric no MIB
// describes still gets a row -- which is the case that matters, since optical
// power is published under a name derived from its unit.
func TestCatalogEmitterDescribesDerivedMetrics(t *testing.T) {
	cache := catalogTestCache(t, nil)

	published := newPublishedMetrics()
	published.record([]*Sample{{Name: "snmp_entity_sensor_dbm"}})

	exporter := &captureExporter{}
	emitter := newCatalogEmitter(exporter, cache, published)
	emitter.emit(context.Background())

	// The emission now covers the whole corpus plus every derived family, so
	// find the dBm row rather than assuming it is the only one.
	var dbm *Sample
	for _, s := range exporter.samples() {
		if s.Labels[labelCatalogMetric] == "snmp_entity_sensor_dbm" {
			dbm = s
			break
		}
	}
	if dbm == nil {
		t.Fatal("no catalog row for snmp_entity_sensor_dbm")
	}
	if got := dbm.Labels[labelCatalogUnit]; got != "dBm" {
		t.Errorf("unit = %q, want dBm", got)
	}
	if got := dbm.Labels[labelCatalogIdentity]; got != labelEntityName {
		t.Errorf("identity label = %q, want %q", got, labelEntityName)
	}
}

// TestCatalogEmitterCountsWhatItCannotDescribe checks undescribed metrics are counted.
func TestCatalogEmitterCountsWhatItCannotDescribe(t *testing.T) {
	cache := catalogTestCache(t, nil)

	published := newPublishedMetrics()
	published.record([]*Sample{{Name: "snmp_1_3_6_1_4_1_9999_1_1"}})

	exporter := &captureExporter{}
	emitter := newCatalogEmitter(exporter, cache, published)

	_, stats := emitter.render()
	if stats.undescribed != 1 {
		t.Errorf("undescribed = %d, want 1", stats.undescribed)
	}

	emitter.emit(context.Background())
	if catalogedMetrics(exporter.samples())["snmp_1_3_6_1_4_1_9999_1_1"] {
		t.Error("emitted a catalog row for a raw-OID name nothing describes")
	}
}

// TestCatalogEmitterCountsUnknownKinds checks the diagnostic surfacing for
// objects whose MIB module parsed with unresolved imports.
func TestCatalogEmitterCountsUnknownKinds(t *testing.T) {
	cache := catalogTestCache(t, []*mib.OIDMetadata{
		{OID: ".1.3.6.1.2.1.37.1.6.1.1.2", Name: "atmVplOperStatus", Module: "ATM-MIB", IsColumn: true},
	})

	published := newPublishedMetrics()
	published.record([]*Sample{{Name: "snmp_atmVplOperStatus"}})

	emitter := newCatalogEmitter(&captureExporter{}, cache, published)
	samples, stats := emitter.render()

	if stats.unknownKind != 1 {
		t.Errorf("unknownKind = %d, want 1", stats.unknownKind)
	}
	// It is still published: an operator needs to see the metric exists and
	// that its kind is unknown, not have it disappear.
	var atm *Sample
	for _, s := range samples {
		if s.Labels[labelCatalogMetric] == "snmp_atmVplOperStatus" {
			atm = s
			break
		}
	}
	if atm == nil || atm.Labels[labelCatalogKind] != string(MetricKindUnknown) {
		t.Fatalf("atmVplOperStatus row = %+v, want kind=unknown", atm)
	}
}

// TestCatalogEmitsTheCorpusBeforeAnyPoll checks the catalog is published at load time.
func TestCatalogEmitsTheCorpusBeforeAnyPoll(t *testing.T) {
	cache := catalogTestCache(t, []*mib.OIDMetadata{
		{OID: ".1.3.6.1.2.1.2.2.1.10", Name: "ifInOctets", Module: "IF-MIB", Type: "Counter32", IsColumn: true},
	})

	// Nothing polled, nothing published.
	emitter := newCatalogEmitter(&captureExporter{}, cache, newPublishedMetrics())

	samples, _ := emitter.render()
	if !catalogedMetrics(samples)["snmp_ifInOctets"] {
		t.Error("no catalog row before the first poll; a restarted emitter would " +
			"blank every dashboard until a device answered")
	}
}

// TestCatalogEmitterStartsImmediatelyAndStops checks the lifecycle: a restart
// must not leave consumers without a catalog for a whole interval, and Stop
// has to be safe to call more than once.
func TestCatalogEmitterStartsImmediatelyAndStops(t *testing.T) {
	cache := catalogTestCache(t, []*mib.OIDMetadata{
		{OID: ".1.3.6.1.2.1.2.2.1.10", Name: "ifInOctets", Module: "IF-MIB", Type: "Counter32", IsColumn: true},
	})
	published := newPublishedMetrics()
	published.record([]*Sample{{Name: "snmp_ifInOctets"}})

	exporter := &captureExporter{}
	emitter := newCatalogEmitter(exporter, cache, published)
	// Long enough that only the immediate emission can happen.
	emitter.interval = time.Hour

	emitter.Start(context.Background())

	deadline := time.Now().Add(2 * time.Second)
	for len(exporter.samples()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	emitter.Stop()
	emitter.Stop() // must be idempotent

	if len(exporter.samples()) == 0 {
		t.Error("the emitter published nothing on start; a restart would leave consumers without a catalog")
	}
}

// TestCatalogEmitterStopsWhenTheContextIsCancelled checks the other exit.
func TestCatalogEmitterStopsWhenTheContextIsCancelled(t *testing.T) {
	emitter := newCatalogEmitter(&captureExporter{}, catalogTestCache(t, nil), newPublishedMetrics())
	emitter.interval = time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	emitter.Start(ctx)
	cancel()

	// Waited on directly rather than through Stop, which would also close
	// the stop channel: with both ready, select picks either, and the test
	// would only sometimes exercise cancellation.
	done := make(chan struct{})
	go func() {
		emitter.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the emitter did not stop when its context was canceled")
	}

	// Stop must still be safe afterwards, since a caller does not know which
	// of the two ended it.
	emitter.Stop()
}

// TestCatalogEmitterIsIndependentOfDeviceCount is the property the whole
// design rests on: emitting the catalog costs the same at three devices as at
// thirty thousand, because it is written once and carries no device dimension.
func TestCatalogEmitterIsIndependentOfDeviceCount(t *testing.T) {
	cache := catalogTestCache(t, []*mib.OIDMetadata{
		{OID: ".1.3.6.1.2.1.2.2.1.10", Name: "ifInOctets", Module: "IF-MIB", Type: "Counter32", IsColumn: true},
	})

	sizeAfter := func(devices int) int {
		published := newPublishedMetrics()
		for i := 0; i < devices; i++ {
			published.record([]*Sample{
				{Name: "snmp_ifInOctets", Labels: map[string]string{labelDevice: "d" + strconv.Itoa(i)}},
			})
		}

		exporter := &captureExporter{}
		emitter := newCatalogEmitter(exporter, cache, published)
		emitter.emit(context.Background())
		return len(exporter.samples())
	}

	small, large := sizeAfter(3), sizeAfter(30000)
	if small != large {
		t.Errorf("catalog is %d series at 3 devices and %d at 30000; it must not scale with the fleet",
			small, large)
	}
}

// TestCatalogEmitterReportsAFailedExport checks a failed catalog write is reported.
func TestCatalogEmitterReportsAFailedExport(t *testing.T) {
	cache := catalogTestCache(t, []*mib.OIDMetadata{
		{OID: ".1.3.6.1.2.1.2.2.1.10", Name: "ifInOctets", Module: "IF-MIB", Type: "Counter32", IsColumn: true},
	})
	published := newPublishedMetrics()
	published.record([]*Sample{{Name: "snmp_ifInOctets"}})

	failures := &captureExporter{err: errors.New("remote write refused")}
	emitter := newCatalogEmitter(failures, cache, published)

	before := testutil.ToFloat64(catalogEmissions.WithLabelValues("error"))
	emitter.emit(context.Background())
	if after := testutil.ToFloat64(catalogEmissions.WithLabelValues("error")); after != before+1 {
		t.Errorf("error emissions = %v, want %v", after, before+1)
	}
}

// TestCatalogEmitterRepublishesOnItsInterval checks the catalog is rewritten before it goes stale.
func TestCatalogEmitterRepublishesOnItsInterval(t *testing.T) {
	cache := catalogTestCache(t, []*mib.OIDMetadata{
		{OID: ".1.3.6.1.2.1.2.2.1.10", Name: "ifInOctets", Module: "IF-MIB", Type: "Counter32", IsColumn: true},
	})
	published := newPublishedMetrics()
	published.record([]*Sample{{Name: "snmp_ifInOctets"}})

	exporter := &captureExporter{}
	emitter := newCatalogEmitter(exporter, cache, published)
	emitter.interval = time.Millisecond

	emitter.Start(context.Background())
	defer emitter.Stop()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		exporter.mu.Lock()
		emissions := len(exporter.batches)
		exporter.mu.Unlock()
		if emissions >= 3 {
			return
		}
		time.Sleep(time.Millisecond)
	}

	exporter.mu.Lock()
	emissions := len(exporter.batches)
	exporter.mu.Unlock()
	t.Errorf("the emitter published %d times, want it to keep republishing", emissions)
}

// TestPublishedMetricsIgnoresNothingToRecord checks the degenerate calls,
// which every poll that produced no samples makes.
func TestPublishedMetricsIgnoresNothingToRecord(t *testing.T) {
	var nilSet *publishedMetrics
	nilSet.record([]*Sample{{Name: "snmp_x"}})
	if got := nilSet.len(); got != 0 {
		t.Errorf("a nil set recorded %d names", got)
	}
	if nilSet.snapshot() != nil {
		t.Error("a nil set returned a snapshot")
	}

	set := newPublishedMetrics()
	set.record(nil)
	set.record([]*Sample{})
	if got := set.len(); got != 0 {
		t.Errorf("recording nothing left %d names", got)
	}
}

// TestCatalogIndexEntryForDegenerateCases checks the nil index, which is what a
// collector with no MIB metadata loaded reads as.
func TestCatalogIndexEntryForDegenerateCases(t *testing.T) {
	var index *catalogIndex
	if _, found := index.entryFor("snmp_anything"); found {
		t.Error("a nil catalog index described a metric")
	}
	if _, found := emptyCatalogIndex.entryFor("snmp_anything"); found {
		t.Error("the empty catalog index described a metric")
	}
}

// TestATableWithNoNamingColumnHasNoIdentity checks a table without a name column gets no identity, independent of column order.
func TestATableWithNoNamingColumnHasNoIdentity(t *testing.T) {
	columns := []statusColumnInfo{{Name: "zzzState"}, {Name: "aaaLabel"}}

	if got := identityInfoMetric(columns); got != "" {
		t.Errorf("identityInfoMetric() = %q, want none: neither column declares "+
			"itself a name, and an arbitrary one is worse than the index", got)
	}
}

// TestIdentityElectionDoesNotDependOnColumnOrder keeps the choice identical on
// every collector given the same MIBs.
func TestIdentityElectionDoesNotDependOnColumnOrder(t *testing.T) {
	forwards := []statusColumnInfo{{Name: "ifAlias"}, {Name: "ifDescr"}, {Name: "ifName"}}
	backwards := []statusColumnInfo{{Name: "ifName"}, {Name: "ifDescr"}, {Name: "ifAlias"}}

	got, again := identityInfoMetric(forwards), identityInfoMetric(backwards)
	if got != infoMetricName("ifName") {
		t.Errorf("identityInfoMetric() = %q, want ifName's info metric", got)
	}
	if again != got {
		t.Errorf("identityInfoMetric() = %q then %q; the choice must not depend on "+
			"input order", got, again)
	}
}

// TestNewCatalogEmitterForWiresTheExportersOwnState checks the emitter uses its exporter's published set and MIBs.
func TestNewCatalogEmitterForWiresTheExportersOwnState(t *testing.T) {
	exporter, err := NewPrometheusExporter("http://127.0.0.1:1/write", 1000, time.Hour, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	t.Cleanup(func() { _ = exporter.Close() })

	emitter := NewCatalogEmitterFor(exporter)

	if emitter.published != exporter.publishedMetricNames() {
		t.Error("the emitter describes a different set of published metrics than the exporter records")
	}
	if emitter.index != exporter.TableIndexCache() {
		t.Error("the emitter reads MIB metadata from a different index than the exporter names samples with")
	}
	if emitter.interval != CatalogInterval {
		t.Errorf("interval = %s, want %s", emitter.interval, CatalogInterval)
	}
}

// TestPublishedMetricsExcludesTheCatalogsOwnFamilies checks the catalog does not count its own families.
func TestPublishedMetricsExcludesTheCatalogsOwnFamilies(t *testing.T) {
	published := newPublishedMetrics()

	published.record([]*Sample{
		{Name: catalogMetricName},
		{Name: catalogEnumMetricName},
		{Name: "snmp_ifInOctets"},
	})

	names := published.snapshot()
	if len(names) != 1 || names[0] != "snmp_ifInOctets" {
		t.Errorf("published set = %v, want only the SNMP metric; the catalog does not describe itself", names)
	}
}

// TestCatalogEmitterDoesNotDescribeItself checks the property end to end: an
// emitter whose own output is fed back through the export path must still
// report nothing undescribed.
func TestCatalogEmitterDoesNotDescribeItself(t *testing.T) {
	cache := catalogTestCache(t, []*mib.OIDMetadata{
		{OID: ".1.3.6.1.2.1.2.2.1.10", Name: "ifInOctets", Module: "IF-MIB", Type: "Counter32", IsColumn: true},
	})

	published := newPublishedMetrics()
	published.record([]*Sample{{Name: "snmp_ifInOctets"}})

	exporter := &captureExporter{}
	emitter := newCatalogEmitter(exporter, cache, published)

	// Emit, then feed the emitter's own output back the way a real export
	// path does.
	emitter.emit(context.Background())
	published.record(exporter.samples())

	_, stats := emitter.render()
	if stats.undescribed != 0 {
		t.Errorf("undescribed = %d after the catalog described a fully described fleet; "+
			"the catalog is describing itself", stats.undescribed)
	}
}

// TestPublishedMetricsEvictsWhatIsNoLongerPublished checks stale names leave the set.
func TestPublishedMetricsEvictsWhatIsNoLongerPublished(t *testing.T) {
	published := newPublishedMetrics()

	base := time.Unix(1756600000, 0)
	published.now = func() time.Time { return base }
	published.record([]*Sample{{Name: "snmp_retired"}, {Name: "snmp_current"}})

	// A day and a minute later, only one of them is still being published.
	published.now = func() time.Time { return base.Add(publishedMetricRetention + time.Minute) }
	published.record([]*Sample{{Name: "snmp_current"}})

	names := published.snapshot()
	if len(names) != 1 || names[0] != "snmp_current" {
		t.Errorf("published set = %v, want only the metric still being published", names)
	}
}

// TestPublishedMetricsKeepsSlowlyPolledMetrics checks the one-day window keeps slowly polled metrics.
func TestPublishedMetricsKeepsSlowlyPolledMetrics(t *testing.T) {
	published := newPublishedMetrics()

	base := time.Unix(1756600000, 0)
	published.now = func() time.Time { return base }
	published.record([]*Sample{{Name: "snmp_hourly"}})

	// Many emitter intervals later, but well inside the retention window.
	for elapsed := CatalogInterval; elapsed < time.Hour; elapsed += CatalogInterval {
		at := base.Add(elapsed)
		published.now = func() time.Time { return at }
		if names := published.snapshot(); len(names) != 1 {
			t.Fatalf("after %s the set is %v; a metric polled hourly was evicted between polls",
				elapsed, names)
		}
	}
}

// TestPublishedMetricsEvictionIsSelfHealing checks the cost of getting the
// window wrong is bounded: a metric that is still live comes back on its very
// next successful export, so an over-eager eviction costs a description for
// one poll rather than losing anything.
func TestPublishedMetricsEvictionIsSelfHealing(t *testing.T) {
	published := newPublishedMetrics()

	base := time.Unix(1756600000, 0)
	published.now = func() time.Time { return base }
	published.record([]*Sample{{Name: "snmp_live"}})

	later := base.Add(publishedMetricRetention + time.Hour)
	published.now = func() time.Time { return later }
	if names := published.snapshot(); len(names) != 0 {
		t.Fatalf("snapshot = %v, want the stale name evicted", names)
	}

	published.record([]*Sample{{Name: "snmp_live"}})
	if names := published.snapshot(); len(names) != 1 {
		t.Errorf("snapshot = %v, want the name back after the next export", names)
	}
}

// TestPublishedMetricsGaugeTracksTheSet checks the gauge equals the set size after every change.
func TestPublishedMetricsGaugeTracksTheSet(t *testing.T) {
	published := newPublishedMetrics()
	base := time.Unix(1756600000, 0)
	published.now = func() time.Time { return base }

	published.record([]*Sample{{Name: "snmp_a"}})
	if got := testutil.ToFloat64(catalogPublishedMetrics); got != 1 {
		t.Errorf("gauge = %v after one metric, want 1", got)
	}

	published.record([]*Sample{{Name: "snmp_b"}, {Name: "snmp_a"}})
	if got := testutil.ToFloat64(catalogPublishedMetrics); got != 2 {
		t.Errorf("gauge = %v after two distinct metrics, want 2", got)
	}

	// Eviction has to move it back down, or the gauge reports a set larger
	// than the catalog it describes.
	published.now = func() time.Time { return base.Add(publishedMetricRetention + time.Minute) }
	published.snapshot()
	if got := testutil.ToFloat64(catalogPublishedMetrics); got != 0 {
		t.Errorf("gauge = %v after everything aged out, want 0", got)
	}
}

// TestPublishedMetricsRecordsOnlyWhatReachedPrometheus checks a metric is recorded only after a successful send.
func TestPublishedMetricsRecordsOnlyWhatReachedPrometheus(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Fail the first write, accept the retry.
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	exporter, err := NewPrometheusExporter(server.URL, 1, time.Hour, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	defer func() { _ = exporter.Close() }()

	published := exporter.publishedMetricNames()
	ctx := context.Background()

	// A batch that reaches the batch size flushes immediately, and this one
	// fails.
	_ = exporter.Export(ctx, []*Sample{
		{Name: "snmp_never_landed", Timestamp: time.Now(), Value: 1},
	})

	if got := published.len(); got != 0 {
		t.Fatalf("published set holds %d names after a failed write, want 0; "+
			"the catalog would describe a series that never arrived", got)
	}

	// The retry lands and only now is it published; bounded loop because the first attempt runs on the sender.
	var flushErr error
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if flushErr = exporter.Flush(ctx); flushErr == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if flushErr != nil {
		t.Fatalf("Flush after the endpoint recovered: %v", flushErr)
	}
	if got := published.len(); got != 1 {
		t.Errorf("published set holds %d names after the write landed, want 1", got)
	}
}

// TestCatalogEmissionSuccessMeansDelivery checks success counts delivered catalogs, not queued ones.
func TestCatalogEmissionSuccessMeansDelivery(t *testing.T) {
	cache := catalogTestCache(t, []*mib.OIDMetadata{
		{OID: ".1.3.6.1.2.1.2.2.1.10", Name: "ifInOctets", Module: "IF-MIB", Type: "Counter32", IsColumn: true},
	})
	published := newPublishedMetrics()
	published.record([]*Sample{{Name: "snmp_ifInOctets"}})

	// Delivery fails.
	exporter := &captureExporter{err: errors.New("remote write refused")}
	emitter := newCatalogEmitter(exporter, cache, published)

	beforeOK := testutil.ToFloat64(catalogEmissions.WithLabelValues("success"))
	beforeErr := testutil.ToFloat64(catalogEmissions.WithLabelValues("error"))

	emitter.emit(context.Background())

	if got := testutil.ToFloat64(catalogEmissions.WithLabelValues("success")); got != beforeOK {
		t.Errorf("success counter moved to %v although the catalog was only buffered", got)
	}
	if got := testutil.ToFloat64(catalogEmissions.WithLabelValues("error")); got != beforeErr+1 {
		t.Errorf("error counter = %v, want %v", got, beforeErr+1)
	}
}

// TestCatalogEmissionRecordsWhenItLanded checks the freshness gauge, which is
// what an alert watches as its age approaches the lookback consumers query the
// catalog with.
func TestCatalogEmissionRecordsWhenItLanded(t *testing.T) {
	cache := catalogTestCache(t, []*mib.OIDMetadata{
		{OID: ".1.3.6.1.2.1.2.2.1.10", Name: "ifInOctets", Module: "IF-MIB", Type: "Counter32", IsColumn: true},
	})
	published := newPublishedMetrics()
	published.record([]*Sample{{Name: "snmp_ifInOctets"}})

	landed := time.Unix(1756600000, 0)
	emitter := newCatalogEmitter(&captureExporter{}, cache, published)
	emitter.now = func() time.Time { return landed }

	emitter.emit(context.Background())

	if got := testutil.ToFloat64(catalogLastSuccess); got != float64(landed.Unix()) {
		t.Errorf("last-success gauge = %v, want %v", got, landed.Unix())
	}

	// A later failure must not advance it: its age is the signal.
	failing := newCatalogEmitter(&captureExporter{err: errors.New("refused")}, cache, published)
	failing.now = func() time.Time { return landed.Add(time.Hour) }
	failing.emit(context.Background())

	if got := testutil.ToFloat64(catalogLastSuccess); got != float64(landed.Unix()) {
		t.Errorf("last-success gauge advanced to %v on a failed emission; its age is the signal", got)
	}
}

// benchmarkBatch is one flush's worth of samples: a realistic mix where a
// large batch carries relatively few distinct metric names, because a poll of
// one device produces many rows of a few tables.
func benchmarkBatch(samples, distinctNames int) []*Sample {
	batch := make([]*Sample, 0, samples)
	for i := 0; i < samples; i++ {
		batch = append(batch, &Sample{
			Name: "snmp_metric_" + strconv.Itoa(i%distinctNames),
			Labels: map[string]string{
				labelDevice: "device-1",
				"ifindex":   strconv.Itoa(i),
			},
		})
	}
	return batch
}

// BenchmarkPublishedMetricsRecord measures record() on the flush path in steady state.
func BenchmarkPublishedMetricsRecord(b *testing.B) {
	batch := benchmarkBatch(15000, 200)

	published := newPublishedMetrics()
	published.record(batch)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		published.record(batch)
	}
}

// BenchmarkPublishedMetricsRecordParallel measures contention, which is the
// question that matters at a hundred thousand devices: every device's flush
// goes through this one process-wide lock.
func BenchmarkPublishedMetricsRecordParallel(b *testing.B) {
	batch := benchmarkBatch(15000, 200)

	published := newPublishedMetrics()
	published.record(batch)

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			published.record(batch)
		}
	})
}

// BenchmarkCatalogRender measures rebuilding the catalog's label sets, which
// happens in full on every emission even though the content is unchanged
// between MIB loads.
func BenchmarkCatalogRender(b *testing.B) {
	objects := make([]*mib.OIDMetadata, 0, 2000)
	for i := 0; i < 2000; i++ {
		objects = append(objects, &mib.OIDMetadata{
			OID: ".1.3.6.1.4.1.9999.1." + strconv.Itoa(i), Name: "obj" + strconv.Itoa(i),
			Module: "BENCH-MIB", Type: "Counter32", IsColumn: true,
			Description: strings.Repeat("a description of this object. ", 7),
		})
	}

	cache := &TableIndexCache{}
	cache.publishIndex(&mibSweep{
		names:         newObjectNameIndex(objects),
		tables:        newTableSet(),
		statusColumns: make(map[string][]statusColumnInfo),
		catalog:       newCatalogIndex(objects, newTableSet(), nil),
	})

	published := newPublishedMetrics()
	names := make([]*Sample, 0, len(objects))
	for i := range objects {
		names = append(names, &Sample{Name: "snmp_obj" + strconv.Itoa(i)})
	}
	published.record(names)

	emitter := newCatalogEmitter(&captureExporter{}, cache, published)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if samples, _ := emitter.render(); len(samples) == 0 {
			b.Fatal("render produced nothing")
		}
	}
}

// TestSendCatalogNeverTouchesTheTelemetryBuffer checks a rejected catalog write never delays device samples.
func TestSendCatalogNeverTouchesTheTelemetryBuffer(t *testing.T) {
	refused := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer refused.Close()

	exporter, err := NewPrometheusExporter(refused.URL, 1000, time.Hour, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	defer func() { _ = exporter.Close() }()

	ctx := context.Background()

	// A device's reading, buffered and not yet flushed.
	reading := []*Sample{{Name: "snmp_ifInOctets", Value: 42, Timestamp: time.Now()}}
	if err := exporter.Export(ctx, reading); err != nil {
		t.Fatalf("buffering a reading: %v", err)
	}

	exporter.bufferLock.Lock()
	buffered := len(exporter.buffer)
	exporter.bufferLock.Unlock()
	if buffered != 1 {
		t.Fatalf("buffer holds %d samples before the catalog, want 1", buffered)
	}

	// The catalog is refused. The reading must be exactly where it was.
	catalog := []*Sample{{Name: catalogMetricName, Value: 1, Timestamp: time.Now()}}
	if err := exporter.SendCatalog(ctx, catalog); err == nil {
		t.Fatal("SendCatalog reported success against a refusing endpoint")
	}

	exporter.bufferLock.Lock()
	after := exporter.buffer
	exporter.bufferLock.Unlock()

	if len(after) != 1 {
		t.Fatalf("buffer holds %d samples after the catalog was refused, want the 1 reading", len(after))
	}
	if after[0].Name != "snmp_ifInOctets" {
		t.Errorf("buffer holds %q; the catalog entered the telemetry buffer", after[0].Name)
	}
}

// TestSendCatalogChunksItsRequests checks one emission is split into several requests.
func TestSendCatalogChunksItsRequests(t *testing.T) {
	var requests atomic.Int32
	accepting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer accepting.Close()

	const batch = 100
	exporter, err := NewPrometheusExporter(accepting.URL, batch, time.Hour, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	defer func() { _ = exporter.Close() }()

	samples := make([]*Sample, 0, 450)
	for i := 0; i < 450; i++ {
		samples = append(samples, &Sample{
			Name:      catalogMetricName,
			Labels:    map[string]string{labelCatalogMetric: "snmp_m" + strconv.Itoa(i)},
			Value:     1,
			Timestamp: time.Now(),
		})
	}

	if err := exporter.SendCatalog(context.Background(), samples); err != nil {
		t.Fatalf("SendCatalog: %v", err)
	}

	if got := requests.Load(); got != 5 {
		t.Errorf("450 samples at batch size %d took %d requests, want 5", batch, got)
	}
}

// TestSendCatalogStopsAtTheFirstFailure checks a partial catalog is not left behind.
func TestSendCatalogStopsAtTheFirstFailure(t *testing.T) {
	var requests atomic.Int32
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer failing.Close()

	exporter, err := NewPrometheusExporter(failing.URL, 10, time.Hour, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	defer func() { _ = exporter.Close() }()

	samples := make([]*Sample, 0, 100)
	for i := 0; i < 100; i++ {
		samples = append(samples, &Sample{
			Name: catalogMetricName, Value: 1, Timestamp: time.Now(),
			Labels: map[string]string{labelCatalogMetric: "snmp_m" + strconv.Itoa(i)},
		})
	}

	if err := exporter.SendCatalog(context.Background(), samples); err == nil {
		t.Fatal("SendCatalog reported success although a chunk was refused")
	}
	if got := requests.Load(); got != 2 {
		t.Errorf("%d requests were made, want 2: the send must stop at the first failure", got)
	}
}
