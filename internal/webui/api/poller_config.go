package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/config"
	mibStorage "github.com/jeremyhahn/go-netmon/internal/mib/storage"
	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// PollerConfigResponse represents the complete configuration for the poller
type PollerConfigResponse struct {
	Devices    []PollerDeviceConfig `json:"devices"`
	MIBModules []PollerMIBConfig    `json:"mib_modules"`
	Version    int64                `json:"version"`
}

// PollerDeviceConfig represents a device with its MIBs and objects for the poller
type PollerDeviceConfig struct {
	Name          string                        `json:"name"`
	Address       string                        `json:"address"`
	SNMPVersion   string                        `json:"snmp_version"`
	Community     string                        `json:"community,omitempty"`
	Username      string                        `json:"username,omitempty"`
	AuthProtocol  string                        `json:"auth_protocol,omitempty"`
	AuthPassword  string                        `json:"auth_password,omitempty"`
	PrivProtocol  string                        `json:"priv_protocol,omitempty"`
	PrivPassword  string                        `json:"priv_password,omitempty"`
	PollInterval  int                           `json:"poll_interval"`
	Enabled       bool                          `json:"enabled"`
	Profile       string                        `json:"profile"`
	Modules       []string                      `json:"modules,omitempty"`
	ModuleFilters map[string]PollerModuleFilter `json:"module_filters,omitempty"`
	Tags          map[string]string             `json:"tags,omitempty"`
}

// PollerModuleFilter narrows the objects the poller reads from one module.
//
// It mirrors the poller's own module filter: patterns are regular expressions
// matched against an object's OID or name, and excludes win over includes.
// It is config.ModuleFilter under another name: the poller reads that type, the
// YAML config declares it, and the control API carries it. Three copies of the
// same five fields is how they drift apart.
type PollerModuleFilter = config.ModuleFilter

// newPollerModuleFilter converts the filtering half of a stored assignment to
// what the poller is told.
func newPollerModuleFilter(a storage.MIBAssignment) PollerModuleFilter {
	return PollerModuleFilter{
		IncludeOIDs:  a.IncludeOIDs,
		ExcludeOIDs:  a.ExcludeOIDs,
		IncludeNames: a.IncludeNames,
		ExcludeNames: a.ExcludeNames,
		MaxOIDs:      a.MaxOIDs,
	}
}

// PollerMIBConfig represents a MIB module configuration
type PollerMIBConfig struct {
	Name    string   `json:"name"`
	Enabled bool     `json:"enabled"`
	OIDs    []string `json:"oids,omitempty"`
}

// DeviceConfig represents a device with its MIBs for legacy compatibility
type DeviceConfig struct {
	ID          int64       `json:"id"`
	Name        string      `json:"name"`
	Address     string      `json:"address"`
	SNMPVersion string      `json:"snmp_version"`
	Community   string      `json:"community,omitempty"`
	Port        int         `json:"port"`
	MIBs        []MIBConfig `json:"mibs"`
}

// MIBConfig represents a MIB with its pollable objects
type MIBConfig struct {
	ID      int64       `json:"id"`
	Name    string      `json:"name"`
	Objects []MIBObject `json:"objects"`
}

// MIBObjectConfig represents a pollable MIB object
type MIBObjectConfig struct {
	Name     string `json:"name"`
	OID      string `json:"oid"`
	Type     string `json:"type"`
	Pollable bool   `json:"pollable"`
}

