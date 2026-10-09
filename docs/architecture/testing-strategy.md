# MIB Dashboard Redesign - Testing Strategy

> **Status: ARCHIVE** · Scope: go-netmon
>
> Test plan for a redesign proposal that was only partly built; not maintained.
> Only the parser test file it names (`internal/mib/parser/parser_enhanced_test.go`)
> exists; the storage, dashboard-generator and integration test files do not.
> For how the project tests today see [../testing.md](../testing.md).

## Overview

This document outlines the testing strategy for the MIB dashboard redesign to ensure 90%+ code coverage and correct functionality.

## Test Categories

### 1. Unit Tests

#### 1.1 Parser Tests

**File**: `internal/mib/parser/parser_enhanced_test.go`

```go
package parser_test

import (
    "testing"
    "github.com/jeremyhahn/go-netmon/internal/mib"
    "github.com/jeremyhahn/go-netmon/internal/mib/parser"
)

func TestDetermineObjectClass_IndexColumn(t *testing.T) {
    // Test that ifIndex is classified as IndexColumn
    p := parser.New("testdata/mibs")
    p.Init()
    defer p.Close()

    oids, err := p.ParseModule("IF-MIB")
    require.NoError(t, err)

    // Find ifIndex
    var ifIndex *mib.OIDMetadata
    for _, oid := range oids {
        if oid.Name == "ifIndex" {
            ifIndex = oid
            break
        }
    }

    require.NotNil(t, ifIndex)
    assert.Equal(t, mib.ObjectClassIndexColumn, ifIndex.ObjectClass)
    assert.True(t, ifIndex.IsIndex)
    assert.False(t, ifIndex.IsChartable)
}

func TestDetermineObjectClass_MetricColumn(t *testing.T) {
    // Test that ifInOctets is classified as MetricColumn
    p := parser.New("testdata/mibs")
    p.Init()
    defer p.Close()

    oids, err := p.ParseModule("IF-MIB")
    require.NoError(t, err)

    var ifInOctets *mib.OIDMetadata
    for _, oid := range oids {
        if oid.Name == "ifInOctets" {
            ifInOctets = oid
            break
        }
    }

    require.NotNil(t, ifInOctets)
    assert.Equal(t, mib.ObjectClassMetricColumn, ifInOctets.ObjectClass)
    assert.False(t, ifInOctets.IsIndex)
    assert.True(t, ifInOctets.IsChartable)
    assert.Equal(t, "line", ifInOctets.ChartType)
}

func TestDetermineObjectClass_StatusColumn(t *testing.T) {
    // Test that ifOperStatus is classified as StatusColumn
    p := parser.New("testdata/mibs")
    p.Init()
    defer p.Close()

    oids, err := p.ParseModule("IF-MIB")
    require.NoError(t, err)

    var ifOperStatus *mib.OIDMetadata
    for _, oid := range oids {
        if oid.Name == "ifOperStatus" {
            ifOperStatus = oid
            break
        }
    }

    require.NotNil(t, ifOperStatus)
    assert.Equal(t, mib.ObjectClassStatusColumn, ifOperStatus.ObjectClass)
    assert.False(t, ifOperStatus.IsChartable)
    assert.True(t, ifOperStatus.IsDisplayable)
    assert.NotEmpty(t, ifOperStatus.EnumValues)
    assert.Equal(t, "up", ifOperStatus.EnumValues[1])
    assert.Equal(t, "down", ifOperStatus.EnumValues[2])
}

func TestExtractEnumValues(t *testing.T) {
    p := parser.New("testdata/mibs")
    p.Init()
    defer p.Close()

    oids, err := p.ParseModule("IF-MIB")
    require.NoError(t, err)

    var ifAdminStatus *mib.OIDMetadata
    for _, oid := range oids {
        if oid.Name == "ifAdminStatus" {
            ifAdminStatus = oid
            break
        }
    }

    require.NotNil(t, ifAdminStatus)
    assert.Len(t, ifAdminStatus.EnumValues, 3)
    assert.Equal(t, "up", ifAdminStatus.EnumValues[1])
    assert.Equal(t, "down", ifAdminStatus.EnumValues[2])
    assert.Equal(t, "testing", ifAdminStatus.EnumValues[3])
}

func TestTableHierarchy(t *testing.T) {
    p := parser.New("testdata/mibs")
    p.Init()
    defer p.Close()

    oids, err := p.ParseModule("IF-MIB")
    require.NoError(t, err)

    var ifDescr *mib.OIDMetadata
    for _, oid := range oids {
        if oid.Name == "ifDescr" {
            ifDescr = oid
            break
        }
    }

    require.NotNil(t, ifDescr)
    assert.Equal(t, "1.3.6.1.2.1.2.2.1.2", ifDescr.OID)
    assert.Equal(t, "1.3.6.1.2.1.2.2.1", ifDescr.EntryOID)
    assert.Equal(t, "1.3.6.1.2.1.2.2", ifDescr.TableOID)
}
```

