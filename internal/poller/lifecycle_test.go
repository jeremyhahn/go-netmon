package poller

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/logging"
)

// silentAgent binds a real UDP socket on loopback and never answers it, so a
// worker pointed at it spends its full SNMP retry budget in every poll. It is
// the in-process stand-in for an unreachable device.
func silentAgent(t *testing.T) string {
	t.Helper()

	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind silent agent: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return conn.LocalAddr().String()
}

// lifecycleConfig is a poller configuration whose SNMP budget is long enough
// that a control-plane call which waited for it would be obvious.
func lifecycleConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Global.DefaultPollInterval = 30
	cfg.Global.MaxWorkers = 4
	cfg.Global.SNMPTimeout = 5
	cfg.Global.SNMPRetries = 2
	return cfg
}

func silentDevice(name, address string) *config.Device {
	return &config.Device{
		Name:         name,
		Address:      address,
		SNMPVersion:  "2c",
		Community:    "public",
		PollInterval: 30,
		Enabled:      true,
	}
}

// TestRemoveDeviceDoesNotWaitForAnInFlightPoll is the regression for the
// poller's control API stalling behind a dead device. RemoveDevice used to hold
// the poller lock while closing an SNMP client, and closing that client waited
// for the request it had in flight — the full timeout × retries budget.
func TestRemoveDeviceDoesNotWaitForAnInFlightPoll(t *testing.T) {
	t.Parallel()

	p := NewWithExporter(lifecycleConfig(), nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop() })

	if err := p.AddDevice(ctx, silentDevice("bastion", silentAgent(t))); err != nil {
		t.Fatalf("AddDevice: %v", err)
	}

	// Let the worker get into its first, never-answered request.
	time.Sleep(200 * time.Millisecond)

	start := time.Now()
	if err := p.RemoveDevice("bastion"); err != nil {
		t.Fatalf("RemoveDevice: %v", err)
	}
	elapsed := time.Since(start)

	budget := time.Duration(lifecycleConfig().Global.SNMPTimeout) * time.Second
	if elapsed >= budget {
		t.Fatalf("RemoveDevice took %v: it waited for the device's SNMP budget (%v per attempt)", elapsed, budget)
	}
	if p.GetPoolSize() != 0 {
		t.Errorf("pool size = %d, want the removed device's client released", p.GetPoolSize())
	}
}

// TestRemoveDeviceDoesNotBlockAnotherDevice states the head-of-line guarantee
// on the poller's control plane: removing a dead device must not delay a
// registration that has nothing to do with it.
func TestRemoveDeviceDoesNotBlockAnotherDevice(t *testing.T) {
	t.Parallel()

	p := NewWithExporter(lifecycleConfig(), nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop() })

	if err := p.AddDevice(ctx, silentDevice("bastion", silentAgent(t))); err != nil {
		t.Fatalf("AddDevice: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	removed := make(chan struct{})
	go func() {
		defer close(removed)
		_ = p.RemoveDevice("bastion")
	}()

	start := time.Now()
	if err := p.AddDevice(ctx, silentDevice("other", silentAgent(t))); err != nil {
		t.Fatalf("AddDevice for an unrelated device: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("registering an unrelated device waited %v behind a dead device's removal", elapsed)
	}

	<-removed
}

// TestRemoveDeviceStopsPolling is the goroutine-leak regression: every worker
// used to share the poller's single stop channel, so removing a device only
// forgot it — the goroutine kept polling for the life of the process, and each
// device update leaked another one.
func TestRemoveDeviceStopsPolling(t *testing.T) {
	t.Parallel()

	cfg := lifecycleConfig()
	cfg.Global.SNMPTimeout = 1
	cfg.Global.SNMPRetries = 0

	var sink safeBuffer
	logger, err := logging.NewWithWriter(
		config.LoggingConfig{Level: "debug", Format: "text", Output: "stdout"}, &sink)
	if err != nil {
		t.Fatalf("NewWithWriter: %v", err)
	}

	p := NewWithExporter(cfg, nil, nil, WithLogger(logger))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop() })

	device := silentDevice("bastion", silentAgent(t))
	device.PollInterval = 1 // poll every second so a survivor shows up quickly
	if err := p.AddDevice(ctx, device); err != nil {
		t.Fatalf("AddDevice: %v", err)
	}

	// The first poll must have started, or there is nothing to prove stopped.
	waitFor(t, 3*time.Second, func() bool {
		return strings.Contains(sink.String(), "polling scalar OID chunk")
	}, "the worker never polled")

	if err := p.RemoveDevice("bastion"); err != nil {
		t.Fatalf("RemoveDevice: %v", err)
	}

	before := strings.Count(sink.String(), "polling scalar OID chunk")
	// Two poll intervals is long enough for a surviving worker to speak again.
	time.Sleep(2500 * time.Millisecond)
	after := strings.Count(sink.String(), "polling scalar OID chunk")

	if after > before {
		t.Errorf("the worker polled %d more times after removal: its goroutine outlived the device", after-before)
	}
}

