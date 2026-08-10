# MIB System Architecture - Visual Reference

## System Architecture Overview

```
┌─────────────────────────────────────────────────────────────────────┐
│                         go-netmon                                   │
└─────────────────────────────────────────────────────────────────────┘
                                 │
        ┌────────────────────────┼────────────────────────┐
        │                        │                        │
    ┌───────────┐          ┌──────────┐            ┌────────────┐
    │  Poller   │          │  WebUI   │            │ Exporter   │
    └─────┬─────┘          └────┬─────┘            └──────┬─────┘
          │                     │                        │
          └─────────────────┬───┴──────────┬─────────────┘
                            │              │
                    ┌───────▼──────┐  ┌────▼──────┐
                    │  API Layer   │  │ Dashboard │
                    └───────┬──────┘  └─────────┬─┘
                            │                  │
                    ┌───────▼──────────────────┴───────┐
                    │                                   │
              ┌─────▼──────┐  ┌──────────────┐  ┌──────▼─────┐
              │  Selector  │  │   Profiles   │  │   Loader   │
              └─────┬──────┘  └──────┬───────┘  └──────┬─────┘
                    │                │                │
                    └────────────────┼────────────────┘
                                     │
                        ┌────────────▼────────────┐
                        │    MIB Storage          │
                        │  (internal/mib/...)    │
                        └────────────┬────────────┘
                                     │
        ┌────────────────────────────┼────────────────────────┐
        │                            │                        │
   ┌────▼─────┐          ┌──────────▼──────┐         ┌───────▼──┐
   │  Parser  │          │  PebbleDB       │         │  Storage │
   │(gosmi)   │          │  (Key-Value DB) │         │ Instance │
   └────┬─────┘          └──────────┬──────┘         └──────────┘
        │                           │
        │         ┌─────────────────┘
        │         │
    ┌───▼─────────▼────┐
    │  MIB Search Paths │
    ├───────────────────┤
    │ /mibs/standard/   │
    │ /mibs/cisco/      │
    │ /mibs/vendor/     │
    │ /mibs/asr1001/    │
    └───────────────────┘
```

## MIB File → Storage Pipeline

```
┌────────────────────────────────────────────────────────────────┐
│ Step 1: File Loading                                           │
├────────────────────────────────────────────────────────────────┤
│                                                                │
│  IF-MIB.txt (1814 lines)                                       │
│      ↓                                                         │
│  gosmi.LoadModule("IF-MIB")                                    │
│      ↓                                                         │
│  Module parsed and dependencies resolved                       │
│                                                                │
└────────────────────────────────────────────────────────────────┘
                              ↓
┌────────────────────────────────────────────────────────────────┐
│ Step 2: Node Parsing                                           │
├────────────────────────────────────────────────────────────────┤
│                                                                │
│  For each gosmi.SmiNode:                                       │
│  • Extract OID (1.3.6.1.2.1.2.2.1.10)                          │
│  • Extract Name (ifInOctets)                                   │
│  • Extract Type (Counter32)                                    │
│  • Extract Access (read-only)                                  │
│  • Extract Status (current)                                    │
│  • Extract Description, Units, Parent, Index info              │
│  • Classify NodeKind (Scalar, Table, Row, Column)              │
│      ↓                                                         │
│  OIDMetadata struct created                                    │
│                                                                │
└────────────────────────────────────────────────────────────────┘
                              ↓
┌────────────────────────────────────────────────────────────────┐
│ Step 3: Storage                                                │
├────────────────────────────────────────────────────────────────┤
│                                                                │
│  Storage.StoreOID(meta) {                                      │
│    • JSON serialize OIDMetadata                                │
│    • Create primary key: "oid:1.3.6.1.2.1.2.2.1.10"           │
│    • Store metadata value in PebbleDB                          │
│    • Create reverse index: "name:IF-MIB:ifInOctets"           │
│    • Store OID reference "1.3.6.1.2.1.2.2.1.10"               │
│  }                                                             │
│      ↓                                                         │
│  PebbleDB contains indexed, searchable OID data                │
│                                                                │
└────────────────────────────────────────────────────────────────┘
                              ↓
┌────────────────────────────────────────────────────────────────┐
│ Step 4: Querying & Filtering                                   │
├────────────────────────────────────────────────────────────────┤
│                                                                │
│  Query Types:                                                  │
│  1. Direct: GetOID("1.3.6.1.2.1.2.2.1.10")                    │
│  2. Name:   GetOIDByName("IF-MIB", "ifInOctets")               │
│  3. Prefix: ListOIDs("1.3.6.1.2.1.2.2")                       │
│  4. Filter: GetPollableOIDs("IF-MIB")                          │
│  5. Profile: Selector.SelectOIDs(deviceProfile)               │
│      ↓                                                         │
│  Filtered OID list for polling                                │
│                                                                │
└────────────────────────────────────────────────────────────────┘
```