#### 1.2 Storage Tests

**File**: `internal/mib/storage/storage_enhanced_test.go`

```go
func TestGetTableInfoEnhanced(t *testing.T) {
    // Create temp storage
    tempDir := t.TempDir()
    store, err := storage.New(filepath.Join(tempDir, "test.db"))
    require.NoError(t, err)
    defer store.Close()

    // Load IF-MIB
    p := parser.New("testdata/mibs")
    p.Init()
    defer p.Close()

    moduleInfo, err := p.LoadModule("IF-MIB")
    require.NoError(t, err)
    err = store.StoreModule(moduleInfo)
    require.NoError(t, err)

    oids, err := p.ParseModule("IF-MIB")
    require.NoError(t, err)
    for _, oid := range oids {
        err = store.StoreOID(oid)
        require.NoError(t, err)
    }

    // Test GetTableInfoEnhanced
    tableInfo, err := store.GetTableInfoEnhanced("IF-MIB", "ifTable")
    require.NoError(t, err)

    assert.Equal(t, "ifTable", tableInfo.Name)
    assert.Equal(t, "IF-MIB", tableInfo.Module)
    assert.NotEmpty(t, tableInfo.IndexColumns)
    assert.NotEmpty(t, tableInfo.StatusColumns)
    assert.NotEmpty(t, tableInfo.MetricColumns)

    // Check index column
    assert.Len(t, tableInfo.IndexColumns, 1)
    assert.Equal(t, "ifIndex", tableInfo.IndexColumns[0].Name)
    assert.True(t, tableInfo.IndexColumns[0].IsIndex)

    // Check status columns include ifDescr and ifOperStatus
    statusNames := make(map[string]bool)
    for _, col := range tableInfo.StatusColumns {
        statusNames[col.Name] = true
    }
    assert.True(t, statusNames["ifDescr"])
    assert.True(t, statusNames["ifOperStatus"])

    // Check metric columns include ifInOctets
    metricNames := make(map[string]bool)
    for _, col := range tableInfo.MetricColumns {
        metricNames[col.Name] = true
        assert.True(t, col.IsChartable)
    }
    assert.True(t, metricNames["ifInOctets"])
    assert.True(t, metricNames["ifOutOctets"])
}
```

#### 1.3 Dashboard Generator Tests

**File**: `internal/mib/dashboard/generator_test.go`

