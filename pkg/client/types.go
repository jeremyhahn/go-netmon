// Package client is a Go SDK for the go-netmon management API.
//
// It speaks the same HTTP API the web UI uses, so anything the GUI can do is
// reachable from here — that is what keeps the CLI and the GUI in parity
// rather than letting them drift into two half-featured surfaces.
package client

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"time"
)

// Device is a monitored SNMP device.
type Device struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Address     string    `json:"address"`
	SNMPVersion string    `json:"snmp_version"`
	Community   string    `json:"community,omitempty"`
	Port        int       `json:"port"`
	CreatedAt   time.Time `json:"created_at"`

	// SNMPv3 credentials. Empty on a v1 or v2c device, which authenticates with
	// Community instead. A v3 device without a Username is refused by the agent
	// with "unknown username".
	Username     string `json:"username,omitempty"`
	AuthProtocol string `json:"auth_protocol,omitempty"`
	AuthPassword string `json:"auth_password,omitempty"`
	PrivProtocol string `json:"priv_protocol,omitempty"`
	PrivPassword string `json:"priv_password,omitempty"`

	// PollInterval is how often this device is polled, in seconds. It is nil on
	// a device that inherits the server's global default, which is the state
	// every device starts in.
	//
	// On a write, nil means "inherit"; use PollIntervalOf and InheritPollInterval
	// rather than taking the address of a literal, so that a caller reading a
	// device, changing one field and writing it back cannot accidentally pin the
	// interval to today's default.
	PollInterval *int `json:"poll_interval,omitempty"`

	// EffectivePollInterval is the interval in seconds the device is actually
	// polled at: PollInterval when it has one, the server's global default when
	// it does not. It is computed by the server on every read and ignored on a
	// write.
	EffectivePollInterval int `json:"effective_poll_interval,omitempty"`

	// Warnings are configurations the server accepted but flagged, such as a
	// poll interval shorter than the SNMP timeout budget. It is computed by the
	// server on every read and ignored on a write.
	Warnings []string `json:"warnings,omitempty"`
}

// PollIntervalOf returns a device's own poll interval in seconds, and whether
// it has one. A device that inherits the global default reports false, which is
// the distinction a caller has to draw before printing a number.
func PollIntervalOf(d Device) (int, bool) {
	if d.PollInterval == nil {
		return 0, false
	}
	return *d.PollInterval, true
}

// SetPollInterval gives a device its own poll interval, in seconds.
func SetPollInterval(d *Device, seconds int) {
	d.PollInterval = &seconds
}

// InheritPollInterval clears a device's own poll interval so that it follows
// the server's global default again, including later changes to it.
func InheritPollInterval(d *Device) {
	d.PollInterval = nil
}

// Profile is a named bundle of MIBs that can be assigned to many devices.
type Profile struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

// MIB is an imported MIB module.
type MIB struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	FilePath    string    `json:"file_path"`
	Description string    `json:"description"`
	ObjectCount int       `json:"object_count"`
	Tags        []string  `json:"tags,omitempty"`
	ImportedAt  time.Time `json:"imported_at"`

	// RefreshedAt is when the module's parsed metadata was last replaced in
	// place by a re-import. It is absent on a module that has only ever been
	// imported once.
	RefreshedAt *time.Time `json:"refreshed_at,omitempty"`
}

// MIBImportOptions controls how a MIB file is uploaded.
type MIBImportOptions struct {
	// Tags are applied to a module the server has not seen before. A module
	// that is being refreshed keeps the tags it already carries, which are the
	// operator's, not the file's; use SetMIBTags to change them.
	Tags []string

	// Replace refreshes a module that is already imported instead of refusing
	// the upload.
	Replace bool
}

// MIBImportResult reports what the server did with an uploaded MIB file.
type MIBImportResult struct {
	MIB MIB `json:"mib"`

	// Replaced reports that an already-imported module was refreshed in place
	// rather than a new one created.
	Replaced bool   `json:"replaced"`
	Message  string `json:"message"`
}

