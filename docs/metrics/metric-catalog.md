# The Metric Catalog

How a consumer learns what a series means without parsing a MIB.

## Why it exists

The dashboard used to resolve MIB modules itself, which coupled the UI to the
MIB store, the parser and a search path — and broke when any of them moved.
The collector already parses every MIB it polls, so the metadata a renderer
needs should travel with the metrics rather than be looked up again.

It cannot travel as Prometheus metric metadata. **Remote write v1 cannot carry
it**: `prompb.WriteRequest` has a `Metadata` field, but Prometheus's v1 receive
path never reads it — only `appendV2` calls `UpdateMetadata`, and only when the
receiver runs the experimental `metadata-wal-records` feature. monitor-01 runs
Prometheus 3.14.0 with `--web.enable-remote-write-receiver` and no experimental
features, so `/api/v1/metadata` for an SNMP series returns `{}`.

So it travels as ordinary series.

## The two families

```
snmp_metric_catalog{
  metric="snmp_entity_sensor_dbm",      # published name — the primary key
  module="CISCO-ENTITY-SENSOR-MIB",     # provenance, empty for derived metrics
  object="entSensorValue",
  oid="1.3.6.1.4.1.9.9.91.1.1.1.1.4",
  unit="dBm",
  kind="gauge",
  description="...",
  identity_label="entphysicalname",     # a label on the series itself
  identity_info_metric="snmp_ifname_info",  # ...or an info metric to join
  index_labels="entphysicalindex"
} 1

snmp_metric_enum{metric="snmp_ifOperStatus", value="1", label="up"} 1
```

Both are constant-1 info series: everything they carry is in their labels.

## One object, several published names

A MIB object does not map to one metric. The exporter publishes:

| object | published as |
| --- | --- |
| a numeric column or scalar | `snmp_<Name>` — case preserved |
| a **text** object | `snmp_<name>_info` — lowercased, value in a `value` label |
| a **status column** with a numeric value | **both**: `snmp_ifOperStatus` *and* `snmp_ifoperstatus_info` |

A text value cannot be a sample, so `snmp_ifName` is a name nothing ever
writes. The catalog holds one row per *published name*, so a status column has
two rows: the number and the label it renders to. The info row is always
`kind=string`, and carries no unit or enumeration — those belong to the number,
and a unit on a series whose value is always 1 describes nothing.

Getting this wrong left every `_info` family — every text scalar and every
status column — with no catalog row at all, while entries sat under names
nothing publishes. It looked complete.

## The primary key is the published name, not the MIB object

This is load-bearing. Entity sensor readings are published as
`snmp_entity_sensor_dbm` — named for the unit they were scaled into, because
one metric holding dBm and celsius and volts cannot be aggregated. **No MIB
object is called `entity_sensor_dbm`**, so a catalog keyed on object name would
describe every interface counter and none of the optical power.

Derived metrics carry no `module` or `object`. That is honest rather than
incomplete: two different MIB objects on two different tables
(`entPhySensorValue` and `entSensorValue`) produce them, and naming either would
be wrong on half the devices.

## `kind` is what a renderer keys on; `unit` is decoration

**Only 1,821 of 9,638 pollable objects declare UNITS** — measured across 198
modules. A renderer keying an axis on `unit` is blank four times in five. An
empty unit renders a unitless axis and never suppresses a panel; there is a
test for that invariant.

`kind` is derivable for every object whose type resolved:

| kind | rule |
| --- | --- |
| `counter` | `Counter32`, `Counter64`, `TimeTicks` |
| `enum` | the object has enumeration values, whatever its integer type |
| `string` | text and address types |
| `gauge` | everything else numeric |
| `unknown` | **no type resolved at all** |

`unknown` is a diagnosis, not a default. **730 of 9,638 objects (7.6%)** have no
resolved type, and they lose their units, display hint and enumeration in the
same stroke: `parser.go` only fills those when gosmi resolved a type node, and
it resolves none when a module the MIB IMPORTS from is absent. `ATM-MIB` needs
`ATM-TC-MIB`; without it `atmVplOperStatus` — an enumeration — arrives with
nothing. The module still reports a successful parse.
`gonetmon_catalog_unknown_kind` makes that countable instead of invisible.

## Identity: on the series, or one join away

A renderer needs a human-readable name for each line of a panel. Where it lives
differs, so the catalog says which:

- `identity_label` — the name is a label on the series itself.
  `snmp_entity_sensor_dbm` carries `entphysicalname`.
- `identity_info_metric` — the series carries only its index, and the name is
  in a separate info metric to be joined on it. `snmp_ifInOctets` has `ifindex`
  and no `ifname`; the name is on `snmp_ifname_info`.

Which applies is not a preference — it is whether the name is on the series.

