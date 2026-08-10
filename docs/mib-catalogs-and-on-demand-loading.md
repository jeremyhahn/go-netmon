# MIB catalogs and on-demand loading

Two services parse MIBs, and each keeps its own catalog of the result — the OID
metadata of every module it has parsed. This document says where those catalogs
live, why there are two of them, and how a module an operator assigns in the web
UI reaches the poller.

## Two catalogs, by necessity

Both catalogs are PebbleDB stores, and Pebble admits exactly one writer per
directory. The poller holds an exclusive lock on its store for the life of the
process, so the two services cannot share one.

| | catalog (parsed OID metadata) | MIB files |
|---|---|---|
| poller | `mib.storage_path` in `config.yaml` → `/app/data/mib-storage` | `mib.paths` |
| web UI | `-mib-catalog` → `/app/data/webui/mib.db` | `-mib-storage` → `/app/data/mibs`, uploads land in its `uploaded/` subdirectory |

The web UI's catalog **must** sit on a mounted volume. Left at its default it
lands on the container's writable layer, where `docker compose build` or any
container recreate destroys it. It must also stay out of the poller's store
directory, where Pebble's lock would turn the mistake into a crash loop.

`-mib-catalog` always wins over `mib.storage_path`, even when a config file is
present in the web UI container. That file is the poller's, and its
`storage_path` names the poller's store: adopting it would either fail to start
or, with the poller down, take the lock and keep the poller out.

The MIB *files* are shared. The web UI writes uploads to `/app/data/mibs/uploaded`
on the `go-netmon-data` volume, which the poller sees as `/app/data/uploaded` and
already has on its search path, so either service can parse a module the other
imported.

Losing the web UI catalog is recoverable but not free: `LoadStoredMIBs` rebuilds
it at startup from the MIB records in `-db`, which is on a volume.

## Assignment does not imply metadata

The web UI tells the poller only the **names** of a device's modules, over
`PUT /api/poller/devices/{name}`. A name whose objects are not in the poller's own
catalog selects nothing, so a device silently polls whatever subset happened to
be in `mib.auto_load` at startup. On one install a router assigned a
seventeen-module profile polled 38 objects instead of 155 for exactly this
reason.

`mib.auto_load` is therefore a bootstrap list, not a limit. Everything else is
parsed on demand.

## When loading happens

`Poller.hydrateModules` runs for a device:

- from `Start`, for each enabled device in the configuration, and
- from `AddDevice`, which is what both `POST` and `PUT /api/poller/devices/...`
  reach, so a device added or re-pushed by the web UI is covered.

For each assigned module it calls `Loader.EnsureModule`, which parses the module
from the search path only if the catalog does not already hold it.

Three properties are worth knowing:

- **It runs outside the poller lock.** Parsing seventeen modules takes on the
  order of two seconds, and holding the poller's lock across that would stall
  every other device's registration, removal and status lookup. Devices already
  polling are unaffected regardless: the polling loop takes neither lock.
- **A module already present costs a lookup, not a parse** — about 7 µs against
  about 290 ms for a load (`make bench-mib-loader`).
- **A module that will not parse costs only itself.** It is logged, counted, and
  skipped; the device keeps polling every module that did load. Failing the
  device instead would stop a router polling at all because one of its
  seventeen MIBs is malformed.

## Observing failures

    gonetmon_mib_module_load_failures_total{device="core-01",module="CISCO-FOO-MIB"}

is exported on the poller's metrics endpoint (`metrics.listen_address`, scraped
as `go-netmon:9100`). A non-zero value means that device is polling less than
was configured for it, and names the module responsible. It is a counter rather
than a device status because the device itself is healthy — marking it unhealthy
would be misleading, and an operator wants to be alerted on this rather than to
discover it by opening a page.

## Concurrency

`gosmi`, which the parser wraps, keeps its loaded modules in package-global
state and is not safe for concurrent use — two goroutines parsing two *different*
modules corrupt it just as surely as two parsing the same one. `Loader` therefore
serializes parsing behind one mutex, held across the parse and the store write
together so a module is never visible half-written.

`EnsureModule` checks for the module before taking that lock and again after
taking it. The first check keeps a slow parse from serializing everyone else's
presence check; the second is what makes several workers arriving at the same
missing module parse it once between them.

## Failed reloads are not destructive

`LoadModule` parses before it deletes. It used to delete the stored copy first,
so an attempt to refresh a module whose file had become unreadable erased the
good copy the operator already had. Parsing first degrades that to "nothing
changed".
