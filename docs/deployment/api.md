# Deployment — API

> **Status: NORMATIVE** · Scope: go-netmon native deployment · Last verified: 2026-08-19
> Conflicts with this document are defects. Fix them here first.

Deployment adds no endpoints of its own. What it defines is **where the
existing APIs listen** once installed, and which of them cross a host boundary.
The endpoints themselves are documented with the features that own them —
[../webui-backend.md](../webui-backend.md) for the management API,
[../metrics.md](../metrics.md) for the metric surface.

## 1. Listening sockets

| Port | Bound to | Process | Surface | Reaches beyond the host |
|---|---|---|---|---|
| 8080 | all interfaces | `go-netmon-webui` | Web UI and management API | **Yes** |
| 9080 | all interfaces | `go-netmon` | Poller control API | Yes, but nothing should |
| 9100 | all interfaces | `go-netmon` | The poller's own Prometheus metrics | Yes, but nothing should |
| 9090 | `127.0.0.1` only | `prometheus` | Query API, remote-write receiver, admin | **No** |

Only 8080 is meant to be reached from off the appliance. 9080 and 9100 bind
all interfaces because the code takes a plain `listen_address`, not because
anything outside should connect; a host firewall is the right place to say so.

**Deploying alongside another service on the same host?** These four ports are
the ones this feature claims.

Watch **9100** in particular: it is the conventional `node_exporter` port, so
it is the one most likely to collide with a host-metrics agent someone adds
later. Here it serves the poller's own counters, not node metrics. If you need
to move it, change `metrics.listen_address` in `config.yaml` **and** the
`go-netmon` scrape target in `prometheus.yml` — changing one without the other
leaves the poller exporting to a port nothing reads, which looks like working
software right up until you go looking for the data.

## 2. Trust

**The management API on 8080 is unauthenticated.** There is no login, no token,
no allow-list. `GET /api/devices` returns each device's SNMP community string
in clear text, so reaching the port is equivalent to holding the credentials
for every monitored device. This is accepted only because the appliance is
expected to sit on an internal network. Do not forward the port, and do not put
it behind a proxy reachable from anywhere less trusted, without adding
authentication first. See
[../operations/security-posture.md](../operations/security-posture.md).

**Prometheus on 9090 is unauthenticated too**, which is why it binds loopback.
`--web.enable-lifecycle` means `POST /-/reload` and `POST /-/quit` are live;
`--web.enable-remote-write-receiver` means anything that can reach the port can
inject samples. Both are safe at loopback and neither is safe off-host. If you
ever move Prometheus to a routable address, drop these flags first.

## 3. Internal calls the deployment wires up

These are the calls the unit files' flags and environment establish. All are
loopback; none crosses a host boundary.

| From | To | Purpose | Configured by |
|---|---|---|---|
| `go-netmon` | `127.0.0.1:9090/api/v1/write` | Ship samples | `prometheus.remote_write_url` in `config.yaml` |
| `go-netmon` | `127.0.0.1:8080` | Fetch the device list at startup and on sync | `WEBUI_URL` in `go-netmon.service` |
| `go-netmon-webui` | `127.0.0.1:9090` | PromQL for the dashboard | `-prometheus` in `go-netmon-webui.service` |
| `go-netmon-webui` | `127.0.0.1:9080` | Register and control devices | `-poller` in `go-netmon-webui.service` |
| `prometheus` | `127.0.0.1:9100` | Scrape the poller's own metrics | `prometheus.yml` |
| `prometheus` | `127.0.0.1:9090` | Self-monitoring | `prometheus.yml` |

Changing any address on one side without the other is the most common way to
break a deployment: the poller will start happily and simply produce nothing.

## 4. Health and readiness

Used by `deploy.sh` and `migrate.sh`, and worth knowing by hand:

| Endpoint | Meaning |
|---|---|
| `GET :9080/health` | Poller is up. Returns `{"status":"healthy","version":"…"}`. |
| `GET :9090/-/ready` | Prometheus is serving. **Not ready during WAL replay** — after a migration this can take minutes, which is why the migration polls it rather than sleeping a fixed interval. |
| `GET :9090/-/healthy` | Prometheus process is alive, replay or not. |
| `GET :9100/metrics` | Poll counters and durations, in Prometheus text format. |

Distinguish the two Prometheus endpoints carefully: `/-/healthy` says the
process has not crashed, `/-/ready` says it can answer queries. Only the second
one tells you the migration worked.

## 5. Useful queries after a migration

Run on the appliance, against loopback. These are how you prove history
survived rather than assuming it:

```sh
# Earliest and latest sample for a metric — should span the source's history
curl -s 'http://127.0.0.1:9090/api/v1/query?query=count(up)' | jq .

# How many distinct devices are reporting
curl -s --data-urlencode 'match[]={__name__=~".+"}' \
    http://127.0.0.1:9090/api/v1/label/instance/values | jq '.data | length'

# Total series in the head block
curl -s http://127.0.0.1:9090/api/v1/status/tsdb | jq '.data.headStats'

# Devices the web UI knows about
curl -s http://127.0.0.1:8080/api/devices | jq 'length'
```

## Configuring bind and target addresses

Every bind and target address resolves through viper with the precedence
**flag > environment > config file > default**. A flag counts only if it was
set on the command line. The environment variable is `NETMON_` plus the key
upper-cased, with `.` replaced by `_`.

| Key | Env | Flag | Default |
| --- | --- | --- | --- |
| `api.listen_address` | `NETMON_API_LISTEN_ADDRESS` | `go-netmon -api-listen` | `0.0.0.0:8080` |
| `metrics.listen_address` | `NETMON_METRICS_LISTEN_ADDRESS` | `go-netmon -metrics-listen` | `0.0.0.0:9100` |
| `webui.listen_address` | `NETMON_WEBUI_LISTEN_ADDRESS` | `webui -listen` | `:8080` |
| `webui.prometheus_url` | `NETMON_WEBUI_PROMETHEUS_URL` | `webui -prometheus` | `http://localhost:9091` |
| `webui.poller_url` | `NETMON_WEBUI_POLLER_URL` | `webui -poller` | `http://localhost:9080` |
| `dashboard.prometheus_url` | `NETMON_DASHBOARD_PROMETHEUS_URL` | `mib-importer dashboard … --prometheus-url` | `http://prometheus:9090` |
