package config

import (
	"strconv"
	"time"
)

// Config represents the main application configuration
type Config struct {
	Global     GlobalConfig     `yaml:"global"`
	Prometheus PrometheusConfig `yaml:"prometheus"`
	API        APIConfig        `yaml:"api"`
	Metrics    MetricsConfig    `yaml:"metrics"`
	MIB        MIBConfig        `yaml:"mib"`
	WebUI      WebUIConfig      `yaml:"webui"`
	Dashboard  DashboardConfig  `yaml:"dashboard"`
	Devices    []Device         `yaml:"devices"`
	MIBModules []MIBModule      `yaml:"mib_modules"`
	Logging    LoggingConfig    `yaml:"logging"`

	// Alarms are evaluated by the web UI and ignored by the poller; both read this file.
	Alarms []AlarmRule `yaml:"alarms"`
}

// AlarmRule is one alarm condition, written as a PromQL expression.
type AlarmRule struct {
	// Name identifies the rule and is what the reader sees first.
	Name string `yaml:"name"`

	// Expr fires for every series it returns, so a rule about interfaces
	// fires once per interface rather than once per device.
	Expr string `yaml:"expr"`

	// Severity is one of critical, warning, info.
	Severity string `yaml:"severity"`

	// Summary is one line saying what is wrong; Description is the longer
	// text shown when the reader opens the alarm.
	Summary     string `yaml:"summary"`
	Description string `yaml:"description"`

	// For is how long the expression must keep returning a series before the
	// alarm is raised, as a duration ("5m"). It is what separates a condition
	// from a blip. Empty means raise it on the first evaluation.
	For string `yaml:"for"`
}

// WebUIConfig contains settings for the web UI process. The poller reads this
// file too and ignores this section, the same way the web UI ignores the
// poller's devices.
type WebUIConfig struct {
	// StreamInterval is how often, in seconds, the web UI pushes metric updates to every connected browser.
	StreamInterval int `yaml:"stream_interval"`

	// ListenAddress is where the web UI binds; PrometheusURL and PollerURL are
	// the upstreams it queries. All three are also settable by environment
	// (NETMON_WEBUI_*) and flag.
	ListenAddress string `yaml:"listen_address"`
	PrometheusURL string `yaml:"prometheus_url"`
	PollerURL     string `yaml:"poller_url"`
}

// DashboardConfig configures dashboard generation (mib-importer).
type DashboardConfig struct {
	// PrometheusURL is the Prometheus the generator queries to check that a
	// metric exists before it gets a panel.
	PrometheusURL string `yaml:"prometheus_url"`
}

// GlobalConfig contains global application settings
type GlobalConfig struct {
	DefaultPollInterval int `yaml:"default_poll_interval"` // seconds
	MaxWorkers          int `yaml:"max_workers"`
	SNMPTimeout         int `yaml:"snmp_timeout"` // seconds
	SNMPRetries         int `yaml:"snmp_retries"`
}

// PrometheusConfig contains Prometheus remote write settings
type PrometheusConfig struct {
	RemoteWriteURL string `yaml:"remote_write_url"`
	BatchSize      int    `yaml:"batch_size"`
	FlushInterval  int    `yaml:"flush_interval"` // seconds

	// EmitMetricCatalog designates this collector as the fleet's single catalog publisher; defaults to false.
	EmitMetricCatalog *bool `yaml:"emit_metric_catalog"`
}

// CatalogEmissionEnabled reports whether this collector publishes the metric catalog; unset means no.
func (c PrometheusConfig) CatalogEmissionEnabled() bool {
	return c.EmitMetricCatalog != nil && *c.EmitMetricCatalog
}

// APIConfig contains API server settings; unknown keys are ignored.
type APIConfig struct {
	ListenAddress string `yaml:"listen_address"`
}

// MetricsConfig contains metrics endpoint settings
type MetricsConfig struct {
	ListenAddress string `yaml:"listen_address"`
}

// MIBConfig contains MIB management settings
type MIBConfig struct {
	Paths       []string `yaml:"paths"`        // MIB file search paths
	StoragePath string   `yaml:"storage_path"` // PebbleDB storage path
	AutoLoad    []string `yaml:"auto_load"`    // Modules to load at startup
}

