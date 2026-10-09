# MIB System Architecture

> **Status: REFERENCE** · Scope: go-netmon · Last verified: 2026-10-09

A map of the MIB packages and how data moves through them. Details of each
step are in [MIB_PARSING_AND_STORAGE.md](MIB_PARSING_AND_STORAGE.md); how the
poller and web UI each keep a catalog is in
[mib-catalogs-and-on-demand-loading.md](mib-catalogs-and-on-demand-loading.md).

## Components

| Package | Role |
|---------|------|
| `internal/mib` | Shared data model (`OIDMetadata`, `ModuleInfo`, `TableInfo`) and `FormatValue` |
| `internal/mib/parser` | gosmi wrapper: loads MIB files, produces `OIDMetadata` and `TableInfo` |
| `internal/mib/storage` | PebbleDB store of parsed metadata |
| `internal/mib/loader` | Parses modules and writes them to storage; `EnsureModule`, `EnsureModuleCurrent`, `Revision` |
| `internal/mib/selector` | Chooses which stored objects a device polls (`Plan`/`Polled`, `Selector.SelectOIDs`) |
| `internal/mib/profiles` | Built-in profiles and a manager that converts a profile into a selector request |
| `internal/mib/importer` | Support-list parsing and MIB download for `mib-importer` |
| `internal/grafana` | Dashboard generation from stored MIBs |

```
MIB files (mibs/standard, mibs/cisco, mibs/vendor, per-device dirs, uploads)
        │
        ▼
   parser (gosmi)  ──►  loader  ──►  storage (PebbleDB)
                                          ▲   │
        poller ───── selector / profiles ─┘   │
        web UI ───── API handlers ────────────┘
        exporter ─── metric names and index labels built from storage
```

The poller and the web UI each open their own PebbleDB catalog, because Pebble
admits one writer per directory. The web UI loads uploaded MIBs into its catalog;
the poller loads the modules a device is assigned into its own on demand.

## Pipeline

1. **File loading.** `Parser.LoadModule` has gosmi load the module and resolve
   its IMPORTS through the search paths.
2. **Node parsing.** `Parser.ParseModule` walks the module and builds an
   `OIDMetadata` for each node: OID, name, module, type, access, status,
   description, units, enum values, range bounds, display hint, parent, table
   hierarchy and index information, object class and chartability. Objects with
   no OID are discarded and counted.
3. **Storage.** `Loader.LoadModule` parses first, then replaces the stored
   module: the module record, each object (`StoreOID`), and each table
   (`StoreTable`). `StoreOID` writes the record, a name entry and a module-index
   entry in one atomic batch.
4. **Querying.** `GetOID`, `GetOIDByName`, `ListOIDs(prefix)`,
   `ListModuleOIDs(module)`, `GetPollableOIDs(module)`, then selection.

## Storage keys

| Prefix | Example | Value |
|--------|---------|-------|
| `oid:` | `oid:1.3.6.1.2.1.2.2.1.10` | `OIDMetadata` JSON |
| `name:` | `name:IF-MIB:ifInOctets` | the OID key |
| `midx:` | `midx:IF-MIB:1.3.6.1.2.1.2.2.1.10` | the OID key (per-module index) |
| `midxdone:` | `midxdone:IF-MIB` | marker that the module's index is built |
| `mod:` | `mod:IF-MIB` | `ModuleInfo` JSON |
| `tbl:` | `tbl:IF-MIB:ifTable` | `TableInfo` JSON |

`ListOIDs("1.3.6.1.2.1.1")` scans the range `["oid:1.3.6.1.2.1.1",
"oid:1.3.6.1.2.1.1/")` and matches whole sub-identifiers, so it returns the
`system` subtree without `1.3.6.1.2.1.10` or `1.3.6.1.2.1.11`.

## Whether an object is polled

Two layers apply, and they are not the same test.

**Per object (`OIDMetadata.IsPollable`).** True when the object has an OID, is
`read-only` or `read-write`, has status `current`, and is not a table or table
entry. Storage's `GetPollableOIDs` filters with this.

```
has an OID?  ──no──► not pollable
   │ yes
readable (read-only / read-write)?  ──no──► not pollable
   │ yes
status current?  ──no──► not pollable
   │ yes
table or table entry?  ──yes──► not pollable (its columns are)
   │ no
pollable
```

**Per device (`selector.Plan` / `selector.Polled`).** For a device with assigned
modules, the poller reads those modules' objects and applies the selection plan.
It fetches scalars with GET (they must be readable and current), walks table
nodes (the node is `not-accessible` in practice, so readability is not tested
for it), skips columns and entry nodes already covered by a walked table, and
then applies the module's filters and `max_oids` cap. Every rejection carries a
reason (for example `not-current`, `covered-by-table-walk`,
`excluded-by-filter`, `capped-by-max-oids`, `unresolved-oid`), and the web UI
shows the same plan the poller uses.

A device with no assigned modules uses its profile (`default` when unset):
`profiles.Manager.SelectOIDsForProfile` builds a `selector.DeviceProfile`
(modules, include/exclude patterns on OID and name, `IncludeTables`, `MaxOIDs`)
and calls `Selector.SelectOIDs`.

## Type classification

| Method | Types |
|--------|-------|
| `IsCounter()` | `Counter32`, `Counter64`, `Counter`, `TimeTicks` |
| `IsGauge()` | `Gauge32`, `Gauge`, `Integer32`, `Integer`, `Unsigned32` |
| `IsString()` | `OctetString`, `DisplayString` |

Other types (`ObjectIdentifier`, `Bits`, ...) match none of these. The Prometheus
exporter uses the classification to choose a metric type; metric names follow
[metric-naming.md](metric-naming.md).

## Tables

`TableInfo` for `ifTable` holds the table OID (`1.3.6.1.2.1.2.2`), the entry OID
(`1.3.6.1.2.1.2.2.1`), the index column names (`ifIndex`), resolved
`IndexColumns`, whether the index is `IMPLIED`, and the column OIDs in order.

A table is polled by walking the table node. Each returned instance carries the
row index as its OID suffix (for example `1.3.6.1.2.1.2.2.1.10.3` is
`ifInOctets` for `ifIndex` 3), and the exporter turns the index into labels using
`IndexColumns`.

## Concurrency

- `Storage` guards its operations with a `sync.RWMutex`: writers (store, delete)
  take the exclusive lock and readers take the shared lock. PebbleDB admits one
  writer per directory, hence separate catalogs for the poller and web UI.
- gosmi keeps global state and is not safe for concurrent use; all parser entry
  points take one package-wide lock.
- `Loader.LoadModule` serializes on a mutex, and `EnsureModule` re-checks the
  store under that mutex so concurrent callers load a module once.
  `Loader.Revision()` increases after each completed load; the poller uses it to
  skip rebuilding exporter metadata when nothing has changed.

## Preserved and not preserved

Preserved: OID, name, module, declared type name, access, status, description,
units, parent, table hierarchy (`TableOID`, `EntryOID`), index names and resolved
index columns, `AUGMENTS`, enumeration values, range bounds (outer bounds only),
`DISPLAY-HINT`, object class and chartability, and the module's import list
(names only).

Not preserved: BITS positions and names (only the `Bits` type), `DEFVAL` default
values, textual convention definitions (the name and display hint are kept), and
a resolved import graph.
