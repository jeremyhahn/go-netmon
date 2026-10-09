// Package poller walks the configured devices over SNMP on a schedule and hands
// the results to the metrics exporter.
package poller

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/logging"
	"github.com/jeremyhahn/go-netmon/internal/metrics"
	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/mib/loader"
	"github.com/jeremyhahn/go-netmon/internal/mib/storage"
	"github.com/jeremyhahn/go-netmon/internal/snmp"
)

// Poller manages SNMP polling for multiple devices
type Poller struct {
	config    *config.Config
	mibLoader *loader.Loader
	exporter  metrics.Exporter
	pool      *snmp.Pool
	// registry is who owns each device name right now.
	//
	// ONE map, deliberately. It used to be two -- a worker map and a retiring
	// map -- and "this name is free" was then two facts in two places, with
	// nothing requiring a reader to consult both. Four check-then-act defects
	// shipped in one branch against that split, each a correct fix that left
	// the same seam one layer out, and the websocket hub reproduced it a fifth
	// time in another package. A reader that consults this key alone gets a
	// correct answer, because the intermediate state is a state of the entry
	// rather than the absence of one thing beside the presence of another.
	registry   map[string]*registration
	resultChan chan *PollResult
	logger     *slog.Logger
	mu         sync.RWMutex
	wg         sync.WaitGroup
	stopChan   chan struct{}
	stopped    bool

	// indexedRevision is the MIB loader revision the exporter's metric naming
	// and index labels were last rebuilt from, and refreshMu serializes those
	// rebuilds. See refreshMetricMetadata.
	indexedRevision atomic.Uint64
	refreshMu       sync.Mutex

	// baseCtx is the lifetime workers run under. It is deliberately not the
	// context of whichever API request added a device: a worker outlives the
	// call that created it, and inheriting a request context would stop it the
	// moment that request returned.
	baseCtx context.Context
}

// registration is one device name's entry in the poller, and the single answer
// to who owns that name.
//
// Three states, and the ABSENCE of a fourth is the point:
//
//	key absent                        the name is free
//	worker != nil, retired == nil     the name is live
//	worker == nil, retired != nil     the name is being retired
//
// There is no way to spell "gone from one place, present in another", because
// there is only one place. That is what makes the ownership rule construction
// rather than discipline: a reader consulting the key gets a whole answer, and
// cannot get half of one by forgetting a second map.
type registration struct {
	// worker polls the device. nil once a removal has taken the name.
	worker *worker

	// retired is non-nil while a removal holds the name, and is closed when
	// that removal has finished with it -- after the pool entry is gone, not
	// before, so a waiter that wakes finds the name genuinely free.
	retired chan struct{}
}

// live reports whether a registration is a worker rather than a retirement.
func (r *registration) live() bool { return r != nil && r.worker != nil }

// liveWorker returns the worker registered for a name, or nil when the name is
// free or being retired. The caller holds p.mu.
//
// A name being retired has no worker to answer for: its worker has stopped and
// its client is being closed, so reporting it would describe something that is
// on its way out as though it were polling.
func (p *Poller) liveWorker(name string) *worker {
	entry := p.registry[name]
	if !entry.live() {
		return nil
	}
	return entry.worker
}

// Option configures a Poller at construction.
type Option func(*Poller)

// WithLogger routes the poller's level-gated diagnostics to logger.
//
// Without it the poller discards them, so a caller that never configures
// logging pays nothing for the debug statements on the polling path.
func WithLogger(logger *slog.Logger) Option {
	return func(p *Poller) { p.logger = logging.OrDiscard(logger) }
}

// worker represents a polling worker for a single device
type worker struct {
	device     *config.Device
	client     *snmp.Client
	oids       []*mib.OIDMetadata // OIDs to poll for this device
	interval   time.Duration
	stopChan   chan struct{}
	resultChan chan<- *PollResult
	exporter   metrics.Exporter
	pool       *snmp.Pool // For lazy client initialization
	logger     *slog.Logger
	clientMu   sync.Mutex // Protects client initialization

	// removed is closed when this worker's device is removed, which is what
	// ends the polling loop. stopChan stops every worker at once; this stops
	// exactly one, so a removed device stops polling instead of being merely
	// forgotten by the map.
	removed    chan struct{}
	removeOnce sync.Once

	// commitMu is held across the whole of a poll's commit -- reachability,
	// export and report -- and by RemoveDevice on its way out. It is what makes
	// "is this device still mine?" part of the commit instead of a question
	// asked just before it. See commit.
	commitMu sync.Mutex

	// health decides how long to wait before the next poll and carries whether
	// the device is answering at all. See reachability.
	health *reachability
}

