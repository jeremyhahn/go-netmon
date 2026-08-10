# MIB Parser Integration Tests

This document describes the comprehensive integration tests for the MIB parser, which run against the real MIB files in the repository's `mibs/` tree.

## Overview

The integration tests verify that the MIB parser correctly handles real-world MIB files, with special focus on detecting critical bugs like empty OID fields that would prevent proper SNMP polling.

## Running the Tests

### Run All Integration Tests

```bash
make integration-test-parser
```

Or directly:

```bash
go test -v -tags=integration -timeout=10m ./internal/mib/parser/...
```

### Run Specific Tests

#### CISCO-BGP4-MIB Empty OID Bug Test
```bash
make integration-test-parser-cisco-bgp
```

This test specifically checks for the empty OID bug documented in the bug report. If OIDs have empty numeric OID fields, the test will FAIL with detailed output.

#### Critical MIBs Test
```bash
make integration-test-parser-critical
```

Tests a curated list of critical MIBs required for production network monitoring.

### Run with Benchmarks

```bash
make bench-parser-integration
```

## Test Coverage

### 1. Load All MIBs (`TestIntegration_LoadAllASR1001MIBs`)

Attempts to load all MIB files from the ASR1001 directory and reports:
- Total number of MIB files
- Successfully loaded MIBs
- Failed MIBs with error messages
- OID count per MIB

**Purpose**: Verify the parser can handle all available MIBs without crashing.

### 2. CISCO-BGP4-MIB Empty OID Detection (`TestIntegration_CISCO_BGP4_MIB_NoEmptyOIDs`)

**Critical Test** - This test catches the documented empty OID bug.

Verifies:
- No OID has an empty `OID` field
- No pollable OID has an empty numeric OID
- Minimum OID count threshold is met
- Classification breakdown is generated

**Expected Behavior**: Test should PASS when the bug is fixed (all OIDs have numeric values).

**Current Status**: Test FAILS - detecting 157 OIDs with empty OID fields (100% of all OIDs in CISCO-BGP4-MIB).

### 3. IF-MIB Empty OID Detection (`TestIntegration_IF_MIB_NoEmptyOIDs`)

Similar to CISCO-BGP4-MIB test but for the standard IF-MIB.

Verifies:
- No empty OIDs in IF-MIB
- Table structure is correct
- Pollable OIDs have valid numeric OIDs

### 4. BGP4-MIB Empty OID Detection (`TestIntegration_BGP4_MIB_NoEmptyOIDs`)

Tests the standard BGP4-MIB for empty OID issues.

### 5. Table Structure Parsing (`TestIntegration_TableStructureParsing`)

Verifies correct parsing of SNMP table structures:
- Table OID is set
- Entry OID is set
- Indexes are identified
- Columns are identified
- All column OIDs are non-empty

### 6. OID Hierarchy (`TestIntegration_OIDHierarchy`)

Tests that the parser correctly establishes parent-child relationships:
- Columns have parent OIDs
- Columns have TableOID set
- Columns have EntryOID set
- Parent nodes are properly linked

### 7. Object Classification (`TestIntegration_ObjectClassification`)

Verifies objects are correctly classified:
- Scalars
- Table nodes
- Entry nodes
- Index columns
- Metric columns
- Status columns

### 8. Pollable OID Validation (`TestIntegration_PollableOIDValidation`)

**Critical for SNMP Polling** - Ensures pollable OIDs can actually be polled.

Verifies:
- All pollable OIDs have non-empty numeric OIDs
- OIDs are in correct format (starts with "1." or ".1.")
- Minimum percentage of OIDs are pollable
- Tested across multiple MIBs

### 9. Chartability Classification (`TestIntegration_ChartabilityClassification`)

Tests that objects are correctly marked as chartable:
- Counter and Gauge types are chartable
- Index columns are NOT chartable
- Chart types are assigned correctly

### 10. Enum Values Extraction (`TestIntegration_EnumValues`)

Verifies that enumerated values are correctly extracted from INTEGER types.

### 11. Critical MIBs Test (`TestIntegration_CriticalMIBs`)

Tests a curated list of production-critical MIBs:
- IF-MIB (required)
- CISCO-BGP4-MIB (required)
- BGP4-MIB (optional)
- CISCO-PROCESS-MIB (optional)
- CISCO-MEMORY-POOL-MIB (optional)

