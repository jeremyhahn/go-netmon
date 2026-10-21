# go-netmon

go-netmon is an SNMP network monitor. It polls devices for the OIDs their MIBs
define, writes the samples to Prometheus over remote-write, and ships a web UI,
a `netmon` CLI and a cold-backup tool.

## Binaries

| Binary | Source | Purpose |
|---|---|---|
| `go-netmon` | `cmd/go-netmon` | the SNMP poller and its API |
| `webui` | `cmd/webui` | the web UI server |
| `netmon` | `cmd/netmon` | the command-line interface |
| `mib-importer` | `cmd/mib-importer` | imports MIB files |
| `netmon-backup` | `cmd/netmon-backup` | backs up and restores the stack |

## Build

Requires Go (the version in `go.mod`).

```sh
make deps        # download dependencies
make build       # ./bin/go-netmon
make build-all   # all five binaries above into ./bin
```

## Run

The full stack (go-netmon, Prometheus, Grafana and the web UI) runs in Docker:

```sh
make run         # build and start the stack
make status      # show the services
make logs        # follow their logs
make stop        # stop the stack
```

`make run-webui` starts only the web UI and Prometheus, with no SNMP polling.
`make docker-up` and `make docker-down` start and stop the Compose stack
without rebuilding.

To run the poller directly, copy `config.example.yaml` to `config.yaml` (or
point `CONFIG_PATH` at your file) and start it:

```sh
cp config.example.yaml config.yaml
./bin/go-netmon -api-listen :9080 -metrics-listen :9100
```

Both flags are optional; unset, they fall back to `NETMON_API_LISTEN_ADDRESS`,
`NETMON_METRICS_LISTEN_ADDRESS` and then the config file.

## Test

```sh
make test               # unit tests, no Docker
make integration-test   # integration tests, needs Docker
make lint               # linters
make security           # vulnerability scan
make ci                 # everything a pull request must pass
```

`make help` lists every target.

## Documentation

- [docs/README.md](docs/README.md) — the documentation index
- [ROADMAP.md](ROADMAP.md) — open work