## OID Metadata Extraction Example

```
MIB Definition:
───────────────
ifInOctets OBJECT-TYPE
    SYNTAX      Counter32
    MAX-ACCESS  read-only
    STATUS      current
    DESCRIPTION "The total number of octets received on the interface"
    ::= { ifEntry 10 }

                     ↓ Parser.parseNode()

OIDMetadata Result:
──────────────────
{
  OID:          "1.3.6.1.2.1.2.2.1.10"
  Name:         "ifInOctets"
  Module:       "IF-MIB"
  Type:         "Counter32"
  Access:       "read-only"
  Status:       "current"
  Description:  "The total number of octets received on..."
  Units:        ""
  Syntax:       "Counter32"
  Parent:       "1.3.6.1.2.1.2.2.1"
  IsScalar:     false
  IsColumn:     true
  IsTable:      false
  IsTableEntry: false
  Pollable:     true (computed)
  CreatedAt:    2024-10-25T12:34:56Z
  UpdatedAt:    2024-10-25T12:34:56Z
  Tags:         {}
}
```

## Storage Key Schema

```
┌─────────────────────────────────────────────────────────────┐
│ PebbleDB Key Organization                                   │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│ OID Storage (Primary Index)                                 │
│ ├─ oid:1.3.6.1.2.1.1.1         → OIDMetadata JSON           │
│ ├─ oid:1.3.6.1.2.1.1.3         → OIDMetadata JSON           │
│ ├─ oid:1.3.6.1.2.1.2.2.1.1     → OIDMetadata JSON           │
│ ├─ oid:1.3.6.1.2.1.2.2.1.2     → OIDMetadata JSON           │
│ └─ ...                                                      │
│                                                             │
│ Name Index (Reverse Lookup)                                │
│ ├─ name:SNMPv2-MIB:sysDescr    → "1.3.6.1.2.1.1.1"        │
│ ├─ name:SNMPv2-MIB:sysUpTime   → "1.3.6.1.2.1.1.3"        │
│ ├─ name:IF-MIB:ifIndex         → "1.3.6.1.2.1.2.2.1.1"    │
│ ├─ name:IF-MIB:ifInOctets      → "1.3.6.1.2.1.2.2.1.10"   │
│ └─ ...                                                      │
│                                                             │
│ Module Storage                                              │
│ ├─ mod:SNMPv2-MIB              → ModuleInfo JSON            │
│ ├─ mod:IF-MIB                  → ModuleInfo JSON            │
│ ├─ mod:IP-MIB                  → ModuleInfo JSON            │
│ └─ ...                                                      │
│                                                             │
│ Table Storage                                               │
│ ├─ tbl:IF-MIB:ifTable          → TableInfo JSON             │
│ ├─ tbl:IF-MIB:ifXTable         → TableInfo JSON             │
│ ├─ tbl:IP-MIB:ipRouteTable     → TableInfo JSON             │
│ └─ ...                                                      │
│                                                             │
└─────────────────────────────────────────────────────────────┘

Range Query Example:
───────────────────
ListOIDs("1.3.6.1.2.1.1") returns all keys between:
  LowerBound: "oid:1.3.6.1.2.1.1"
  UpperBound: "oid:1.3.6.1.2.1.1~"  (~ is after all digits)

Result:
  ├─ "oid:1.3.6.1.2.1.1.1"       (sysDescr)
  ├─ "oid:1.3.6.1.2.1.1.2.0"     (sysObjectID)
  ├─ "oid:1.3.6.1.2.1.1.3.0"     (sysUpTime)
  ├─ "oid:1.3.6.1.2.1.1.4.0"     (sysContact)
  └─ ... (all system objects)
```

## OID Polling Decision Tree

