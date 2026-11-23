package metrics

import (
	"context"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// The catalog is written by exactly one designated emitter, and never from a
// device's poll path.
//
// Both halves of that matter. Writing it from a poll path would emit the whole
// catalog once per device per cycle -- 20,390 series times the fleet, which at
// a hundred thousand devices is billions of writes for data that is identical
// every time. And letting every process emit it on its own timer would have
// several collectors writing the same series at uncoordinated moments: the
// first write to land sets the sample's timestamp, and a second arriving
// behind it is rejected out of order, because out_of_order_time_window is 0 by
// default. That failure needs more than one shard to appear at all, so it
// would work in development and fail intermittently in production.
//
// Designating one emitter by configuration avoids both without a coordination
// protocol, an election, or any reliance on how the receiver treats duplicate
// samples. A single-process deployment designates itself; a sharded one names
// one instance. It is a separate component from the exporter for the same
// reason: sharding must not require restructuring it.

// CatalogInterval is how often the designated emitter republishes the catalog.
//
// The catalog is static between MIB loads, so this is not a data-freshness
// question -- it is only about staying inside Prometheus's staleness window,
// which is five minutes by default. Two minutes leaves room for one missed
// emission without the series going stale, and puts the cost at roughly 2 MB
// of labels before compression every two minutes, forever, regardless of how
// many devices exist or how often they are polled.
const CatalogInterval = 2 * time.Minute

// Self-instrumentation for the catalog.
var (
	catalogSeriesEmitted = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "gonetmon_catalog_series",
			Help: "Number of series in the metric catalog at its last emission, including enumeration rows.",
		},
	)
	catalogPublishedMetrics = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "gonetmon_catalog_published_metrics",
			Help: "Number of distinct metric names this collector has published. NOT the catalog's scope -- the catalog describes every family the loaded MIBs can, including ones other shards publish. This is the denominator for the undescribed count.",
		},
	)
	catalogUndescribed = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "gonetmon_catalog_undescribed_metrics",
			Help: "Published metric names the catalog can say nothing about, because no loaded MIB describes them and they are not a family this collector derives.",
		},
	)
	catalogUnknownKind = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "gonetmon_catalog_unknown_kind",
			Help: "Cataloged metrics whose kind could not be determined, which means their MIB module parsed with unresolved imports and they also lost their units and enumeration.",
		},
	)
	catalogNameCollisions = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "gonetmon_catalog_name_collisions",
			Help: "MIB objects discarded from the catalog because an earlier object already claimed their published metric name.",
		},
	)
	catalogEmissions = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gonetmon_catalog_emissions_total",
			Help: "Total catalog emissions, by outcome (success, error). Success means the catalog reached Prometheus, not that it was queued.",
		},
		[]string{"outcome"},
	)
	catalogLastSuccess = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "gonetmon_catalog_last_success_timestamp_seconds",
			Help: "Unix timestamp of the last catalog emission that reached Prometheus. 0 if none has. Alert as its age approaches the lookback consumers query the catalog with.",
		},
	)
)

func init() {
	prometheus.MustRegister(
		catalogSeriesEmitted,
		catalogPublishedMetrics,
		catalogUndescribed,
		catalogUnknownKind,
		catalogNameCollisions,
		catalogEmissions,
		catalogLastSuccess,
	)
}

// catalogSink delivers the catalog, and delivers only the catalog.
//
// It is a narrower dependency than Exporter on purpose. The catalog must not
// share a batch with device telemetry -- a rejected catalog write that put
// device samples back with it could delay or discard measurements, so a
// description would be able to cost a reading. Naming exactly what the emitter
// needs is what stops a later change reaching for Export and reintroducing
// that coupling.
type catalogSink interface {
	// SendCatalog delivers samples and reports whether Prometheus took them.
	// It returns on delivery, not on queueing.
	SendCatalog(ctx context.Context, samples []*Sample) error
}

// CatalogEmitter publishes the metric catalog on its own schedule.
type CatalogEmitter struct {
	exporter  catalogSink
	index     *TableIndexCache
	published *publishedMetrics
	interval  time.Duration

	// now is the clock, injected so tests do not depend on wall time.
	now func() time.Time

	stop chan struct{}
	wg   sync.WaitGroup
	once sync.Once
}

// NewCatalogEmitterFor returns an emitter publishing the catalog for what
// exporter has sent, described by the MIB metadata it was built with.
//
// It does not start on construction: a process that is not the designated
// emitter never calls Start, or never builds one at all.
func NewCatalogEmitterFor(exporter *PrometheusExporter) *CatalogEmitter {
	return newCatalogEmitter(exporter, exporter.TableIndexCache(), exporter.publishedMetricNames())
}

// Compile-time check that the exporter can actually deliver a catalog.
var _ catalogSink = (*PrometheusExporter)(nil)

