package websocket

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// Only what changed goes on the wire.
//
// The hub re-queried every subscription every 5 seconds and sent the whole
// result each time, whether or not anything in it had moved. Devices are polled
// every 30 to 60 seconds, so eleven of every twelve frames carried a value the
// browser already had. Multiplied by the subscriptions a dashboard holds and
// the sessions watching, that was the bulk of the traffic.
//
// The trap: an instant query is stamped with the time it was evaluated, not
// with the scrape the value came from, so an unchanged reading arrives with a
// later timestamp every single round. A change test that looks at the timestamp
// therefore fires every round and saves nothing. Only the rows and the numbers
// count.
//
// What a steady reading still gets is a heartbeat naming its query. The
// dashboard is a tick-based live view: its charts have to advance while a value
// holds, or the line stops at the last change and reads as data having stopped.
// The browser holds the reading already, so the heartbeat carries names, not
// numbers.

// readings keeps only the frames that carry data, of either kind.
func readings(frames []ServerMessage) []ServerMessage {
	var out []ServerMessage
	for _, f := range frames {
		if f.Type == MessageTypeMetricSnapshot || f.Type == MessageTypeMetricDelta {
			out = append(out, f)
		}
	}
	return out
}

// snapshots keeps the whole readings, for the tests that care which kind.
func snapshots(frames []ServerMessage) []ServerMessage {
	var out []ServerMessage
	for _, f := range frames {
		if f.Type == MessageTypeMetricSnapshot {
			out = append(out, f)
		}
	}
	return out
}

// heartbeats keeps only the frames that say a query is steady.
func heartbeats(frames []ServerMessage) []ServerMessage {
	var out []ServerMessage
	for _, f := range frames {
		if f.Type == MessageTypeMetricsUnchanged {
			out = append(out, f)
		}
	}
	return out
}

// collectFrames reads everything queued for a client without blocking.
func collectFrames(t *testing.T, c *Client) []ServerMessage {
	t.Helper()

	var out []ServerMessage
	for {
		select {
		case raw := <-c.send:
			var msg ServerMessage
			if err := json.Unmarshal(raw, &msg); err != nil {
				t.Fatalf("decoding frame: %v", err)
			}
			out = append(out, msg)
		default:
			return out
		}
	}
}

// staticQuery answers with the same samples every time.
func staticQuery(metrics []MetricValue) QueryFunc {
	return func(context.Context, string) ([]MetricValue, error) {
		// A fresh slice each round, as a real query would return.
		out := make([]MetricValue, len(metrics))
		copy(out, metrics)
		return out, nil
	}
}

func sample(index string, value float64, at int64) MetricValue {
	return MetricValue{
		Metric:    map[string]string{"__name__": "snmp_ifInOctets", "device": "core-01", "ifindex": index},
		Value:     value,
		Timestamp: at,
	}
}

// TestUnchangedResultIsNotResent is the requirement. The first round delivers
// the value; later rounds that find the same sample deliver nothing.
func TestUnchangedResultIsNotResent(t *testing.T) {
	h := NewHub(staticQuery([]MetricValue{sample("1", 42, 1700000000)}), nil)
	client := &Client{send: make(chan []byte, 64)}
	client.announce([]string{CapabilityAllSteady}) // this browser can apply an all-steady frame
	h.Subscribe(client, `snmp_ifInOctets{device="core-01"}`)

	h.streamMetrics(context.Background())
	if got := len(readings(collectFrames(t, client))); got != 1 {
		t.Fatalf("first round sent %d readings, want 1: a new subscriber must get the current value", got)
	}

	for range 5 {
		h.streamMetrics(context.Background())
	}

	frames := collectFrames(t, client)
	if got := len(readings(frames)); got != 0 {
		t.Errorf("five unchanged rounds re-sent the reading %d times, want none", got)
	}
	// But the browser is told each round, so its charts keep advancing.
	beats := heartbeats(frames)
	if len(beats) != 5 {
		t.Errorf("five unchanged rounds sent %d heartbeats, want one per round: "+
			"without them a steady chart stops at its last change", len(beats))
	}
	for _, beat := range beats {
		// This client holds a single subscription, so a steady round is an
		// all-steady round: the flag is set and no query is named, and the
		// browser advances every reading it holds -- here, the one.
		if !beat.AllSteady {
			t.Error("heartbeat did not set all_steady though every subscription was steady")
		}
		if len(beat.Queries) != 0 {
			t.Errorf("heartbeat named %v; the all-steady flag re-lists nothing", beat.Queries)
		}
		if len(beat.Metrics) != 0 {
			t.Errorf("heartbeat carried %d samples; it exists to carry none", len(beat.Metrics))
		}
		if beat.Timestamp == 0 {
			t.Error("heartbeat carried no timestamp, so a chart cannot know how far to advance")
		}
	}
}

