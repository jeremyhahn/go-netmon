package websocket

import (
	"context"
	"testing"
)

// A delta is only applied to the reading it was computed against; every other client gets the whole reading.

// subscriber is a client with room to be sent to, and its own inbox.
func subscriber() *Client {
	return &Client{send: make(chan []byte, 64)}
}

// TestTheDiffHappensOncePerRoundNotOncePerClient checks one diff per round serves every client.
func TestTheDiffHappensOncePerRoundNotOncePerClient(t *testing.T) {
	value := 1.0
	queries := 0
	h := NewHub(func(context.Context, string) ([]MetricValue, error) {
		queries++
		return []MetricValue{sample("1", value, 1700000000)}, nil
	}, nil)

	watchers := []*Client{subscriber(), subscriber(), subscriber()}
	for _, c := range watchers {
		h.Subscribe(c, "q")
	}

	h.streamMetrics(context.Background())
	for _, c := range watchers {
		collectFrames(t, c)
	}

	value = 2
	h.streamMetrics(context.Background())

	if queries != 2 {
		t.Errorf("Prometheus was asked %d times for one query over two rounds, "+
			"want 2: the answer is the same for every watcher", queries)
	}
	for i, c := range watchers {
		frames := readings(collectFrames(t, c))
		if len(frames) != 1 || frames[0].Type != MessageTypeMetricDelta {
			t.Fatalf("watcher %d got %+v, want one delta", i, frames)
		}
		if frames[0].Delta.Changed[0].Value != 2 {
			t.Errorf("watcher %d got %v, want the new value", i,
				frames[0].Delta.Changed[0].Value)
		}
	}
}

// TestAClientThatMissedAFrameIsSentTheWholeReading checks an unqueued frame leads to a full resend.
func TestAClientThatMissedAFrameIsSentTheWholeReading(t *testing.T) {
	value := 1.0
	h := NewHub(func(context.Context, string) ([]MetricValue, error) {
		return []MetricValue{sample("1", value, 1700000000)}, nil
	}, nil)

	// A client whose queue is full: trySend cannot deliver to it.
	client := &Client{send: make(chan []byte)}
	h.Subscribe(client, "q")

	// Round one: the snapshot is dropped on the floor.
	h.streamMetrics(context.Background())

	// The server must not believe this client holds anything.
	if at := client.at("q"); at.seq != 0 {
		t.Fatalf("the server records this client at sequence %d after a frame it "+
			"could not queue; the next delta would be applied to a reading the "+
			"browser never received", at.seq)
	}

	// Give it somewhere to receive, and move the value on.
	client.send = make(chan []byte, 64)
	value = 2
	h.streamMetrics(context.Background())

	frames := readings(collectFrames(t, client))
	if len(frames) != 1 {
		t.Fatalf("frames = %+v, want one", frames)
	}
	if frames[0].Type != MessageTypeMetricDelta {
		// This is the assertion that matters. If the server had recorded the
		// dropped frame as delivered, this client would be sent a delta
		// against a reading it never received.
		if frames[0].Type != MessageTypeMetricSnapshot {
			t.Fatalf("type = %q, want a snapshot", frames[0].Type)
		}
		if len(frames[0].Metrics) != 1 || frames[0].Metrics[0].Value != 2 {
			t.Errorf("snapshot carried %+v, want the current reading", frames[0].Metrics)
		}
		return
	}
	t.Error("a client that never received the first frame was sent a delta " +
		"against it; the reading it renders would be stale with nothing saying so")
}

// TestAClientThatLostItsPlaceAsksAndIsResynced checks a client resync request forces a full reading.
func TestAClientThatLostItsPlaceAsksAndIsResynced(t *testing.T) {
	value := 1.0
	h := NewHub(func(context.Context, string) ([]MetricValue, error) {
		return []MetricValue{sample("1", value, 1700000000)}, nil
	}, nil)

	client := subscriber()
	h.Subscribe(client, "q")
	h.streamMetrics(context.Background())
	collectFrames(t, client)

	// The browser could not apply what it was sent and says so.
	client.forget("q")

	value = 2
	h.streamMetrics(context.Background())

	frames := readings(collectFrames(t, client))
	if len(frames) != 1 || frames[0].Type != MessageTypeMetricSnapshot {
		t.Fatalf("frames = %+v, want the whole reading after a resync", frames)
	}
	if len(frames[0].Metrics) != 1 || frames[0].Metrics[0].Value != 2 {
		t.Errorf("snapshot carried %+v, want the current reading", frames[0].Metrics)
	}
}

