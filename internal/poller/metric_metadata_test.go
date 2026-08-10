package poller

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/metrics"
	"github.com/jeremyhahn/go-netmon/internal/mib/loader"
	"github.com/jeremyhahn/go-netmon/internal/snmp"
)

// The exporter names and labels samples from MIB metadata it read when it was
// constructed. Hydration loads modules into that store afterwards, so unless
// something tells the exporter, every object of every hydrated module falls
// back to a raw-OID metric name. These tests pin when that rebuild happens and,
// just as importantly, when it does not: a device carries seventeen modules,
// and rebuilding per module would sweep the whole MIB store seventeen times.

// countingExporter records how often its MIB-derived state was rebuilt. It
// exports nothing: these tests are about the rebuild trigger, and a device that
// never polls still has to end hydration with a correct index.
type countingExporter struct {
	refreshes atomic.Int64
	err       error
}

func (e *countingExporter) Export(context.Context, []*metrics.Sample) error { return nil }

func (e *countingExporter) ExportSNMPResults(context.Context, string, []*snmp.Result, map[string]string) error {
	return nil
}

func (e *countingExporter) Flush(context.Context) error { return nil }

func (e *countingExporter) Close() error { return nil }

func (e *countingExporter) RefreshMIBMetadata() error {
	e.refreshes.Add(1)
	return e.err
}

// plainExporter has no MIB-derived state, like a sink configured without MIB
// storage. Hydration must tolerate it.
type plainExporter struct{}

func (plainExporter) Export(context.Context, []*metrics.Sample) error { return nil }

func (plainExporter) ExportSNMPResults(context.Context, string, []*snmp.Result, map[string]string) error {
	return nil
}

func (plainExporter) Flush(context.Context) error { return nil }

func (plainExporter) Close() error { return nil }

// newRefreshPoller builds a poller over mibLoader and exporter with no devices
// of its own.
func newRefreshPoller(t *testing.T, mibLoader *loader.Loader, exporter metrics.Exporter) *Poller {
	t.Helper()

	cfg := getTestConfig(t)
	cfg.Devices = nil

	p := NewWithExporter(cfg, mibLoader, exporter)
	t.Cleanup(func() { _ = p.Stop() })
	return p
}

// TestHydrationRebuildsMetricMetadataOnceForAWholeDevice is the coalescing
// rule. Seventeen modules are loaded and the MIB store is swept once, not
// seventeen times: the sweep reads every object in the store, so paying it per
// module would make hydration cost seventeen full catalog scans.
func TestHydrationRebuildsMetricMetadataOnceForAWholeDevice(t *testing.T) {
	mibLoader := newHydrationLoader(t, autoLoadedModules())
	exporter := &countingExporter{}
	p := newRefreshPoller(t, mibLoader, exporter)

	device := &config.Device{Name: "core-01", Modules: coreRouterModules()}
	p.hydrateModules(context.Background(), device)

	if got := exporter.refreshes.Load(); got != 1 {
		t.Errorf("hydrating %d modules rebuilt the index %d times, want 1",
			len(device.Modules), got)
	}
	if p.indexedRevision.Load() != mibLoader.Revision() {
		t.Errorf("indexed revision = %d, want the loader's %d",
			p.indexedRevision.Load(), mibLoader.Revision())
	}
}

// TestHydrationDoesNotRebuildWhenNothingWasLoaded is the other half of
// coalescing, and the case that recurs forever: a device added to a poller
// whose store already holds its modules changes nothing, so rebuilding would be
// a full store sweep for no new metadata at all.
func TestHydrationDoesNotRebuildWhenNothingWasLoaded(t *testing.T) {
	mibLoader := newHydrationLoader(t, autoLoadedModules())
	exporter := &countingExporter{}
	p := newRefreshPoller(t, mibLoader, exporter)

	device := &config.Device{Name: "core-01", Modules: coreRouterModules()}

	p.hydrateModules(context.Background(), device)
	afterFirst := exporter.refreshes.Load()
	if afterFirst != 1 {
		t.Fatalf("first hydration rebuilt the index %d times, want 1", afterFirst)
	}

	// The same device again, then a second device with the same profile:
	// every module is already loaded, so neither may rebuild.
	p.hydrateModules(context.Background(), device)
	p.hydrateModules(context.Background(), &config.Device{
		Name: "core-02", Modules: coreRouterModules(),
	})

	if got := exporter.refreshes.Load(); got != afterFirst {
		t.Errorf("re-hydrating already-loaded modules rebuilt the index %d more times, want 0",
			got-afterFirst)
	}
}

// TestHydrationRebuildsAgainForAModuleNotYetLoaded checks the skip is driven by
// what the store holds rather than by having run once: a device introducing a
// module nobody has loaded must still get an index that names it.
func TestHydrationRebuildsAgainForAModuleNotYetLoaded(t *testing.T) {
	mibLoader := newHydrationLoader(t, autoLoadedModules())
	exporter := &countingExporter{}
	p := newRefreshPoller(t, mibLoader, exporter)

	p.hydrateModules(context.Background(), &config.Device{
		Name: "edge-01", Modules: []string{"IF-MIB", "SNMPv2-MIB"},
	})
	afterFirst := exporter.refreshes.Load()

	p.hydrateModules(context.Background(), &config.Device{
		Name: "core-01", Modules: coreRouterModules(),
	})

	if got := exporter.refreshes.Load(); got != afterFirst+1 {
		t.Errorf("a device introducing new modules rebuilt the index %d times, want 1",
			got-afterFirst)
	}
}

