package api

import (
	"context"
	"net/http"

	"github.com/jeremyhahn/go-netmon/internal/webui/alarm"
)

// The alarm bell API.

// AlarmsResponse is what the bell reads.
type AlarmsResponse struct {
	// Alarms are what is firing, worst first.
	Alarms []alarm.Alarm `json:"alarms"`

	// Count is len(Alarms), carried so the badge can be drawn without walking
	// the list.
	Count int `json:"count"`

	// Configured is how many rules are loaded.
	Configured int `json:"configured"`

	// Unevaluated is how many rules could not be evaluated last round; their last alarms are still listed.
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
func (s *Server) ReloadAlarms(rules []alarm.Rule) {
	if s.alarms == nil {
		return
	}
	s.alarms.Reload(rules)
}
