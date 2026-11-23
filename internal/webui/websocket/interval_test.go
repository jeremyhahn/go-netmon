package websocket

import (
	"context"
	"testing"
	"time"
)

// The stream cadence used to be a literal inside Run, which meant the one
// number standing between the poller and the dashboard was the only one nobody
// could state or change (#99). These tests hold it to being a real setting.

func TestNewHub_DefaultsToTheDocumentedInterval(t *testing.T) {
	h := NewHub(nil, nil)

	if got := h.StreamInterval(); got != DefaultStreamInterval {
		t.Errorf("StreamInterval() = %s, want %s", got, DefaultStreamInterval)
	}
	if DefaultStreamInterval != 5*time.Second {
		t.Errorf("DefaultStreamInterval = %s, want 5s: a configuration that omits "+
			"the key must keep ticking at the rate the literal used to", DefaultStreamInterval)
	}
}

func TestWithStreamInterval_IsWhatRunTicksAt(t *testing.T) {
	// The proof that the option reaches the loop rather than only the getter:
	// at the default this hub would not query once inside the window below.
	rounds := make(chan struct{}, 16)
	h := NewHub(func(context.Context, string) ([]MetricValue, error) {
		select {
		case rounds <- struct{}{}:
		default:
		}
		return sampleMetrics(), nil
	}, nil, WithStreamInterval(20*time.Millisecond))

	h.Subscribe(newTestClient(), "snmp_if_in")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.Run(ctx)

	for i := 0; i < 2; i++ {
		select {
		case <-rounds:
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d rounds ran; the configured interval is not driving Run", i)
		}
	}
}

func TestWithStreamInterval_NonPositiveKeepsTheDefault(t *testing.T) {
	// An unset configuration field arrives here as a zero duration. Spinning on
	// a zero ticker would panic; falling back is the only safe reading.
	for _, d := range []time.Duration{0, -time.Second} {
		h := NewHub(nil, nil, WithStreamInterval(d))
		if got := h.StreamInterval(); got != DefaultStreamInterval {
			t.Errorf("WithStreamInterval(%s): StreamInterval() = %s, want the default %s",
				d, got, DefaultStreamInterval)
		}
	}
}

func TestWithStreamInterval_LastOptionWins(t *testing.T) {
	h := NewHub(nil, nil, WithStreamInterval(time.Minute), WithStreamInterval(3*time.Second))

	if got := h.StreamInterval(); got != 3*time.Second {
		t.Errorf("StreamInterval() = %s, want 3s", got)
	}
}