// TestWorkerOutlivesTheRequestThatAddedIt keeps a device registered through the
// control API from being stopped the moment that HTTP request returns. The
// worker's lifetime is the poller's, not the caller's.
func TestWorkerOutlivesTheRequestThatAddedIt(t *testing.T) {
	t.Parallel()

	cfg := lifecycleConfig()
	cfg.Global.SNMPTimeout = 1
	cfg.Global.SNMPRetries = 0

	var sink safeBuffer
	logger, err := logging.NewWithWriter(
		config.LoggingConfig{Level: "debug", Format: "text", Output: "stdout"}, &sink)
	if err != nil {
		t.Fatalf("NewWithWriter: %v", err)
	}

	p := NewWithExporter(cfg, nil, nil, WithLogger(logger))
	pollerCtx, cancelPoller := context.WithCancel(context.Background())
	defer cancelPoller()

	if err := p.Start(pollerCtx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop() })

	// A request context, as the control API hands one to AddDevice.
	requestCtx, endRequest := context.WithCancel(context.Background())

	device := silentDevice("bastion", silentAgent(t))
	device.PollInterval = 1
	if err := p.AddDevice(requestCtx, device); err != nil {
		t.Fatalf("AddDevice: %v", err)
	}

	// The request ends, as it does the instant the handler writes its response.
	endRequest()

	waitFor(t, 5*time.Second, func() bool {
		return strings.Count(sink.String(), "polling scalar OID chunk") >= 2
	}, "the worker stopped when the request that added it returned")
}

// TestAddDeviceRefusesAnAbandonedRequest keeps a caller that has already given
// up from leaving a worker behind.
func TestAddDeviceRefusesAnAbandonedRequest(t *testing.T) {
	t.Parallel()

	p := NewWithExporter(lifecycleConfig(), nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := p.AddDevice(ctx, silentDevice("bastion", "127.0.0.1:1"))
	if err == nil {
		t.Fatal("AddDevice accepted a canceled request")
	}
	if !strings.Contains(err.Error(), "bastion") {
		t.Errorf("error = %q, want it to name the device", err)
	}
	if p.GetWorkerStatus()["bastion"].DeviceName != "" {
		t.Error("a worker was started for a canceled request")
	}
}

func TestRemoveDeviceRejectsUnknownDevice(t *testing.T) {
	t.Parallel()

	p := NewWithExporter(lifecycleConfig(), nil, nil)

	err := p.RemoveDevice("nobody")
	if err == nil {
		t.Fatal("RemoveDevice accepted a device that was never added")
	}
	if !strings.Contains(err.Error(), "nobody") {
		t.Errorf("error = %q, want it to name the device", err)
	}
}

// TestWithLoggerGatesPollTracing is the poller half of the log-level guarantee:
// the per-OID tracing on the polling path is silent at "info" and speaks at
// "debug".
func TestWithLoggerGatesPollTracing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		level     string
		wantTrace bool
	}{
		{"info is silent", "info", false},
		{"debug traces the poll", "debug", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := lifecycleConfig()
			cfg.Global.SNMPTimeout = 1
			cfg.Global.SNMPRetries = 0

			var sink safeBuffer
			logger, err := logging.NewWithWriter(
				config.LoggingConfig{Level: tt.level, Format: "json", Output: "stdout"}, &sink)
			if err != nil {
				t.Fatalf("NewWithWriter: %v", err)
			}

			p := NewWithExporter(cfg, nil, nil, WithLogger(logger))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			if err := p.Start(ctx); err != nil {
				t.Fatalf("Start: %v", err)
			}
			t.Cleanup(func() { _ = p.Stop() })

			if err := p.AddDevice(ctx, silentDevice("bastion", silentAgent(t))); err != nil {
				t.Fatalf("AddDevice: %v", err)
			}

			if tt.wantTrace {
				waitFor(t, 3*time.Second, func() bool {
					return strings.Contains(sink.String(), "classified OIDs")
				}, "debug level produced no poll tracing")
				return
			}

			// Give the worker as long as the traced run needed to speak.
			time.Sleep(1500 * time.Millisecond)
			if out := sink.String(); strings.Contains(out, "classified OIDs") ||
				strings.Contains(out, "polling scalar OID chunk") {
				t.Errorf("info level emitted debug tracing: %q", out)
			}
		})
	}
}

