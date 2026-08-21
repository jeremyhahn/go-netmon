package config

import "time"

// Config represents the main application configuration
type Config struct {
	Global     GlobalConfig     `yaml:"global"`
	Prometheus PrometheusConfig `yaml:"prometheus"`
	API        APIConfig        `yaml:"api"`
	Metrics    MetricsConfig    `yaml:"metrics"`
	MIB        MIBConfig        `yaml:"mib"`
	Devices    []Device         `yaml:"devices"`
	MIBModules []MIBModule      `yaml:"mib_modules"`
	Logging    LoggingConfig    `yaml:"logging"`
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
}

// APIConfig contains API server settings.
//
// It once also carried enable_cors and jwt_secret. Nothing ever read either:
// there is no JWT dependency in the module, no authentication middleware, and
// the CLI sends no credential, so jwt_secret described an authentication layer
// that was never built. A secret-shaped setting nobody reads is worse than an
// absent one — it reads as though the API is protected. Both are removed; see
// issue #3. Config files still carrying the keys load unchanged, because the
// decoder ignores unknown fields.
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

// ModuleFilter narrows the objects polled from one MIB module for one device.
//
// Patterns are regular expressions matched against an object's OID or name.
// Excludes are applied before includes, so an object matching both is excluded.
// It carries JSON tags as well as YAML because it is both a configuration key
// and the wire format the web UI pushes to the poller's control API. One type
// for both keeps a filter meaning the same thing wherever it is written down.
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

	return nil
}

// Validate validates a device configuration
func (d *Device) Validate() error {
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
	if (d.SNMPVersion == "1" || d.SNMPVersion == "2c") && d.Community == "" {
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
