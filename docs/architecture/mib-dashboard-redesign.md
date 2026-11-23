# MIB Dashboard Architecture Redesign

> **Status: ARCHIVE** · Scope: go-netmon
>
> Design proposal, built in part and otherwise differently; not maintained.
> **Built:** the semantic fields on `mib.OIDMetadata` (`ObjectClass`, `TableOID`,
> `EntryOID`, `IsIndex`, `IndexFor`, `DisplayHint`, `EnumValues`, `RangeMin`,
> `RangeMax`, `IsChartable`, `IsDisplayable`, `ChartType`) and the parser
> classification that fills `ObjectClass` (`internal/mib/types.go`,
> `internal/mib/parser/parser.go`). **Not built:** the `DashboardSpec`,
> `ScalarWidget` and `TableWidget` contract, the `internal/mib/dashboard`
> package, the enhanced `TableInfo` column lists, and `GetTableInfoEnhanced`.
> `detectTables` in `internal/webui/api/mib_handler.go` still matches OID
> patterns for `GET /api/mibs/{id}/tables`. **Replaced by:** dashboards are
> built from the metrics a device reports in Prometheus joined with the metric
> catalog the poller publishes (`generateDashboard` in
> `internal/webui/api/dashboard_gen.go`), returned as `Dashboard` and `Panel`
> values. See [../dashboard-generation.md](../dashboard-generation.md),
> [../MIB_ARCHITECTURE.md](../MIB_ARCHITECTURE.md) and
> [system-architecture.md](system-architecture.md). The file and line
> references below describe the code as it was when this was written.

## Executive Summary

The current MIB dashboard generation system has critical architectural flaws that prevent it from correctly displaying MIB objects. This document provides a comprehensive analysis of the problems and proposes a complete architectural redesign.

## Problem Analysis

### Critical Issues

#### 1. **Tab/MIB Association Broken**
**Symptom**: Clicking CISCO-BGP4-MIB tab displays IF-MIB objects.

**Root Cause**: The system doesn't maintain proper association between MIB tabs and their objects. The `loadMibCharts()` function fetches objects from a database that may contain multiple MIBs, but filtering by `mib.name` relies on accurate Module field values from the parser.

**Current Code Flow**:
```javascript
// app.js:1931 - finds MIB by name
const mib = deviceDash.mibs.find(m => m.name === mibName);

// app.js:1958 - filters objects by module
const pollableObjects = mibDetail.objects.filter(obj =>
    obj.pollable === true && obj.oid && obj.oid.trim() !== ''
);

// BUT: No module-based filtering! All objects from all MIBs are returned
```

**Evidence**: The handler in `mib_handler.go:51-73` iterates ALL OIDs and filters by `oid.Module == mib.Name`, but if the module name is wrong or the database has mixed data, wrong objects appear.

#### 2. **Instance Selector Not Populating**
**Symptom**: Master instance selector dropdown is empty.

**Root Cause Chain**:
1. `isTableColumn()` function (app.js:1896) depends on `tableMetadata[mibId]`
2. `tableMetadata` is populated by `fetchTableMetadata()` (app.js:1873)
3. `fetchTableMetadata()` calls `/api/mibs/{id}/tables`
4. Backend `detectTables()` (mib_handler.go:196) uses pattern matching on OID strings
5. Pattern matching is fragile and fails to detect valid SNMP tables

**Pattern Matching Failure**:
```go
// mib_handler.go:202-203 - Requires 10+ OID segments
if len(parts) < 10 {
    continue // Too short to be a table column
}

// This arbitrary threshold fails for:
// - Short OIDs (e.g., 1.3.6.1.2.1.2.2.1.1 = 10 parts, barely passes)
// - BGP MIBs with different structures
```

#### 3. **Inappropriate Chart Generation**
**Symptom**: Charts created for `ifIndex` (integer identifier, value 25).

**Root Cause**: The system doesn't distinguish between:
- **Index objects**: Identifiers (ifIndex, bgpPeerRemoteAddr) - should NOT be charted
- **Status objects**: Enums/strings (ifOperStatus, ifDescr) - display as text
- **Metric objects**: Counters/gauges (ifInOctets, ifOutOctets) - chart as time-series

**Current Logic**:
```javascript
// app.js:1976 - Weak categorization
if (this.isStatusObject(obj.type, obj.name)) {
    statusObjects.push(obj);
} else {
    metricObjects.push(obj);  // Everything else becomes a chart!
}
```