// MIBObject is a single object (OID) within a MIB module.
type MIBObject struct {
	OID         string `json:"oid"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Unit        string `json:"unit,omitempty"`
	Pollable    bool   `json:"pollable"`

	// Presentation metadata. These let a caller render a raw SNMP value as
	// text without a second round trip for the MIB definition.
	Access      string           `json:"access,omitempty"`
	Status      string           `json:"status,omitempty"`
	DisplayHint string           `json:"display_hint,omitempty"`
	EnumValues  map[int64]string `json:"enum_values,omitempty"`
	IsScalar    bool             `json:"is_scalar,omitempty"`
	IsColumn    bool             `json:"is_column,omitempty"`
	IsTable     bool             `json:"is_table,omitempty"`
	IsIndex     bool             `json:"is_index,omitempty"`
	TableOID    string           `json:"table_oid,omitempty"`

	// Index names the columns of a conceptual row's INDEX clause, in INDEX
	// order, and is present only on a table entry.
	//
	// It is what identifies a series' genuine index labels. The exporter labels
	// every sample of a table column with the lowercased name of each INDEX
	// column, and with enrichment labels beside them — ifname, ifdescr and the
	// rest — that are not part of the row's identity. Holding the INDEX clause
	// is how a client tells the two apart without a list of names to ignore.
	Index []string `json:"index,omitempty"`
}

// MIBDetail is a MIB module together with its objects.
type MIBDetail struct {
	ID          int64       `json:"id"`
	Name        string      `json:"name"`
	FilePath    string      `json:"file_path"`
	Description string      `json:"description"`
	ObjectCount int         `json:"object_count"`
	ImportedAt  time.Time   `json:"imported_at"`
	Objects     []MIBObject `json:"objects"`

	// Page describes which slice of the module's objects Objects carries. It is
	// absent from an unnarrowed request, which carries all of them. ObjectCount
	// above always counts the module.
	Page *Page `json:"page,omitempty"`
}

// Table is a conceptual SNMP table discovered in a MIB module.
type Table struct {
	TableOID    string      `json:"table_oid"`
	TableName   string      `json:"table_name"`
	EntryOID    string      `json:"entry_oid"`
	EntryName   string      `json:"entry_name"`
	IndexColumn string      `json:"index_column,omitempty"`
	Columns     []MIBObject `json:"columns"`
}

// MIBAssignment describes how one MIB is polled for the device or profile it is
// assigned to. It mirrors the server's stored assignment exactly.
type MIBAssignment struct {
	// Enabled reports whether this MIB is polled at all. A disabled assignment
	// stays attached, so pausing a MIB does not lose its filters.
	Enabled bool `json:"enabled"`

	// Include and exclude patterns are regular expressions applied to an
	// object's OID or name. Excludes win over includes. Empty include lists
	// mean "everything that is pollable".
	IncludeOIDs  []string `json:"include_oids,omitempty"`
	ExcludeOIDs  []string `json:"exclude_oids,omitempty"`
	IncludeNames []string `json:"include_names,omitempty"`
	ExcludeNames []string `json:"exclude_names,omitempty"`

	// MaxOIDs caps how many objects this assignment contributes, 0 for no cap.
	MaxOIDs int `json:"max_oids,omitempty"`
}

// Validate reports the first filter the server would reject.
//
// Patterns are compiled in the caller's process so a malformed one is named
// where it was typed instead of coming back as a 400 several layers away, and
// so it never reaches a poller worker.
func (a MIBAssignment) Validate() error {
	groups := []struct {
		field    string
		patterns []string
	}{
		{"include_oids", a.IncludeOIDs},
		{"exclude_oids", a.ExcludeOIDs},
		{"include_names", a.IncludeNames},
		{"exclude_names", a.ExcludeNames},
	}

	for _, group := range groups {
		for _, pattern := range group.patterns {
			if _, err := regexp.Compile(pattern); err != nil {
				return &PatternError{Field: group.field, Pattern: pattern, Err: err}
			}
		}
	}

	if a.MaxOIDs < 0 {
		return &ConfigError{Field: "max_oids", Reason: "must not be negative"}
	}
	return nil
}

// AssignedMIB is a MIB together with how it is polled.
//
// The server flattens the MIB's own fields into the object rather than nesting
// them, so the MIB is embedded here: a listing reports every module's polling
// state without a request per module.
type AssignedMIB struct {
	MIB
	Assignment MIBAssignment `json:"assignment"`
}

// MIBSeriesEstimate answers the question that has to be asked before a MIB is
// enabled on a device: how many Prometheus series will it add?
//
// Object count is the wrong number, because every pollable column costs one
// series per table row. Series is the number that decides whether a device can
// carry the MIB.
type MIBSeriesEstimate struct {
	MIB         string `json:"mib"`
	Device      string `json:"device"`
	Series      int    `json:"series"`
	Scalars     int    `json:"scalars"`
	Columns     int    `json:"columns"`
	Instances   int    `json:"instances"`   // instances assumed per table
	Exact       bool   `json:"exact"`       // true when counted from Prometheus
	Explanation string `json:"explanation"` // one sentence a human can act on
}

// MIBObjectPlan answers, for one device and one MIB, which of the module's
// objects the device is actually polling and why not the rest.
//
// It is computed on the server by the same code the poller runs, so a client
// never has to reproduce the rule about tables, columns, access and filters.
type MIBObjectPlan struct {
	DeviceID int64  `json:"device_id"`
	Device   string `json:"device"`
	MIBID    int64  `json:"mib_id"`
	MIB      string `json:"mib"`

	// Assignment is the assignment the poller acts on, and Source names where
	// it came from: "device", "profile" or "default".
	Assignment MIBAssignment `json:"assignment"`
	Source     string        `json:"source"`

	// Total is how many objects the module defines; Polled how many of them
	// this device fetches.
	Total  int `json:"total"`
	Polled int `json:"polled"`

	// Estimate prices the whole module, and is what the cost endpoint returns.
	Estimate *MIBSeriesEstimate `json:"estimate"`

	// Objects are the module's objects, in the order the poller sees them. A
	// max_oids cap is spent in that order, so re-sorting this list changes
	// which objects look capped.
	Objects []MIBObjectDecision `json:"objects"`

	// Page describes which slice of the plan Objects carries, and is absent
	// from an unnarrowed request. Total and Polled above always count the whole
	// module.
	Page *Page `json:"page,omitempty"`
}

// MIBObjectDecision is one object with the verdict on it. The MIB's own fields
// are flattened in; Pollable reports what the MIB says about the object, while
// Polled reports what this device does with it.
type MIBObjectDecision struct {
	MIBObject

	// Kind is how the poller would fetch it: scalar, column, table or entry.
	Kind string `json:"kind"`

	// Polled reports whether this device fetches the object right now.
	Polled bool `json:"polled"`

	// Reason is "polled", or what stopped it: not-accessible, not-current,
	// covered-by-table-walk, excluded-by-filter, not-included-by-filter,
	// capped-by-max-oids or module-disabled.
	Reason string `json:"reason"`

	// Series is what the object costs in Prometheus series, 0 when unpolled.
	Series int `json:"series"`
}

// DeviceMIBs separates a device's directly assigned MIBs from the full set it
// inherits through its profiles.
//
// The direct entries carry the device's own assignment, which is what a write
// to that MIB's assignment edits; the combined entries carry the effective one,
// which is what the poller acts on.
type DeviceMIBs struct {
	DirectMIBs []AssignedMIB `json:"direct_mibs"`
	AllMIBs    []AssignedMIB `json:"all_mibs"`
}

// SNMPTestRequest asks the server to probe a device's SNMP agent.
type SNMPTestRequest struct {
	Address     string `json:"address"`
	SNMPVersion string `json:"snmp_version"`
	Community   string `json:"community,omitempty"`
	Port        int    `json:"port,omitempty"`
	Timeout     int    `json:"timeout,omitempty"`

	// SNMPv3. Testing an ad-hoc v3 address is how credentials are checked
	// before they are stored anywhere.
	Username     string `json:"username,omitempty"`
	AuthProtocol string `json:"auth_protocol,omitempty"`
	AuthPassword string `json:"auth_password,omitempty"`
	PrivProtocol string `json:"priv_protocol,omitempty"`
	PrivPassword string `json:"priv_password,omitempty"`
}

// SNMPTestResult is one OID returned by a connectivity test.
type SNMPTestResult struct {
	OID   string `json:"oid"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

// SNMPTestResponse is the outcome of a connectivity test.
//
// The server answers with this same shape whether the probe succeeded or the
// device never replied, so a failure is a result to render rather than a
// transport error to guess at.
type SNMPTestResponse struct {
	Success  bool   `json:"success"`
	Message  string `json:"message"`
	Device   string `json:"device,omitempty"`
	Address  string `json:"address,omitempty"`
	Duration string `json:"duration"`

	// TimedOut says the device stayed silent for the whole probe budget;
	// Rejected says the server refused the request before probing anything.
	TimedOut bool `json:"timed_out,omitempty"`
	Rejected bool `json:"rejected,omitempty"`

	Results     []SNMPTestResult `json:"results,omitempty"`
	Error       string           `json:"error,omitempty"`
	SysDescr    string           `json:"sys_descr,omitempty"`
	SysName     string           `json:"sys_name,omitempty"`
	SysUpTime   string           `json:"sys_uptime,omitempty"`
	SysContact  string           `json:"sys_contact,omitempty"`
	SysLocation string           `json:"sys_location,omitempty"`
}

// DeviceStatus reports what the poller knows about a device.
//
// Address is not part of the poller's response; callers that display it fill
// it in from the device record.
type DeviceStatus struct {
	Name             string            `json:"name"`
	Connected        bool              `json:"connected"`
	LastPollTime     time.Time         `json:"last_poll_time"`
	LastPollSuccess  bool              `json:"last_poll_success"`
	LastError        string            `json:"last_error,omitempty"`
	MetricsCollected int               `json:"metrics_collected"`
	PollInterval     string            `json:"poll_interval"`
	Tags             map[string]string `json:"tags,omitempty"`

	// Unreachable is set while the poller is getting no SNMP answers at all
	// from the device and is retrying it on a widening interval. It is not the
	// same as a failed poll: a device that answers some objects and errors on
	// others fails polls while staying perfectly reachable.
	Unreachable bool `json:"unreachable"`

	// ConsecutiveFailures counts the polls in a row the device answered no part
	// of, and is what NextPollTime is backed off by.
	ConsecutiveFailures int `json:"consecutive_failures,omitempty"`

	// NextPollTime is when the poller will next try the device.
	//
	// ABSENT from the JSON means no retry is scheduled yet -- the state a device
	// is in for the whole of its first poll. Treat a missing next_poll_time as
	// "not yet known", not as an error. See MarshalJSON.
	NextPollTime time.Time `json:"next_poll_time,omitempty"`

	// Address is populated by the client from the device record so status
	// tables can show it without a second lookup per row.
	Address string `json:"address,omitempty"`
}

// MarshalJSON omits next_poll_time while no retry has been scheduled.
//
// The `omitempty` tag cannot do this by itself: encoding/json treats only
// false, 0, nil pointers and interfaces, and empty arrays, slices, maps and
// strings as empty, and time.Time is a struct, so a zero value would marshal as
// "0001-01-01T00:00:00Z". This mirrors internal/api.DeviceStatus.MarshalJSON --
// the two shapes are one wire contract and must not drift.
//
// LastPollTime keeps its always-present zero deliberately: that is what
// distinguishes "never polled" from "polled and failed".
func (s DeviceStatus) MarshalJSON() ([]byte, error) {
	// The local type sheds the method set so this does not recurse; the
	// shadowing pointer field at depth 0 wins over the embedded one at depth 1.
	type alias DeviceStatus
	out := struct {
		alias
		NextPollTime *time.Time `json:"next_poll_time,omitempty"`
	}{alias: alias(s)}

	if !s.NextPollTime.IsZero() {
		out.NextPollTime = &s.NextPollTime
	}

	return json.Marshal(out)
}

// MetricSample is one value in a time series.
type MetricSample struct {
	Timestamp time.Time
	Value     float64
}

// TimeSeries is a labeled series of samples.
type TimeSeries struct {
	Metric string            `json:"metric"`
	Labels map[string]string `json:"labels,omitempty"`
	Values []MetricSample    `json:"values"`
}

// wireTimeSeries mirrors the server's range-query response, which encodes each
// sample as a two-element [timestamp, value] array in the Prometheus style
// rather than as an object.
type wireTimeSeries struct {
	Metric string            `json:"metric"`
	Labels map[string]string `json:"labels"`
	Values [][]any           `json:"values"`
}

// toTimeSeries decodes the positional sample arrays into typed samples,
// skipping any entry that is not a well-formed [timestamp, value] pair.
func (w wireTimeSeries) toTimeSeries() TimeSeries {
	series := TimeSeries{
		Metric: w.Metric,
		Labels: w.Labels,
		Values: make([]MetricSample, 0, len(w.Values)),
	}
	for _, pair := range w.Values {
		if len(pair) != 2 {
			continue
		}
		ts, tsOK := toFloat(pair[0])
		value, valueOK := toFloat(pair[1])
		if !tsOK || !valueOK {
			continue
		}
		series.Values = append(series.Values, MetricSample{
			Timestamp: unixSeconds(ts),
			Value:     value,
		})
	}
	return series
}

// InstantValue is a single current value for a query.
type InstantValue struct {
	Metric map[string]string `json:"metric"`
	Value  float64           `json:"value"`
	Time   time.Time         `json:"-"`
}

// wireInstantValue mirrors the server's instant-query element, which reports
// the timestamp as a Unix value rather than a formatted time.
type wireInstantValue struct {
	Metric    map[string]string `json:"metric"`
	Value     float64           `json:"value"`
	Timestamp int64             `json:"timestamp"`
}

// toInstantValue converts a wire element to its typed form.
func (w wireInstantValue) toInstantValue() InstantValue {
	return InstantValue{
		Metric: w.Metric,
		Value:  w.Value,
		Time:   unixSeconds(float64(w.Timestamp)),
	}
}

// SystemInfo is the system-group snapshot the server holds for a device.
type SystemInfo struct {
	Device    string            `json:"device"`
	Timestamp int64             `json:"timestamp"`
	Values    map[string]string `json:"values"`
}

// unixSeconds converts a Unix timestamp to a time.Time. The server emits
// seconds in some responses and milliseconds in others, so values large enough
// to be milliseconds are scaled down.
func unixSeconds(ts float64) time.Time {
	const millisecondThreshold = 1e11
	if ts > millisecondThreshold {
		return time.UnixMilli(int64(ts))
	}
	sec, frac := math.Modf(ts)
	return time.Unix(int64(sec), int64(frac*float64(time.Second)))
}

// toFloat converts a decoded JSON number, which may arrive as a float64 or as
// a string, to a float64.
func toFloat(v any) (float64, bool) {
	switch value := v.(type) {
	case float64:
		return value, true
	case int64:
		return float64(value), true
	case json.Number:
		f, err := value.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(value, 64)
		return f, err == nil
	default:
		return 0, false
	}
}

// PollerConfig is the configuration the poller pulls from the management API.
type PollerConfig struct {
	Devices    []PollerDevice `json:"devices"`
	MIBModules []PollerMIB    `json:"mib_modules"`
	Version    int64          `json:"version"`
}

// PollerDevice is one device entry within PollerConfig.
type PollerDevice struct {
	Name         string            `json:"name"`
	Address      string            `json:"address"`
	SNMPVersion  string            `json:"snmp_version"`
	Community    string            `json:"community,omitempty"`
	Username     string            `json:"username,omitempty"`
	PollInterval int               `json:"poll_interval"`
	Profile      string            `json:"profile"`
	Modules      []string          `json:"modules,omitempty"`
	Enabled      bool              `json:"enabled"`
	Tags         map[string]string `json:"tags,omitempty"`
}

// PollerMIB is one MIB module the poller is told to load.
type PollerMIB struct {
	Name    string   `json:"name"`
	Enabled bool     `json:"enabled"`
	OIDs    []string `json:"oids,omitempty"`
}