// stop ends this worker's polling loop. It is safe to call more than once.
func (w *worker) stop() {
	w.removeOnce.Do(func() { close(w.removed) })
}

// commit publishes one poll's outcome, unless the device has been removed.
//
// The check and the publishing are ONE step. Asking retired() and then
// publishing left a window: RemoveDevice could close removed in between, and
// the retired worker would go on to write reachability over its replacement's
// and hand the exporter samples stamped BEFORE the replacement's -- which is
// the out-of-order remote write this whole branch exists to stop.
//
// Holding a lock across the commit is affordable because the commit no longer
// touches the network. Export buffers the samples and hands whole batches to a
// sender goroutine, so the slowest thing here is a queue walk -- which is what
// lets RemoveDevice wait for a commit in progress without reintroducing the
// control-plane stall TestRemoveDeviceDoesNotWaitForAnInFlightPoll forbids.
// Before the sender existed this fix was not available: the export could block
// on a remote-write request.
func (w *worker) commit(ctx context.Context, result *PollResult, health cycle) bool {
	w.commitMu.Lock()
	defer w.commitMu.Unlock()

	if w.retired() {
		log.Printf("Discarding the poll of %s that finished after the device was removed: %d results",
			w.device.Name, len(result.Results))
		return false
	}

	// The device either took part in this cycle or it did not, and that -- not
	// whether every object came back -- is what decides its schedule.
	//
	// Recording the outcome IS scheduling the retry. They were two steps, the
	// second of them after poll returned, and a status read between them saw a
	// device reported unreachable with no retry time at all (#33).
	w.health.record(!health.unreachable())

	if w.exporter != nil && len(result.Results) > 0 {
		if exportErr := w.exporter.ExportSNMPResults(ctx, w.device.Name, result.Results, w.device.Tags); exportErr != nil {
			log.Printf("Failed to export metrics for device %s: %v", w.device.Name, exportErr)
		}
	}

	w.report(ctx, result)
	return true
}

// awaitCommit returns once no commit is in progress.
//
// A barrier rather than a critical section: what matters is that a commit which
// started BEFORE removal has finished, and that any commit starting after it
// finds the device gone. Holding the lock beyond that would serve nothing --
// the worker is already stopped.
func (w *worker) awaitCommit() {
	w.commitMu.Lock()
	w.commitMu.Unlock() //nolint:gocritic,staticcheck // a barrier: the wait IS the point
}

// retired reports whether this worker's device has been removed.
//
// Removal does not interrupt the poll already running -- deliberately, because
// the poll may be stuck in a request to a device that has stopped answering,
// and the control plane must not stall behind it
// (TestRemoveDeviceDoesNotWaitForAnInFlightPoll). So a poll can finish after
// its device is gone, and a device UPDATE is a remove followed by an add: the
// replacement worker is already polling by then.
func (w *worker) retired() bool {
	select {
	case <-w.removed:
		return true
	default:
		return false
	}
}

// PollResult represents the result of a polling operation
type PollResult struct {
	DeviceName string
	Timestamp  time.Time
	Results    []*snmp.Result
	Error      error
}

// New creates a new Poller instance
func New(cfg *config.Config, mibLoader *loader.Loader, opts ...Option) *Poller {
	return NewWithExporter(cfg, mibLoader, nil, opts...)
}

// NewWithExporter creates a new Poller instance with a metrics exporter
func NewWithExporter(cfg *config.Config, mibLoader *loader.Loader, exporter metrics.Exporter, opts ...Option) *Poller {
	timeout := time.Duration(cfg.Global.SNMPTimeout) * time.Second
	retries := cfg.Global.SNMPRetries

	p := &Poller{
		config:     cfg,
		mibLoader:  mibLoader,
		exporter:   exporter,
		pool:       snmp.NewPool(timeout, retries),
		registry:   make(map[string]*registration),
		resultChan: make(chan *PollResult, 100),
		logger:     logging.Discard(),
		stopChan:   make(chan struct{}),
		baseCtx:    context.Background(),
	}

	for _, opt := range opts {
		opt(p)
	}

	return p
}