// newCatalogEmitter is NewCatalogEmitterFor with its collaborators supplied
// directly, which is what the tests need.
func newCatalogEmitter(exporter catalogSink, index *TableIndexCache, published *publishedMetrics) *CatalogEmitter {
	return &CatalogEmitter{
		exporter:  exporter,
		index:     index,
		published: published,
		interval:  CatalogInterval,
		now:       time.Now,
		stop:      make(chan struct{}),
	}
}

// Start begins publishing the catalog until ctx is canceled or Stop is
// called. It emits once immediately, so a restart does not leave consumers
// without a catalog for a whole interval.
func (e *CatalogEmitter) Start(ctx context.Context) {
	e.wg.Add(1)
	go e.run(ctx)
}

// Stop ends publication and waits for the emitter to finish.
func (e *CatalogEmitter) Stop() {
	e.once.Do(func() { close(e.stop) })
	e.wg.Wait()
}

func (e *CatalogEmitter) run(ctx context.Context) {
	defer e.wg.Done()

	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()

	e.emit(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-e.stop:
			return
		case <-ticker.C:
			e.emit(ctx)
		}
	}
}

// emit renders the catalog and exports it once.
func (e *CatalogEmitter) emit(ctx context.Context) {
	samples, stats := e.render()
	if len(samples) == 0 {
		return
	}

	catalogSeriesEmitted.Set(float64(len(samples)))
	catalogUndescribed.Set(float64(stats.undescribed))
	catalogUnknownKind.Set(float64(stats.unknownKind))
	catalogNameCollisions.Set(float64(stats.collisions))

	// SendCatalog delivers rather than queues, so this outcome means the
	// catalog reached Prometheus. Counting a success on a buffered write
	// would give an emitter whose remote write was being refused a clean
	// record while every description aged out.
	if err := e.exporter.SendCatalog(ctx, samples); err != nil {
		e.recordFailure(err)
		return
	}

	catalogEmissions.WithLabelValues("success").Inc()
	catalogLastSuccess.Set(float64(e.now().Unix()))
}

// catalogStats is what one render found, reported as gauges so an operator can
// see the catalog's coverage without querying it.
type catalogStats struct {
	undescribed int
	unknownKind int
	collisions  int
}

// render builds the catalog series for every family the loaded MIBs can
// describe -- not merely the families this process has published.
//
// Scoping emission to the local published set is wrong in the deployment this
// exists for. Exactly one collector is designated as the emitter, and the
// published set is process-local, so a designated shard A polling IF-MIB
// devices never observes a shard B that publishes a BGP family: the fleet-wide
// catalog would carry no row for it, and dashboards could not render B's
// devices at all. The MIB corpus, by contrast, is identical on every shard, so
// cataloging it gives one emitter complete knowledge with no cross-process
// coordination and no second writer to collide with.
//
// The cost is rows for families no device answers. That is harmless here: a
// renderer must ask Prometheus which series a given device actually has
// regardless, and that query filters the surplus. Describing a metric that is
// absent costs a row; omitting one that is present costs a blank dashboard.
func (e *CatalogEmitter) render() ([]*Sample, catalogStats) {
	var stats catalogStats

	catalog := e.index.catalogIndex()
	stats.collisions = catalog.collisions

	// Derived families are described by the collector rather than by any MIB,
	// so they are indexed here rather than swept from the store.
	derived := make(map[string]*CatalogEntry, len(sensorUnitSuffixes)+1)
	for _, entry := range derivedCatalogEntries() {
		derived[entry.Metric] = entry
	}

	entries := make([]*CatalogEntry, 0, len(catalog.entries)+len(derived))
	for _, entry := range catalog.entries {
		if entry.Kind == MetricKindUnknown {
			stats.unknownKind++
		}
		entries = append(entries, entry)
	}
	for _, entry := range derived {
		entries = append(entries, entry)
	}
	if len(entries) == 0 {
		return nil, stats
	}

	// Sorted so two emissions of an unchanged corpus render an identical
	// series set in an identical order; map iteration alone is not stable.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Metric < entries[j].Metric })

	// The published set no longer scopes emission, but it still answers the
	// question the coverage gauge asks: what did this collector publish that
	// nothing can describe? Raw-OID names are the expected residue.
	for _, name := range e.published.snapshot() {
		if _, described := catalog.entries[name]; described {
			continue
		}
		if _, isDerived := derived[name]; isDerived {
			continue
		}
		stats.undescribed++
	}

	// One timestamp for the whole emission, so the catalog is a single
	// consistent view rather than a set of rows smeared across the render.
	return catalogSamples(entries, e.now()), stats
}

// recordFailure reports a catalog that did not reach Prometheus.
//
// It is loud because the failure is quiet: the device metrics keep arriving
// and look healthy while every description ages out, so the symptom presents
// as a broken UI rather than a broken collector. The last-success gauge is
// deliberately not touched -- its age is what says how close the catalog is to
// falling outside the lookback consumers query it with.
func (e *CatalogEmitter) recordFailure(err error) {
	catalogEmissions.WithLabelValues("error").Inc()
	log.Printf("ERROR: the metric catalog did not reach Prometheus, so dashboards will lose "+
		"every metric description once the series age out: %v", err)
}
