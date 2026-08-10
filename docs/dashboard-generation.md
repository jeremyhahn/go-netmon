# Dashboard Generation

The `mib-importer` CLI tool can automatically generate Grafana dashboards from SNMP MIB metadata stored in the MIB storage database.

## Overview

The dashboard generator:
- Reads MIB metadata from PebbleDB storage
- Identifies pollable OIDs (scalars and table columns)
- Generates appropriate panel types based on OID data types
- Creates Prometheus queries with proper rate functions for counters
- Organizes panels in a logical layout
- Produces Grafana-compatible JSON dashboard files

## Commands

### generate

Generate a dashboard for a specific MIB module.

```bash
mib-importer dashboard generate <module-name> [options]
```

**Options:**
- `--storage-path <path>` - Path to MIB storage (default: `/app/data/mib-storage`)
- `--output-dir <dir>` - Output directory (default: `./grafana/provisioning/dashboards/_generated`)
- `--overwrite` - Overwrite existing dashboard
- `--validate` - Validate JSON before writing (default: `true`)

**Example:**
```bash
mib-importer dashboard generate IF-MIB \
  --storage-path /app/data/mib-storage \
  --output-dir ./grafana/dashboards
```

### generate-all

Generate dashboards for all loaded MIB modules.

```bash
mib-importer dashboard generate-all [options]
```

**Options:**
- `--storage-path <path>` - Path to MIB storage
- `--output-dir <dir>` - Output directory
- `--overwrite` - Overwrite existing dashboards
- `--validate` - Validate JSON before writing (default: `true`)
- `--index` - Generate index.json catalog (default: `true`)

**Example:**
```bash
mib-importer dashboard generate-all \
  --output-dir ./grafana/provisioning/dashboards/_generated \
  --overwrite \
  --index
```

**Output:**
```
Generating dashboards for all modules...

[1/5] IF-MIB... OK (15 panels)
[2/5] IP-MIB... OK (28 panels)
[3/5] SNMPv2-MIB... OK (8 panels)
[4/5] CISCO-PROCESS-MIB... skipped (no pollable OIDs)
[5/5] CISCO-MEMORY-POOL-MIB... OK (6 panels)

Generated index.json with 4 dashboards

=== Summary ===
Generated:  4
Skipped:    1
Duration:   2.3s
Output:     ./grafana/provisioning/dashboards/_generated
```

### list

List available MIB modules that can be used for dashboard generation.

```bash
mib-importer dashboard list [options]
```

**Options:**
- `--storage-path <path>` - Path to MIB storage
- `--pollable` - Show pollable OID count for each module
- `--verbose` - Show detailed information

**Example:**
```bash
mib-importer dashboard list --pollable --verbose
```

**Output:**
```
Available modules (3):

IF-MIB                         The MIB module to describe network interfaces
  OIDs: 45
  Loaded: 2025-10-23 14:30:00
  Pollable: 15

IP-MIB                         The MIB module for IP protocol
  OIDs: 120
  Loaded: 2025-10-23 14:30:05
  Pollable: 28

SNMPv2-MIB                     SNMPv2 Management Information Base
  OIDs: 30
  Loaded: 2025-10-23 14:30:10
  Pollable: 8
```

### clean

Remove all generated dashboards from the output directory.

```bash
mib-importer dashboard clean [options]
```

**Options:**
- `--output-dir <dir>` - Output directory to clean
- `--force` - Skip confirmation prompt

**Example:**
```bash
mib-importer dashboard clean --output-dir ./grafana/dashboards --force
```

## Panel Types

The dashboard generator automatically selects appropriate panel types based on OID metadata:

### Counter OIDs
- **Panel Type:** Time Series
- **Query:** `rate(snmp_<ObjectName>{device=~"$device"}[5m])`
- **Features:**
  - Automatic rate calculation
  - Byte counters converted to bits
  - Legend shows device and OID name

### Gauge OIDs
- **Panel Type:** Stat (or Gauge for percentages)
- **Query:** `snmp_<ObjectName>{device=~"$device"}`
- **Features:**
  - Last value display
  - Thresholds for percentages (80%, 90%)
  - Min/Max ranges for percentage values

