# MIB Dashboard Architecture Diagrams

> **Status: ARCHIVE** · Scope: go-netmon
>
> Diagrams of a redesign proposal that was only partly built; not maintained.
> The "proposed" diagrams show a dashboard spec generator that does not exist.
> Dashboards are now built from the metrics a device reports in Prometheus
> joined with the poller's metric catalog. See
> [mib-dashboard-redesign.md](mib-dashboard-redesign.md) for what was built,
> [system-architecture.md](system-architecture.md) and
> [../dashboard-generation.md](../dashboard-generation.md).

## Current Architecture (Broken)

```
┌─────────────────────────────────────────────────────────────────────────┐
│                              MIB File                                   │
└──────────────────────────────┬──────────────────────────────────────────┘
                               │
                               ▼
                    ┌──────────────────────┐
                    │   Parser (gosmi)     │
                    │  - Extracts metadata │
                    │  - Sets flags        │
                    └──────────┬───────────┘
                               │ Rich metadata extracted
                               │ but not fully utilized
                               ▼
                    ┌──────────────────────┐
                    │   Storage (PebbleDB) │
                    │  - Stores flat OIDs  │
                    │  - Loses hierarchy   │
                    └──────────┬───────────┘
                               │
                               ▼
                    ┌──────────────────────┐
                    │   DB (SQLite)        │
                    │  - MIB records       │
                    │  - Module names      │
                    └──────────┬───────────┘
                               │
                               ▼
                    ┌──────────────────────────────┐
                    │  API Handler                 │
                    │  /api/mibs/{id}             │
                    │  - Returns ALL objects       │
                    │  - Weak module filtering     │
                    │                              │
                    │  /api/mibs/{id}/tables       │
                    │  - Pattern-based detection   │ ⚠️ FRAGILE
                    │  - OID string parsing        │
                    │  - Arbitrary thresholds      │
                    └──────────┬───────────────────┘
                               │
                               ▼
           ┌───────────────────────────────────────────┐
           │         Frontend (app.js)                  │
           │                                            │
           │  fetchTableMetadata()                      │
           │    ↓                                       │
           │  isTableColumn(oid, mibId)                 │ ⚠️ FAILS
           │    - Depends on broken table detection    │
           │    ↓                                       │
           │  setupInstanceSelectors()                  │
           │    - No table metadata found              │ ⚠️ EMPTY
           │    - Selector stays empty                 │
           │    ↓                                       │
           │  loadMibCharts()                           │
           │    - Gets mixed objects from all MIBs     │ ⚠️ WRONG DATA
           │    - Weak isStatusObject() heuristic      │
           │    - Charts everything else               │ ⚠️ CHARTS ifIndex!
           │    ↓                                       │
           │  Renders broken dashboard                  │
           └────────────────────────────────────────────┘

PROBLEMS:
  ⚠️  Pattern-based table detection fails (line too short, wrong structure)
  ⚠️  Instance selector not populated (no table metadata)
  ⚠️  Wrong objects displayed (module filtering broken)
  ⚠️  Index columns charted (no semantic understanding)
```

## Proposed Architecture (Robust)

