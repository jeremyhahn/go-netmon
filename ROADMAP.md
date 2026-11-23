# go-netmon roadmap

> **Status: REFERENCE** · Last verified: 2026-11-23
> Orders the features in [docs/SPEC.md](docs/SPEC.md) by priority. The linked GitHub issues hold the current status of each item.

## Mission

A standalone SNMP network monitor and central aggregator for network telemetry.
It polls devices over SNMP, imports MIBs, and serves a web UI, an HTTP API and a CLI over the collected state.
It stores state in PebbleDB and writes metrics to Prometheus by remote-write.

## Complete

- **snmp-polling** — a replaced worker and its replacement never contend for one device — [#71](../../issues/71)
- **web-ui** — tab render and subscription bookkeeping hold together — [#148](../../issues/148)
- **dashboard-generation** — dashboards resolve the MIBs they are built from — [#59](../../issues/59)
- **deployment** — a unit reports active only when it is serving — [#118](../../issues/118)
- **backup-restore** — backup survives empty docker output — [#34](../../issues/34)

## Now: first public release (v0.2.0)

- **release** — GitHub release archives with checksums and provenance attestations, multi-arch container images, FIPS variants — [#179](../../issues/179)
- **ci** — `make security` runs a trivy scan — [#155](../../issues/155)
- **docs** — docs/SPEC.md and ROADMAP.md agree — [#178](../../issues/178)
- **snmp-credentials** — SNMP credentials held in protected memory through go-enclave — [#8](../../issues/8)
- **config-export-import** — config export carries MIB sources, path derived at read time — [#61](../../issues/61)
- **mibs** — unresolved IMPORTS reported as a typed result — [#62](../../issues/62)
- **testing** — the e2e suite is removed — [#70](../../issues/70)

## Next

- **devices** — registering an already-registered device is not an HTTP 500 — [#83](../../issues/83)
- **devices** — device delete stops the poller first and reports failure — [#87](../../issues/87)
- **devices** — device Enabled is settable from the UI — [#86](../../issues/86)
- **testing** — integration suite builds its volume helper instead of pulling busybox — [#37](../../issues/37)
- **api** — endpoints serve poller-updated state — [#100](../../issues/100)
- **metrics** — entity sensor cache grows with inventory, not history — [#54](../../issues/54)
- **metrics** — the remote-write sender is the only writer to the endpoint — [#137](../../issues/137)
- **testing** — throwaway test stack with a poller — [#111](../../issues/111)
- **testing** — integration stack runs production's Prometheus major version — [#138](../../issues/138)
- **ci** — checks agree in their human- and machine-readable layers — [#120](../../issues/120)

## Later

- **fips** — FIPS profile allows SNMPv3 with SHA and AES only, and `authPriv` works in FIPS mode — [#7](../../issues/7)
- **docs** — docs carry an index and verified status banners — [#153](../../issues/153)
- **qrdb-storage** — the store moves onto go-qrdb as a client — [#198](../../issues/198)

## Dependencies

- [go-enclave](https://github.com/jeremyhahn/go-enclave), for protected memory holding SNMP credentials ([#8](../../issues/8)).
