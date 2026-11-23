package api

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/webui/client"
	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// How the fleet overview costs as the fleet grows.
//
// The poller answers GET /api/poller/devices with every device and its status
// in one response, built entirely from in-memory worker state. The web UI did
// not use it: it asked GET /devices/{name}/status once per device, so a single
// fleet refresh cost one HTTP round trip per monitored device.
//
// That is invisible on a rack and ruinous on a datacenter. Every browser with
// the overview open repeats the whole fan-out every 30 seconds, so the load on
// the poller is devices x sessions, and the 8-second collection budget is
// shared across all of it -- past a few thousand devices rows start coming back
// as "poller unavailable" not because anything is wrong but because the budget
// ran out while they were still queued.

// countingPoller answers like a poller on a busy network: each per-device call
// costs a round trip, while the bulk listing costs one.
type countingPoller struct {
	devices  []client.DeviceInfo
	latency  time.Duration
	perCalls atomic.Int64
	getCalls atomic.Int64

	// serving bounds how many requests the poller handles at once, so a fan-out
	// from many sessions queues the way it would against one real process.
	serving chan struct{}
}

// throttle caps the poller at ratePerSecond requests, modeling a single
// process rather than an infinitely parallel one.
func (p *countingPoller) throttle(ratePerSecond int) {
	// One in-flight slot per millisecond of service time at the target rate.
	const inFlight = 64
	p.serving = make(chan struct{}, inFlight)
	p.latency = time.Duration(inFlight) * time.Second / time.Duration(ratePerSecond)
}

