# go-netmon roadmap

> **Status: REFERENCE** · Owner: Software Department · Last verified: 2026-10-21
> Orders the features in [docs/SPEC.md](docs/SPEC.md) by milestone. Gitea is the source of truth for every status below.

## Mission

Standalone SNMP network monitor run by Infrastructure, and the central aggregator for network telemetry.
Polls devices over SNMP, imports MIBs, and serves a web UI, an HTTP API and a CLI over the collected state.
Stores state in PebbleDB and writes metrics to Prometheus by remote-write.

## Milestones

- [Dev complete](../../milestone/25) — RC, the current milestone; RC tags only after go-trusted-platform is dev complete, alpha until then — projected date not set.
- [Later](../../milestone/36) — target kind not set; holds shelved work — projected date not set.

## Complete

- **snmp-polling** — a replaced worker and its replacement never contend for one device — [#71](../../issues/71) — done
- **web-ui** — tab render and subscription bookkeeping hold together — [#148](../../issues/148) — done
- **dashboard-generation** — dashboards resolve the MIBs they are built from — [#59](../../issues/59) — done
- **deployment** — a unit reports active only when it is serving — [#118](../../issues/118) — done
- **backup-restore** — backup survives empty docker output — [#34](../../issues/34) — done

## Now

- **release** — release-alpha and release-candidate targets, release workflow, changelog.d — [#179](../../issues/179) — in review
- **ci** — `make security` runs a trivy scan — [#155](../../issues/155) — in review
- **docs** — docs/SPEC.md and ROADMAP.md agree — [#178](../../issues/178) — in review
- **snmp-credentials** — SNMP credentials held through go-enclave's interface — [#8](../../issues/8) — open
- **config-export-import** — config export carries MIB sources, path derived at read time — [#61](../../issues/61) — open
- **mibs** — unresolved IMPORTS reported as a typed result — [#62](../../issues/62) — open
- **testing** — the e2e suite is removed — [#70](../../issues/70) — open

## Next

- **devices** — registering an already-registered device is not an HTTP 500 — [#83](../../issues/83) — open
- **devices** — device delete stops the poller first and reports failure — [#87](../../issues/87) — open
- **testing** — integration suite builds its volume helper instead of pulling busybox — [#37](../../issues/37) — open
- **fips** — FIPS profile allows SNMPv3 with SHA and AES only — [#7](../../issues/7) — blocked: FIPS deferred
- **docs** — docs carry index, banners and Serena setup — [#153](../../issues/153) — blocked: FIPS deferred
- **devices** — device Enabled is settable from the UI — [#86](../../issues/86) — blocked: outside the milestone
- **api** — endpoints serve poller-updated state — [#100](../../issues/100) — blocked: outside the milestone
- **metrics** — entity sensor cache grows with inventory, not history — [#54](../../issues/54) — blocked: outside the milestone
- **metrics** — the remote-write sender is the only writer to the endpoint — [#137](../../issues/137) — blocked: outside the milestone
- **testing** — throwaway test stack with a poller — [#111](../../issues/111) — blocked: outside the milestone
- **testing** — integration stack runs production's Prometheus major version — [#138](../../issues/138) — blocked: outside the milestone
- **ci** — checks agree in their human- and machine-readable layers — [#120](../../issues/120) — blocked: outside the milestone

## Later

- **qrdb-storage** — the store moves onto go-qrdb as a client — [#198](../../issues/198) — blocked: shelved

## Future

None.

## Dependencies

- Up: go-enclave, for the SNMP credential interface ([#8](../../issues/8)); go.mod pins no in-house module yet.
- Up: go-trusted-platform dev complete, before any RC tag.
- Down: none; Infrastructure runs it.
- Standards watch: none.
