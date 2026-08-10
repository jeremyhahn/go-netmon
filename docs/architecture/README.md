# MIB Dashboard Architecture Documentation

## Overview

This directory contains comprehensive architectural documentation for the MIB dashboard redesign project.

## Critical Problem

The current SNMP MIB dashboard generation system has fundamental architectural flaws:

1. **Wrong data displayed**: Clicking CISCO-BGP4-MIB tab shows IF-MIB objects
2. **Empty instance selector**: Dropdown doesn't populate with interfaces
3. **Inappropriate visualizations**: Creating charts for index columns (ifIndex)
4. **No semantic understanding**: System doesn't distinguish between indices, status fields, and metrics

## Solution

Complete architectural redesign with semantic understanding of MIB structures.

## Documentation Structure

### 1. [mib-dashboard-redesign.md](./mib-dashboard-redesign.md)
**Main Design Document** - 80 pages

Comprehensive analysis and design including:
- Detailed problem analysis with code examples
- Proposed architecture with component breakdown
- Complete data structure definitions
- API contracts with JSON examples
- 7-phase implementation roadmap (10-15 days)
- Success criteria and risk assessment

**Start here for**: Complete understanding of the problem and solution.

### 2. [mib-dashboard-diagrams.md](./mib-dashboard-diagrams.md)
**Visual Architecture** - ASCII diagrams

Side-by-side comparison diagrams:
- Current broken architecture vs proposed robust architecture
- Detailed data flow comparisons (what happens when user clicks a tab)
- Object classification logic flowcharts
- Storage structure comparisons
- Query template generation examples

**Start here for**: Visual understanding of the architecture.

### 3. [implementation-guide.md](./implementation-guide.md)
**Code Examples** - Concrete implementation

Actual code snippets for each phase:
- Enhanced parser with object classification
- Storage layer with categorized columns
- Dashboard generator with spec creation
- API handlers with new endpoints
- Frontend refactor examples

**Start here for**: Beginning implementation.

### 4. [testing-strategy.md](./testing-strategy.md)
**Testing Plan** - 90%+ coverage

Comprehensive testing approach:
- Unit tests with actual test code
- Integration tests
- End-to-end test scripts
- Performance benchmarks
- Manual testing checklist
- CI/CD pipeline configuration

**Start here for**: Ensuring quality and preventing regressions.

## Quick Reference

### Key Concepts

#### Object Classes (Semantic Understanding)
```
ObjectClassScalar       - Single global value (e.g., ifNumber)
ObjectClassTableNode    - Table container (not displayed)
ObjectClassEntryNode    - Table row definition (not displayed)
ObjectClassIndexColumn  - Index/key (used for selector, not charted)
ObjectClassStatusColumn - Status/info field (displayed as text)
ObjectClassMetricColumn - Time-series metric (charted)
```

#### Data Flow (Simplified)
```
MIB File
  → Enhanced Parser (classifies objects)
    → Enriched Storage (maintains hierarchy)
      → Dashboard Generator (creates spec)
        → API (returns spec)
          → Frontend (renders from spec)
```

#### Example: IF-MIB Object Classification

| Object | Type | Current Behavior | New Class | New Behavior |
|--------|------|------------------|-----------|--------------|
| ifIndex | Integer32 | Charted (wrong!) | IndexColumn | Used for selector only |
| ifDescr | DisplayString | Sometimes shown | StatusColumn | Displayed as text |
| ifOperStatus | INTEGER | Sometimes charted | StatusColumn | Displayed with enums (up/down) |
| ifInOctets | Counter32 | Charted | MetricColumn | Charted with rate() |

### Implementation Phases

1. **Phase 1** (1-2 days): Enhanced Parser
   - Add object classification
   - Extract enum values
   - Build hierarchies

2. **Phase 2** (1 day): Enhanced Storage
   - Categorize table columns
   - Efficient querying

3. **Phase 3** (2-3 days): Dashboard Generator
   - Generate dashboard specs
   - Create query templates

4. **Phase 4** (1-2 days): Enhanced API
   - New dashboard endpoint
   - Return specs not raw data

5. **Phase 5** (2-3 days): Frontend Refactor
   - Spec-based rendering
   - Remove fragile code

6. **Phase 6** (2-3 days): Testing
   - 90%+ code coverage
   - E2E validation

7. **Phase 7** (1 day): Documentation
   - API docs
   - Developer guide

**Total Effort**: 10-15 days (2-3 weeks for 1 developer)

### Before Starting Implementation

1. **Review** [mib-dashboard-redesign.md](./mib-dashboard-redesign.md) - Understand the full scope
2. **Study** [mib-dashboard-diagrams.md](./mib-dashboard-diagrams.md) - Visualize the architecture
3. **Setup** feature flag for gradual rollout
4. **Create** detailed task breakdown for Phase 1
5. **Begin** with parser enhancement