// TestANewSubscriberToAMovingQueryGetsASnapshotWhileOthersGetTheDelta is the
// fan-out rule with both kinds of client in one round.
func TestANewSubscriberToAMovingQueryGetsASnapshotWhileOthersGetTheDelta(t *testing.T) {
	value := 1.0
	h := NewHub(func(context.Context, string) ([]MetricValue, error) {
		return []MetricValue{sample("1", value, 1700000000)}, nil
	}, nil)

	established := subscriber()
	h.Subscribe(established, "q")
	h.streamMetrics(context.Background())
	collectFrames(t, established)

	joined := subscriber()
	h.Subscribe(joined, "q")

	value = 2
	h.streamMetrics(context.Background())

	if got := readings(collectFrames(t, established)); len(got) != 1 ||
		got[0].Type != MessageTypeMetricDelta {
		t.Errorf("the established client got %+v, want a delta", got)
	}
	newcomer := readings(collectFrames(t, joined))
	if len(newcomer) != 1 || newcomer[0].Type != MessageTypeMetricSnapshot {
		t.Fatalf("the new client got %+v, want the whole reading", newcomer)
	}
	if len(newcomer[0].Metrics) != 1 || newcomer[0].Metrics[0].Value != 2 {
		t.Errorf("snapshot carried %+v, want the current reading", newcomer[0].Metrics)
	}
}

// TestANewSubscriberToASteadyQueryIsNotLeftWaiting checks a new subscriber gets a steady query's value.
func TestANewSubscriberToASteadyQueryIsNotLeftWaiting(t *testing.T) {
	h := NewHub(staticQuery([]MetricValue{sample("1", 42, 1700000000)}), nil)

	established := subscriber()
	h.Subscribe(established, "q")
	h.streamMetrics(context.Background())
	collectFrames(t, established)

	joined := subscriber()
	h.Subscribe(joined, "q")
	h.streamMetrics(context.Background())

	frames := readings(collectFrames(t, joined))
	if len(frames) != 1 || frames[0].Type != MessageTypeMetricSnapshot {
		t.Fatalf("the new client got %+v, want the current reading", frames)
	}
	if len(frames[0].Metrics) != 1 || frames[0].Metrics[0].Value != 42 {
		t.Errorf("snapshot carried %+v, want the steady reading", frames[0].Metrics)
	}
	// And the client that already had it is not sent it again.
	if got := readings(collectFrames(t, established)); len(got) != 0 {
		t.Errorf("the established client was re-sent %+v", got)
	}
}

// TestARemovedSeriesIsCarriedInTheDelta checks deltas carry removed series.
func TestARemovedSeriesIsCarriedInTheDelta(t *testing.T) {
	both := true
	h := NewHub(func(context.Context, string) ([]MetricValue, error) {
		if both {
			return []MetricValue{sample("1", 1, 1700000000), sample("2", 2, 1700000000)}, nil
		}
		return []MetricValue{sample("1", 1, 1700000000)}, nil
	}, nil)

	client := subscriber()
	h.Subscribe(client, "q")
	h.streamMetrics(context.Background())
	collectFrames(t, client)

	both = false
	h.streamMetrics(context.Background())

	frames := readings(collectFrames(t, client))
	if len(frames) != 1 || frames[0].Delta == nil {
		t.Fatalf("frames = %+v, want a delta", frames)
	}
	if len(frames[0].Delta.Removed) != 1 {
		t.Fatalf("removed = %+v, want the series that went away", frames[0].Delta.Removed)
	}
	if frames[0].Delta.Removed[0]["ifindex"] != "2" {
		t.Errorf("removed %v, want ifindex 2", frames[0].Delta.Removed[0])
	}
	// The row that did not change is not re-sent.
	if len(frames[0].Delta.Changed) != 0 {
		t.Errorf("changed = %+v, want none: only one series moved and it was removed",
			frames[0].Delta.Changed)
	}
}

