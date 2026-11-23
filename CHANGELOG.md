# Changelog

All notable changes to go-netmon are recorded here, in the format of
[Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html). A change adds a
fragment to changelog.d/ instead of editing this file (changelog.d/README.md).

## [Unreleased]

## [0.2.1] - 2026-11-23

### Changed

- **release**: A release is published as soon as its release pull request merges and CI passes on `main`; the tag, archives, images and GitHub release follow without a manual step.
- **release**: Before 1.0 every release, `-alpha` and `-rc.N` included, is GitHub's Latest release and moves the images' `X.Y` and `latest` tags, so `docker pull ghcr.io/jeremyhahn/go-netmon:latest` and the Releases page show the current release. From 1.0 on only a plain `vX.Y.Z` does.

## [0.2.0] - 2026-11-23

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