// serve applies the poller's service time and its concurrency limit.
func (p *countingPoller) serve(ctx context.Context) error {
	if p.serving != nil {
		select {
		case p.serving <- struct{}{}:
			defer func() { <-p.serving }()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if p.latency <= 0 {
		return nil
	}
	select {
	case <-time.After(p.latency):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// polledAt is a fixed, non-zero poll time: a device whose last poll is the zero
// time reads as never polled, whatever else its status says.
var polledAt = time.Date(2026, 8, 12, 20, 0, 0, 0, time.UTC)

func newCountingPoller(n int, latency time.Duration) *countingPoller {
	devices := make([]client.DeviceInfo, n)
	for i := range devices {
		devices[i] = client.DeviceInfo{
			Name:    fmt.Sprintf("device-%05d", i),
			Address: fmt.Sprintf("10.%d.%d.%d", i>>16&255, i>>8&255, i&255),
			Enabled: true,
			Status: &client.DeviceStatus{
				Name:            fmt.Sprintf("device-%05d", i),
				Connected:       true,
				LastPollSuccess: true,
				// A stamped poll is what separates a healthy device from one
				// that has been registered and never answered.
				LastPollTime: polledAt,
			},
		}
	}
	return &countingPoller{devices: devices, latency: latency}
}

func (p *countingPoller) RegisterDevice(context.Context, *storage.Device, []string, map[string]config.ModuleFilter) error {
	return nil
}

func (p *countingPoller) UpdateDevice(context.Context, *storage.Device, []string, map[string]config.ModuleFilter) error {
	return nil
}

func (p *countingPoller) RemoveDevice(context.Context, string) error { return nil }

func (p *countingPoller) GetDeviceStatus(ctx context.Context, name string) (*client.DeviceStatus, error) {
	p.perCalls.Add(1)
	if err := p.serve(ctx); err != nil {
		return nil, err
	}
	return &client.DeviceStatus{Name: name, Connected: true, LastPollSuccess: true, LastPollTime: polledAt}, nil
}

func (p *countingPoller) ListDevices(ctx context.Context) ([]client.DeviceInfo, error) {
	p.getCalls.Add(1)
	if err := p.serve(ctx); err != nil {
		return nil, err
	}
	return p.devices, nil
}

// fleetOf builds a server holding n stored devices, matching the poller's names.
func fleetOf(tb testing.TB, n int, latency time.Duration) (*Server, *countingPoller, []storage.Device) {
	tb.Helper()

	s := newTestServer(tb)
	poller := newCountingPoller(n, latency)
	s.pollerClient = poller

	devices := make([]storage.Device, n)
	for i := range devices {
		devices[i] = storage.Device{
			Name:        fmt.Sprintf("device-%05d", i),
			Address:     fmt.Sprintf("10.%d.%d.%d", i>>16&255, i>>8&255, i&255),
			SNMPVersion: "v2c",
			Community:   "public",
		}
	}
	return s, poller, devices
}

// TestFleetStatusCostsOneCallPerRefresh is the requirement: the overview's cost
// must not grow a round trip for every device on the network.
func TestFleetStatusCostsOneCallPerRefresh(t *testing.T) {
	t.Parallel()

	const fleet = 2000
	s, poller, devices := fleetOf(t, fleet, 0)

	rows := s.fleetStatus(context.Background(), devices)

	if len(rows) != fleet {
		t.Fatalf("returned %d rows, want one per device (%d)", len(rows), fleet)
	}
	if got := poller.perCalls.Load(); got != 0 {
		t.Errorf("made %d per-device calls; the fleet view must ask once, not once per device", got)
	}
	if got := poller.getCalls.Load(); got != 1 {
		t.Errorf("made %d bulk calls, want exactly 1", got)
	}

	// The rows must still carry real status, in the caller's order.
	for i, row := range rows {
		if row.Name != devices[i].Name {
			t.Fatalf("row %d is %q, want %q: the overview must not reshuffle", i, row.Name, devices[i].Name)
		}
	}
	if rows[0].Status == nil || !rows[0].Status.Connected {
		t.Error("status was lost in the bulk path: row 0 carries no connected status")
	}
}

// TestFleetStatusHoldsUpAcrossManySessions is the failure that matters.
//
// One operator on a big fleet is survivable: 10,000 sequential round trips at
// 1ms with 16 in flight finish inside the budget. What does not survive is a
// room full of operators, because the poller is one process with finite
// throughput and every browser repeats the whole fan-out every 30 seconds. The
// requests queue behind each other, the shared budget expires while rows are
// still waiting, and the overview reports an outage that is not happening.
func TestFleetStatusHoldsUpAcrossManySessions(t *testing.T) {
	t.Parallel()

	const (
		fleet    = 10000
		sessions = 25
	)

	s, poller, devices := fleetOf(t, fleet, 0)
	// A real poller serves a bounded number of requests at a time; past that
	// they wait. 5,000/s is generous for a Go HTTP handler doing map lookups.
	poller.throttle(5000)

	var wg sync.WaitGroup
	missing := make([]int, sessions)
	start := time.Now()
	for session := range sessions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, row := range s.fleetStatus(context.Background(), devices) {
				if row.Status == nil {
					missing[session]++
				}
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	var blind int
	for _, n := range missing {
		blind += n
	}
	t.Logf("%d sessions x %d devices in %v: %d calls to the poller, %d rows with no status",
		sessions, fleet, elapsed.Round(time.Millisecond),
		poller.perCalls.Load()+poller.getCalls.Load(), blind)

	if blind > 0 {
		t.Errorf("%d rows across %d sessions came back without status; "+
			"concurrent operators must not blind each other's overview", blind, sessions)
	}
}

// BenchmarkFleetStatus reports the per-refresh cost as the fleet grows. Every
// browser session with the overview open pays this every 30 seconds.
func BenchmarkFleetStatus(b *testing.B) {
	for _, fleet := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("devices=%d", fleet), func(b *testing.B) {
			s, poller, devices := fleetOf(b, fleet, 0)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				s.fleetStatus(context.Background(), devices)
			}
			b.StopTimer()

			b.ReportMetric(float64(poller.perCalls.Load())/float64(b.N), "per-device-calls/op")
			b.ReportMetric(float64(poller.getCalls.Load())/float64(b.N), "bulk-calls/op")
		})
	}
}
