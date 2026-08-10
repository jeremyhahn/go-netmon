# Scalar OID Detection Fix

## Problem

The SNMP poller was not collecting scalar OIDs correctly, particularly SNMPv2-MIB system group scalars like `sysDescr`, `sysUpTime`, and `sysName`. 

### Symptoms
- Polling 126 scalar OIDs in 2 chunks (100 + 26)
- First chunk of 100 returned 0 results (no error)
- Second chunk of 26 returned 26 results
- SNMPv2-MIB system scalars were NOT being collected

### Root Cause

The issue was in `/internal/poller/poller.go` lines 282-290:

```go
// Scalar OID - need to append .0 for SNMP GET
oidStr := oid.OID
if oid.IsScalar {
    // Check if it doesn't already end with .0
    if len(oidStr) < 2 || oidStr[len(oidStr)-2:] != ".0" {
        oidStr = oidStr + ".0"
    }
}
scalarOIDs = append(scalarOIDs, oidStr)
```

The problem: `.0` was ONLY appended if `oid.IsScalar == true`. However, many scalar OIDs in the MIB database had `IsScalar = false` because:

1. The MIB parser set `IsScalar = false` for OIDs with `NodeKind == types.NodeNode`
2. Many scalar OIDs (like sysDescr, sysUpTime) are classified as `NodeNode` instead of `NodeScalar` by the gosmi library

## Solution

### 1. Fixed Poller Logic (Primary Fix)

Changed the logic in `/internal/poller/poller.go` to append `.0` to ALL non-table/column/entry OIDs:

```go
// NEW: Helper function to determine if OID is scalar
func isScalarOID(oid *mib.OIDMetadata) bool {
    // If it's a table, column, or table entry, it's NOT scalar
    if oid.IsTable || oid.IsColumn || oid.IsTableEntry {
        return false
    }
    // Everything else is scalar
    return true
}

// NEW: Helper function to ensure .0 suffix
func ensureScalarSuffix(oidStr string) string {
    if len(oidStr) >= 2 && oidStr[len(oidStr)-2:] == ".0" {
        return oidStr
    }
    return oidStr + ".0"
}

// In poll() function:
for _, oid := range w.oids {
    if oid.IsTable || oid.IsColumn || oid.IsTableEntry {
        if !oid.IsTableEntry {
            tableOIDs = append(tableOIDs, oid)
        }
    } else {
        // FIX: Append .0 to ALL non-table/column/entry OIDs
        oidStr := ensureScalarSuffix(oid.OID)
        scalarOIDs = append(scalarOIDs, oidStr)
    }
}
```

### 2. Enhanced MIB Parser (Secondary Fix)

Improved scalar detection in `/internal/mib/parser/parser.go`:

```go
// FIX: Improve scalar detection
if raw.NodeKind == types.NodeNode && !meta.IsTable && !meta.IsTableEntry && !meta.IsColumn {
    // Check if this node has any children that are columns or rows
    hasTableChildren := p.hasTableChildren(node)
    if !hasTableChildren {
        // This is a leaf node that's not part of a table structure, so it's a scalar
        meta.IsScalar = true
    }
}
```

### 3. Added Debug Logging

Enhanced logging to help diagnose scalar OID issues:

```go
log.Printf("DEBUG: Device %s - Adding scalar OID: %s -> %s (IsScalar=%v IsTable=%v IsColumn=%v IsTableEntry=%v)",
    w.device.Name, oid.Name, oidStr, oid.IsScalar, oid.IsTable, oid.IsColumn, oid.IsTableEntry)
```

## Testing

Created comprehensive tests in `/internal/poller/scalar_test.go`:

1. **TestIsScalarOID**: Verifies scalar detection logic
   - Scalar OID with IsScalar=true
   - Scalar OID with IsScalar=false (should still be treated as scalar)
   - Table OID (should not be scalar)
   - Column OID (should not be scalar)
   - Table Entry OID (should not be scalar)
   - Node OID (should be treated as scalar)

2. **TestEnsureScalarSuffix**: Verifies .0 suffix logic
   - OID without .0 suffix
   - OID with .0 suffix (don't double-append)
   - OID ending with .10 (not .0)
   - Short OID
   - Empty OID

3. **TestScalarOIDDetectionInPoll**: End-to-end test of scalar detection in poll logic

All tests pass:
```
PASS: TestIsScalarOID (0.00s)
PASS: TestEnsureScalarSuffix (0.00s)
PASS: TestScalarOIDDetectionInPoll (0.00s)
```

## Impact

After this fix:
- All scalar OIDs are correctly identified regardless of the `IsScalar` flag
- The `.0` suffix is properly appended to all scalar OIDs
- SNMPv2-MIB system group scalars (sysDescr, sysUpTime, sysName, etc.) are now correctly polled
- No double-appending of `.0` suffix
- Backward compatible with existing code

## Files Modified

1. `/internal/poller/poller.go` - Fixed scalar OID detection and .0 appending logic
2. `/internal/mib/parser/parser.go` - Enhanced IsScalar flag detection
3. `/internal/poller/scalar_test.go` - New test file with comprehensive coverage

## Related Issues

- Scalar OIDs not being collected
- First chunk of 100 OIDs returning 0 results
- SNMPv2-MIB system scalars missing from metrics
