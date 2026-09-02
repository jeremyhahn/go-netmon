package main

import (
	"errors"
	"log"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/metrics"
)

// syncBuffer collects log output from whichever goroutine writes it.
type syncBuffer struct {
	mu    sync.Mutex
	lines strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lines.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lines.String()
}

func captureLog(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	previous := log.Writer()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(previous) })
	return buf
}

// TestShutdownSaysWhenSamplesWereLost is the last link in "nothing leaves
// without something observable saying so".
//
// Flush and Close return a typed DroppedError carrying how many samples never
// reached Prometheus. Reporting that as "Error closing exporter" is true and
// useless -- it reads like a tidy-up that went wrong rather than telemetry that
// is gone, and shutdown is the one moment nobody re-reads the logs for. The
// error was already being logged; what was missing was it saying what it meant.
func TestShutdownSaysWhenSamplesWereLost(t *testing.T) {
	buf := captureLog(t)

	logExportLoss("closing the exporter", &metrics.DroppedError{Samples: 40000})

	got := buf.String()
	if !strings.Contains(got, "DATA LOSS") {
		t.Errorf("shutdown logged %q, which does not say that telemetry was lost", got)
	}
	if !strings.Contains(got, "40000") {
		t.Errorf("shutdown logged %q, which does not say how much", got)
	}
}

// TestAnOrdinaryShutdownErrorIsNotCalledDataLoss keeps the loud line meaningful.
// A close that failed for some other reason lost nothing, and crying loss for
// it is how a real one stops being read.
func TestAnOrdinaryShutdownErrorIsNotCalledDataLoss(t *testing.T) {
	buf := captureLog(t)

	logExportLoss("closing the exporter", errors.New("connection reset"))

	if got := buf.String(); strings.Contains(got, "DATA LOSS") {
		t.Errorf("an ordinary error was reported as data loss: %q", got)
	}
}
