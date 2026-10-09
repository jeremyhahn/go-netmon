# go-netmon roadmap

> **Status: REFERENCE** · Owner: Software Department · Last verified: 2026-10-08
> Changes are proposed by docs-only PR.

## Mission

Standalone SNMP network monitor run by Infrastructure, and the central aggregator for network telemetry.
Polls devices over SNMP, imports MIBs, and serves a web UI and API over the collected state.
Stores state in PebbleDB and writes metrics to Prometheus by remote-write.

## Current milestone

None open. Target kind RC; projected date not set.

## Now

- [#153](../../issues/153) docs carry index, banners and stamps — open
- [#83](../../issues/83) an existing registration is not an HTTP 500 — open
- [#70](../../issues/70) e2e suite no longer touches production devices — open
- [#87](../../issues/87) device delete stops the poller first and reports failure — open
- [#86](../../issues/86) device Enabled is settable from the UI — open
- [#7](../../issues/7) FIPS profile restricts SNMP to v3 SHA/AES — open
- [#28](../../issues/28) bind and target addresses configurable by file, env and flag — open

## Next

- [#147](../../issues/147) MIBs load when file name differs from module name — open
- [#148](../../issues/148) dashboard tab render and subscription bookkeeping stay consistent — open
- [#100](../../issues/100) API serves poller-updated state — open
- [#62](../../issues/62) unresolved MIB IMPORTS fail the parse — open
- [#111](../../issues/111) throwaway test stack with a poller — open
- [#8](../../issues/8) SNMP credentials held in memguard — blocked: secret custody ruling
- [#13](../../issues/13) PebbleDB encrypted at rest — blocked: encryption layer and key source undecided

## Later

Nothing accepted. New work arrives as issues.

## Dependencies

- Up: none; go.mod pins no in-house modules.
- Down: Infrastructure runs it; planned to deploy into go-trusted-platform.

## Non-goals

- Host and process monitoring, which go-sysmon owns.
- Metrics storage and querying, which Prometheus owns.
- Ownership boundaries per [docs/00](../handbook/software/docs/00-ownership-and-sdk-boundary.md).