```go
func TestGenerateDashboardSpec_IFMIB(t *testing.T) {
    // Setup storage with IF-MIB
    store := setupTestStorage(t, "IF-MIB")
    defer store.Close()

    gen := dashboard.NewGenerator(store)
    spec, err := gen.Generate("IF-MIB")
    require.NoError(t, err)

    assert.Equal(t, "IF-MIB", spec.MIBName)
    assert.NotEmpty(t, spec.Scalars)
    assert.NotEmpty(t, spec.Tables)

    // Check for ifNumber scalar
    var ifNumber *dashboard.ScalarWidget
    for i := range spec.Scalars {
        if spec.Scalars[i].Name == "ifNumber" {
            ifNumber = &spec.Scalars[i]
            break
        }
    }
    require.NotNil(t, ifNumber)
    assert.Equal(t, "status", ifNumber.WidgetType)
    assert.Contains(t, ifNumber.QueryTemplate, "{{device}}")

    // Check for ifTable widget
    var ifTableWidget *dashboard.TableWidget
    for i := range spec.Tables {
        if spec.Tables[i].TableName == "ifTable" {
            ifTableWidget = &spec.Tables[i]
            break
        }
    }
    require.NotNil(t, ifTableWidget)

    // Check instance selector
    assert.Equal(t, "ifindex", ifTableWidget.InstanceSelector.LabelName)
    assert.NotEmpty(t, ifTableWidget.InstanceSelector.InstanceQueryTemplate)

    // Check status fields
    assert.NotEmpty(t, ifTableWidget.StatusFields)
    statusNames := make(map[string]bool)
    for _, field := range ifTableWidget.StatusFields {
        statusNames[field.Name] = true
        assert.Contains(t, field.QueryTemplate, "{{device}}")
        assert.Contains(t, field.QueryTemplate, "{{instance}}")
    }
    assert.True(t, statusNames["ifDescr"])
    assert.True(t, statusNames["ifOperStatus"])

    // Check metric charts
    assert.NotEmpty(t, ifTableWidget.MetricCharts)
    metricNames := make(map[string]bool)
    for _, chart := range ifTableWidget.MetricCharts {
        metricNames[chart.Name] = true
        assert.Contains(t, chart.QueryTemplate, "{{device}}")
        assert.Contains(t, chart.QueryTemplate, "{{instance}}")

        // Counters should use rate()
        if strings.Contains(chart.Type, "Counter") {
            assert.Contains(t, chart.QueryTemplate, "rate(")
        }
    }
    assert.True(t, metricNames["ifInOctets"])
    assert.True(t, metricNames["ifOutOctets"])
}

func TestGenerateDashboardSpec_BGP(t *testing.T) {
    store := setupTestStorage(t, "CISCO-BGP4-MIB")
    defer store.Close()

    gen := dashboard.NewGenerator(store)
    spec, err := gen.Generate("CISCO-BGP4-MIB")
    require.NoError(t, err)

    assert.Equal(t, "CISCO-BGP4-MIB", spec.MIBName)

    // Should have BGP peer table, not IF-MIB objects
    for _, table := range spec.Tables {
        assert.Contains(t, table.TableName, "bgp", "Table name should contain 'bgp'")
    }
}

func TestQueryTemplateGeneration(t *testing.T) {
    store := setupTestStorage(t, "IF-MIB")
    defer store.Close()

    gen := dashboard.NewGenerator(store)

    tests := []struct {
        name       string
        oid        string
        useRate    bool
        labelName  string
        labelValue string
        expected   string
    }{
        {
            name:     "scalar without rate",
            oid:      "1.3.6.1.2.1.2.1.0",
            useRate:  false,
            expected: `snmp_ifNumber{device="{{device}}"}`,
        },
        {
            name:       "column with instance",
            oid:        "1.3.6.1.2.1.2.2.1.10",
            useRate:    false,
            labelName:  "ifindex",
            labelValue: "{{instance}}",
            expected:   `snmp_ifInOctets{device="{{device}}",ifindex="{{instance}}"}`,
        },
        {
            name:       "counter with rate",
            oid:        "1.3.6.1.2.1.2.2.1.10",
            useRate:    true,
            labelName:  "ifindex",
            labelValue: "{{instance}}",
            expected:   `rate(snmp_ifInOctets{device="{{device}}",ifindex="{{instance}}"}[5m])`,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            result := gen.buildQueryTemplate(tt.oid, tt.useRate, tt.labelName, tt.labelValue)
            assert.Equal(t, tt.expected, result)
        })
    }
}
```

### 2. Integration Tests

#### 2.1 API Integration Tests

**File**: `internal/webui/api/mib_dashboard_integration_test.go`

```go
func TestDashboardEndpoint_IFMIB(t *testing.T) {
    // Setup test server with database
    server := setupTestServer(t)
    defer server.Close()

    // Import IF-MIB
    importMIB(t, server, "IF-MIB")

    // Create test device
    deviceID := createTestDevice(t, server, "test-router", "203.0.113.1")

    // Test dashboard endpoint
    resp := httptest.NewRecorder()
    req := httptest.NewRequest("GET", fmt.Sprintf("/api/devices/%d/mibs/IF-MIB/dashboard", deviceID), nil)
    server.ServeHTTP(resp, req)

    assert.Equal(t, http.StatusOK, resp.Code)

    var spec dashboard.DashboardSpec
    err := json.Unmarshal(resp.Body.Bytes(), &spec)
    require.NoError(t, err)

    assert.Equal(t, "IF-MIB", spec.MIBName)
    assert.NotEmpty(t, spec.Tables)

    // Verify no IF-MIB objects in BGP dashboard
    resp = httptest.NewRecorder()
    req = httptest.NewRequest("GET", fmt.Sprintf("/api/devices/%d/mibs/CISCO-BGP4-MIB/dashboard", deviceID), nil)
    server.ServeHTTP(resp, req)

    assert.Equal(t, http.StatusOK, resp.Code)

    var bgpSpec dashboard.DashboardSpec
    err = json.Unmarshal(resp.Body.Bytes(), &bgpSpec)
    require.NoError(t, err)

    // Ensure no ifTable in BGP spec
    for _, table := range bgpSpec.Tables {
        assert.NotEqual(t, "ifTable", table.TableName)
    }
}
```

