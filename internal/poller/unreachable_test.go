package poller

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/credential"
	"github.com/jeremyhahn/go-netmon/internal/logging"
	"github.com/jeremyhahn/go-netmon/internal/metrics"
	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/snmp"
	"github.com/jeremyhahn/go-netmon/internal/testsupport"
)

// Workers run against the in-process SNMPv2c agent; only the SNMP time budget is scaled down.
const (
	testSNMPBudget  = 60 * time.Millisecond
	testPollTimeout = 5 * time.Second
)

// systemOID is the subtree holding sysDescr, sysUpTime and sysName. ifTableOID,
// declared alongside the selection tests, is the subtree the agent is told to
// stop answering for.
const systemOID = "1.3.6.1.2.1.1"

// answeringAgent starts an agent serving the system group and two interfaces.
func answeringAgent(t *testing.T) *testsupport.SNMPAgent {
	t.Helper()

	agent, err := testsupport.NewSNMPAgent(
		testsupport.OctetString(sysDescrOID, "go-netmon in-process test agent"),
		testsupport.TimeTicks(sysUpTimeOID, 4242),
		testsupport.OctetString(sysNameOID, "agent"),

		testsupport.Integer(ifTableOID+".1.1.1", 1),
		testsupport.OctetString(ifTableOID+".1.2.1", "eth0"),
		testsupport.Counter32(ifTableOID+".1.10.1", 1_000),
		testsupport.Integer(ifTableOID+".1.1.2", 2),
		testsupport.OctetString(ifTableOID+".1.2.2", "eth1"),
		testsupport.Counter32(ifTableOID+".1.10.2", 2_000),
	)
	if err != nil {
		t.Fatalf("starting the test SNMP agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })

	return agent
}

// scalar and table describe one selected object of each kind.
func scalar(oid string) *mib.OIDMetadata {
	return &mib.OIDMetadata{OID: oid, Name: "scalar" + oid, IsScalar: true}
}

func table(oid string) *mib.OIDMetadata {
	return &mib.OIDMetadata{OID: oid, Name: "table" + oid, IsTable: true}
}

// tableColumns names n distinct columns of the interface table, which is how a
// device ends up with a long list of walks against one subtree.
func tableColumns(n int) []*mib.OIDMetadata {
	oids := make([]*mib.OIDMetadata, 0, n)
	for i := 1; i <= n; i++ {
		oids = append(oids, table(ifTableOID+".1."+strconv.Itoa(i)))
	}
	return oids
}

// pollingWorker builds a worker around addr with no Poller behind it, so a test
// can drive one cycle at a time and read its result.
func pollingWorker(t *testing.T, addr string, interval time.Duration, oids ...*mib.OIDMetadata) (*worker, <-chan *PollResult) {
	t.Helper()

	device := &config.Device{
		Name:         t.Name(),
		Address:      addr,
		SNMPVersion:  "2c",
		Community:    "public",
		PollInterval: int(interval.Seconds()),
		Enabled:      true,
	}

	// One attempt per operation. gosnmp's retries multiply the budget and are
	// not what is being measured here; the count of operations is.
	pool := snmp.NewPool(testSNMPBudget, 0)
	t.Cleanup(func() { _ = pool.CloseAll() })

	// Sealed as startWorker seals them.
	creds, err := snmp.SealCredentials(context.Background(), credential.Default(), device)
	if err != nil {
		t.Fatalf("seal credentials: %v", err)
	}
	t.Cleanup(func() { _ = creds.Destroy() })

	results := make(chan *PollResult, 16)
	w := &worker{
		device:     device,
		creds:      creds,
		oids:       oids,
		interval:   interval,
		stopChan:   make(chan struct{}),
		removed:    make(chan struct{}),
		resultChan: results,
		pool:       pool,
		logger:     logging.Discard(),
		health:     newReachability(device.Name, interval),
	}
	t.Cleanup(w.health.forget)

	return w, results
}

// nextResult reads the poll result the worker just produced.
func nextResult(t *testing.T, results <-chan *PollResult) *PollResult {
	t.Helper()

	select {
	case result := <-results:
		return result
	case <-time.After(testPollTimeout):
		t.Fatal("the worker produced no poll result")
		return nil
	}
}

// TestSilentDeviceAbandonsTheCycle checks a silent device is not asked for every object.
func TestSilentDeviceAbandonsTheCycle(t *testing.T) {
	t.Parallel()

	agent := answeringAgent(t)
	agent.SetSilent(true)

	const tables = 10
	oids := append([]*mib.OIDMetadata{scalar(sysDescrOID), scalar(sysNameOID)}, tableColumns(tables)...)

	w, results := pollingWorker(t, agent.Addr(), time.Minute, oids...)
	w.poll(context.Background())

	// One chunk of scalars, two walks, then the sysUpTime probe that confirms
	// the device really is gone: four requests, against eleven operations the
	// device was carrying.
	const wantRequests = silenceThreshold + 1
	if got := agent.Requests(); got != wantRequests {
		t.Errorf("the device received %d requests, want %d: the poll did not stop at the threshold",
			got, wantRequests)
	}

	result := nextResult(t, results)
	var unreachable *UnreachableError
	if !errors.As(result.Error, &unreachable) {
		t.Fatalf("poll error = %v, want an UnreachableError", result.Error)
	}
	if unreachable.Attempts != wantRequests {
		t.Errorf("UnreachableError.Attempts = %d, want %d", unreachable.Attempts, wantRequests)
	}
	if unreachable.Skipped == 0 {
		t.Error("UnreachableError.Skipped = 0; the poll claims to have attempted every object")
	}
	if len(result.Results) != 0 {
		t.Errorf("a device that answered nothing produced %d results", len(result.Results))
	}

	if !w.health.unreachable() {
		t.Error("the device is not marked unreachable")
	}
	if got := testutil.ToFloat64(deviceUnreachable.WithLabelValues(w.device.Name)); got != 1 {
		t.Errorf("gonetmon_device_unreachable = %v, want 1", got)
	}
}

// TestTheSilentDeviceIsProbedOnlyOnce keeps the confirmation probe from being
// repeated: a device carrying more scalars than one request can hold reaches
// the silence threshold inside the scalar loop, and the walks that follow must
// take that answer rather than asking the dead device again.
func TestTheSilentDeviceIsProbedOnlyOnce(t *testing.T) {
	t.Parallel()

	agent := answeringAgent(t)
	agent.SetSilent(true)

	// Four chunks of scalars, so the threshold is reached before the scalar
	// loop has run out of work, plus tables that must never be attempted.
	var oids []*mib.OIDMetadata
	for i := 0; i < 76; i++ {
		oids = append(oids, scalar(sysDescrOID))
	}
	oids = append(oids, tableColumns(3)...)

	w, results := pollingWorker(t, agent.Addr(), time.Minute, oids...)
	w.poll(context.Background())

	// Three unanswered chunks and one probe. Nothing else may be sent: not the
	// fourth chunk, not any of the walks, and not a second probe.
	const wantRequests = silenceThreshold + 1
	if got := agent.Requests(); got != wantRequests {
		t.Errorf("the device received %d requests, want %d", got, wantRequests)
	}

	result := nextResult(t, results)
	var unreachable *UnreachableError
	if !errors.As(result.Error, &unreachable) {
		t.Fatalf("poll error = %v, want an UnreachableError", result.Error)
	}
	// The fourth chunk of scalars and all three tables went unasked.
	if unreachable.Skipped != 4 {
		t.Errorf("UnreachableError.Skipped = %d, want the 4 operations never attempted", unreachable.Skipped)
	}
}

// TestPartiallyAnsweringDeviceKeepsCollecting checks a device timing out on one subtree stays reachable and collected.
func TestPartiallyAnsweringDeviceKeepsCollecting(t *testing.T) {
	t.Parallel()

	agent := answeringAgent(t)
	agent.SilenceSubtree(ifTableOID)

	const wedged = 5
	oids := append(
		[]*mib.OIDMetadata{scalar(sysDescrOID), scalar(sysUpTimeOID), scalar(sysNameOID)},
		tableColumns(wedged)...,
	)

	w, results := pollingWorker(t, agent.Addr(), time.Minute, oids...)
	w.poll(context.Background())

	result := nextResult(t, results)
	if len(result.Results) != 3 {
		t.Errorf("collected %d objects, want the 3 the device answers", len(result.Results))
	}
	if result.Error == nil {
		t.Error("the wedged subtree was not reported as an error")
	}
	var unreachable *UnreachableError
	if errors.As(result.Error, &unreachable) {
		t.Errorf("a device answering 3 of its objects was reported unreachable: %v", result.Error)
	}

	// Every wedged walk was still attempted: one scalar chunk plus five walks.
	if got, want := agent.Requests(), int64(1+wedged); got != want {
		t.Errorf("the device received %d requests, want %d: part of its selection was skipped", got, want)
	}

	if w.health.unreachable() {
		t.Error("a device that answered is marked unreachable")
	}
	if got := w.health.delay(); got != w.interval {
		t.Errorf("next poll in %s, want the normal interval %s: a healthy device was backed off", got, w.interval)
	}
	if got := testutil.ToFloat64(deviceUnreachable.WithLabelValues(w.device.Name)); got != 0 {
		t.Errorf("gonetmon_device_unreachable = %v, want 0", got)
	}
}

// TestWedgedSubtreeFirstDoesNotLookUnreachable covers the ordering trap. A
// device with no scalars selected, whose first three walks all fall in a wedged
// subtree, is silent for exactly as long as the abandon rule looks at — and it
// is answering perfectly well. The direct sysUpTime probe is what tells the two
// apart, and this is what would break if it were removed.
func TestWedgedSubtreeFirstDoesNotLookUnreachable(t *testing.T) {
	t.Parallel()

	agent := answeringAgent(t)
	agent.SilenceSubtree(ifTableOID)

	oids := append(tableColumns(silenceThreshold), table(systemOID))

	w, results := pollingWorker(t, agent.Addr(), time.Minute, oids...)
	w.poll(context.Background())

	result := nextResult(t, results)
	if len(result.Results) == 0 {
		t.Error("the subtree the device does answer was never walked: the cycle was abandoned")
	}
	var unreachable *UnreachableError
	if errors.As(result.Error, &unreachable) {
		t.Errorf("a device with one wedged subtree was reported unreachable: %v", result.Error)
	}
	if w.health.unreachable() {
		t.Error("a device with one wedged subtree is marked unreachable")
	}
}

// TestBackoffWidensAcrossCyclesAndResetsOnRecovery drives whole cycles rather
// than the schedule arithmetic, so it covers the wiring between a poll's
// outcome and the wait that follows it.
func TestBackoffWidensAcrossCyclesAndResetsOnRecovery(t *testing.T) {
	t.Parallel()

	agent := answeringAgent(t)
	agent.SetSilent(true)

	const interval = time.Minute
	w, results := pollingWorker(t, agent.Addr(), interval, scalar(sysDescrOID))

	for cycle, want := range []time.Duration{interval, 2 * interval, 4 * interval} {
		w.poll(context.Background())
		if result := nextResult(t, results); result.Error == nil {
			t.Fatalf("cycle %d against a silent device reported success", cycle+1)
		}
		if got := w.health.delay(); got != want {
			t.Errorf("after %d failed cycles the next poll is in %s, want %s", cycle+1, got, want)
		}
	}

	agent.SetSilent(false)
	w.poll(context.Background())

	result := nextResult(t, results)
	if result.Error != nil {
		t.Fatalf("the recovered device still reports a failure: %v", result.Error)
	}
	if w.health.unreachable() {
		t.Error("the recovered device is still marked unreachable")
	}
	if got := w.health.delay(); got != interval {
		t.Errorf("the recovered device is polled in %s, want its normal interval %s", got, interval)
	}
}

// TestRecoveredDeviceIsPolledAgainPromptly runs the real polling loop, because
// the backoff is only as good as the wait the loop actually takes: a recovered
// device must not sit out the penalty it earned while it was down.
func TestRecoveredDeviceIsPolledAgainPromptly(t *testing.T) {
	t.Parallel()

	agent := answeringAgent(t)
	agent.SetSilent(true)

	const interval = 50 * time.Millisecond
	w, results := pollingWorker(t, agent.Addr(), interval, scalar(sysDescrOID))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go w.run(ctx, &wg)
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})

	// Two failed cycles: enough for the backoff to have widened.
	for i := 0; i < 2; i++ {
		if result := nextResult(t, results); result.Error == nil {
			t.Fatalf("cycle %d against a silent device reported success", i+1)
		}
	}
	if !w.health.unreachable() {
		t.Fatal("the device is not marked unreachable after two failed cycles")
	}

	agent.SetSilent(false)

	deadline := time.Now().Add(testPollTimeout)
	for {
		result := nextResult(t, results)
		if result.Error == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the recovered device is still failing: %v", result.Error)
		}
	}

	if w.health.unreachable() {
		t.Error("the device is still marked unreachable after answering")
	}
}

