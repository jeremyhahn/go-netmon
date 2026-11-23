package poller

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/config"
)

// ConfigSyncClient handles configuration synchronization with WebUI
type ConfigSyncClient struct {
	baseURL    string
	httpClient *http.Client
	timeout    time.Duration
	maxRetries int
}

// ConfigResponse represents the configuration response from WebUI
type ConfigResponse struct {
	Devices    []DeviceConfig `json:"devices"`
	MIBModules []MIBConfig    `json:"mib_modules"`
	Version    int64          `json:"version"`
}

// DeviceConfig represents a device configuration from WebUI
type DeviceConfig struct {
	Name         string            `json:"name"`
	Address      string            `json:"address"`
	SNMPVersion  string            `json:"snmp_version"`
	Community    string            `json:"community,omitempty"`
	Username     string            `json:"username,omitempty"`
	AuthProtocol string            `json:"auth_protocol,omitempty"`
	AuthPassword string            `json:"auth_password,omitempty"`
	PrivProtocol string            `json:"priv_protocol,omitempty"`
	PrivPassword string            `json:"priv_password,omitempty"`
	PollInterval int               `json:"poll_interval"`
	Enabled      bool              `json:"enabled"`
	Profile      string            `json:"profile"`
	Modules      []string          `json:"modules,omitempty"`
	Tags         map[string]string `json:"tags,omitempty"`

	// ModuleFilters narrows what is polled within a module, keyed by module
	// name. A module in Modules with no entry here is polled in full.
	ModuleFilters map[string]ModuleFilterConfig `json:"module_filters,omitempty"`
}

// ModuleFilterConfig is a module filter as WebUI sends it.
type ModuleFilterConfig struct {
	IncludeOIDs  []string `json:"include_oids,omitempty"`
	ExcludeOIDs  []string `json:"exclude_oids,omitempty"`
	IncludeNames []string `json:"include_names,omitempty"`
	ExcludeNames []string `json:"exclude_names,omitempty"`
	MaxOIDs      int      `json:"max_oids,omitempty"`
}

// MIBConfig represents a MIB module configuration
type MIBConfig struct {
	Name    string   `json:"name"`
	Enabled bool     `json:"enabled"`
	OIDs    []string `json:"oids,omitempty"`
}

// NewConfigSyncClient creates a new configuration sync client
func NewConfigSyncClient(baseURL string, timeout time.Duration) *ConfigSyncClient {
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	return &ConfigSyncClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: timeout,
		},
		timeout:    timeout,
		maxRetries: 5,
	}
}

// SyncConfiguration fetches configuration from WebUI with exponential backoff retry
func (c *ConfigSyncClient) SyncConfiguration(ctx context.Context) (*ConfigResponse, error) {
	log.Printf("Starting configuration synchronization with WebUI at %s", c.baseURL)

	var lastErr error

	for attempt := 0; attempt < c.maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 1s, 2s, 4s, 8s, 16s
			backoff := time.Duration(math.Pow(2, float64(attempt))) * time.Second
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}

			log.Printf("Configuration sync attempt %d/%d failed, retrying in %v: %v",
				attempt, c.maxRetries, backoff, lastErr)

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}

		resp, err := c.fetchConfiguration(ctx)
		if err == nil {
			log.Printf("Successfully synchronized configuration: %d devices, %d MIB modules (version: %d)",
				len(resp.Devices), len(resp.MIBModules), resp.Version)
			return resp, nil
		}

		lastErr = err

		// Check if context was canceled
		if ctx.Err() != nil {
			return nil, fmt.Errorf("context canceled during sync: %w", ctx.Err())
		}
	}

	return nil, fmt.Errorf("failed to sync configuration after %d attempts: %w", c.maxRetries, lastErr)
}

