package api

import (
	"context"
	"net/http"

	"github.com/jeremyhahn/go-netmon/internal/webui/alarm"
)

// The bell.
//
// Everything the UI said about a fault used to be a toast: shown once, for a
// few seconds, and then gone. A reader who was not watching the screen at that
// moment had no way to find out afterwards, and there was nowhere on the page
// that answered "is anything wrong right now". This is that place.

// AlarmsResponse is what the bell reads.
type AlarmsResponse struct {
	// Alarms are what is firing, worst first.
	Alarms []alarm.Alarm `json:"alarms"`

	// Count is len(Alarms), carried so the badge can be drawn without walking
	// the list.
	Count int `json:"count"`

	// Configured is how many rules are loaded. Zero is not a fault -- an
	// operator who has written no rules has none -- but it is the difference
	// between "nothing is wrong" and "nothing is being watched", and a bell
	// that cannot tell them apart is a bell that lies.
	Configured int `json:"configured"`

	// Unevaluated is how many rules could not be asked on the last round.
	// Their last known alarms are still in the list, so this is what says the
	// answer is incomplete rather than current -- the difference between a
	// quiet fleet and a Prometheus that has stopped answering.
	Unevaluated int `json:"unevaluated,omitempty"`
}

// alarmSampler adapts the Prometheus client to the evaluator, which is
// deliberately ignorant of it: an evaluator that imported this package could
// not be tested without a Prometheus.
func (s *Server) alarmSampler(ctx context.Context, expr string) ([]alarm.Sample, error) {
	rows, err := s.promClient.Query(ctx, expr)
	if err != nil {
		return nil, err
	}

	samples := make([]alarm.Sample, 0, len(rows))
	for _, row := range rows {
		samples = append(samples, alarm.Sample{Labels: row.Metric, Value: row.Value})
	}
	return samples, nil
}

// handleGetAlarms answers what is firing right now.
func (s *Server) handleGetAlarms(w http.ResponseWriter, _ *http.Request) {
	if s.alarms == nil {
		// No evaluator at all, which is a server built without one rather
		// than a quiet fleet. Answering with an empty list would be
		// indistinguishable from all-clear.
		respondJSON(w, http.StatusOK, AlarmsResponse{Alarms: []alarm.Alarm{}})
		return
	}

	held := s.alarms.Snapshot()
	if held.Alarms == nil {
		held.Alarms = []alarm.Alarm{}
	}
	respondJSON(w, http.StatusOK, AlarmsResponse{
		Alarms:      held.Alarms,
		Count:       len(held.Alarms),
		Configured:  s.alarms.RuleCount(),
		Unevaluated: held.Unevaluated,
	})
}

// ReloadAlarms replaces the alarm rules on a running server.
//
// This is the whole of hot reload from the server's side: the config file is
// re-read by the process that owns it and the rules it yielded arrive here. A
// file that does not parse or does not validate never reaches this call, so a
// bad edit leaves the running rules exactly as they were rather than emptying
// the bell.
func (s *Server) ReloadAlarms(rules []alarm.Rule) {
	if s.alarms == nil {
		return
	}
	s.alarms.Reload(rules)
}