Where a table has several candidate columns (`ifName`, `ifDescr`, `ifAlias`) the
MIB does not declare which names the row, so the choice is a documented
presentation convention: a column ending in `Name`, then `Descr`, then `Alias`,
otherwise the first in sorted order. Deterministic, so every collector given the
same MIBs picks the same one.

## Scale: global, and it must stay that way

**The catalog carries no `device` label.** Its size is the number of distinct
metric names the loaded MIBs can describe — measured at **20,390 series**
across 198 modules, including 11,188 enumeration rows — and it is identical at
five devices and at a hundred thousand. A device dimension would make the
description of a metric cost more than the metric.

## Scope: the corpus, not what this process happened to publish

The catalog describes **every family the loaded MIBs can describe**, plus the
collector's own derived families — not merely the families this collector has
published.

That is not the obvious choice, and the obvious one is wrong. Scoping emission
to the local published set describes reality more tightly and is 18× smaller,
but the published set is *process-local* while the emitter is *fleet-wide*:
exactly one collector is designated, so a designated shard A polling IF-MIB
devices never observes a shard B publishing some other family, and the fleet's
catalog carries no row for it. B's devices then cannot be rendered at all, and
nothing reports a fault — A is publishing happily, and the gap looks like
metrics nobody described. The MIB corpus, by contrast, is identical on every
shard, so cataloguing it gives one emitter complete knowledge with no
coordination protocol and no second writer to collide with.

The cost is rows for families no device answers. That is cheap here: a renderer
must ask Prometheus which series a given device actually has regardless, and
that query filters the surplus. **Describing an absent metric costs a row;
omitting a present one costs a blank dashboard.**

The published set is still tracked, but only as the diagnostic behind
`gonetmon_catalog_undescribed_metrics` — what did this collector publish that
nothing can describe? Raw-OID names are the expected residue.

Two consequences fall out of that, and both are tested:

**It is written once, not once per device.** `internal/poller` runs one worker
per device, so anything emitted from the poll path is emitted per device per
cycle. The catalog is emitted by a separate component on its own timer.

**Exactly one collector may write it.** Every collector would write the *same*
series, and the first write to land sets a sample's timestamp — a second
arriving behind it is rejected out of order, because `out_of_order_time_window`
is 0 by default. That needs more than one collector to appear at all, so it
would work in development and fail intermittently in production.

`prometheus.emit_metric_catalog` designates the emitter, and it defaults to
**false**. Designation has to be a decision somebody made: a default of true is
fail-open, and every shard whose configuration predates the field would become
an emitter the moment it was upgraded. Because the catalog shares a buffer with
device telemetry, a rejected catalog write can delay or reject device samples
too — so the failure is not a blank dashboard, it is dropped measurements.

Failing closed costs a fresh install its catalog until somebody sets it, and
that omission is loud: no catalog series exist at all and
`MetricCatalogNotBeingPublished` fires within fifteen minutes.

Designation is configuration rather than an election: no coordination protocol,
no leader lease, and no reliance on how the receiver treats duplicate samples.

## Cadence

`CatalogInterval` is 2 minutes. The catalog is static between MIB loads, so this
is not about freshness — it is about staying inside Prometheus's 5-minute
staleness window, with room for one missed emission. The cost is roughly 2 MB of
labels before compression every two minutes, forever, regardless of fleet size
or poll interval.

Full DESCRIPTION text is carried. It is 2,014 KiB across 9,638 objects (mean
214 B, max 7,942 B), stored once per series in the TSDB index and
symbol-deduplicated, so storage is not the constraint and the recurring cost is
fixed.

## The read contract: lookback, and newest wins

Two rules, and they are needed together because each creates the problem the
other solves.

**1. Query with a lookback, not at an instant:**

    last_over_time(snmp_metric_catalog[1h])

Every panel depends on the catalog, so its failure mode is unusual: the metrics
keep arriving and look perfectly healthy while every description disappears at
once, fleet-wide, from one emitter's bad minute. A point query makes catalog
freshness load-bearing for the whole UI. A lookback makes a missed emission
invisible, and turns a total outage into a slow degradation that the alerts
below catch first.

`deploy/rules/catalog.yml` alerts on write failure, on nothing being published
at all, and on published metrics the catalog cannot describe. Note that
`deploy/prometheus.yml` ships with no Alertmanager configured, so these
evaluate and appear as `ALERTS` series but do not page anyone until one is
wired up.

**2. Resolve duplicates per `metric=` by newest sample.**

Module, description, unit and identity are all labels, so a MIB reload that
changes any of them produces a *different series*. The emitter stops writing
the superseded one immediately, but the lookback above keeps it queryable for
as long as the window — so within that window a consumer can see two rows for
one `metric=` with conflicting contracts.

