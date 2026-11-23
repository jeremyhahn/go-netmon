// Package selector decides which OIDs a device should be polled for, given its
// profile and the MIB metadata available.
package selector

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/mib/storage"
)

// DeviceProfile defines which OIDs to poll for a device type
type DeviceProfile struct {
	Name          string   `yaml:"name"`
	Description   string   `yaml:"description"`
	Modules       []string `yaml:"modules"`        // MIB modules to load (e.g., "IF-MIB", "SNMPv2-MIB")
	IncludeOIDs   []string `yaml:"include_oids"`   // OID patterns to include (regex)
	ExcludeOIDs   []string `yaml:"exclude_oids"`   // OID patterns to exclude (regex)
	IncludeNames  []string `yaml:"include_names"`  // OID names to include (regex)
	ExcludeNames  []string `yaml:"exclude_names"`  // OID names to exclude (regex)
	IncludeTables bool     `yaml:"include_tables"` // Whether to include table OIDs (default: false)
	MaxOIDs       int      `yaml:"max_oids"`       // Maximum number of OIDs to poll (0 = unlimited)
}

// Selector selects OIDs to poll based on device profiles and MIB metadata
type Selector struct {
	storage *storage.Storage
}

// New creates a new OID selector
func New(store *storage.Storage) *Selector {
	return &Selector{
		storage: store,
	}
}

// SelectOIDs selects OIDs to poll for a device based on its profile
func (s *Selector) SelectOIDs(profile *DeviceProfile) ([]*mib.OIDMetadata, error) {
	if profile == nil {
		return nil, fmt.Errorf("profile cannot be nil")
	}

	var allOIDs []*mib.OIDMetadata

	// Collect OIDs from all specified modules
	for _, moduleName := range profile.Modules {
		oids, err := s.storage.GetPollableOIDs(moduleName)
		if err != nil {
			// Log but don't fail - module might not be loaded yet
			continue
		}
		allOIDs = append(allOIDs, oids...)
	}

	// If no modules specified, get all pollable OIDs
	if len(profile.Modules) == 0 {
		oids, err := s.storage.GetPollableOIDs("")
		if err != nil {
			return nil, fmt.Errorf("failed to get pollable OIDs: %w", err)
		}
		allOIDs = oids
	}

	// Apply filters
	filtered := s.applyFilters(allOIDs, profile)

	// Apply limit
	if profile.MaxOIDs > 0 && len(filtered) > profile.MaxOIDs {
		filtered = filtered[:profile.MaxOIDs]
	}

	return filtered, nil
}

// applyFilters applies include/exclude filters to OIDs
func (s *Selector) applyFilters(oids []*mib.OIDMetadata, profile *DeviceProfile) []*mib.OIDMetadata {
	var filtered []*mib.OIDMetadata

	// Compile regex patterns
	includeOIDPatterns := compilePatterns(profile.IncludeOIDs)
	excludeOIDPatterns := compilePatterns(profile.ExcludeOIDs)
	includeNamePatterns := compilePatterns(profile.IncludeNames)
	excludeNamePatterns := compilePatterns(profile.ExcludeNames)

	for _, oid := range oids {
		// Skip tables if not included
		if !profile.IncludeTables && (oid.IsTable || oid.IsTableEntry) {
			continue
		}

		// Check exclude patterns first
		if matchesAny(oid.OID, excludeOIDPatterns) {
			continue
		}
		if matchesAny(oid.Name, excludeNamePatterns) {
			continue
		}

		// Check include patterns
		if len(includeOIDPatterns) > 0 && !matchesAny(oid.OID, includeOIDPatterns) {
			continue
		}
		if len(includeNamePatterns) > 0 && !matchesAny(oid.Name, includeNamePatterns) {
			continue
		}

		filtered = append(filtered, oid)
	}

	return filtered
}

// SelectTableColumns selects specific columns from a table
func (s *Selector) SelectTableColumns(module, tableName string, columns []string) ([]*mib.OIDMetadata, error) {
	// Get table info
	table, err := s.storage.GetTable(module, tableName)
	if err != nil {
		return nil, fmt.Errorf("failed to get table %s::%s: %w", module, tableName, err)
	}

	var selected []*mib.OIDMetadata

	// If no columns specified, return all columns
	if len(columns) == 0 {
		for _, colOID := range table.Columns {
			oid, err := s.storage.GetOID(colOID)
			if err != nil {
				continue
			}
			selected = append(selected, oid)
		}
		return selected, nil
	}

	// Select specified columns
	for _, colName := range columns {
		oid, err := s.storage.GetOIDByName(module, colName)
		if err != nil {
			continue
		}
		selected = append(selected, oid)
	}

	return selected, nil
}

// GetDefaultProfile returns a default profile for standard monitoring
func GetDefaultProfile() *DeviceProfile {
	return &DeviceProfile{
		Name:        "default",
		Description: "Default profile with standard system and interface metrics",
		Modules: []string{
			"SNMPv2-MIB",
			"IF-MIB",
		},
		IncludeTables: false,
		MaxOIDs:       0, // unlimited
	}
}

// GetCiscoProfile returns a profile optimized for Cisco devices
func GetCiscoProfile() *DeviceProfile {
	return &DeviceProfile{
		Name:        "cisco",
		Description: "Cisco device profile with CPU, memory, and interface monitoring",
		Modules: []string{
			"SNMPv2-MIB",
			"IF-MIB",
			"IP-MIB",
			"CISCO-PROCESS-MIB",
			"CISCO-MEMORY-POOL-MIB",
		},
		IncludeTables: false,
		MaxOIDs:       0,
	}
}

