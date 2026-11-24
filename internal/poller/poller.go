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
	"github.com/jeremyhahn/go-netmon/internal/credential"
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
	// sealer seals each device's SNMP credentials when its worker starts.
	// See WithSealer.
	sealer credential.Sealer
	// registry is who owns each device name now; one map so a single lookup answers.
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

	// baseCtx is the workers' lifetime, not the context of the request that added a device.
	baseCtx context.Context
}

// registration is a name's entry: absent is free, worker set is live, retired set is being retired.
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

// liveWorker returns the worker for a name, or nil when free or retiring; the caller holds p.mu.
func (p *Poller) liveWorker(name string) *worker {
	entry := p.registry[name]
	if !entry.live() {
		return nil
	}
	return entry.worker
}

// Option configures a Poller at construction.
type Option func(*Poller)

// WithLogger routes the poller's diagnostics to logger; without it they are discarded.
func WithLogger(logger *slog.Logger) Option {
	return func(p *Poller) { p.logger = logging.OrDiscard(logger) }
}

// WithSealer sets the sealer the poller keeps device credentials in. Without
// it the poller uses credential.Default, the process's one go-enclave
// provider.
func WithSealer(sealer credential.Sealer) Option {
	return func(p *Poller) {
		if sealer != nil {
			p.sealer = sealer
		}
	}
}

// worker represents a polling worker for a single device
type worker struct {
	// device is the worker's own copy of the device, with its credentials
	// sealed out of it into creds. The worker owns creds; its pooled client
	// borrows them, and the poller destroys them once the device is gone.
	device     *config.Device
	creds      *snmp.Credentials
	client     *snmp.Client
	oids       []*mib.OIDMetadata // OIDs to poll for this device
	interval   time.Duration
	stopChan   chan struct{}
	resultChan chan<- *PollResult
	exporter   metrics.Exporter
	pool       *snmp.Pool // For lazy client initialization
	logger     *slog.Logger
	clientMu   sync.Mutex // Protects client initialization

	// removed is closed when this worker's device is removed, ending its loop.
	removed    chan struct{}
	removeOnce sync.Once

	// commitMu is held across a poll's commit and by RemoveDevice, so ownership is checked inside the commit.
	commitMu sync.Mutex

	// health decides how long to wait before the next poll and carries whether
	// the device is answering at all. See reachability.
	health *reachability
}

// stop ends this worker's polling loop. It is safe to call more than once.
func (w *worker) stop() {
	w.removeOnce.Do(func() { close(w.removed) })
}