// TestFirstPollFailureIsReportedAsAFailedPoll is the "never polled" bug: a
// device whose first cycle fails must produce a poll result, because a device
// that reports nothing at all is indistinguishable from one that has not
// started yet — which is exactly how an unreachable device managed to record
// neither a poll nor an error for hours.
func TestFirstPollFailureIsReportedAsAFailedPoll(t *testing.T) {
	t.Parallel()

	agent := answeringAgent(t)
	agent.SetSilent(true)

	cfg := &config.Config{}
	cfg.Global.DefaultPollInterval = 1
	cfg.Global.SNMPTimeout = 1
	cfg.Global.SNMPRetries = 0

	p := NewWithExporter(cfg, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop() })

	device := silentDevice("bastion", agent.Addr())
	device.PollInterval = 1
	if err := p.AddDevice(ctx, device); err != nil {
		t.Fatalf("AddDevice: %v", err)
	}

	result := nextResult(t, p.Results())
	if result.Error == nil {
		t.Fatal("the first poll of a silent device reported success")
	}
	if result.Timestamp.IsZero() {
		t.Error("the failed poll carries no timestamp, so it reads as never having happened")
	}

	// The worker records the outcome and its retry before reporting the result.
	status, polling := p.WorkerStatus("bastion")
	if !polling {
		t.Fatal("the poller has no worker for bastion, which it was just given")
	}
	if status.NextPoll.IsZero() {
		t.Fatal("the device is reported with no scheduled retry although its poll has already failed")
	}
	if !status.Unreachable {
		t.Error("WorkerStatus does not report the device as unreachable")
	}
	if status.ConsecutiveFailures == 0 {
		t.Error("WorkerStatus reports no consecutive failures for a device that answered nothing")
	}

	// The retry must fall after the poll that earned it; time.Now() would make this load-dependent.
	if !status.NextPoll.After(result.Timestamp) {
		t.Errorf("WorkerStatus.NextPoll = %s, want a retry later than the poll that failed at %s",
			status.NextPoll, result.Timestamp)
	}
}

