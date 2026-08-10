# Friendly Name Labels for SNMP Metrics

## Overview

This feature automatically adds human-readable friendly name labels to SNMP metrics in Prometheus. Previously, metrics only had index labels like `ifindex="1"`. Now they also include friendly name labels like `ifdescr="GigabitEthernet0/0"` and `ifname="Gi0/0"`.

## Problem Solved

SNMP tables contain two types of columns:
1. **Metric columns** - numeric values (Counter32, Gauge32) that we want to chart
2. **Status columns** - string values (DisplayString, OctetString) that describe the metrics

Before this implementation:
- Only index values were extracted from OIDs (e.g., `ifindex="1"`)
- Friendly names were available in SNMP responses but not cached or used
- Querying metrics required knowing numeric indexes

After this implementation:
- Both index and friendly name labels are added to metrics
- Status column values are cached from string SNMP results
- Metrics can be queried using friendly names: `{device="router1", ifdescr="GigabitEthernet0/0"}`

## Architecture

### Components

1. **TableIndexCache** (`internal/metrics/table_index_cache.go`)
   - Extended with `friendlyNameCache` map for caching name-to-value mappings
   - Extended with `statusColumns` map for tracking which columns contain friendly names
   - New method: `CacheFriendlyNames()` - processes string SNMP results
   - Enhanced method: `ExtractIndexLabels()` - adds friendly name labels

2. **MIB Metadata Integration**
   - Uses `ObjectClassStatusColumn` from MIB storage to identify friendly name columns
   - Automatically discovers status columns during table metadata loading
   - Generic approach works for ALL SNMP tables, not just interfaces

3. **Caching Strategy**
   - Cache Key Format: `{device}:{entryOID}:{indexValue}:{columnName}`
   - Example: `"router1:.1.3.6.1.2.1.2.2.1:1:ifdescr"` -> `"GigabitEthernet0/0"`
   - Thread-safe using RWMutex
   - In-memory cache persists for the lifetime of the process

### Flow

1. **SNMP Polling** - Service polls configured OIDs from devices
   
2. **Friendly Name Caching** (`CacheFriendlyNames`)
   - Processes ALL SNMP results first
   - Extracts string values from status columns
   - Stores in cache: `device:entryOID:index:columnName -> value`
   
3. **Metric Conversion** (`ConvertSNMPResultsWithTableIndexes`)
   - Processes numeric results
   - Calls `ExtractIndexLabels(deviceName, oid)` with device context
   - Extracts both index labels and friendly name labels
   - Adds all labels to Prometheus samples

4. **Label Extraction** (`ExtractIndexLabels`)
   - Parses OID to extract table, column, and index information
   - Extracts index values based on index types
   - Looks up friendly names from cache
   - Returns combined label map

## Implementation Details

### Status Column Discovery

During table metadata loading (`loadAllTables`):
```go
// Find all status columns for this table
for _, oid := range allOIDs {
    if oid.IsColumn && oid.ObjectClass == mib.ObjectClassStatusColumn {
        // Store column info
        statusCols = append(statusCols, statusColumnInfo{
            Name:      oid.Name,      // "ifDescr"
            OID:       oid.OID,       // "1.3.6.1.2.1.2.2.1.2"
            ColumnID:  "2",           // Extracted from OID
            LabelName: "ifdescr",     // Lowercased for Prometheus
        })
    }
}
```

### Friendly Name Caching

When SNMP results arrive:
```go
func (c *TableIndexCache) CacheFriendlyNames(deviceName string, results []*snmp.Result) {
    for _, result := range results {
        // Get string value
        strValue, err := result.GetStringValue()
        if err != nil || strValue == "" {
            continue
        }
        
        // Find table and status columns
        tableInfo := c.findTableForOIDLocked(result.OID)
        statusCols := c.statusColumns[tableInfo.EntryOID]
        
        // Extract OID parts: entryOID.columnID.index
        // Example: .1.3.6.1.2.1.2.2.1.2.1 -> columnID=2, index=1
        
        // Cache: device:entryOID:index:labelname -> value
        cacheKey := fmt.Sprintf("%s:%s:%s:%s", 
            deviceName, tableInfo.EntryOID, indexValue, statusCol.LabelName)
        c.friendlyNameCache[cacheKey] = strValue
    }
}
```

