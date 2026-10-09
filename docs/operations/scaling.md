# Scaling

> **Status: REFERENCE** · Scope: go-netmon · Last verified: 2026-10-09

What grows with the size of the fleet, what grows with the number of people
watching it, and which of those are bounded today.

The two axes are independent and both matter: a datacenter is tens of thousands
of devices, and a NOC is tens of operators with dashboards open all day. A cost
that is linear in *devices × sessions* is the shape to hunt for.

## Fleet overview

`GET /api/devices/status` answers for every configured device.

It makes **one** call to the poller regardless of fleet size. The poller builds
its device listing from in-memory worker state, so a single response already
carries everything it polls.

It previously asked the poller once per device. That is invisible on a rack and
compounding in a datacenter, because every open overview repeats the whole
fan-out on each refresh (every 30 seconds by default; the Settings view can set
it between 1 and 60) — the load was devices × sessions.

The old shape also hurt correctness, not only speed: with a throttled poller the
per-device fan-out exhausted the collection budget while most devices were still
queued, so most rows came back with no status and the overview displayed a
fleet-wide outage that was not happening.

`TestFleetStatusHoldsUpAcrossManySessions` drives many sessions against a
throttled poller and a large fleet, and `BenchmarkFleetStatus` holds the poller
call count at one regardless of device count; both are in
`internal/webui/api/fleet_status_scale_test.go`. Run them to get numbers for
your hardware.

### One page, not the whole fleet

`GET /api/devices/status` takes `limit`, `offset`, `search` and `state`, and
returns the page alongside `total` (matching the filters), `configured` (the
whole fleet) and `summary` (every device counted by state).

The body itself was the remaining term that grew with device count: the whole
fleet serialized on every refresh of every open browser, for a table that shows
a few dozen rows. `BenchmarkFleetStatusPayload`
(`internal/webui/api/fleet_page_test.go`) compares the two sizes.

The summary is computed server-side and travels as numbers, because a chip
reading "12 unreachable" is a statement about the fleet and not about page one.
Searching, filtering, sorting and paging are all asked of the server; the
browser holds only what it is showing.

**Omitting `limit` returns the whole fleet**, unchanged, so the CLI and any
script keep working — a one-shot caller has no paging problem.

## Live metric streaming

Two rules govern what is on the wire: **only the selected device**, and **only
what changed**. `tests/e2e/tests/delta-streaming.spec.ts` measures frames and
bytes in a browser against a deployed stack.

### Only the selected device

The page subscribes to the device on screen and nothing else.

It used to also hold a "warm-up" subscription to four unqualified metric names —
`snmp_sysUpTime`, `snmp_cpmCPUTotal5sec`, `snmp_ciscoMemoryPoolUsed`,
`snmp_ciscoMemoryPoolFree`. An unqualified name matches **every series on every
device**, so each round pushed the whole fleet's uptime to every open browser
whatever it was displaying, on every round. Nothing in the page ever read it — the only consumer matched pre-rename
OID-style names that no longer exist. It and its dead consumer are gone.

The fleet overview needs no metric stream at all; it has `/api/devices/status`.

### Only what changed — but every tick still lands

A subscription is re-queried every round (5 seconds, `DefaultStreamInterval`),
but a device is polled at its own, much longer, poll interval, so most rounds read back the reading they read last time. Each result
is fingerprinted over its labels and values; a client is sent the **numbers**
only when its fingerprint differs from the last one that client received.

A steady query is still announced every round, by a `metrics_unchanged` frame
naming the queries whose readings the client already holds. The browser replays
the reading it kept through the same path a fresh one takes, so its charts
advance on every tick.

That frame is not optional. The dashboard is a tick-based live view, and a chart
that stops receiving points stops at its last change: a value steady for ten
minutes draws a line ending ten minutes ago, which reads as data having stopped
rather than as a value holding. Sending only changes was tried and had exactly
that effect. `delta-streaming.spec.ts` asserts that drawn charts keep gaining
points on idle ticks.

One heartbeat covers every steady query in a round, so the notification costs a
frame per client per tick regardless of how many subscriptions are steady.

Change detection is **per client**, not global, so opening a dashboard delivers
the current value immediately even if it has not moved for an hour and every
other session already has it. A frame that cannot be delivered — a slow or
disconnected browser — is un-recorded so the next round retries.

