// Package alarm evaluates the conditions an operator wants to be told about.
//
// The dashboard shows what a device is doing. An alarm is the other question --
// is anything wrong right now -- and until this existed the answer was 72
// transient toast notifications, each of which said something once and then
// vanished. A reader who was not looking at the screen at that moment had no
// way to find out.
//
// A rule is a PromQL expression with a name and a severity. It fires when the
// expression returns anything, which is Prometheus's own definition and means a
// rule is written the same way it would be written in a Prometheus rules file.
// One rule can fire many times at once -- once per series the expression
// returns -- because "interface Gi0/0/2 is down" and "interface Gi0/0/5 is
// down" are two things to fix.
//
// Nothing here knows what SNMP is, and nothing here knows what a MIB is. That
// is deliberate: the metrics an operator wants to alarm on include the ones the
// poller derives and the ones no MIB describes, and a rule that had to name a
// MIB object could not reach them.
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

// Severity orders alarms by how much they want the reader's attention.
//
// It is a string on the wire because an operator writes it in a config file,
// and ordered here because a bell showing thirty alarms has to put the worst
// one at the top.
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

	// For is how long the expression must return a series before the alarm is
	// raised. It is what separates a condition from a blip: an interface that
	// bounces for one poll is not something to wake anyone for. Zero means
	// fire on the first evaluation.
	For time.Duration
}

// Alarm is one firing instance of a rule.
//
// A rule that returns forty series produces forty alarms, and they are separate
// because they are separately actionable. Identity is the rule plus the series'
// labels, so an alarm that stays up keeps the same ID and therefore the same
// Since across evaluations -- which is what lets a reader see how long
// something has been broken.
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

// Snapshot is one evaluation's whole answer.
//
// It carries what could NOT be found out as well as what could, because those
// are different states and a list of alarms alone cannot tell them apart. A
// bell showing nothing because nothing is wrong and a bell showing nothing
// because no rule could be evaluated look identical, and only one of them
// means the fleet is healthy.
type Snapshot struct {
	// Alarms are what is firing, worst first.
	Alarms []Alarm

	// Unevaluated is how many rules could not be asked this round. Their last
	// known alarms are carried in Alarms rather than dropped, so a Prometheus
	// that stops answering does not clear the board.
	Unevaluated int
}

// Sample is one series a rule's expression returned.
//
// It is the shape the Prometheus client already produces, restated here so this
// package depends on nothing: an evaluator that imported the API layer could
// not be tested without one.
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

// alarmID identifies one firing series of one rule, stably.
//
// Stability is the whole requirement: the same condition has to produce the
// same ID on every evaluation or its Since resets every fifteen seconds and
// nothing can be said about how long anything has been broken. Labels are
// sorted, and both the name and the value are length-prefixed, so no label
// value can be arranged to collide with another series' identity.
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