```
┌─────────────────────────────────────────────────────────────────────────┐
│                              MIB File                                   │
└──────────────────────────────┬──────────────────────────────────────────┘
                               │
                               ▼
                    ┌─────────────────────────────────┐
                    │   Enhanced Parser (gosmi)       │
                    │  ✓ Extract table hierarchies    │
                    │  ✓ Identify index columns       │
                    │  ✓ Extract enum values          │
                    │  ✓ Get display hints            │
                    │  ✓ Classify object roles        │
                    └──────────┬──────────────────────┘
                               │ OIDMetadata with:
                               │ - ObjectClass
                               │ - IsIndex flag
                               │ - TableOID/EntryOID
                               │ - EnumValues
                               │ - IsChartable
                               ▼
                    ┌─────────────────────────────────┐
                    │   Enriched Storage (PebbleDB)   │
                    │  ✓ Store structured metadata    │
                    │  ✓ Index by table/entry/column  │
                    │  ✓ Maintain hierarchies         │
                    │  ✓ Efficient table queries      │
                    └──────────┬──────────────────────┘
                               │
                               ▼
                    ┌─────────────────────────────────┐
                    │   Dashboard Generator (NEW)     │
                    │  ✓ Categorize by semantic role  │
                    │  ✓ Generate widget specs        │
                    │  ✓ Create query templates       │
                    │  ✓ Define instance selectors    │
                    └──────────┬──────────────────────┘
                               │ DashboardSpec:
                               │ - ScalarWidgets
                               │ - TableWidgets
                               │   - InstanceSelector
                               │   - StatusFields
                               │   - MetricCharts
                               ▼
           ┌────────────────────────────────────────────────┐
           │  API Handler                                   │
           │                                                │
           │  GET /api/devices/{id}/mibs/{name}/dashboard   │
           │    ✓ Returns DashboardSpec                     │
           │    ✓ Includes query templates                  │
           │    ✓ Includes enum mappings                    │
           │    ✓ Includes rendering hints                  │
           │                                                │
           │  GET /api/mibs/{id}/tables (enhanced)          │
           │    ✓ Returns categorized columns               │
           │    ✓ Uses parser metadata (not patterns)       │
           └────────────────┬───────────────────────────────┘
                            │
                            ▼
           ┌──────────────────────────────────────────────┐
           │         Frontend (app.js - refactored)       │
           │                                              │
           │  loadDashboardSpec(deviceId, mibName)        │
           │    ✓ Fetches DashboardSpec                   │
           │    ↓                                         │
           │  renderDashboardFromSpec(spec)               │
           │    ✓ Renders scalars (if present)            │
           │    ✓ Renders table widgets                   │
           │    ↓                                         │
           │  renderTableWidget(widget)                   │
           │    ✓ Creates instance selector                │
           │    ✓ Uses instance_query_template            │
           │    ✓ Populates from Prometheus               │
           │    ↓                                         │
           │  onInstanceSelected(instance)                │
           │    ✓ Renders status fields                   │
           │      - Replaces {{instance}} in queries      │
           │      - Shows enum values                     │
           │    ✓ Renders metric charts                   │
           │      - Replaces {{instance}} in queries      │
           │      - Applies rate() for counters           │
           │    ↓                                         │
           │  Displays correct dashboard                  │
           └──────────────────────────────────────────────┘

BENEFITS:
  ✓  Semantic understanding (parser classifies objects)
  ✓  Instance selector populated (from spec query template)
  ✓  Correct objects displayed (spec generated per module)
  ✓  Only metrics charted (semantic classification)
  ✓  Status fields show enums (extracted from parser)
  ✓  Maintainable (clear contracts between layers)
```

## Data Flow Comparison

### Current Flow (Broken)

```
Step 1: User clicks "IF-MIB" tab
   │
   ├─> Frontend: loadMibCharts(deviceId, "IF-MIB")
   │
   ├─> Fetch: GET /api/mibs/{id}/tables
   │    │
   │    └─> Backend: detectTables(oids) ⚠️ PATTERN MATCHING
   │         - Checks if len(oidParts) >= 10  ⚠️ ARBITRARY
   │         - Checks if parent ends in ".1"  ⚠️ FRAGILE
   │         - Often fails to detect valid tables
   │         └─> Returns: { tables: [] } ⚠️ EMPTY
   │
   ├─> Frontend: tableMetadata[mibId] = { tables: [] }
   │
   ├─> Fetch: GET /api/mibs/{id}
   │    │
   │    └─> Backend: Filter by oid.Module == mib.Name
   │         - BUT: Module might be wrong in DB
   │         - OR: Returns objects from other MIBs ⚠️
   │         └─> Returns: { objects: [... mixed ...] }
   │
   ├─> Frontend: Filter pollable objects
   │    - Gets objects from multiple MIBs ⚠️
   │    - isStatusObject(type, name) ⚠️ WEAK HEURISTIC
   │      - Only checks type == "Integer" or "DisplayString"
   │      - Misses many status objects
   │    - Everything else → metricObjects ⚠️
   │      - Includes ifIndex (not a metric!)
   │
   ├─> Frontend: hasTableObjects = objects.some(isTableColumn)
   │    - isTableColumn() checks tableMetadata
   │    - tableMetadata.tables = [] ⚠️ EMPTY
   │    └─> Returns false (no table objects detected)
   │
   ├─> Frontend: No instance selector created ⚠️
   │
   └─> Frontend: Creates charts for ALL metricObjects
        - Charts ifIndex (value: 25) ⚠️ MEANINGLESS
        - Charts ifOperStatus (enum) ⚠️ WRONG VISUALIZATION
        - Shows IF-MIB objects in BGP tab ⚠️ WRONG MIB

Result: Broken dashboard
```

