# MIB Index Refresh

How the metrics exporter keeps metric names and index labels in step with the
MIB modules the poller loads on demand.

## The problem it solves

A sample's metric name and index labels come from the MIB object its OID
instantiates. The exporter resolves that from a MIB-derived index built by
sweeping the MIB store.

The store is not static. `mib.auto_load` in `config.yaml` is only a bootstrap;
the modules a device is actually assigned are parsed into the store when its
worker starts (`Poller.hydrateModules` → `loader.EnsureModule`). An index built
once, at exporter construction, therefore describes the auto-loaded modules and
nothing else. Every object of every module loaded afterwards resolves to no MIB
definition and falls back to a raw-OID metric name with no index labels:

    snmp__1_3_6_1_2_1_47_1_1_1_1_16_1026        instead of
    snmp_entPhysicalSerialNum{entphysicalindex="1026"}

The fallback is deliberate — dropping the sample would lose data silently — but
it is meant for OIDs no loaded MIB describes, not for MIBs the index has not
been told about.

## Design

### One immutable index, published by atomic swap

`metrics.mibIndex` holds the whole MIB-derived view: the metric name of every
instantiable object, the index structure of every table, the column-to-table
map, and the status columns that supply friendly-name labels. It is immutable
once published.

A rebuild builds a complete new index off to the side and installs it with one
`atomic.Pointer` store. Readers on the poll path take no lock, so a rebuild
never stalls a poll — which matters because the sweep reads every object in the
store, tens of thousands on a production install.

Only `friendlyNameCache` stays behind a mutex. It is learned from SNMP
responses rather than from MIBs and is written on every poll, so it is genuinely
mutable state rather than a derived snapshot.

Every OID key in the index is normalized to carry a leading dot, and
`TableIndexInfo.EntryOID` is normalized to match, which keeps the three maps
mutually consistent and removes per-lookup normalization.

### Learned tables survive rebuilds

The OID learner discovers tables that no loaded MIB describes. Those are not in
the store, so a rebuild that published only what it swept would drop them. The
cache therefore keeps two sources — the last store sweep and the learner's
overlay — and merges them into every published index. A MIB definition always
wins over a learned table of the same entry OID, because a learned table's index
types are inferred from column names.

### Refresh trigger and coalescing

`loader.Loader` exposes `Revision()`, a counter bumped after every change it
makes to the store's contents. `Poller.hydrateModules` ends by calling
`refreshMetricMetadata`, which rebuilds only when the revision has moved since
the last successful rebuild.

That gives one store sweep per device that actually introduced modules, rather
than one per module (seventeen for a core router), and none at all for a device
whose modules are already loaded. Because the rebuild is the last step of
hydration, a device's *first* poll is already named correctly.

A failed rebuild does not record its revision, so it is retried; and the
previously published index stays in place, so polling keeps the names it had.

The web UI imports MIBs into a different store in a different process. Those
imports cannot refresh this cache and this mechanism does not pretend to.

## Observability

| Metric | Meaning |
| --- | --- |
| `gonetmon_mib_index_objects` | Instantiable MIB objects in the published name index |
| `gonetmon_mib_index_tables` | Tables with usable index metadata |
| `gonetmon_mib_index_rebuilds_total{outcome}` | Rebuild attempts, `success` or `error` |
| `gonetmon_mib_index_last_rebuild_timestamp_seconds` | When the index last changed |
| `gonetmon_metric_name_unresolved_samples_total{device}` | Samples that fell back to a raw-OID name |

The last one says samples are unnamed; the others say why. A high unresolved
count with a low object count means the index is stale. A high unresolved count
with an object count matching the loaded catalog means the MIBs genuinely do not
describe those OIDs.

## Performance

The name lookup is a longest-prefix probe over a precomputed map and allocates
nothing: ~13–16 ns/op, 0 allocs (`BenchmarkObjectNameIndexMetricNameFor`).
`TestLookupPathAllocatesNothing` pins the zero-allocation property for both the
name lookup and the table lookup.

The table lookup is memoized inside the index, so publishing a new index
discards answers derived from the old one automatically — an invalidation that
cannot be forgotten and cannot race with a lookup still writing back a result
from the previous view.

## Tests

- `internal/metrics/mib_index_test.go` — the regression (a module loaded after
  construction), index labels for a late-loaded table, failed-rebuild
  behavior, unresolved fallback and counter, concurrent poll + refresh under
  `-race`, zero-allocation lookups.
- `internal/poller/metric_metadata_test.go` — coalescing: one rebuild per
  device, none when nothing loaded, retry after failure.
- `internal/mib/loader/revision_test.go` — the revision moves only when the
  store's contents change.
