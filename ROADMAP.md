# go-netmon roadmap

> **Status: REFERENCE** · Last verified: 2026-11-23
> Orders the features in [docs/SPEC.md](docs/SPEC.md) by priority. A linked GitHub issue holds the current status of its item.

## Mission

A standalone SNMP network monitor and central aggregator for network telemetry.
It polls devices over SNMP, imports MIBs, and serves a web UI, an HTTP API and a CLI over the collected state.
It stores state in PebbleDB and writes metrics to Prometheus by remote-write.

## Complete

- **snmp-polling** — a replaced worker and its replacement never contend for one device
- **web-ui** — tab render and subscription bookkeeping hold together
- **dashboard-generation** — dashboards resolve the MIBs they are built from
- **deployment** — a unit reports active only when it is serving
- **backup-restore** — backup survives empty docker output

## Now: first public release (v0.2.0)

- **release** — GitHub release archives with checksums and provenance attestations, multi-arch container images, FIPS variants
- **ci** — `make security` runs a trivy scan
- **docs** — docs/SPEC.md and ROADMAP.md agree
- **snmp-credentials** — SNMP credentials held in protected memory through go-enclave
- **config-export-import** — config export carries MIB sources, path derived at read time
- **mibs** — unresolved IMPORTS reported as a typed result
- **testing** — the e2e suite is removed

## Next

- **devices** — registering an already-registered device is not an HTTP 500
- **devices** — device delete stops the poller first and reports failure
- **devices** — device Enabled is settable from the UI
- **testing** — integration suite builds its volume helper instead of pulling busybox
- **api** — endpoints serve poller-updated state — [#3](../../issues/3)
- **metrics** — entity sensor cache grows with inventory, not history
- **metrics** — the remote-write sender is the only writer to the endpoint — [#2](../../issues/2)
- **testing** — throwaway test stack with a poller
- **testing** — integration stack runs production's Prometheus major version
- **ci** — checks agree in their human- and machine-readable layers

## Later

- **fips** — FIPS profile allows SNMPv3 with SHA and AES only, and `authPriv` works in FIPS mode — [#4](../../issues/4)
- **docs** — docs carry an index and verified status banners
- **qrdb-storage** — the store moves onto go-qrdb as a client

## Dependencies

- [go-enclave](https://github.com/jeremyhahn/go-enclave), for protected memory holding SNMP credentials.