// TestNewDiscardsWithoutALogger keeps the debug statements on the polling path
// free for a caller that never configured logging.
func TestNewDiscardsWithoutALogger(t *testing.T) {
	t.Parallel()

	p := New(lifecycleConfig(), nil)
	if p.logger == nil {
		t.Fatal("a poller built without a logger has none, so its call sites would panic")
	}
	if p.logger.Enabled(context.Background(), slog.LevelError) {
		t.Error("a poller built without a logger emits records")
	}
}

// waitFor polls cond until it holds or the timeout expires.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s (waited %v)", msg, timeout)
}

// safeBuffer is a bytes.Buffer that a worker goroutine may write to while the
// test reads it.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestAPollThatOutlivedItsDeviceReportsNothing is the trigger from
// go-netmon#69, at the layer that decides whether those samples exist at all.
//
// RemoveDevice deliberately does not wait for the poll in flight -- the control
// plane must not stall behind a device that has stopped answering, which
// TestRemoveDeviceDoesNotWaitForAnInFlightPoll states. So a poll finishes after
// its device is gone, and because a device UPDATE is a remove followed by an
// add, the replacement worker is already polling by then. That late poll's
// varbinds were stamped BEFORE the replacement's and reach the exporter after,
// which leaves one series holding two readings in the wrong order and costs the
// whole remote-write request.
//
// A poll that outlived its device must therefore produce nothing at all: no
// export, and no result either. The result channel is what this asserts on,
// because it is reachable with a device that answers nothing -- and a device
// that answers nothing is the one whose poll is certainly still running when it
// is removed. The export sits behind the same guard, one line above.
func TestAPollThatOutlivedItsDeviceReportsNothing(t *testing.T) {
	t.Parallel()

	p := NewWithExporter(lifecycleConfig(), nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop() })

	if err := p.AddDevice(ctx, silentDevice("bastion", silentAgent(t))); err != nil {
		t.Fatalf("AddDevice: %v", err)
	}

	// Into the first, never-answered request.
	time.Sleep(200 * time.Millisecond)

	if err := p.RemoveDevice("bastion"); err != nil {
		t.Fatalf("RemoveDevice: %v", err)
	}

	// Long enough for that poll to exhaust its budget and reach its report.
	budget := time.Duration(lifecycleConfig().Global.SNMPTimeout) * time.Second
	deadline := time.After(budget * 3)
	for {
		select {
		case result := <-p.Results():
			if result != nil && result.DeviceName == "bastion" {
				t.Fatalf("the poll of bastion reported after the device was removed "+
					"(error: %v); its samples were stamped before the replacement "+
					"worker's and arrive after them", result.Error)
			}
		case <-deadline:
			return
		}
	}
}