// commit publishes one poll's outcome unless the device was removed, as one locked step; it does no network I/O.
func (w *worker) commit(ctx context.Context, result *PollResult, health cycle) bool {
	w.commitMu.Lock()
	defer w.commitMu.Unlock()

	if w.retired() {
		log.Printf("Discarding the poll of %s that finished after the device was removed: %d results",
			w.device.Name, len(result.Results))
		return false
	}

	// Recording the outcome also schedules the retry.
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
func (w *worker) awaitCommit() {
	w.commitMu.Lock()
	w.commitMu.Unlock() //nolint:gocritic,staticcheck // a barrier: the wait IS the point
}

// retired reports whether this worker's device has been removed; a poll in flight is not interrupted.
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
		sealer:     credential.Default(),
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

	// Every worker now holds its device's credentials sealed, and nothing
	// reads them from the loaded configuration again, so it keeps none.
	p.config.ScrubCredentials()

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

	// After wg.Wait, forget what the workers published, since workers do not forget their own device.
	p.mu.Lock()
	var creds []*snmp.Credentials
	for _, entry := range p.registry {
		if entry.live() {
			entry.worker.health.forget()
			creds = append(creds, entry.worker.creds)
		}
	}
	p.mu.Unlock()

	// Close all SNMP connections, then destroy the credentials they borrowed.
	closeErr := p.pool.CloseAll()
	for _, c := range creds {
		_ = c.Destroy()
	}
	if closeErr != nil {
		return fmt.Errorf("error closing SNMP connections: %w", closeErr)
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

			// Read only the modules this device polls, not the whole store.
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

	// The worker keeps its own copy of the device, and the copy keeps no
	// credentials: they are sealed here and opened only for each request.
	owned := *device
	creds, err := snmp.SealCredentials(context.Background(), p.sealer, &owned)
	if err != nil {
		return err
	}

	w := &worker{
		device:     &owned,
		creds:      creds,
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

// run executes a worker's polling loop, asking for the next wait after each poll so retries can back off.
func (w *worker) run(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	// A worker does not drop its published reachability on exit; RemoveDevice does, once nothing more can be published.

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
	client, err := w.pool.GetOrCreateWithCredentials(w.device, w.creds)
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
		// Through commit, like every outcome; a client that cannot be built backs off like a silent device.
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

	// Start small: some agents answer tooBig beyond about 30 varbinds; GetMultiple splits on tooBig.
	const maxOIDsPerRequest = 25

	var allResults []*snmp.Result
	var pollErr error

	// health is this cycle's running answer to "is the device talking at all",
	// which decides whether the remaining operations are worth attempting.
	var health cycle

	// planned counts this cycle's SNMP operations and attempted those sent.
	planned := len(tableOIDs) + (len(scalarOIDs)+maxOIDsPerRequest-1)/maxOIDsPerRequest
	attempted := 0

	// Poll scalar OIDs using GetMultiple (batch operation with chunking)
	if len(scalarOIDs) > 0 {
		// Chunk OIDs into batches to avoid exceeding MaxOids limit
		for i := 0; i < len(scalarOIDs); i += maxOIDsPerRequest {
			if w.abandon(ctx, &health) {
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
			results, err := w.client.GetMultipleContext(ctx, chunk)
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
		if w.abandon(ctx, &health) {
			break
		}
		attempted++

		results, err := w.client.BulkWalkAllContext(ctx, tableOID.OID)
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

// abandon reports whether to skip the rest of the poll: past the silence threshold and sysUpTime also fails.
func (w *worker) abandon(ctx context.Context, c *cycle) bool {
	if !c.abandoned() {
		return false
	}
	if c.probed {
		return true
	}
	c.probed = true

	_, err := w.client.GetMultipleContext(ctx, []string{sysUpTimeOID})
	if err != nil {
		log.Printf("Device %s did not answer %d operations; direct probe of sysUpTime also failed: %v",
			w.device.Name, c.silent, err)
	}

	return c.record(err)
}

// report hands one poll's outcome to the results reader, dropping it rather than blocking if the reader is behind.
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

// WorkerStatus returns one worker's status and whether the poller has a worker for that device.
func (p *Poller) WorkerStatus(deviceName string) (WorkerStatus, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	w := p.liveWorker(deviceName)
	if w == nil {
		return WorkerStatus{}, false
	}
	return p.statusOf(w), true
}

// statusOf describes one worker from one reachability snapshot; the caller holds p.mu.
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

	// Wait for a retiring device of this name and register under one hold of p.mu, re-checking at the top of the loop.
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
			return &DeviceExistsError{Device: device.Name}
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

// RemoveDevice removes a device; the poller lock is released before the possibly slow connection teardown.
func (p *Poller) RemoveDevice(deviceName string) error {
	p.mu.Lock()
	w := p.liveWorker(deviceName)
	if w == nil {
		p.mu.Unlock()
		return fmt.Errorf("device %s not found", deviceName)
	}

	// Live becomes retiring in one write, so the name never looks free.
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

	// Wait out a commit in progress (bounded, no network I/O), not the poll itself.
	//
	w.awaitCommit()

	// Now that nothing more can be published for this device, its series goes.
	// Done here rather than by the worker, which does not own the name.
	w.health.forget()

	// Also evict the exporter's friendly names for the device, after awaitCommit.
	if forgetter, ok := p.exporter.(metrics.DeviceForgetter); ok {
		forgetter.ForgetDevice(deviceName)
	}

	// The credentials go with the device, whether or not its connection
	// closes cleanly. A poll still in flight fails rather than reopening them.
	defer func() { _ = w.creds.Destroy() }()

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