```
                    ┌─────────────────┐
                    │ OID Candidate   │
                    └────────┬────────┘
                             │
                    ┌────────▼────────┐
                    │ Is readable?    │
                    │ (read-only or   │
                    │  read-write)    │
                    └────┬────────┬───┘
                    NO   │        │   YES
                    ┌────▼──┐     │
                    │ SKIP  │     │
                    └───────┘     │
                                  │
                    ┌─────────────▼───────┐
                    │ Is current status?  │
                    │ (not deprecated or  │
                    │  obsolete)          │
                    └────┬────────┬───────┘
                    NO   │        │   YES
                    ┌────▼──┐     │
                    │ SKIP  │     │
                    └───────┘     │
                                  │
                    ┌─────────────▼──────────┐
                    │ Is table/row?          │
                    │ (IsTable or            │
                    │  IsTableEntry)         │
                    └────┬────────┬──────────┘
                    YES  │        │   NO
                    ┌────▼──┐     │
                    │ SKIP  │     │
                    └───────┘     │
                                  │
                    ┌─────────────▼──────────┐
                    │ Is not-accessible?     │
                    └────┬────────┬──────────┘
                    YES  │        │   NO
                    ┌────▼──┐     │
                    │ SKIP  │     │
                    └───────┘     │
                                  │
                             ┌────▼──────┐
                             │ POLLABLE   │
                             │ Pollable=✓ │
                             └────────────┘
```

## Type Classification System

```
OIDMetadata.Type → Classification
═════════════════════════════════════════════════════════════

Counter Types (metrics that increase monotonically):
├─ Counter32   → IsCounter() = true
├─ Counter64   → IsCounter() = true
└─ Counter     → IsCounter() = true

Gauge Types (metrics that can go up or down):
├─ Gauge32     → IsGauge() = true
├─ Gauge       → IsGauge() = true
├─ Integer32   → IsGauge() = true
├─ Integer     → IsGauge() = true
└─ Unsigned32  → IsGauge() = true

String Types:
├─ OctetString → IsString() = true
└─ DisplayString → IsString() = true

Other Types:
├─ ObjectIdentifier → (not classified)
├─ Bits         → (not classified)
├─ TimeTicks    → (numeric, handled as gauge-like)
├─ Boolean      → (binary state)
└─ IPAddress    → (address string)

Prometheus Metric Mapping:
──────────────────────────
Counter32/Counter64 → prometheus.Counter
Gauge32/Integer    → prometheus.Gauge
OctetString        → prometheus.Gauge (label) or ignored
```

## Device Profile → OID Selection Flow

```
Device Configuration
│
├─ Profile: "cisco-ios"
│  │
│  ├─ Modules: ["SNMPv2-MIB", "IF-MIB", "CISCO-PROCESS-MIB"]
│  ├─ IncludeOIDs: []
│  ├─ ExcludeOIDs: [".*deprecated.*", ".*obsolete.*"]
│  ├─ IncludeNames: []
│  ├─ ExcludeNames: []
│  ├─ IncludeTables: false
│  └─ MaxOIDs: 0 (unlimited)
│
└─ Selector.SelectOIDs(profile) {
    │
    ├─ Step 1: Collect from modules
    │   Storage.GetPollableOIDs("SNMPv2-MIB")      → 185 OIDs
    │   Storage.GetPollableOIDs("IF-MIB")          → 95 OIDs
    │   Storage.GetPollableOIDs("CISCO-PROCESS-MIB") → 1200 OIDs
    │   Total: 1480 OIDs
    │
    ├─ Step 2: Apply filters (regex patterns)
    │   Exclude: ".*deprecated.*"    → Remove 45 OIDs
    │   Exclude: ".*obsolete.*"      → Remove 20 OIDs
    │   Remaining: 1415 OIDs
    │
    ├─ Step 3: Filter by table inclusion
    │   IncludeTables=false
    │   Remove IsTable and IsTableEntry OIDs → Remove 12 OIDs
    │   Remaining: 1403 OIDs
    │
    ├─ Step 4: Apply limit
    │   MaxOIDs=0 (unlimited)
    │   No filtering
    │   Final: 1403 OIDs
    │
    └─ Return 1403 []*OIDMetadata
       Ready for polling!
}
```

## Table Structure Navigation

