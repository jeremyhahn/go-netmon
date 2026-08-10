# Phase 2 Progress: MIB Parser & Dynamic OID Management

**Date:** 2025-10-23
**Status:** 🚧 IN PROGRESS (70% Complete)

## Completed Items ✅

### 1. Dependencies Added ✅
- **PebbleDB** (v1.1.5): High-performance LSM key-value store
- **gosmi** (v0.4.4): Go library for parsing SMI MIB files

### 2. MIB Storage Layer (PebbleDB) ✅
**Package**: `internal/mib/storage`

**Files Created**:
- `storage.go` - Complete PebbleDB storage implementation
- `storage_test.go` - Comprehensive test suite (16 tests)

**Test Coverage**: **80.3%** 🎉

**Features**:
- Store/retrieve OID metadata by OID or name
- Module information storage
- Table information storage
- Prefix-based OID listing
- Pollable OID filtering
- Name→OID reverse lookup
- Concurrent-safe operations (RWMutex)
- Idempotent Close() for cleanup

**Key Prefixes**:
```
oid:    → OID metadata
mod:    → Module info
tbl:    → Table info
name:   → Name→OID mapping
```

**API**:
```go
storage := storage.New("/path/to/db")
storage.StoreOID(metadata)
storage.GetOID("1.3.6.1.2.1.1.1.0")
storage.GetOIDByName("SNMPv2-MIB", "sysDescr")
storage.ListOIDs("1.3.6.1.2.1")  // Prefix search
storage.GetPollableOIDs("IF-MIB")
```

### 3. MIB Types & Metadata ✅
**Package**: `internal/mib`

**Types Created**:
- `OIDMetadata` - Complete OID information with metadata
- `ModuleInfo` - MIB module information
- `TableInfo` - SNMP table structure
- `PollingProfile` - Device-specific polling configuration

**Features**:
- `IsPollable()` - Intelligent OID selection
- `IsCounter()`, `IsGauge()`, `IsString()` - Type helpers
- JSON serialization ready
- Timestamps for tracking

### 4. MIB Parser (gosmi) ✅
**Package**: `internal/mib/parser`

**Files Created**:
- `parser.go` - Complete MIB parser implementation

**Features**:
- Initialize gosmi library with custom MIB paths
- Load MIB modules by name
- Parse all OIDs from a module
- Extract table information
- Convert gosmi types to our metadata types
- Support for:
  - OID metadata extraction
  - Table/column detection
  - Index extraction
  - Access/status parsing
  - Type conversion

**API**:
```go
parser := parser.New("/usr/share/snmp/mibs")
parser.Init()

// Load module
info, _ := parser.LoadModule("IF-MIB")

// Parse all OIDs
oids, _ := parser.ParseModule("IF-MIB")

// Get table info
tables, _ := parser.GetTableInfo("IF-MIB")

parser.Close()
```

## Remaining Work 🚧

### 5. Parser Tests (30% done)
- ✅ Basic unit tests for type conversion
- ✅ Structure tests
- ⏳ Integration tests (need MIB files)
- ⏳ Full coverage tests

### 6. Download Standard MIBs
Need to obtain:
- SNMPv2-MIB
- IF-MIB
- IP-MIB
- HOST-RESOURCES-MIB

### 7. Download Cisco MIBs
Need to obtain:
- CISCO-PROCESS-MIB (CPU monitoring)
- CISCO-MEMORY-POOL-MIB (memory monitoring)
- CISCO-ENVMON-MIB (environmental monitoring)
- BGP4-MIB (BGP status)
- OSPF-MIB (OSPF status)

### 8. Dynamic OID Selection
Implement:
- Filter pollable OIDs from MIB
- Handle SNMP tables with automatic index discovery
- Generate polling lists per device
- Support include/exclude patterns

### 9. Integrate with Poller
Modify `internal/poller`:
- Load MIBs at startup
- Query pollable OIDs from storage
- Replace hard-coded OIDs with MIB-driven selection
- Support per-device MIB profiles

### 10. Test with Cisco Routers
- Load Cisco MIBs
- Configure polling for 198.51.100.252-253
- Verify metrics collection
- Validate table walking (ifTable, etc.)

## Architecture Overview