// TestTheWholeReadingIsResentPeriodically checks periodic full resends.
func TestTheWholeReadingIsResentPeriodically(t *testing.T) {
	value := 0.0
	h := NewHub(func(context.Context, string) ([]MetricValue, error) {
		return []MetricValue{sample("1", value, 1700000000)}, nil
	}, nil)

	client := subscriber()
	h.Subscribe(client, "q")

	seen := 0
	for round := 0; round <= SnapshotInterval+1; round++ {
		value++
		h.streamMetrics(context.Background())
		for _, frame := range snapshots(collectFrames(t, client)) {
			if len(frame.Metrics) == 0 {
				t.Fatalf("a snapshot carried no reading: %+v", frame)
			}
			seen++
		}
	}

	// One for the join, and one when the interval came round.
	if seen < 2 {
		t.Errorf("%d whole readings in %d changed rounds, want at least 2: a "+
			"client can otherwise never be corrected", seen, SnapshotInterval+2)
	}
}

// TestASteadyReadingStillSendsNothing keeps the saving the deltas were built
// on top of. The heartbeat says the query is steady; the numbers do not move.
func TestASteadyReadingStillSendsNothing(t *testing.T) {
	h := NewHub(staticQuery([]MetricValue{sample("1", 42, 1700000000)}), nil)

	client := subscriber()
	h.Subscribe(client, "q")
	h.streamMetrics(context.Background())
	collectFrames(t, client)

	h.streamMetrics(context.Background())

	frames := collectFrames(t, client)
	if got := readings(frames); len(got) != 0 {
		t.Errorf("an unchanged reading sent %+v", got)
	}
	if len(heartbeats(frames)) != 1 {
		t.Errorf("an unchanged reading sent %d heartbeats, want 1: the charts "+
			"still have to reach this tick", len(heartbeats(frames)))
	}
}

// TestAQueryNobodyWatchesIsNotRemembered checks unwatched queries are dropped.
func TestAQueryNobodyWatchesIsNotRemembered(t *testing.T) {
	h := NewHub(staticQuery([]MetricValue{sample("1", 42, 1700000000)}), nil)

	client := subscriber()
	h.Subscribe(client, "q")
	h.streamMetrics(context.Background())
	collectFrames(t, client)

	if h.states.reading("q") == nil {
		t.Fatal("the hub kept no reading for a subscribed query, so nothing " +
			"could be diffed against it")
	}

	h.Unsubscribe(client, "q")

	if held := h.states.reading("q"); held != nil {
		t.Errorf("the hub still holds %+v for a query nobody watches; it would "+
			"hold one of these for every query anyone has ever opened", held)
	}

	// And the query starts over rather than continuing from a sequence whose
	// reading is older than the gap.
	h.Subscribe(client, "q")
	h.streamMetrics(context.Background())

	frames := readings(collectFrames(t, client))
	if len(frames) != 1 || frames[0].Type != MessageTypeMetricSnapshot {
		t.Fatalf("frames = %+v, want the whole reading", frames)
	}
	if frames[0].Seq != 1 {
		t.Errorf("seq = %d, want 1: the query is new again", frames[0].Seq)
	}
}

// TestSeriesIdentityIsNotAmbiguous guards the key the diff is built on.
//
// Two different series keying the same would make one of them invisible: the
// diff would see a value change where there were two rows, and the browser
// would be told to overwrite the wrong one.
func TestSeriesIdentityIsNotAmbiguous(t *testing.T) {
	tests := []struct {
		name string
		a, b map[string]string
	}{
		{
			// The pair a naive name+value concatenation collapses.
			name: "a label boundary that could be misread",
			a:    map[string]string{"a": "bc"},
			b:    map[string]string{"ab": "c"},
		},
		{
			name: "one label against two",
			a:    map[string]string{"device": "a", "ifindex": "1"},
			b:    map[string]string{"device": "a1"},
		},
		{
			name: "a value containing the separator",
			a:    map[string]string{"device": "a:b"},
			b:    map[string]string{"device": "a", "b": ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if seriesKey(tt.a) == seriesKey(tt.b) {
				t.Errorf("%v and %v key the same (%q); one row would overwrite the other",
					tt.a, tt.b, seriesKey(tt.a))
			}
		})
	}

	// And the same labels key the same however the map is iterated.
	same := map[string]string{"device": "a", "ifindex": "1", "ifdescr": "Gi0/0/0"}
	first := seriesKey(same)
	for i := 0; i < 20; i++ {
		if seriesKey(same) != first {
			t.Fatal("one series keyed two ways; every round would look like a change")
		}
	}
}