The `isStatusObject()` function only checks type names like "Integer" or "DisplayString", not semantic meaning.

#### 4. **No MIB Structure Understanding**
**Root Cause**: The parser (`parser.go`) extracts metadata but doesn't capture critical relationships:

**What's Missing**:
- Table-to-entry-to-column hierarchy
- Which columns are indices vs data
- Object augmentation relationships
- Object groups and their members
- Label mappings (numeric index to human-readable name)

**What's Available But Unused**:
```go
// types.go:20-24 - Metadata exists but isn't leveraged
Index        []string  `json:"index,omitempty"`        // For tables
Augments     string    `json:"augments,omitempty"`     // For augmented tables
IsTable      bool      `json:"is_table"`
IsTableEntry bool      `json:"is_table_entry"`
IsColumn     bool      `json:"is_column"`
```

The parser sets these flags (parser.go:139-145) but downstream code doesn't use them.

### Data Flow Issues

**Current Flow**:
```
MIB File → Parser → Storage (OIDMetadata) → DB (MIB records) → API → Frontend
                                                                          ↓
                                                Pattern-based table detection
                                                          ↓
                                                    Dashboard rendering
```

**Problems**:
1. **Lost Context**: Parser extracts rich metadata, but it's flattened in storage
2. **Disconnected Data**: MIB storage and device database are separate, causing sync issues
3. **Fragile Detection**: Pattern-based table detection instead of using parsed metadata
4. **No Semantic Layer**: No component understands "what should be charted vs displayed"

---

## Proposed Architecture

### Design Principles

1. **Single Source of Truth**: Use parser metadata as the authoritative source
2. **Semantic Understanding**: Explicitly model object roles (index, status, metric)
3. **Structured Metadata**: Preserve table/entry/column hierarchies
4. **Contract-First APIs**: Define clear data structures for each layer
5. **Fail-Safe Defaults**: Gracefully handle missing or incomplete metadata

### Architecture Overview

```
┌─────────────────────────────────────────────────────────────────────┐
│                         MIB File (ASN.1)                            │
└────────────────────────────┬────────────────────────────────────────┘
                             │
                             ▼
┌─────────────────────────────────────────────────────────────────────┐
│                    Enhanced Parser (gosmi)                          │
│  • Parse table structures (table → entry → columns)                │
│  • Extract index definitions                                        │
│  • Identify object dependencies                                     │
│  • Extract syntax/enums for status objects                          │
└────────────────────────────┬────────────────────────────────────────┘
                             │
                             ▼
┌─────────────────────────────────────────────────────────────────────┐
│                    Enriched MIB Storage                             │
│  • Store structured metadata (not just flat OIDs)                  │
│  • Maintain object hierarchies                                      │
│  • Index by multiple keys (OID, name, parent)                      │
│  • Support efficient querying                                       │
└────────────────────────────┬────────────────────────────────────────┘
                             │
                             ▼
┌─────────────────────────────────────────────────────────────────────┐
│                    Dashboard Generator                              │
│  • Categorize objects by semantic role                             │
│  • Generate dashboard specifications                                │
│  • Create instance selectors for tables                             │
│  • Define appropriate visualizations                                │
└────────────────────────────┬────────────────────────────────────────┘
                             │
                             ▼
┌─────────────────────────────────────────────────────────────────────┐
│                    Dashboard API (JSON)                             │
│  • Return dashboard spec (not raw objects)                          │
│  • Include rendering hints                                          │
│  • Provide instance metadata                                        │
│  • Include Prometheus query templates                               │
└────────────────────────────┬────────────────────────────────────────┘
                             │
                             ▼
┌─────────────────────────────────────────────────────────────────────┐
│                    Frontend Renderer                                │
│  • Consume dashboard spec                                           │
│  • Render appropriate UI components                                 │
│  • Execute Prometheus queries                                       │
│  • Update displays via WebSocket                                    │
└─────────────────────────────────────────────────────────────────────┘
```

---

## Data Structures

### 1. Enhanced OID Metadata

