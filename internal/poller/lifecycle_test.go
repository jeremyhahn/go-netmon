package poller

import (
	"bytes"
	"context"
	"log/slog"
	"net"
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
