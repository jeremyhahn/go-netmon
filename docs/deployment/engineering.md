# Deployment — engineering contract

> **Status: NORMATIVE** · Scope: go-netmon native deployment · Last verified: 2026-08-19
> Conflicts with this document are defects. Fix them here first.

## 1. Artifacts

| Path | What it is |
|---|---|
| `scripts/deploy.sh` | Build here, ship over SSH, install there. The `make deploy` entry point. |
| `scripts/migrate.sh` | Restore a containerised deployment's backup onto the appliance. |
| `deploy/install.sh` | Runs **on the target**, with sudo, from inside the shipped payload. |
| `deploy/go-netmon.service` | Poller unit. |
| `deploy/go-netmon-webui.service` | Web UI unit. |
| `deploy/prometheus.service` | Metrics backend unit. |
| `deploy/config.yaml` | Seed configuration, native paths. |
| `deploy/prometheus.yml` | Seed scrape configuration. |

`deploy/config.yaml` is exempted by name in `.gitignore`, which otherwise
ignores `config.yaml` at every level. Without the exemption a clean checkout
cannot deploy.

## 2. Invariants

These are the properties the scripts are required to hold. A change that
breaks one is a defect, not a trade-off.

1. **Idempotent.** Running `deploy.sh` twice in a row makes no change the
   second time beyond restarting the services. Units are compared before being
   written; binaries are replaced unconditionally but atomically.
2. **Operator data is never overwritten.** `/etc/go-netmon/config.yaml`,
   `/etc/go-netmon/prometheus.yml` and everything under `/var/lib` survive a
   redeploy. `FORCE_CONFIG=1` is the only way past that, and it keeps a
   timestamped copy of what it replaced.
3. **The migration source is read-only.** `migrate.sh` never contacts the
   source host. It consumes a backup directory and nothing else.
4. **Checksums are verified before anything is stopped**, and again after
   upload. A truncated archive must fail before it can replace good data.
5. **Restores are atomic per component.** Extraction goes to `<dest>.incoming`
   and is renamed into place; the previous directory is kept as
   `<dest>.replaced`. An extraction that dies halfway leaves the old data
   intact.
6. **Services are restarted on every exit path** of the migration, including
   interruption, via a shell `trap`.
7. **The target needs no toolchain.** Binaries are built on the deploy host
   with `CGO_ENABLED=0`, so they do not depend on the appliance's libc.

## 3. Paths and accounts

Created by `deploy/install.sh`, all overridable by environment variable:

| Variable | Default | Owner | Mode |
|---|---|---|---|
| `BIN_DIR` | `/usr/local/bin` | `root:root` | 0755 |
| `CONF_DIR` | `/etc/go-netmon` | `root:root` | 0755 |
| `DATA_DIR` | `/var/lib/go-netmon` | `go-netmon:go-netmon` | 0750 |
| `PROM_DATA_DIR` | `/var/lib/prometheus` | `prometheus:prometheus` | 0750 |
| `SHARE_DIR` | `/usr/local/share/go-netmon` | `root:root` | 0755 |
| `UNIT_DIR` | `/etc/systemd/system` | `root:root` | 0644 |

`go-netmon` and `prometheus` are system accounts created with
`--system --no-create-home --shell /usr/sbin/nologin`. They exist to own files
and run processes; a login shell on either is only ever a way in.

0750 on the data directory is not decoration: the web UI's store holds SNMP
community strings in clear text.

## 4. Container paths that had to become configurable

The code defaulted two paths to container mount points. A native install has no
`/app`, so both are set explicitly in the unit files:

| Env | Default (container) | Native |
|---|---|---|
| `MIB_UPLOADED_PATH` | `/app/data/uploaded` | `/var/lib/go-netmon/poller/uploaded` |
| `DASHBOARD_PATH` | `/app/dashboards` | `/var/lib/go-netmon/dashboards` |

`DASHBOARD_PATH` was added by this feature (`cmd/go-netmon/main.go`,
`dashboardPath`); it mirrors the existing `MIB_UPLOADED_PATH` override rather
than inventing a second mechanism. The defaults are unchanged, so the container
image behaves exactly as before.

## 5. The single-writer rule

Pebble admits one writer per directory. Three stores, three owners:

- `poller/mib-storage` — the **poller** writes it. The web UI is pointed at the
  same tree by `-mib-storage` but only reads MIB *files* from it.
- `webui/mib.db` — the **web UI's** own catalog, set by `-mib-catalog`. It is
  separate precisely so there is never a second writer on the poller's store.
- `webui/webui.db` — the **web UI's** device registry, set by `-db`.

A change that points two processes at one Pebble directory for writing will
corrupt it. There is no locking that will save you.

## 6. Resource ceilings

The appliance is sized for 4 GB. Every unit carries `MemoryMax` as a runaway
guard, because on a box this small an unbounded process does not just die, it
takes the metrics store with it.

| Unit | `MemoryMax` | `MemoryHigh` | Why that number |
|---|---|---|---|
| `prometheus` | 1536M | 1280M | Head-block memory scales with active series; ~35k series fits with room for compaction and query spikes. |
| `go-netmon` | 768M | 640M | MIB parsing over a large module set is the memory-hungry path. |
| `go-netmon-webui` | 512M | 448M | Serves HTTP and reads Pebble; does not parse MIBs. |

That leaves roughly 1 GB for the OS and any co-tenant service. If series count
grows, raise the Prometheus ceiling — it is the one under pressure.

`MemoryDenyWriteExecute` is set on the go-netmon units but deliberately **not**
on Prometheus.

## 7. Retention

`prometheus.service` sets `--storage.tsdb.retention.time=15d`, carried over
from the containerised deployment. **This is a data-loss setting.** Measured on
the production deployment: roughly 180 MB of TSDB per day at ~35k active
series. Sizing the retention window is a deliberate choice, not a default to
inherit by accident — a migrated history older than the window ages out
silently on first compaction.

## 8. Failure modes

| Symptom | Cause | Where to look |
|---|---|---|
| Poller exits immediately, `prometheus.remote_write_url cannot be empty` | No metrics backend configured | `/etc/go-netmon/config.yaml` |
| Poller runs, no device metrics appear | Web UI down, so the device list is empty | `journalctl -u go-netmon-webui` |
| Prometheus takes minutes to become ready after a migration | WAL replay over a restored TSDB | `curl 127.0.0.1:9090/-/ready` |
| `ProtectSystem=strict` denials on a new path | Path not in `ReadWritePaths` | `journalctl -u <unit>` |
| Unit killed with `MemoryMax` in the log | Ceiling hit | Raise the ceiling, or find the leak |

## 9. Tests

There is no automated test for the deploy path: it needs a target host, and a
mock target would only test the mock. What is verified, and how:

- **Shell syntax**: `make lint-scripts` (shellcheck) and `bash -n`.
- **Build**: `deploy.sh` fails on a build error before it touches the target.
- **Preflight**: SSH reachability, passwordless sudo, target architecture, and
  the presence of systemd are all checked before anything is copied.
- **Post-deploy**: every unit is polled with `systemctl is-active`, and on
  failure the last 30 journal lines for each dead unit are printed.
- **Post-migration**: Prometheus is polled on `/-/ready` until the WAL replay
  finishes, then the unit states are reported.

A deployment is confirmed by checking that the device count and the metric
history on the target match the source — see [user.md](user.md) §5.