// GetInterfaceProfile returns a profile focused on interface monitoring
func GetInterfaceProfile() *DeviceProfile {
	return &DeviceProfile{
		Name:        "interfaces",
		Description: "Interface monitoring profile",
		Modules: []string{
			"IF-MIB",
		},
		IncludeNames: []string{
			"^if.*", // Include all interface metrics
		},
		IncludeTables: false,
		MaxOIDs:       0,
	}
}

// compilePatterns compiles a list of regex patterns
func compilePatterns(patterns []string) []*regexp.Regexp {
	var compiled []*regexp.Regexp
	for _, pattern := range patterns {
		if re, err := regexp.Compile(pattern); err == nil {
			compiled = append(compiled, re)
		}
	}
	return compiled
}

// matchesAny checks if a string matches any of the regex patterns
func matchesAny(s string, patterns []*regexp.Regexp) bool {
	for _, pattern := range patterns {
		if pattern.MatchString(s) {
			return true
		}
	}
	return false
}

// ValidateProfile validates a device profile
func ValidateProfile(profile *DeviceProfile) error {
	if profile == nil {
		return fmt.Errorf("profile cannot be nil")
	}

	if profile.Name == "" {
		return fmt.Errorf("profile name cannot be empty")
	}

	// Validate regex patterns
	for _, pattern := range profile.IncludeOIDs {
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("invalid include OID pattern %q: %w", pattern, err)
		}
	}

	for _, pattern := range profile.ExcludeOIDs {
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("invalid exclude OID pattern %q: %w", pattern, err)
		}
	}

	for _, pattern := range profile.IncludeNames {
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("invalid include name pattern %q: %w", pattern, err)
		}
	}

	for _, pattern := range profile.ExcludeNames {
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("invalid exclude name pattern %q: %w", pattern, err)
		}
	}

	if profile.MaxOIDs < 0 {
		return fmt.Errorf("max_oids cannot be negative")
	}

	return nil
}

// ListAvailableModules lists all modules available in storage
func (s *Selector) ListAvailableModules() ([]*mib.ModuleInfo, error) {
	return s.storage.ListModules()
}

// GetModuleInfo returns information about a specific module
func (s *Selector) GetModuleInfo(moduleName string) (*mib.ModuleInfo, error) {
	return s.storage.GetModule(moduleName)
}

// GetOIDInfo returns detailed information about a specific OID
func (s *Selector) GetOIDInfo(oid string) (*mib.OIDMetadata, error) {
	return s.storage.GetOID(oid)
}

// GetOIDByName returns OID metadata by module and name
func (s *Selector) GetOIDByName(module, name string) (*mib.OIDMetadata, error) {
	return s.storage.GetOIDByName(module, name)
}

// SearchOIDs searches for OIDs matching a pattern
func (s *Selector) SearchOIDs(pattern string) ([]*mib.OIDMetadata, error) {
	// Check if pattern is an OID prefix
	if isOIDPrefix(pattern) {
		return s.storage.ListOIDs(pattern)
	}

	// Otherwise treat as name pattern
	allOIDs, err := s.storage.ListOIDs("")
	if err != nil {
		return nil, err
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid pattern: %w", err)
	}

	var matches []*mib.OIDMetadata
	for _, oid := range allOIDs {
		if re.MatchString(oid.Name) || re.MatchString(oid.Description) {
			matches = append(matches, oid)
		}
	}

	return matches, nil
}

// isOIDPrefix checks if a string looks like an OID prefix
func isOIDPrefix(s string) bool {
	// Simple check: starts with digit and contains only digits and dots
	if s == "" || s[0] < '0' || s[0] > '9' {
		return false
	}
	for _, c := range s {
		if c != '.' && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// GetStatistics returns statistics about selected OIDs
func (s *Selector) GetStatistics(profile *DeviceProfile) (*SelectionStats, error) {
	oids, err := s.SelectOIDs(profile)
	if err != nil {
		return nil, err
	}

	stats := &SelectionStats{
		TotalOIDs: len(oids),
		ByModule:  make(map[string]int),
		ByType:    make(map[string]int),
	}

	for _, oid := range oids {
		stats.ByModule[oid.Module]++
		stats.ByType[oid.Type]++

		switch {
		case oid.IsCounter():
			stats.Counters++
		case oid.IsGauge():
			stats.Gauges++
		case oid.IsString():
			stats.Strings++
		}
	}

	return stats, nil
}

// SelectionStats holds statistics about OID selection
type SelectionStats struct {
	TotalOIDs int
	Counters  int
	Gauges    int
	Strings   int
	ByModule  map[string]int
	ByType    map[string]int
}

// String returns a human-readable representation of statistics
func (s *SelectionStats) String() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Total OIDs: %d\n", s.TotalOIDs)
	fmt.Fprintf(&sb, "  Counters: %d\n", s.Counters)
	fmt.Fprintf(&sb, "  Gauges: %d\n", s.Gauges)
	fmt.Fprintf(&sb, "  Strings: %d\n", s.Strings)
	fmt.Fprintf(&sb, "By Module:\n")
	for module, count := range s.ByModule {
		fmt.Fprintf(&sb, "  %s: %d\n", module, count)
	}
	return sb.String()
}
