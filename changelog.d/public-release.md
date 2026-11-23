### Added

- **release**: Initial public release of go-netmon under the Apache License 2.0.
- **snmp-polling**: Polls each enabled device on its own interval over SNMPv1, v2c or v3, in parallel across devices, and reports a failed poll and an unreachable device as such.
- **web-ui**: A web UI for managing devices, profiles and MIBs, with a live per-tab dashboard whose row table filters every chart.
- **dashboard-generation**: Generates Grafana dashboards from a device's assigned MIBs and the metric catalog.
- **deployment**: `make deploy` installs the poller, the web UI and Prometheus as native systemd services over SSH, and `make migrate` moves a containerised deployment onto them.
- **backup-restore**: `make backup`, `make backup-verify` and `make restore` back up, verify and restore a deployed stack.
- **release**: Each release publishes Linux amd64 and arm64 archives on GitHub Releases with a `SHA256SUMS` file and build provenance attestations you can check with `gh attestation verify`.
- **release**: Multi-arch (amd64, arm64) container images for the poller and the web UI are published to `ghcr.io/jeremyhahn/go-netmon` and `ghcr.io/jeremyhahn/go-netmon-webui`.
- **fips**: Every archive and image has a FIPS 140-3 variant (`_fips` archives, `-fips` image tags) for deployments that must run under `GODEBUG=fips140=only`. ([#4](../../issues/4))

### Changed

- **release**: Every binary reports the version it was built from.

### Removed

- **docs**: Internal development logs are removed from `docs/`; the maintained documentation is indexed in `docs/README.md`.
