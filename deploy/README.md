# Deploying go-netmon

`scripts/deploy.sh <host>` builds the binaries, stages them with the MIBs, the
configuration, the alert rules and the unit files, ships the lot over SSH and
runs `install.sh` on the far side. `make deploy` is the same thing.

## ⚠️ monitor-01 can only be deployed from `issue/59-catalog-driven-dashboard`

**A build from `main` will serve 404 on every page** while `/api/*` answers
normally — the shape that passes a post-deploy smoke test and fails for the
operator.

The web UI's static assets are served from a path RELATIVE to the process's
working directory on `main`, so the page resolves only where that directory
happens to contain a source checkout. monitor-01 was working because someone had put
one at `/usr/local/share/go-netmon/webui` by hand, plus a systemd drop-in
setting `WorkingDirectory`. Both are gone: the branch compiles the assets into
the binary (`internal/webui/assets.go`), which is what removed the need for
them.

So until the embed reaches `main` — PR #60, or this branch — the appliance and
`main` disagree about where the page lives, and only the branch is right.

**The real fix is merging #60.** This notice goes away with it, and so does the
class of bug: an embedded asset tree cannot be left behind by a deploy, and a
missing one is a startup failure with a name rather than a running service that
answers 404.

## What the payload contains

| | |
|---|---|
| `bin/` | the binaries, built `CGO_ENABLED=0` so they do not care what libc the host has |
| `mibs/standard` | the IETF base set, so a fresh install can parse the standard modules |
| `config.yaml` | seeded on first install only; an existing one is left alone unless `--force-config` |
| `prometheus.yml`, `rules/` | rules are replaced wholesale — they carry no site-specific settings |
| `*.service` | the unit files |

The web page is **not** in the payload. It is inside the binary.

## After a deploy

The web UI takes about three minutes to bind, because it parses every imported
MIB module at startup (179 of them on monitor-01). For those three minutes a restart
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
under test: measured on monitor-01, three of four e2e failures in one run were a
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
