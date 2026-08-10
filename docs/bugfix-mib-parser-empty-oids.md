# Bug Fix: MIB Parser Empty OID Fields

## Problem Summary

The MIB parser was failing to correctly extract OID values from MIB files, causing critical functionality issues:

### Issue 1: CISCO-BGP4-MIB - Empty OID Fields
- **Symptom**: All 157 objects returned with empty `OID=""` fields
- **Impact**: MIB browser unable to display OIDs, dashboard generation failed
- **Root Cause**: `raw.Oid.String()` method returned empty strings for certain MIBs

### Issue 2: IF-MIB - Objects Not Found
- **Symptom**: ListOIDs("IF-MIB") returned 0 objects despite 91+ objects being stored
- **Impact**: MIB browser appeared to have no IF-MIB data, object count showed 0
- **Root Cause**: Objects were stored with numeric OID keys but searched by module name

## Evidence

From container logs:
```
DEBUG ListOIDs: Added OID 1: Name=cbgpGlobal, Module=CISCO-BGP4-MIB, OID=
DEBUG ListOIDs: Added OID 2: Name=cbgpLocalAs, Module=CISCO-BGP4-MIB, OID=
DEBUG ListOIDs: Added OID 3: Name=cbgpNotifsEnable, Module=CISCO-BGP4-MIB, OID=
```

```
DEBUG ListOIDs: oidPrefix="IF-MIB", keyPrefix="oid:IF-MIB"
DEBUG ListOIDs: Iterated 0 keys, returning 0 OIDs
```

## Root Cause Analysis

### Primary Issue: Incorrect OID Extraction Method

**File**: `internal/mib/parser/parser.go`
**Line**: 117

```go
// BUGGY CODE (before fix)
numericOID := raw.Oid.String()
```

The `raw.Oid.String()` method from the gosmi library was inconsistent:
- Worked correctly for SNMPv2-MIB
- Returned EMPTY strings for CISCO-BGP4-MIB
- Worked for IF-MIB but objects weren't retrievable due to storage key mismatch

The comment on line 116 incorrectly stated:
```go
// Use raw.Oid.String() instead of node.RenderNumeric() which is broken in Alpine
```

**This comment was WRONG** - `node.RenderNumeric()` works correctly in all cases, while `raw.Oid.String()` has issues.

### Secondary Issue: Object Count Display

**File**: `internal/webui/provider/provider.go`
**Line**: 36

```go
// INEFFICIENT CODE (before fix)
oids, err := p.storage.ListOIDs(module.Name)
```

This tried to filter OIDs using a key prefix match on module names, but OIDs are stored by numeric OID, not module name. This caused:
- Multiple inefficient storage queries
- Incorrect object counts (always 0)
- Confusion in the UI

## Solution

### Fix 1: Use Correct OID Extraction Method

**File**: `internal/mib/parser/parser.go`

```go
// FIXED CODE
// FIX: Use node.RenderNumeric() instead of raw.Oid.String()
// The raw.Oid.String() method returns empty strings for some MIBs (e.g., CISCO-BGP4-MIB)
// while node.RenderNumeric() correctly renders the OID in all cases
numericOID := node.RenderNumeric()
```

**Changes**:
- Line 117: Changed from `raw.Oid.String()` to `node.RenderNumeric()`
- Line 116-119: Updated comments to explain the correct approach
- Line 122-124: Updated debug logging format

### Fix 2: Optimize Object Count Retrieval

**File**: `internal/webui/provider/provider.go`

```go
// FIXED CODE
// Get all OIDs once to avoid multiple storage calls
allOIDs, err := p.storage.ListOIDs("")
if err != nil {
    return nil, fmt.Errorf("failed to list OIDs: %w", err)
}

// Count OIDs per module
oidCounts := make(map[string]int)
for _, oid := range allOIDs {
    oidCounts[oid.Module]++
}
```

**Changes**:
- Lines 33-43: Retrieve all OIDs once and count by module
- Eliminated N storage queries (one per module)
- Correct object counts displayed in UI

## Verification

### Unit Tests

Created comprehensive test suite: `internal/mib/parser/parser_oid_fix_test.go`

