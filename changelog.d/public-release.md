### Added

- **release**: Initial public release of go-netmon under the Apache License 2.0.
- **snmp-polling**: Polls each enabled device on its own interval over SNMPv1, v2c or v3, in parallel across devices, and reports a failed poll and an unreachable device as such. ([#71](../../issues/71))
- **web-ui**: A web UI for managing devices, profiles and MIBs, with a live per-tab dashboard whose row table filters every chart. ([#148](../../issues/148))
- **dashboard-generation**: Generates Grafana dashboards from a device's assigned MIBs and the metric catalog. ([#59](../../issues/59))
- **deployment**: `make deploy` installs the poller, the web UI and Prometheus as native systemd services over SSH, and `make migrate` moves a containerised deployment onto them. ([#118](../../issues/118))
- **backup-restore**: `make backup`, `make backup-verify` and `make restore` back up, verify and restore a deployed stack. ([#34](../../issues/34))
- **release**: Each release publishes Linux amd64 and arm64 archives on GitHub Releases with a `SHA256SUMS` file and build provenance attestations you can check with `gh attestation verify`. ([#179](../../issues/179))
- **release**: Multi-arch (amd64, arm64) container images for the poller and the web UI are published to `ghcr.io/jeremyhahn/go-netmon` and `ghcr.io/jeremyhahn/go-netmon-webui`. ([#179](../../issues/179))
- **fips**: Every archive and image has a FIPS 140-3 variant (`_fips` archives, `-fips` image tags) for deployments that must run under `GODEBUG=fips140=only`. ([#7](../../issues/7))

### Changed

- **release**: Every binary reports the version it was built from. ([#179](../../issues/179))

### Removed

- **docs**: Internal development logs are removed from `docs/`; the maintained documentation is indexed in `docs/README.md`.
