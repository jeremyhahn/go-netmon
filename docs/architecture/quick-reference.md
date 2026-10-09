# MIB Dashboard Redesign - Quick Reference Card

> **Status: ARCHIVE** · Scope: go-netmon
>
> Summary card for a redesign proposal that was only partly built; not
> maintained. Its file list and phases describe the plan, not the code. See
> [mib-dashboard-redesign.md](mib-dashboard-redesign.md) for what was built and
> [../dashboard-generation.md](../dashboard-generation.md) for what replaced it.

## 30-Second Overview

**Problem**: Dashboard shows wrong data, empty instance selectors, charts meaningless indices.

**Root Cause**: Pattern-based table detection and no semantic understanding of MIB objects.

**Solution**: Use parser metadata for semantic classification and generate explicit dashboard specifications.

**Effort**: 10-15 days, 7 phases, feature-flagged rollout.

---

## Key Changes At-a-Glance

| Component | Current | New |
|-----------|---------|-----|
| **Parser** | Extracts basic metadata | + Classifies objects semantically |
| **Storage** | Flat OID list | + Categorized table columns |
| **API** | Returns raw OID list | Returns dashboard specification |
| **Frontend** | Categorizes and renders | Renders from specification |

---

## Object Classification Cheat Sheet

```go
ObjectClassScalar       → Single value (ifNumber)           → Display as status/chart
ObjectClassTableNode    → Table container (ifTable)         → Not displayed
ObjectClassEntryNode    → Row definition (ifEntry)          → Not displayed
ObjectClassIndexColumn  → Index (ifIndex)                   → Use for selector
ObjectClassStatusColumn → Status field (ifOperStatus)       → Display as text
ObjectClassMetricColumn → Time-series (ifInOctets)          → Display as chart
```

---

## Decision Tree: What to Chart?

```
Is it pollable?
  No → Don't display
  Yes ↓

Is it a table/entry node?
  Yes → Don't display (organizational only)
  No ↓

Is it an index column?
  Yes → Use for instance selector (don't chart)
  No ↓

Is it a counter or gauge?
  Yes → Chart it! (MetricColumn)
  No ↓

Is it a string or has enum values?
  Yes → Display as text (StatusColumn)
  No ↓

Is it a scalar numeric?
  Yes → Chart it (ScalarWidget)
  No → Unknown, skip
```

---

## Quick Implementation Checklist

### Phase 1: Parser (1-2 days)
- [ ] Add ObjectClass to OIDMetadata
- [ ] Implement determineObjectClass()
- [ ] Extract enum values from gosmi
- [ ] Build table hierarchy (TableOID, EntryOID)
- [ ] Set IsIndex flag
- [ ] Set IsChartable based on classification
- [ ] Test with IF-MIB and CISCO-BGP4-MIB

### Phase 2: Storage (1 day)
- [ ] Add GetTableInfoEnhanced() method
- [ ] Categorize columns into Index/Status/Metric
- [ ] Add efficient table querying
- [ ] Test table retrieval

### Phase 3: Dashboard Generator (2-3 days)
- [ ] Create dashboard package
- [ ] Implement DashboardSpec generation
- [ ] Generate ScalarWidgets from scalars
- [ ] Generate TableWidgets from tables
- [ ] Generate query templates
- [ ] Test spec generation for multiple MIBs

### Phase 4: API (1-2 days)
- [ ] Add GET /api/devices/{id}/mibs/{name}/dashboard
- [ ] Enhance GET /api/mibs/{id}/tables
- [ ] Add feature flag support
- [ ] Test endpoints return correct specs

### Phase 5: Frontend (2-3 days)
- [ ] Implement loadDashboardSpec()
- [ ] Implement renderDashboardFromSpec()
- [ ] Implement instance selector from spec
- [ ] Implement status field rendering with enums
- [ ] Implement metric chart rendering
- [ ] Remove deprecated code
- [ ] Test all MIBs render correctly

### Phase 6: Testing (2-3 days)
- [ ] Unit tests (90%+ coverage)
- [ ] Integration tests
- [ ] E2E tests
- [ ] Performance benchmarks
- [ ] Manual testing checklist

### Phase 7: Documentation (1 day)
- [ ] Update API docs
- [ ] Create developer guide
- [ ] Update architecture docs

---

## Common Patterns

### Pattern 1: Check if Index Column

```go
// In parser
if meta.IsColumn && meta.EntryOID != "" {
    entryNode, _ := gosmi.GetNodeByOID(parseOID(meta.EntryOID))
    indexes := entryNode.GetIndex()
    for _, idx := range indexes {
        if string(idx.GetRaw().Name) == meta.Name {
            meta.IsIndex = true
            meta.ObjectClass = ObjectClassIndexColumn
        }
    }
}
```

### Pattern 2: Generate Query Template

