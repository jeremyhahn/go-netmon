# Unreachable devices

> **Status: REFERENCE** · Scope: go-netmon · Last verified: 2026-10-09

A device that stops answering SNMP used to cost a worker one full retry budget
per selected object, every cycle:

```
Error walking table OID ifLastChange (1.3.6.1.2.1.2.2.1.9) for device bastion: SNMP BULKWALK failed: request timeout (after 3 retries)
Error walking table OID ifName (1.3.6.1.2.1.31.1.1.1.1) for device bastion: SNMP BULKWALK failed: request timeout (after 3 retries)
```

With the shipped `snmp_timeout: 10` and `snmp_retries: 3`, one operation is
40s. `bastion` had 63 objects selected on a 60s interval, so a cycle took about 42
minutes and never finished. Nothing was ever reported: `netmon health` said
`last poll never` indefinitely, `gonetmon_polls_total` recorded neither a
success nor a failure for the device, and a goroutine and a socket were tied up
producing nothing.

Other devices were unaffected — one goroutine per device — so this is about the
dead device's own worker, the honesty of its status, and the waste.

## The rule

Reachability is judged **per device, per cycle**, never per object.

An operation is a chunk of scalar GETs or one table walk. It counts as
**answered** if anything at all came back from the agent:

| Outcome                                        | Reachable? |
| ---------------------------------------------- | ---------- |
| The operation succeeded                        | yes        |
| The agent returned an error status (noSuchName…)| yes       |
| The agent answered `tooBig`                    | yes        |
| A walk delivered rows and then stopped         | yes        |
| Every attempt timed out                        | **no**     |
| ICMP port-unreachable: nothing is listening    | **no**     |

That table is `snmp.Unanswered`, and it is the only definition of silence in
the codebase. `snmp.TimedOut` is the narrower question the web UI's SNMP probe
asks, and it is the same code.

A walk that broke off part-way is deliberately on the reachable side. gosnmp
throws the partial rows away, so `snmp.WalkError.Received` carries how many
arrived — without it, a device with one slow table looks exactly like a device
that is gone.

**A cycle is abandoned** once the device has left three operations in a row
unanswered *and* answered nothing else in that cycle — and then only if it also
fails a direct GET of `sysUpTime.0`, the one object every SNMP agent
implements. If sysUpTime comes back, the silence was about those objects, the
cycle carries on, and nothing is marked unreachable. The probe costs one
request, once, and only in a cycle that is already failing.

The probe is what makes the ordering safe: a device whose first three walks
happen to fall in a wedged subtree is not down, and treating it as down would
stop collecting data that was there for the asking.

**A device is unreachable** when it answered no part of a cycle. A device with
one object selected can never reach the threshold, but a cycle in which
everything attempted went unanswered is still a device that is not there.

`bastion` now costs four requests per attempt instead of sixty-three.

## The schedule

Backoff doubles from the device's own poll interval and stops at 15 minutes:

| Consecutive failed cycles | 1   | 2   | 3   | 4   | 5+  |
| ------------------------- | --- | --- | --- | --- | --- |
| Wait, on a 60s interval   | 60s | 2m  | 4m  | 8m  | 15m |

A device polled less often than the ceiling keeps its own interval — backing
off to something shorter than the schedule it was given would poll a dead
device more often than a live one.

**One reply resets it.** A recovered device is back on its normal interval on
the very next cycle rather than serving out the penalty it earned.

## A partially answering device is not this

`pdu-03` answers its system group and times out on objects it half
implements: 79 successful polls alongside 52 per-object errors. It is healthy.
It keeps its normal interval, it is never marked unreachable, and every object
it does answer is still collected. `TestPartiallyAnsweringDeviceKeepsCollecting`
and `TestWedgedSubtreeFirstDoesNotLookUnreachable` are the regressions for that,
and they are the ones to keep working: getting this wrong stops collecting real
data, which is worse than the bug being fixed here.

## What an operator sees

**Metrics.** `gonetmon_device_unreachable{device}` is 1 while a device answers
nothing and 0 while it answers. It is published at 0 when a device is
registered, so an alert has a series to watch before anything has gone wrong,
and deleted when the device is removed.

```promql
gonetmon_device_unreachable == 1
```

`gonetmon_polls_total{device,status="error"}` now moves for these devices at
all, which it never did before: an abandoned cycle is reported as a failed
poll. "Unreachable" and "still starting up" are finally different on a
dashboard.

**Status.** `GET /api/poller/devices/{name}/status` carries three more fields:

```json
{
  "name": "bastion",
  "connected": false,
  "last_poll_time": "2026-08-09T18:04:11Z",
  "last_error": "poller: device bastion answered none of 4 SNMP operations; skipped the remaining 59",
  "unreachable": true,
  "consecutive_failures": 5,
  "next_poll_time": "2026-08-09T18:19:11Z"
}
```

`unreachable` is deliberately narrower than `!last_poll_success`: a device that
answers some objects and errors on others has failed polls forever and is
reachable.

A registered device that has not produced a poll result yet gets a status with
a zero `last_poll_time` rather than a 404 — "never polled" and "polled and
failed" are different facts, and a failed poll now always stamps the time.

**CLI.**

```
$ netmon health
SERVICE          STATE  DETAIL
poller           up     polling bastion, last poll 4m ago (bastion is unreachable after 5 failed polls, retrying in 11m2s)

$ netmon monitor bastion
Connected          no
Last poll          4m ago
Last error         poller: device bastion answered none of 4 SNMP operations; skipped the remaining 59
Unreachable        yes, after 5 failed polls
Next poll          11m2s
```

The last two rows appear only for a device that is answering nothing.

## What was not changed

The SNMP timeout. `snmp_timeout × (snmp_retries + 1)` is one link in the budget
in `internal/webui/api/timeouts.go` (see [timeout-budget.md](timeout-budget.md)),
and shortening it to paper over this would have traded a correct fix for a
device that answers slowly under load. The fix is to stop making 63 requests to
a device that will not answer one.

## Where it lives

| Concern                                   | File                              |
| ----------------------------------------- | --------------------------------- |
| What counts as silence                    | `internal/snmp/reachable.go`      |
| Partial-walk evidence                     | `internal/snmp/errors.go`         |
| Threshold, backoff, the published gauge   | `internal/poller/reachability.go` |
| The abandon decision and the sysUpTime probe | `internal/poller/poller.go`    |
| Poll failures as types                    | `internal/poller/errors.go`       |
| Merging poll history with reachability    | `internal/api/poller_control.go`  |
