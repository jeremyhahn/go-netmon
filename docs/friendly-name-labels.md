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

`TableIndexCache.friendlyNameCache` is a `map[string]string` guarded by an
`RWMutex` (`friendlyMu`). It is the only mutable state left on the cache; the
MIB-derived half is an immutable index swapped atomically.

Key format: `{device}:{entryOID}:{indexValue}:{labelName}`, for example
`router1:.1.3.6.1.2.1.2.2.1:1:ifdescr` → `GigabitEthernet0/0`.

Entries are overwritten on every poll and never evicted, so the cache lives as
long as the process and holds one entry per device × row × status column.
Because lookups are keyed by the row, a name cached from an earlier poll is
used if a later poll does not return that column. (Entity sensors deliberately
do the opposite; see [metrics/entity-sensors.md](metrics/entity-sensors.md).)

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