// TestARemovedDeviceStopsBeingPublished keeps a decommissioned box off the
// dashboard. The worker drops the series itself once its loop ends, so this
// also proves the loop ends.
func TestARemovedDeviceStopsBeingPublished(t *testing.T) {
	t.Parallel()

	agent := answeringAgent(t)
	agent.SetSilent(true)

	cfg := &config.Config{}
	cfg.Global.DefaultPollInterval = 1
	cfg.Global.SNMPTimeout = 1
	cfg.Global.SNMPRetries = 0

	p := NewWithExporter(cfg, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop() })

	device := silentDevice(t.Name(), agent.Addr())
	device.PollInterval = 1
	if err := p.AddDevice(ctx, device); err != nil {
		t.Fatalf("AddDevice: %v", err)
	}
	if !publishesUnreachable(t, device.Name) {
		t.Fatal("a registered device publishes no reachability at all")
	}

	if err := p.RemoveDevice(device.Name); err != nil {
		t.Fatalf("RemoveDevice: %v", err)
	}

	deadline := time.Now().Add(testPollTimeout)
	for publishesUnreachable(t, device.Name) {
		if time.Now().After(deadline) {
			t.Fatal("a removed device still publishes gonetmon_device_unreachable")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestWorkerStatusForAnUnknownDevice is the bad-input half: asking about a
// device the poller does not run must say so rather than describe a zero value
// as though it were a real worker.
func TestWorkerStatusForAnUnknownDevice(t *testing.T) {
	t.Parallel()

	p := NewWithExporter(lifecycleConfig(), nil, nil)

	if status, polling := p.WorkerStatus("no-such-device"); polling {
		t.Errorf("WorkerStatus reported a worker for an unknown device: %+v", status)
	}
}

// TestAddDeviceRejectsANonPositiveInterval covers the schedule a worker cannot
// honor: without an interval the loop would poll continuously, which is worse
// for the device than not polling it at all.
func TestAddDeviceRejectsANonPositiveInterval(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	cfg.Global.SNMPTimeout = 1

	p := NewWithExporter(cfg, nil, nil)
	ctx := context.Background()
	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop() })

	device := silentDevice("bastion", unreachableSNMPAddress(t))
	device.PollInterval = 0

	err := p.AddDevice(ctx, device)

	var interval *IntervalError
	if !errors.As(err, &interval) {
		t.Fatalf("AddDevice with no poll interval = %v, want an IntervalError", err)
	}
	if _, polling := p.WorkerStatus("bastion"); polling {
		t.Error("a device with no schedule was given a worker anyway")
	}
}

