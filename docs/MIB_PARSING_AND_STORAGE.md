# MIB Parsing and Storage Implementation - Thorough Exploration

## Executive Summary

The go-netmon project implements a complete MIB management system using **gosmi** (Go bindings for libsmi) for parsing and **PebbleDB** for persistent storage. The system extracts SNMP OID metadata from MIB files, stores them in a key-value database, and provides APIs for querying and filtering based on device profiles.

## 1. Parser Implementation (`internal/mib/parser/parser.go`)

### 1.1 Overview
The parser is a wrapper around the **gosmi** library (Go SNMP MIB library). It:
- Initializes gosmi with MIB search paths
- Loads MIB modules and parses all nodes
- Extracts detailed metadata about OIDs
- Identifies table structures and relationships

### 1.2 Key Components

#### Parser Struct
```go
type Parser struct {
    paths []string  // MIB search paths
}
```

#### Core Methods

**Init()**
- Initializes gosmi library
- Registers all MIB search paths
- Called once during application startup

**LoadModule(moduleName string)**
- Loads a MIB module by name
- Returns `ModuleInfo` with:
  - Module name, path, organization
  - OID count
  - Import dependencies
  - Contact information and description

**ParseModule(moduleName string)**
- Parses all nodes in a module
- Returns array of `OIDMetadata` structures
- Handles all object types (scalars, tables, columns, etc.)

**parseNode(node gosmi.SmiNode)**
- Core parsing logic for individual nodes
- Extracts the following from each node:
  - **OID**: Numeric OID (e.g., "1.3.6.1.2.1.1.1")
  - **Name**: Symbolic name (e.g., "sysDescr")
  - **Module**: Source MIB module
  - **Type**: SNMP type (Counter32, Gauge32, OctetString, etc.)
  - **Access**: read-only, read-write, not-accessible
  - **Status**: current, deprecated, obsolete, mandatory, optional
  - **Description**: Human-readable description
  - **NodeKind**: Type classification (Scalar, Table, Row, Column)
  - **Parent**: Parent OID reference
  - **Index/Augments**: Table index information

### 1.3 Type Mapping

The parser converts gosmi types to readable strings:

```
BaseType -> String Representation
================================================
BaseTypeInteger32 -> "Integer32"
BaseTypeInteger64 -> "Integer64"
BaseTypeUnsigned32 -> "Unsigned32"
BaseTypeOctetString -> "OctetString"
BaseTypeObjectIdentifier -> "ObjectIdentifier"
BaseTypeEnum -> "Integer"
BaseTypeBits -> "Bits"
```

### 1.4 Access and Status Mapping

```
Access Conversion:
- AccessNotAccessible -> "not-accessible"
- AccessReadOnly -> "read-only"
- AccessReadWrite -> "read-write"
- AccessNotify -> "accessible-for-notify"

Status Conversion:
- StatusCurrent -> "current"
- StatusDeprecated -> "deprecated"
- StatusObsolete -> "obsolete"
- StatusMandatory -> "mandatory"
- StatusOptional -> "optional"
```

### 1.5 Table Detection

**GetTableInfo(moduleName string)**
- Identifies all table nodes in a module
- Returns `TableInfo` structures with:
  - **OID**: Table OID (e.g., "1.3.6.1.2.1.2.2" for ifTable)
  - **Name**: Table name (e.g., "ifTable")
  - **EntryOID**: Row entry OID (e.g., "1.3.6.1.2.1.2.2.1")
  - **Columns**: Array of column OIDs
  - **Indexes**: Index column names

### 1.6 Information Loss During Parsing

**What IS preserved:**
- Numeric OID
- Object names and descriptions
- Type information
- Access and status
- Table structure
- Index information

**What IS NOT fully preserved (or requires inference):**
- Range constraints (e.g., 0..255)
- Default values
- Enumeration mappings (only type name preserved)
- Complex syntax definitions
- Units (if specified in MIB)
- Textual conventions details

## 2. Storage Implementation (`internal/mib/storage/storage.go`)

### 2.1 Overview

Uses **PebbleDB** (a RocksDB-based Go key-value store) for persistent, fast storage of MIB metadata. Provides both retrieval and filtering capabilities.

### 2.2 Storage Architecture

#### Database Initialization
```go
type Storage struct {
    db   *pebble.DB      // PebbleDB instance
    path string           // Filesystem path
    mu   sync.RWMutex    // Concurrency control
}

// Configuration
Cache:        64MB
MaxOpenFiles: 500
```