### Success Metrics

**Functional**:
- [ ] CISCO-BGP4-MIB tab shows BGP objects (not IF-MIB)
- [ ] Instance selector populates with interfaces
- [ ] ifIndex is NOT charted
- [ ] ifOperStatus shows "up" not "1"
- [ ] Charts show correct data for selected instance

**Non-Functional**:
- [ ] Dashboard loads < 2 seconds
- [ ] Instance selector populates < 1 second
- [ ] Code coverage > 90%
- [ ] No console errors

**Technical Debt**:
- [ ] Remove pattern-based table detection
- [ ] Remove ~500 lines of fragile frontend code
- [ ] Clear separation of concerns

## Architecture Decisions

### Why Not Pattern-Based Detection?

**Current approach** (broken):
```go
// Checks if OID is long enough - arbitrary threshold
if len(parts) < 10 {
    continue
}

// Checks if parent ends in .1 - fragile assumption
if parts[len(parts)-2] == "1" {
    // Assume it's a column
}
```

**Problems**:
- Fails for short OIDs
- Fails for non-standard table structures
- No semantic understanding
- High maintenance burden

**Better approach** (proposed):
```go
// Use metadata from parser
if oid.IsColumn && oid.IsIndex {
    // This is an index column
} else if oid.IsColumn && oid.IsCounter() {
    // This is a metric column
}
```

### Why Dashboard Spec?

**Current approach**: Frontend receives raw OID list, tries to figure out what to display

**Problems**:
- Frontend has business logic
- Duplicated categorization code
- Hard to maintain
- Inconsistent behavior

**Better approach**: Backend generates explicit dashboard specification

**Benefits**:
- Single source of truth (backend)
- Frontend is pure rendering
- Easy to test
- Consistent behavior
- Can preview/validate spec

### Why Semantic Classification?

**Current approach**: Heuristic based on type name
```javascript
if (type == "Integer" || type == "DisplayString") {
    // It's a status object
} else {
    // Chart it! (Wrong for ifIndex!)
}
```

**Better approach**: Explicit classification during parsing
```go
// Parser understands MIB structure
if isInIndexList {
    objectClass = IndexColumn
} else if isCounter || isGauge {
    objectClass = MetricColumn
} else {
    objectClass = StatusColumn
}
```

## API Changes

### New Endpoint

```
GET /api/devices/{deviceId}/mibs/{mibName}/dashboard

Returns: DashboardSpec (complete rendering specification)
```

### Enhanced Endpoint

```
GET /api/mibs/{mibId}/tables

Returns: Enhanced table metadata with categorized columns
```

### Deprecated Behavior

- Pattern-based table detection will be removed
- Raw OID lists will be replaced with structured specs

## Migration Path

### Phase 1-3: Backend Only
- No frontend changes
- Feature flag disabled
- Build and test backend components

### Phase 4: API Layer
- New endpoints available
- Old endpoints still work
- Feature flag enables new endpoints

### Phase 5: Frontend
- Refactor frontend to use new endpoints
- Feature flag toggles between old/new UI
- Both versions work

### Phase 6: Testing
- Validate new architecture
- Compare old vs new behavior
- Performance testing

### Phase 7: Cutover
- Enable feature flag by default
- Monitor for issues
- Remove old code after validation period

## Common Pitfalls to Avoid

1. **Don't skip the parser enhancement**
   - Tempting to fix frontend only
   - Won't solve root cause
   - Will fail on other MIBs

2. **Don't use pattern matching**
   - Already proven inadequate
   - Will break on edge cases
   - Not maintainable

3. **Don't mix old and new code**
   - Use feature flag for clean separation
   - Don't try to make them coexist
   - Complete one phase before starting next

4. **Don't skip testing**
   - 90% coverage is required
   - E2E tests prevent regressions
   - Manual testing catches edge cases

5. **Don't rush Phase 3**
   - Dashboard generator is the most complex
   - Get it right, everything else follows
   - Budget extra time for edge cases

## Questions?

For questions about:
- **Architecture**: See [mib-dashboard-redesign.md](./mib-dashboard-redesign.md)
- **Data flow**: See [mib-dashboard-diagrams.md](./mib-dashboard-diagrams.md)
- **Implementation**: See [implementation-guide.md](./implementation-guide.md)
- **Testing**: See [testing-strategy.md](./testing-strategy.md)

## Related Documentation

- `/docs/api/` - API documentation (to be updated in Phase 7)
- `/docs/development/` - Developer guide (to be updated in Phase 7)
- `/internal/mib/` - MIB package source code
- `/internal/webui/` - Web UI source code

## Change Log

- 2025-10-26: Initial architectural design completed
- TBD: Phase 1 implementation started
- TBD: Phase 1 implementation completed
- TBD: Final cutover to new architecture
