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
	config     *config.Config
	mibLoader  *loader.Loader
	exporter   metrics.Exporter
	pool       *snmp.Pool
	workers    map[string]*worker
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

	// health decides how long to wait before the next poll and carries whether
	// the device is answering at all. See reachability.
	health *reachability
}

// stop ends this worker's polling loop. It is safe to call more than once.
func (w *worker) stop() {
	w.removeOnce.Do(func() { close(w.removed) })
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
		workers:    make(map[string]*worker),
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
	p.workers[device.Name] = w

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

	// The worker is the only writer of its device's published reachability, so
	// it is also what drops it. Doing that from RemoveDevice instead would race
	// with the poll already in flight, which would republish the device after
	// it had been deleted and leave a decommissioned box on a dashboard.
	defer w.health.forget()

	timer := time.NewTimer(w.interval)
	defer timer.Stop()

	for {
		// The first poll happens immediately; every later one waits out the
		// delay the previous poll's outcome earned.
		w.poll(ctx)

		delay := w.health.schedule()
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
		// A device whose client will not even be built collected nothing, so it
		// backs off exactly like one that answered nothing: the address is
		// wrong or the socket cannot be had, and neither is fixed by trying
		// again in a minute.
		w.health.silent()
		w.report(ctx, &PollResult{
			DeviceName: w.device.Name,
			Timestamp:  timestamp,
			Error:      &ClientError{Device: w.device.Name, Err: err},
		})
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

	// The device either took part in this cycle or it did not, and that -- not
	// whether every object came back -- is what decides its schedule.
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
		w.health.silent()
	} else {
		w.health.answered()
	}

	// Export metrics if exporter is available
	if w.exporter != nil && len(allResults) > 0 {
		// Export with device tags
		if exportErr := w.exporter.ExportSNMPResults(ctx, w.device.Name, allResults, w.device.Tags); exportErr != nil {
			log.Printf("Failed to export metrics for device %s: %v", w.device.Name, exportErr)
		}
	}

	w.report(ctx, &PollResult{
		DeviceName: w.device.Name,
		Timestamp:  timestamp,
		Results:    allResults,
		Error:      pollErr,
	})
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

	status := make(map[string]WorkerStatus, len(p.workers))
	for name, w := range p.workers {
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

	w, exists := p.workers[deviceName]
	if !exists {
		return WorkerStatus{}, false
	}
	return p.statusOf(w), true
}

// statusOf describes one worker. The caller holds p.mu.
func (p *Poller) statusOf(w *worker) WorkerStatus {
	return WorkerStatus{
		DeviceName:          w.device.Name,
		Device:              w.device,
		Interval:            w.interval,
		Running:             !p.stopped,
		Unreachable:         w.health.unreachable(),
		ConsecutiveFailures: w.health.failures(),
		NextPoll:            w.health.due(),
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

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.stopped {
		return fmt.Errorf("poller is stopped")
	}

	if _, exists := p.workers[device.Name]; exists {
		return fmt.Errorf("device %s already exists", device.Name)
	}

	// Start worker for the new device
	return p.startWorker(device)
}

// RemoveDevice dynamically removes a device from the poller.
//
// The poller lock is released before the connection is torn down. Closing an
// SNMP client waits for whatever request that client has in flight, which for
// an unreachable device is the full retry budget; holding the poller lock
// across that would stall every other device's registration and status lookup
// behind one dead box.
func (p *Poller) RemoveDevice(deviceName string) error {
	p.mu.Lock()
	w, exists := p.workers[deviceName]
	if !exists {
		p.mu.Unlock()
		return fmt.Errorf("device %s not found", deviceName)
	}
	delete(p.workers, deviceName)
	p.mu.Unlock()

	// Stop the polling loop. Without this the goroutine outlives its entry in
	// the map and keeps polling a device nobody asked for any more.
	w.stop()

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