### 3. End-to-End Tests

#### 3.1 Full Dashboard Flow Test

```bash
#!/bin/bash
# e2e_test.sh

set -e

# Start test environment.
#
# Through the make target, never "docker compose -f docker-compose.test.yml"
# directly: with no -p, compose infers the project from the directory --
# "go-netmon", which is the project a DEPLOYED stack occupies -- so "up"
# recreates the live prometheus against the test configuration and the "down"
# at the bottom of this script deletes the deployment's volumes. The target
# runs test/integration/stack.sh, which pins a project of its own and refuses
# to start if it is the directory default.
#
# It also waits for health rather than sleeping: --wait blocks until every
# started service reports healthy, and fails if any does not.
make integration-up

# Import MIBs
curl -X POST http://localhost:8080/api/mibs/import \
  -H "Content-Type: application/json" \
  -d '{"module": "IF-MIB"}'

# Create device
DEVICE_ID=$(curl -X POST http://localhost:8080/api/devices \
  -H "Content-Type: application/json" \
  -d '{"name": "test-router", "address": "203.0.113.1", "community": "public"}' \
  | jq -r '.id')

# Test dashboard endpoint
DASHBOARD=$(curl -s http://localhost:8080/api/devices/$DEVICE_ID/mibs/IF-MIB/dashboard)

# Verify structure
echo "$DASHBOARD" | jq -e '.tables[0].instance_selector' > /dev/null
echo "$DASHBOARD" | jq -e '.tables[0].status_fields' > /dev/null
echo "$DASHBOARD" | jq -e '.tables[0].metric_charts' > /dev/null

# Verify no ifIndex in charts
CHART_NAMES=$(echo "$DASHBOARD" | jq -r '.tables[0].metric_charts[].name')
if echo "$CHART_NAMES" | grep -q "ifIndex"; then
  echo "ERROR: ifIndex should not be charted"
  exit 1
fi

# Verify ifOperStatus has enum values
ENUM_COUNT=$(echo "$DASHBOARD" | jq -r '.tables[0].status_fields[] | select(.name == "ifOperStatus") | .enum_values | length')
if [ "$ENUM_COUNT" -lt 3 ]; then
  echo "ERROR: ifOperStatus should have enum values"
  exit 1
fi

echo "E2E test passed!"

# Cleanup
make integration-down
```

### 4. Performance Tests

#### 4.1 Dashboard Generation Benchmark

```go
func BenchmarkDashboardGeneration(b *testing.B) {
    store := setupBenchStorage(b, "IF-MIB")
    defer store.Close()

    gen := dashboard.NewGenerator(store)

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        _, err := gen.Generate("IF-MIB")
        if err != nil {
            b.Fatal(err)
        }
    }
}

func BenchmarkTableMetadata(b *testing.B) {
    store := setupBenchStorage(b, "IF-MIB")
    defer store.Close()

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        _, err := store.GetTableInfoEnhanced("IF-MIB", "ifTable")
        if err != nil {
            b.Fatal(err)
        }
    }
}
```

### 5. Frontend Tests

#### 5.1 Dashboard Rendering Tests