// TestALaterEvaluationOfTheSameValueIsNotAChange is the production case, and
// the one an earlier version of this got wrong.
//
// Prometheus evaluates an instant query at the moment it is asked and stamps
// the result with that time, so a device polled every 60 seconds reports the
// same number under twelve different timestamps. Measured against the deployed
// stack, snmp_hrSystemProcesses read 37 at t=1786583794 and 37 again at
// t=1786583800. If the timestamp counts, nothing is ever unchanged.
func TestALaterEvaluationOfTheSameValueIsNotAChange(t *testing.T) {
	at := int64(1786583794)
	h := NewHub(func(context.Context, string) ([]MetricValue, error) {
		return []MetricValue{sample("1", 37, at)}, nil
	}, nil)

	client := &Client{send: make(chan []byte, 64)}
	h.Subscribe(client, `snmp_hrSystemProcesses{device="lab-node-1-bmc"}`)

	h.streamMetrics(context.Background())
	collectFrames(t, client)

	// Re-evaluated six seconds later; the device has not been polled since.
	at = 1786583800
	h.streamMetrics(context.Background())

	if got := readings(collectFrames(t, client)); len(got) != 0 {
		t.Errorf("the same reading under a later evaluation time was re-sent %d times, want none; "+
			"a timestamp that moves every round makes every round look like a change", len(got))
	}
}

// TestChangedResultIsSentAsADelta is the other half: a moving value must still
// arrive, and now it arrives as what moved rather than as everything.
func TestChangedResultIsSentAsADelta(t *testing.T) {
	value := 42.0
	at := int64(1700000000)
	h := NewHub(func(context.Context, string) ([]MetricValue, error) {
		return []MetricValue{sample("1", value, at)}, nil
	}, nil)

	client := &Client{send: make(chan []byte, 64)}
	h.Subscribe(client, `snmp_ifInOctets{device="core-01"}`)

	h.streamMetrics(context.Background())
	first := readings(collectFrames(t, client))
	if len(first) != 1 || first[0].Type != MessageTypeMetricSnapshot {
		t.Fatalf("the first frame was %+v, want a snapshot", first)
	}
	base := first[0].Seq

	value, at = 43, 1700000060
	h.streamMetrics(context.Background())

	frames := readings(collectFrames(t, client))
	if len(frames) != 1 {
		t.Fatalf("a changed value produced %d readings, want 1", len(frames))
	}
	if frames[0].Type != MessageTypeMetricDelta {
		t.Fatalf("type = %q, want a delta: the client holds the previous reading",
			frames[0].Type)
	}
	if frames[0].BaseSeq != base {
		t.Errorf("base_seq = %d, want %d: a delta has to name what it applies to",
			frames[0].BaseSeq, base)
	}
	if frames[0].Seq != base+1 {
		t.Errorf("seq = %d, want %d", frames[0].Seq, base+1)
	}
	if frames[0].Delta == nil || len(frames[0].Delta.Changed) != 1 ||
		frames[0].Delta.Changed[0].Value != 43 {
		t.Fatalf("delta carried %+v, want the new value", frames[0].Delta)
	}
	if frames[0].Delta.Changed[0].Timestamp != 1700000060 {
		t.Errorf("sample timestamp = %d, want the moment it was collected",
			frames[0].Delta.Changed[0].Timestamp)
	}
	// And nothing the client already has.
	if len(frames[0].Metrics) != 0 {
		t.Errorf("the delta carried the whole reading as well: %+v", frames[0].Metrics)
	}
}

