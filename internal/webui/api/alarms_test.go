package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/webui/alarm"
)

// alarmServer builds a server whose evaluator answers from a fixture.
func alarmServer(t *testing.T, samples map[string][]alarm.Sample, rules []alarm.Rule) *Server {
	t.Helper()

	s := newTestServer(t)
	s.alarms = alarm.New(func(_ context.Context, expr string) ([]alarm.Sample, error) {
		return samples[expr], nil
	}, rules, nil)
	return s
}

// TestTheBellReportsWhatIsFiring is the endpoint the bell reads.
func TestTheBellReportsWhatIsFiring(t *testing.T) {
	s := alarmServer(t,
		map[string][]alarm.Sample{
			"unreachable": {{Labels: map[string]string{"device": "core-02"}, Value: 1}},
		},
		[]alarm.Rule{
			{Name: "DeviceUnreachable", Expr: "unreachable", Severity: alarm.SeverityCritical,
				Summary: "A device has stopped answering SNMP"},
			{Name: "Quiet", Expr: "nothing", Severity: alarm.SeverityWarning},
		})
	s.alarms.EvaluateOnce(context.Background())

	rec := do(t, s, http.MethodGet, "/api/alarms", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}

	var got AlarmsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding the bell: %v (body %q)", err, rec.Body.String())
	}

	if got.Count != 1 || len(got.Alarms) != 1 {
		t.Fatalf("count = %d with %d alarms, want 1: %+v", got.Count, len(got.Alarms), got.Alarms)
	}
	if got.Alarms[0].Device != "core-02" {
		t.Errorf("device = %q, want core-02 lifted out of the labels", got.Alarms[0].Device)
	}
	if got.Alarms[0].Summary == "" {
		t.Error("the alarm carries no summary, so the bell would show a name and nothing else")
	}
	if got.Alarms[0].Since.IsZero() {
		t.Error("the alarm carries no since, so nothing says how long it has been up")
	}
	// Both rules are being watched even though one is quiet. A reader has to
	// be able to tell "nothing is wrong" from "nothing is being watched".
	if got.Configured != 2 {
		t.Errorf("configured = %d, want 2", got.Configured)
	}
}

// TestAQuietFleetIsAnEmptyListAndNotANull is what the browser iterates.
func TestAQuietFleetIsAnEmptyListAndNotANull(t *testing.T) {
	s := alarmServer(t, nil, []alarm.Rule{
		{Name: "Quiet", Expr: "nothing", Severity: alarm.SeverityCritical},
	})
	s.alarms.EvaluateOnce(context.Background())

	rec := do(t, s, http.MethodGet, "/api/alarms", "")

	if body := rec.Body.String(); !strings.Contains(body, `"alarms":[]`) {
		t.Errorf("body = %q, want an empty array rather than null", body)
	}
}

// TestReloadingReplacesTheRunningRules is hot reload from the server's side.
func TestReloadingReplacesTheRunningRules(t *testing.T) {
	s := alarmServer(t,
		map[string][]alarm.Sample{
			"old": {{Labels: map[string]string{"device": "a"}, Value: 1}},
			"new": {{Labels: map[string]string{"device": "b"}, Value: 1}},
		},
		[]alarm.Rule{{Name: "Old", Expr: "old", Severity: alarm.SeverityWarning}})
	s.alarms.EvaluateOnce(context.Background())
	if len(s.alarms.Firing()) != 1 {
		t.Fatal("the original rule did not fire")
	}

	s.ReloadAlarms([]alarm.Rule{{Name: "New", Expr: "new", Severity: alarm.SeverityCritical}})
	s.alarms.EvaluateOnce(context.Background())

	firing := s.alarms.Firing()
	if len(firing) != 1 || firing[0].Rule != "New" {
		t.Errorf("firing = %+v, want only the reloaded rule", firing)
	}
}

// TestAServerWithNoEvaluatorDoesNotClaimAllClear covers the build that has no
// bell at all -- a Server with no Prometheus behind it.
func TestAServerWithNoEvaluatorDoesNotClaimAllClear(t *testing.T) {
	s := newTestServer(t)

	rec := do(t, s, http.MethodGet, "/api/alarms", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var got AlarmsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if got.Configured != 0 {
		t.Errorf("configured = %d, want 0: there is no evaluator, so nothing is "+
			"being watched", got.Configured)
	}
}

// TestAlarmSeveritiesSurviveTheWire pins the strings the browser switches on.
func TestAlarmSeveritiesSurviveTheWire(t *testing.T) {
	for _, severity := range []alarm.Severity{
		alarm.SeverityCritical, alarm.SeverityWarning, alarm.SeverityInfo,
	} {
		encoded, err := json.Marshal(alarm.Alarm{Severity: severity, Since: time.Now()})
		if err != nil {
			t.Fatalf("encoding %s: %v", severity, err)
		}
		if !strings.Contains(string(encoded), `"severity":"`+string(severity)+`"`) {
			t.Errorf("%s did not survive encoding: %s", severity, encoded)
		}
		if !severity.Valid() {
			t.Errorf("%s is not accepted by its own validator", severity)
		}
	}
}