// Device represents an SNMP device to monitor
type Device struct {
	Name         string            `yaml:"name"`
	Address      string            `yaml:"address"`
	SNMPVersion  string            `yaml:"snmp_version"` // "2c" or "3"
	Community    string            `yaml:"community"`    // for SNMPv2c
	Username     string            `yaml:"username"`     // for SNMPv3
	AuthProtocol string            `yaml:"auth_protocol"`
	AuthPassword string            `yaml:"auth_password"`
	PrivProtocol string            `yaml:"priv_protocol"`
	PrivPassword string            `yaml:"priv_password"`
	PollInterval int               `yaml:"poll_interval"` // seconds, 0 = use global default
	Profile      string            `yaml:"profile"`       // MIB profile: "default", "cisco", "interfaces"
	Modules      []string          `yaml:"modules"`       // MIB modules to poll for this device
	Enabled      bool              `yaml:"enabled"`
	Tags         map[string]string `yaml:"tags,omitempty"`

	// ModuleFilters narrows what is polled within a module, keyed by module
	// name. A module listed in Modules with no entry here is polled in full.
	ModuleFilters map[string]ModuleFilter `yaml:"module_filters,omitempty"`
}

// ModuleFilter narrows one MIB module's polled objects by OID or name regex; excludes win; also the control API wire format.
type ModuleFilter struct {
	IncludeOIDs  []string `yaml:"include_oids,omitempty" json:"include_oids,omitempty"`
	ExcludeOIDs  []string `yaml:"exclude_oids,omitempty" json:"exclude_oids,omitempty"`
	IncludeNames []string `yaml:"include_names,omitempty" json:"include_names,omitempty"`
	ExcludeNames []string `yaml:"exclude_names,omitempty" json:"exclude_names,omitempty"`
	MaxOIDs      int      `yaml:"max_oids,omitempty" json:"max_oids,omitempty"`
}

// MIBModule represents a MIB module configuration
type MIBModule struct {
	Name    string   `yaml:"name"`
	Enabled bool     `yaml:"enabled"`
	OIDs    []string `yaml:"oids"`
}

// LoggingConfig contains logging settings
type LoggingConfig struct {
	Level  string `yaml:"level"`  // debug, info, warn, error
	Format string `yaml:"format"` // json, text
	Output string `yaml:"output"` // stdout, file

	// File names the log file and is required when Output is "file". There is
	// no default: a log that lands somewhere the operator did not choose is
	// worse than a startup failure naming the key to set.
	File string `yaml:"file,omitempty"`
}

// Validate validates the configuration
func (c *Config) Validate() error {
	// Global validation
	if c.Global.DefaultPollInterval <= 0 {
		return &ValidationError{Field: "global.default_poll_interval", Message: "must be greater than 0"}
	}
	if c.Global.MaxWorkers <= 0 {
		return &ValidationError{Field: "global.max_workers", Message: "must be greater than 0"}
	}
	if c.Global.SNMPTimeout <= 0 {
		return &ValidationError{Field: "global.snmp_timeout", Message: "must be greater than 0"}
	}
	if c.Global.SNMPRetries < 0 {
		return &ValidationError{Field: "global.snmp_retries", Message: "must be non-negative"}
	}

	// Prometheus validation
	if c.Prometheus.RemoteWriteURL == "" {
		return &ValidationError{Field: "prometheus.remote_write_url", Message: "cannot be empty"}
	}
	if c.Prometheus.BatchSize <= 0 {
		return &ValidationError{Field: "prometheus.batch_size", Message: "must be greater than 0"}
	}
	if c.Prometheus.FlushInterval <= 0 {
		return &ValidationError{Field: "prometheus.flush_interval", Message: "must be greater than 0"}
	}

	// API validation
	if c.API.ListenAddress == "" {
		return &ValidationError{Field: "api.listen_address", Message: "cannot be empty"}
	}

	// Metrics validation
	if c.Metrics.ListenAddress == "" {
		return &ValidationError{Field: "metrics.listen_address", Message: "cannot be empty"}
	}

	// Devices validation - allow empty device list for API-based management
	for i, device := range c.Devices {
		if err := device.Validate(); err != nil {
			return &ValidationError{
				Field:   "devices[" + device.Name + "]",
				Message: err.Error(),
			}
		}
		// Check for duplicate names
		for j := i + 1; j < len(c.Devices); j++ {
			if c.Devices[j].Name == device.Name {
				return &ValidationError{
					Field:   "devices",
					Message: "duplicate device name: " + device.Name,
				}
			}
		}
	}

	// Logging validation
	validLogLevels := map[string]bool{"debug": true, "info": true, "warn": true, "error": true}
	if !validLogLevels[c.Logging.Level] {
		return &ValidationError{Field: "logging.level", Message: "must be one of: debug, info, warn, error"}
	}

	validLogFormats := map[string]bool{"json": true, "text": true}
	if !validLogFormats[c.Logging.Format] {
		return &ValidationError{Field: "logging.format", Message: "must be one of: json, text"}
	}

	validLogOutputs := map[string]bool{"stdout": true, "file": true}
	if !validLogOutputs[c.Logging.Output] {
		return &ValidationError{Field: "logging.output", Message: "must be one of: stdout, file"}
	}

	if c.Logging.Output == "file" && c.Logging.File == "" {
		return &ValidationError{Field: "logging.file", Message: "required when logging.output is \"file\""}
	}

	return validateAlarms(c.Alarms)
}