// TestANewSubscriberGetsTheCurrentValue covers the join case. Change detection
// is per client, not global: someone opening a dashboard must see the value
// immediately even though it has not moved for an hour and everyone else
// already has it.
func TestANewSubscriberGetsTheCurrentValue(t *testing.T) {
	const query = `snmp_ifInOctets{device="core-01"}`
	h := NewHub(staticQuery([]MetricValue{sample("1", 42, 1700000000)}), nil)

	early := &Client{send: make(chan []byte, 64)}
	h.Subscribe(early, query)
	h.streamMetrics(context.Background())
	collectFrames(t, early)

	late := &Client{send: make(chan []byte, 64)}
	h.Subscribe(late, query)
	h.streamMetrics(context.Background())

	if got := len(readings(collectFrames(t, late))); got != 1 {
		t.Errorf("a client that just subscribed got %d readings, want the current value", got)
	}
	if got := len(readings(collectFrames(t, early))); got != 0 {
		t.Errorf("a client that already had the value was sent it again %d times, want none", got)
	}
}

// TestAVanishedSeriesIsAChange guards the case a value-only fingerprint would
// miss: the numbers that remain are identical, but a row has gone.
func TestAVanishedSeriesIsAChange(t *testing.T) {
	metrics := []MetricValue{sample("1", 42, 1700000000), sample("2", 42, 1700000000)}
	h := NewHub(func(context.Context, string) ([]MetricValue, error) {
		return metrics, nil
	}, nil)

	client := &Client{send: make(chan []byte, 64)}
	h.Subscribe(client, `snmp_ifInOctets{device="core-01"}`)
	h.streamMetrics(context.Background())
	collectFrames(t, client)

	// The interface went away; the surviving row is untouched.
	metrics = metrics[:1]
	h.streamMetrics(context.Background())

	if got := len(readings(collectFrames(t, client))); got != 1 {
		t.Errorf("a series disappearing produced %d readings, want 1: the dashboard would still be showing it", got)
	}
}

// TestResultOrderIsNotAChange keeps the fingerprint stable against Prometheus
// returning the same series in a different order, which would otherwise make
// every round look like a change and undo the whole optimisation.
func TestResultOrderIsNotAChange(t *testing.T) {
	forward := []MetricValue{sample("1", 1, 1700000000), sample("2", 2, 1700000000)}
	reversed := []MetricValue{sample("2", 2, 1700000000), sample("1", 1, 1700000000)}

	order := forward
	h := NewHub(func(context.Context, string) ([]MetricValue, error) { return order, nil }, nil)

	client := &Client{send: make(chan []byte, 64)}
	h.Subscribe(client, `snmp_ifInOctets{device="core-01"}`)
	h.streamMetrics(context.Background())
	collectFrames(t, client)

	order = reversed
	h.streamMetrics(context.Background())

	if got := len(readings(collectFrames(t, client))); got != 0 {
		t.Errorf("reordering the same samples produced %d readings, want none", got)
	}
}

// TestUnsubscribeForgetsWhatTheClientHadSeen stops the per-client record from
// growing forever as an operator moves between devices, and makes a re-visit
// deliver the value again rather than assume the browser still has it.
func TestUnsubscribeForgetsWhatTheClientHadSeen(t *testing.T) {
	const query = `snmp_ifInOctets{device="core-01"}`
	h := NewHub(staticQuery([]MetricValue{sample("1", 42, 1700000000)}), nil)

	client := &Client{send: make(chan []byte, 64)}
	h.Subscribe(client, query)
	h.streamMetrics(context.Background())
	collectFrames(t, client)

	h.Unsubscribe(client, query)
	if got := len(client.seen); got != 0 {
		t.Errorf("client still remembers %d queries after unsubscribing", got)
	}

	// Coming back must re-deliver: the card was torn down with the dashboard.
	h.Subscribe(client, query)
	h.streamMetrics(context.Background())
	if got := len(readings(collectFrames(t, client))); got != 1 {
		t.Errorf("returning to a device delivered %d readings, want the current value", got)
	}
}

// TestAMovingValueIsNeverDelayed is the constraint on the backoff.
//
// Asking Prometheus less often is only acceptable if it costs nothing an
// operator can see. A value that is changing must therefore be queried on every
// single round; only one that has demonstrably stopped may be asked about less
// often, and the moment it moves again it returns to full rate.
func TestAMovingValueIsNeverDelayed(t *testing.T) {
	var value float64
	var asked int
	h := NewHub(func(context.Context, string) ([]MetricValue, error) {
		asked++
		value++
		return []MetricValue{sample("1", value, 1700000000)}, nil
	}, nil)

	client := &Client{send: make(chan []byte, 512)}
	h.Subscribe(client, `rate(snmp_ifInOctets{device="core-01"}[4m])`)

	const rounds = 20
	for range rounds {
		h.streamMetrics(context.Background())
	}

	if asked != rounds {
		t.Errorf("a value that changed every round was queried %d times in %d rounds; "+
			"a moving reading must never be asked about less often", asked, rounds)
	}
	if got := len(readings(collectFrames(t, client))); got != rounds {
		t.Errorf("delivered %d readings for %d changes, want every one", got, rounds)
	}
}