### 2.3 Key Schema Design

Six prefix categories organize different data types:

```
Prefix System
====================
PrefixOID           = "oid:"      -> Stores OID metadata
PrefixModule        = "mod:"      -> Stores module info
PrefixTable         = "tbl:"      -> Stores table metadata
PrefixName          = "name:"     -> Index for reverse lookups
PrefixModuleOID     = "midx:"     -> Index of a module's OID keys
PrefixModuleIndexed = "midxdone:" -> Marks a module's index as built
```

The module index prefixes deliberately do not begin with `mod`. A prefix such as
`modoid:` sorts inside the `["mod:", "mod;")` range that `ListModules` and
`Count` scan, so tens of thousands of index entries would be walked as if they
were module records.

#### Example Keys
```
OID Storage:
- Key: "oid:1.3.6.1.2.1.1.1"
- Value: JSON-serialized OIDMetadata

Name Index:
- Key: "name:SNMPv2-MIB:sysDescr"
- Value: "1.3.6.1.2.1.1.1" (points to OID key)

Module OID Index:
- Key: "midx:SNMPv2-MIB:1.3.6.1.2.1.1.1"
- Value: "1.3.6.1.2.1.1.1" (points to OID key)

Module Indexed Marker:
- Key: "midxdone:SNMPv2-MIB"
- Value: presence only

Module Storage:
- Key: "mod:SNMPv2-MIB"
- Value: JSON-serialized ModuleInfo

Table Storage:
- Key: "tbl:IF-MIB:ifTable"
- Value: JSON-serialized TableInfo
```

### 2.4 Core Operations

#### StoreOID(meta *OIDMetadata) error
- Stores OID by numeric OID or module:name
- Creates three entries in one atomic batch:
  1. Primary: OID -> metadata
  2. Secondary: name -> OID (for reverse lookup)
  3. Module index: module + OID key -> OID key
- Handles OIDs without numeric values (groups, notifications)
- `DeleteOID` removes all three, so the index never goes stale

#### GetOID(oid string) *OIDMetadata
- O(1) lookup by numeric OID
- Returns complete OIDMetadata structure

#### GetOIDByName(module, name string) *OIDMetadata
- Lookup: module:name -> OID -> metadata
- Two-step retrieval process
- Supports symbolic name queries

#### ListOIDs(oidPrefix string) []*OIDMetadata
- Prefix-based range query
- Efficient iteration using PebbleDB's range scanning
- Example: ListOIDs("1.3.6.1.2.1.1") returns all system OIDs
- Upper bound: oidPrefix + "~" (character after digits)

#### ListModuleOIDs(module string) []*OIDMetadata
- Reads one module's objects through the module index
- Cost is O(module), not O(store): a 200-object MIB costs the same whether the
  store holds 2,000 OIDs or 29,000
- Unknown module -> empty result; empty module name -> `*ModuleRequiredError`
- Filtering `ListOIDs("")` by module instead deserializes the whole store on
  every request, which is what pushed MIB imports and `GET /api/mibs/{id}` past
  their timeouts at 170 modules

##### Index migration

A store written before the index exists holds OID records with no `midx:`
entries. The first module lookup against such a store falls back to a full scan
and backfills the index from it. Because the scan has already deserialized every
record, it indexes *every* module it sees, not just the one requested, and marks
the requested module even when that module has no objects. One scan therefore
repairs the whole store, and no re-import is needed after an upgrade.

#### GetPollableOIDs(module string) []*OIDMetadata
- Uses `ListModuleOIDs` for a named module, `ListOIDs("")` for an empty one
- Keeps only OIDs where `IsPollable()` returns true
- Returns only OIDs suitable for polling

### 2.5 Concurrency Model

```go
// Read operations
s.mu.RLock()
defer s.mu.RUnlock()

// Write operations  
s.mu.Lock()
defer s.mu.Unlock()

// Pebble handles internal concurrency for multiple readers
```

### 2.6 Query Patterns

The storage supports three main query patterns:

```
1. Direct OID Lookup: "1.3.6.1.2.1.1.1" -> OIDMetadata

2. Name-based Lookup: ("SNMPv2-MIB", "sysDescr") -> OIDMetadata

3. Range/Prefix Queries: "1.3.6.1.2.1" -> [OIDMetadata...]

4. Module-scoped Queries: "IF-MIB" -> [OIDMetadata...]
```