### Label Injection

When extracting labels:
```go
func (c *TableIndexCache) ExtractIndexLabels(deviceName, oid string) map[string]string {
    labels := make(map[string]string)
    
    // Extract index labels (existing logic)
    labels["ifindex"] = "1"
    
    // Add friendly name labels (new logic)
    for _, statusCol := range c.statusColumns[tableInfo.EntryOID] {
        cacheKey := fmt.Sprintf("%s:%s:%s:%s", 
            deviceName, tableInfo.EntryOID, indexValue, statusCol.LabelName)
        
        if friendlyName, found := c.friendlyNameCache[cacheKey]; found {
            labels[statusCol.LabelName] = friendlyName
        }
    }
    
    return labels
}
```

## Examples

### Interface Metrics

Before:
```
snmp_1_3_6_1_2_1_2_2_1_10{device="router1", ifindex="1", oid="1.3.6.1.2.1.2.2.1.10.1"} 12345678
```

After:
```
snmp_1_3_6_1_2_1_2_2_1_10{device="router1", ifindex="1", ifdescr="GigabitEthernet0/0", ifname="Gi0/0", oid="1.3.6.1.2.1.2.2.1.10.1"} 12345678
```

### sysORTable Metrics

Before:
```
snmp_1_3_6_1_2_1_1_9_1_2{device="router1", sysortableindex="1", oid="1.3.6.1.2.1.1.9.1.2.1"} 123
```

After:
```
snmp_1_3_6_1_2_1_1_9_1_2{device="router1", sysortableindex="1", sysordescr="SNMP-FRAMEWORK-MIB", oid="1.3.6.1.2.1.1.9.1.2.1"} 123
```

### Prometheus Queries

Query by friendly name:
```promql
{device="router1", ifdescr="GigabitEthernet0/0"}
```

Query by index (still works):
```promql
{device="router1", ifindex="1"}
```

Query all interfaces with specific pattern:
```promql
{device="router1", ifdescr=~"GigabitEthernet.*"}
```

## Performance Considerations

1. **Memory Usage**
   - Cache grows with: number of devices × number of table rows × number of status columns
   - Typical interface table: 48 ports × 2 status columns = 96 cache entries per device
   - Each entry: ~100 bytes → ~10KB per device
   
2. **CPU Impact**
   - Caching happens once per poll cycle
   - Label lookup is O(1) hash map access
   - No measurable performance impact in testing

3. **Concurrency**
   - Thread-safe using RWMutex
   - Read-heavy workload (label extraction)
   - Write-only during SNMP result processing

## Testing

Comprehensive unit tests in `internal/metrics/table_index_cache_test.go`:

1. **TestCacheFriendlyNames** - Verifies caching from SNMP results
2. **TestExtractIndexLabelsWithFriendlyNames** - Verifies label extraction
3. **TestConvertSNMPResultsWithFriendlyNames** - End-to-end test
4. **TestExtractColumnID** - Helper function tests
5. **TestCacheFriendlyNamesWithInvalidResults** - Error handling

All tests pass with realistic MIB data.

## Future Enhancements

Potential improvements:
1. **Cache Persistence** - Save cache to disk for faster restarts
2. **Cache Expiration** - TTL for entries to handle interface changes
3. **Cache Metrics** - Expose cache hit/miss rates
4. **Label Prioritization** - Configure which status columns to use
5. **Label Normalization** - Sanitize values for Prometheus compatibility

## Compatibility

- **Backward Compatible** - Existing queries still work
- **No Breaking Changes** - Only adds new labels
- **Generic Implementation** - Works with ANY SNMP table that has status columns
- **MIB-Driven** - Uses MIB metadata, no hardcoded tables

## Related Files

- `/home/user/sources/go-netmon/internal/metrics/table_index_cache.go` - Main implementation
- `/home/user/sources/go-netmon/internal/metrics/table_index_cache_test.go` - Unit tests
- `/home/user/sources/go-netmon/internal/mib/types.go` - ObjectClass definitions
- `/home/user/sources/go-netmon/internal/mib/storage/storage.go` - MIB storage API