// TestASettledValueIsAskedAboutLessOften is the saving itself, and its bound: a
// reading only backs off after it has held still for several rounds, and its
// subscribers are still told it is steady every round so the charts advance.
func TestASettledValueIsAskedAboutLessOften(t *testing.T) {
	var asked int
	h := NewHub(func(context.Context, string) ([]MetricValue, error) {
		asked++
		return []MetricValue{sample("1", 37, 1700000000)}, nil
	}, nil)

	client := &Client{send: make(chan []byte, 512)}
	h.Subscribe(client, `snmp_hrSystemProcesses{device="lab-node-1-bmc"}`)

	const rounds = 30
	for range rounds {
		h.streamMetrics(context.Background())
	}

	if asked >= rounds {
		t.Errorf("a reading that never moved was still queried %d times in %d rounds", asked, rounds)
	}
	// The first SteadyRoundsBeforeBackoff rounds are at full rate, and the rest
	// at half, so well under three quarters of the rounds.
	if asked > rounds*3/4 {
		t.Errorf("queried %d times in %d rounds; the backoff is not taking effect", asked, rounds)
	}

	frames := collectFrames(t, client)
	if got := len(readings(frames)); got != 1 {
		t.Errorf("sent %d readings for a value that never moved, want 1", got)
	}
	// Every round after the first still tells the browser the query is steady,
	// including the rounds it was not queried on -- otherwise the chart would
	// stall on exactly the readings that back off.
	if got := len(heartbeats(frames)); got != rounds-1 {
		t.Errorf("sent %d heartbeats across %d rounds, want one per round after the first: "+
			"a backed-off query must still tick", got, rounds-1)
	}
}

// TestHeartbeatCollapsesToAFlagWhenEverySubscriptionIsSteady is the point of
// the all-steady flag: on the common round where nothing a client watches has
// moved, it is told with one flag rather than a re-listing of every query name
// it already holds. On a dashboard of a couple hundred queries that is the
// query text of the whole page, every round, to say nothing changed.
func TestHeartbeatCollapsesToAFlagWhenEverySubscriptionIsSteady(t *testing.T) {
	h := NewHub(staticQuery([]MetricValue{sample("1", 42, 1700000000)}), nil)
	client := &Client{send: make(chan []byte, 64)}
	client.announce([]string{CapabilityAllSteady}) // this browser can apply an all-steady frame
	h.Subscribe(client, `snmp_ifInOctets{device="core-01"}`)
	h.Subscribe(client, `snmp_ifOutOctets{device="core-01"}`)

	// The first round delivers both readings; drain them.
	h.streamMetrics(context.Background())
	if got := len(readings(collectFrames(t, client))); got != 2 {
		t.Fatalf("first round sent %d readings, want 2: a new subscriber gets each value", got)
	}

	// The second round moves nothing, and every subscription is steady, so a
	// single flag stands in for the whole list.
	h.streamMetrics(context.Background())
	beats := heartbeats(collectFrames(t, client))
	if len(beats) != 1 {
		t.Fatalf("steady round sent %d heartbeats, want one", len(beats))
	}
	if !beats[0].AllSteady {
		t.Error("all_steady not set though both subscriptions were steady")
	}
	if len(beats[0].Queries) != 0 {
		t.Errorf("heartbeat named %v; the all-steady flag re-lists nothing", beats[0].Queries)
	}
	if len(beats[0].Metrics) != 0 {
		t.Errorf("heartbeat carried %d samples; it exists to carry none", len(beats[0].Metrics))
	}
	if beats[0].Timestamp == 0 {
		t.Error("heartbeat carried no timestamp, so a chart cannot know how far to advance")
	}
}

