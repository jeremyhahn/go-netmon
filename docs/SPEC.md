# go-netmon specification

> **Status: NORMATIVE** · Scope: go-netmon · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

## Purpose

go-netmon is a standalone SNMP network monitor, outside the layered stack, and the central aggregator for network telemetry.
It polls devices over SNMP, imports MIBs, and serves a web UI, an HTTP API and the `netmon` CLI over the collected state.
It keeps its own state in PebbleDB and writes every sample to Prometheus by remote-write.
Infrastructure runs it; the system overview is [architecture/system-architecture.md](architecture/system-architecture.md).

## Boundaries

- Owns SNMP polling, MIB import, device and profile configuration, and the dashboards built from them.
- Stays the central aggregator for network telemetry.
- Never monitors hosts or processes; go-sysmon owns that.
- Never stores or queries metrics itself; Prometheus does, and go-netmon does not start without a remote-write URL.
- Uses PebbleDB directly; it does not move to go-qrdb without a recorded architecture decision.
- Takes no stack dependency and builds toward no integration with go-trusted-platform until that design is decided.
- Does not authenticate its API or web UI; it runs behind a trusted network ([operations/security-posture.md](operations/security-posture.md)).
- Does not terminate TLS, cluster, or roll back a failed deploy ([deployment/architecture.md](deployment/architecture.md) §7).

## Features

| Feature | Status | Requirements | Detail |
|---|---|---|---|
| snmp-polling | shipped | Polls each enabled device on its own interval over SNMPv1, v2c or v3, in parallel across devices. A poll that fails is reported as a failed poll, and an unreachable device is reported as unreachable. One device is owned by one worker at a time. | none |
| web-ui | shipped | Serves the device, profile and MIB management pages and a live dashboard. The browser subscribes per tab over a WebSocket. A row table drives the charts, and its selection filters every chart. | none |
| dashboard-generation | shipped | Builds dashboard panels from a device's assigned MIBs and the metric catalog. Resolves MIBs from the directory that holds them. | none |
| deployment | shipped | Deploys the poller, the web UI and Prometheus as native systemd services onto a host with SSH, sudo and systemd. A unit reports active only when it is serving. Migrates a containerised deployment by copying it, leaving the source untouched. | [docs/deployment/](deployment/) |
| backup-restore | shipped | Backs up a deployed stack's volumes and config, verifies an archive without changing anything, and restores it. Restore prints its plan unless confirmed, and overwrites a MIB tree only with a second flag. | [docs/operations/](operations/) |
| devices | in progress | Registers, updates and deletes SNMP devices through the API. Registering a device that is already registered is not a failure. Deleting a device stops its poller before removing it from storage, and reports a failure instead of swallowing it. A device's enabled flag is settable from the UI. | none |
| mibs | in progress | Imports MIB modules from files, including a file whose name differs from its module name, without reparsing it on every start. A module with unresolved IMPORTS is reported as a typed result, not a successful parse. | none |
| config-export-import | in progress | Exports devices, profiles and per-assignment rules as one archive and imports it, showing the change before applying it. The export carries the MIB sources, with each source path derived at read time. An export holds credentials and is written mode 0600. | [docs/operations/](operations/) |
| snmp-credentials | in progress | Holds SNMP credentials in protected memory through go-enclave's interface, not in clear-text process memory. | none |
| fips | in progress | Builds with the `fips` tag and runs under `fips140=only`. The FIPS profile disables SNMPv1 and v2c and allows SNMPv3 with SHA and AES only. | none |
| metrics | in progress | Writes samples to Prometheus by remote-write in bounded batches, and one out-of-order sample does not reject a batch. Export counters exist from start, so alerts can fire. The remote-write sender is the only writer to the endpoint. Entity sensor metadata stays bounded by inventory, not history. | [docs/metrics/](metrics/) |
| api | in progress | Serves device, MIB, profile and monitoring state over HTTP to the web UI and the CLI. Endpoints serve poller-updated state rather than querying devices per request. | none |
| release | in progress | Cuts alpha and release-candidate builds with `make` targets and a release workflow, with changes recorded in `changelog.d`. | none |
| ci | in progress | `make security` scans dependencies with trivy as well as the linked standard library. A check's human-readable and machine-readable layers report the same result. | none |
| testing | in progress | Unit tests need no Docker. The integration suite builds every helper image it uses instead of pulling it, and runs the same Prometheus major version as production. Nothing in the test suites touches a production device; the e2e suite is removed. | none |
| docs | in progress | Every page carries a status banner and a `Last verified:` stamp, and the docs index lists every page. SPEC.md and ROADMAP.md agree. | none |
| qrdb-storage | planned | Moves the store onto go-qrdb as a client, with tenant DEKs and a barrier seal. Shelved. | none |

## Interfaces

- Web UI and HTTP management API, served by `cmd/webui`; [webui.md](webui.md).
- Poller control API and Prometheus metrics endpoint, served by `cmd/go-netmon`; [architecture/system-architecture.md](architecture/system-architecture.md).
- `netmon` CLI, a client of the management API (`cmd/netmon`); [cli.md](cli.md).
- `mib-importer` tool (`cmd/mib-importer`); [mib-importer.md](mib-importer.md).
- `netmon-backup` tool behind `make backup` and `make restore` (`cmd/netmon-backup`); [operations/backup-restore.md](operations/backup-restore.md).
- `make deploy` and `make migrate`; [deployment/cli.md](deployment/cli.md).

## Release criteria

- Every issue in the [Dev complete](../../milestone/25) milestone is closed or moved out by the owner.
- CI is green on `main` for both the ordinary and the `fips` build.
- Release-candidate tags are cut only after go-trusted-platform is dev complete; until then releases are alpha.
- `make release-alpha` and `make release-candidate` cut a release from `main` through the release workflow.
- SPEC.md and ROADMAP.md agree feature for feature.