### Proposed Flow (Robust)

```
Step 1: User clicks "IF-MIB" tab
   │
   ├─> Frontend: loadDashboardSpec(deviceId, "IF-MIB")
   │
   ├─> Fetch: GET /api/devices/{deviceId}/mibs/IF-MIB/dashboard
   │    │
   │    └─> Backend: DashboardGenerator.Generate("IF-MIB")
   │         │
   │         ├─> Storage: GetModule("IF-MIB")
   │         │
   │         ├─> Storage: ListOIDs("", module="IF-MIB") ✓ ACCURATE
   │         │    - Filter by Module at storage level
   │         │    - Returns only IF-MIB objects
   │         │
   │         ├─> Categorize objects by ObjectClass:
   │         │    - ObjectClassScalar → ScalarWidget
   │         │    - ObjectClassIndexColumn → Skip (used for selector)
   │         │    - ObjectClassStatusColumn → StatusField
   │         │    - ObjectClassMetricColumn → MetricChart
   │         │
   │         ├─> Group by table:
   │         │    - ifTable:
   │         │      - Index: ifIndex (skip from display)
   │         │      - Status: ifDescr, ifType, ifOperStatus
   │         │      - Metrics: ifInOctets, ifOutOctets, ifInErrors
   │         │
   │         ├─> Generate query templates:
   │         │    - snmp_ifInOctets{device="{{device}}",ifindex="{{instance}}"}
   │         │
   │         └─> Return DashboardSpec:
   │              {
   │                "tables": [{
   │                  "instance_selector": {
   │                    "label_name": "ifindex",
   │                    "instance_query_template": "..."
   │                  },
   │                  "status_fields": [
   │                    {
   │                      "name": "ifDescr",
   │                      "query_template": "..."
   │                    },
   │                    {
   │                      "name": "ifOperStatus",
   │                      "enum_values": {"1": "up", "2": "down"},
   │                      "query_template": "..."
   │                    }
   │                  ],
   │                  "metric_charts": [
   │                    {
   │                      "name": "ifInOctets",
   │                      "chart_type": "line",
   │                      "query_template": "rate(...)"
   │                    }
   │                  ]
   │                }]
   │              }
   │
   ├─> Frontend: renderDashboardFromSpec(spec) ✓
   │    │
   │    ├─> For each table widget:
   │    │    │
   │    │    ├─> Create instance selector dropdown
   │    │    │
   │    │    ├─> Query Prometheus with instance_query_template
   │    │    │    - Replace {{device}} with device name
   │    │    │    └─> Get instances: [{ifindex: "1", ifname: "eth0"}, ...]
   │    │    │
   │    │    ├─> Populate dropdown: "eth0 (1)", "eth1 (2)", ...
   │    │    │
   │    │    ├─> Create status field placeholders
   │    │    │    - ifDescr: "Select an instance"
   │    │    │    - ifOperStatus: "Select an instance"
   │    │    │
   │    │    └─> Create chart containers (empty until instance selected)
   │    │
   │    └─> Wait for user to select instance
   │
Step 2: User selects instance "1" (eth0)
   │
   ├─> Frontend: onInstanceSelected("1")
   │
   ├─> For each status field:
   │    │
   │    ├─> Replace {{device}} and {{instance}} in query_template
   │    │    - snmp_ifDescr{device="router1",ifindex="1"}
   │    │
   │    ├─> Query Prometheus
   │    │    └─> Get value: "GigabitEthernet0/0"
   │    │
   │    ├─> Display in status field
   │    │    - ifDescr: "GigabitEthernet0/0" ✓
   │    │
   │    └─> For ifOperStatus:
   │         - Get value: 1
   │         - Lookup in enum_values: {"1": "up"}
   │         - Display: "up" ✓ HUMAN READABLE
   │
   ├─> For each metric chart:
   │    │
   │    ├─> Replace {{device}} and {{instance}} in query_template
   │    │    - rate(snmp_ifInOctets{device="router1",ifindex="1"}[5m])
   │    │
   │    ├─> Initialize chart (Plotly)
   │    │
   │    └─> Subscribe to WebSocket for updates ✓
   │
   └─> Display complete, correct dashboard ✓

Result: Working dashboard with correct data
```

## Object Classification Logic

