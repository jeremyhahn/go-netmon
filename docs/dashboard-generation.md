# Dashboard Generation

> **Status: REFERENCE** · Scope: go-netmon · Last verified: 2026-10-09

The `mib-importer dashboard` command generates Grafana dashboards from the SNMP
MIB metadata stored in the MIB storage database. The code lives in
`cmd/mib-importer/dashboard.go` (CLI), `internal/grafana/builder` (panels,
queries, variables) and `internal/grafana/template` (dashboard spec and file
writer).

## Overview

The dashboard generator:
- Reads MIB metadata from PebbleDB storage
- Identifies pollable OIDs (scalars and table columns)
- Generates panel types based on OID data types
- Creates Prometheus queries with `rate()` for counters
- Lays panels out two per row, scalars first, then tables
- Produces Grafana-compatible JSON dashboard files, one per module

Pebble admits one writer per directory, so the generator cannot open the
poller's store while the poller is running; see
[mib-catalogs-and-on-demand-loading.md](mib-catalogs-and-on-demand-loading.md).
`make generate-dashboards` runs `generate-all` inside the `go-netmon` container
and prints the HTTP alternative (`POST /api/dashboards/generate`) if the store
is locked.

## Commands

### generate

Generate a dashboard for a specific MIB module.

```bash
mib-importer dashboard generate <module-name> [options]
```

**Options:**
- `--storage-path <path>` - Path to MIB storage (default: `/app/data/mib-storage`)
- `--output-dir <dir>` - Output directory (default: `./grafana/provisioning/dashboards/_generated`)
- `--overwrite` - Overwrite an existing dashboard file (without it, an existing
  `<module>.json` is an error)