// TestAResyncThatLandsMidDeliveryIsNotOverwritten checks the epoch protects a resync against a concurrent mark.
func TestAResyncThatLandsMidDeliveryIsNotOverwritten(t *testing.T) {
	client := subscriber()

	// What a delivery captured before the browser gave up.
	before := client.at("q")

	// The browser says it has lost its place.
	client.forget("q")

	// The delivery, which started earlier, now records what it sent.
	client.mark("q", held{seq: 7, epoch: before.epoch})

	if at := client.at("q"); at.seq != 0 {
		t.Errorf("the client is recorded at sequence %d, want 0: it asked to be "+
			"resent and that has been overwritten by a frame it had already "+
			"given up on", at.seq)
	}
}

// TestADeliveryInTheCurrentEpochIsRecorded is the other half: the guard must
// not swallow an ordinary delivery.
func TestADeliveryInTheCurrentEpochIsRecorded(t *testing.T) {
	client := subscriber()

	at := client.at("q")
	client.mark("q", held{seq: 7, epoch: at.epoch})

	if got := client.at("q"); got.seq != 7 {
		t.Errorf("sequence = %d, want 7: an uncontested delivery must be recorded, "+
			"or every frame is a snapshot", got.seq)
	}
}

// TestAQueryUnsubscribedMidRoundIsNotLeftBehind checks the post-round sweep.
func TestAQueryUnsubscribedMidRoundIsNotLeftBehind(t *testing.T) {
	client := subscriber()
	var h *Hub

	// Unsubscribe from inside the query, which is exactly when the round is in
	// flight and the state has just been deleted.
	h = NewHub(func(context.Context, string) ([]MetricValue, error) {
		h.Unsubscribe(client, "q")
		return []MetricValue{sample("1", 1, 1700000000)}, nil
	}, nil)

	h.Subscribe(client, "q")
	h.streamMetrics(context.Background())

	if held := h.states.reading("q"); held != nil {
		t.Errorf("the hub still holds %+v for a query nobody watches; nothing "+
			"would ever take it out again", held)
	}
}

// TestReSubscribingToASteadyQueryResendsIt checks a resubscribe gets the full reading.
func TestReSubscribingToASteadyQueryResendsIt(t *testing.T) {
	// A reading that never moves, so nothing but an explicit resend can fill
	// the panel.
	h := NewHub(staticQuery([]MetricValue{sample("1", 42, 1700000000)}), nil)

	client := subscriber()
	h.Subscribe(client, "q")
	h.streamMetrics(context.Background())
	if got := readings(collectFrames(t, client)); len(got) != 1 {
		t.Fatalf("the first round delivered %+v, want the reading", got)
	}

	// The tab is re-rendered: the browser drops the query and asks again.
	h.Unsubscribe(client, "q")
	h.Subscribe(client, "q")
	h.streamMetrics(context.Background())

	frames := readings(collectFrames(t, client))
	if len(frames) != 1 || frames[0].Type != MessageTypeMetricSnapshot {
		t.Fatalf("frames = %+v, want the whole reading again: the markup it was "+
			"drawn into no longer exists", frames)
	}
	if len(frames[0].Metrics) != 1 || frames[0].Metrics[0].Value != 42 {
		t.Errorf("snapshot carried %+v, want the steady reading", frames[0].Metrics)
	}
}

// TestASecondSubscribeToAQueryAlreadyHeldDoesNotResetIt checks a repeat subscribe is idempotent.
func TestASecondSubscribeToAQueryAlreadyHeldDoesNotResetIt(t *testing.T) {
	h := NewHub(staticQuery([]MetricValue{sample("1", 42, 1700000000)}), nil)

	client := subscriber()
	h.Subscribe(client, "q")
	h.streamMetrics(context.Background())
	collectFrames(t, client)

	// Already subscribed: the hub returns early and nothing is forgotten.
	h.Subscribe(client, "q")
	h.streamMetrics(context.Background())

	frames := collectFrames(t, client)
	if got := readings(frames); len(got) != 0 {
		t.Errorf("a repeated subscribe resent %+v; a reconnecting browser would "+
			"pull every reading it already has", got)
	}
	if len(heartbeats(frames)) != 1 {
		t.Errorf("heartbeats = %d, want 1: the client is up to date and its charts "+
			"still have to reach this tick", len(heartbeats(frames)))
	}
}

