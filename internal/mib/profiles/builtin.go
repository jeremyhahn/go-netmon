package profiles

import "time"

// GetBuiltInProfiles returns all built-in profiles
func GetBuiltInProfiles() []*MIBProfile {
	now := time.Now()

	return []*MIBProfile{
		getDefaultProfile(now),
		getCiscoIOSProfile(now),
		getCiscoIOSWithTablesProfile(now),
		getCiscoNexusProfile(now),
		getJuniperMXProfile(now),
		getAristaEOSProfile(now),
		getGenericRouterProfile(now),
		getGenericSwitchProfile(now),
		getInterfacesOnlyProfile(now),
	}
}

// getDefaultProfile returns the default monitoring profile
func getDefaultProfile(now time.Time) *MIBProfile {
	return &MIBProfile{
		Name:        "default",
		Description: "Default profile with standard system and interface metrics",
		Modules: []string{
			"SNMPv2-MIB",
			"IF-MIB",
		},
		IncludeTables: false,
		MaxOIDs:       0, // unlimited
		IsBuiltIn:     true,
		CreatedAt:     now,
		UpdatedAt:     now,
		Tags: map[string]string{
			"category": "general",
			"vendor":   "all",
		},
	}
}

// getCiscoIOSProfile returns the Cisco IOS profile
func getCiscoIOSProfile(now time.Time) *MIBProfile {
	return &MIBProfile{
		Name:        "cisco-ios",
		Description: "Cisco IOS routers with CPU, memory, and interface monitoring",
		Modules: []string{
			"SNMPv2-MIB",
			"IF-MIB",
			"IP-MIB",
			"CISCO-PROCESS-MIB",
			"CISCO-MEMORY-POOL-MIB",
		},
		ExcludeNames: []string{
			".*deprecated.*",
			".*obsolete.*",
		},
		IncludeTables: false,
		MaxOIDs:       0,
		IsBuiltIn:     true,
		CreatedAt:     now,
		UpdatedAt:     now,
		Tags: map[string]string{
			"category": "router",
			"vendor":   "cisco",
			"platform": "ios",
		},
	}
}

// getCiscoIOSWithTablesProfile returns the Cisco IOS profile with table monitoring enabled
func getCiscoIOSWithTablesProfile(now time.Time) *MIBProfile {
	return &MIBProfile{
		Name:        "cisco-ios-with-tables",
		Description: "Cisco IOS routers with full interface table monitoring",
		Modules: []string{
			"SNMPv2-MIB",
			"IF-MIB",
			"IP-MIB",
			"CISCO-PROCESS-MIB",
			"CISCO-MEMORY-POOL-MIB",
		},
		ExcludeNames: []string{
			".*deprecated.*",
			".*obsolete.*",
		},
		IncludeTables: true,
		MaxOIDs:       0, // unlimited
		IsBuiltIn:     true,
		CreatedAt:     now,
		UpdatedAt:     now,
		Tags: map[string]string{
			"category": "router",
			"vendor":   "cisco",
			"platform": "ios",
			"tables":   "enabled",
		},
	}
}

// getCiscoNexusProfile returns the Cisco Nexus profile
func getCiscoNexusProfile(now time.Time) *MIBProfile {
	return &MIBProfile{
		Name:        "cisco-nexus",
		Description: "Cisco Nexus switches with datacenter monitoring",
		Modules: []string{
			"SNMPv2-MIB",
			"IF-MIB",
			"IP-MIB",
			"CISCO-PROCESS-MIB",
			"CISCO-MEMORY-POOL-MIB",
		},
		ExcludeNames: []string{
			".*deprecated.*",
			".*obsolete.*",
		},
		IncludeTables: false,
		MaxOIDs:       0,
		IsBuiltIn:     true,
		CreatedAt:     now,
		UpdatedAt:     now,
		Tags: map[string]string{
			"category": "switch",
			"vendor":   "cisco",
			"platform": "nexus",
		},
	}
}