// handleGetPollerConfig returns the complete poller configuration
func (s *Server) handleGetPollerConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	log.Println("Poller requesting configuration...")

	// Get all devices from storage
	devices, err := s.storage.ListDevices(ctx)
	if err != nil {
		log.Printf("Error listing devices: %v", err)
		http.Error(w, fmt.Sprintf("failed to list devices: %v", err), http.StatusInternalServerError)
		return
	}

	// Build the configuration response
	response := PollerConfigResponse{
		Devices:    make([]PollerDeviceConfig, 0, len(devices)),
		MIBModules: make([]PollerMIBConfig, 0),
		Version:    time.Now().Unix(),
	}

	// Process each device
	for _, device := range devices {
		deviceConfig, buildErr := s.buildPollerDeviceConfig(ctx, &device)
		if buildErr != nil {
			log.Printf("Warning: failed to build config for device %s (ID: %d): %v", device.Name, device.ID, buildErr)
			continue
		}
		response.Devices = append(response.Devices, *deviceConfig)
	}

	// Get all MIBs from storage
	mibs, err := s.storage.ListMIBs(ctx)
	if err != nil {
		log.Printf("Warning: failed to list MIBs: %v", err)
	} else {
		for _, mib := range mibs {
			mibConfig := PollerMIBConfig{
				Name:    mib.Name,
				Enabled: true,
				OIDs:    []string{}, // OIDs come from MIB parser
			}
			response.MIBModules = append(response.MIBModules, mibConfig)
		}
	}

	log.Printf("Serving poller configuration: %d devices, %d MIB modules (version: %d)",
		len(response.Devices), len(response.MIBModules), response.Version)

	respondJSON(w, http.StatusOK, response)
}

// buildPollerDeviceConfig builds one device's poller config: enabled modules only, and 0 for an inherited interval.
func (s *Server) buildPollerDeviceConfig(ctx context.Context, device *storage.Device) (*PollerDeviceConfig, error) {
	selection, err := s.resolveDeviceModules(ctx, device.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve device modules: %w", err)
	}

	// Get MIBs from assigned profiles
	profiles, err := s.storage.GetDeviceProfiles(ctx, device.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to get device profiles: %w", err)
	}

	// Determine profile name
	// If device has directly assigned MIBs (not via profile), use empty profile
	// Otherwise use the first assigned profile name
	profileName := ""
	if len(profiles) > 0 {
		profileName = profiles[0].Name
	}

	// Format address with port
	address := formatDeviceAddress(device.Address, device.Port)

	// Tags become labels on every sample, so only values stable for the device's life belong here.
	tags := make(map[string]string)
	tags["source"] = "webui"

	return &PollerDeviceConfig{
		Name:        device.Name,
		Address:     address,
		SNMPVersion: device.SNMPVersion,
		Community:   device.Community,

		// The poller has read these fields since it was written; nothing ever
		// filled them, because the store had nowhere to keep them. A v3 device
		// therefore reached the agent with no username and was refused.
		Username:     device.Username,
		AuthProtocol: device.AuthProtocol,
		AuthPassword: device.AuthPassword,
		PrivProtocol: device.PrivProtocol,
		PrivPassword: device.PrivPassword,

		PollInterval:  storage.PollIntervalOf(device),
		Enabled:       storage.EnabledOf(device),
		Profile:       profileName,
		Modules:       selection.Modules,
		ModuleFilters: selection.Filters,
		Tags:          tags,
	}, nil
}

// buildMIBConfig constructs the MIB configuration with pollable objects
func (s *Server) buildMIBConfig(ctx context.Context, mib *storage.MIB) (*MIBConfig, error) {
	// Get the MIB storage to access OID metadata
	mibStorageInterface := s.mibLoader.GetStorage()
	mibStore, ok := mibStorageInterface.(*mibStorage.Storage)
	if !ok || mibStore == nil {
		return nil, fmt.Errorf("failed to get MIB storage")
	}

	// Get this module's pollable OIDs. The store answers per module, so this
	// does not deserialize every OID of every other MIB.
	oids, err := mibStore.GetPollableOIDs(mib.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to list OIDs: %w", err)
	}

	objects := make([]MIBObject, 0, len(oids))
	for _, oid := range oids {
		object := newMIBObject(oid)
		object.Pollable = true // Already filtered for pollable
		objects = append(objects, object)
	}

	return &MIBConfig{
		ID:      mib.ID,
		Name:    mib.Name,
		Objects: objects,
	}, nil
}

// formatDeviceAddress formats device address with port
func formatDeviceAddress(address string, port int) string {
	if port == 0 || port == 161 {
		return address
	}
	return fmt.Sprintf("%s:%d", address, port)
}