// Start begins polling all enabled devices
func (p *Poller) Start(ctx context.Context) error {
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return fmt.Errorf("poller has been stopped and cannot be restarted")
	}
	// Devices added later through the control API run for the process's
	// lifetime, which is this context, not the lifetime of the request that
	// added them.
	p.baseCtx = ctx
	p.mu.Unlock()

	enabledDevices := p.config.GetEnabledDevices()
	if len(enabledDevices) == 0 {
		log.Println("No enabled devices to poll - poller will wait for devices to be added via API")
	} else {
		// Start workers for each enabled device
		for i := range enabledDevices {
			device := &enabledDevices[i]

			// The store holds only the auto-loaded modules at this point, so
			// anything else this device is assigned has to be parsed before its
			// OIDs can be selected.
			p.hydrateModules(ctx, device)

			if err := p.startWorker(device); err != nil {
				return fmt.Errorf("failed to start worker for device %s: %w", device.Name, err)
			}
		}
	}

	return nil
}

// GetMIBLoader returns the MIB loader for dynamic module loading
func (p *Poller) GetMIBLoader() *loader.Loader {
	return p.mibLoader
}

// Stop gracefully stops all polling workers
func (p *Poller) Stop() error {
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return nil
	}
	p.stopped = true
	p.mu.Unlock()

	// Signal all workers to stop
	close(p.stopChan)

	// Wait for all workers to finish
	p.wg.Wait()

	// Then drop what they published. Workers no longer forget their own device
	// on the way out -- the name is not theirs to delete, since a replacement
	// may already be publishing under it -- so stopping the poller has to do
	// it, or every device it was watching keeps a reachability series that
	// nothing will ever update again.
	//
	// After wg.Wait, which means no worker is left to republish.
	p.mu.Lock()
	for _, entry := range p.registry {
		if entry.live() {
			entry.worker.health.forget()
		}
	}
	p.mu.Unlock()

	// Close all SNMP connections
	if err := p.pool.CloseAll(); err != nil {
		return fmt.Errorf("error closing SNMP connections: %w", err)
	}

	// Close result channel
	close(p.resultChan)

	return nil
}

// Results returns the channel for receiving poll results
func (p *Poller) Results() <-chan *PollResult {
	return p.resultChan
}

