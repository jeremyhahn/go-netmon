// Package config loads and validates the go-netmon configuration file.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

const (
	// DefaultConfigPath is the default configuration file path
	DefaultConfigPath = "config.yaml"

	// ConfigPathEnvVar is the environment variable for config path
	ConfigPathEnvVar = "CONFIG_PATH"
)

// Load loads configuration from file
func Load() (*Config, error) {
	return LoadWithOverrides(nil)
}

// LoadWithOverrides loads configuration from the configured file, with the
// flags the operator set taking precedence (flags > env > file > defaults).
func LoadWithOverrides(o Overrides) (*Config, error) {
	return LoadFromFileWithOverrides(getConfigPath(), o)
}

// LoadFromFile loads configuration from a specific file
func LoadFromFile(path string) (*Config, error) {
	return LoadFromFileWithOverrides(path, nil)
}

// LoadFromFileWithOverrides loads configuration from a specific file; the bind
// and target addresses resolve flags > env (NETMON_*) > file > defaults.
func LoadFromFileWithOverrides(path string, o Overrides) (*Config, error) {
	// path is the configuration file the operator named; reading it is the point.
	data, err := os.ReadFile(path) // #nosec G304 -- operator-supplied config path
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	// Set defaults
	setDefaults(&config)

	// Resolve the addresses through the env and flag layers
	if err := ResolveAddresses(&config, path, o); err != nil {
		return nil, err
	}

	// Validate configuration
	if err := config.Validate(); err != nil {
		return nil, err
	}

	return &config, nil
}

// getConfigPath returns the configuration file path
// Priority: ENV var > default path
func getConfigPath() string {
	if path := os.Getenv(ConfigPathEnvVar); path != "" {
		return path
	}
	return DefaultConfigPath
}

// ApplyDefaults fills in every optional field a caller left unset.
//
// Load applies these itself, so this exists for callers that build a Config in
// code — notably the fallback path taken when no config file is present. A
// Config assembled as a struct literal has zero values in every section the
// literal does not mention, and those are not all usable: logging.output ""
// is not a valid output, so a service that skipped this would fail to start
// rather than fall back.
func ApplyDefaults(config *Config) {
	setDefaults(config)
}

// setDefaults sets default values for optional configuration fields
func setDefaults(config *Config) {
	// Global defaults
	if config.Global.DefaultPollInterval == 0 {
		config.Global.DefaultPollInterval = 60
	}
	if config.Global.MaxWorkers == 0 {
		config.Global.MaxWorkers = 10
	}
	if config.Global.SNMPTimeout == 0 {
		config.Global.SNMPTimeout = 10
	}
	if config.Global.SNMPRetries == 0 {
		config.Global.SNMPRetries = 3
	}

	// Web UI defaults. Five seconds is what the hub ticked at when the value
	// was a literal, so a configuration that omits the key behaves as before.
	if config.WebUI.StreamInterval == 0 {
		config.WebUI.StreamInterval = 5
	}

	// Prometheus defaults
	if config.Prometheus.BatchSize == 0 {
		config.Prometheus.BatchSize = 1000
	}
	if config.Prometheus.FlushInterval == 0 {
		config.Prometheus.FlushInterval = 10
	}

	// API defaults
	if config.API.ListenAddress == "" {
		config.API.ListenAddress = DefaultAPIListen
	}
	if config.Metrics.ListenAddress == "" {
		config.Metrics.ListenAddress = DefaultMetricsListen
	}
	if config.WebUI.ListenAddress == "" {
		config.WebUI.ListenAddress = DefaultWebUIListen
	}
	if config.WebUI.PrometheusURL == "" {
		config.WebUI.PrometheusURL = DefaultWebUIPrometheus
	}
	if config.WebUI.PollerURL == "" {
		config.WebUI.PollerURL = DefaultWebUIPoller
	}
	if config.Dashboard.PrometheusURL == "" {
		config.Dashboard.PrometheusURL = DefaultDashboardPromURL
	}

	// MIB defaults
	if config.MIB.StoragePath == "" {
		config.MIB.StoragePath = "./data/mib-storage"
	}
	if len(config.MIB.Paths) == 0 {
		config.MIB.Paths = []string{"./mibs/standard"}
	}

	// Device defaults
	for i := range config.Devices {
		if config.Devices[i].Profile == "" {
			config.Devices[i].Profile = "default"
		}
	}

	// Logging defaults
	if config.Logging.Level == "" {
		config.Logging.Level = "info"
	}
	if config.Logging.Format == "" {
		config.Logging.Format = "json"
	}
	if config.Logging.Output == "" {
		config.Logging.Output = "stdout"
	}
}

// GetEnabledDevices returns only enabled devices
func (c *Config) GetEnabledDevices() []Device {
	enabled := make([]Device, 0, len(c.Devices))
	for _, device := range c.Devices {
		if device.Enabled {
			enabled = append(enabled, device)
		}
	}
	return enabled
}

// GetEnabledMIBModules returns only enabled MIB modules
func (c *Config) GetEnabledMIBModules() []MIBModule {
	enabled := make([]MIBModule, 0, len(c.MIBModules))
	for _, module := range c.MIBModules {
		if module.Enabled {
			enabled = append(enabled, module)
		}
	}
	return enabled
}

// GetDeviceByName returns a device by its name
func (c *Config) GetDeviceByName(name string) (*Device, bool) {
	for i := range c.Devices {
		if c.Devices[i].Name == name {
			return &c.Devices[i], true
		}
	}
	return nil, false
}
