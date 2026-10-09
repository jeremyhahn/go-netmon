# MIB Documentation Index

> **Status: REFERENCE** · Scope: go-netmon · Last verified: 2026-10-09

Where to read about go-netmon's MIB subsystem (`internal/mib/**`): parsing with
gosmi, storage in PebbleDB, loading, and OID selection.

## Pages

| Page | Read it for |
|------|-------------|
| [MIB_ARCHITECTURE.md](MIB_ARCHITECTURE.md) | The packages, the parse-to-store pipeline, key schema, pollability rules, concurrency |
| [MIB_PARSING_AND_STORAGE.md](MIB_PARSING_AND_STORAGE.md) | Parser output, data model, storage operations, selection, built-in profiles, web UI endpoints |
| [mib-catalogs-and-on-demand-loading.md](mib-catalogs-and-on-demand-loading.md) | The separate poller and web UI catalogs and how modules are loaded on demand |
| [mib-importer.md](mib-importer.md) | Downloading Cisco MIBs with `mib-importer` |
| [dashboard-generation.md](dashboard-generation.md) | Generating Grafana dashboards from stored MIBs |
| [MIB_EXPLORATION_SUMMARY.md](MIB_EXPLORATION_SUMMARY.md) | An earlier overview; not covered by the verification date above |

## Pipeline

```
MIB files → parser (gosmi) → OIDMetadata → loader → storage (PebbleDB)
                                                        │
                                       selector / profiles → poller
                                       API handlers        → web UI
                                       metadata            → metrics exporter
```

## Query patterns

```go
// Direct lookup
oid, _ := store.GetOID("1.3.6.1.2.1.1.1")

// Name lookup
oid, _ := store.GetOIDByName("SNMPv2-MIB", "sysDescr")

// Prefix (whole sub-identifiers) and per-module queries
oids, _ := store.ListOIDs("1.3.6.1.2.1.1")
oids, _ = store.ListModuleOIDs("IF-MIB")

// Pollable objects of a module
pollable, _ := store.GetPollableOIDs("IF-MIB")
```

For which objects a device polls, see section 5 of
[MIB_PARSING_AND_STORAGE.md](MIB_PARSING_AND_STORAGE.md).

## MIB files

```
mibs/
├── standard/   IETF and IANA MIBs
├── cisco/      Cisco MIBs
├── vendor/     other vendors' MIBs
├── asr1001/    per-device directories created by mib-importer
└── r640/
```

See `mibs/README.md` for details.

## Testing

```bash
go test ./internal/mib/...
```

## Troubleshooting

- **Module not found**: check that the file is in one of the MIB search paths and
  that its IMPORTS are present. A module whose imports are missing parses, but
  its objects come out with no OID and are discarded.
- **Object not polled**: it must have an OID, be readable, have status `current`
  and not be a table or entry; the device's module filters and `max_oids` also
  apply. The web UI shows the reason for each object.
- **Slow lookups on a large catalog**: use `ListModuleOIDs`/`GetPollableOIDs`
  with a module name rather than listing the whole store.
