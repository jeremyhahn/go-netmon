package api

import (
	"strconv"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// Polling defaults matching internal/config; TestPollingDefaultsMatchConfigDefaults keeps them equal.
const (
	DefaultPollInterval = 60
	DefaultSNMPTimeout  = 10
	DefaultSNMPRetries  = 3
)

// PollingConfig is the poller's global timing as the web UI needs it; the zero value uses the defaults.
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

// Effective returns the device's interval in seconds, or the global default; for reporting only.
func (p PollingConfig) Effective(interval *int) int {
	if interval != nil && *interval > 0 {
		return *interval
	}
	return p.withDefaults().DefaultInterval
}

// SNMPBudget is the longest one poll can take: timeout x (retries+1).
func (p PollingConfig) SNMPBudget() time.Duration {
	c := p.withDefaults()
	return time.Duration(c.SNMPTimeout*(c.SNMPRetries+1)) * time.Second
}

// Warning describes an interval shorter than SNMPBudget, or returns ""; such intervals are allowed.
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

// DeviceView is a stored device plus derived, read-only effective interval and warning.
type DeviceView struct {
	storage.Device

	// EffectivePollInterval is the interval in seconds this device is polled at.
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