// TestHeartbeatKeepsTheListWhenOnlySomeSubscriptionsAreSteady is the other
// half: when a subset moved, the steady ones must still be named so the client
// advances those and no others. The flag would tell it to advance everything,
// including the query it is about to receive a new value for.
func TestHeartbeatKeepsTheListWhenOnlySomeSubscriptionsAreSteady(t *testing.T) {
	var moved float64
	h := NewHub(func(_ context.Context, q string) ([]MetricValue, error) {
		if strings.Contains(q, "moving") {
			moved++
			return []MetricValue{{
				Metric: map[string]string{"__name__": "snmp_moving"}, Value: moved, Timestamp: 1700000000,
			}}, nil
		}
		return []MetricValue{{
			Metric: map[string]string{"__name__": "snmp_steady"}, Value: 1, Timestamp: 1700000000,
		}}, nil
	}, nil)
	client := &Client{send: make(chan []byte, 64)}
	h.Subscribe(client, "snmp_moving")
	h.Subscribe(client, "snmp_steady")

	h.streamMetrics(context.Background()) // first round: two snapshots
	if got := len(readings(collectFrames(t, client))); got != 2 {
		t.Fatalf("first round sent %d readings, want 2", got)
	}

	h.streamMetrics(context.Background()) // second round: one delta, one steady
	frames := collectFrames(t, client)

	if got := len(readings(frames)); got != 1 {
		t.Fatalf("second round sent %d readings, want 1 (only the query that moved)", got)
	}
	beats := heartbeats(frames)
	if len(beats) != 1 {
		t.Fatalf("second round sent %d heartbeats, want one", len(beats))
	}
	if beats[0].AllSteady {
		t.Error("all_steady set though one subscription moved this round")
	}
	if len(beats[0].Queries) != 1 || beats[0].Queries[0] != "snmp_steady" {
		t.Errorf("heartbeat named %v, want [snmp_steady]", beats[0].Queries)
	}
}

// TestAHeartbeatToAnUnannouncedBrowserStillNamesEveryQuery is the compatibility
// half of the all-steady flag, and the reason the flag is negotiated at all.
//
// A browser that was loaded before this build exists is running a script whose
// unchanged-handler reads message.queries and nothing else. Send it a frame
// that names none of them and it advances none of them: every chart on the page
// holds its last value, with the socket still open and nothing on screen saying
// anything is wrong. In a monitoring tool that reads as calm, which is the one
// failure a monitoring tool must never have. The page cannot be fixed from the
// server -- it is already loaded -- so the server stays conservative until the
// browser says otherwise.
func TestAHeartbeatToAnUnannouncedBrowserStillNamesEveryQuery(t *testing.T) {
	h := NewHub(staticQuery([]MetricValue{sample("1", 42, 1700000000)}), nil)

	// No announce: this is a browser from before the flag existed.
	client := &Client{send: make(chan []byte, 64)}
	h.Subscribe(client, `snmp_ifInOctets{device="core-01"}`)
	h.Subscribe(client, `snmp_ifOutOctets{device="core-01"}`)

	h.streamMetrics(context.Background())
	if got := len(readings(collectFrames(t, client))); got != 2 {
		t.Fatalf("first round sent %d readings, want 2", got)
	}

	h.streamMetrics(context.Background())
	beats := heartbeats(collectFrames(t, client))
	if len(beats) != 1 {
		t.Fatalf("steady round sent %d heartbeats, want one", len(beats))
	}
	if beats[0].AllSteady {
		t.Error("all_steady was set for a browser that never said it could apply it")
	}
	if len(beats[0].Queries) != 2 {
		t.Errorf("heartbeat named %v; an unannounced browser needs every query listed, "+
			"because naming none of them stops all of its charts", beats[0].Queries)
	}
}

// TestAnnouncingIsCumulativeAcrossSubscribes guards the reconnect path. A page
// re-subscribes one dashboard at a time, and only the frames it builds itself
// carry the capability list; anything that ever sent a bare subscribe would
// otherwise retract the announcement and silently drop the browser back to the
// full-list frames it had already outgrown.
func TestAnnouncingIsCumulativeAcrossSubscribes(t *testing.T) {
	client := &Client{send: make(chan []byte, 64)}

	client.announce([]string{CapabilityAllSteady})
	if !client.supportsAllSteady.Load() {
		t.Fatal("announcing the capability did not record it")
	}

	client.announce(nil)
	client.announce([]string{})
	client.announce([]string{"something-else-entirely"})
	if !client.supportsAllSteady.Load() {
		t.Error("a later subscribe without a capability list retracted the announcement")
	}
}
