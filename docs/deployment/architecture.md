# Deployment — architecture

> **Status: NORMATIVE** · Scope: go-netmon native deployment · Last verified: 2026-08-19
> Conflicts with this document are defects. Fix them here first.

## 1. The problem

go-netmon shipped as a Docker Compose stack: four containers, four named
volumes, a bridge network. That is a reasonable way to run it on a workstation
that already has a Docker daemon. It is the wrong way to run it on a dedicated
monitoring appliance, where the container runtime is a second supervisor, a
second network stack and a second failure domain layered under a workload that
needs none of them.

This feature deploys go-netmon as **native systemd services** onto a host that
has SSH, sudo and systemd, and nothing else.

## 2. The appliance is three units, and one of them is not optional

The monitoring appliance runs **three** services: `go-netmon` (the poller),
`go-netmon-webui`, and `prometheus`. A fourth service, `go-sysmon`, shares the
host but is a separate product and is not part of this deployment.

Prometheus is there by explicit ruling, and the reason has to survive contact
with a future reader who thinks the box would be tidier with one fewer daemon:

> **Remove Prometheus and go-netmon does not start at all** — `config.Validate`
> rejects an empty `prometheus.remote_write_url` outright
> ([`internal/config/types.go:124`](../../internal/config/types.go)) — and
> every sample ever collected, which lives only in its TSDB, is orphaned along
> with it.

That is the whole answer to "why is there a time-series database on a box
scoped to two services". It is not a dashboard, it is the storage layer, and
go-netmon has none of its own.

## 3. What actually stores what

This is the load-bearing fact about go-netmon's architecture, and getting it
wrong loses data:

**go-netmon stores no time series.** The poller walks SNMP, turns the results
into samples, and pushes them to a Prometheus remote-write endpoint. It keeps
no sample storage of its own — `internal/metrics` is exporter, naming and
index code, and `config.Validate` rejects an empty
`prometheus.remote_write_url` outright rather than degrade to a local store
that does not exist.

The consequence: **Prometheus is not a dashboard layer, it is the database.**
Every metric the system has ever recorded is in its TSDB. Dropping it does not
simplify the deployment, it deletes the monitoring history and stops the poller
from starting.

Where each kind of state lives:

| State | Store | Path on the appliance |
|---|---|---|
| Monitoring history (all samples) | Prometheus TSDB | `/var/lib/prometheus` |
| Device registry (addresses, communities, profiles) | Pebble | `/var/lib/go-netmon/webui/webui.db` |
| Parsed MIB catalog, web UI's copy | Pebble | `/var/lib/go-netmon/webui/mib.db` |
| MIB modules as uploaded | files | `/var/lib/go-netmon/poller/uploaded` |
| Parsed MIB catalog, poller's copy | Pebble | `/var/lib/go-netmon/poller/mib-storage` |
| Generated Grafana dashboards | files | `/var/lib/go-netmon/dashboards` |
| Deployment shape | YAML | `/etc/go-netmon/` |

Note what is **not** in `config.yaml`: the devices. They live in the web UI's
Pebble store and the poller pulls them from it at startup. Migrating a
deployment therefore means moving that store, not copying a config file.

## 4. Components and process boundaries

Three long-running processes, three units, one host.

```
   SNMP (UDP 161)                  ┌──────────────────────────┐
  ┌────────────────────────────────┤  go-netmon.service       │
  │                                │  poller                  │
  ▼                                │  :9080 control API       │
 devices                           │  :9100 own metrics       │
                                   └───────┬──────────┬───────┘
                                           │          │ remote write
                          device list      │          ▼
                          (HTTP, loopback) │   ┌──────────────────────┐
                                           │   │ prometheus.service   │
   operator ──── :8080 ──┐                 │   │ TSDB, :9090 loopback │
                         ▼                 │   └──────────┬───────────┘
              ┌──────────────────────┐     │              │ PromQL
              │ go-netmon-webui      │◀────┘              │
              │ .service             │◀───────────────────┘
              │ device registry      │
              └──────────────────────┘
```

- **The web UI owns the device registry.** The poller is a consumer of it.
  That is why the web UI starts first and why the poller tolerates it being
  down rather than refusing to run.
- **Prometheus binds loopback only.** Nothing outside the host has any reason
  to reach it, and it has no authentication of its own.
- **Device metrics never get scraped.** They arrive by remote write. The only
  scrape targets are the appliance's own two processes.

## 5. Rejected alternatives

**Keep Docker Compose on the appliance.** Rejected: the appliance exists to do
one job, and a container runtime on it is an extra supervisor, an extra network
namespace and an extra thing to patch, in exchange for isolation between three
processes that already trust each other completely.

**Drop Prometheus and let go-netmon store its own metrics.** Rejected: it
cannot. There is no sample store in the codebase, and adding one is a rewrite
of the storage layer, not a deployment decision. Running it as a second unit
on the appliance was ratified by the owner rather than assumed.

**Drop Prometheus and point remote-write at a Prometheus elsewhere.** Rejected
as the default, but supported: `WITH_PROMETHEUS=` skips the local server. It
makes the appliance depend on a host outside itself for the monitoring system
to function, which is the wrong trade for a box whose purpose is to keep
working when other things break. Choose it deliberately, not by omission.

**Keep Grafana.** Rejected: it genuinely *was* a dashboard layer. go-netmon
serves its own web UI, and `internal/grafana/builder` generates dashboard JSON
on demand for anyone who wants to import it into a Grafana that lives
elsewhere. Nothing is stored in Grafana that cannot be regenerated.

**Ship a distro package instead of a tarball over SSH.** Rejected for now:
building and hosting a repository is more machinery than a three-binary
deployment to a small number of hosts justifies. The install step is already
idempotent, which is the property packaging would have bought.

## 6. Trust boundaries

- **The web UI is unauthenticated.** Every endpoint is open to whoever reaches
  the port, and `GET /api/devices` returns each device's SNMP community string
  in clear text. The unit binds `:8080` on all interfaces because an operator
  has to reach it; put the appliance on a network where that is acceptable, or
  put it behind something that authenticates. See
  [../operations/security-posture.md](../operations/security-posture.md).
- **SNMP community strings are credentials** and they are stored unencrypted in
  the web UI's Pebble store. `/var/lib/go-netmon` is mode 0750, owned by the
  service account.
- **All three services run as unprivileged system accounts** with an empty
  capability bounding set — `go-netmon` for the poller and web UI, `prometheus`
  for the store. Reachability is decided by whether an SNMP request answers, so
  no raw sockets and no `CAP_NET_RAW` are needed.
- **The deployment channel is SSH with sudo.** Whoever can deploy can root the
  appliance; that is inherent, not incidental.

## 7. What this feature deliberately does not do

- **No TLS anywhere.** Not between the poller and Prometheus (loopback), not on
  the web UI. Terminating TLS is the network's job here.
- **No clustering, no HA.** One appliance, one TSDB. Prometheus remote-write
  makes a federated setup possible later; nothing here anticipates it.
- **No automatic rollback.** A failed deploy leaves the previous binaries and
  units in place and reports the failure; it does not try to reason about what
  a half-applied state should become.
- **No secret management.** There are no secrets in the deployment payload.
  Device credentials arrive through the web UI, after the appliance is up.
- **It does not decommission the source deployment.** A migration copies; the
  old stack is left exactly as it was found.
