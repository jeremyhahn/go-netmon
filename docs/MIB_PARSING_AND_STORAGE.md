# MIB Parsing and Storage

> **Status: REFERENCE** · Scope: go-netmon · Last verified: 2026-10-09

How go-netmon turns MIB files into stored OID metadata and how that metadata is
queried. Parsing uses **gosmi** (Go bindings for libsmi); storage uses
**PebbleDB**. See [MIB_ARCHITECTURE.md](MIB_ARCHITECTURE.md) for the component
overview and [mib-catalogs-and-on-demand-loading.md](mib-catalogs-and-on-demand-loading.md)
for how the poller and web UI keep their separate catalogs.

## 1. Parser (`internal/mib/parser/parser.go`)

The parser wraps gosmi. gosmi keeps global state and is not safe for concurrent
use, so every parser entry point takes one package-wide lock (`gosmiLibrary`).

```go
type Parser struct {
    paths []string // MIB search paths
}
```

Main methods:

- `Init()`: initializes gosmi and registers the search paths.
- `Reload()`: shuts gosmi down and initializes it again, picking up files added
  to the search paths.
- `LoadModule(name)`: loads a module and returns its `ModuleInfo`.
- `ParseModule(name)`: returns an `OIDMetadata` for every node in the module.
  `ParseModuleWithReport(name)` does the same and also returns a `ParseReport`
  whose `Unresolved` lists the objects that came out without an OID.
- `GetTableInfo(name)`: returns a `TableInfo` for every table node in the module.
- `LoadModuleFromFile(path)`: adds the file's directory to the search path and
  loads the module named after the file (name without extension).
- `GetModules()`: the names of the modules gosmi has loaded.

### 1.1 What `parseNode` extracts

For each node: numeric OID (rendered with `node.RenderNumeric()`; for a table
entry gosmi cannot render, the OID is derived from its first child), name,
module, description, type, units, enum values, range bounds, display hint,
access, status, parent, and for table rows the index and `AUGMENTS` information.
It then sets the node flags (`IsScalar`, `IsTable`, `IsTableEntry`, `IsColumn`),
`ObjectClass`, `IsChartable`/`ChartType`, `IsDisplayable` and `Pollable`.

A `NodeNode` is marked scalar only when it has a type; organizational nodes such
as `system` carry none.

Type, units, enums, ranges and display hint are all read from the **resolved**
gosmi type (`node.SmiType`), never from `node.SmiType.GetRaw()`. `GetRaw` returns
the type exactly as written in the SYNTAX clause, which for a subtyped textual
convention such as `DisplayString (SIZE (0..255))` is an anonymous type with no
name, no `DISPLAY-HINT` and no `UNITS`.

### 1.2 Type, access and status strings

`Type` and `Syntax` hold the declared type name (`Counter32`, `DisplayString`,
`TruthValue`). Only when the resolved type has no name does the parser fall back
to the base type:

| gosmi base type | String |
|-----------------|--------|
| `BaseTypeInteger32` | `Integer32` |
| `BaseTypeInteger64` | `Integer64` |
| `BaseTypeUnsigned32` | `Unsigned32` |
| `BaseTypeUnsigned64` | `Unsigned64` |
| `BaseTypeOctetString` | `OctetString` |
| `BaseTypeObjectIdentifier` | `ObjectIdentifier` |
| `BaseTypeEnum` | `Integer` |
| `BaseTypeBits` | `Bits` |
| anything else | `Unknown` |

Access: `not-accessible`, `read-only`, `read-write`, `accessible-for-notify`,
otherwise `unknown`.
Status: `current`, `deprecated`, `obsolete`, `mandatory`, `optional`, otherwise
`unknown`.

### 1.3 Tables

`GetTableInfo` finds the table nodes of a module and, for each, records the table
OID and name, the description, the entry (row) OID, the index column names and
resolved index columns, whether the `INDEX` clause ends with `IMPLIED`, and the
column OIDs in column order.

### 1.4 Information captured

Besides OID, name, description, type, access, status and table structure, the
parser captures:

1. **Enumeration values**: `OIDMetadata.EnumValues` maps each value to its label
   (`ifOperStatus` gives `{1: up, 2: down, 3: testing, ...}`).
2. **Range constraints**: `RangeMin`/`RangeMax`, collapsed to the outer bounds
   when a type declares several disjoint ranges.