### String OIDs
- **Panel Type:** Table
- **Query:** `snmp_<ObjectName>{device=~"$device"}`
- **Features:**
  - Sortable columns
  - Device grouping

### Table Columns
- **Organization:** Grouped by table
- **Counter Columns:** Time series panels with rate()
- **Gauge Columns:** Stat panels
- **String Columns:** Combined table panel
- **Features:**
  - Index variable for filtering
  - Multi-series support

## Dashboard Features

### Variables
All dashboards include:
- **device** - Multi-select device variable (populated from `snmp_up` metric)
- **index** - (For tables) Multi-select index variable

### Layout
- **Grid:** 24-column layout
- **Panel Width:** 12 columns (2 panels per row)
- **Panel Height:** 8 units for time series, 6 units for stats
- **Organization:** Scalars first, then tables

### Time Range
- **Default:** Last 6 hours
- **Refresh:** 30 seconds

### Tags
- `snmp` - All SNMP dashboards
- `mib` - MIB-generated dashboards
- `<module-name>` - Specific module (lowercase)

## Integration with Grafana

### Provisioning

Place generated dashboards in Grafana's provisioning directory:

```yaml
# grafana/provisioning/dashboards/dashboards.yaml
apiVersion: 1

providers:
  - name: 'SNMP Dashboards'
    folder: 'SNMP'
    type: file
    disableDeletion: false
    updateIntervalSeconds: 10
    allowUiUpdates: true
    options:
      path: /etc/grafana/provisioning/dashboards/_generated
      foldersFromFilesStructure: false
```

### Directory Structure

```
grafana/
├── provisioning/
│   └── dashboards/
│       ├── dashboards.yaml
│       └── _generated/
│           ├── IF-MIB.json
│           ├── IP-MIB.json
│           ├── SNMPv2-MIB.json
│           └── index.json
```

### Dashboard Index

The `index.json` file provides a catalog of generated dashboards:

```json
{
  "dashboards": [
    {
      "uid": "if-mib",
      "title": "IF-MIB - Network Interface MIB",
      "module": "IF-MIB",
      "panels": 15,
      "generated": "2025-10-23T14:30:15Z"
    }
  ]
}
```

## Prometheus Configuration

go-netmon's own exporter writes these metrics through Prometheus remote write,
already named `snmp_<ObjectName>`; nothing needs relabelling. The rules below
are only for a third-party SNMP exporter whose names you want to match:

```yaml
# prometheus.yml
scrape_configs:
  - job_name: 'snmp'
    static_configs:
      - targets:
        - 203.0.113.1  # SNMP device
    metrics_path: /snmp
    params:
      module: [if_mib]
    relabel_configs:
      - source_labels: [__address__]
        target_label: __param_target
      - source_labels: [__param_target]
        target_label: instance
      - target_label: __address__
        replacement: localhost:9116  # SNMP exporter address
    metric_relabel_configs:
      - source_labels: [__name__]
        regex: '(.+)'
        target_label: __name__
        replacement: 'snmp_${1}'
```

## Troubleshooting

### No Dashboards Generated

**Symptom:** `generate-all` produces no output

**Causes:**
1. No modules in storage
2. Modules have no pollable OIDs
3. Storage path is incorrect

**Solution:**
```bash
# Check modules exist
mib-importer dashboard list --pollable

# Verify storage path
ls -la /app/data/mib-storage
```

### Invalid Dashboard JSON

**Symptom:** Grafana rejects dashboard

**Causes:**
1. Validation was disabled
2. Custom panel options conflict

**Solution:**
```bash
# Regenerate with validation enabled
mib-importer dashboard generate <module> --validate --overwrite
```

### Missing Metrics in Panels

**Symptom:** Panels show "No data"

**Causes:**
1. Prometheus not scraping SNMP exporter
2. Metric prefix mismatch
3. Device label not matching

**Solution:**
1. Verify Prometheus targets: `http://prometheus:9090/targets`
2. Check metric names: `snmp_<ObjectName>` format, e.g. `snmp_ifInOctets`
3. Verify device label in Prometheus queries

