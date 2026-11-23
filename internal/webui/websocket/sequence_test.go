package websocket

import (
	"context"
	"testing"
)

// The rules a delta stream has to obey, and the one it exists to make safe.
//
// A delta is only ever applied to the reading it was computed against. Every
// other client -- new, behind, or one frame short of an answer that never
// arrived -- gets the whole reading instead. That is what makes it impossible
// for a dropped frame to leave a stale value rendering as a current one, and
// it is the property every test below is a different angle on.

// subscriber is a client with room to be sent to, and its own inbox.
func subscriber() *Client {
	return &Client{send: make(chan []byte, 64)}
}

// TestTheDiffHappensOncePerRoundNotOncePerClient is the shape of the whole
// design.
//
// Ten browsers on the same dashboard read the same numbers. Diffing per client
// would be the same work ten times over, and it is not even the work that
// differs between them -- only what each has already seen, which is one integer
// each.
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

// TestAClientThatMissedAFrameIsSentTheWholeReading is the correctness bar.
//
// A frame that could not be queued never reached the browser, so the record of
// what that browser holds must not claim otherwise. The next round has to find
// it behind and send the reading whole -- because the alternative is a delta
// applied to a reading the client does not have, which is a panel showing
// numbers that are no longer true with nothing on screen to say so.
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

// TestAClientThatLostItsPlaceAsksAndIsResynced is the half the server cannot
// see.
//
// A frame can be queued, accepted by the socket and still never applied -- a
// browser that threw while rendering it. The server believes that client is up
// to date and will keep sending deltas it cannot use. Saying so is the client's
// job, and forgetting what it was believed to hold is the whole of the answer.
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

// TestANewSubscriberToASteadyQueryIsNotLeftWaiting covers the join that would
// otherwise wait for the value to move.
//
// A query that has not changed sends nobody a frame. A client that has just
// subscribed to it holds nothing, so without this its cards sit on "waiting for
// data" until something happens -- which for a healthy, steady reading may be
// never.
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

// TestARemovedSeriesIsCarriedInTheDelta is the change a value-only diff would
// miss.
//
// An interface that goes away stops being in the reading. Sending only the
// series that CHANGED would leave the browser drawing a line for a row that no
// longer exists, forever.
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

// TestTheWholeReadingIsResentPeriodically bounds drift nothing else would
// catch.
//
// A client whose sequence is right while its contents are not -- a rendering
// bug, a frame the browser accepted and mishandled -- would otherwise never be
// corrected, because every subsequent frame is a delta it believes it can
// apply.
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

// TestAQueryNobodyWatchesIsNotRemembered bounds what a long-running hub holds.
//
// The last reading of every query is kept so the next one can be diffed
// against it. A dashboard is opened, looked at and closed; a browser moves
// between devices all day. Without this the hub accumulates one whole reading
// per query anyone has ever subscribed to, for the life of the process.
//
// It is also what makes a resubscribed query start over rather than continue
// from a sequence whose reading is arbitrarily old.
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

// TestAResyncThatLandsMidDeliveryIsNotOverwritten is the race between the two
// goroutines that touch what a client is believed to hold.
//
// A frame is queued and its sequence recorded in two steps. The client's own
// read loop runs in between and can process a resync -- the browser saying it
// could not apply what it was sent. Without the epoch, the record written
// second silently undoes the clearing that happened first: the server goes on
// sending deltas the browser has already given up on, the browser stops asking
// because it is already waiting, and a steady query then replays its old value
// at new timestamps for as long as the page is open.
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

// TestAQueryUnsubscribedMidRoundIsNotLeftBehind covers the sweep.
//
// Unsubscribing drops the query's state, but it can happen WHILE the round is
// running: the state is deleted, the query already in flight returns, and the
// diff puts it back. Nothing would take it out again, because the removal only
// happens on the transition to zero subscribers and that has been and gone.
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

// TestReSubscribingToASteadyQueryResendsIt is the blank-pane bug, at the layer
// that has to prevent it.
//
// A browser re-renders a tab -- a reload, a MIB assignment, a device the reader
// came back to -- and the markup it was drawing into is gone. It resubscribes,
// and if the server still believes that client holds the reading, it sends a
// heartbeat naming the query instead of the numbers. On a query that is STEADY
// there is no next change to rescue it: the panel waits for one that may never
// come, for as long as the page is open.
//
// The scheme this replaced kept a per-client FINGERPRINT that survived exactly
// that cycle. Measured against the live appliance afterwards: re-rendering a
// 42-panel tab produced 42 snapshots and every panel refilled within one tick.
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

