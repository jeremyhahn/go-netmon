# Deployment — CLI

> **Status: NORMATIVE** · Scope: go-netmon native deployment · Last verified: 2026-08-19
> Conflicts with this document are defects. Fix them here first.

Throughout, `HOST` is the appliance's hostname or address and `USER` is an SSH
account on it that can `sudo` without a password prompt.

## 1. `make` targets

| Target | What it does |
|---|---|
| `make deploy HOST=… [USER=…]` | Build, ship, install, enable and start. |
| `make deploy-dry-run HOST=…` | Print the plan; change nothing. |
| `make prometheus-bin` | Extract the Prometheus binary into `dist/`. |
| `make migrate HOST=… FROM=…` | Restore a backup onto the appliance. |
| `make migrate-dry-run HOST=… FROM=…` | Print the migration plan. |

Make-level variables:

| Variable | Default | Meaning |
|---|---|---|
| `HOST` | *(required)* | Target appliance. |
| `USER` | your login name | SSH user on the target. |
| `FROM` | *(required for migrate)* | Backup directory to restore. |
| `WITH_PROMETHEUS` | `1` | Install the metrics backend. Set empty to skip. |
| `PROMETHEUS_IMAGE` | `prom/prometheus:latest` | Image `prometheus-bin` extracts from. |
| `DEPLOY_ARGS` | — | Extra flags passed through to `deploy.sh`. |
| `MIGRATE_ARGS` | — | Extra flags passed through to `migrate.sh`. |

## 2. `scripts/deploy.sh`

```
deploy.sh --host HOST [options]
```

| Flag | Env | Default | Meaning |
|---|---|---|---|
| `--host HOST` | `HOST` | *(required)* | Target hostname or address. |
| `--user USER` | `USER` | your login name | SSH user on the target. |
| `--with-prometheus` | `WITH_PROMETHEUS` | off | Also install Prometheus. |
| `--prometheus-bin PATH` | `PROMETHEUS_BIN` | `dist/prometheus` | Binary to ship. Implies `--with-prometheus`. |
| `--skip-build` | `SKIP_BUILD` | off | Use the binaries already in `bin/`. |
| `--force-config` | `FORCE_CONFIG` | off | Replace `config.yaml` and `prometheus.yml` on the target. |
| `--no-start` | `NO_START` | off | Install and enable the units without starting them. |
| `--dry-run` | `DRY_RUN` | off | Print the plan; change nothing. |
| — | `SSH_OPTS` | empty | Extra arguments for `ssh`. |

Exit status is 0 only if every unit is active afterwards. On failure the last
30 journal lines of each dead unit are printed to stderr.

```sh
# First deployment of a self-contained appliance
make prometheus-bin
make deploy HOST=monitor.example USER=ops

# Redeploy new binaries, leaving configuration alone
make deploy HOST=monitor.example USER=ops

# Stage everything but leave the services down for a maintenance window
./scripts/deploy.sh --host monitor.example --user ops --with-prometheus --no-start

# Deploy against a Prometheus that lives elsewhere
make deploy HOST=monitor.example USER=ops WITH_PROMETHEUS=
```

## 3. `scripts/migrate.sh`

```
migrate.sh --host HOST --backup DIR [options]
```

| Flag | Env | Default | Meaning |
|---|---|---|---|
| `--host HOST` | `HOST` | *(required)* | Target, already deployed to. |
| `--user USER` | `USER` | your login name | SSH user on the target. |
| `--backup DIR` | `BACKUP_DIR` | *(required)* | Backup directory to restore. |
| `--remote-backup` | `REMOTE_BACKUP` | off | `DIR` is a path **on the target**; skip the upload. |
| `--replace` | `REPLACE` | off | Overwrite target data that is already present. |
| `--keep-going` | `KEEP_GOING` | off | Restore what is present instead of requiring every member. |
| `--dry-run` | `DRY_RUN` | off | Print the plan; change nothing. |

The backup directory is the format `netmon-backup` writes: a `manifest.json`,
a `checksums.sha256`, and `volumes/*.tar.gz`. Checksums are verified before
anything is stopped, and again after upload.

```sh
# Migrate from a backup on this machine
make migrate HOST=monitor.example FROM=backups/prod-premigration

# The backup is already staged on the appliance
./scripts/migrate.sh --host monitor.example \
    --remote-backup --backup /var/backups/go-netmon-migration/prod-premigration

# Re-run onto an appliance that already holds data
make migrate HOST=monitor.example FROM=backups/prod MIGRATE_ARGS=--replace
```

## 4. Operating the installed services

These run on the appliance, not on the deploy host.

```sh
systemctl status go-netmon go-netmon-webui prometheus
systemctl restart go-netmon

journalctl -u go-netmon -f              # logs are JSON on stdout
journalctl -u go-netmon --since -1h

# Reload Prometheus scrape config without dropping the head block
curl -X POST http://127.0.0.1:9090/-/reload
```

The `netmon` CLI is installed to `/usr/local/bin/netmon` alongside the
services; its own verbs are documented in [../cli.md](../cli.md).

## 5. Uninstalling

There is no `make uninstall`: removing a monitoring appliance's data is not
something to make one keystroke away.

```sh
systemctl disable --now go-netmon go-netmon-webui prometheus
rm /etc/systemd/system/{go-netmon,go-netmon-webui,prometheus}.service
systemctl daemon-reload
rm /usr/local/bin/{go-netmon,go-netmon-webui,netmon,netmon-backup,mib-importer,prometheus}
# Data and configuration are left deliberately. Remove them only when you are
# certain, and after a backup:
#   rm -rf /var/lib/go-netmon /var/lib/prometheus /etc/go-netmon
#   userdel go-netmon && userdel prometheus
```