// fetchConfiguration performs a single configuration fetch request
func (c *ConfigSyncClient) fetchConfiguration(ctx context.Context) (*ConfigResponse, error) {
	url := fmt.Sprintf("%s/api/poller/config", c.baseURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	// Closing the body only returns the connection to the pool; the call's
	// outcome is already determined by what was read above.
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var configResp ConfigResponse
	if err := json.NewDecoder(resp.Body).Decode(&configResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &configResp, nil
}

// ToConfig converts ConfigResponse to config.Config
func (r *ConfigResponse) ToConfig(baseConfig *config.Config) *config.Config {
	cfg := &config.Config{
		Global:     baseConfig.Global,
		Prometheus: baseConfig.Prometheus,
		API:        baseConfig.API,
		Metrics:    baseConfig.Metrics,
		MIB:        baseConfig.MIB,
		Logging:    baseConfig.Logging,
		Devices:    make([]config.Device, 0, len(r.Devices)),
		MIBModules: make([]config.MIBModule, 0, len(r.MIBModules)),
	}

	// Convert devices
	for _, dev := range r.Devices {
		cfg.Devices = append(cfg.Devices, dev.toConfigDevice())
	}

	// Convert MIB modules
	for _, mib := range r.MIBModules {
		cfg.MIBModules = append(cfg.MIBModules, config.MIBModule{
			Name:    mib.Name,
			Enabled: mib.Enabled,
			OIDs:    mib.OIDs,
		})
	}

	return cfg
}

// toConfigDevice converts a device from the WebUI wire format to the poller's
// own configuration type.
//
// It normalizes the SNMP version, which WebUI states as "v2c"/"v3" and the
// poller as "2c"/"3", and carries the per-module filters across so a device
// that polls part of a MIB keeps doing so.
func (d DeviceConfig) toConfigDevice() config.Device {
	snmpVersion := d.SNMPVersion
	switch snmpVersion {
	case "v2c":
		snmpVersion = "2c"
	case "v3":
		snmpVersion = "3"
	}

	return config.Device{
		Name:          d.Name,
		Address:       d.Address,
		SNMPVersion:   snmpVersion,
		Community:     d.Community,
		Username:      d.Username,
		AuthProtocol:  d.AuthProtocol,
		AuthPassword:  d.AuthPassword,
		PrivProtocol:  d.PrivProtocol,
		PrivPassword:  d.PrivPassword,
		PollInterval:  d.PollInterval,
		Profile:       d.Profile,
		Modules:       d.Modules,
		ModuleFilters: toConfigModuleFilters(d.ModuleFilters),
		Enabled:       d.Enabled,
		Tags:          d.Tags,
	}
}

// toConfigModuleFilters converts the wire filters to the poller's own type,
// returning nil when there are none so an unfiltered device carries no map.
func toConfigModuleFilters(filters map[string]ModuleFilterConfig) map[string]config.ModuleFilter {
	if len(filters) == 0 {
		return nil
	}

	converted := make(map[string]config.ModuleFilter, len(filters))
	for module, f := range filters {
		converted[module] = config.ModuleFilter{
			IncludeOIDs:  f.IncludeOIDs,
			ExcludeOIDs:  f.ExcludeOIDs,
			IncludeNames: f.IncludeNames,
			ExcludeNames: f.ExcludeNames,
			MaxOIDs:      f.MaxOIDs,
		}
	}
	return converted
}

// ApplyConfiguration applies fetched configuration to the poller
func ApplyConfiguration(ctx context.Context, p *Poller, syncedConfig *ConfigResponse) error {
	log.Println("Applying synchronized configuration to poller...")

	// Load the MIB modules WebUI enables globally. A device's own modules are
	// not loaded here: AddDevice below brings in whatever each device needs,
	// which is also the path a device added later takes, so doing it twice
	// would be two implementations of the same rule.
	if mibLoader := p.GetMIBLoader(); mibLoader != nil {
		for _, mibModule := range syncedConfig.MIBModules {
			if !mibModule.Enabled {
				continue
			}
			if err := mibLoader.EnsureModule(mibModule.Name); err != nil {
				log.Printf("Warning: failed to load MIB module %s: %v", mibModule.Name, err)
				// Continue loading other modules
				continue
			}
		}
	}

	enabledCount := 0
	for i := range syncedConfig.Devices {
		device := &syncedConfig.Devices[i]
		if !device.Enabled {
			log.Printf("Skipping disabled device: %s", device.Name)
			continue
		}

		// Convert to config.Device
		converted := device.toConfigDevice()
		configDevice := &converted

		// Validate device
		if err := configDevice.Validate(); err != nil {
			log.Printf("Warning: invalid device configuration for %s: %v", device.Name, err)
			continue
		}

		// Add device to poller
		if err := p.AddDevice(ctx, configDevice); err != nil {
			log.Printf("Warning: failed to add device %s: %v", device.Name, err)
			continue
		}

		enabledCount++
		log.Printf("Added device %s (address: %s, profile: %s)", device.Name, device.Address, device.Profile)
	}

	log.Printf("Successfully applied configuration: %d/%d devices enabled and added",
		enabledCount, len(syncedConfig.Devices))

	return nil
}