// startWorker starts a polling worker for a device
func (p *Poller) startWorker(device *config.Device) error {
	// Select OIDs based on device modules or profile (if MIB loader is available)
	var oids []*mib.OIDMetadata
	if p.mibLoader != nil {
		// If device has explicit modules assigned, use those instead of profile
		if len(device.Modules) > 0 {
			// Select OIDs from specified modules
			storageInterface := p.mibLoader.GetStorage()
			mibStorage, ok := storageInterface.(*storage.Storage)
			if !ok || mibStorage == nil {
				return fmt.Errorf("failed to get MIB storage")
			}

			// Read only the modules this device polls. Listing the whole
			// store and filtering costs the size of the catalog — 29,000
			// objects on a install with the vendor MIBs loaded — to answer a
			// question about a few hundred.
			var moduleOIDs []*mib.OIDMetadata
			for _, module := range device.Modules {
				found, err := mibStorage.ListModuleOIDs(module)
				if err != nil {
					return fmt.Errorf("failed to list OIDs for module %s: %w", module, err)
				}
				moduleOIDs = append(moduleOIDs, found...)
			}

			oids = selectModuleOIDs(moduleOIDs, device)

			log.Printf("Selected %d OIDs for device %s from %d modules",
				len(oids), device.Name, len(device.Modules))
		} else {
			// Use profile-based OID selection
			profileName := device.Profile
			if profileName == "" {
				profileName = "default"
			}

			profileMgr := p.mibLoader.GetProfileManager()
			var err error
			oids, err = profileMgr.SelectOIDsForProfile(profileName)
			if err != nil {
				return fmt.Errorf("failed to select OIDs for profile %s: %w", profileName, err)
			}

			if len(oids) == 0 {
				log.Printf("Warning: no OIDs selected for device %s with profile %s", device.Name, profileName)
			} else {
				log.Printf("Selected %d OIDs for device %s with profile %s", len(oids), device.Name, profileName)
			}
		}
	}

	// Create worker without SNMP client (will be lazy-initialized on first poll)
	interval := device.GetPollInterval(p.config.Global.DefaultPollInterval)
	if interval <= 0 {
		// Rejected rather than defaulted: a worker whose schedule is zero polls
		// in a loop as fast as the device will answer, and the device did not
		// ask to be polled at all.
		return &IntervalError{Device: device.Name, Interval: interval}
	}

	w := &worker{
		device:     device,
		client:     nil, // Lazy initialization
		oids:       oids,
		interval:   interval,
		stopChan:   p.stopChan,
		resultChan: p.resultChan,
		exporter:   p.exporter,
		pool:       p.pool, // Need pool reference for lazy client creation
		logger:     p.logger.With("device", device.Name),
		removed:    make(chan struct{}),
		health:     newReachability(device.Name, interval),
	}

	// Store worker (caller must hold p.mu lock)
	p.registry[device.Name] = &registration{worker: w}

	// Start worker goroutine. ctx bounds the work this call may do; the worker
	// itself runs under the poller's own lifetime.
	p.wg.Add(1)
	go w.run(p.baseCtx, &p.wg)

	log.Printf("Started worker for device %s (profile: %s, interval: %s)", device.Name, device.Profile, interval)

	return nil
}

