package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/webui/websocket"
)

// Finding the rows of a table.
//
// A table widget offers an instance picker, and every card under it stays blank
// until a row is chosen. The rows come from this endpoint, which asked
// Prometheus only for info metrics -- the ones carrying a text value.
//
// A table with no text column therefore had no rows to offer. Its picker came
// up empty, nothing subscribed, and every card under it sat on "Waiting for
// data…" while the poller was collecting the whole time. cpmCPUTotalTable is
// entirely numeric and this is precisely what happened to it; measured in a
// browser across five devices, 507 cards were in this state.

// recordingQuery captures the PromQL a handler issues and answers with canned
// series, so a test can assert on what was asked as well as what came back.
type recordingQuery struct {
	mu      sync.Mutex
	asked   []string
	answers map[string][]websocket.MetricValue
}

func (q *recordingQuery) fn() func(context.Context, string) ([]websocket.MetricValue, error) {
	return func(_ context.Context, query string) ([]websocket.MetricValue, error) {
		q.mu.Lock()
		defer q.mu.Unlock()
		q.asked = append(q.asked, query)
		for match, answer := range q.answers {
			if strings.Contains(query, match) {
				return answer, nil
			}
		}
		return nil, nil
	}
}

func (q *recordingQuery) queries() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]string(nil), q.asked...)
}

// instances runs one label_values request and returns the rows it found.
func instances(t *testing.T, s *Server, query string) []map[string]string {
	t.Helper()

	rec := do(t, s, http.MethodPost, "/api/metrics/instant",
		`{"query":`+quoteJSON(query)+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Metrics []struct {
			Metric map[string]string `json:"metric"`
		} `json:"metrics"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}

	out := make([]map[string]string, 0, len(body.Metrics))
	for _, m := range body.Metrics {
		out = append(out, m.Metric)
	}
	return out
}

func quoteJSON(s string) string {
	encoded, _ := json.Marshal(s)
	return string(encoded)
}

// TestANumericTableStillHasRows is the bug: a table whose columns are all
// numbers must still offer its rows.
func TestANumericTableStillHasRows(t *testing.T) {
	s := newTestServer(t)
	q := &recordingQuery{answers: map[string][]websocket.MetricValue{
		// The aggregate over every series carrying the label.
		"count by (cpmcputotalindex)": {
			{Metric: map[string]string{"cpmcputotalindex": "7"}, Value: 28},
		},
		// No info metrics: this table has no text column at all.
		"_info": nil,
	}}
	s.queryFunc = q.fn()

	rows := instances(t, s, `label_values({device="core-01"}, cpmcputotalindex)`)

	if len(rows) != 1 {
		t.Fatalf("found %d rows, want 1: an all-numeric table used to offer none", len(rows))
	}
	if rows[0]["cpmcputotalindex"] != "7" {
		t.Errorf("row = %v, want the index the device reports", rows[0])
	}

	// And it must not have asked only about info metrics, which is what made
	// the table invisible.
	var askedBroadly bool
	for _, asked := range q.queries() {
		if strings.Contains(asked, "count by (cpmcputotalindex)") {
			askedBroadly = true
		}
	}
	if !askedBroadly {
		t.Errorf("the rows were never asked for across all series; asked: %v", q.queries())
	}
}

// TestFriendlyNamesStillReachTheRow keeps the reason the info query existed. An
// operator picks an interface by name, not by ifIndex 7.
func TestFriendlyNamesStillReachTheRow(t *testing.T) {
	s := newTestServer(t)
	q := &recordingQuery{answers: map[string][]websocket.MetricValue{
		"count by (ifindex)": {
			{Metric: map[string]string{"ifindex": "1"}, Value: 40},
			{Metric: map[string]string{"ifindex": "2"}, Value: 40},
		},
		"_info": {
			{Metric: map[string]string{
				"__name__": "snmp_ifname_info", "ifindex": "1", "value": "GigabitEthernet0/0/0",
			}, Value: 1},
		},
	}}
	s.queryFunc = q.fn()

	rows := instances(t, s, `label_values({device="core-01"}, ifindex)`)

	if len(rows) != 2 {
		t.Fatalf("found %d rows, want both interfaces", len(rows))
	}
	byIndex := map[string]map[string]string{}
	for _, r := range rows {
		byIndex[r["ifindex"]] = r
	}
	if got := byIndex["1"]["ifname"]; got != "GigabitEthernet0/0/0" {
		t.Errorf("interface 1 name = %q, want the value from its info metric", got)
	}
	// The interface with no info metric is still offered, unnamed.
	if _, found := byIndex["2"]; !found {
		t.Error("an interface with no friendly name was dropped from the picker")
	}
}

// TestRowsSurviveTheNamesFailing covers the degraded case. Names are an
// enrichment; losing them must not empty the picker, because an unnamed row
// still selects real data.
func TestRowsSurviveTheNamesFailing(t *testing.T) {
	s := newTestServer(t)
	s.queryFunc = func(_ context.Context, query string) ([]websocket.MetricValue, error) {
		if strings.Contains(query, "_info") {
			return nil, errQueryFailed
		}
		return []websocket.MetricValue{
			{Metric: map[string]string{"ifindex": "3"}, Value: 12},
		}, nil
	}

	rows := instances(t, s, `label_values({device="core-01"}, ifindex)`)

	if len(rows) != 1 || rows[0]["ifindex"] != "3" {
		t.Errorf("rows = %v, want the one the device reports even with names unavailable", rows)
	}
}

// TestATableWithNoRowsReportsNone is the honest empty case: a device that does
// not implement the table has nothing to pick, and saying so is right.
func TestATableWithNoRowsReportsNone(t *testing.T) {
	s := newTestServer(t)
	s.queryFunc = func(context.Context, string) ([]websocket.MetricValue, error) { return nil, nil }

	if rows := instances(t, s, `label_values({device="bastion"}, ifindex)`); len(rows) != 0 {
		t.Errorf("rows = %v, want none", rows)
	}
}

var errQueryFailed = &QueryError{Reason: "prometheus is down"}

// QueryError reports a Prometheus query that could not be run.
type QueryError struct{ Reason string }

func (e *QueryError) Error() string { return "query failed: " + e.Reason }
