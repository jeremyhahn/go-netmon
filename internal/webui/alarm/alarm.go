// Package alarm evaluates PromQL alarm rules; a rule fires once per series its expression returns.
package alarm

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DeviceLabel is the label a series carries the device's name in. It mirrors
// promname.DeviceLabel and is restated so this package stays free of the
// collector's own types.
const DeviceLabel = "device"

// Severity orders alarms by urgency; it is a string in config files.
type Severity string

// The severities a rule may declare.
const (
	SeverityCritical Severity = "critical"
	SeverityWarning  Severity = "warning"
	SeverityInfo     Severity = "info"
)

// rank orders severities, worst first. An unrecognized severity sorts last
// rather than being rejected: a rule that fires is still worth showing, and
// refusing to display it is a worse answer than displaying it low down.
func (s Severity) rank() int {
	switch s {
	case SeverityCritical:
		return 0
	case SeverityWarning:
		return 1
	case SeverityInfo:
		return 2
	default:
		return 3
	}
}

// Valid reports whether this is a severity the operator may write.
func (s Severity) Valid() bool {
	return s == SeverityCritical || s == SeverityWarning || s == SeverityInfo
}

// Rule is one condition an operator wants to be told about.
type Rule struct {
	// Name identifies the rule and is what the reader sees first.
	Name string

	// Expr is PromQL. It fires for every series it returns, so a rule about
	// interfaces fires once per interface rather than once per device.
	Expr string

	// Severity orders the alarm against the others.
	Severity Severity

	// Summary is one line saying what is wrong. Description is the longer
	// text, shown when the reader opens the alarm.
	Summary     string
	Description string

	// For is how long the expression must return a series before the alarm is raised; zero fires at once.
	For time.Duration
}

// Alarm is one firing series of a rule, identified by rule and labels so Since survives evaluations.
type Alarm struct {
	ID       string   `json:"id"`
	Rule     string   `json:"rule"`
	Severity Severity `json:"severity"`
	Summary  string   `json:"summary,omitempty"`

	// Description is the rule's longer text.
	Description string `json:"description,omitempty"`

	// Device is the device the alarm is about, when the series names one. It
	// is lifted out of the labels because it is the first thing a reader
	// wants and the last thing they should have to hunt for.
	Device string `json:"device,omitempty"`

	// Labels are the firing series' labels, so a reader can tell which of a
	// rule's forty alarms this one is.
	Labels map[string]string `json:"labels,omitempty"`

	// Value is what the expression returned.
	Value float64 `json:"value"`

	// Since is when this alarm was first raised, not when it was last seen.
	Since time.Time `json:"since"`
}

// Snapshot is one evaluation's result, including how many rules could not be evaluated.
type Snapshot struct {
	// Alarms are what is firing, worst first.
	Alarms []Alarm

	// Unevaluated is how many rules could not be asked this round. Their last
	// known alarms are carried in Alarms rather than dropped, so a Prometheus
	// that stops answering does not clear the board.
	Unevaluated int
}

// Sample is one series a rule's expression returned.
type Sample struct {
	Labels map[string]string
	Value  float64
}

// QueryFunc runs one PromQL expression.
type QueryFunc func(ctx context.Context, expr string) ([]Sample, error)

// newAlarm describes one firing series of one rule.
func newAlarm(rule Rule, sample Sample) Alarm {
	return Alarm{
		ID:          alarmID(rule.Name, sample.Labels),
		Rule:        rule.Name,
		Severity:    rule.Severity,
		Summary:     rule.Summary,
		Description: rule.Description,
		Device:      sample.Labels[DeviceLabel],
		Labels:      sample.Labels,
		Value:       sample.Value,
	}
}

// alarmID identifies one firing series of one rule stably, from sorted, length-prefixed labels.
func alarmID(rule string, labels map[string]string) string {
	names := make([]string, 0, len(labels))
	for name := range labels {
		names = append(names, name)
	}
	sort.Strings(names)

	var id strings.Builder
	id.WriteString(rule)
	for _, name := range names {
		id.WriteByte(0)
		id.WriteString(strconv.Itoa(len(name)))
		id.WriteByte(':')
		id.WriteString(name)
		id.WriteByte('=')
		id.WriteString(strconv.Itoa(len(labels[name])))
		id.WriteByte(':')
		id.WriteString(labels[name])
	}
	return id.String()
}

// sortAlarms puts the worst first, and makes the order stable within a
// severity so a list that has not changed does not appear to shuffle.
func sortAlarms(alarms []Alarm) {
	sort.Slice(alarms, func(i, j int) bool {
		a, b := alarms[i], alarms[j]
		if a.Severity.rank() != b.Severity.rank() {
			return a.Severity.rank() < b.Severity.rank()
		}
		// Longest-standing first within a severity: an alarm that has been up
		// for an hour is more likely the cause than one raised a moment ago.
		if !a.Since.Equal(b.Since) {
			return a.Since.Before(b.Since)
		}
		return a.ID < b.ID
	})
}