// TestRefreshFailureIsRetriedRatherThanRecorded covers the bad-input case. A
// rebuild that failed indexed nothing, so recording its revision would leave
// every module loaded so far unnamed until some unrelated load happened to
// trigger another attempt.
func TestRefreshFailureIsRetriedRatherThanRecorded(t *testing.T) {
	mibLoader := newHydrationLoader(t, autoLoadedModules())
	exporter := &countingExporter{err: &loader.ModuleNotFoundError{Module: "IF-MIB"}}
	p := newRefreshPoller(t, mibLoader, exporter)

	device := &config.Device{Name: "core-01", Modules: coreRouterModules()}

	p.hydrateModules(context.Background(), device)
	if p.indexedRevision.Load() != 0 {
		t.Errorf("a failed rebuild recorded revision %d, want 0", p.indexedRevision.Load())
	}

	// Nothing new loads the second time, but the failure has to be retried.
	p.hydrateModules(context.Background(), device)
	if got := exporter.refreshes.Load(); got != 2 {
		t.Errorf("rebuild attempts = %d, want 2 (the failure must be retried)", got)
	}

	exporter.err = nil
	p.hydrateModules(context.Background(), device)
	if p.indexedRevision.Load() != mibLoader.Revision() {
		t.Errorf("after the rebuild succeeded, indexed revision = %d, want %d",
			p.indexedRevision.Load(), mibLoader.Revision())
	}
}

// TestRefreshMetricMetadataToleratesExportersWithoutMIBState covers the
// degraded wirings the poller is really constructed with: no exporter at all,
// an exporter with no MIB-derived state, and no MIB loader. Each must be a
// silent no-op, since hydration runs on every device that is added.
func TestRefreshMetricMetadataToleratesExportersWithoutMIBState(t *testing.T) {
	mibLoader := newHydrationLoader(t, autoLoadedModules())
	device := &config.Device{Name: "core-01", Modules: []string{"IF-MIB"}}

	tests := []struct {
		name     string
		loader   *loader.Loader
		exporter metrics.Exporter
	}{
		{name: "no exporter", loader: mibLoader, exporter: nil},
		{name: "exporter without MIB state", loader: mibLoader, exporter: plainExporter{}},
		{name: "no MIB loader", loader: nil, exporter: &countingExporter{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newRefreshPoller(t, tt.loader, tt.exporter)
			p.hydrateModules(context.Background(), device)

			if p.indexedRevision.Load() != 0 {
				t.Errorf("indexed revision = %d, want 0", p.indexedRevision.Load())
			}
		})
	}
}

// TestConcurrentHydrationRebuildsAreSerialized checks that devices registered
// at the same time -- which is what a configuration sync does -- do not each
// sweep the store. Under -race this also proves the revision bookkeeping is
// sound.
func TestConcurrentHydrationRebuildsAreSerialized(t *testing.T) {
	mibLoader := newHydrationLoader(t, coreRouterModules())
	exporter := &countingExporter{}
	p := newRefreshPoller(t, mibLoader, exporter)

	const devices = 8
	done := make(chan struct{})
	for i := range devices {
		go func() {
			defer func() { done <- struct{}{} }()
			p.hydrateModules(context.Background(), &config.Device{
				Name:    "core-" + string(rune('a'+i)),
				Modules: coreRouterModules(),
			})
		}()
	}
	for range devices {
		<-done
	}

	// Every module was already loaded, so exactly one rebuild is needed: the
	// one that catches the index up with the auto-load done at startup.
	if got := exporter.refreshes.Load(); got != 1 {
		t.Errorf("%d concurrent hydrations rebuilt the index %d times, want 1", devices, got)
	}
}

// TestCanceledHydrationStillIndexesWhatWasLoaded covers the bad-input case for
// the trigger. A caller that gives up part way through leaves modules in the
// store; abandoning the rebuild too would leave their objects nameless until
// some unrelated device happened to load something.
func TestCanceledHydrationStillIndexesWhatWasLoaded(t *testing.T) {
	mibLoader := newHydrationLoader(t, autoLoadedModules())
	exporter := &countingExporter{}
	p := newRefreshPoller(t, mibLoader, exporter)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	p.hydrateModules(ctx, &config.Device{Name: "core-01", Modules: coreRouterModules()})

	if got := exporter.refreshes.Load(); got != 1 {
		t.Errorf("a canceled hydration rebuilt the index %d times, want 1", got)
	}
	if p.indexedRevision.Load() != mibLoader.Revision() {
		t.Errorf("indexed revision = %d, want the loader's %d",
			p.indexedRevision.Load(), mibLoader.Revision())
	}
}
