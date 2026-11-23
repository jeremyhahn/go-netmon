package api

import (
	"strconv"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// The polling defaults the web UI assumes when it was not told the poller's.
//
// They are the same numbers internal/config applies to a configuration that
// omits them, so a web UI started without its polling settings wired reports
// what the poller would actually do rather than a number of its own invention.
// TestPollingDefaultsMatchConfigDefaults fails the build if the two drift.
const (
	DefaultPollInterval = 60
	DefaultSNMPTimeout  = 10
	DefaultSNMPRetries  = 3
)

// PollingConfig is the poller's global timing, as the web UI needs to know it.
//
// The web UI does not poll anything. It needs these three numbers for two jobs
// it cannot do without them: telling an operator what interval a device that
// inherits the default will actually be polled at, and warning when an interval
// is short enough that a failing device cannot finish inside it.
//
// The zero value is usable and answers with the defaults above.
type PollingConfig struct {
	// DefaultInterval is global.default_poll_interval, in seconds. It is what a
	// device with no interval of its own is polled at.
	DefaultInterval int

	// SNMPTimeout is global.snmp_timeout, in seconds: the deadline for one SNMP
	// attempt against a device.
	SNMPTimeout int

	// SNMPRetries is global.snmp_retries: how many further attempts a poll makes
	// after its first.
	SNMPRetries int
}

// withDefaults fills in whatever the caller left at zero.
func (p PollingConfig) withDefaults() PollingConfig {
	if p.DefaultInterval <= 0 {
		p.DefaultInterval = DefaultPollInterval
	}
	if p.SNMPTimeout <= 0 {
		p.SNMPTimeout = DefaultSNMPTimeout
	}
	// A zero retry count falls back rather than meaning "one attempt", because
	// that is what internal/config does with snmp_retries: 0, and the number
	// here has to be the number the poller is using.
	if p.SNMPRetries <= 0 {
		p.SNMPRetries = DefaultSNMPRetries
	}
	return p
}

// Effective returns the interval in seconds a device will actually be polled
// at: its own if it has one, the global default if it does not.
//
// This is a report, not a decision. The number the poller acts on is resolved
// by the poller, from the same rule against its own configuration, which is why
// a device that inherits is pushed to the poller as inheriting rather than as
// the number resolved here. Resolving it at push time would freeze today's
// default into every device and make a later change to
// global.default_poll_interval silently ineffective.
func (p PollingConfig) Effective(interval *int) int {
	if interval != nil && *interval > 0 {
		return *interval
	}
	return p.withDefaults().DefaultInterval
}

// SNMPBudget is the longest a single poll of a device can take before the SNMP
// layer gives up on it: one attempt's timeout for every attempt it makes.
//
// gosnmp makes retries+1 attempts, so with the shipped snmp_timeout of 10 and
// snmp_retries of 3 this is 40 seconds against a device that answers nothing.
func (p PollingConfig) SNMPBudget() time.Duration {
	c := p.withDefaults()
	return time.Duration(c.SNMPTimeout*(c.SNMPRetries+1)) * time.Second
}

// Warning describes what is wrong with an otherwise valid interval, or returns
// "" when there is nothing to say.
//
// An interval shorter than SNMPBudget cannot be met by a device that has gone
// silent: the poll is still waiting on SNMP when the next one is due. That is
// worth saying, and it is deliberately not worth refusing.
//
// It is not refused because it is only self-defeating while the device is
// already broken. A healthy device answers in milliseconds, so a 10s interval
// against a 40s worst case is an ordinary and useful configuration, and the
// poller widens the interval on consecutive failures, which bounds the
// degenerate case on its own. Refusing it would also make the product's own
// shipped configuration unrepresentable: config.yaml sets a 30s default poll
// interval against a 40s SNMP budget. A rule that rejects the defaults is a
// rule about the wrong thing, so this is a warning the operator sees at the
// moment they make the trade, not an error.
func (p PollingConfig) Warning(interval *int) string {
	if interval == nil || *interval <= 0 {
		return ""
	}

	budget := p.SNMPBudget()
	if time.Duration(*interval)*time.Second >= budget {
		return ""
	}

	c := p.withDefaults()
	return "poll interval " + (time.Duration(*interval) * time.Second).String() +
		" is shorter than this device's SNMP timeout budget of " + budget.String() +
		" (snmp_timeout " + strconv.Itoa(c.SNMPTimeout) + "s x " +
		strconv.Itoa(c.SNMPRetries+1) + " attempts); a device that stops answering " +
		"cannot finish a poll within it and will be polled on the poller's backoff instead"
}

// polling returns the server's polling settings, usable even on a Server that
// was built without them.
func (s *Server) polling() PollingConfig {
	return s.pollingConfig.withDefaults()
}

// DeviceView is a device as the management API reports it.
//
// It is the stored record plus what only the server can work out: the interval
// the device is really polled at, and anything questionable about the interval
// it was given. Both are derived on every response rather than stored, so they
// cannot go stale against a changed global default.
//
// The stored record is embedded, so every field the API has ever returned for a
// device is still there, at the same name and the same level. The additions are
// read-only; a client that sends them back is ignored.
type DeviceView struct {
	storage.Device

	// EffectivePollInterval is the interval in seconds this device is polled
	// at. It equals poll_interval when the device has one and the global
	// default when poll_interval is absent, which is how a client renders
	// "default (60s)" without knowing the server's configuration.
	EffectivePollInterval int `json:"effective_poll_interval"`

	// Enabled is whether the device is polled, always stated. It shadows the
	// stored field, which is absent on a device that never said and so could
	// not be rendered without a client knowing what absent means.
	Enabled bool `json:"enabled"`

	// Warnings are configurations the server accepted but an operator should
	// know about. It is absent when there are none.
	Warnings []string `json:"warnings,omitempty"`
}

// newDeviceView describes one device under the given polling settings.
func newDeviceView(device storage.Device, polling PollingConfig) DeviceView {
	view := DeviceView{
		Device:                device,
		EffectivePollInterval: polling.Effective(device.PollInterval),
		Enabled:               storage.EnabledOf(&device),
	}
	if warning := polling.Warning(device.PollInterval); warning != "" {
		view.Warnings = []string{warning}
	}
	return view
}

// newDeviceViews describes a list of devices, preserving order.
func newDeviceViews(devices []storage.Device, polling PollingConfig) []DeviceView {
	views := make([]DeviceView, 0, len(devices))
	for _, device := range devices {
		views = append(views, newDeviceView(device, polling))
	}
	return views
}
