# Reading the export counters

The poller collects samples and writes them to Prometheus by remote write. These
counters say whether that is working, and when it is not, which of several
different faults it is. They exist because a single "samples dropped" number
could not tell a clock problem from a backpressure problem from an endpoint
refusing what it will always refuse — and go-netmon once discarded roughly a
million samples an hour for fifteen hours while every signal it published said
"buffered, failing to flush", which was true and was a different problem
([#69](../../issues/69)).

## Is anything being lost

```
gonetmon_export_dropped_samples_total{reason="..."}
```

Every reason below publishes `0` from the first scrape, before anything has
been dropped. That matters for alerting: a Prometheus `rate()` over a series
that does not exist returns an **empty** result rather than zero, and an empty
result satisfies no comparison — so an alert on a lazily created counter is
silent on exactly the process that has not failed yet, and stays silent until
the first drop creates the series it was meant to warn about.

It also means **an absent series says something**: the build does not have that
counter. `scripts/measure-export-health.sh` relies on that to catch a deploy
that copied a binary without restarting it.

Non-zero means samples were accepted from a device and never delivered. The
reason says which fault:

| reason | what happened | what to do |
|---|---|---|
| `superseded` | Another reading already owns that point of the series — either one the endpoint accepted earlier, or another sample in the same batch claiming the same instant with a different value. The sample is dropped **on its own**; the rest of its batch is still sent. | Usually the poller producing one series' readings out of order — see `repaired_order_total` below. One or two per device edit is expected. A steady rate is not. |
| `aged` | The sample waited longer than the age horizon (six flush intervals, at least a minute) and is now too old to be accepted. | Remote write has been failing or slow. Check `gonetmon_export_consecutive_failures` and the endpoint. **A sudden fleet-wide spike with a healthy endpoint means the poller's clock moved** — every sample is stamped from the poller's own clock, so an NTP step drops a window of them at once. |
| `depth` | The queue exceeded its sample bound while every sample in it was still fresh. | The endpoint cannot keep up with collection. Not a clock or ordering problem. |
| `rejected` | The endpoint refused a request for a reason that cannot be retried — a 4xx other than 429. | Look at the log line; it carries the endpoint's own words. This should be rare now that unacceptable samples are removed before sending. |
| `shutdown` | The process stopped with samples still queued. | Expected if the endpoint was down at shutdown. The count is also returned from `Close`, and appears in the log as `DATA LOSS: N sample(s) never reached Prometheus`. |

### Samples are not flushes

```
gonetmon_exports_total{status="success"|"error"}   # FLUSHES
gonetmon_export_dropped_samples_total{reason=...}  # SAMPLES
```

These two are in different units and must never be divided by one another.
`gonetmon_exports_total` counts remote-write requests and their outcome; the
drop counter counts individual readings. A request that was never made — every
reading in it given up on before the wire — is neither a success nor an error,
so it appears in neither.

`gonetmon_exports_total` used to carry a third child, `status="dropped"`, which
counted samples among its flush-counting siblings. Anything summed or divided
across that metric was therefore meaningless, and #69's own measurement had to
carry a paragraph saying so. It is gone; `sum(gonetmon_export_dropped_samples_
total)` is the number it held.

## Is anything out of order

Two counters, because they are two different facts:

```
gonetmon_export_out_of_order_total     # the endpoint WILL refuse these
gonetmon_export_repaired_order_total   # sorted before sending; the write succeeds
```

`repaired_order_total` rising is a signal about the **poller**, not the
endpoint: some series' readings arrived in an order other than the one they were
taken in. The commonest cause is two overlapping polls of one device, which
happens when a device is reconfigured while a poll is in flight. The write
succeeds — the encoder sorts each series first — so this is not an error and is
not logged as one.

`out_of_order_total` rising means samples remote write would refuse: older than
what Prometheus already holds for their series, or a different value at an
instant that series already holds. Those are dropped as `superseded` rather than
sent, because the endpoint rejects the whole request on the first one and a
batch is a thousand samples.

## Is delivery keeping up

```
gonetmon_export_buffered_samples        # accepted and not yet delivered
gonetmon_export_consecutive_failures    # flushes failed in a row
gonetmon_export_stalled                 # 1 while nothing has flushed for a long time
```

`buffered_samples` counts everything undelivered: still accumulating towards a
batch, queued for the sender, and the batch on the wire. It reads zero when
delivery is healthy, and a persistent non-zero value with
`consecutive_failures` rising is remote write failing rather than collection
stopping.

## Measuring a device edit

`scripts/measure-export-health.sh` runs the check below rather than printing
numbers to be interpreted:

```
scripts/measure-export-health.sh baseline    # before the change
scripts/measure-export-health.sh edit        # after: drives the edit, judges it
```

It prints each counter's movement against the expectation stated here and marks
it MATCH or UNEXPECTED, so the run can come out wrong. It changes nothing on the
target except the MIB assignment it creates and deletes again.

**Give it the sha256 of what you deployed**, or it will say the build was not
verified:

```
EXPECT_SHA256=$(sha256sum bin/go-netmon | awk '{print $1}') \
  scripts/measure-export-health.sh edit
```

That is checked against the image the service is **executing**, read through
`/proc/<pid>/exe` rather than from the path on disk — a deploy that copied the
binary but failed to restart leaves the right file in place and the old code
running, and only the executing image tells those apart.

The `edit` arm also refuses to evaluate anything until every expected series is
present. Both preflights exist for the same reason: against a build without the
per-reason counters, *every* expectation passes — "must not move" is satisfied
by a series that is not there — so a harness that read absence as zero would
certify a node that was never updated. Absent is a distinct state throughout,
and after a deploy it is fatal rather than passing.

`make test-export-health` tests its parsing against captured exposition from a
build with the counters and one without, and touches no appliance.

## What a device edit looks like

Assigning or unassigning a MIB replaces the device's poll worker. A poll already
in flight finishes against the old configuration and its results are discarded,
so expect in the seconds afterwards:

- `repaired_order_total` may rise by a few
- `superseded` may rise by a few
- `out_of_order_total` should stay flat or match `superseded`
- `aged`, `depth` and `rejected` should not move at all

Anything else is worth reading the log for.
