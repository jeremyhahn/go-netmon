# Enhanced MIB Parser

## Overview

The enhanced MIB parser extracts comprehensive metadata from SNMP MIB files to enable intelligent dashboard generation. This is Phase 1 of the MIB Dashboard Redesign.

## Features

### 1. Enum Value Extraction
Automatically extracts named values from INTEGER types:
```go
// Example: ifOperStatus
EnumValues: {
    1: "up",
    2: "down",
    3: "testing",
    4: "unknown",
    5: "dormant",
    6: "notPresent",
    7: "lowerLayerDown",
}
```

### 2. Table Hierarchy Detection
Identifies table relationships automatically:
```go
// Example: ifDescr column
TableOID: "1.3.6.1.2.1.2.2"       // ifTable
EntryOID: "1.3.6.1.2.1.2.2.1"     // ifEntry
```

### 3. Object Classification
Categorizes objects by semantic role:
- **Scalar**: Single global values (e.g., ifNumber)
- **TableNode**: Table containers (e.g., ifTable)
- **EntryNode**: Row definitions (e.g., ifEntry)
- **IndexColumn**: Index/key columns (e.g., ifIndex)
- **StatusColumn**: Display-only columns (e.g., ifDescr, ifOperStatus)
- **MetricColumn**: Time-series metrics (e.g., ifInOctets, ifOutOctets)

### 4. Chartability Detection
Determines appropriate visualization:
- Index columns → NOT chartable (identifiers)
- Status columns → NOT chartable (display as text)
- Counter/Gauge columns → Chartable as line/gauge charts
- Scalars → Chartable if numeric

### 5. Range Extraction
Captures value constraints:
```go
RangeMin: &minValue
RangeMax: &maxValue
```

### 6. Display Hints
Extracts formatting information for proper rendering.

## Usage

### Basic Example
```go
package main

import (
    "github.com/jeremyhahn/go-netmon/internal/mib/parser"
    "github.com/jeremyhahn/go-netmon/internal/mib"
)

func main() {
    // Create parser with MIB search paths
    p := parser.New("/usr/share/snmp/mibs/ietf", "/usr/share/snmp/mibs")
    
    // Initialize gosmi library
    if err := p.Init(); err != nil {
        panic(err)
    }
    defer p.Close()
    
    // Load and parse a MIB module
    if _, err := p.LoadModule("IF-MIB"); err != nil {
        panic(err)
    }
    
    oids, err := p.ParseModule("IF-MIB")
    if err != nil {
        panic(err)
    }
    
    // Process parsed OIDs
    for _, oid := range oids {
        switch oid.ObjectClass {
        case mib.ObjectClassIndexColumn:
            fmt.Printf("Index: %s\n", oid.Name)
            
        case mib.ObjectClassStatusColumn:
            fmt.Printf("Status: %s", oid.Name)
            if len(oid.EnumValues) > 0 {
                fmt.Printf(" (enums: %v)", oid.EnumValues)
            }
            fmt.Println()
            
        case mib.ObjectClassMetricColumn:
            if oid.IsChartable {
                fmt.Printf("Metric: %s (chart: %s)\n", oid.Name, oid.ChartType)
            }
        }
    }
}
```

### Filtering Examples
```go
// Get all index columns
for _, oid := range oids {
    if oid.IsIndex {
        fmt.Printf("Index: %s for %s\n", oid.Name, oid.IndexFor)
    }
}

// Get all chartable metrics
for _, oid := range oids {
    if oid.IsChartable {
        fmt.Printf("Chart: %s (%s)\n", oid.Name, oid.ChartType)
    }
}

// Get all objects with enum values
for _, oid := range oids {
    if len(oid.EnumValues) > 0 {
        fmt.Printf("%s: %v\n", oid.Name, oid.EnumValues)
    }
}
```

## Testing

### Prerequisites
Standard IETF MIBs must be installed:

```bash
# Ubuntu/Debian
sudo apt-get install snmp-mibs-downloader
sudo download-mibs

# Verify installation
ls /usr/share/snmp/mibs/ietf/
```

### Running Tests
```bash
# Run all parser tests
make test-parser

# Run enhanced feature tests
make test-parser-enhanced

# Generate coverage report
make coverage-parser

# Run benchmarks
make bench-parser
```

### Test Coverage
The test suite includes:
- Enum value extraction validation
- Table relationship detection verification
- Object classification accuracy
- Chartability determination correctness
- Index column identification
- Displayability flag validation
- Backward compatibility checks

Target coverage: 90%+

## API Reference

### New OIDMetadata Fields

```go
type OIDMetadata struct {
    // ... existing fields ...
    
    // Semantic classification
    ObjectClass ObjectClass `json:"object_class"`
    
    // Hierarchy
    TableOID string `json:"table_oid,omitempty"`
    EntryOID string `json:"entry_oid,omitempty"`
    
    // Index tracking
    IsIndex  bool   `json:"is_index"`
    IndexFor string `json:"index_for,omitempty"`
    
    // Display metadata
    DisplayHint string            `json:"display_hint,omitempty"`
    EnumValues  map[int64]string  `json:"enum_values,omitempty"`
    RangeMin    *int64            `json:"range_min,omitempty"`
    RangeMax    *int64            `json:"range_max,omitempty"`
    
    // Rendering hints
    IsChartable   bool   `json:"is_chartable"`
    IsDisplayable bool   `json:"is_displayable"`
    ChartType     string `json:"chart_type,omitempty"`
}
```

### ObjectClass Constants
```go
const (
    ObjectClassScalar       ObjectClass = "scalar"
    ObjectClassTableNode    ObjectClass = "table_node"
    ObjectClassEntryNode    ObjectClass = "entry_node"
    ObjectClassIndexColumn  ObjectClass = "index_column"
    ObjectClassStatusColumn ObjectClass = "status_column"
    ObjectClassMetricColumn ObjectClass = "metric_column"
    ObjectClassUnknown      ObjectClass = "unknown"
)
```

## Architecture

### Design Documents
- [MIB Dashboard Redesign](/docs/architecture/mib-dashboard-redesign.md)
- [Implementation Guide](/docs/architecture/implementation-guide.md)

### Implementation Phases
1. **Phase 1** (CURRENT): Enhanced Parser
2. **Phase 2** (NEXT): Enhanced Storage Layer
3. **Phase 3**: Dashboard Generator
4. **Phase 4**: Enhanced API Handlers
5. **Phase 5**: Frontend Refactor

## Performance

The enhanced parser maintains similar performance to the basic parser:
- Enum extraction: O(n) where n = number of enum values
- Table hierarchy: O(1) lookups using gosmi
- Classification: O(1) decision tree per object

## Known Limitations

1. **MIB Dependencies**: Requires standard MIBs to be installed on system
2. **Type Assertions**: Enum value extraction uses type assertions (safe with fallback)
3. **gosmi API**: Uses lower-level `smi` package functions for advanced features

## Contributing

When adding new classification logic:
1. Add test case to `parser_enhanced_test.go`
2. Implement logic in `determineObjectClass()` or related functions
3. Update documentation
4. Ensure 90%+ coverage maintained

## Related Components

- **Storage**: `internal/mib/storage/` - Persists parsed metadata
- **Dashboard Generator**: `internal/mib/dashboard/` (Phase 3)
- **API Handlers**: `internal/webui/api/mib_handler.go`

## License

Same as parent project.
