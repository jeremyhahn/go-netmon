package poller

import (
	"context"
	"log"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/metrics"
)

// moduleLoadFailures counts the MIB modules the poller could not bring into its
// metadata store, by the device that wanted the module and the module itself.
//
// A counter rather than a device status: the device is otherwise healthy and
// still polls every module that did load, so marking it unhealthy would be
// wrong, while what an operator needs to know — "this device is polling less
// than you configured, because this named module would not load" — is exactly a
// per-device, per-module count. It also reaches the metrics endpoint the
// deployment already scrapes, so it can be alerted on rather than only noticed.
var moduleLoadFailures = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "gonetmon_mib_module_load_failures_total",
		Help: "MIB modules assigned to a device that could not be loaded, so none of their objects are polled.",
	},
	[]string{"device", "module"},
)

func init() {
	prometheus.MustRegister(moduleLoadFailures)
}

// hydrateModules brings every MIB module a device is assigned into the poller's
// metadata store, parsing the ones the store does not already hold.
//
// This is what makes a MIB assigned through the web UI actually get polled. The
// web UI and the poller keep separate stores — Pebble admits one writer, so
// they cannot share one — and the poller is told only the *names* of a device's
// modules. Without this step a name whose objects are not in the poller's own
// store contributes nothing to the selection, and the device silently polls the
// subset that happened to be auto-loaded at startup.
//
// Loading a module changes what the MIB store describes, which is what the
// exporter names and labels samples from, so hydration ends by refreshing that
// derived state. It is done here, rather than left to each caller, so that the
// invariant a caller depends on — once this returns, the device's objects have
// both their metadata and their names — cannot be forgotten at a call site.
//
// A module that will not load is skipped rather than fatal. The alternative,
// failing the device, would take a device that polls sixteen of its seventeen
// modules perfectly well and stop it polling at all.
//
// The caller must not hold the poller lock: parsing a module costs tens of
// milliseconds and a device can carry seventeen of them, so holding that lock
// across this would stall every other device's registration, removal and status
// lookup. Devices already polling are unaffected either way — the polling loop
// takes neither this lock nor the loader's.
func (p *Poller) hydrateModules(ctx context.Context, device *config.Device) {
	if p.mibLoader == nil {
		return
	}

	for _, module := range device.Modules {
		// A caller that has given up gets no further parsing: the remaining
		// modules would be loaded for a device that is about to be dropped.
		// The refresh below still runs, so whatever did load is indexed rather
		// than left nameless until some later device happens to trigger one.
		if ctx.Err() != nil {
			break
		}

		if err := p.mibLoader.EnsureModule(module); err != nil {
			moduleLoadFailures.WithLabelValues(device.Name, module).Inc()
			log.Printf("Device %s: MIB module %s could not be loaded and will not be polled: %v",
				device.Name, module, err)
		}
	}

	p.refreshMetricMetadata()
}

// refreshMetricMetadata rebuilds the exporter's MIB-derived metric names and
// index labels, unless nothing has been loaded since they were last built.
//
// Without this, an object of a module loaded after the exporter was constructed
// matches no MIB definition and its samples fall back to a raw-OID metric name
// with no index labels: lossless and visible, but not what anyone queries.
//
// The loader's revision is what makes the skip safe and is why this is not
// simply called once per module: a device carrying seventeen modules would
// otherwise sweep the whole MIB store seventeen times, and a device whose
// modules were all already loaded would sweep it for no reason at all.
//
// A mutex rather than a compare-and-swap retry: the point of serializing is
// that the *second* caller does no work, and a lock-free loop would instead
// have every caller that observed a change sweep the store concurrently. It is
// held across the rebuild, which no poll waits on — the exporter builds the new
// index off to the side and installs it with one atomic store.
func (p *Poller) refreshMetricMetadata() {
	if p.mibLoader == nil {
		return
	}

	refresher, refreshable := p.exporter.(metrics.MIBMetadataRefresher)
	if !refreshable {
		return
	}

	// Read the revision before rebuilding, never after: the sweep it triggers
	// is guaranteed to see everything this revision accounts for, so recording
	// it can only under-claim what was indexed, and an under-claim costs one
	// redundant sweep whereas an over-claim would leave modules unnamed until
	// the next unrelated load.
	if p.indexedRevision.Load() >= p.mibLoader.Revision() {
		return
	}

	p.refreshMu.Lock()
	defer p.refreshMu.Unlock()

	revision := p.mibLoader.Revision()
	if p.indexedRevision.Load() >= revision {
		return
	}

	if err := refresher.RefreshMIBMetadata(); err != nil {
		// Reported, not fatal: the previous index is still published, so
		// polling continues with the names it already had rather than none.
		log.Printf("MIB index rebuild failed; metric names for newly loaded modules will be stale: %v", err)
		return
	}

	p.indexedRevision.Store(revision)
}