// TestASecondSubscribeToAQueryAlreadyHeldDoesNotResetIt is the other side of
// that, and the reason the reset lives in Subscribe rather than in the render.
//
// Subscribing to a query a client already holds is idempotent -- it happens on
// every reconnect and every overlapping render. If it resent the whole reading
// each time, a browser that resubscribed on a timer would pull a full snapshot
// of every query it holds, which is the traffic the deltas exist to remove.
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

// TestANewSubscriberToABackedOffQueryIsNotLeftWaiting is the same guarantee as
// TestANewSubscriberToASteadyQueryIsNotLeftWaiting, after the query has been
// steady long enough to be asked about at half rate.
//
// A query that has not moved for SteadyRoundsBeforeBackoff rounds is asked only
// on even rounds. Subscribing did not reset that, so a client inherited a
// backoff it had no part in creating; on a skipped round the hub told it the
// query was "still reading what it already has", which it did not have.
// Measured before the fix: with the round counter odd the new client received
// nothing at all that round.
//
// The existing test cannot see this. It runs two rounds and the backoff needs
// six, so it only ever exercises the path where every query is asked.
//
// It matters beyond one wasted tick because the dashboard's row table is a
// table of CURRENT VALUES, which are mostly constants: opening a device someone
// else is already watching would show blanks in exactly the columns that are
// healthy, and that reads as a table bug rather than a transport one.
func TestANewSubscriberToABackedOffQueryIsNotLeftWaiting(t *testing.T) {
	h := NewHub(staticQuery([]MetricValue{sample("1", 42, 1700000000)}), nil)

	established := subscriber()
	h.Subscribe(established, "q")

	// Both parities, because which rounds a backed-off query lands on depends
	// only on the round counter. A warm-up of a fixed length joins on whichever
	// parity that arithmetic happens to produce, and then passes or fails by
	// luck rather than by the rule -- a fixture too regular to tell "always
	// served" from "served on even rounds". Measured: the odd arm alone catches
	// this, and the even arm alone passes with the defect present.
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

		// Waking the query must not cost the clients that already have it a
		// frame: the reading has not moved and they are holding it. Today that
		// holds because who receives a frame is decided by states.advance
		// rather than by the steady bookkeeping wake touches -- so this does
		// not discriminate between clearing the streak and clearing the
		// reading, and it is not offered as though it did. It guards the
		// property, not the mechanism.
		if got := readings(collectFrames(t, established)); len(got) != 0 {
			t.Errorf("after %d warm-up rounds the established client was re-sent "+
				"%+v; the reading has not moved and it already has it", warmup, got)
		}
	}
}

// steadyHeld reports what the tracker is holding, reading its maps rather than
// calling queries().
//
// The sweep uses queries(), so an assertion that also used it would share an
// accessor with the code under test: a queries() that reported only one of the
// two maps would hide the leak from the sweep AND from the assertion, and the
// test would pass while the entry sat there. Measured -- that is exactly what
// the first version of this test did.
func steadyHeld(s *steadyQueryRounds) (rounds, last int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.rounds), len(s.last)
}

// TestTheSteadyTrackerIsSweptWhenTheLastReaderLeavesMidRound covers the second
// structure the post-round sweep has to check.
//
// detach clears the steady tracker when the last subscriber goes, so the clean
// path was already right. The race is not the clean path: the last reader can
// leave WHILE a round is running, the query that was already in flight returns,
// and observe records its reading after detach has removed it. Nothing takes it
// out again, because removal only happens on the transition to zero
// subscribers and that has been and gone.
//
// forgetUnsubscribed exists for exactly that race and swept only h.states, so
// h.steady kept rounds for a query with no subscribers at all -- and a later
// genuinely-sole subscriber inherited a backoff with nobody else on the query.
//
// The shape is the one this repository keeps meeting: two structures that must
// agree about what is live, and only one of them swept. See go-netmon#71 for
// the same pair in the poller.
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

	// The last reader leaves. detach clears both trackers, which is the CLEAN
	// path and was always right -- a test that stops here passes with or
	// without the sweep, because there is nothing left to sweep.
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

	// The same leak in the shape wake leaves behind, because the sweep has to
	// see BOTH of the tracker's maps.
	//
	// observe writes the reading and the streak together, so a sweep reading
	// either map alone would find that leak. wake deletes only the streak --
	// and wake runs on every Subscribe -- so a query woken and then abandoned
	// mid-round sits in the reading map with no streak beside it. A sweep that
	// consults the streaks alone walks straight past it.
	h.steady.observe("q", 1)
	h.steady.wake("q")

	h.streamMetrics(context.Background())

	if rounds, last := steadyHeld(h.steady); rounds != 0 || last != 0 {
		t.Errorf("the steady tracker still holds %d streak(s) and %d reading(s) "+
			"after a wake left only the reading behind; the sweep is consulting "+
			"one of its two maps", rounds, last)
	}
}
