# Export ordering: what the receiver actually does

> **Status: REFERENCE** · Scope: go-netmon · Last verified: 2026-10-21

Two different faults are called "out of order", they behave nothing alike, and
telling them apart is what [#69][69] took fifteen hours and a million samples an
hour to learn. This is the measured behaviour, and how to reproduce it.

First measured against Prometheus 2.45.0 (the integration stack) on 2026-09-06.
The stack now pins 3.14.0, the version production runs ([#138][138]). The
receiver's behaviour is version-dependent, so the reproduction asserts it rather
than assuming it, and names the version when it is surprised.

## Disorder inside one request: accepted, and silently lossy

A remote-write request whose samples **descend within a series** is not refused.
Prometheus answers **HTTP 200** and keeps only the newest reading. The response
says nothing; only `prometheus_tsdb_out_of_order_samples_total`, on the
receiver, records that anything happened.

The cause is in the receive path. Every sample in one request is staged in a
single appender and checked against its series' **committed** head, which does
not move until the request commits. Two readings staged together are therefore
both acceptable when checked, and the loser is discarded at commit — which does
not fail.

This is what `orderedSamples` prevents. It is not protecting against a 400; it
is protecting against loss that nothing downstream can see.

## A reading behind its head: refused, and what the refusal costs

A reading offered **after a newer one for its series has been committed** cannot
be staged at all: the check has something to compare it against. The request is
refused with `400: out of order sample`. What happens to the rest of it depends
on the receiver's major version:

- **Prometheus 2.45.0** returned on the first failure and rolled the appender
  back, so **every other reading in the request was lost** with it. This is
  #69's headline, with the trigger correctly attributed, and it is why a
  retried batch was lethal rather than merely late: once fresher samples for its
  series had landed, the batch could never be accepted, and each attempt cost
  everything travelling with it.
- **Prometheus 3.14.0** (production, and the stack since #138) does the same.
  An earlier revision of this page said 3.x skips the offending reading and
  commits the rest; the first CI run against the 3.14.0 pin (#201, 2026-10-21)
  measured otherwise: `400: out of order sample`, and 200 of 200 good series in
  the request lost with it. #69's cost arithmetic still applies.

The reproduction keys this on the running version (`refusalCommitsTheRest` in
`test/integration/ordering_test.go`): a pin that changes the behaviour, or one
nobody has measured, fails the test rather than changing what a 400 costs
unnoticed.

Two things in the exporter follow from it:

- `partition` (`internal/metrics/sender.go`) holds back readings the endpoint
  has already moved past, so they are dropped by name — `reason="superseded"` —
  instead of being offered in a request that would then discard everything else
  in it.
- one sender goroutine owns the endpoint, so two requests can never be in flight
  with nothing deciding which arrives first. That ordering is the property the
  admission check is measured against.

## Reproducing it

```
make integration-test-ordering
```

Four tests in `test/integration/ordering_test.go`, against the real Prometheus
in the integration stack. Nothing touches monitor-01 or any live device.

| Test | Asks |
|---|---|
| `TestDisorderInsideOneRequestIsDiscardedWithoutASound` | what the receiver does with disorder inside one request |
| `TestOneReadingBehindTheHeadIsRefusedAtItsMeasuredCost` | what it does with a reading behind its head, and what the refusal costs |
| `TestOverlappingPollsAreRepairedAndNothingIsLost` | whether the exporter delivers both of two overlapping polls |
| `TestAPollTheEndpointHasMovedPastIsDroppedNotRetriedForever` | whether a superseded poll is given up on rather than retried |

The disorder is **produced**, not waited for. Two polls of one device are built
with per-reading timestamps — which is how `snmp.Client` stamps them — so that
the poll starting first finishes second, and they are exported in completion
order. That is what the overlap produces; stating it directly makes every run
drive the same disorder, where racing a real reconfiguration would reproduce it
only sometimes.

### What it is worth

Both halves fail without their fix. Measured on this stack when the tests were
written (figures are as recorded then, not re-measured):

- with the per-series sort removed, `TestOverlappingPollsAreRepairedAndNothing
  IsLost` stores **10 of 20** readings for every one of its 64 series — 640
  readings gone, with every flush reporting success;
- with `partition` removed, `TestAPollTheEndpointHasMovedPastIsDroppedNot
  RetriedForever` turns one late poll into a 400 and 64 samples dropped as
  `rejected`, instead of 64 dropped by name at no other cost.

This is a separate exercise from `scripts/measure-export-health.sh`, which
answers "did this deploy break anything" — a different question with a different
acceptable runtime.

## Counters

| Counter | Means |
|---|---|
| `gonetmon_export_repaired_order_total` | readings that arrived out of order for their series and were sorted before sending. The write succeeds. Above zero says something upstream — usually two overlapping polls of one device — is producing one series' readings out of order |
| `gonetmon_export_out_of_order_total` | readings the endpoint would refuse: behind their series' head, or a different value at an instant it already holds |
| `gonetmon_export_dropped_samples_total{reason="superseded"}` | those readings, given up on before the request rather than in it |
| `gonetmon_export_dropped_samples_total{reason="rejected"}` | a batch the endpoint refused for a reason not predicted. Above zero means readings that could have landed were lost with one that could not |

Every child publishes 0 from the first scrape, so absence means the build does
not have the counter — not that nothing has happened.

[69]: https://github.com/jeremyhahn/go-netmon/issues/69
[138]: https://github.com/jeremyhahn/go-netmon/issues/138