// TestAddDeviceWaitsForTheSameNameToFinishRetiring is the pool race, forced
// rather than raced for.
//
// RemoveDevice cannot hold the poller lock for its whole length: closing an
// SNMP client waits for the request it has in flight, and the control plane
// must not stall behind a dead device. So the name leaves the worker map before
// the pool entry is retired. A replacement registered inside that window takes
// a client the outgoing removal then closes -- and because ensureClient does
// nothing once its field is non-nil, the replacement polls a dead connection
// for good. A device that silently stops reporting and never recovers.
//
// The commit barrier this branch added widens that window, which is why it is
// fixed here rather than deferred: a pre-existing race that a change makes
// materially more likely is that change's problem.
//
// The ordering is built, not timed. The removal is parked at the commit barrier
// by holding the worker's commit lock, so the window is held open for as long
// as the test wants rather than for however long a machine takes.
func TestAddDeviceWaitsForTheSameNameToFinishRetiring(t *testing.T) {
	p := NewWithExporter(lifecycleConfig(), nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop() })

	const device = "pool-race-fixture"
	address := silentAgent(t)
	if err := p.AddDevice(ctx, silentDevice(device, address)); err != nil {
		t.Fatalf("AddDevice: %v", err)
	}

	p.mu.RLock()
	w := p.workers[device]
	p.mu.RUnlock()
	if w == nil {
		t.Fatal("no worker was registered for the fixture device")
	}

	// Park the removal at its commit barrier, holding the window open.
	w.commitMu.Lock()

	removed := make(chan error, 1)
	go func() { removed <- p.RemoveDevice(device) }()

	// Give the removal time to reach the barrier and leave the worker map.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.RLock()
		_, stillListed := p.workers[device]
		p.mu.RUnlock()
		if !stillListed {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// The name is now free in the worker map but its pool entry is not yet
	// retired. A replacement must not take it.
	added := make(chan error, 1)
	go func() { added <- p.AddDevice(ctx, silentDevice(device, address)) }()

	select {
	case err := <-added:
		w.commitMu.Unlock()
		<-removed
		t.Fatalf("AddDevice returned (%v) while the same name was still retiring: "+
			"the replacement takes a client the removal is about to close, and "+
			"ensureClient never reacquires once its field is set", err)
	case <-time.After(200 * time.Millisecond):
	}

	// Let the removal finish; the registration may then proceed.
	w.commitMu.Unlock()

	if err := <-removed; err != nil {
		t.Errorf("RemoveDevice: %v", err)
	}
	select {
	case err := <-added:
		if err != nil {
			t.Errorf("AddDevice after the retirement finished: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("AddDevice never completed after the removal released the name")
	}
}

// TestAddDeviceNeverInstallsAWorkerOntoAClaimedName is the interleaving the
// retirement gate exists to make impossible, and the one the first version of
// it still allowed.
//
// That version read retiring[name], released the poller lock, and took it again
// to install. Between the two holds RemoveDevice could take the name: delete
// the worker, claim the retirement, and drop the lock on its way to closing the
// client. The registration then acquired the lock, found no worker -- because
// the removal had just deleted it -- and started a replacement onto a pool
// entry that was about to be closed. The gate reported a name it had genuinely
// observed free, which is what made it convincing.
//
// There is no seam to park a goroutine in that window -- in the broken version
// it is one statement wide -- so the interleaving is raced for rather than
// forced. What is NOT raced is the observation: the removal is parked at its
// commit barrier, so a name it has claimed stays claimed until this test
// releases it, and a worker listed under a claimed name stays listed. Both
// halves of the violating state persist, so seeing it is not a sampling
// artifact; only reaching it is probabilistic.
//
// The counts below are measured, not guessed. One registration against one
// removal never hit the window at all: 0 detections in 2,000 iterations, a test
// that could not fail. Sixteen registrations against one removal hit it about
// once in 45 iterations, so 600 iterations detect it with near-certainty --
// 10 of 10 mutated runs, worst detection at iteration 149. Against the fixed
// gate, 0 in 4,800.
func TestAddDeviceNeverInstallsAWorkerOntoAClaimedName(t *testing.T) {
	t.Parallel()

	p := NewWithExporter(lifecycleConfig(), nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop() })

	const device = "claimed-name"
	address := silentAgent(t)

	for i := 0; i < addRemoveRaces; i++ {
		if err := p.AddDevice(ctx, silentDevice(device, address)); err != nil {
			t.Fatalf("iteration %d: seeding AddDevice: %v", i, err)
		}

		p.mu.RLock()
		w := p.workers[device]
		p.mu.RUnlock()
		if w == nil {
			t.Fatalf("iteration %d: no worker was registered", i)
		}

		// Park the removal at its commit barrier before either call starts.
		w.commitMu.Lock()

		removed := make(chan error, 1)
		added := make(chan error, concurrentAdders)
		var start sync.WaitGroup
		start.Add(1)
		for a := 0; a < concurrentAdders; a++ {
			go func() {
				start.Wait()
				added <- p.AddDevice(ctx, silentDevice(device, address))
			}()
		}
		go func() { removed <- p.RemoveDevice(device) }()
		start.Done()

		// RemoveDevice deletes the worker and claims the name under one hold of
		// p.mu, so the two are never both true unless a registration put the
		// worker back while the claim was up.
		violated := false
		deadline := time.Now().Add(raceObservationWindow)
		for time.Now().Before(deadline) {
			p.mu.RLock()
			_, claimed := p.retiring[device]
			_, listed := p.workers[device]
			p.mu.RUnlock()
			if claimed && listed {
				violated = true
				break
			}
			runtime.Gosched()
		}

		w.commitMu.Unlock()
		if err := <-removed; err != nil {
			t.Fatalf("iteration %d: RemoveDevice: %v", i, err)
		}
		installed := 0
		for a := 0; a < concurrentAdders; a++ {
			if err := <-added; err == nil {
				installed++
			}
		}

		if violated {
			t.Fatalf("iteration %d: a worker was registered for %s while an "+
				"unfinished removal still held the name: it takes the client "+
				"that removal is about to close, and ensureClient never "+
				"reacquires once its field is set", i, device)
		}

		// At most one registration installs a worker. None does if every adder
		// reached the lock before the removal and was refused by the seeded
		// worker; one does if any of them waited the removal out. Two would
		// mean the name stopped being exclusive.
		if installed > 1 {
			t.Fatalf("iteration %d: %d of %d registrations installed a worker "+
				"for one name", i, installed, concurrentAdders)
		}
		if installed == 1 {
			if err := p.RemoveDevice(device); err != nil {
				t.Fatalf("iteration %d: cleanup RemoveDevice: %v", i, err)
			}
		}
	}
}

// addRemoveRaces and raceObservationWindow size the race above: how many times
// the pair is run, and how long each run is watched before the parked removal
// is released. The window only has to outlast one contested lock handoff.
const (
	addRemoveRaces        = 600
	concurrentAdders      = 16
	raceObservationWindow = 2 * time.Millisecond
)