The current row is always the one with the newest sample, because only the
current row is still being written. In PromQL, take the maximum timestamp per
metric:

    last_over_time(snmp_metric_catalog[1h])
      and on (metric) (
        timestamp(last_over_time(snmp_metric_catalog[1h]))
          == on (metric) group_left max by (metric) (
               timestamp(last_over_time(snmp_metric_catalog[1h])))
      )

**The tension is deliberate and worth naming:** a longer lookback buys
resilience against a missed emission and, by exactly the same mechanism,
extends the window in which a superseded row is still visible. Neither rule is
optional. There is no `generation` label on the rows because there does not
need to be one — and adding one would mint 20,390 fresh series on every
reload, which is a worse cure than the disease.

## The published set records delivery, not intent

This section is about the published set — the coverage diagnostic — **not**
about what the catalog emits. Emission scope is the MIB corpus; see *Scope*
above.

A metric enters the published set only once it has actually been **written to
Prometheus** — recorded after a successful send, not when the samples were
buffered. A batch that was dropped or whose write failed was never published,
and counting it would understate
`gonetmon_catalog_undescribed_metrics` against series that never arrived.
Failed batches are put back and retried, so a name enters the set on the flush
that lands.

The catalog does not describe **itself**: its own two families are excluded
from the published set. They flow through the same export path as everything
else, so without that they entered the set on the first tick, could never be
described, and pinned `gonetmon_catalog_undescribed_metrics` at two forever —
corrupting the one number that says how good the catalog's coverage is.

Names are **evicted after 24 hours** without being published, so the set is what
the fleet publishes now rather than everything it ever published. The window is
far longer than any plausible poll interval on purpose: evicting sooner would
drop a metric from a device polled hourly. Eviction is self-healing — a live
metric returns on its next successful export — so the cost of the window being
slightly wrong is a description missing for one poll, not data lost. Cataloging every object in every loaded module would describe 9,202 metrics on
a fleet publishing a few hundred, and promise panels for objects no device
answers. A row appears when the data does — the same semantics the dashboard
already has.

## Delivery: the catalog does not share a batch with telemetry

`PrometheusExporter.SendCatalog` bypasses the sample buffer entirely and sends
the catalog in chunks of `batch_size`, synchronously, reporting whether
Prometheus took it.

That is a correctness property, not an optimization. Sharing one buffer meant a
rejected catalog write put device samples back with it — delaying them and,
once the retry backlog hit its cap, discarding them. **A description must never
be able to cost a reading.** It also meant one emission, being larger than any
sane `batch_size`, forced an immediate flush of whatever telemetry happened to
be queued and sent the whole lot as a single request.

A failed chunk stops the rest: a catalog describing an arbitrary subset of
metrics is worse than one that is a refresh behind, because the missing rows
look like metrics that have no description rather than like a failed write.

Chunking here is by sample count only. Bounding a request by its *encoded size*
is a property every send should have rather than just this one, and is tracked
separately.

## Cost, measured

Recorded so it is not re-litigated from first principles:

| | |
| --- | --- |
| `record` over a 15,000-sample batch | 247 µs, 9 allocations |
| the same under `RunParallel` | 26 µs/op |
| rendering 2,000 metrics | 668 µs, 870 KB, 6,060 allocations |

Recording is **faster per operation in parallel**, because the O(samples) scan
runs outside the lock and only the deduplicated names are written under it —
so the process-wide mutex is not a contention point at fleet scale.

Rendering projects to roughly 6.8 ms and 8.9 MB per emission at 20,390 series
every two minutes: a 0.006% duty cycle. The catalog is identical between MIB
loads, so caching the rendered label sets would avoid that — and it is
deliberately **not** done. It would add an invalidation path through a
component two products depend on, and 8.9 MB per two minutes does not justify
one. Complexity without a demonstrated need is its own kind of debt.

## Self-instrumentation

| Metric | Meaning |
| --- | --- |
| `gonetmon_catalog_series` | series in the catalog at its last emission |
| `gonetmon_catalog_published_metrics` | distinct metric names published |
| `gonetmon_catalog_undescribed_metrics` | published metrics the catalog cannot describe — no loaded MIB names them, typically raw-OID metric names |
| `gonetmon_catalog_unknown_kind` | catalogued metrics whose MIB module parsed with unresolved imports |
| `gonetmon_catalog_name_collisions` | objects discarded because an earlier object claimed their metric name |
| `gonetmon_catalog_emissions_total` | emissions by outcome — success means the catalog **reached** Prometheus, not that it was queued |
| `gonetmon_catalog_last_success_timestamp_seconds` | when the catalog last landed; alert as its age approaches the lookback consumers query with |

**436 of 9,638 objects collide** on published name across 198 modules. First
definition wins, matching `objectNameIndex` — if the two disagreed, the catalog
would publish one module's DESCRIPTION for a series named from another's.
