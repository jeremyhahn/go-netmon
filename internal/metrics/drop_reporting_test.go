package metrics

// What the exporter says about what it threw away.
//
// #69 was invisible for fifteen hours while the process was losing a million
// samples an hour, and both halves of that were reporting faults rather than
// losing faults: a counter whose siblings were in different units, so no ratio
// taken across them meant anything, and an error that named memory pressure
// whatever had actually happened. Wrong loudly is worse than silent, because it
// is acted on.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestEveryDropReasonIsDescribed is the bad-input half of droppedSamplesHelp:
// a reason added to dropReasons without a phrase would publish "(", nothing,
// ")" on /metrics, and nothing else would notice.
func TestEveryDropReasonIsDescribed(t *testing.T) {
	for _, reason := range dropReasons {
		phrase, described := dropPhrase[reason]
		if !described {
			t.Errorf("drop reason %q has no phrase, so /metrics and every DroppedError "+
				"carrying it describe the discard with an empty parenthesis", reason)
			continue
		}
		if strings.TrimSpace(phrase) == "" {
			t.Errorf("drop reason %q has an empty phrase", reason)
		}
	}

	if len(dropPhrase) != len(dropReasons) {
		t.Errorf("dropPhrase describes %d reasons and dropReasons has %d: a phrase for a "+
			"reason that cannot occur is dead text a reader will look for and not find",
			len(dropPhrase), len(dropReasons))
	}
}

// TestTheHelpTextNamesEveryReason is the success half: the description an
// operator reads on /metrics is generated from the same words the errors use,
// so the two cannot drift.
func TestTheHelpTextNamesEveryReason(t *testing.T) {
	help := droppedSamplesHelp()

	for _, reason := range dropReasons {
		if !strings.Contains(help, reason) {
			t.Errorf("the help text does not name reason %q: %s", reason, help)
		}
		if !strings.Contains(help, dropPhrase[reason]) {
			t.Errorf("the help text does not carry reason %q's phrase %q", reason, dropPhrase[reason])
		}
	}

	if !strings.Contains(help, "absence means the build does not have it") {
		t.Errorf("the help text no longer says what absence means, which is the one thing "+
			"a checker reading this counter depends on: %s", help)
	}
}

// TestADroppedErrorNamesWhyRatherThanGuessing is the defect this file is named
// for.
//
// Every DroppedError said "awaiting write to bound queue memory", whatever had
// happened. A poll superseded by a newer one -- which is a POLLER fault, two
// overlapping polls of one device -- therefore reported backpressure, and sent
// whoever read it looking for memory pressure that was not there.
func TestADroppedErrorNamesWhyRatherThanGuessing(t *testing.T) {
	for _, reason := range dropReasons {
		err := &DroppedError{Samples: 7, Reason: reason}
		message := err.Error()

		if !strings.Contains(message, dropPhrase[reason]) {
			t.Errorf("a %q discard reports %q, which does not say why", reason, message)
		}
		if !strings.Contains(message, reason) {
			t.Errorf("a %q discard reports %q, which does not name the counter label an "+
				"operator would then go and look at", reason, message)
		}
		if strings.Contains(message, "bound queue memory") {
			t.Errorf("a %q discard still reports memory pressure: %q", reason, message)
		}
	}
}

// TestADroppedErrorWithNoOneReasonDoesNotInventOne is the bad-input half.
//
// A Flush waiting behind several discards, and a shutdown that gave up on a
// mixed queue, genuinely have more than one reason. Picking one would be a
// guess stated as a fact, which is the same defect in the other direction.
func TestADroppedErrorWithNoOneReasonDoesNotInventOne(t *testing.T) {
	for _, reason := range []string{"", "not-a-reason"} {
		message := (&DroppedError{Samples: 3, Reason: reason}).Error()

		if !strings.Contains(message, "3 sample(s)") {
			t.Errorf("Reason %q lost the sample count: %q", reason, message)
		}
		for _, phrase := range dropPhrase {
			if strings.Contains(message, phrase) {
				t.Errorf("Reason %q was reported as %q, which attributes a cause nobody "+
					"established: %q", reason, phrase, message)
			}
		}
	}
}

