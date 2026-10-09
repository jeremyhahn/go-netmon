# Integration Tests

> **Status: REFERENCE** · Scope: go-netmon · Last verified: 2026-10-09

How the Docker-based integration suite is laid out and run. Everything here was
checked against `docker-compose.test.yml`, `test/integration/*.sh`, the
`Makefile` and the test files. The commands themselves need a Docker daemon and
were not executed for this verification.

## Services

`docker-compose.test.yml` (repository root) defines:

| Service            | Purpose |
| ------------------ | ------- |
| `prometheus`       | Receives metrics on the remote-write receiver (built from `test/integration/Dockerfile.prometheus`) |
| `snmp-simulator`   | Mock SNMP agent serving `public.snmprec` (built from `test/integration/Dockerfile.snmpsim`) |
| `dind`             | A privileged `docker:27-dind` sibling daemon, behind the `dind` profile, for the backup tests |
| `integration-test` | The test runner (built from `test/integration/Dockerfile.test`); waits for the first two to be healthy |

Prometheus and the simulator publish **ephemeral** host ports, so two stacks do
not collide. The tests reach them by service name on `test-network`
(`PROMETHEUS_URL=http://prometheus:9090`, `SNMP_HOST=snmp-simulator`,
`SNMP_PORT=161`). Find a published port with
`docker compose -p <project> port prometheus 9090`.

## Which packages run

The runner does not run a fixed directory. It runs every package holding a file
behind `//go:build integration`, discovered by
`test/integration/tagged-packages.sh` (it prints one `./dir/...` pattern per
line). Empty output is an error: `run-tests.sh` refuses to report a green run
for a suite that does not exist. Adding a tagged test file to a new package is
enough to have it run. `make vet-tags` vets the tree under every tag the
repository defines, and `make test-integration-harness` tests the harness's
compose namespacing and teardown against a stub docker (no daemon).

## Running

```bash
make test-integration          # alias: make integration-test
```

This runs `test/integration/run-tests.sh`, which:

1. refuses an unsafe compose project (`compose-guard.sh`: never empty, never the
   directory default, which is a deployed stack's project, and never the manual
   stack below);
2. starts `prometheus` and `snmp-simulator` under a per-run project name with
   `up -d --build --wait`;
3. tries to start `dind`; if the runner cannot start a privileged container it
   prints a WARNING and the backup tests skip rather than fail;
4. runs `go test -v -tags=integration <discovered packages>` in the
   `integration-test` container, rebuilt on every run so the baked-in source is
   current;
5. tears everything down on exit, interrupt or termination
   (`down -v --remove-orphans`, including the `dind` profile).

The script takes an optional `-run` regex as its first argument; `make` does not
pass one through, so use it directly:

```bash
test/integration/run-tests.sh TestEndToEnd
```

Narrower targets: `make integration-test-ordering` (the #69 ordering
reproduction, via `test/integration/Makefile`), and
`make integration-test-parser`, `integration-test-parser-cisco-bgp`,
`integration-test-parser-critical` (MIB parser tests against real MIB files).

## Manual stack

```bash
make integration-up      # prometheus + snmp-simulator, waits for healthy
make integration-logs    # follow logs
make integration-shell   # sh in the integration-test container
make integration-down    # down -v --remove-orphans
```

These call `test/integration/stack.sh up|down|logs|shell`. They use one fixed
project name (not the directory default) so that `up` and `down` can name the
same stack across separate invocations. Two people on one host share it.
`integration-up` prints the ephemeral ports. Inside `integration-shell`:

```bash
go test -v -tags=integration $(test/integration/tagged-packages.sh)
```

## Tests

`test/integration/integration_test.go`:

- `TestEndToEndIntegration`: poll the simulator and export to Prometheus
- `TestSNMPConnectivity`
- `TestPrometheusConnectivity`

`test/integration/backup_test.go` (need the `dind` daemon, otherwise they skip):
`TestVolumeRoundTripSurvivesTheDaemon`, `TestImportReplacesRatherThanMerges`,
`TestOwnershipSurvivesTheRoundTrip`, `TestVolumeOwnershipIsReadFromRealLabels`.

`test/integration/ordering_test.go` (the #69 reproduction):
`TestDisorderInsideOneRequestIsDiscardedWithoutASound`,
`TestOneReadingBehindTheHeadRefusesTheWholeRequest`,
`TestOverlappingPollsAreRepairedAndNothingIsLost`,
`TestAPollTheEndpointHasMovedPastIsDroppedNotRetriedForever`.

`test/integration/table_index_test.go` (`TestTableIndexExtraction`,
`TestEndToEndPipeline`) carries no `integration` build tag, so the runner does
not discover it through the tag; check its header before relying on it.

Mock device data is `test/integration/snmp-data/public.snmprec`; add OIDs there
as `OID|type|value` lines.

## Troubleshooting

- **Docker not running or compose plugin missing:** the scripts exit with
  `docker is not installed…` or `the docker compose plugin is not available`.
- **Services not healthy:** `make integration-logs`, or
  `docker compose -p <project> -f docker-compose.test.yml ps`.
- **Backup tests skipped:** the run printed the `could not start the sibling
  Docker daemon` warning; the runner cannot start a privileged container.