```go
// OIDMetadata contains comprehensive metadata about an SNMP OID
type OIDMetadata struct {
    // Basic identification
    OID          string    `json:"oid"`
    Name         string    `json:"name"`
    Module       string    `json:"module"`

    // Type information
    Type         string    `json:"type"`          // Counter32, Gauge32, etc.
    Syntax       string    `json:"syntax"`        // DisplayString, INTEGER, etc.
    Access       string    `json:"access"`
    Status       string    `json:"status"`

    // Semantic classification (NEW)
    ObjectClass  ObjectClass `json:"object_class"` // Scalar, TableNode, EntryNode, IndexColumn, StatusColumn, MetricColumn

    // Hierarchy information
    Parent       string    `json:"parent,omitempty"`
    TableOID     string    `json:"table_oid,omitempty"`    // For columns, the parent table
    EntryOID     string    `json:"entry_oid,omitempty"`    // For columns, the parent entry

    // Table-specific
    IsIndex      bool      `json:"is_index"`               // NEW: Is this an index column?
    IndexFor     string    `json:"index_for,omitempty"`    // NEW: Entry this indexes
    Indexes      []string  `json:"indexes,omitempty"`      // For entries, the index columns
    Augments     string    `json:"augments,omitempty"`

    // Display hints (NEW)
    DisplayHint  string              `json:"display_hint,omitempty"`  // Format string (e.g., "1d-1d-1d,1d:1d:1d.1d,1a1d,1a")
    EnumValues   map[int64]string    `json:"enum_values,omitempty"`   // For INTEGER types with named values
    RangeMin     *int64              `json:"range_min,omitempty"`
    RangeMax     *int64              `json:"range_max,omitempty"`

    // Metric metadata (NEW)
    IsChartable  bool      `json:"is_chartable"`           // Should this be displayed as a chart?
    IsDisplayable bool     `json:"is_displayable"`         // Should this be displayed as text?
    ChartType    string    `json:"chart_type,omitempty"`   // line, gauge, bar

    // Existing fields
    Description  string    `json:"description"`
    Units        string    `json:"units,omitempty"`
    Pollable     bool      `json:"pollable"`
    IsTable      bool      `json:"is_table"`
    IsTableEntry bool      `json:"is_table_entry"`
    IsColumn     bool      `json:"is_column"`
    IsScalar     bool      `json:"is_scalar"`
    CreatedAt    time.Time `json:"created_at"`
    UpdatedAt    time.Time `json:"updated_at"`
    Tags         map[string]string `json:"tags,omitempty"`
}

// ObjectClass defines the semantic role of an OID
type ObjectClass string

const (
    ObjectClassScalar       ObjectClass = "scalar"         // Single global value
    ObjectClassTableNode    ObjectClass = "table_node"     // Table container
    ObjectClassEntryNode    ObjectClass = "entry_node"     // Table entry/row
    ObjectClassIndexColumn  ObjectClass = "index_column"   // Index/key column
    ObjectClassStatusColumn ObjectClass = "status_column"  // Status/info column
    ObjectClassMetricColumn ObjectClass = "metric_column"  // Time-series metric column
    ObjectClassUnknown      ObjectClass = "unknown"        // Unclassified
)
```

### 2. Enhanced Table Info

```go
// TableInfo contains comprehensive information about an SNMP table
type TableInfo struct {
    OID         string         `json:"oid"`
    Name        string         `json:"name"`
    Module      string         `json:"module"`
    Description string         `json:"description"`

    // Entry information
    EntryOID    string         `json:"entry_oid"`
    EntryName   string         `json:"entry_name"`

    // Columns categorized by role
    IndexColumns  []ColumnInfo `json:"index_columns"`   // NEW: Index columns
    StatusColumns []ColumnInfo `json:"status_columns"`  // NEW: Status/info columns
    MetricColumns []ColumnInfo `json:"metric_columns"`  // NEW: Metric columns

    // Augmentation
    Augments      string       `json:"augments,omitempty"`
    AugmentedBy   []string     `json:"augmented_by,omitempty"`

    // Instance metadata (NEW)
    InstanceLabels []string    `json:"instance_labels"`  // Labels to use (e.g., ["ifindex", "ifname"])
}

// ColumnInfo represents a table column with full metadata
type ColumnInfo struct {
    OID          string            `json:"oid"`
    Name         string            `json:"name"`
    Type         string            `json:"type"`
    Syntax       string            `json:"syntax"`
    Description  string            `json:"description"`
    Units        string            `json:"units,omitempty"`

    // Display hints
    DisplayHint  string            `json:"display_hint,omitempty"`
    EnumValues   map[int64]string  `json:"enum_values,omitempty"`

    // Classification
    IsIndex      bool              `json:"is_index"`
    IsChartable  bool              `json:"is_chartable"`
    ChartType    string            `json:"chart_type,omitempty"`  // line, gauge, bar
}
```