// run executes the polling loop for a worker.
//
// The wait between polls is asked for after each one rather than fixed by a
// ticker, because a device that is not answering must be retried on a widening
// interval instead of on the schedule it was given while it worked. It also
// means the interval is measured from the end of a poll: a cycle that overruns
// its interval no longer has the next one queued behind it the instant it
// finishes.
func (w *worker) run(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	// A worker does NOT drop its device's published reachability on the way
	// out, because the name is not its own: a device update is a remove
	// followed by an add, so by the time an exiting worker reached a deferred
	// forget, the replacement had usually already published. Deleting then
	// takes the REPLACEMENT's series away, and a missing series is worse than
	// a stale one -- an alert with nothing to evaluate does not fire. Every
	// poll republishes, but the next poll can be a whole backoff interval
	// away, and that gap is silent.
	//
	// RemoveDevice does the forgetting, once nothing more can be published for
	// the device. That was not available before: the comment here used to say
	// so, because a poll in flight could republish after the delete. It cannot
	// any more -- a retired poll commits nothing, under the lock RemoveDevice
	// takes on its way out.

	timer := time.NewTimer(w.interval)
	defer timer.Stop()

	for {
		// The first poll happens immediately; every later one waits out the
		// delay the previous poll's outcome earned.
		w.poll(ctx)

		// The wait was decided when the poll's outcome was recorded, so that
		// the retry an operator is told about is the one actually waited for.
		delay := w.health.delay()
		if w.health.unreachable() {
			log.Printf("Device %s is unreachable (%d consecutive failed polls); next attempt in %s",
				w.device.Name, w.health.failures(), delay)
		}

		if !timer.Stop() {
			// Drain a timer that fired while the poll was running, so the wait
			// below is the delay just computed and not zero.
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(delay)

		select {
		case <-ctx.Done():
			return
		case <-w.stopChan:
			return
		case <-w.removed:
			return
		case <-timer.C:
		}
	}
}

// ensureClient lazy-initializes the SNMP client if needed
func (w *worker) ensureClient() error {
	w.clientMu.Lock()
	defer w.clientMu.Unlock()

	if w.client != nil {
		return nil
	}

	// Create SNMP client
	client, err := w.pool.GetOrCreate(w.device)
	if err != nil {
		return fmt.Errorf("failed to create SNMP client: %w", err)
	}

	w.client = client
	log.Printf("Initialized SNMP client for device %s", w.device.Name)
	return nil
}

// isScalarOID determines if an OID is a scalar that needs .0 appended
// An OID is scalar if it's NOT a table, column, or table entry
func isScalarOID(oid *mib.OIDMetadata) bool {
	// If it's a table, column, or table entry, it's NOT scalar
	if oid.IsTable || oid.IsColumn || oid.IsTableEntry {
		return false
	}
	// Everything else (including objects marked as IsScalar=false) is scalar
	return true
}

// ensureScalarSuffix ensures a scalar OID ends with .0
func ensureScalarSuffix(oidStr string) string {
	// Check if it doesn't already end with .0
	if len(oidStr) >= 2 && oidStr[len(oidStr)-2:] == ".0" {
		return oidStr
	}
	return oidStr + ".0"
}

// poll performs a single polling operation
func (w *worker) poll(ctx context.Context) {
	timestamp := time.Now()

	// Lazy-initialize client on first poll
	if err := w.ensureClient(); err != nil {
		log.Printf("Failed to initialize client for device %s: %v", w.device.Name, err)
		// Through commit, like every other outcome.
		//
		// This path used to publish directly, so a removal landing while client
		// initialization was failing passed awaitCommit, returned, and then the
		// retired worker published an unreachable gauge and a result anyway --
		// and its deferred forget could delete a same-name replacement's
		// series. The original defect, alive through the one path the new
		// invariant did not cover.
		//
		// A partial invariant is worse than none: the comments and tests here
		// now say every poll outcome is atomic with removal, so the next reader
		// stops looking. Failure is an outcome.
		//
		// A device whose client will not even be built collected nothing, so it
		// backs off exactly like one that answered nothing: the address is
		// wrong or the socket cannot be had, and neither is fixed by trying
		// again in a minute.
		w.commit(ctx, &PollResult{
			DeviceName: w.device.Name,
			Timestamp:  timestamp,
			Error:      &ClientError{Device: w.device.Name, Err: err},
		}, cycle{silent: 1})
		return
	}

	// Separate scalar OIDs from table/column OIDs
	var scalarOIDs []string
	var tableOIDs []*mib.OIDMetadata

	// A device can carry thousands of OIDs and this runs on every poll, so the
	// per-OID classification trace is skipped outright rather than gated one
	// record at a time.
	traceOIDs := w.logger.Enabled(ctx, slog.LevelDebug)

	for _, oid := range w.oids {
		// Check if this is a table, column, or table entry OID
		switch {
		case oid.IsTable || oid.IsColumn || oid.IsTableEntry:
			// For table entries (rows), we don't walk them directly
			// For tables and columns, we walk them
			if !oid.IsTableEntry {
				tableOIDs = append(tableOIDs, oid)
				if traceOIDs {
					w.logger.DebugContext(ctx, "adding table OID",
						"oid", oid.Name, "is_table", oid.IsTable, "is_column", oid.IsColumn)
				}
			}
		case oid.IsScalar:
			// Scalar OID - need to append .0 for SNMP GET
			// Only poll OIDs explicitly marked as scalar (not organizational/parent nodes)
			oidStr := ensureScalarSuffix(oid.OID)
			if traceOIDs {
				w.logger.DebugContext(ctx, "adding scalar OID",
					"oid", oid.Name, "polled_oid", oidStr, "is_scalar", oid.IsScalar,
					"is_table", oid.IsTable, "is_column", oid.IsColumn, "is_table_entry", oid.IsTableEntry)
			}
			scalarOIDs = append(scalarOIDs, oidStr)
		default:
			// Skip organizational/parent nodes (IsScalar=false, not a table/column/entry)
			if traceOIDs {
				w.logger.DebugContext(ctx, "skipping organizational node",
					"oid", oid.Name, "is_scalar", oid.IsScalar, "is_table", oid.IsTable,
					"is_column", oid.IsColumn, "is_table_entry", oid.IsTableEntry)
			}
		}
	}

	w.logger.DebugContext(ctx, "classified OIDs",
		"total", len(w.oids), "scalars", len(scalarOIDs), "tables", len(tableOIDs))

	// If no OIDs selected, fallback to basic system OIDs
	if len(scalarOIDs) == 0 && len(tableOIDs) == 0 {
		scalarOIDs = []string{
			"1.3.6.1.2.1.1.1.0", // sysDescr
			sysUpTimeOID,
			"1.3.6.1.2.1.1.5.0", // sysName
		}
	}

	// Real agents cap how much they will put in one response: a Cisco ASR1000
	// answers tooBig beyond roughly 30 varbinds. GetMultiple splits on tooBig so
	// correctness does not depend on this number, but starting small avoids
	// paying for that split on every poll.
	const maxOIDsPerRequest = 25

	var allResults []*snmp.Result
	var pollErr error

	// health is this cycle's running answer to "is the device talking at all",
	// which decides whether the remaining operations are worth attempting.
	var health cycle

	// planned is the SNMP operations this cycle performs if the device answers —
	// one per chunk of scalars and one per table — and attempted is how many of
	// them were actually sent. The difference is what an abandoned cycle did not
	// ask for, which is the whole point of abandoning it.
	planned := len(tableOIDs) + (len(scalarOIDs)+maxOIDsPerRequest-1)/maxOIDsPerRequest
	attempted := 0

	// Poll scalar OIDs using GetMultiple (batch operation with chunking)
	if len(scalarOIDs) > 0 {
		// Chunk OIDs into batches to avoid exceeding MaxOids limit
		for i := 0; i < len(scalarOIDs); i += maxOIDsPerRequest {
			if w.abandon(&health) {
				break
			}
			attempted++

			end := i + maxOIDsPerRequest
			if end > len(scalarOIDs) {
				end = len(scalarOIDs)
			}

			chunk := scalarOIDs[i:end]
			w.logger.DebugContext(ctx, "polling scalar OID chunk",
				"oids", len(chunk),
				"chunk", i/maxOIDsPerRequest+1,
				"chunks", (len(scalarOIDs)+maxOIDsPerRequest-1)/maxOIDsPerRequest)
			// Slicing the sample is only worth doing if it will be printed.
			if i == 0 && len(chunk) > 0 && traceOIDs {
				w.logger.DebugContext(ctx, "first chunk sample", "oids", chunk[:min(5, len(chunk))])
			}
			results, err := w.client.GetMultiple(chunk)
			if err != nil {
				log.Printf("Error polling scalar OID chunk (%d-%d/%d) for device %s: %v",
					i, end, len(scalarOIDs), w.device.Name, err)
				if pollErr == nil {
					pollErr = err
				}
				health.record(err)
				continue
			}
			health.record(nil)
			w.logger.DebugContext(ctx, "scalar chunk returned results", "results", len(results))
			allResults = append(allResults, results...)
		}

		if pollErr == nil && len(allResults) > 0 {
			log.Printf("Successfully polled %d scalar OIDs in %d chunks for device %s",
				len(scalarOIDs), (len(scalarOIDs)+maxOIDsPerRequest-1)/maxOIDsPerRequest, w.device.Name)
		}
	}

	// Walk table/column OIDs using BulkWalkAll
	for _, tableOID := range tableOIDs {
		if w.abandon(&health) {
			break
		}
		attempted++

		results, err := w.client.BulkWalkAll(tableOID.OID)
		if err != nil {
			log.Printf("Error walking table OID %s (%s) for device %s: %v",
				tableOID.Name, tableOID.OID, w.device.Name, err)
			if pollErr == nil {
				pollErr = err
			}
			health.record(err)
			continue
		}
		health.record(nil)

		if len(results) > 0 {
			log.Printf("Walked table %s (%s) on device %s: got %d results",
				tableOID.Name, tableOID.OID, w.device.Name, len(results))
			allResults = append(allResults, results...)
		}
	}

	if health.unreachable() {
		skipped := planned - attempted
		pollErr = &UnreachableError{
			Device:   w.device.Name,
			Attempts: health.silent,
			Skipped:  skipped,
			Err:      pollErr,
		}
		log.Printf("Device %s answered none of its %d SNMP operations; %d more skipped this cycle",
			w.device.Name, health.silent, skipped)
	}

	// Everything this poll produced is published, or none of it is. See commit.
	w.commit(ctx, &PollResult{
		DeviceName: w.device.Name,
		Timestamp:  timestamp,
		Results:    allResults,
		Error:      pollErr,
	}, health)
}

// abandon reports whether the rest of this poll should be skipped because the
// device is not answering anything.
//
// Reaching the silence threshold is not enough on its own to conclude that. The
// objects polled first are whichever ones the selection happened to put first,
// and a device can have a wedged subtree at the front of its list while
// answering everything after it perfectly well — so before giving up on the
// rest of the cycle, the device is asked the one question every SNMP agent can
// answer. If sysUpTime comes back, the silence was about those objects and not
// about the device: the cycle carries on and nothing is marked unreachable.
//
// The probe costs one GET, once, and only in a cycle that is already failing.
// Being wrong in the other direction — treating a device that answers most of
// its objects as dead — would stop collecting real data, which is far worse
// than paying for one extra request on a device that is genuinely down.
func (w *worker) abandon(c *cycle) bool {
	if !c.abandoned() {
		return false
	}
	if c.probed {
		return true
	}
	c.probed = true

	_, err := w.client.GetMultiple([]string{sysUpTimeOID})
	if err != nil {
		log.Printf("Device %s did not answer %d operations; direct probe of sysUpTime also failed: %v",
			w.device.Name, c.silent, err)
	}

	return c.record(err)
}

// report hands one poll's outcome to whoever is draining the results.
//
// Every poll reports, including the ones that collected nothing: a device that
// never reports is indistinguishable from a device that has not started yet,
// which is how an unreachable device managed to record neither a poll nor a
// failure for hours.
//
// A reader that has fallen behind loses the result rather than stalling the
// polling loop, because the loop is what keeps every other device on schedule.
func (w *worker) report(ctx context.Context, result *PollResult) {
	select {
	case w.resultChan <- result:
	default:
		w.logger.DebugContext(ctx, "dropped a poll result: the results channel is full",
			"results", len(result.Results))
	}
}

// GetWorkerStatus returns the status of all workers
func (p *Poller) GetWorkerStatus() map[string]WorkerStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()

	status := make(map[string]WorkerStatus, len(p.registry))
	for name, entry := range p.registry {
		if !entry.live() {
			continue
		}
		w := entry.worker
		status[name] = p.statusOf(w)
	}

	return status
}