// TestPollResultIsDroppedRatherThanStallingTheLoop keeps one stalled reader
// from stopping every device: the polling loop is what keeps the rest of the
// fleet on schedule.
func TestPollResultIsDroppedRatherThanStallingTheLoop(t *testing.T) {
	t.Parallel()

	agent := answeringAgent(t)

	w, results := pollingWorker(t, agent.Addr(), time.Minute, scalar(sysDescrOID))
	for len(results) < cap(results) {
		w.poll(context.Background())
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		w.report(context.Background(), &PollResult{DeviceName: w.device.Name})
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("report blocked on a full results channel")
	}
}

// TestARetiredPollDoesNotPublishReachability checks a retired worker does not publish over its same-name replacement.
func TestARetiredPollDoesNotPublishReachability(t *testing.T) {
	w, results := pollingWorker(t, silentAgent(t), time.Second, scalar(sysUpTimeOID))

	// What the replacement worker published for the same device name.
	deviceUnreachable.WithLabelValues(w.device.Name).Set(0)

	// The device is removed while this poll is about to run.
	w.stop()
	w.poll(context.Background())

	if got := testutil.ToFloat64(deviceUnreachable.WithLabelValues(w.device.Name)); got != 0 {
		t.Errorf("gonetmon_device_unreachable{device=%q} = %v, want 0: a poll that "+
			"outlived its device wrote over the reading published for the same name",
			w.device.Name, got)
	}

	select {
	case result := <-results:
		t.Errorf("the retired poll reported %+v; it should produce nothing at all", result)
	default:
	}
}