3. **UNITS**, from the type (overlaid with the OBJECT-TYPE's own clause).
4. **DISPLAY-HINT**, resolved through the textual convention.
5. **Textual convention names**: `DisplayString`, not `OctetString`.
6. **Index types**: `IndexColumns []IndexColumn` records each index column's
   name, OID, type, base type, `IMPLIED` flag and fixed length, so composite and
   string-valued indexes decode from their declared types.

`internal/mib.FormatValue` consumes this metadata to render values as text; the
CLI, the web UI and the metrics exporter share it. See [cli.md](cli.md).

### 1.5 Information not captured

1. **BITS positions and names**: only the type name `Bits` is kept.
2. **Default values**: `DEFVAL` is not stored.
3. **Import dependencies**: `ModuleInfo.Imports` holds names only; the graph is
   not resolved or validated (gosmi loads IMPORTS through its search path).
4. **Textual convention definitions**: a TC's name and display hint are kept, but
   the definition is not stored separately.
5. **Augmentation**: `Augments` holds the name of the augmented row; the
   relationship is not otherwise modeled.

Objects whose IMPORTS did not resolve come out with no OID. They are counted
(`UnresolvedCount`), discarded on import, and reported by the web UI.

## 2. Data model (`internal/mib/types.go`)

`OIDMetadata` is the stored record for one object:

- Identity: `OID`, `Name`, `Module`, `Parent`.
- Type: `Type`, `Syntax`, `Units`, `Access`, `Status`, `Description`.
- Structure flags: `IsScalar`, `IsTable`, `IsTableEntry`, `IsColumn`, plus
  `Index`, `IndexColumns`, `Augments`, `TableOID`, `EntryOID`, `IsIndex`,
  `IndexFor`.
- Classification: `ObjectClass` (`scalar`, `table_node`, `entry_node`,
  `index_column`, `status_column`, `metric_column`, `unknown`) and `Pollable`.
- Display: `DisplayHint`, `EnumValues`, `RangeMin`, `RangeMax`, `IsChartable`,
  `IsDisplayable`, `ChartType`.
- Bookkeeping: `CreatedAt`, `UpdatedAt`, `Tags`.

`ModuleInfo`: `Name`, `Path`, `Organization`, `ContactInfo`, `Description`,
`Revision`, `Imports`, `OIDCount`, `LoadedAt`, `Tags`.

`TableInfo`: `OID`, `Name`, `Module`, `Description`, `Indexes`, `Columns` (column
OIDs), `EntryOID`, `IndexColumns`, `Implied`.

### 2.1 Pollability and type classification

`IsPollable()` is true when **all** of these hold:

1. the object has an OID (an object from a module whose IMPORTS did not resolve
   has none, and could never be asked for);
2. access is `read-only` or `read-write`;
3. status is `current`;
4. it is not a table or a table entry (the columns are pollable).

`IsCounter()`: `Counter32`, `Counter64`, `Counter`, `TimeTicks` (counter-like,
monotonically increasing). `IsGauge()`: `Gauge32`, `Gauge`, `Integer32`,
`Integer`, `Unsigned32`. `IsString()`: `OctetString`, `DisplayString`. The
Prometheus exporter uses these for metric type mapping.

`IsPollable()` is the per-object test used by storage queries. What a device
actually polls is decided by the selection rules in section 5.

## 3. Storage (`internal/mib/storage/storage.go`)

`Storage` wraps a PebbleDB directory opened with a 64 MB cache and
`MaxOpenFiles` 500. Pebble admits one writer per directory, which is why the
poller and the web UI each keep their own MIB catalog.

### 3.1 Key schema

| Prefix | Key | Value |
|--------|-----|-------|
| `oid:` | `oid:1.3.6.1.2.1.1.1` | JSON `OIDMetadata` |
| `mod:` | `mod:SNMPv2-MIB` | JSON `ModuleInfo` |
| `tbl:` | `tbl:IF-MIB:ifTable` | JSON `TableInfo` |
| `name:` | `name:SNMPv2-MIB:sysDescr` | the OID key it points at |
| `midx:` | `midx:SNMPv2-MIB:1.3.6.1.2.1.1.1` | the OID key it points at |
| `midxdone:` | `midxdone:SNMPv2-MIB` | presence only |

The module index prefixes deliberately do not begin with `mod`. A prefix such as
`modoid:` would sort inside the `["mod:", "mod;")` range that `ListModules` and
`Count` scan, so tens of thousands of index entries would be walked as if they
were module records.

### 3.2 Operations

- `StoreOID(meta)`: writes three entries in one atomic batch: the record, the
  name entry and the module index entry. The record is keyed by OID, or by
  `module:name` for an object with no numeric OID (the same convention is used
  in the name and module index keys). `DeleteOID` removes all three, so an index
  entry never outlives its record.
- `GetOID(oid)`: direct key lookup.
- `GetOIDByName(module, name)`: two steps, name entry to OID key to record.
- `ListOIDs(prefix)`: range scan over `[key, key+"/")` (see `oidScanBounds`). The
  prefix matches whole sub-identifiers, so `1.3.6.1.2.1.2` selects that subtree
  without also matching `1.3.6.1.2.1.20`.
- `ListModuleOIDs(module)`: reads one module's objects through the `midx:` index.
  Cost is proportional to the module, not the store. An unknown module returns an
  empty result; an empty module name returns `*ModuleRequiredError`.
- `GetPollableOIDs(module)`: `ListModuleOIDs` for a named module, every module
  for `""`, keeping only objects where `IsPollable()` is true.
- `StoreTable` / `StoreModule` and their getters: table records are keyed
  `tbl:<module>:<name>`.
- Also present: `DeleteObject`, `DeleteModule`, `ListModules`, `Count`,
  `GetMIBColumns`, `GetScalars`, `GetMetricColumns`, `GetStatusColumns` and
  `GetTableColumns` (this last one reads the whole store).

Filtering `ListOIDs("")` by module instead of using the index deserializes the
whole store on every request, which is what pushed MIB imports and
`GET /api/mibs/{id}` past their timeouts once about 170 modules were loaded.

### 3.3 Index migration

A store written before the module index existed holds OID records with no
`midx:` entries. The first module lookup against such a store falls back to a
full scan and backfills the index from it. Because the scan has already
deserialized every record, it indexes **every** module it sees, not only the one
requested, and marks the requested module done even when it has no objects. One
scan therefore repairs the whole store; no re-import is needed after an upgrade.

### 3.4 Lookup cost

| Operation | Cost |
|-----------|------|
| `GetOID` | one key lookup |
| `GetOIDByName` | two key lookups |
| `ListOIDs(prefix)` | range scan over the matching objects |
| `ListModuleOIDs(module)` | module index scan over the module's objects |
| `GetPollableOIDs(module)` | module index scan plus the `IsPollable` filter |
| `GetPollableOIDs("")` | full scan plus filter |
| `ListModules()` | range scan over module records |

`BenchmarkModuleLookup` in `internal/mib/storage/storage_bench_test.go` compares
the indexed lookup with a full scan across store sizes; run it for numbers on
your hardware.

## 4. Loading (`internal/mib/loader/loader.go`)

`Loader.LoadModule(name)` serializes on `loadMu` and does:

1. Parse the module and its tables first (`parseModule`). A module that fails to
   parse leaves the existing stored copy untouched.
2. Replace the stored module (`replaceModule`): clear the old module, then store
   the `ModuleInfo`, each OID, and each table. A failure to store an individual
   OID or table is logged and skipped; a failure to store the module record
   returns a `*ModuleLoadError`.
3. Bump the loader's revision, only after the store write completes.
4. Log the number of OIDs and tables loaded.

`EnsureModule` loads a module only if the store does not already hold it.
`EnsureModuleCurrent` additionally reparses when the file is newer than the
stored `LoadedAt`; it is used at startup.

## 5. Selection

### 5.1 Devices with explicit modules

When a device has `modules` assigned, `Poller.startWorker`
(`internal/poller/poller.go`) reads only those modules with
`ListModuleOIDs` and passes the objects to `selectModuleOIDs`
(`internal/poller/selection.go`), which calls `selector.Polled(objects, modules,
moduleFilters)` in `internal/mib/selector/plan.go`.

`selector.Plan` returns a `Decision` (`Object`, `Kind`, `Polled`, `Reason`) for
every object, and `Polled` is `Plan` with the rejected objects dropped. The
poller and the web UI therefore use one rule. Highlights:

- Scalars are fetched with GET and must be readable.
- Tables are fetched by walking the table node. The table node is itself
  `not-accessible` in practice, so readability is not the test for it.
- Columns and entry nodes are redundant once their table is walked
  (`covered-by-table-walk`).
- Per-module filters (include and exclude patterns, `max_oids`) narrow further.
- Objects without an OID are rejected as `unresolved-oid`.

The reasons are `polled`, `module-disabled`, `not-accessible`, `not-current`,
`covered-by-table-walk`, `excluded-by-filter`, `not-included-by-filter`,
`capped-by-max-oids` and `unresolved-oid`.

### 5.2 Devices with a profile

A device with no modules uses its profile (`default` when unset):
`Manager.SelectOIDsForProfile` in `internal/mib/profiles/manager.go` converts the
profile to a `selector.DeviceProfile` and calls `Selector.SelectOIDs`.

`DeviceProfile` has `Modules`, `IncludeOIDs`/`ExcludeOIDs` (regexes on the OID),
`IncludeNames`/`ExcludeNames` (regexes on the name), `IncludeTables` and
`MaxOIDs`. `SelectOIDs`:

1. Collects the pollable OIDs of each listed module (a module that cannot be
   read is skipped); with no modules it takes every module.
2. Drops tables and entries unless `IncludeTables` is set, then applies the
   exclude patterns, then the include patterns.
3. Truncates to `MaxOIDs` when that is greater than zero.

### 5.3 Built-in profiles (`internal/mib/profiles/builtin.go`)

| Profile | Modules | IncludeTables |
|---------|---------|---------------|
| `default` | SNMPv2-MIB, IF-MIB | false |
| `cisco-ios` | SNMPv2-MIB, IF-MIB, IP-MIB, CISCO-PROCESS-MIB, CISCO-MEMORY-POOL-MIB | false |
| `cisco-ios-with-tables` | same as `cisco-ios` | true |
| `cisco-nexus` | same as `cisco-ios` | false |
| `juniper-mx` | SNMPv2-MIB, IF-MIB, IP-MIB | false |
| `arista-eos` | SNMPv2-MIB, IF-MIB, IP-MIB | false |
| `generic-router` | SNMPv2-MIB, IF-MIB, IP-MIB, TCP-MIB, UDP-MIB | false |
| `generic-switch` | SNMPv2-MIB, IF-MIB | false |
| `interfaces-only` | IF-MIB | false |

Every profile also excludes names matching `.*deprecated.*` (most also
`.*obsolete.*`). `generic-switch` includes only names matching `^sys.*` and
`^if.*`, and `interfaces-only` only `^if.*`.

## 6. Web UI endpoints (`internal/webui/api/mib_handler.go`, `mib_import.go`)

- `GET /api/mibs`: lists stored MIBs; `filter=available` (with an optional
  `device`) keeps only modules that hold data.
- `GET /api/mibs/{id}` (`handleGetStoredMIB`): the MIB record plus its objects,
  read from the web UI's catalog with `ListModuleOIDs` and optionally narrowed by
  a query (offset, limit and filters). If the objects cannot be read, the bare
  record is returned.
- `GET /api/mibs/{id}/tables` (`handleGetMIBTables`): `detectTables` infers
  tables from OID shape: an OID with at least ten sub-identifiers whose parent
  ends in `.1` is treated as a column, and its parent's parent as the table.
  Only pollable columns are returned. This is a heuristic on the OID, not the
  parsed `TableInfo`.
- `POST /api/mibs/import` (`handleImportMIB`): accepts an uploaded MIB, writes it
  to the uploaded-MIB directory, parses and loads it, and creates the record.
  Uploading a module that is already stored is a 409 unless `?replace=true`,
  which refreshes the parsed metadata and keeps the record's ID, name, tags and
  assignments. A file that does not parse is rolled back.
- `DELETE /api/mibs/{id}` and `PUT /api/mibs/{id}/tags` delete a record and set
  its tags.

## 7. Example: IF-MIB structure

```
ifNumber (1.3.6.1.2.1.2.1.0)               scalar, Integer32, read-only
ifTable  (1.3.6.1.2.1.2.2)                 table
└── ifEntry (1.3.6.1.2.1.2.2.1)            row, INDEX { ifIndex }
    ├── ifIndex       (...2.2.1.1)         column, index
    ├── ifDescr       (...2.2.1.2)         column
    ├── ifSpeed       (...2.2.1.5)         column
    ├── ifOperStatus  (...2.2.1.8)         column, enumerated
    ├── ifInOctets    (...2.2.1.10)        column, Counter32
    └── ...
```

Walking `ifTable` returns every column for every row, with the row index as the
OID suffix (`1.3.6.1.2.1.2.2.1.10.<ifIndex>` for `ifInOctets`).

The MIB files shipped in the repository are under `mibs/` (`standard/`,
`cisco/`, `vendor/` and per-device directories created by `mib-importer`).