// WorkerStatus returns one worker's status, and whether the poller has a worker
// for that device at all.
//
// Answering for a single device exists so a status request does not have to
// build the whole fleet's status to read one row of it.
func (p *Poller) WorkerStatus(deviceName string) (WorkerStatus, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	w := p.liveWorker(deviceName)
	if w == nil {
		return WorkerStatus{}, false
	}
	return p.statusOf(w), true
}

// statusOf describes one worker. The caller holds p.mu.
//
// The three reachability fields come from ONE snapshot rather than three
// separate reads, so the row an operator is shown is a state the device was
// actually in.
func (p *Poller) statusOf(w *worker) WorkerStatus {
	health := w.health.snapshot()
	return WorkerStatus{
		DeviceName:          w.device.Name,
		Device:              w.device,
		Interval:            w.interval,
		Running:             !p.stopped,
		Unreachable:         health.consecutive > 0,
		ConsecutiveFailures: int(health.consecutive),
		NextPoll:            health.nextPoll,
	}
}

// WorkerStatus represents the status of a polling worker
type WorkerStatus struct {
	DeviceName string
	Device     *config.Device
	Interval   time.Duration
	Running    bool

	// Unreachable is set while the device is answering no SNMP at all. It is
	// not set for a device that answers some objects and fails on others: that
	// device is reachable and stays on its normal interval.
	Unreachable bool

	// ConsecutiveFailures counts the polls in a row the device answered no part
	// of. It is what the retry interval is derived from, so it is also the
	// explanation for a NextPoll further away than Interval.
	ConsecutiveFailures int

	// NextPoll is when the worker will next try the device, which for an
	// unreachable device is further out than Interval by its backoff.
	NextPoll time.Time
}