// publishedReachability gathers the gauge for one device and reports whether the series exists.
func publishedReachability(t *testing.T, device string) (float64, bool) {
	t.Helper()

	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gathering metrics: %v", err)
	}
	for _, family := range families {
		if family.GetName() != "gonetmon_device_unreachable" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "device" && label.GetValue() == device {
					return metric.GetGauge().GetValue(), true
				}
			}
		}
	}
	return 0, false
}

// TestAHealthyDeviceRepublishesItsReachability checks a healthy device republishes its gauge every poll.
func TestAHealthyDeviceRepublishesItsReachability(t *testing.T) {
	agent := answeringAgent(t)
	w, results := pollingWorker(t, agent.Addr(), time.Second, scalar(sysUpTimeOID))

	w.poll(context.Background())
	nextResult(t, results)

	if value, published := publishedReachability(t, w.device.Name); !published || value != 0 {
		t.Fatalf("reachability after a healthy poll = (%v, published=%v), want (0, true)",
			value, published)
	}

	// Something else removes the series -- which is exactly what a retiring
	// worker for a device of the same name does.
	deviceUnreachable.DeleteLabelValues(w.device.Name)
	if _, published := publishedReachability(t, w.device.Name); published {
		t.Fatal("the fixture did not actually remove the series")
	}

	w.poll(context.Background())
	nextResult(t, results)

	value, published := publishedReachability(t, w.device.Name)
	if !published {
		t.Error("the series is still gone after a healthy poll: a device that never " +
			"republishes stays absent until it breaks and recovers")
	}
	if value != 0 {
		t.Errorf("reachability = %v, want 0 for a device that is answering", value)
	}
}