### 3. Dashboard Specification (NEW)

This is the key contract between backend and frontend.

```go
// DashboardSpec defines how to render a MIB dashboard
type DashboardSpec struct {
    MIBName     string              `json:"mib_name"`
    MIBModule   string              `json:"mib_module"`
    Description string              `json:"description"`

    // Dashboard sections
    Scalars     []ScalarWidget      `json:"scalars,omitempty"`      // Global scalars
    Tables      []TableWidget       `json:"tables,omitempty"`       // Table-based widgets
}

// ScalarWidget defines a single scalar value display
type ScalarWidget struct {
    OID         string            `json:"oid"`
    Name        string            `json:"name"`
    DisplayName string            `json:"display_name"`
    Type        string            `json:"type"`
    Description string            `json:"description"`
    Units       string            `json:"units,omitempty"`

    // Rendering
    WidgetType  string            `json:"widget_type"`    // "status", "gauge", "chart"
    EnumValues  map[int64]string  `json:"enum_values,omitempty"`

    // Query template
    QueryTemplate string          `json:"query_template"` // Prometheus query with {{device}} placeholder
}

// TableWidget defines a table-based display with instance selector
type TableWidget struct {
    TableName   string            `json:"table_name"`
    Description string            `json:"description"`

    // Instance selection
    InstanceSelector InstanceSelector `json:"instance_selector"`

    // Status fields (displayed as text)
    StatusFields []StatusField     `json:"status_fields,omitempty"`

    // Metric charts (displayed as time-series)
    MetricCharts []MetricChart     `json:"metric_charts,omitempty"`
}

// InstanceSelector defines how to select table instances
type InstanceSelector struct {
    LabelName    string   `json:"label_name"`      // Prometheus label (e.g., "ifindex", "ifname")
    DisplayLabel string   `json:"display_label"`   // UI label (e.g., "Interface")
    IndexColumns []string `json:"index_columns"`   // OID names of index columns

    // Query to get available instances
    InstanceQueryTemplate string `json:"instance_query_template"` // Query to list instances
}

// StatusField defines a text status display
type StatusField struct {
    OID          string            `json:"oid"`
    Name         string            `json:"name"`
    DisplayName  string            `json:"display_name"`
    Description  string            `json:"description"`
    Type         string            `json:"type"`
    EnumValues   map[int64]string  `json:"enum_values,omitempty"`

    // Query template with {{device}} and {{instance}} placeholders
    QueryTemplate string           `json:"query_template"`
}

// MetricChart defines a time-series chart
type MetricChart struct {
    OID          string   `json:"oid"`
    Name         string   `json:"name"`
    DisplayName  string   `json:"display_name"`
    Description  string   `json:"description"`
    Type         string   `json:"type"`        // Counter32, Gauge32, etc.
    Units        string   `json:"units,omitempty"`
    ChartType    string   `json:"chart_type"`  // line, gauge, bar

    // Query template with {{device}} and {{instance}} placeholders
    QueryTemplate string  `json:"query_template"`
}
```

---

## API Contracts

### 1. Get Dashboard Specification

**Endpoint**: `GET /api/devices/{deviceId}/mibs/{mibName}/dashboard`

