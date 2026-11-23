# Friendly Name Labels for SNMP Metrics

> **Status: REFERENCE** · Scope: go-netmon · Last verified: 2026-10-09

Samples from a table column carry the row's index labels (`ifindex="1"`) and
also the row's friendly-name labels (`ifdescr="GigabitEthernet0/0"`,
`ifname="Gi0/0"`), so a series can be queried by name rather than by number.
This page covers where those labels come from and how they are cached. The
metric names themselves are covered in [metric-naming.md](metric-naming.md).

## What is a friendly-name column

A table's columns are either **metric columns** (counters and gauges worth
charting) or **status columns** (`mib.ObjectClassStatusColumn`: text, and
enumerations such as `ifOperStatus`) that describe the row. The status columns
of a table are found while the MIB index is built (`TableIndexCache.sweepStore`
→ `statusColumnsOf`), so the mechanism works for every table in the loaded
MIBs, not just interfaces. See [metrics/mib-index-refresh.md](metrics/mib-index-refresh.md)
for when that index is rebuilt.

A status column's label is its object name lowercased (`ifDescr` → `ifdescr`).
Its value is rendered by `statusColumnInfo.labelValue`: text is formatted with
the column's DISPLAY-HINT, and an integer is rendered as its enumeration label
(`ifOperStatus` 1 → `up`).

## Flow

`ConvertSNMPResultsWithTableIndexes` (`internal/metrics/table_index_cache.go`),
for each poll:

1. `CacheFriendlyNames(device, results)` runs first over **all** results. For
   each result that is a status column of a known table and renders to a
   non-empty value, it stores the value in `friendlyNameCache`.
2. Each numeric result is converted. `extractIndexLabels` decodes the index
   labels from the OID against the table's `INDEX` clause, then adds a label for
   every status column of the table that has a cached value for that row.
3. Device tags are merged ([metrics/sample-labels.md](metrics/sample-labels.md))
   and the sample is named after the MIB object it instantiates.
4. `ConvertStatusColumnsToInfoMetrics` publishes the `snmp_*_info` series.

Only the MIB-aware converter adds friendly-name labels. The MIB-less converters
(`ConvertSNMPResultsToSamples`, `ConvertSNMPResultsWithInterfaceNames`) do not
use this cache.

## The cache

The cache holds current inventory, not history (#54). `TableIndexCache.friendly`
is per device: a `sync.Map` from device name to that device's names, which are
one immutable map (table entry OID → row index and label name → value)
published by atomic pointer, next to a per-device writer mutex
(`internal/metrics/friendly_names.go`). It is the only mutable state left on
the cache; the MIB-derived half is an immutable index swapped atomically.

For example, device `router1`, table `.1.3.6.1.2.1.2.2.1`, row `1`, label
`ifdescr` → `GigabitEthernet0/0`.

Retention:

- **A poll replaces its device's names for every table it walked.** A row
  that disappeared from the table loses its name with the next poll, and an
  index reused by a different row carries only the name the new row reported,
  never the old one. A table the poll did not return at all keeps its names.
- **A device the poller retires loses its names.** `Poller.RemoveDevice`
  calls the exporter's optional `metrics.DeviceForgetter` capability after the
  device's last commit, so no later poll can put them back. It is the only way
  a device leaves the poller: the web UI's device deletion and configuration
  apply reach it through the poller API.

Readers never block: a sample is labeled from the device's names as last
published. Writers contend only with polls of the same device. (Entity
sensors keep nothing between polls at all; see
[metrics/entity-sensors.md](metrics/entity-sensors.md).)

## Example

```
snmp_ifInOctets{device="router1", oid=".1.3.6.1.2.1.2.2.1.10.1", ifindex="1", ifdescr="GigabitEthernet0/0", ifname="Gi0/0"} 12345678
```

Query by friendly name:

```promql
snmp_ifInOctets{device="router1", ifdescr="GigabitEthernet0/0"}
snmp_ifInOctets{device="router1", ifdescr=~"GigabitEthernet.*"}
```

Query by index (still works):

```promql
snmp_ifInOctets{device="router1", ifindex="1"}
```

## Compatibility

Adding friendly-name labels only adds labels; existing selectors on index
labels keep matching.

## Tests

`internal/metrics/table_index_cache_test.go`: `TestCacheFriendlyNames`,
`TestExtractIndexLabelsWithFriendlyNames`, `TestConvertSNMPResultsWithFriendlyNames`,
`TestExtractColumnID` and `TestCacheFriendlyNamesWithInvalidResults`.
Retention: `internal/metrics/friendly_names_test.go` (removed row, reused
index, unwalked table, forgotten device, concurrent devices under `-race`) and
`internal/poller/forget_test.go` (`RemoveDevice` evicts the device).