// TestANewSubscriberToABackedOffQueryIsNotLeftWaiting checks a new subscriber is served on any round parity.
func TestANewSubscriberToABackedOffQueryIsNotLeftWaiting(t *testing.T) {
	h := NewHub(staticQuery([]MetricValue{sample("1", 42, 1700000000)}), nil)

	established := subscriber()
	h.Subscribe(established, "q")

	// Both round parities, since which rounds a backed-off query lands on depends on the counter.
	for _, warmup := range []int{SteadyRoundsBeforeBackoff + 1, SteadyRoundsBeforeBackoff + 2} {
		for i := 0; i < warmup; i++ {
			h.streamMetrics(context.Background())
		}
		collectFrames(t, established)

		joined := subscriber()
		h.Subscribe(joined, "q")
		h.streamMetrics(context.Background())

		frames := readings(collectFrames(t, joined))
		if len(frames) != 1 || frames[0].Type != MessageTypeMetricSnapshot {
			t.Fatalf("after %d warm-up rounds the new client got %+v, want the "+
				"current reading: it has no value to hold steady, and being told a "+
				"query is unchanged is not the same as being told what it reads",
				warmup, frames)
		}
		if len(frames[0].Metrics) != 1 || frames[0].Metrics[0].Value != 42 {
			t.Errorf("after %d warm-up rounds the snapshot carried %+v, want the "+
				"steady reading", warmup, frames[0].Metrics)
		}

		// Waking must not send existing holders a frame.
		if got := readings(collectFrames(t, established)); len(got) != 0 {
			t.Errorf("after %d warm-up rounds the established client was re-sent "+
				"%+v; the reading has not moved and it already has it", warmup, got)
		}
	}
}

// steadyHeld reads the tracker's maps directly rather than through queries().
func steadyHeld(s *steadyQueryRounds) (rounds, last int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.rounds), len(s.last)
}

// TestTheSteadyTrackerIsSweptWhenTheLastReaderLeavesMidRound checks the sweep clears h.steady as well as h.states.
func TestTheSteadyTrackerIsSweptWhenTheLastReaderLeavesMidRound(t *testing.T) {
	h := NewHub(staticQuery([]MetricValue{sample("1", 42, 1700000000)}), nil)

	client := subscriber()
	h.Subscribe(client, "q")
	h.streamMetrics(context.Background())

	// The reading is recorded, so the tracker holds the query.
	if rounds, last := steadyHeld(h.steady); rounds == 0 && last == 0 {
		t.Fatal("the steady tracker holds nothing after a round; the rest of this " +
			"test would pass against a tracker that never records anything")
	}

	// The last reader leaves; detach clears both trackers.
	h.Unsubscribe(client, "q")
	if rounds, last := steadyHeld(h.steady); rounds != 0 || last != 0 {
		t.Fatalf("detach left %d streak(s) and %d reading(s) behind; this test is "+
			"about the race, and the clean path has to be clean first for that "+
			"to be the thing it measures", rounds, last)
	}

	// The race is a round already in flight when the last reader left. Its
	// query returns afterwards and streamQuery records the reading -- exactly
	// this call, at hub.go:369 -- putting the entry back behind detach. From
	// here nothing else will ever remove it: removal happens on the transition
	// to zero subscribers, and that has been and gone.
	h.steady.observe("q", 1)

	h.streamMetrics(context.Background())

	if rounds, last := steadyHeld(h.steady); rounds != 0 || last != 0 {
		t.Errorf("the steady tracker still holds %d streak(s) and %d reading(s) "+
			"with no subscribers; a later sole subscriber would inherit a backoff "+
			"with nobody else on the query, and the entry is never collected",
			rounds, last)
	}

	// The leak wake leaves: a reading with no streak, which a streak-only sweep misses.
	h.steady.observe("q", 1)
	h.steady.wake("q")

	h.streamMetrics(context.Background())

	if rounds, last := steadyHeld(h.steady); rounds != 0 || last != 0 {
		t.Errorf("the steady tracker still holds %d streak(s) and %d reading(s) "+
			"after a wake left only the reading behind; the sweep is consulting "+
			"one of its two maps", rounds, last)
	}
}