- `--validate` - Validate the dashboard spec before writing (default: `true`)
- `--prometheus-url <url>` - Prometheus to query for metric existence (see
  [Filtering by Prometheus](#filtering-by-prometheus))

**Example:**
```bash
mib-importer dashboard generate IF-MIB \
  --storage-path /app/data/mib-storage \
  --output-dir ./grafana/dashboards
```

The command fails if the module is not in storage or has no pollable OIDs. The
file is named `<module>.json`.

### generate-all

Generate dashboards for all modules in storage.

```bash
mib-importer dashboard generate-all [options]
```

**Options:**
- `--storage-path <path>` - Path to MIB storage (default as above)
- `--output-dir <dir>` - Output directory (default as above)
- `--overwrite` - Replace existing dashboards. **This first deletes every
  `*.json` file in the output directory except `index.json`**, then writes the
  new ones. Without it, modules whose file exists are skipped.
- `--validate` - Validate each spec before writing (default: `true`)
- `--index` - Generate `index.json` (default: `true`; written only if at least
  one dashboard was written)
- `--prometheus-url <url>` - as for `generate`

**Example:**
```bash
mib-importer dashboard generate-all \
  --output-dir ./grafana/provisioning/dashboards/_generated \
  --overwrite \
  --index
```

**Output** (illustrative):
```
Generating dashboards for all modules...

[1/4] IF-MIB... OK (15 panels)
[2/4] IP-MIB... OK (28 panels)
[3/4] SNMPv2-MIB... OK (8 panels)
[4/4] CISCO-MEMORY-POOL-MIB... OK (6 panels)

Generated index.json with 4 dashboards

=== Summary ===
Generated:  4
Duration:   2.3s
Output:     ./grafana/provisioning/dashboards/_generated
```

Modules with no pollable OIDs (or no metrics in Prometheus) are left out silently: they do not appear in the numbered list. A
module skipped because its file exists prints `skipped (exists)`; a write error
prints `failed (...)`. `Skipped:` and `Failed:` summary lines appear only when
non-zero. If no module yields a dashboard the command exits with an error.

### list

List the modules in storage.

```bash
mib-importer dashboard list [options]
```

**Options:**
- `--storage-path <path>` - Path to MIB storage
- `--pollable` - Show pollable OID count for each module
- `--verbose` - Show detailed information (name, description, OID count, load
  time; the pollable count is shown only when `--pollable` is also given)
- `--prometheus-url <url>` - accepted, as for `generate`

**Example:**
```bash
mib-importer dashboard list --pollable --verbose
```

**Output** (illustrative):
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
```

Without `--verbose`, each module is one line, `  <name>`, or
`  <name>                         (pollable: <n>)` with `--pollable` (a module
with none prints `(no pollable OIDs)` after the count). Modules are sorted by
name.

### clean

Remove generated dashboards from the output directory.

```bash
mib-importer dashboard clean [options]
```

**Options:**
- `--output-dir <dir>` - Output directory to clean (default as above)
- `--force` - Skip the `Continue? (y/n)` prompt

**Example:**
```bash
mib-importer dashboard clean --output-dir ./grafana/dashboards --force
```

It lists the `*.json` files it will remove and removes them. `index.json` is
not removed.

## Panel Types

The generator selects the panel type from the OID's type and name. Metric names
are `snmp_<ObjectName>` (see [Metric Naming](#metric-naming)); `$device` is the
dashboard's device variable.

### Counter OIDs
- **Panel Type:** Time Series
- **Query:** `rate(snmp_<ObjectName>{device=~"$device"}[5m])`
- **Features:**
  - Octet/byte counters (name contains `octet`, `byte`, `hcin` or `hcout`) are
    multiplied by 8 to give bits per second
  - Legend is `{{device}} - <ObjectName>`

### Gauge OIDs
- **Panel Type:** Stat, or Gauge when the units or name contain `percent`,
  `utilization` or `usage`
- **Query:** `snmp_<ObjectName>{device=~"$device"}` (instant)
- **Features:**
  - Last non-null value
  - Percentages: 0-100 range and yellow/red thresholds at 80 and 90

### Other scalar OIDs (strings)
- **Panel Type:** Table for string types, Stat otherwise
- **Query:** `snmp_<ObjectName>{device=~"$device"}` (instant)

### Table Columns
- **Organization:** Columns are grouped by table. Within a table: counters
  first, then gauges, then everything else.
- **Counter Columns** (by type, or by a name containing `octet`, `pkt`,
  `packet`, `error`, `discard`, `drop`, `collision` or `frame`): Time Series
  with `rate(...[5m])`
- **Gauge Columns:** Time Series (so every row of the table shows)
- **Other Columns:** one full-width table panel, `<table> - Details`, with one
  query per column
- **Features:**
  - Every column query carries `ifname=~"$interface"`, in addition to the
    device matcher; tables without an `ifname` label are unaffected while
    `$interface` is "All"
  - Legend is `{{device}} - {{ifname}} [{{ifindex}}]`

Units are taken from the object's UNITS clause when it has one, and otherwise
guessed from the name (for example `octet` → bits per second for counters,
`packet` → packets per second, `temperature` → Celsius).

## Dashboard Features

### Variables
- **device** - always present; multi-select with "All", from
  `label_values(device)`
- **interface** - added when the dashboard has table columns; multi-select with
  "All", from `label_values(ifname)`

### Layout
- **Grid:** 24 columns
- **Panels:** 12 columns wide and 8 units high (two per row); the combined
  table panel of a table is 24 wide
- **Organization:** scalars first, then tables. The order of tables within a
  dashboard is not guaranteed.

### Time Range
- **Default:** Last 6 hours
- **Refresh:** 30 seconds

### Identity and tags
- **UID:** the module name lowercased, with `_` and spaces replaced by `-`
  (`IF-MIB` → `if-mib`)
- **Title:** `<module> - <module description>`, cut to 100 characters
- **Tags:** `snmp`, `mib`, and the lowercased module name

## Filtering by Prometheus

The Prometheus URL defaults to `http://prometheus:9090`, so unless it is
overridden the generator asks Prometheus whether each
pollable object has a series and builds panels only for those that do; a module
with none produces no dashboard. If Prometheus cannot be queried, it warns and
uses every pollable OID.

The URL is resolved as flag `--prometheus-url` > environment
`NETMON_DASHBOARD_PROMETHEUS_URL` > config file key `dashboard.prometheus_url`
> the default above. A host that cannot resolve `prometheus` gets the warning
and unfiltered dashboards.

## Integration with Grafana

### Provisioning

The repository's `grafana/provisioning/dashboards/dashboard.yml` already has a
file provider for the generated directory:

```yaml
  - name: 'SNMP MIB Dashboards'
    orgId: 1
    folder: 'SNMP'
    type: file
    disableDeletion: false
    updateIntervalSeconds: 10
    allowUiUpdates: true
    options:
      path: /etc/grafana/provisioning/dashboards/_generated
```

`docker-compose.yml` mounts `./grafana/provisioning` into the Grafana container
and mounts `./grafana/provisioning/dashboards/_generated` into the `go-netmon`
container as `/app/dashboards`.

### Directory Structure

```
grafana/
└── provisioning/
    └── dashboards/
        ├── dashboard.yml
        └── _generated/
            ├── IF-MIB.json
            ├── IP-MIB.json
            ├── SNMPv2-MIB.json
            └── index.json
```

### Dashboard Index

`index.json` is a catalog of the generated dashboards. `module` is the
uppercased module tag and `generated` is the time the index was written:

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

## Troubleshooting

### No Dashboards Generated

**Symptom:** `generate-all` fails with "no dashboards generated" or a module is
missing from the output.

**Causes:**
1. No modules in storage
2. Modules have no pollable OIDs
3. Prometheus is reachable and holds no series for the module (see
   [Filtering by Prometheus](#filtering-by-prometheus))
4. Storage path is incorrect, or the poller holds the store's lock

**Solution:**
```bash
# Check modules exist
mib-importer dashboard list --pollable

# Verify storage path
ls -la /app/data/mib-storage
```

### Dashboard Not Overwritten

`generate` refuses to replace an existing file, and `generate-all` skips one,
unless `--overwrite` is given.

### Missing Metrics in Panels

**Symptom:** Panels show "No data"

**Causes:**
1. The device has not been polled for that object
2. Metric name mismatch
3. Device label not matching

**Solution:**
1. Check metric names: `snmp_<ObjectName>` format, e.g. `snmp_ifInOctets`
2. Verify the `device` label values in Prometheus match the dashboard's
   `device` variable

## Examples

### Complete Workflow

```bash
# 1. Download MIBs for a device
mib-importer download \
  --device asr1001 \
  --support-list asr1001-supportlist.html

# 2. Import them (web UI upload, or the poller's mib.paths + mib.auto_load)
#    so they are in the MIB storage

# 3. Generate dashboards
mib-importer dashboard generate-all \
  --storage-path /app/data/mib-storage \
  --output-dir ./grafana/provisioning/dashboards/_generated \
  --overwrite

# 4. Verify generated dashboards
ls -l ./grafana/provisioning/dashboards/_generated/
```

Grafana's file provider rescans the directory every 10 seconds, so a restart is
not needed.

### Selective Generation

```bash
# Generate only interface-related dashboards
for module in IF-MIB EtherLike-MIB; do
  mib-importer dashboard generate $module \
    --output-dir ./grafana/dashboards/interfaces
done
```

## Configuration Reference

### Builder Config

The builder uses the following defaults (`builder.DefaultConfig`):

```go
&builder.Config{
    PrometheusDataSource: "Prometheus",
    RefreshInterval:      "30s",
    DeviceLabel:          "device",   // promname.DeviceLabel
    OIDLabel:             "oid",      // promname.OIDLabel
    MetricPrefix:         "snmp_",    // promname.Prefix
}
```

The CLI sets `PrometheusURL` as described under
[Filtering by Prometheus](#filtering-by-prometheus); `builder.DefaultConfig`
alone leaves it empty, which turns the filter off.

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
