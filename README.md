# go-netmon

[![CI](https://github.com/jeremyhahn/go-netmon/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/jeremyhahn/go-netmon/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/jeremyhahn/go-netmon?include_prereleases&sort=semver)](https://github.com/jeremyhahn/go-netmon/releases)
[![License](https://img.shields.io/github/license/jeremyhahn/go-netmon)](LICENSE)
[![Go Reference](https://pkg.go.dev/badge/github.com/jeremyhahn/go-netmon.svg)](https://pkg.go.dev/github.com/jeremyhahn/go-netmon)

go-netmon is an SNMP network monitor written in Go. It polls routers, switches,
servers and PDUs for the objects their MIBs define, writes every sample to
Prometheus over remote-write, and serves a web UI, an HTTP API and a `netmon`
CLI for managing devices, MIBs and dashboards. Prometheus is its time-series
store; go-netmon keeps only its configuration and MIB catalog, in PebbleDB.

## Features

- SNMPv1, v2c and v3 polling, per-device intervals, parallel across devices
- MIB import from files, with on-demand parsing of the modules a device uses
- Prometheus remote-write export with bounded batches and export-health counters
- Web UI with live per-device dashboards, alarms and MIB management
- Grafana dashboard generation from a device's assigned MIBs
- Entity sensor and optical DOM readings, scaled to real units
- Native systemd deployment, cold backup and restore, config export and import
- An optional FIPS 140-3 build

## Status

Pre-1.0. Configuration, the HTTP API and metric names can still change between
minor releases; read [CHANGELOG.md](CHANGELOG.md) before upgrading.

> **Security:** the API and web UI have no authentication and return SNMP
> credentials. Run them on a trusted network only; see [SECURITY.md](SECURITY.md).

## Install

### Release archives

Download `go-netmon_<version>_linux_amd64.tar.gz` or
`go-netmon_<version>_linux_arm64.tar.gz` from
[GitHub Releases](https://github.com/jeremyhahn/go-netmon/releases). Each has a
`_fips` variant (`go-netmon_<version>_linux_amd64_fips.tar.gz`) built for FIPS
140-3 mode; see [docs/fips.md](docs/fips.md). To fetch the latest release and
verify it before extracting it:

```sh
tag=$(curl -fsSLo /dev/null -w '%{url_effective}' https://github.com/jeremyhahn/go-netmon/releases/latest)
tag=${tag##*/}
base=https://github.com/jeremyhahn/go-netmon/releases/download/${tag}
curl -LO "${base}/go-netmon_${tag#v}_linux_amd64.tar.gz"
curl -LO "${base}/SHA256SUMS"
sha256sum --ignore-missing -c SHA256SUMS
gh attestation verify "go-netmon_${tag#v}_linux_amd64.tar.gz" --repo jeremyhahn/go-netmon
```

### Container images

Multi-arch (amd64, arm64) images are published to GitHub Container Registry:

| Image | Contents |
|---|---|
| `ghcr.io/jeremyhahn/go-netmon` | the poller |
| `ghcr.io/jeremyhahn/go-netmon-webui` | the web UI |

Tags are `X.Y.Z`, `X.Y` and `latest`; the FIPS builds are tagged `X.Y.Z-fips`,
`X.Y-fips` and `latest-fips`. `latest` follows the latest release: before 1.0
that is every release, `-alpha` included.

```sh
docker pull ghcr.io/jeremyhahn/go-netmon:latest
docker pull ghcr.io/jeremyhahn/go-netmon-webui:latest
```

### go install

Requires the Go version in [go.mod](go.mod).

```sh
go install github.com/jeremyhahn/go-netmon/cmd/...@latest
```

### From source

```sh
git clone https://github.com/jeremyhahn/go-netmon.git
cd go-netmon
make build-all   # all five binaries into ./bin
```

## Quickstart

The quickest way to try go-netmon is the Docker Compose stack in this
repository: the poller, the web UI, Prometheus and Grafana.

```sh
cp config.example.yaml config.yaml   # add your devices and SNMP credentials
make run                             # build and start the stack
```

Open the web UI at <http://localhost:8082>, add devices in the Devices tab,
import MIBs and assign them to devices. `make status`, `make logs` and
`make stop` manage the stack.

To run the poller without Docker, point `prometheus.remote_write_url` in
`config.yaml` at a Prometheus started with `--web.enable-remote-write-receiver`,
then:

```sh
CONFIG_PATH=./config.yaml go-netmon -api-listen 127.0.0.1:9080 -metrics-listen 127.0.0.1:9100
```

For a permanent installation as systemd services, see
[docs/deployment/user.md](docs/deployment/user.md).

## Binaries

| Binary | Source | Purpose |
|---|---|---|
| `go-netmon` | [cmd/go-netmon](cmd/go-netmon) | SNMP poller, control API and Prometheus exporter |
| `webui` | [cmd/webui](cmd/webui) | web UI and management API |
| `netmon` | [cmd/netmon](cmd/netmon) | command-line client of the management API ([docs/cli.md](docs/cli.md)) |
| `netmon-backup` | [cmd/netmon-backup](cmd/netmon-backup) | backs up, verifies and restores a deployment ([docs/operations/backup-restore.md](docs/operations/backup-restore.md)) |
| `mib-importer` | [cmd/mib-importer](cmd/mib-importer) | downloads and imports MIB files ([docs/mib-importer.md](docs/mib-importer.md)) |

`netmon version` prints the version a build was made from.

## Documentation

- [docs/README.md](docs/README.md): index of all documentation
- [docs/SPEC.md](docs/SPEC.md): features and their status
- [docs/webui.md](docs/webui.md): the web UI and its API
- [docs/operations/security-posture.md](docs/operations/security-posture.md): what is and is not defended
- [ROADMAP.md](ROADMAP.md): planned work

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Report security issues privately as
described in [SECURITY.md](SECURITY.md).

## License

Apache License 2.0. See [LICENSE](LICENSE).
