# Grafana Template Engine

The Grafana Template Engine provides a type-safe, programmatic way to generate Grafana dashboards from SNMP MIB metadata.

## Overview

This package consists of three main components:

1. **Types** (`types.go`) - Complete Grafana dashboard JSON structure
2. **Specification** (`spec.go`) - High-level dashboard builder
3. **Template Engine** (`template.go`) - Rendering and validation
4. **Writer** (`writer.go`) - File I/O and management

## Usage

### Basic Example

```go
package main

import (
    "time"
    "github.com/jeremyhahn/go-netmon/internal/grafana/template"
    "github.com/jeremyhahn/go-netmon/internal/mib"
)

func main() {
    // Create module info
    module := &mib.ModuleInfo{
        Name:        "IF-MIB",
        Description: "Interfaces MIB",
        OIDCount:    25,
        LoadedAt:    time.Now(),
    }

    // Create dashboard specification
    spec := template.NewDashboardSpec(module)

    // Add panels
    spec.AddPanel(template.Panel{
        ID:    1,
        Type:  "graph",
        Title: "Interface Traffic",
        GridPos: &template.GridPos{
            H: 8,
            W: 12,
            X: 0,
            Y: 0,
        },
        Targets: []template.Target{
            {
                Expr:         "rate(ifInOctets{device=~\"$device\"}[5m])",
                RefID:        "A",
                LegendFormat: "{{interface}} In",
            },
        },
    })

    // Create engine and render
    engine := template.New()
    data, err := engine.Render(spec)
    if err != nil {
        panic(err)
    }

    // Write to file
    writer := template.NewWriter("./grafana/provisioning/dashboards/_generated")
    if err := writer.WriteSpec(spec, "if-mib.json"); err != nil {
        panic(err)
    }
}
```

### Dashboard Specification

The `DashboardSpec` provides a high-level interface for building dashboards:

```go
spec := template.NewDashboardSpec(module)

// Customize dashboard
spec.SetTitle("Custom Title")
spec.SetDescription("Custom description")
spec.AddTag("monitoring")

// Add variables
spec.AddVariable(template.Variable{
    Name:       "interface",
    Type:       "query",
    Datasource: "Prometheus",
    Query:      "label_values(ifIndex)",
    Multi:      true,
    IncludeAll: true,
})

// Add panels
spec.AddPanel(panel)
```

### Validation

The engine validates dashboards before rendering:

```go
engine := template.New()

// Validate spec
if err := spec.Validate(); err != nil {
    // Handle validation error
}

// Render (includes validation)
data, err := engine.Render(spec)
```

### File Management

The `Writer` provides file management operations:

```go
writer := template.NewWriter("./output")

// Write single dashboard
writer.Write(dashboard, "dashboard.json")

// Write multiple dashboards
writer.WriteAll(dashboards, "")

// Generate index
writer.GenerateIndex(dashboards)

// List dashboards
dashboards, _ := writer.ListDashboards()

// Load dashboard
dashboard, _ := writer.LoadDashboard("if-mib.json")

// Clean generated files (except index.json)
writer.Clean("")
```

## Dashboard Structure

### Default Settings

Generated dashboards include:
- Schema version: 39 (Grafana 10+)
- Timezone: "browser"
- Editable: true
- Refresh: "30s"
- Time range: last 6 hours
- Device variable (auto-added)

### Grid Layout

Panels use Grafana's 24-column grid system:
- Width: 1-24 columns
- Height: 1+ rows
- Position: (x, y) coordinates

### Variables

Device variable is automatically added:
- Name: "device"
- Type: query
- Datasource: Prometheus
- Multi-select: enabled
- Include all: enabled
- Query: `label_values(snmp_up, device)`

## Testing

Run tests:
```bash
make test-grafana-template
```

Generate coverage:
```bash
make coverage-grafana-template
```

## File Structure

```
grafana/provisioning/dashboards/
├── _generated/              # Auto-generated dashboards
│   ├── .gitignore          # Ignore all *.json except index
│   ├── index.json          # Dashboard catalog
│   ├── if-mib.json         # Example dashboard
│   └── ip-mib.json         # Example dashboard
└── dashboard.yml           # Grafana provisioning config
```

## Index Format

The `index.json` provides a catalog of all dashboards:

```json
{
  "dashboards": [
    {
      "uid": "if-mib",
      "title": "IF-MIB - Network Interfaces",
      "module": "IF-MIB",
      "panels": 15,
      "generated": "2025-10-23T17:00:00Z"
    }
  ]
}
```

## Error Handling

All functions return errors for:
- Invalid dashboard structure
- Missing required fields
- Duplicate IDs/names
- Grid layout violations
- File I/O failures

Always check errors:
```go
if err := engine.Validate(dashboard); err != nil {
    log.Fatalf("validation failed: %v", err)
}
```

## Best Practices

1. **Validate early**: Check specs before rendering
2. **Use descriptive titles**: Make dashboards easy to identify
3. **Follow grid conventions**: Use standard panel sizes
4. **Add meaningful tags**: Help with organization
5. **Document panels**: Add descriptions for complex visualizations
6. **Test thoroughly**: Validate generated JSON before deployment

## Integration with Grafana

The generated dashboards are automatically provisioned when:
1. Files are placed in `_generated/` directory
2. Grafana provisioning is configured (via `dashboard.yml`)
3. Grafana service is restarted or reloads configuration

Grafana will:
- Load dashboards from `_generated/` directory
- Place them in the "SNMP" folder
- Allow UI updates (configurable)
- Refresh every 10 seconds (configurable)