### Dashboard UID Conflicts

**Symptom:** Dashboard not updating

**Causes:**
1. Existing dashboard with same UID
2. Dashboard manually created with conflicting UID

**Solution:**
```bash
# Clean and regenerate
mib-importer dashboard clean --force
mib-importer dashboard generate-all --overwrite
```

## Best Practices

### 1. Use Provisioning
Always use Grafana's provisioning feature rather than manual import:
- Enables version control
- Supports automated deployment
- Allows regeneration without conflicts

### 2. Organize by Function
Group related modules in separate folders:
```
dashboards/
├── _generated/
│   ├── interfaces/
│   │   ├── IF-MIB.json
│   │   └── EtherLike-MIB.json
│   └── system/
│       ├── HOST-RESOURCES-MIB.json
│       └── UCD-SNMP-MIB.json
```

### 3. Regular Regeneration
Regenerate dashboards when:
- MIB modules are updated
- New devices are added
- Polling configuration changes

### 4. Customize After Generation
Generated dashboards are a starting point:
- Add custom panels
- Adjust thresholds
- Modify layouts
- Add annotations

### 5. Version Control
Store generated dashboards in Git:
```bash
git add grafana/provisioning/dashboards/_generated/
git commit -m "Update SNMP dashboards"
```

## Examples

### Complete Workflow

```bash
# 1. Import MIBs for device
mib-importer download \
  --device asr1001 \
  --support-list asr1001-supportlist.html

# 2. Parse MIBs into storage (assuming parser is implemented)
# mib-parser import --mibs ./mibs/asr1001 --storage /app/data/mib-storage

# 3. Generate dashboards
mib-importer dashboard generate-all \
  --storage-path /app/data/mib-storage \
  --output-dir ./grafana/provisioning/dashboards/_generated \
  --overwrite

# 4. Verify generated dashboards
ls -l ./grafana/provisioning/dashboards/_generated/

# 5. Deploy to Grafana (via provisioning)
docker-compose restart grafana
```

### Selective Generation

```bash
# Generate only interface-related dashboards
for module in IF-MIB EtherLike-MIB; do
  mib-importer dashboard generate $module \
    --output-dir ./grafana/dashboards/interfaces
done
```

### CI/CD Integration

```bash
#!/bin/bash
# .github/workflows/generate-dashboards.sh

set -e

echo "Generating SNMP dashboards..."

mib-importer dashboard generate-all \
  --storage-path /app/data/mib-storage \
  --output-dir ./grafana/provisioning/dashboards/_generated \
  --overwrite \
  --validate

echo "Dashboard generation complete"

# Commit and push if changes detected
if git diff --quiet grafana/provisioning/dashboards/_generated/; then
  echo "No changes detected"
else
  git add grafana/provisioning/dashboards/_generated/
  git commit -m "chore: regenerate SNMP dashboards [skip ci]"
  git push
fi
```

## Configuration Reference

### Builder Config

The builder uses the following defaults:

```go
&builder.Config{
    PrometheusDataSource: "Prometheus",
    RefreshInterval:      "30s",
    DeviceLabel:          "device",
    OIDLabel:             "oid",
    MetricPrefix:         "snmp_",   // promname.Prefix
}
```

### Metric Naming

A metric is named after the MIB **object**, and the table row it came from is
carried in labels:
- `ifInOctets` → `snmp_ifInOctets`
- one name per object, not one per row
- rows are selected with label matchers such as `ifindex="10"`

Object names cannot be derived from an OID, so the builder takes
`*mib.OIDMetadata` rather than an OID string. See
[metric naming](metric-naming.md) for the full rule and the drift test that
pins it to the exporter.

### Panel Sizing

Default panel dimensions:
- Width: 12 columns (50% of row)
- Height: 8 units (time series), 6 units (stats)
- Spacing: Automatic grid layout

## Related Documentation

- [MIB Storage](./mib-storage.md) - MIB metadata storage
- [SNMP Polling](./snmp-polling.md) - SNMP data collection
- [Prometheus Integration](./prometheus.md) - Metric export