// validateAlarms rejects a rule without a name or expression or with an unknown severity; PromQL is not parsed here.
func validateAlarms(rules []AlarmRule) error {
	seen := make(map[string]bool, len(rules))

	for i, rule := range rules {
		field := "alarms[" + strconv.Itoa(i) + "]"

		if rule.Name == "" {
			return &ValidationError{Field: field + ".name", Message: "cannot be empty"}
		}
		if seen[rule.Name] {
			return &ValidationError{
				Field:   field + ".name",
				Message: "duplicate alarm name: " + rule.Name,
			}
		}
		seen[rule.Name] = true

		if rule.Expr == "" {
			return &ValidationError{
				Field:   field + ".expr",
				Message: "cannot be empty: a rule with no expression can never fire",
			}
		}
		if !validAlarmSeverities[rule.Severity] {
			return &ValidationError{
				Field:   field + ".severity",
				Message: "must be one of: critical, warning, info",
			}
		}
		if rule.For != "" {
			if _, err := time.ParseDuration(rule.For); err != nil {
				return &ValidationError{
					Field:   field + ".for",
					Message: "is not a duration: " + err.Error(),
				}
			}
		}
	}
	return nil
}

// validAlarmSeverities are the severities an operator may write. They mirror
// internal/webui/alarm, which cannot be imported here: the poller reads this
// package and has no business linking the web UI's alarm engine.
var validAlarmSeverities = map[string]bool{
	"critical": true, "warning": true, "info": true,
}

// Validate validates a device configuration
func (d *Device) Validate() error {
	return d.validate(d.Community != "")
}

// ValidateSealed validates a device whose credentials have been sealed and
// removed from it (see snmp.SealCredentials), so that its empty Community is
// not mistaken for a missing one. hasCommunity reports whether one was sealed.
func (d *Device) ValidateSealed(hasCommunity bool) error {
	return d.validate(hasCommunity)
}

func (d *Device) validate(hasCommunity bool) error {
	if d.Name == "" {
		return &ValidationError{Field: "name", Message: "cannot be empty"}
	}
	if d.Address == "" {
		return &ValidationError{Field: "address", Message: "cannot be empty"}
	}

	// Validate SNMP version. v1 is here because plenty of embedded equipment
	// offers it and some offers nothing else.
	if d.SNMPVersion != "1" && d.SNMPVersion != "2c" && d.SNMPVersion != "3" {
		return &ValidationError{Field: "snmp_version", Message: "must be '1', '2c' or '3'"}
	}

	// Validate version-specific fields. v1 and v2c both authenticate with a
	// community string and neither has anything else to check.
	if (d.SNMPVersion == "1" || d.SNMPVersion == "2c") && !hasCommunity {
		return &ValidationError{
			Field:   "community",
			Message: "required for SNMPv1 and SNMPv2c",
		}
	}

	if d.SNMPVersion == "3" && d.Username == "" {
		return &ValidationError{Field: "username", Message: "required for SNMPv3"}
	}

	if d.PollInterval < 0 {
		return &ValidationError{Field: "poll_interval", Message: "must be non-negative"}
	}

	return nil
}

// GetPollInterval returns the effective poll interval for a device
func (d *Device) GetPollInterval(defaultInterval int) time.Duration {
	interval := d.PollInterval
	if interval == 0 {
		interval = defaultInterval
	}
	return time.Duration(interval) * time.Second
}

// ValidationError represents a configuration validation error
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return "config validation error: " + e.Field + ": " + e.Message
}