## 3. Object Retrieval and Categorization

### 3.1 OIDMetadata.IsPollable() Decision Logic

An OID is pollable if ALL conditions are met:

```go
func (o *OIDMetadata) IsPollable() bool {
    // 1. Must be readable
    if o.Access != "read-only" && o.Access != "read-write" {
        return false
    }
    
    // 2. Must be current status (not deprecated/obsolete)
    if o.Status != "current" {
        return false
    }
    
    // 3. Tables and entries aren't directly pollable
    //    (columns of tables are pollable)
    if o.IsTable || o.IsTableEntry {
        return false
    }
    
    // 4. Not-accessible objects are excluded
    if o.Access == "not-accessible" {
        return false
    }
    
    return true
}
```

### 3.2 Type Classification

Each OID is classified by type:

**IsCounter()**: Counter32, Counter64, Counter
**IsGauge()**: Gauge32, Gauge, Integer32, Integer, Unsigned32
**IsString()**: OctetString, DisplayString

This classification is used by Prometheus exporter for metric type mapping.

### 3.3 Object Categorization

```
NodeType Flags
==============
IsScalar      - Standalone OID (e.g., sysDescr)
IsTable       - Table container (e.g., ifTable)
IsTableEntry  - Table row (e.g., ifEntry)
IsColumn      - Column within a table (e.g., ifDescr within ifEntry)
IsIndex       - Index column (implicit via Index array)
```

## 4. Data Flow: MIB File → API

### 4.1 Complete Flow Chain

```
MIB File (IF-MIB.txt)
    ↓
Parser.ParseModule("IF-MIB")
    ↓ (using gosmi)
Extract Nodes → OIDMetadata array
    ↓
Storage.StoreOID() → PebbleDB
Storage.StoreTable() → PebbleDB
Storage.StoreModule() → PebbleDB
    ↓
Query Layer (Selector)
    ↓
API Endpoints (webui/api/mib_handler.go)
    ↓
Frontend (Web UI)
```

### 4.2 Loader Integration (`internal/mib/loader/loader.go`)

The Loader orchestrates the entire MIB loading process:

```go
func (l *Loader) LoadModule(moduleName string) error {
    1. Check if already loaded (delete if so)
    2. Parse module with Parser.LoadModule()
    3. Parse all OIDs with Parser.ParseModule()
    4. Store module info
    5. Store each OID
    6. Extract and store table info
    7. Log statistics
}
```

### 4.3 Selector-based Filtering (`internal/mib/selector/selector.go`)

The Selector applies device profiles to select relevant OIDs:

```go
func (s *Selector) SelectOIDs(profile *DeviceProfile) []*OIDMetadata {
    1. Get all OIDs from modules specified in profile
    2. Apply include/exclude patterns (regex)
    3. Filter by table inclusion preference
    4. Apply max OID limit
    5. Return filtered OIDs for polling
}
```

### 4.4 API Endpoint Integration

**GET /api/mibs/{id}** (`handleGetStoredMIB`)
- Retrieves MIB from database
- Enriches with OID details from storage
- Filters by module name
- Returns MIBObject array with pollable count

**GET /api/mibs/{id}/tables** (`handleGetMIBTables`)
- Detects tables by analyzing OID patterns
- Uses pattern: parent ends in `.1` indicates column
- Identifies table, entry, and column OIDs
- Returns TableMetadata with columns

**POST /api/mibs/import** (`handleImportMIB`)
- Accepts file upload
- Saves to uploaded MIBs directory
- Uses loader's parser (with all paths configured)
- Stores in database via storage.CreateMIB()

## 5. Test Data Analysis

### 5.1 MIB Files in Repository

**Standard MIBs** (`/mibs/standard/`)
```
SNMPv2-MIB.txt (854 lines)
  - Core system objects (sysDescr, sysUpTime, etc.)
  - Module: SNMPv2-MIB
  - Well-established baseline

IF-MIB.txt (1814 lines)
  - Interface table definitions
  - Module: IF-MIB
  - Contains: ifTable, ifXTable (extended)
  - Key objects: ifIndex, ifDescr, ifSpeed, ifInOctets, ifOutOctets

IP-MIB.txt (4993 lines)
  - IP routing and addressing
  - Large MIB with many objects
  
TCP-MIB.txt (785 lines)
- TCP protocol statistics

UDP-MIB.txt (549 lines)
- UDP protocol statistics

SNMPv2-SMI.txt (344 lines)
- SNMP meta definitions

SNMPv2-TC.txt (772 lines)
- Textual conventions

SNMPv2-CONF.txt (322 lines)
- Compliance definitions
```

