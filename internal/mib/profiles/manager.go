// Package profiles groups MIB modules into reusable device profiles.
package profiles

import (
	"fmt"
	"sync"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/mib/selector"
	"github.com/jeremyhahn/go-netmon/internal/mib/storage"
)

// MIBProfile represents a collection of MIB modules and OID selection rules
type MIBProfile struct {
	Name          string            `json:"name" yaml:"name"`
	Description   string            `json:"description" yaml:"description"`
	Modules       []string          `json:"modules" yaml:"modules"`
	IncludeOIDs   []string          `json:"include_oids,omitempty" yaml:"include_oids,omitempty"`
	ExcludeOIDs   []string          `json:"exclude_oids,omitempty" yaml:"exclude_oids,omitempty"`
	IncludeNames  []string          `json:"include_names,omitempty" yaml:"include_names,omitempty"`
	ExcludeNames  []string          `json:"exclude_names,omitempty" yaml:"exclude_names,omitempty"`
	IncludeTables bool              `json:"include_tables" yaml:"include_tables"`
	MaxOIDs       int               `json:"max_oids" yaml:"max_oids"`
	IsBuiltIn     bool              `json:"is_builtin" yaml:"is_builtin"`
	CreatedAt     time.Time         `json:"created_at" yaml:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at" yaml:"updated_at"`
	Tags          map[string]string `json:"tags,omitempty" yaml:"tags,omitempty"`
}

// Manager manages MIB profiles (simplified in-memory version)
type Manager struct {
	storage  *storage.Storage
	selector *selector.Selector
	profiles map[string]*MIBProfile
	mu       sync.RWMutex
}

// NewManager creates a new profile manager
func NewManager(store *storage.Storage) *Manager {
	return &Manager{
		storage:  store,
		selector: selector.New(store),
		profiles: make(map[string]*MIBProfile),
	}
}

// LoadBuiltInProfiles loads all built-in profiles
func (m *Manager) LoadBuiltInProfiles() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	profiles := GetBuiltInProfiles()
	for _, profile := range profiles {
		m.profiles[profile.Name] = profile
	}

	return nil
}

// GetProfile retrieves a profile by name
func (m *Manager) GetProfile(name string) (*MIBProfile, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	profile, ok := m.profiles[name]
	if !ok {
		return nil, fmt.Errorf("profile %s not found", name)
	}

	return profile, nil
}

// ListProfiles returns all profiles
func (m *Manager) ListProfiles() []*MIBProfile {
	m.mu.RLock()
	defer m.mu.RUnlock()

	profiles := make([]*MIBProfile, 0, len(m.profiles))
	for _, profile := range m.profiles {
		profiles = append(profiles, profile)
	}

	return profiles
}

// PreviewOIDs shows what OIDs will be selected for a profile
func (m *Manager) PreviewOIDs(profileName string) ([]*mib.OIDMetadata, error) {
	profile, err := m.GetProfile(profileName)
	if err != nil {
		return nil, err
	}

	// Convert profile to selector device profile
	deviceProfile := m.toDeviceProfile(profile)

	// Select OIDs
	return m.selector.SelectOIDs(deviceProfile)
}

// GetStatistics returns statistics about a profile
func (m *Manager) GetStatistics(profileName string) (*selector.SelectionStats, error) {
	profile, err := m.GetProfile(profileName)
	if err != nil {
		return nil, err
	}

	deviceProfile := m.toDeviceProfile(profile)
	return m.selector.GetStatistics(deviceProfile)
}

// SelectOIDsForProfile selects OIDs using a profile
func (m *Manager) SelectOIDsForProfile(profileName string) ([]*mib.OIDMetadata, error) {
	profile, err := m.GetProfile(profileName)
	if err != nil {
		return nil, err
	}

	deviceProfile := m.toDeviceProfile(profile)
	return m.selector.SelectOIDs(deviceProfile)
}

// toDeviceProfile converts a MIBProfile to a selector.DeviceProfile
func (m *Manager) toDeviceProfile(profile *MIBProfile) *selector.DeviceProfile {
	return &selector.DeviceProfile{
		Name:          profile.Name,
		Description:   profile.Description,
		Modules:       profile.Modules,
		IncludeOIDs:   profile.IncludeOIDs,
		ExcludeOIDs:   profile.ExcludeOIDs,
		IncludeNames:  profile.IncludeNames,
		ExcludeNames:  profile.ExcludeNames,
		IncludeTables: profile.IncludeTables,
		MaxOIDs:       profile.MaxOIDs,
	}
}