// AddDevice dynamically adds a device to the poller
func (p *Poller) AddDevice(ctx context.Context, device *config.Device) error {
	// A caller that has already given up gets no worker: the device would run
	// unowned until the next configuration sync noticed it.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("add device %s: %w", device.Name, err)
	}

	// Validate device
	if err := device.Validate(); err != nil {
		return fmt.Errorf("invalid device: %w", err)
	}

	// Load whatever MIB modules this device was assigned but the store does not
	// hold yet. It happens before the poller lock is taken because parsing is
	// slow enough to be felt: see hydrateModules.
	p.hydrateModules(ctx, device)

	// A device of this name may still be retiring, in which case its client is
	// about to be closed. Taking the name now would hand this worker a
	// connection the removal is about to shut, and ensureClient never
	// reacquires once its field is set -- so the device would poll a dead
	// socket permanently.
	//
	// The wait and the registration happen under ONE hold of p.mu, and the
	// re-check is at the TOP of the loop rather than before it. Checking the
	// name free and then taking the lock to install is a check across a lock
	// release: RemoveDevice can claim the name in the gap, and this call then
	// finds no worker -- because the removal has already deleted it -- and
	// starts a replacement onto the pool entry that removal is on its way to
	// close. The gate would report a name it had genuinely observed free, and
	// the worker would poll a dead socket for good.
	//
	// Waiting is still per-name: the lock is released while blocked, so another
	// device's removal is not delayed by this one.
	p.mu.Lock()
	for {
		if p.stopped {
			p.mu.Unlock()
			return fmt.Errorf("poller is stopped")
		}

		// ONE lookup answers the whole question. Absent is free, a worker is
		// taken, a retirement is wait -- and there is no fourth reading to
		// forget, which is what the two maps kept producing.
		entry, held := p.registry[device.Name]
		if !held {
			break
		}
		if entry.live() {
			p.mu.Unlock()
			return fmt.Errorf("device %s already exists", device.Name)
		}

		retired := entry.retired
		p.mu.Unlock()
		select {
		case <-retired:
		case <-ctx.Done():
			return fmt.Errorf("add device %s: %w", device.Name, ctx.Err())
		}
		p.mu.Lock()
	}
	defer p.mu.Unlock()

	// Start worker for the new device
	return p.startWorker(device)
}

