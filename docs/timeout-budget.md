# The web UI's timeout budget

> **Status: REFERENCE** · Scope: go-netmon · Last verified: 2026-10-09

An unreachable device used to make the web UI drop the connection instead of
answering. The CLI reported:

```
netmon api: cannot reach the server: POST .../api/devices/3/test-snmp: EOF
```

`EOF` means the server closed the socket without writing a response. That is
what `net/http` does when a connection's write deadline expires while the
handler is still running: there is no partial response, no status, nothing for
a client to act on.

## Root cause

`POST /api/devices/{id}/test-snmp` probed the device with a 10s per-attempt
SNMP timeout and 2 retries. gosnmp makes `Retries + 1` attempts, so the probe
took **30s** against a device that never answers, inside a **15s**
`http.Server.WriteTimeout`.

Measured in-process against a UDP socket that binds but never replies:

```
performSNMPTest took 30.026s
Post "http://127.0.0.1:36175/x": EOF   (after 30.023s)
```

## The budget

Every value lives in `internal/webui/api/timeouts.go`. The chain only works if
each link fits inside the one outside it:

```
SNMPProbeTimeout x SNMPProbeAttempts  <=  SNMPProbeBudget  <=  HandlerBudget  <  ServerWriteTimeout
PollerRequestTimeout                   <   PollerSyncBudget <=  HandlerBudget  <  ServerWriteTimeout
PollerRequestTimeout                   <   FleetStatusBudget <= HandlerBudget  <  ServerWriteTimeout
```

| Constant               | Value | Why                                                                                   |
| ---------------------- | ----- | ------------------------------------------------------------------------------------- |
| `SNMPProbeTimeout`     | 2s    | One SNMP attempt. A device that has not answered in this long on a LAN is not going to. |
| `SNMPProbeRetries`     | 2     | 3 attempts in total.                                                                    |
| `SNMPProbeBudget`      | 8s    | Hard ceiling on a whole probe, connect and encoding included. 6s of SNMP with slack.    |
| `PollerRequestTimeout` | 3s    | One HTTP call to the poller's control API.                                              |
| `PollerSyncBudget`     | 5s    | A whole best-effort push of a device to the poller, retries included.                   |
| `FleetStatusBudget`    | 8s    | A whole bulk fleet request: one call to the poller's device listing, however large the fleet. Exceeds `PollerRequestTimeout` so the poller client's own deadline and retries expire first. |
| `HandlerBudget`        | 10s   | The widest any single handler's outbound work may be.                                   |
| `ServerWriteTimeout`   | 15s   | The connection's write deadline. 5s of headroom to serialize and write a response.      |
| `ServerReadTimeout`    | 15s   | Bounds reading a request, including its body. Not part of the chain.                    |
| `ConfigImportBudget`   | 10m   | A configuration import, which parses every MIB module in the bundle. Deliberately outside the chain: the import handler extends its own connection deadline rather than widening `ServerWriteTimeout` for every endpoint. |

`TestTimeoutBudgetOrdering` asserts the ordering, so an edit that inverts a
pair fails the build rather than the field. `TestServerTimeoutsComeFromTheBudget`
keeps the running listener wired to the same constants.

The budget was not widened to accommodate the slow path. The probe was made to
fit: gosnmp honors the deadline of the context it is given on every attempt, so
`performSNMPTest` binds the probe to `context.WithTimeout(ctx, SNMPProbeBudget)`
and a caller-supplied timeout can no longer push a handler past its budget.

## What an unreachable device returns now

The same `SNMPTestResponse` shape as a success, with a status that says what
happened:

| Outcome                                        | Status | Body flag      |
| ---------------------------------------------- | ------ | -------------- |
| The device answered                            | 200    | `success:true` |
| The device stayed silent for the whole budget  | 504    | `timed_out`    |
| The probe failed some other way (connect, ICMP)| 502    | —              |
| The server refused the request (bad port/version) | 400 | `rejected`     |

The body names the device and the address, so the CLI renders:

```
FAILED: snmp: device bastion (198.51.100.14:161) did not answer within 8s: request timeout (after 2 retries)
```

`pkg/client` recovers the structured body from a 502/504 rather than surfacing
an opaque `504 Gateway Timeout`.

## Head-of-line blocking

Two relationships were fixed so one dead device cannot stall unrelated work.

**In the web UI**, `syncDeviceWithPoller` runs inside the MIB assignment
handler. Its outcome is never reported to the caller — the change is already
durable and the poller re-reads it on its next configuration sync — so it must
never be what makes a request time out. It is now capped at `PollerSyncBudget`.

**In the poller**, `RemoveDevice` held the poller's write lock while closing the
device's SNMP client, and `snmp.Client.Close` took a *write* lock that waited
for whatever request that client had in flight. Against a dead device that wait
is the full `SNMPTimeout x (SNMPRetries + 1)` budget — 40s with the shipped
`snmp_timeout: 10` and `snmp_retries: 3`. Every other device's registration,
update and status lookup queued behind it, which is why the web UI's PUT to
`/api/poller/devices/{name}` timed out three times over and the MIB assignment
endpoint blocked for 17-30s.

Measured before and after, with a worker mid-request against a silent device:

```
RemoveDevice took 39.535s     ->     RemoveDevice took 89.7us
```

`Close` now takes a read lock: closing the socket is precisely how an in-flight
request is interrupted, so waiting for that request to finish first defeats the
purpose. `RemoveDevice` releases the poller lock before any teardown. Teardown
is still not strictly bounded: `Close` queues behind `Connect`, which holds the
client's write lock for the whole dial, so the poller lock must stay released
across it.

## Two poller bugs found on the way

Both are covered by regression tests in `internal/poller/lifecycle_test.go`.

- **Removed workers never stopped.** Every worker shared the poller's single
  stop channel, so `RemoveDevice` only deleted the map entry. The goroutine kept
  polling for the life of the process, and because an update is a remove
  followed by an add, every MIB assignment change leaked another one. Workers
  now have their own stop signal.
- **API-registered workers died with the request.** `startWorker` ran the worker
  under the caller's context, which for the control API is `r.Context()` — so a
  device registered or updated over HTTP polled exactly once and stopped when
  the handler returned. Workers now run under the poller's own lifetime, taken
  from `Start`.
