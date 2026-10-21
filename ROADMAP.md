# go-netmon roadmap

> **Status: REFERENCE** · Owner: Software Department · Last verified: 2026-10-11
> Changes are proposed by docs-only PR. Gitea is the source of truth for every status below.

## Mission

Standalone SNMP network monitor run by Infrastructure, and the central aggregator for network telemetry.
Polls devices over SNMP, imports MIBs, and serves a web UI and API over the collected state.
Stores state in PebbleDB and writes metrics to Prometheus by remote-write.

## Current milestone

[Release candidate](../../milestone/25), target kind RC; it holds every Now item and every blocked Next item.
Projected date: not yet set in the department plan.

## Now

Each line: issue, outcome, the `docs/<feature>/` folder that specifies it, status. A folder in code font does not exist yet.

- [#177](../../issues/177) roadmap ordered by the Release candidate milestone · [docs/](docs/) · open
- [#180](../../issues/180) root README.md describes and links the project · [docs/](docs/) · open
- [#179](../../issues/179) release targets, release workflow and changelog.d · `docs/release/` · open
- [#155](../../issues/155) trivy runs in `make security` · [docs/operations/](docs/operations/) · open
- [#83](../../issues/83) registering an already-registered device is not an HTTP 500 · `docs/devices/` · open
- [#87](../../issues/87) device delete stops the poller first · `docs/devices/` · open
- [#37](../../issues/37) integration suite builds its volume helper instead of pulling busybox · `docs/testing/` · open

## Next

Release candidate items waiting on a ruling; each moves to Now when it is unblocked.

- [#7](../../issues/7) FIPS profile restricts SNMP to approved algorithms · `docs/fips/` · blocked: AES-CFB under fips140=only ruling
- [#8](../../issues/8) SNMP credentials held in memguard · `docs/snmp-credentials/` · blocked: is go-enclave the primitive
- [#13](../../issues/13) PebbleDB encrypted at rest · `docs/storage/` · blocked: encryption layer ruling
- [#62](../../issues/62) unresolved MIB IMPORTS fail the parse · `docs/mibs/` · blocked: owner ruling on direction
- [#61](../../issues/61) config export carries MIB sources · [docs/operations/](docs/operations/) · blocked: owner pick
- [#70](../../issues/70) e2e suite never mutates production devices · `docs/testing/` · blocked: owner's choice of option
- [#153](../../issues/153) docs carry index, banners and Serena setup · [docs/](docs/) · blocked: #7 and the `.serena/` ruling
- [#178](../../issues/178) docs pass a spec-review scrub · [docs/](docs/) · blocked: department slot

## Later

Accepted direction outside the milestone, undated; ordered when the milestone clears.

- [#100](../../issues/100) API serves poller-updated state · [docs/architecture/](docs/architecture/) · open
- [#86](../../issues/86) device Enabled is settable from the UI · `docs/devices/` · open
- [#111](../../issues/111) throwaway test stack with a poller · `docs/testing/` · open
- [#54](../../issues/54) device metric labels stay stable across delete and re-add · [docs/metrics/](docs/metrics/) · open
- [#138](../../issues/138) integration stack runs the same Prometheus major version as production · `docs/testing/` · open
- [#137](../../issues/137) remote-write sender is the only writer to the endpoint, SendCatalog included · [docs/metrics/](docs/metrics/) · open
- [#120](../../issues/120) checks are correct in both their human-readable and machine-readable layers · `docs/ci/` · open

## Dependencies

- Up: none; go.mod pins no in-house modules.
- Down: Infrastructure runs it; planned to deploy into go-trusted-platform.

## Non-goals

- Host and process monitoring, which go-sysmon owns.
- Metrics storage and querying, which Prometheus owns.
- Ownership boundaries per [docs/00 §R](../../../handbook/src/branch/main/software/docs/00-ownership-and-sdk-boundary.md).