**Response**:
```json
{
  "mib_name": "IF-MIB",
  "mib_module": "IF-MIB",
  "description": "Interface monitoring dashboard",
  "scalars": [
    {
      "oid": "1.3.6.1.2.1.2.1.0",
      "name": "ifNumber",
      "display_name": "Number of Interfaces",
      "type": "Integer32",
      "description": "The number of network interfaces present on this system",
      "widget_type": "status",
      "query_template": "snmp_ifNumber{device=\"{{device}}\"}"
    }
  ],
  "tables": [
    {
      "table_name": "ifTable",
      "description": "Interface statistics table",
      "instance_selector": {
        "label_name": "ifindex",
        "display_label": "Interface",
        "index_columns": ["ifIndex"],
        "instance_query_template": "group by (ifindex, ifname) (snmp_ifIndex{device=\"{{device}}\"})"
      },
      "status_fields": [
        {
          "oid": "1.3.6.1.2.1.2.2.1.2",
          "name": "ifDescr",
          "display_name": "Description",
          "description": "A textual string containing information about the interface",
          "type": "DisplayString",
          "query_template": "snmp_ifDescr{device=\"{{device}}\",ifindex=\"{{instance}}\"}"
        },
        {
          "oid": "1.3.6.1.2.1.2.2.1.8",
          "name": "ifOperStatus",
          "display_name": "Operational Status",
          "type": "INTEGER",
          "enum_values": {
            "1": "up",
            "2": "down",
            "3": "testing",
            "4": "unknown",
            "5": "dormant",
            "6": "notPresent",
            "7": "lowerLayerDown"
          },
          "query_template": "snmp_ifOperStatus{device=\"{{device}}\",ifindex=\"{{instance}}\"}"
        }
      ],
      "metric_charts": [
        {
          "oid": "1.3.6.1.2.1.2.2.1.10",
          "name": "ifInOctets",
          "display_name": "Inbound Traffic",
          "type": "Counter32",
          "units": "octets",
          "chart_type": "line",
          "query_template": "rate(snmp_ifInOctets{device=\"{{device}}\",ifindex=\"{{instance}}\"}[5m])"
        },
        {
          "oid": "1.3.6.1.2.1.2.2.1.16",
          "name": "ifOutOctets",
          "display_name": "Outbound Traffic",
          "type": "Counter32",
          "units": "octets",
          "chart_type": "line",
          "query_template": "rate(snmp_ifOutOctets{device=\"{{device}}\",ifindex=\"{{instance}}\"}[5m])"
        }
      ]
    }
  ]
}
```

### 2. Get Table Metadata (Enhanced)

**Endpoint**: `GET /api/mibs/{mibId}/tables`

**Response**:
```json
{
  "tables": [
    {
      "oid": "1.3.6.1.2.1.2.2",
      "name": "ifTable",
      "module": "IF-MIB",
      "description": "A list of interface entries",
      "entry_oid": "1.3.6.1.2.1.2.2.1",
      "entry_name": "ifEntry",
      "index_columns": [
        {
          "oid": "1.3.6.1.2.1.2.2.1.1",
          "name": "ifIndex",
          "type": "InterfaceIndex",
          "syntax": "Integer32",
          "is_index": true,
          "is_chartable": false
        }
      ],
      "status_columns": [
        {
          "oid": "1.3.6.1.2.1.2.2.1.2",
          "name": "ifDescr",
          "type": "DisplayString",
          "syntax": "OCTET STRING",
          "is_chartable": false
        },
        {
          "oid": "1.3.6.1.2.1.2.2.1.8",
          "name": "ifOperStatus",
          "type": "INTEGER",
          "syntax": "INTEGER",
          "enum_values": {"1": "up", "2": "down", "3": "testing"},
          "is_chartable": false
        }
      ],
      "metric_columns": [
        {
          "oid": "1.3.6.1.2.1.2.2.1.10",
          "name": "ifInOctets",
          "type": "Counter32",
          "syntax": "Counter32",
          "units": "octets",
          "is_chartable": true,
          "chart_type": "line"
        }
      ],
      "instance_labels": ["ifindex", "ifname"]
    }
  ]
}
```

---

## Implementation Roadmap

### Phase 1: Enhanced Parser (1-2 days)

**Components to Modify**:
- `internal/mib/parser/parser.go`
- `internal/mib/types.go`

**Tasks**:
1. Add `ObjectClass` classification to `parseNode()`
2. Extract enum values from gosmi for INTEGER types
3. Implement `DetermineObjectClass()` function:
   - Check `IsTable` → `ObjectClassTableNode`
   - Check `IsTableEntry` → `ObjectClassEntryNode`
   - Check `IsColumn` + in `Index` array → `ObjectClassIndexColumn`
   - Check `IsColumn` + counter/gauge type → `ObjectClassMetricColumn`
   - Check `IsColumn` + string/enum type → `ObjectClassStatusColumn`
   - Check `IsScalar` → `ObjectClassScalar`
4. Add display hints extraction (DisplayHint from gosmi)
5. Populate `TableOID` and `EntryOID` for columns
6. Set `IsIndex` flag for index columns
7. Extract range information (min/max)

**Deliverables**:
- Enhanced `OIDMetadata` with all new fields populated
- Unit tests for each object class detection
- Integration test with IF-MIB and CISCO-BGP4-MIB

### Phase 2: Enhanced Storage Layer (1 day)