## Test Output

### Classification Breakdown

Each test provides detailed classification breakdown:

```
=== CISCO-BGP4-MIB Classification Breakdown ===
Total OIDs: 157
Pollable: 95 (60.5%)
Chartable: 2 (1.3%)
Displayable: 23 (14.6%)
With Enums: 14 (8.9%)
Empty OID: 157 (100.0%) ⚠️  WARNING

By Type:
  : 128
  CbgpSafi: 3
  ...

By Access:
  read-only: 83
  read-write: 18
  ...

By ObjectClass:
  metric_column: 94
  status_column: 21
  ...
```

### Failure Messages

When empty OIDs are detected:

```
CRITICAL: Found OID with empty OID field: Name=cbgpRouteOrigin, Module=CISCO-BGP4-MIB, Type=Enumeration, IsColumn=true, IsScalar=false, Pollable=true
CRITICAL: Found pollable OID with empty OID field: Name=cbgpRouteOrigin, Module=CISCO-BGP4-MIB, Type=Enumeration
```

## Debugging

### Detailed OID Dump

Run with verbose output to see detailed OID information:

```bash
go test -v -tags=integration ./internal/mib/parser/... -run "TestIntegration_DetailedOIDDump"
```

This generates a detailed dump of the first 20 OIDs showing:
- OID numeric value
- Name, Module, Type
- Classification and flags
- Parent/Table/Entry relationships
- Enum values (if any)

## Known Issues

### Empty OID Bug

**Status**: ACTIVE BUG

**Affected MIBs**:
- CISCO-BGP4-MIB (100% of OIDs)
- IF-MIB (significant percentage)
- Likely others

**Symptom**: The parser returns empty strings for the `OID` field in `OIDMetadata` structures, making these OIDs impossible to poll via SNMP.

**Root Cause**: The parser uses `raw.Oid.String()` which returns empty strings for certain MIB objects. This appears to be an issue with the gosmi library on Alpine Linux or how OIDs are being rendered.

**Impact**:
- Pollable OIDs cannot be polled
- Dashboards cannot be generated
- Monitoring is broken for affected MIBs

**Detection**: The integration tests will FAIL when this bug exists, showing:
```
Found X OIDs with empty OID fields in CISCO-BGP4-MIB
Found Y pollable OIDs with empty numeric OID
```

**Resolution**: When fixed, all integration tests should PASS with 0 empty OIDs detected.

## Test Requirements

### MIB Files

Tests parse with the **complete** search path — every directory under `mibs/`
(`standard`, `cisco`, `asr1001`, `vendor`, `r640`) — located through the
`internal/testsupport` helpers rather than a hardcoded path, so a checkout
anywhere works.

A complete path is part of what these tests fix in place. Parsing with a subset
does not fail: it silently yields fewer objects, most with an empty OID field
and no type. Parsed with `mibs/asr1001` alone, IF-MIB gives 62 objects with 62
empty OIDs; with the full path it gives 91 with none.

The MIB tree ships with the repository, so a missing directory is a failure, not
a reason to skip. There are no skip guards: the project forbids them, and every
one that used to be here was hiding a real defect — including a `cbgpPeerTable`
assertion that named the wrong table.

### Dependencies

- gosmi library
- Go 1.21+
- Integration build tag

## Continuous Integration

These tests should be run as part of CI/CD to catch regressions:

```yaml
# Example GitHub Actions
- name: Run Integration Tests
  run: |
    make integration-test-parser
  timeout-minutes: 10
```

## Performance

### Benchmark Results

Run benchmarks to measure parser performance:

```bash
make bench-parser-integration
```

Expected performance:
- CISCO-BGP4-MIB: ~100-200 OIDs/sec
- IF-MIB: ~50-100 OIDs/sec
- Depends on MIB complexity and dependencies

## Contributing

When adding new integration tests:

1. Use the `//go:build integration` tag
2. Never skip. Assert, and fail with a message naming what was missing
3. Provide detailed failure messages
4. Log classification breakdowns
5. Test both success and failure cases
6. Document expected behavior

## Related Documentation

- [Parser README](./README.md) - Parser design and architecture
- [Bug Report] - Original empty OID bug report
- [MIB Types](../types.go) - OIDMetadata structure definition