// getJuniperMXProfile returns the Juniper MX profile
func getJuniperMXProfile(now time.Time) *MIBProfile {
	return &MIBProfile{
		Name:        "juniper-mx",
		Description: "Juniper MX routers with standard metrics",
		Modules: []string{
			"SNMPv2-MIB",
			"IF-MIB",
			"IP-MIB",
		},
		ExcludeNames: []string{
			".*deprecated.*",
		},
		IncludeTables: false,
		MaxOIDs:       0,
		IsBuiltIn:     true,
		CreatedAt:     now,
		UpdatedAt:     now,
		Tags: map[string]string{
			"category": "router",
			"vendor":   "juniper",
			"platform": "junos",
		},
	}
}

// getAristaEOSProfile returns the Arista EOS profile
func getAristaEOSProfile(now time.Time) *MIBProfile {
	return &MIBProfile{
		Name:        "arista-eos",
		Description: "Arista EOS switches with datacenter monitoring",
		Modules: []string{
			"SNMPv2-MIB",
			"IF-MIB",
			"IP-MIB",
		},
		ExcludeNames: []string{
			".*deprecated.*",
		},
		IncludeTables: false,
		MaxOIDs:       0,
		IsBuiltIn:     true,
		CreatedAt:     now,
		UpdatedAt:     now,
		Tags: map[string]string{
			"category": "switch",
			"vendor":   "arista",
			"platform": "eos",
		},
	}
}

// getGenericRouterProfile returns a generic router profile
func getGenericRouterProfile(now time.Time) *MIBProfile {
	return &MIBProfile{
		Name:        "generic-router",
		Description: "Generic router with standard SNMP metrics",
		Modules: []string{
			"SNMPv2-MIB",
			"IF-MIB",
			"IP-MIB",
			"TCP-MIB",
			"UDP-MIB",
		},
		ExcludeNames: []string{
			".*deprecated.*",
			".*obsolete.*",
		},
		IncludeTables: false,
		MaxOIDs:       100, // Limit to prevent overload
		IsBuiltIn:     true,
		CreatedAt:     now,
		UpdatedAt:     now,
		Tags: map[string]string{
			"category": "router",
			"vendor":   "generic",
		},
	}
}

// getGenericSwitchProfile returns a generic switch profile
func getGenericSwitchProfile(now time.Time) *MIBProfile {
	return &MIBProfile{
		Name:        "generic-switch",
		Description: "Generic switch with interface and system metrics",
		Modules: []string{
			"SNMPv2-MIB",
			"IF-MIB",
		},
		IncludeNames: []string{
			"^sys.*", // System metrics
			"^if.*",  // Interface metrics
		},
		ExcludeNames: []string{
			".*deprecated.*",
			".*obsolete.*",
		},
		IncludeTables: false,
		MaxOIDs:       80,
		IsBuiltIn:     true,
		CreatedAt:     now,
		UpdatedAt:     now,
		Tags: map[string]string{
			"category": "switch",
			"vendor":   "generic",
		},
	}
}

// getInterfacesOnlyProfile returns a profile focused solely on interface monitoring
func getInterfacesOnlyProfile(now time.Time) *MIBProfile {
	return &MIBProfile{
		Name:        "interfaces-only",
		Description: "Interface monitoring only (minimal overhead)",
		Modules: []string{
			"IF-MIB",
		},
		IncludeNames: []string{
			"^if.*", // Only interface metrics
		},
		ExcludeNames: []string{
			".*deprecated.*",
		},
		IncludeTables: false,
		MaxOIDs:       50,
		IsBuiltIn:     true,
		CreatedAt:     now,
		UpdatedAt:     now,
		Tags: map[string]string{
			"category": "minimal",
			"vendor":   "all",
		},
	}
}

// GetProfileByVendor returns recommended profiles for a vendor
func GetProfileByVendor(vendor string) []string {
	vendorProfiles := map[string][]string{
		"cisco": {
			"cisco-ios",
			"cisco-nexus",
		},
		"juniper": {
			"juniper-mx",
		},
		"arista": {
			"arista-eos",
		},
		"generic": {
			"generic-router",
			"generic-switch",
		},
	}

	if profiles, ok := vendorProfiles[vendor]; ok {
		return profiles
	}

	return []string{"default"}
}

// GetProfileByCategory returns profiles filtered by category
func GetProfileByCategory(category string) []string {
	all := GetBuiltInProfiles()
	var matching []string

	for _, profile := range all {
		if cat, ok := profile.Tags["category"]; ok && cat == category {
			matching = append(matching, profile.Name)
		}
	}

	return matching
}