// RemoveDevice dynamically removes a device from the poller.
//
// The poller lock is released before the connection is torn down. Closing an
// SNMP client does not wait for a request in flight -- it takes the client's
// read lock and closes the socket, interrupting that request -- but it does
// queue behind Connect, which holds the client's write lock for the whole
// dial and so, for an unreachable device, can take a long time. Teardown is
// therefore not bounded, and holding the poller lock across it would stall
// every other device's registration and status lookup behind one dead box.
func (p *Poller) RemoveDevice(deviceName string) error {
	p.mu.Lock()
	w := p.liveWorker(deviceName)
	if w == nil {
		p.mu.Unlock()
		return fmt.Errorf("device %s not found", deviceName)
	}

	// Live becomes retiring in ONE write. The worker leaves and the claim
	// arrives together, so there is no instant in which the name looks free to
	// anybody -- which is what the two maps could not promise, however
	// carefully both were updated.
	retired := make(chan struct{})
	p.registry[deviceName] = &registration{retired: retired}
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		delete(p.registry, deviceName)
		p.mu.Unlock()
		// Closed after the key is gone, so a waiter that wakes finds the name
		// free rather than racing the delete it was waiting for.
		close(retired)
	}()

	// Stop the polling loop. Without this the goroutine outlives its entry in
	// the map and keeps polling a device nobody asked for any more.
	w.stop()

	// Then wait out any commit already in progress, so this returns only once
	// nothing more can be published for the device. The poll itself is NOT
	// waited for -- the control plane must not stall behind a device that has
	// stopped answering -- but a commit holds no sockets and does no network
	// work, so waiting for one is bounded.
	//
	w.awaitCommit()

	// Now that nothing more can be published for this device, its series goes.
	// Done here rather than by the worker, which does not own the name.
	w.health.forget()

	if err := p.pool.Remove(w.device.Name); err != nil {
		return fmt.Errorf("failed to remove device from pool: %w", err)
	}

	return nil
}

// GetPoolSize returns the number of SNMP clients in the pool
func (p *Poller) GetPoolSize() int {
	return p.pool.Size()
}

// HealthCheck performs a health check on all devices
func (p *Poller) HealthCheck() map[string]error {
	return p.pool.HealthCheck()
}