```
IF-MIB Table Example:
────────────────────

ifTable (OID: 1.3.6.1.2.1.2.2)
│
├─ TableInfo {
│  Name:     "ifTable"
│  EntryOID: "1.3.6.1.2.1.2.2.1"
│  Columns: [
│    "1.3.6.1.2.1.2.2.1.1"   ← ifIndex (index)
│    "1.3.6.1.2.1.2.2.1.2"   ← ifDescr
│    "1.3.6.1.2.1.2.2.1.3"   ← ifType
│    "1.3.6.1.2.1.2.2.1.10"  ← ifInOctets
│    "1.3.6.1.2.1.2.2.1.16"  ← ifOutOctets
│    ...more columns
│  ]
│  Indexes: ["ifIndex"]
│ }
│
└─ Polling Process:
   1. Walk ifIndex to discover instances:
      SNMP WALK 1.3.6.1.2.1.2.2.1.1
      └─ Returns: .1 → "1"
                  .2 → "2"
                  .3 → "3"
   
   2. Poll each column for each instance:
      GET 1.3.6.1.2.1.2.2.1.10.1  ← ifInOctets for interface 1
      GET 1.3.6.1.2.1.2.2.1.10.2  ← ifInOctets for interface 2
      GET 1.3.6.1.2.1.2.2.1.10.3  ← ifInOctets for interface 3
      GET 1.3.6.1.2.1.2.2.1.16.1  ← ifOutOctets for interface 1
      GET 1.3.6.1.2.1.2.2.1.16.2  ← ifOutOctets for interface 2
      GET 1.3.6.1.2.1.2.2.1.16.3  ← ifOutOctets for interface 3
      ...
```

## Storage Concurrency Model

```
┌──────────────────────────────────────────────┐
│ Storage Instance (sync.RWMutex Protected)    │
├──────────────────────────────────────────────┤
│                                              │
│  Write Operations (Exclusive Lock)           │
│  ├─ StoreOID()      ─ mu.Lock()              │
│  ├─ StoreModule()   ─ mu.Lock()              │
│  ├─ StoreTable()    ─ mu.Lock()              │
│  ├─ DeleteOID()     ─ mu.Lock()              │
│  ├─ DeleteModule()  ─ mu.Lock()              │
│  └─ Close()         ─ mu.Lock()              │
│                                              │
│  Read Operations (Shared Lock)               │
│  ├─ GetOID()        ─ mu.RLock()             │
│  ├─ GetOIDByName()  ─ mu.RLock()             │
│  ├─ ListOIDs()      ─ mu.RLock()             │
│  ├─ GetModule()     ─ mu.RLock()             │
│  ├─ ListModules()   ─ mu.RLock()             │
│  ├─ GetTable()      ─ mu.RLock()             │
│  ├─ ListTables()    ─ mu.RLock()             │
│  ├─ GetPollableOIDs()─ mu.RLock()            │
│  └─ Count()         ─ mu.RLock()             │
│                                              │
│  PebbleDB Handles:                           │
│  ├─ Internal locking for LSM tree            │
│  ├─ Concurrent reads (lock-free)             │
│  ├─ Single writer at a time                  │
│  └─ Snapshot isolation (MVCC)                │
│                                              │
└──────────────────────────────────────────────┘
```

## Data Type Preservation

```
MIB Definition Detail → Storage Preservation
═════════════════════════════════════════════════════════════

✓ PRESERVED:
  ├─ OID (numeric)              → OID field
  ├─ Name (symbolic)            → Name field
  ├─ Module                     → Module field
  ├─ Base type (Counter32, etc.)→ Type field
  ├─ Access (read-only, etc.)   → Access field
  ├─ Status (current, etc.)     → Status field
  ├─ Description                → Description field
  ├─ Units (if present)         → Units field
  ├─ Parent OID                 → Parent field
  ├─ Table indexes              → Index array
  ├─ Table augments             → Augments field
  ├─ Node kind                  → IsTable/IsColumn/etc.
  └─ Import list                → Imports array

✗ NOT PRESERVED (or Simplified):
  ├─ Enumeration mappings       (only type stored)
  ├─ Range constraints          (0..255 not captured)
  ├─ Bit string definitions     (only Bits type stored)
  ├─ Default values             (not extracted)
  ├─ DISPLAY-HINT              (format hints lost)
  ├─ Textual convention details (TC name only)
  ├─ Complex syntax rules       (simplified)
  └─ Dependencies resolution    (stored but not resolved)
```