### 5.2 Cisco MIBs (`/mibs/cisco/`)

```
CISCO-PROCESS-MIB.my (107,423 bytes)
  - CPU and process monitoring
  - Tables: cpmCPUTotalTable, cpmProcessTable
  - Key for Cisco device polling

CISCO-MEMORY-POOL-MIB.my (16,214 bytes)
  - Memory pool statistics
  - Essential for Cisco monitoring

CISCO-SMI.my (16,811 bytes)
  - Cisco-specific SNMP definitions

CISCO-TC.my (102,922 bytes)
  - Cisco textual conventions
```

### 5.3 IF-MIB Structure Example

**Scalar Objects:**
```
ifNumber (1.3.6.1.2.1.2.1.0)
  - Type: Integer
  - Access: read-only
  - Description: Number of interfaces
```

**Table Structure:**
```
ifTable (1.3.6.1.2.1.2.2)
├── ifEntry (1.3.6.1.2.1.2.2.1) [Row]
│   ├── ifIndex (1.3.6.1.2.1.2.2.1.1) [Column, Index]
│   ├── ifDescr (1.3.6.1.2.1.2.2.1.2) [Column]
│   ├── ifType (1.3.6.1.2.1.2.2.1.3) [Column]
│   ├── ifMtu (1.3.6.1.2.1.2.2.1.4) [Column]
│   ├── ifSpeed (1.3.6.1.2.1.2.2.1.5) [Column]
│   ├── ifPhysAddress (1.3.6.1.2.1.2.2.1.6) [Column]
│   ├── ifAdminStatus (1.3.6.1.2.1.2.2.1.7) [Column]
│   ├── ifOperStatus (1.3.6.1.2.1.2.2.1.8) [Column]
│   ├── ifLastChange (1.3.6.1.2.1.2.2.1.9) [Column]
│   ├── ifInOctets (1.3.6.1.2.1.2.2.1.10) [Column, Counter32]
│   ├── ifInUcastPkts (1.3.6.1.2.1.2.2.1.11) [Column, Counter32]
│   ├── ifInDiscards (1.3.6.1.2.1.2.2.1.13) [Column, Counter32]
│   ├── ifInErrors (1.3.6.1.2.1.2.2.1.14) [Column, Counter32]
│   ├── ifOutOctets (1.3.6.1.2.1.2.2.1.16) [Column, Counter32]
│   └── ... (more columns)
```

Polling ifTable means:
1. Walk ifIndex to find all interfaces (1, 2, 3, ...)
2. For each index, poll all pollable columns
3. Example: 1.3.6.1.2.1.2.2.1.10.1, .10.2, .10.3 (ifInOctets for each interface)

## 6. Poller Integration

### 6.1 How Poller Uses MIB Data

```go
// From internal/poller/poller.go

func (p *Poller) startWorker(ctx context.Context, device *config.Device) {
    if p.mibLoader != nil {
        // Get storage from loader
        storageInterface := p.mibLoader.GetStorage()
        mibStorage := storageInterface.(*storage.Storage)
        
        // Get all OIDs
        allOIDs := mibStorage.ListOIDs("")
        
        // Filter by device modules
        for _, oid := range allOIDs {
            if contains(device.Modules, oid.Module) && oid.IsPollable() {
                oids = append(oids, oid)
            }
        }
        
        // Use oids for SNMP polling
        results := client.GetMultiple(oidStrings)
    }
}
```

### 6.2 Profile-based OID Selection

```
Device Config
├── Profile: "cisco-ios"
│   └── Modules: ["SNMPv2-MIB", "IF-MIB", "CISCO-PROCESS-MIB"]
│
└── Query Flow:
    1. Selector.SelectOIDs(profile)
    2. Get all OIDs from specified modules
    3. Apply exclude patterns (deprecated, obsolete)
    4. Filter IsTable and IsTableEntry if IncludeTables=false
    5. Return []*OIDMetadata for polling
```

## 7. Built-in Profiles

The system includes predefined profiles for different device types:

```
default
  - Modules: SNMPv2-MIB, IF-MIB
  - Basic monitoring

cisco-ios
  - Modules: SNMPv2-MIB, IF-MIB, IP-MIB, CISCO-PROCESS-MIB, CISCO-MEMORY-POOL-MIB
  - CPU, memory, interface monitoring
  - Excludes deprecated/obsolete OIDs

cisco-ios-with-tables
  - Same as above
  - IncludeTables: true (includes full interface table walks)

cisco-nexus
  - Cisco NX-OS optimized

juniper-mx
  - Juniper MX router optimized

arista-eos
  - Arista switch optimized

generic-router
  - Vendor-neutral router profile

generic-switch
  - Vendor-neutral switch profile

interfaces-only
  - IF-MIB only (interface monitoring)
```

## 8. Data Structures

### 8.1 OIDMetadata (Full Definition)

```go
type OIDMetadata struct {
    OID          string            // "1.3.6.1.2.1.1.1"
    Name         string            // "sysDescr"
    Module       string            // "SNMPv2-MIB"
    Type         string            // "OctetString"
    Access       string            // "read-only", "read-write", "not-accessible"
    Status       string            // "current", "deprecated", "obsolete"
    Description  string            // Human-readable text
    Units        string            // "milliseconds" (if specified)
    Syntax       string            // Type name
    Parent       string            // Parent OID
    Index        []string          // Index column names (for rows)
    Augments     string            // Augmented row name (for extended tables)
    IsTable      bool              // True if table container
    IsTableEntry bool              // True if table row
    IsColumn     bool              // True if table column
    IsScalar     bool              // True if standalone scalar
    Pollable     bool              // Computed: should be polled?
    CreatedAt    time.Time
    UpdatedAt    time.Time
    Tags         map[string]string // Custom tags
}
```

### 8.2 ModuleInfo

```go
type ModuleInfo struct {
    Name         string            // "SNMPv2-MIB"
    Path         string            // File path
    Organization string            // "IETF"
    ContactInfo  string
    Description  string
    Revision     string
    Imports      []string          // Dependencies
    OIDCount     int               // Total OIDs in module
    LoadedAt     time.Time
    Tags         map[string]string
}
```

### 8.3 TableInfo

```go
type TableInfo struct {
    OID      string   // "1.3.6.1.2.1.2.2"
    Name     string   // "ifTable"
    Module   string   // "IF-MIB"
    Desc     string   // Description
    Indexes  []string // ["ifIndex"]
    Columns  []string // ["1.3.6.1.2.1.2.2.1.1", "1.3.6.1.2.1.2.2.1.2", ...]
    EntryOID string   // "1.3.6.1.2.1.2.2.1"
}
```

## 9. Query Performance

### 9.1 Lookup Complexity

```
Operation              Complexity  Notes
==========================================================
GetOID(oid)           O(1)        Direct key lookup
GetOIDByName()        O(1)        Name index → OID → metadata
ListOIDs(prefix)      O(n)        Range scan, n = matching OIDs
ListModuleOIDs(mod)   O(k)        Module index, k = module's OIDs
GetPollableOIDs(mod)  O(k)        Module index + IsPollable filter
GetPollableOIDs("")   O(n)        Full scan + filter (every module)
ListModules()         O(m)        Range scan, m = modules
```

`BenchmarkModuleLookup` in `internal/mib/storage` measures the difference for a
200-object module (Intel Core Ultra 9 285K):

```
store size   indexed lookup   full scan + filter
2,000 OIDs        0.75 ms            6.0 ms
29,000 OIDs       0.83 ms           79.3 ms
```

The indexed lookup is flat across store sizes; the scan grows with the store.

### 9.2 Practical Performance

- **SNMPv2-MIB**: ~200 OIDs, loads < 10ms
- **IF-MIB**: ~100 OIDs, loads < 10ms
- **IP-MIB**: ~300 OIDs, loads < 20ms
- **CISCO-PROCESS-MIB**: ~2000 OIDs, loads < 50ms
- **Full storage scan**: <100ms for 10,000 OIDs
- **Prefix query (1.3.6.1.2.1)**: <10ms for 100 matching OIDs

## 10. Limitations and Missing Information

### 10.1 Information Now Captured