**The timestamp is deliberately not part of the fingerprint.** An instant query
is evaluated when it is asked and Prometheus stamps the result with that
evaluation time, not with the scrape the value came from, so an unchanged value
comes back with a different timestamp on each round. A fingerprint that includes
the timestamp therefore changes every round and saves nothing.

What this costs is chart density, and while a value is steady it costs nothing:
two points of equal height draw the same flat line however many identical points
sit between them. When a value moves, the line runs from the previous reading to
the new one rather than stepping — an honest picture of a change we only know to
within one poll interval.

### Round bounds

The hub queries Prometheus on a timer for every subscribed query. A device
dashboard subscribes one query per panel, so the number of subscribed
queries is set by how many people are watching how many distinct devices.

Two bounds keep a streaming round finite:

- **`MaxQueriesInFlight`** (32) caps how many Prometheus requests one round has
  open at once. A round used to open one request per subscribed query
  simultaneously — thousands, all landing on a single Prometheus at the same
  instant. The ceiling turns that into a queue: the same work, at a rate
  Prometheus can serve.
- **A round does not overlap itself.** A tick arriving while the previous round
  is still running steps aside. Without this, a Prometheus slower than the tick
  made each tick stack another full set of queries on the last, and the backlog
  never drained.

**Disconnects.** Removing a client visits only that client's own subscriptions.
It used to walk every subscription in the hub with the write lock held, so one
operator refreshing their browser paused streaming for everyone. On a hub
carrying many subscriptions, `BenchmarkClientDisconnect`
(`internal/webui/websocket/hub_scale_test.go`) measures the cost of one
disconnect.

**Subscription teardown.** Leaving a device releases its subscriptions. Keys are
`${deviceId}-${mibName}`, and the browser recovered the MIB name by splitting on
the first hyphen — but MIB names carry hyphens (`HOST-RESOURCES-MIB`, `IF-MIB`),
so the lookup missed and nothing was released: subscriptions accumulated as the
reader walked from device to device, and the later dashboards starved. The name
is now everything past the first hyphen, and `subscription-teardown.spec.ts`
checks that the count holds flat.

**Still on a fixed cadence:** the hub ticks every 5 seconds whatever a device's
poll interval is, so a device is *queried* many times per sample. Change detection keeps that off the wire and out of the browser, but the
Prometheus queries themselves still happen. Waste rather than a stability risk,
now that rounds are bounded and results are deduplicated.

## Dashboards in the browser

Chart instances are built lazily — on scroll into view, or when a sample
arrives, whichever comes first. A device dashboard can hold hundreds of cards
(one core router held 407) and building every plot up front created 271 live
ECharts instances, each with a canvas, resize listener and animation loop, for
the eight or so actually on screen.

The refresh tick patches rows in place rather than replacing markup, so an idle
dashboard is not torn down and rebuilt. (`refresh-stability.spec.ts`)

### Per-client ceiling

`MaxSubscriptionsPerClient` (2048) bounds what one session can ask the server to
do on its behalf, since every subscription is re-queried on every round. A
legitimate device dashboard is far below the ceiling. A refused subscription is
answered with an error frame rather than dropped silently: a page whose
subscriptions vanished quietly shows cards waiting forever for data nobody is
collecting, which reads as a broken device.

### Settled readings are asked about less often

The tick is 5 seconds and nothing here is polled faster than 30, so most rounds
ask Prometheus a question whose answer cannot have changed. After a query has
read the same thing for `SteadyRoundsBeforeBackoff` (6) consecutive rounds it is
re-queried at half rate, returning to every round the moment it moves.

The bound matters more than the saving: **a value that is changing is never
delayed.** The backoff applies only to readings that have demonstrably stopped,
and a backed-off query is still announced as steady every round, so its chart
advances on exactly the same tick as everything else
(`TestAMovingValueIsNeverDelayed`, `TestASettledValueIsAskedAboutLessOften`).

## What is not yet bounded

Nothing in the streaming or fleet paths is known to be unbounded. The API is
unauthenticated, which is a decision about deployment shape rather than scale;
see [security-posture.md](security-posture.md).