// TestASupersededDiscardReportsSupersession is the same property through the
// real path rather than by constructing the error directly: it is discardLocked
// that has to carry the reason out, and it is the only place that knows it.
func TestASupersededDiscardReportsSupersession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	exporter, err := NewPrometheusExporter(server.URL, 1000, time.Hour, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	defer func() { _ = exporter.Close() }()

	exporter.maxSampleAge = time.Hour
	at := time.Now()

	// Establish a head for the series, then offer a reading behind it.
	if err := exporter.Export(context.Background(), []*Sample{sample(0, at)}); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if err := exporter.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if err := exporter.Export(context.Background(), []*Sample{sample(0, at.Add(-time.Minute))}); err != nil {
		t.Fatalf("Export: %v", err)
	}

	var discarded *DroppedError
	if err := exporter.Flush(context.Background()); !errors.As(err, &discarded) {
		t.Fatalf("Flush = %v, want a *DroppedError", err)
	}
	if discarded.Reason != dropSuperseded {
		t.Errorf("the reading was discarded as %q, want %q: the caller is told the wrong "+
			"fault, and the right one is a poller producing overlapping polls",
			discarded.Reason, dropSuperseded)
	}
	if !strings.Contains(discarded.Error(), dropPhrase[dropSuperseded]) {
		t.Errorf("Flush reported %q, which does not say why", discarded.Error())
	}
}

// TestFlushOutcomesAreCountedInFlushesNotSamples is the counter half.
//
// gonetmon_exports_total{status="dropped"} counted SAMPLES while its two
// siblings counted FLUSHES. rate(...{status="dropped"}) /
// rate(...{status="success"}) is how anyone would ask "what fraction are we
// losing", and it divided samples by flushes -- so #69's own measurement had to
// carry a paragraph saying its two headline figures could not be compared.
//
// The samples are counted where they mean something instead, by reason.
func TestFlushOutcomesAreCountedInFlushesNotSamples(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	exporter, err := NewPrometheusExporter(server.URL, 1000, time.Hour, nil)
	if err != nil {
		t.Fatalf("NewPrometheusExporter: %v", err)
	}
	defer func() { _ = exporter.Close() }()

	exporter.maxSampleAge = time.Hour
	own := privateCounters(exporter)
	at := time.Now()

	// One flush that lands, establishing a head for every series.
	const batch = 40
	first := make([]*Sample, 0, batch)
	for i := 0; i < batch; i++ {
		first = append(first, sample(i, at))
	}
	if err := exporter.Export(context.Background(), first); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if err := exporter.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Then one flush whose every reading is behind that head.
	second := make([]*Sample, 0, batch)
	for i := 0; i < batch; i++ {
		second = append(second, sample(i, at.Add(-time.Minute)))
	}
	if err := exporter.Export(context.Background(), second); err != nil {
		t.Fatalf("Export: %v", err)
	}
	var discarded *DroppedError
	if err := exporter.Flush(context.Background()); !errors.As(err, &discarded) {
		t.Fatalf("Flush = %v, want a *DroppedError", err)
	}

	// Every child of the flush counter is in flushes, and only one flush ever
	// reached the endpoint: the second batch was given up on before the
	// request, so there is no outcome from the endpoint to record. That is the
	// point of the counter being in flushes -- a request that was never made is
	// neither a success nor a failure of one.
	if got := testutil.ToFloat64(own.exports.WithLabelValues(exportSuccess)); got != 1 {
		t.Errorf("%v successful flushes counted, want 1", got)
	}
	if got := testutil.ToFloat64(own.exports.WithLabelValues(exportError)); got != 0 {
		t.Errorf("%v failed flushes counted, want 0", got)
	}

	// And the samples are in the counter that is in samples.
	if got := testutil.ToFloat64(own.dropped.WithLabelValues(dropSuperseded)); got != batch {
		t.Errorf("%v samples dropped as superseded, want %d", got, batch)
	}

	// The old child would have read 40 here, beside a "success" of 1, and a
	// reader dividing one by the other would have got 40 -- a loss rate of
	// 4000%, from two counters that were never comparable.
	if got := testutil.ToFloat64(own.exports.WithLabelValues("dropped")); got != 0 {
		t.Errorf("gonetmon_exports_total{status=\"dropped\"} is being written again with %v: "+
			"it counts samples among flush counters, so every ratio taken across this "+
			"metric is nonsense", got)
	}
}
