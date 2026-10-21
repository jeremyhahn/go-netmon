# Deployment — user guide

> **Status: NORMATIVE** · Scope: go-netmon native deployment · Last verified: 2026-08-19
> Conflicts with this document are defects. Fix them here first.

How to put go-netmon on a machine and keep it there. `HOST` is the appliance
and `USER` is an SSH account on it that can `sudo` without a password.

## 1. What the appliance needs

- Linux on x86-64, with systemd.
- SSH access, and `sudo` that does not prompt.
- Roughly 4 GB of RAM and 4 cores for a fleet of this size.
- Disk for the metric history. Budget from measurement, not hope: this
  deployment writes about **180 MB of TSDB per day** at ~35k active series, so
  the configured 90-day retention needs roughly 16 GB.
- Locked memory (`RLIMIT_MEMLOCK`) for the SNMP credentials; the units ask for
  it themselves, see [Locked memory](#locked-memory).
- Nothing else. No Docker, no Go toolchain, no checkout of this repository.

### Locked memory

The poller and the web UI keep every device's SNMP community string and v3
passphrases sealed, and open them into locked memory only for the length of
one request. One open locks at most 24 KiB — three secrets, each a 4 KiB
plaintext page and its data key's page — and the sealer keeps 12 KiB of keys
locked for the life of the process. Both processes size a budget from the
`RLIMIT_MEMLOCK` they start with, log it once
(`credential locked-memory budget`), and make an open wait while the budget is
in use rather than fail.

| Where | Setting | Holds at once |
|---|---|---|
| systemd units in `deploy/` | `LimitMEMLOCK=16M` | about 680 opens: a 256-device fleet polling at the same instant, better than 2x over |
| Docker Compose files | `ulimits: memlock` of 16777216 | the same |
| Docker default, or systemd before 253 | 64 KiB | two — every other poll waits |

A limit too small to open even one device's credentials stops the service at
startup with `RLIMIT_MEMLOCK is N bytes, below the M bytes opening a credential
needs`. Raise it: `LimitMEMLOCK=` in the unit, `ulimits: memlock` in Compose,
`docker run --ulimit memlock=<bytes>`, or `ulimit -l`. For a fleet larger than
256 devices, give each device 24 KiB plus 12 KiB in all, and round up.

The machine that runs `make deploy` needs a Go toolchain and, for
`make prometheus-bin`, Docker.

## 2. First deployment

```sh
make prometheus-bin                      # once, stages dist/prometheus
make deploy HOST=monitor.example USER=ops
```

That builds five static binaries, ships them with the bundled standard MIBs and
the unit files, creates the `go-netmon` and `prometheus` system accounts,
writes `/etc/go-netmon/`, and enables and starts three services. It prints each
step and exits non-zero if any unit fails to come up, with that unit's last 30
journal lines.

Then open `http://HOST:8080` and add devices. **Read
[../operations/security-posture.md](../operations/security-posture.md) first**:
that page is unauthenticated and it will hand out your SNMP community strings
to anyone who asks.

To see what would happen without doing it:

```sh
make deploy-dry-run HOST=monitor.example USER=ops
```

## 3. Redeploying

Exactly the same command. It converges: new binaries replace old ones, unit
files are rewritten only if they changed, and your configuration and data are
left alone.

```sh
make deploy HOST=monitor.example USER=ops
```

To replace the configuration too — which you will rarely want, because the
appliance's config is operator data once it is running:

```sh
make deploy HOST=monitor.example USER=ops DEPLOY_ARGS=--force-config
```

The replaced file is kept beside the new one as
`config.yaml.replaced-<timestamp>`.

## 4. Migrating an existing deployment

Moving a containerised go-netmon onto an appliance is two steps: take a backup
on the old host, restore it on the new one.

**On the old host**, with the stack running:

```sh
make backup NAME=premigration
```

That stops the stack for the length of the copy — the stores are Pebble and a
Prometheus TSDB, and neither can be snapshotted safely while it is being
written — and starts it again afterwards, on every exit path.

**Then, from anywhere that can reach both:**

```sh
make deploy  HOST=monitor.example USER=ops             # appliance first
make migrate HOST=monitor.example USER=ops FROM=backups/premigration
```

What moves, and what it means if you skip it:

| Component | Lands in | If you lose it |
|---|---|---|
| `metrics` | `/var/lib/prometheus` | **All monitoring history.** go-netmon stores no samples itself. |
| `webui` | `/var/lib/go-netmon/webui` | **Every device.** The device list lives here, not in `config.yaml`. |
| `poller` | `/var/lib/go-netmon/poller` | Uploaded MIB modules; re-importable, but tedious. |
| `grafana` | not restored | Nothing. Dashboards regenerate from MIBs. |

The migration verifies checksums before it stops anything, restores each
component into `<dir>.incoming` and renames it into place, and keeps whatever
was there before as `<dir>.replaced`. **The source host is never touched** — it
is not even contacted. Decommission the old stack yourself, after §5 passes.

If the backup is already sitting on the appliance:

```sh
./scripts/migrate.sh --host monitor.example --user ops \
    --remote-backup --backup /var/backups/go-netmon-migration/premigration
```

## 5. Confirming it actually worked

Do not skip this, and do not settle for "the services are running". Run these
on the appliance:

```sh
systemctl is-active go-netmon go-netmon-webui prometheus

# Prometheus replays its write-ahead log first; on a migrated TSDB that is
# minutes, not seconds. Wait for ready, not healthy.
curl -s http://127.0.0.1:9090/-/ready

# Same devices as before?
curl -s http://127.0.0.1:8080/api/devices | jq 'length'

# Same history? The oldest sample should predate the migration.
curl -s http://127.0.0.1:9090/api/v1/status/tsdb | jq '.data.headStats'
```

Compare the device count and the history span against the old deployment.
Matching numbers are the confirmation; a running process is not.

Once you are satisfied, clean up:

```sh
sudo rm -rf /var/lib/go-netmon/*.replaced /var/lib/prometheus.replaced
```

## 6. Day-to-day

```sh
systemctl status go-netmon
journalctl -u go-netmon -f           # JSON lines
journalctl -u go-netmon -p err       # errors only
systemctl restart go-netmon
```

Raise the logging level by editing `logging.level` in
`/etc/go-netmon/config.yaml` and restarting; `debug` turns on per-OID tracing
and is loud.

**Restarting the web UI takes the dashboard down** for as long as its MIB
catalog takes to parse — minutes, on a cold catalog. It is `Type=notify`, so it
reports `activating` until it is really serving and `systemctl status` shows how
far the load has got. Read
[../operations/restart-and-startup.md](../operations/restart-and-startup.md)
before restarting it during an incident, and use `systemctl reload
go-netmon-webui` when all you are changing is an alarm threshold.

## 7. When something is wrong

**The poller exits immediately.** Look for
`prometheus.remote_write_url cannot be empty`. The poller has no local sample
storage and refuses to run without somewhere to write. Either deploy the local
Prometheus or point the URL at one that exists.

**The poller runs but no device metrics appear.** The device list comes from
the web UI. If `go-netmon-webui` is down, the poller starts with nothing to
poll and says so. Check `journalctl -u go-netmon-webui`.

**Prometheus will not become ready after a migration.** WAL replay over a large
restored TSDB takes minutes. `journalctl -u prometheus` reports replay
progress. If it is being killed instead, look for `MemoryMax` in the log and
raise the ceiling in the unit.

**A unit dies on a permission error for a path you just added.** The units run
under `ProtectSystem=strict`; anything writable has to be named in
`ReadWritePaths`.

**Metric history older than 90 days disappears.** That is retention doing its
job: `--storage.tsdb.retention.time=90d` in `prometheus.service`. Raise it
before you need the data, not after — expired blocks are gone, and no backup
you take afterwards will bring them back. Budget ~180 MB per day.
