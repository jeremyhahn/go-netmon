package poller

import (
	"context"
	"log"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/metrics"
)

// moduleLoadFailures counts MIB modules the poller could not load, by device and module.
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

// hydrateModules loads a device's assigned MIB modules into the poller's store, skipping failures, then refreshes metric metadata; caller must not hold the poller lock.
func (p *Poller) hydrateModules(ctx context.Context, device *config.Device) {
	if p.mibLoader == nil {
		return
	}

	for _, module := range device.Modules {
		// Stop parsing once the caller gives up; still refresh so whatever loaded is indexed.
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

// refreshMetricMetadata rebuilds the exporter's MIB-derived names and labels unless the loader revision is unchanged.
func (p *Poller) refreshMetricMetadata() {
	if p.mibLoader == nil {
		return
	}

	refresher, refreshable := p.exporter.(metrics.MIBMetadataRefresher)
	if !refreshable {
		return
	}

	// Read the revision before rebuilding, so it can only under-claim what was indexed.
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