```go
// The metric is named after the MIB object; the row lives in labels.
// See docs/metric-naming.md.

// For scalar
template := fmt.Sprintf("%s{device=\"{{device}}\"}",
    promname.MetricName(meta.Name))

// For column: one metric name covers every row, so the row is a label matcher
template := fmt.Sprintf("%s{device=\"{{device}}\",%s=\"{{instance}}\"}",
    promname.MetricName(meta.Name), promname.IndexLabel(indexColumnName))

// For counter (add rate)
if isCounter {
    template = fmt.Sprintf("rate(%s[5m])", template)
}
```

### Pattern 3: Render Status Field

```javascript
// Frontend
const value = prometheusResult.value;  // e.g., 1
const enumValues = field.enum_values;  // e.g., {1: "up", 2: "down"}

const displayValue = enumValues[value] || value;  // "up"
statusElement.textContent = displayValue;
```

---

## File Locations

### New Files to Create

```
internal/mib/dashboard/
  ├── types.go          (DashboardSpec, ScalarWidget, TableWidget)
  ├── generator.go      (Generate dashboard specs)
  └── generator_test.go (Tests)

docs/architecture/
  ├── mib-dashboard-redesign.md          (Main design doc)
  ├── mib-dashboard-diagrams.md          (Visual diagrams)
  ├── implementation-guide.md            (Code examples)
  ├── testing-strategy.md                (Test plan)
  ├── problem-solution-comparison.md     (Before/after)
  └── quick-reference.md                 (This file)
```

### Files to Modify

```
internal/mib/types.go              (Add ObjectClass, EnumValues, etc.)
internal/mib/parser/parser.go      (Enhanced parsing)
internal/mib/storage/storage.go    (Add GetTableInfoEnhanced)
internal/webui/api/mib_handler.go  (New endpoint)
internal/webui/static/js/app.js    (Refactor rendering)
```

---

## Testing Commands

```bash
# Unit tests
make test-parser
make test-storage

# Integration tests
make integration-test

# E2E test
./scripts/e2e_test.sh

# Coverage
make coverage

# Benchmarks
```

---

## Debug Tips

### Problem: Parser not classifying correctly

```go
// Add debug logging
log.Printf("DEBUG: %s ObjectClass=%s IsIndex=%v IsChartable=%v",
    meta.Name, meta.ObjectClass, meta.IsIndex, meta.IsChartable)
```

### Problem: Dashboard spec missing objects

```go
// Check module filtering
log.Printf("DEBUG: Module=%s filtering for=%s match=%v",
    oid.Module, moduleName, oid.Module == moduleName)
```

### Problem: Instance selector empty

```javascript
// Check query template
console.log("Instance query:", template);
console.log("Prometheus result:", result);
```

### Problem: Enum values not showing

```javascript
// Check enum extraction
console.log("Field enums:", field.enum_values);
console.log("Metric value:", value);
console.log("Display value:", enumValues[value]);
```

---

## Common Mistakes to Avoid

1. **Don't use pattern matching** - Use parser metadata
2. **Don't skip enum extraction** - Users need readable values
3. **Don't chart index columns** - They're identifiers, not metrics
4. **Don't mix modules** - Validate module filtering
5. **Don't skip testing** - 90% coverage required
6. **Don't rush Phase 3** - Dashboard generator is most complex
7. **Don't forget feature flag** - Safe migration is critical

---

## Performance Targets

| Operation | Target | Current | Notes |
|-----------|--------|---------|-------|
| Parse MIB | < 300ms | ~200ms | Acceptable slowdown for better data |
| Load Dashboard | < 1000ms | ~1500ms | Should be 40% faster |
| Populate Selector | < 1000ms | FAILS | Currently broken |
| Render Charts | < 500ms | ~1200ms | Should be 60% faster |

---

## Success Criteria

### Must Pass

- [ ] IF-MIB tab shows only IF-MIB objects
- [ ] BGP tab shows only BGP objects
- [ ] Instance selector populates
- [ ] ifIndex NOT charted
- [ ] ifOperStatus shows "up" not "1"
- [ ] All charts show correct data
- [ ] No console errors
- [ ] 90%+ code coverage

### Nice to Have

- [ ] Dashboard loads < 1 second
- [ ] Smooth animations
- [ ] Helpful error messages
- [ ] Comprehensive logging

---

## Quick Links

- [Full Design](./mib-dashboard-redesign.md)
- [Diagrams](./mib-dashboard-diagrams.md)
- [Code Examples](./implementation-guide.md)
- [Testing](./testing-strategy.md)
- [Comparison](./problem-solution-comparison.md)

---

## Need Help?

### Architecture Questions
→ See mib-dashboard-redesign.md section on "Proposed Architecture"

### Implementation Questions
→ See implementation-guide.md with code examples

### Testing Questions
→ See testing-strategy.md for comprehensive test plan

### Still Stuck?
→ Check problem-solution-comparison.md for visual examples

---

## Version History

- 2025-10-26: Initial quick reference created
- TBD: Updated after Phase 1 completion
- TBD: Updated after final implementation