**Components to Modify**:
- `internal/mib/storage/storage.go`
- `internal/mib/types.go`

**Tasks**:
1. Add `TableInfo` categorization:
   - Create `GetTableInfoEnhanced()` method
   - Separate columns into index/status/metric categories
   - Populate `ColumnInfo` with full metadata
2. Add efficient table querying:
   - Index by table OID
   - Support querying all columns of a table
3. Add module isolation:
   - Ensure module-based filtering is bulletproof
   - Add validation on storage operations

**Deliverables**:
- Enhanced `GetTableInfoEnhanced()` API
- Migration path for existing data
- Unit tests for table categorization

### Phase 3: Dashboard Generator (2-3 days)

**New Component**: `internal/mib/dashboard/generator.go`

**Tasks**:
1. Create `DashboardSpec` generator:
   ```go
   func GenerateDashboardSpec(module string, storage Storage) (*DashboardSpec, error)
   ```
2. Implement widget generation:
   - `generateScalarWidgets()` - for scalar objects
   - `generateTableWidgets()` - for table-based objects
3. Implement query template generation:
   - Prometheus metric name: `snmp_<MIBObjectName>` (see docs/metric-naming.md)
   - Labels: `device`, `ifindex`/`ifname`, etc.
   - Handle rate() for counters vs. raw values for gauges
4. Implement instance selector generation:
   - Determine appropriate labels from index columns
   - Generate instance discovery queries
5. Handle edge cases:
   - Augmented tables (merge columns)
   - Multiple index columns (composite keys)
   - Tables without numeric indices (use IMPLIED indices)

**Deliverables**:
- `DashboardSpec` generation for any MIB
- Query template engine
- Unit tests for IF-MIB, CISCO-BGP4-MIB, SNMPv2-MIB
- Integration test with live storage

### Phase 4: Enhanced API Handlers (1-2 days)

**Components to Modify**:
- `internal/webui/api/mib_handler.go`

**Tasks**:
1. Add new endpoint: `GET /api/devices/{deviceId}/mibs/{mibName}/dashboard`
   ```go
   func (s *Server) handleGetMIBDashboard(w http.ResponseWriter, r *http.Request)
   ```
2. Enhance `handleGetMIBTables()`:
   - Use enhanced `GetTableInfoEnhanced()` instead of pattern detection
   - Remove fragile `detectTables()` function
   - Return categorized columns
3. Add validation:
   - Ensure MIB exists for device
   - Return 404 for missing MIBs
   - Return proper error messages

**Deliverables**:
- New dashboard spec API endpoint
- Enhanced tables API with categorized columns
- API documentation
- Integration tests

### Phase 5: Frontend Refactor (2-3 days)

**Components to Modify**:
- `internal/webui/static/js/app.js`

**Tasks**:
1. Implement dashboard spec consumer:
   - `loadDashboardSpec(deviceId, mibName)` - fetch spec
   - `renderDashboardFromSpec(spec)` - render UI
2. Refactor instance selector:
   - Use `instance_query_template` from spec
   - Populate selector from Prometheus query results
   - Auto-select if single instance
3. Implement widget renderers:
   - `renderScalarWidget(widget)` - for scalars
   - `renderTableWidget(widget)` - for tables
   - `renderStatusField(field, instance)` - for status displays
   - `renderMetricChart(chart, instance)` - for charts
4. Implement query template engine:
   - Replace `{{device}}` with device name
   - Replace `{{instance}}` with selected instance
5. Remove deprecated code:
   - Remove pattern-based `isTableColumn()`
   - Remove `fetchTableMetadata()` in favor of dashboard spec
   - Remove manual categorization logic

**Deliverables**:
- Spec-driven dashboard rendering
- Working instance selectors
- Correct chart generation
- Remove 500+ lines of fragile code

### Phase 6: Testing & Validation (2-3 days)

**Tasks**:
1. End-to-end testing:
   - Test IF-MIB dashboard (interface metrics)
   - Test CISCO-BGP4-MIB dashboard (BGP peer metrics)
   - Test SNMPv2-MIB dashboard (system scalars)
   - Test custom vendor MIBs
2. Cross-browser testing:
   - Chrome, Firefox, Safari
   - Test chart rendering
   - Test WebSocket updates
3. Performance testing:
   - Dashboard load time
   - Instance selector population
   - Chart update frequency
4. Edge case testing:
   - Empty instance lists
   - Missing enum values
   - Augmented tables
   - Multiple index columns
