# Deploying go-netmon

`scripts/deploy.sh <host>` builds the binaries, stages them with the MIBs, the
configuration, the alert rules and the unit files, ships the lot over SSH and
runs `install.sh` on the far side. `make deploy` is the same thing.

To install from a published release instead of a checkout, see
[docs/deployment/user.md](../docs/deployment/user.md#8-installing-a-release):
each release archive carries the same binaries and the unit files in
`deploy/`.

## What the payload contains

| | |
|---|---|
| `bin/` | the binaries, built `CGO_ENABLED=0` so they do not care what libc the host has |
| `mibs/standard` | the IETF base set, so a fresh install can parse the standard modules |
| `config.yaml` | seeded on first install only; an existing one is left alone unless `--force-config` |
| `prometheus.yml`, `rules/` | rules are replaced wholesale — they carry no site-specific settings |
| `*.service` | the unit files |

The web page is **not** in the payload. It is inside the binary.

`go-netmon.service` and `go-netmon-webui.service` set `LimitMEMLOCK=16M`: the
locked memory the sealed SNMP credentials are opened into, sized for a
256-device fleet polling at once. The Compose files set the same through
`ulimits: memlock`. How it is sized, and what too small looks like:
[docs/deployment/user.md](../docs/deployment/user.md#locked-memory).

## After a deploy

The web UI takes about three minutes to bind, because it parses every imported
MIB module at startup (measured with 179 imported modules). For those three minutes a restart
is indistinguishable from a crash, and `systemctl is-active` says `activating`
rather than `failed`. That is issue #58.

While it is down the poller keeps running but the web UI owns the device
registry, so repeated restarts thin the poller's data: a `rate()` window that
normally holds four samples can drop to one, and rate panels go briefly empty.
If you are deploying repeatedly, expect that and do not chase it as a bug —
measure the sample count before widening any window:

```
count_over_time(snmp_ifHCInOctets{device="core-01",ifindex="13"}[2m])
```

### Answering 200 is not being ready

The port opens before the metric catalog has been read. A dashboard asked for
in that window comes back with `catalog_unavailable` set and no panels, which
looks exactly like a device that has stopped reporting.

Waiting for `curl /` to return 200 is therefore not enough, and a test suite
started on that signal fails in ways that have nothing to do with the change
under test: in one measured run, three of four e2e failures were a
cold catalog, including a contrast failure that only appears on an empty
panel. Poll the thing you actually need instead — about 230 seconds:

```
until curl -s --max-time 20 http://127.0.0.1:8080/api/devices/1/dashboard \
  | grep -q '"panels"'; do sleep 10; done
```

## Reloading without a restart

```
systemctl reload go-netmon-webui
```

Re-reads `/etc/go-netmon/config.yaml` and applies the `alarms:` section. Nothing
else in the file is re-applied — everything else is bound at startup — and a
file that will not parse or will not validate leaves the running rules exactly
as they were, with the reason in the journal. See `docs/operations/alarms.md`.

## Checking that samples are actually arriving

```
curl -s localhost:9100/metrics | grep gonetmon_export
```

`gonetmon_export_dropped_samples_total{reason=...}` is the one to read first:
non-zero means samples were collected from a device and never delivered, and the
reason says which fault it is — a clock step, an endpoint that cannot keep up,
one the poller is producing out of order, or a shutdown that gave up. A count
against `superseded` after a device edit is expected and costs one sample; the
same count against `aged` across the whole fleet at once usually means the
poller's clock moved.

`docs/operations/export-health.md` has the table, including what a device edit
should and should not move.