```
┌─────────────────────────────────────┐
│          MIB Files                  │
│  (SNMPv2-MIB, IF-MIB, Cisco MIBs)  │
└────────────────┬────────────────────┘
                 │
                 ▼
        ┌────────────────┐
        │  Parser (gosmi)│
        │   - Parse MIB  │
        │   - Extract    │
        │     metadata   │
        └────────┬───────┘
                 │
                 ▼
      ┌──────────────────┐
      │  Storage (Pebble)│
      │  - OID metadata  │
      │  - Module info   │
      │  - Table info    │
      └────────┬─────────┘
               │
               ▼
      ┌────────────────┐
      │  Poller        │
      │  - Query OIDs  │
      │  - Poll devices│
      └────────────────┘
```

## Code Statistics

### Storage Layer
- **Lines of Code**: ~430
- **Test Lines**: ~620
- **Test Coverage**: 80.3%
- **Tests**: 16 (all passing)
- **Functions**: 18

### Parser Layer
- **Lines of Code**: ~300
- **Functions**: 12
- **Test Coverage**: TBD (need MIB files)

### Total Phase 2
- **New Code**: ~730 lines
- **Test Code**: ~620 lines
- **New Packages**: 2 (storage, parser)
- **Dependencies Added**: 2 (pebble, gosmi)

## Performance Characteristics

### PebbleDB Storage
- **Write Speed**: ~100K ops/sec (estimated)
- **Read Speed**: ~200K ops/sec (estimated)
- **Cache**: 64MB default
- **Storage**: LSM tree (optimized for writes)
- **Concurrent**: Thread-safe with RWMutex

### MIB Parser
- **Parse Speed**: ~1000 OIDs/sec (estimated)
- **Memory**: Depends on MIB size (~10MB for large MIBs)
- **One-time cost**: Parse at startup, cache in PebbleDB

## Integration Points

### With Existing Code

**Config System** (`internal/config`):
```go
// Add to config.yaml
mib_paths:
  - "/usr/share/snmp/mibs"
  - "./mibs"

storage_path: "./data/mib-storage"
```

**Poller** (`internal/poller`):
```go
// In poller initialization
storage := mibstorage.New(cfg.StoragePath)
parser := mibparser.New(cfg.MIBPaths...)
parser.Init()

// Load MIBs
for _, module := range cfg.MIBModules {
    oids, _ := parser.ParseModule(module.Name)
    for _, oid := range oids {
        storage.StoreOID(oid)
    }
}

// Get pollable OIDs for device
pollable, _ := storage.GetPollableOIDs("IF-MIB")
```

## Next Steps (Priority Order)

1. **Download MIB Files** (1 hour)
   - Get standard MIBs from net-snmp
   - Download Cisco MIBs from Cisco website
   - Organize in `mibs/` directory

2. **Complete Parser Tests** (2 hours)
   - Run integration tests with real MIBs
   - Validate parsing of IF-MIB
   - Test table extraction

3. **Implement Dynamic OID Selection** (3 hours)
   - Create OID selector component
   - Handle table index discovery
   - Implement filtering logic

4. **Integrate with Poller** (3 hours)
   - Modify poller to use MIB storage
   - Remove hard-coded OIDs
   - Add MIB loading at startup

5. **Test with Cisco Routers** (2 hours)
   - Configure Cisco MIBs
   - Poll 198.51.100.252-253
   - Verify interface metrics
   - Validate CPU/memory monitoring

**Estimated Remaining Time**: ~11 hours

## Known Issues

1. **Parser tests skip**: Need actual MIB files to run integration tests
2. **No table walking yet**: Need to implement automatic index discovery
3. **Hard-coded OIDs in poller**: Need to integrate MIB storage

## Success Criteria for Phase 2

- [x] PebbleDB storage layer complete
- [x] MIB parser implemented
- [x] Storage tests passing (80%+ coverage)
- [ ] Parser tests passing with real MIBs
- [ ] MIB files organized
- [ ] Dynamic OID selection working
- [ ] Poller using MIB-driven polling
- [ ] Successfully monitoring Cisco routers

---

**Phase 2: 70% Complete** | **Next Milestone**: Download MIBs & Complete Tests