5. Integration testing:
   - Full device → MIB → dashboard flow
   - Polling → Prometheus → WebSocket → UI flow

**Deliverables**:
- Test plan document
- 90%+ code coverage
- Performance benchmarks
- Bug fixes

### Phase 7: Documentation (1 day)

**Tasks**:
1. Update architecture documentation
2. Create API documentation:
   - Dashboard spec endpoint
   - Enhanced tables endpoint
3. Create developer guide:
   - How to add new MIB support
   - How dashboard generation works
   - How to customize widgets
4. Create troubleshooting guide:
   - Common issues
   - Debug logging
   - Validation tools

**Deliverables**:
- Updated `docs/architecture/`
- API documentation in `docs/api/`
- Developer guide in `docs/development/`

---

## Success Criteria

### Functional Requirements
- [ ] Clicking CISCO-BGP4-MIB tab shows BGP objects (not IF-MIB objects)
- [ ] Instance selector populates with available interfaces/instances
- [ ] Index columns (ifIndex) are NOT charted
- [ ] Status columns (ifDescr, ifOperStatus) display as text with enum values
- [ ] Metric columns (ifInOctets, ifOutOctets) display as time-series charts
- [ ] Charts show correct data for selected instance
- [ ] Dashboard works for IF-MIB, CISCO-BGP4-MIB, SNMPv2-MIB

### Non-Functional Requirements
- [ ] Dashboard loads in < 2 seconds
- [ ] Instance selector populates in < 1 second
- [ ] Chart updates occur within 500ms of WebSocket message
- [ ] Code coverage > 90% for new components
- [ ] No console errors or warnings
- [ ] Clean separation of concerns (parser, storage, generator, API, frontend)

### Technical Debt Reduction
- [ ] Remove pattern-based table detection
- [ ] Remove fragile OID string parsing
- [ ] Remove manual categorization logic
- [ ] Reduce frontend code by ~500 lines
- [ ] Improve maintainability with clear contracts

---

## Risk Assessment

### High Risk
1. **gosmi API Limitations**: gosmi may not expose all needed metadata
   - **Mitigation**: Fallback to heuristics for missing data, manual overrides

2. **Prometheus Label Cardinality**: Too many instances could cause performance issues
   - **Mitigation**: Pagination for instance selector, warn on high cardinality

### Medium Risk
3. **Enum Value Extraction**: Not all MIBs define enum names
   - **Mitigation**: Display numeric values if names missing, allow manual mapping

4. **Backwards Compatibility**: Existing dashboards may break
   - **Mitigation**: Feature flag for new architecture, phased rollout

### Low Risk
5. **Frontend Complexity**: Spec-based rendering may introduce bugs
   - **Mitigation**: Comprehensive testing, gradual migration

---

## Alternatives Considered

### Alternative 1: Pattern-Based Table Detection (Current Approach)
**Pros**: Simple, no parser changes
**Cons**: Fragile, fails on many MIBs, no semantic understanding
**Decision**: Reject - already proven inadequate

### Alternative 2: Frontend-Only Fix
**Pros**: No backend changes
**Cons**: Doesn't fix root cause, still relies on broken metadata
**Decision**: Reject - band-aid solution

### Alternative 3: Manual MIB Definitions
**Pros**: Total control, accurate dashboards
**Cons**: Not scalable, high maintenance, defeats purpose of parsing
**Decision**: Reject for general solution, keep as override mechanism

### Alternative 4: Proposed Architecture (Recommended)
**Pros**: Robust, scalable, maintainable, semantic understanding
**Cons**: Significant refactoring effort
**Decision**: Accept - only sustainable long-term solution

---

## Conclusion

The current architecture has fundamental flaws that cannot be fixed with incremental changes. The proposed redesign addresses all critical issues by:

1. Preserving and enriching MIB structure metadata from parsing
2. Implementing semantic understanding of object roles
3. Generating explicit dashboard specifications
4. Using contract-based APIs between layers
5. Simplifying frontend logic with spec-driven rendering

**Estimated Effort**: 10-15 days (2-3 weeks for 1 developer)

**Recommended Approach**: Implement in phases with feature flag, validate each phase before proceeding.

**Next Steps**:
1. Review and approve this design
2. Create detailed task breakdown for Phase 1
3. Set up feature flag for gradual rollout
4. Begin implementation with Phase 1 (Enhanced Parser)