The parser reads every one of these from the **resolved** gosmi type
(`node.SmiType`), never from `node.SmiType.GetRaw()`. `GetRaw` returns the type
exactly as written in the SYNTAX clause, which for a subtyped textual convention
such as `DisplayString (SIZE (0..255))` is an anonymous type carrying no name,
no `UNITS` and no `DISPLAY-HINT`. Reading the raw type silently discarded all
three.

1. **Enumeration Values**: `OIDMetadata.EnumValues` maps each value to its label
   - `ifOperStatus` → `{1: up, 2: down, 3: testing, …}`
2. **Range Constraints**: `RangeMin` / `RangeMax`, collapsed to the outer bounds
   when a type declares several disjoint ranges
3. **UNITS**: `OIDMetadata.Units`, taken from the OBJECT-TYPE's `UNITS` clause
   - `cpmCPUTotal5minRev` → `percent`, `cpmCPUMemoryUsed` → `kilo-bytes`
4. **DISPLAY-HINT**: `OIDMetadata.DisplayHint`, resolved through the textual
   convention — `ifPhysAddress` → `1x:`, `ifDescr` → `255a`
5. **Textual Convention Names**: `Type` / `Syntax` keep the declared TC name
   rather than degrading to the base type — `DisplayString`, not `OctetString`
6. **Table Index Types**: `IndexColumns []IndexColumn` records each index
   column's name, OID, type, base type, `IMPLIED` flag and fixed length, so
   composite and string-valued indexes decode correctly instead of being
   guessed from substrings of the column name

`internal/mib.FormatValue` consumes this metadata to render values as text, and
is shared by the CLI, the web UI and the metrics exporter. See `docs/cli.md`.

### 10.2 Information Still Not Captured

1. **Bit Strings**: Bit positions/names not extracted
   - Example: `StatusBits ::= {enabled(0), disabled(1)}`
2. **Default Values**: `DEFVAL` is not extracted or stored
3. **Import Dependencies**: Stored as strings; the graph is not resolved or
   validated (gosmi loads IMPORTS transitively via its search path)
4. **Textual Convention Definitions**: TC names and their display hints are
   captured, but the TC definitions themselves are not stored separately
5. **Augmented Table Info**: `Augments` is stored but the relationship is not
   fully modeled

### 10.3 Edge Cases Handled

1. **OIDs without numeric values**: Stored as module:name key
2. **Sparse tables**: No special handling (relies on SNMP walk behavior)
3. **Table augmentation**: Stored in OIDMetadata.Augments field
4. **Subtree queries**: `ListOIDs` matches whole sub-identifiers, so the prefix
   `1.3.6.1.2.1.2` selects that subtree without also matching `1.3.6.1.2.1.20`
5. **Unscoped queries**: `GetPollableOIDs("")` means every module, mirroring
   `ListOIDs("")`

### 10.4 Potential Improvements

1. Extract BITS positions and names
2. Capture DEFVAL default values
3. Build a full MIB dependency graph
4. Index per-module OIDs so lookups are O(module) rather than O(store) — import
   time currently grows with the size of the store
5. Implement OID tree visualization

## 11. Summary Table: IF-MIB vs CISCO-PROCESS-MIB

```
Characteristic          IF-MIB              CISCO-PROCESS-MIB
==============================================================
Size (lines)           1814                107,423
Typical OID count      ~100                ~2000
Primary purpose        Interface mgmt      CPU/Process stats
Table types            2 (ifTable, ifXTable) 6+ tables
Pollable objects       ~80-90%             ~70-80%
Module dependency      SNMPv2-MIB          SNMPv2-MIB, CISCO-SMI, CISCO-TC
Vendor-specific        No                  Yes (Cisco)
Deprecated ratio       Low                 High (many revised objects)
Real-world usage       Universal           Cisco-only
```

## Conclusion

The MIB parsing and storage system is a well-architected pipeline that:
1. **Reliably extracts** OID metadata from MIB files using gosmi
2. **Efficiently stores** data in PebbleDB with prefix-based indexing
3. **Flexibly queries** data through multiple access patterns
4. **Intelligently filters** OIDs for polling based on profiles
5. **Integrates seamlessly** with the poller and API layers

The main trade-offs are:
- Simplified type system (loses enumeration details)
- No complex constraint storage (ranges, patterns)
- Flat OID representation (no tree structure inference)

These are reasonable sacrifices for the goal of practical SNMP monitoring, where the key information (OID, name, type, access, status) is captured and the system performs well in production.