```
┌──────────────────────────────────────────────────────────────────┐
│                      Parser Input (gosmi)                        │
│                                                                  │
│  Node: ifInOctets                                               │
│    OID: 1.3.6.1.2.1.2.2.1.10                                   │
│    Type: Counter32                                              │
│    Access: read-only                                            │
│    Status: current                                              │
│    NodeKind: Column                                             │
│    Parent: ifEntry (1.3.6.1.2.1.2.2.1)                        │
│    Entry Index: [ifIndex]                                       │
└────────────────────────┬─────────────────────────────────────────┘
                         │
                         ▼
         ┌───────────────────────────────────┐
         │  DetermineObjectClass()           │
         │                                   │
         │  if IsTable:                      │
         │    → ObjectClassTableNode         │
         │                                   │
         │  if IsTableEntry:                 │
         │    → ObjectClassEntryNode         │
         │                                   │
         │  if IsColumn:                     │
         │    if Name in Entry.Index:        │
         │      → ObjectClassIndexColumn     │ (ifIndex)
         │    else if IsCounter/IsGauge:     │
         │      → ObjectClassMetricColumn    │ (ifInOctets) ✓
         │    else if IsString/IsEnum:       │
         │      → ObjectClassStatusColumn    │ (ifDescr)
         │                                   │
         │  if IsScalar:                     │
         │    → ObjectClassScalar            │
         │                                   │
         │  else:                            │
         │    → ObjectClassUnknown           │
         └────────────┬──────────────────────┘
                      │
                      ▼
         ┌──────────────────────────────────┐
         │  Enhanced OIDMetadata             │
         │                                  │
         │  Name: ifInOctets                │
         │  OID: 1.3.6.1.2.1.2.2.1.10      │
         │  Module: IF-MIB                  │
         │  ObjectClass: MetricColumn ✓     │
         │  IsIndex: false ✓                │
         │  IsChartable: true ✓             │
         │  ChartType: line ✓               │
         │  TableOID: 1.3.6.1.2.1.2.2 ✓    │
         │  EntryOID: 1.3.6.1.2.1.2.2.1 ✓  │
         └──────────────────────────────────┘
```

## Storage Index Structure

```
Current (Flat):
┌─────────────────────────────────────────┐
│ PebbleDB                                │
│                                         │
│ Key: "oid:1.3.6.1.2.1.2.2.1.10"        │
│ Value: { OIDMetadata }                  │
│                                         │
│ Key: "name:IF-MIB:ifInOctets"          │
│ Value: "1.3.6.1.2.1.2.2.1.10"          │
└─────────────────────────────────────────┘

Problem: No table hierarchy, hard to query all columns

Proposed (Structured):
┌─────────────────────────────────────────┐
│ PebbleDB                                │
│                                         │
│ Key: "oid:1.3.6.1.2.1.2.2.1.10"        │
│ Value: { OIDMetadata with enrichments } │
│                                         │
│ Key: "name:IF-MIB:ifInOctets"          │
│ Value: "1.3.6.1.2.1.2.2.1.10"          │
│                                         │
│ Key: "table:IF-MIB:ifTable"            │  ← NEW
│ Value: { TableInfo with categorized     │
│          index/status/metric columns }  │
│                                         │
│ Key: "module:IF-MIB"                   │
│ Value: { ModuleInfo }                   │
└─────────────────────────────────────────┘

Benefit: Single query gets full table structure
```

## Query Template Engine

```
Template:
  rate({{metric}}{device="{{device}}",{{index_label}}="{{instance}}"}[5m])

Inputs:
  - object: ifInOctets  (OID 1.3.6.1.2.1.2.2.1.10)
  - index column: ifIndex
  - device: "router1"
  - instance: "1"

Step 1: Build metric name from the OBJECT name, never from the OID
  snmp_ + ifInOctets → snmp_ifInOctets       (promname.MetricName)

Step 2: Build the index label from the index column name
  ifIndex → ifindex                          (promname.IndexLabel)

Step 3: Replace placeholders
  {{device}} → "router1"
  {{instance}} → "1"

Result:
  rate(snmp_ifInOctets{device="router1",ifindex="1"}[5m])

For status objects (no rate):
  snmp_ifDescr{device="router1",ifindex="1"}

The server puts the metric name in the dashboard spec (widget "metric") because
only it knows the MIB object name. See docs/metric-naming.md.
```