// TestRemovalWaitsForACommitInFlight checks a removal waits while the commit lock is held.
func TestRemovalWaitsForACommitInFlight(t *testing.T) {
	p := NewWithExporter(lifecycleConfig(), nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop() })

	const device = "commit-barrier-fixture"
	if err := p.AddDevice(ctx, silentDevice(device, silentAgent(t))); err != nil {
		t.Fatalf("AddDevice: %v", err)
	}
	t.Cleanup(func() { deviceUnreachable.DeleteLabelValues(device) })

	p.mu.RLock()
	w := p.liveWorker(device)
	p.mu.RUnlock()
	if w == nil {
		t.Fatal("no worker was registered for the fixture device")
	}

	// Stand in for a commit that has begun.
	w.commitMu.Lock()

	removed := make(chan error, 1)
	go func() { removed <- p.RemoveDevice(device) }()

	select {
	case err := <-removed:
		w.commitMu.Unlock()
		t.Fatalf("RemoveDevice returned (%v) while a commit was in progress: the "+
			"check and the publish are not one step, so a removal can land "+
			"between them and the retired poll publishes anyway", err)
	case <-time.After(100 * time.Millisecond):
	}

	w.commitMu.Unlock()

	select {
	case err := <-removed:
		if err != nil {
			t.Errorf("RemoveDevice: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RemoveDevice never returned after the commit finished")
	}
}

// TestACommitAfterRemovalPublishesNothing is the other half: once RemoveDevice
// has returned, a commit that starts afterwards must find the device gone.
func TestACommitAfterRemovalPublishesNothing(t *testing.T) {
	w, results := pollingWorker(t, silentAgent(t), time.Second, scalar(sysUpTimeOID))

	deviceUnreachable.WithLabelValues(w.device.Name).Set(0)
	w.stop()

	published := w.commit(context.Background(), &PollResult{
		DeviceName: w.device.Name,
		Timestamp:  time.Now(),
		Results:    nil,
		Error:      nil,
	}, cycle{})

	if published {
		t.Error("commit published for a device that had been removed")
	}
	if got := testutil.ToFloat64(deviceUnreachable.WithLabelValues(w.device.Name)); got != 0 {
		t.Errorf("gonetmon_device_unreachable = %v, want 0: a retired commit wrote "+
			"over the reading published for the same name", got)
	}
	select {
	case result := <-results:
		t.Errorf("the retired commit reported %+v", result)
	default:
	}
}

// blockingExporter holds a commit open until the test lets it go.
type blockingExporter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (e *blockingExporter) Export(context.Context, []*metrics.Sample) error { return nil }

func (e *blockingExporter) ExportSNMPResults(context.Context, string, []*snmp.Result, map[string]string) error {
	e.once.Do(func() { close(e.entered) })
	<-e.release
	return nil
}

func (e *blockingExporter) Flush(context.Context) error { return nil }

func (e *blockingExporter) Close() error { return nil }

