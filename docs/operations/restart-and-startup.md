# Restarting the web UI is a dashboard outage

`go-netmon-webui` parses its MIB catalog before it binds `:8080`. Nothing
answers until that finishes, so **every restart takes the dashboard down for as
long as the parse takes** — on monitor-01, with 176 modules imported, that has been
measured at **203 seconds**.

That is worth knowing before an incident rather than during one, because the
moment somebody needs the dashboard is exactly the moment they are most likely
to restart it.

## Telling "still starting" from "wedged"

Both look the same from outside the process: no listener, no error, nothing on
the port. Three signals separate them.

**The unit is `Type=notify`.** It reports `activating` until the process has
bound its port and said so, and `active` only after. `systemctl is-active`
therefore means *serving*, not *forked*.

```
$ systemctl is-active go-netmon-webui
activating       # still parsing — this is a healthy start
```

**The status line moves.** `systemctl status` shows how far the load has got,
and a counter that advances is the difference between a slow start and a stuck
one:

```
$ systemctl status go-netmon-webui
     Active: activating (start) since ...
     Status: "loading imported MIB modules: 84/176 (CISCO-PROCESS-MIB)"
```

A status line that has not moved in a minute is worth investigating. One that
is climbing is a service doing its job.

**The journal states the cost when it is over.** The line to grep for:

```
Listening on :8080 — the web UI is ready to serve (startup took 3m23.108s, during which it served nothing)
```

## What NOT to do

**Do not judge a deploy on `systemctl is-active` alone with the old unit, and do
not restart a service that is `activating`.** Before the unit became
`Type=notify` it reported `active` from the moment the process forked, and a
deploy of a good binary was read as hung and rolled back on that signal — twice
the outage, and a month-old build restored to a production dashboard.
`scripts/deploy.sh` now waits for the port to answer rather than for the unit to
say it is up.

If you are checking by hand, check the port:

```
until curl -fsS -o /dev/null http://127.0.0.1:8080/; do sleep 5; done
```

## Why a restart is usually much faster than the first one

The parsed catalog lives in Pebble under `/var/lib/go-netmon/webui/mib.db` and
survives the process. A restart reparses only the modules whose files have
changed since they were parsed, so a warm start costs about the time it takes to
open the store. Measured on a 153-module catalog: **cold 16.9s, warm 1.2s**.

A start that pays the full cost again means one of:

- the catalog directory was emptied or moved (a fresh install, a restore, a
  changed `-mib-catalog` path);
- MIB files were genuinely edited on disk, which is what makes them reparse.

## Reload without restarting

`systemctl reload go-netmon-webui` re-reads the configuration and re-applies the
**alarm rules** with no restart and no outage. Everything else — the MIB
catalog, the record store, the listening socket — is bound at startup. Tuning a
threshold is a reload, not a restart.

## The poller

`go-netmon.service` is ordered after the web UI and starts once the web UI is
serving, because it fetches the device list from it and gives up after about 30
seconds of trying. A poller that came up first would find no devices and
silently monitor nothing. On a cold boot the poller therefore waits out the web
UI's start; that is the ordering working, not a hang.