```javascript
// internal/webui/static/js/tests/dashboard_test.js

describe('Dashboard Spec Rendering', () => {
    let app;

    beforeEach(() => {
        app = new App();
    });

    it('renders instance selector from spec', async () => {
        const spec = {
            tables: [{
                table_name: 'ifTable',
                instance_selector: {
                    label_name: 'ifindex',
                    display_label: 'Interface',
                    instance_query_template: 'group by (ifindex) (...)'
                },
                status_fields: [],
                metric_charts: []
            }]
        };

        await app.renderDashboardFromSpec(spec);

        const selector = document.querySelector('.master-instance-selector');
        expect(selector).toBeTruthy();
    });

    it('does not chart index columns', async () => {
        const spec = {
            tables: [{
                table_name: 'ifTable',
                instance_selector: { /* ... */ },
                status_fields: [],
                metric_charts: [
                    { name: 'ifInOctets', chart_type: 'line' },
                    { name: 'ifOutOctets', chart_type: 'line' }
                ]
            }]
        };

        await app.renderDashboardFromSpec(spec);

        // Should have 2 charts, not including ifIndex
        const charts = document.querySelectorAll('.chart-container');
        expect(charts.length).toBe(2);

        // Verify no chart for ifIndex
        const chartIds = Array.from(charts).map(c => c.id);
        expect(chartIds).not.toContain(expect.stringContaining('ifIndex'));
    });

    it('displays enum values for status fields', async () => {
        const spec = {
            tables: [{
                status_fields: [{
                    name: 'ifOperStatus',
                    enum_values: { '1': 'up', '2': 'down' },
                    query_template: '...'
                }]
            }]
        };

        await app.renderDashboardFromSpec(spec);

        // Simulate query result
        app.handleStatusUpdate('ifOperStatus', 1);

        const statusElement = document.querySelector('#status-ifOperStatus');
        expect(statusElement.textContent).toBe('up');
    });
});
```

## Test Coverage Goals

### Component Coverage Targets

| Component | Target Coverage | Priority |
|-----------|----------------|----------|
| Parser (enhanced) | 95% | Critical |
| Storage (enhanced) | 90% | High |
| Dashboard Generator | 95% | Critical |
| API Handlers | 85% | High |
| Frontend (spec rendering) | 80% | Medium |

### Critical Test Scenarios

1. **Object Classification**:
   - Index columns correctly identified
   - Status columns correctly identified
   - Metric columns correctly identified
   - Edge cases: augmented tables, implied indices

2. **Dashboard Generation**:
   - IF-MIB generates correct dashboard
   - CISCO-BGP4-MIB generates correct dashboard
   - No cross-contamination between MIBs
   - Query templates correctly formatted

3. **Instance Selection**:
   - Instance selector populates from Prometheus
   - Selecting instance updates all charts
   - Multiple tables in same MIB work correctly

4. **Enum Handling**:
   - Enum values extracted from MIB
   - Enum values displayed in UI
   - Numeric fallback when enum missing

## Continuous Integration

### CI Pipeline

```yaml
# .github/workflows/mib-dashboard-tests.yml
name: MIB Dashboard Tests

on: [push, pull_request]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v2

      - name: Setup Go
        uses: actions/setup-go@v2
        with:
          go-version: '1.21'

      - name: Install MIB dependencies
        run: |
          sudo apt-get update
          sudo apt-get install -y snmp libsmi2-dev

      - name: Run unit tests
        run: make test

      - name: Run integration tests
        run: make integration-test

      - name: Generate coverage report
        run: make coverage

      - name: Upload coverage
        uses: codecov/codecov-action@v2
        with:
          files: ./coverage.out

      - name: Run E2E tests
        run: ./scripts/e2e_test.sh
```

## Manual Testing Checklist

Before marking complete, manually verify:

- [ ] Clicking IF-MIB tab shows only IF-MIB objects
- [ ] Clicking CISCO-BGP4-MIB tab shows only BGP objects
- [ ] Instance selector populates with interfaces
- [ ] Selecting instance updates all status fields
- [ ] Selecting instance updates all charts
- [ ] ifIndex is NOT shown as a chart
- [ ] ifOperStatus shows "up"/"down" not "1"/"2"
- [ ] Charts show correct data for selected instance
- [ ] No console errors
- [ ] Page loads in < 2 seconds
- [ ] Instance selector populates in < 1 second

## Regression Prevention

### Before Merge Checklist

1. All unit tests pass
2. All integration tests pass
3. E2E test passes
4. Code coverage >= 90%
5. Manual testing checklist complete
6. Performance benchmarks acceptable
7. No new linter warnings
8. Documentation updated
9. API changes documented
10. Breaking changes noted in CHANGELOG
