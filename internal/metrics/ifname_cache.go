package metrics

import (
	"log"
	"regexp"
	"strings"
	"sync"

	"github.com/jeremyhahn/go-netmon/internal/snmp"
)

// InterfaceNameCache caches interface names by device and index
type InterfaceNameCache struct {
	mu    sync.RWMutex
	cache map[string]map[string]string // device -> index -> interface name
}

// NewInterfaceNameCache creates a new interface name cache
func NewInterfaceNameCache() *InterfaceNameCache {
	return &InterfaceNameCache{
		cache: make(map[string]map[string]string),
	}
}

// UpdateFromSNMPResults updates the cache from SNMP results containing ifDescr values
// ifDescr OID is .1.3.6.1.2.1.2.2.1.2.X where X is the interface index
func (c *InterfaceNameCache) UpdateFromSNMPResults(deviceName string, results []*snmp.Result) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Ensure device entry exists
	if c.cache[deviceName] == nil {
		c.cache[deviceName] = make(map[string]string)
	}

	// ifDescr OID pattern: .1.3.6.1.2.1.2.2.1.2.X
	ifDescrPrefix := ".1.3.6.1.2.1.2.2.1.2."
	ifDescrPrefixNoDot := "1.3.6.1.2.1.2.2.1.2."

	foundCount := 0
	for _, result := range results {
		if strings.HasPrefix(result.OID, ifDescrPrefix) || strings.HasPrefix(result.OID, ifDescrPrefixNoDot) {
			// Extract interface index from OID
			var index string
			if strings.HasPrefix(result.OID, ifDescrPrefix) {
				index = strings.TrimPrefix(result.OID, ifDescrPrefix)
			} else {
				index = strings.TrimPrefix(result.OID, ifDescrPrefixNoDot)
			}

			// Get string value (interface name/description)
			if ifName, err := result.GetStringValue(); err == nil && ifName != "" {
				c.cache[deviceName][index] = ifName
				foundCount++
			}
		}
	}

	if foundCount > 0 {
		log.Printf("Updated interface cache for device %s: %d interfaces cached", deviceName, len(c.cache[deviceName]))
	}
}

// GetInterfaceName returns the interface name for a given device and index
func (c *InterfaceNameCache) GetInterfaceName(deviceName, index string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if deviceCache, exists := c.cache[deviceName]; exists {
		if ifName, exists := deviceCache[index]; exists {
			return ifName
		}
	}

	return "" // Return empty string if not found
}

// ExtractTableIndex extracts the interface index from a table column OID
// For example: .1.3.6.1.2.1.2.2.1.10.5 -> "5"
// Returns empty string if not a table OID
func ExtractTableIndex(oid string) string {
	// Pattern for IF-MIB table columns: .1.3.6.1.2.1.2.2.1.X.INDEX
	// Where X is the column ID and INDEX is the interface index
	re := regexp.MustCompile(`^\.1\.3\.6\.1\.2\.1\.2\.2\.1\.\d+\.(\d+)$`)
	matches := re.FindStringSubmatch(oid)

	if len(matches) > 1 {
		return matches[1]
	}

	// Try without leading dot
	re2 := regexp.MustCompile(`^1\.3\.6\.1\.2\.1\.2\.2\.1\.\d+\.(\d+)$`)
	matches2 := re2.FindStringSubmatch(oid)

	if len(matches2) > 1 {
		return matches2[1]
	}

	// Generic pattern for any table: OID ends with .INDEX
	// This is a fallback for other SNMP tables
	parts := strings.Split(strings.TrimPrefix(oid, "."), ".")
	if len(parts) >= 2 {
		// Last part is typically the index
		lastPart := parts[len(parts)-1]
		// Verify it's numeric (indices are always numeric)
		if _, err := regexp.MatchString(`^\d+$`, lastPart); err == nil {
			return lastPart
		}
	}

	return ""
}

// ConvertSNMPResultsWithInterfaceNames converts SNMP results to samples with interface names.
//
// It has no access to MIB metadata, so every sample is named after its raw
// instance OID. Use ConvertSNMPResultsWithTableIndexes, which resolves names
// against the MIB store, for anything exported to Prometheus.
func ConvertSNMPResultsWithInterfaceNames(
	deviceName string,
	results []*snmp.Result,
	extraLabels map[string]string,
	ifNameCache *InterfaceNameCache,
) []*Sample {
	samples := make([]*Sample, 0, len(results))
	systemInfo := make(map[string]string) // Collect system text values

	for _, result := range results {
		// Try numeric value first
		value, err := result.GetFloat64Value()
		if err != nil {
			// Not a reading. It may still be one of the system scalars that
			// snmp_system_info reports as labels.
			recordSystemInfo(systemInfo, result)
			continue
		}

		labels := newInstanceLabels(deviceName, result.OID)

		// The interface this reading belongs to is as structural as the OID:
		// it says which series the reading is part of.
		if index := ExtractTableIndex(result.OID); index != "" {
			if ifName := ifNameCache.GetInterfaceName(deviceName, index); ifName != "" {
				labels["ifname"] = ifName
				labels["ifindex"] = index
			}
		}

		mergeExtraLabels(deviceName, labels, extraLabels)

		metricName, _ := resolveMetricName(nil, result.OID)

		samples = append(samples, &Sample{
			Name:      metricName,
			Labels:    labels,
			Value:     value,
			Timestamp: result.Timestamp,
		})
	}

	if len(systemInfo) > 0 {
		samples = append(samples, newSystemInfoSample(deviceName, systemInfo, extraLabels, pollTimestamp(results)))
	}

	return samples
}