```bash
go test -v -run TestParser_ParseModule_IF_MIB_OIDs ./internal/mib/parser/
# PASS - All 91 OIDs have valid OID fields

go test -v -run TestParser_AllModules_NoEmptyOIDs ./internal/mib/parser/
# PASS - SNMPv2-MIB: 70 OIDs, IF-MIB: 91 OIDs, IP-MIB: 293 OIDs
```

### Integration Tests

Created integration test suite: `internal/mib/parser/parser_oid_integration_test.go`

```bash
go test -v -tags=integration -run TestIntegration_MIBParsing_NoEmptyOIDs ./internal/mib/parser/
# PASS - Verifies end-to-end parsing and storage
```

### Container Verification

```bash
# Check parsed OID counts
docker logs go-netmon-webui 2>&1 | grep "Stored.*OIDs from"
# Output:
# Stored 70/70 OIDs from SNMPv2-MIB
# Stored 91/91 OIDs from IF-MIB
# Stored 169/169 OIDs from CISCO-BGP4-MIB
```

```bash
# Verify API returns correct data
curl -s http://localhost:8081/api/mibs | jq '.mibs[] | {name, objectCount}'
# Output:
# {"name": "CISCO-BGP4-MIB", "objectCount": 169}
# {"name": "IF-MIB", "objectCount": 91}
# {"name": "SNMPv2-MIB", "objectCount": 70}
```

```bash
# Verify no empty OID fields
curl -s http://localhost:8081/api/mibs/CISCO-BGP4-MIB | jq '[.objects[] | select(.oid == "")] | length'
# Output: 0

curl -s http://localhost:8081/api/mibs/IF-MIB | jq '[.objects[] | select(.oid == "")] | length'
# Output: 0
```

## Impact

### Before Fix
- CISCO-BGP4-MIB: 157 objects with EMPTY OID fields ❌
- IF-MIB: 0 objects found in API (despite 91 stored) ❌
- MIB browser: Non-functional ❌
- Dashboard generation: Failed or used wrong MIB ❌

### After Fix
- CISCO-BGP4-MIB: 169 objects with CORRECT OIDs ✅
- IF-MIB: 91 objects with CORRECT OIDs ✅
- SNMPv2-MIB: 70 objects with CORRECT OIDs ✅
- MIB browser: Fully functional ✅
- Dashboard generation: Works correctly ✅
- API performance: Improved (fewer storage queries) ✅

## Files Modified

1. `internal/mib/parser/parser.go` - Fixed OID extraction (line 117)
2. `internal/webui/provider/provider.go` - Optimized object count retrieval (lines 33-51)

## Files Added

1. `internal/mib/parser/parser_oid_fix_test.go` - Unit tests for OID extraction
2. `internal/mib/parser/parser_oid_integration_test.go` - Integration tests
3. `docs/bugfix-mib-parser-empty-oids.md` - This document

## Lessons Learned

1. **Trust the API, not comments**: The comment stating `node.RenderNumeric()` was "broken in Alpine" was incorrect. Always verify assumptions.

2. **Test with real data**: Unit tests with standard MIBs (SNMPv2-MIB) passed, but vendor-specific MIBs (CISCO-BGP4-MIB) revealed the bug.

3. **Storage key design matters**: Using numeric OIDs as keys is correct, but API layers need to understand this design choice.

4. **Debug logging is essential**: The detailed debug logs made it easy to identify that OID fields were empty.

5. **Integration tests catch real issues**: Unit tests alone didn't reveal the CISCO-BGP4-MIB problem until integration testing with actual container deployment.

## Recommendations

1. Add pre-commit hook to run parser tests
2. Create automated test that uploads real vendor MIB files
3. Add monitoring/alerting for empty OID fields in production
4. Document storage key design in architecture docs
5. Consider adding OID validation at storage layer

## Related Issues

- Dashboard generator was falling back to SNMPv2-MIB when IF-MIB objects were unavailable
- Poller was working correctly because it uses numeric OIDs directly from Prometheus
- MIB browser feature was completely broken for vendor-specific MIBs