// TestACommitInProgressHoldsOffRemoval stops a commit mid-export and checks the removal barrier is unavailable.
func TestACommitInProgressHoldsOffRemoval(t *testing.T) {
	w, results := pollingWorker(t, silentAgent(t), time.Second, scalar(sysUpTimeOID))
	exporter := &blockingExporter{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	w.exporter = exporter

	committed := make(chan bool, 1)
	go func() {
		committed <- w.commit(context.Background(), &PollResult{
			DeviceName: w.device.Name,
			Timestamp:  time.Now(),
			Results:    []*snmp.Result{{OID: sysUpTimeOID, Timestamp: time.Now()}},
		}, cycle{})
	}()

	select {
	case <-exporter.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the commit never reached its export")
	}

	// This is what RemoveDevice does on its way out.
	if w.commitMu.TryLock() {
		w.commitMu.Unlock()
		close(exporter.release)
		<-committed
		t.Fatal("the commit lock was free while a commit was publishing: removal " +
			"would not wait for it, so a poll can publish for a device that has " +
			"already been replaced")
	}

	close(exporter.release)
	if published := <-committed; !published {
		t.Error("the commit did not publish, so the block above proved nothing")
	}
	<-results
}

// TestRemovalDuringAFailingClientInitPublishesNothing covers the client-init failure path.
func TestRemovalDuringAFailingClientInitPublishesNothing(t *testing.T) {
	// An address no client can be built for, so ensureClient fails every time.
	w, results := pollingWorker(t, "", time.Second, scalar(sysUpTimeOID))

	deviceUnreachable.WithLabelValues(w.device.Name).Set(0)

	// Removed before the poll runs.
	w.stop()
	w.poll(context.Background())

	if got := testutil.ToFloat64(deviceUnreachable.WithLabelValues(w.device.Name)); got != 0 {
		t.Errorf("gonetmon_device_unreachable = %v, want 0: a retired worker whose "+
			"client would not build still published, and the gauge is labeled by "+
			"device NAME, so it lands on the replacement", got)
	}
	select {
	case result := <-results:
		t.Errorf("the retired worker reported %+v from a failed client init", result)
	default:
	}
}

// TestAnExitingWorkerDoesNotDeleteTheReplacementsSeries publishes while the old worker is mid-poll, then stops it.
func TestAnExitingWorkerDoesNotDeleteTheReplacementsSeries(t *testing.T) {
	// The worker polls once and parks; the replacement publishes; then the worker is stopped.
	w, results := pollingWorker(t, silentAgent(t), time.Hour, scalar(sysUpTimeOID))
	t.Cleanup(func() { deviceUnreachable.DeleteLabelValues(w.device.Name) })

	var wg sync.WaitGroup
	wg.Add(1)
	go w.run(context.Background(), &wg)

	// Wait until the first poll has been reported, which means the worker is
	// now parked on its (one hour) timer rather than still collecting.
	nextResult(t, results)

	// The replacement publishes for the same device name.
	deviceUnreachable.WithLabelValues(w.device.Name).Set(0)

	// Only now does the old worker exit.
	w.stop()
	wg.Wait()

	value, published := publishedReachability(t, w.device.Name)
	if !published {
		t.Fatal("the series is gone: the worker on its way out deleted the one the " +
			"replacement had already published, and an alert with no series to " +
			"evaluate does not fire")
	}
	if value != 0 {
		t.Errorf("gonetmon_device_unreachable = %v, want the 0 the replacement published", value)
	}
}

// TestStoppingThePollerDropsWhatItPublished checks Stop deletes every device's reachability series.
func TestStoppingThePollerDropsWhatItPublished(t *testing.T) {
	p := NewWithExporter(lifecycleConfig(), nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	const device = "stop-cleanup-fixture"
	if err := p.AddDevice(ctx, silentDevice(device, silentAgent(t))); err != nil {
		t.Fatalf("AddDevice: %v", err)
	}
	t.Cleanup(func() { deviceUnreachable.DeleteLabelValues(device) })

	if _, published := publishedReachability(t, device); !published {
		t.Fatal("the device published no reachability at all, so this proves nothing")
	}

	if err := p.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if _, published := publishedReachability(t, device); published {
		t.Error("the device still has a reachability series after the poller stopped: " +
			"nothing will ever update it again, and a frozen gauge reads as live")
	}
}
