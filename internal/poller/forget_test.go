package poller

import (
	"context"
	"slices"
	"sync"
	"testing"
)

// forgettingExporter records the devices it was told to forget.
type forgettingExporter struct {
	plainExporter

	mu        sync.Mutex
	forgotten []string
}

func (e *forgettingExporter) ForgetDevice(deviceName string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.forgotten = append(e.forgotten, deviceName)
}

func (e *forgettingExporter) forgottenDevices() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.forgotten)
}

// TestRemoveDeviceForgetsTheExportersDeviceState is the #54 eviction hook: a
// device the poller no longer polls must not keep its friendly names in the
// exporter, or that cache holds every device ever polled.
func TestRemoveDeviceForgetsTheExportersDeviceState(t *testing.T) {
	t.Parallel()

	exporter := &forgettingExporter{}
	p := NewWithExporter(lifecycleConfig(), nil, exporter)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop() })

	for _, name := range []string{"bastion", "kept"} {
		if err := p.AddDevice(ctx, silentDevice(name, silentAgent(t))); err != nil {
			t.Fatalf("AddDevice %s: %v", name, err)
		}
	}

	if err := p.RemoveDevice("bastion"); err != nil {
		t.Fatalf("RemoveDevice: %v", err)
	}

	if got := exporter.forgottenDevices(); !slices.Equal(got, []string{"bastion"}) {
		t.Errorf("exporter told to forget %v, want [bastion]", got)
	}
}
