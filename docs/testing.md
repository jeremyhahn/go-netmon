# What a test proves

Repository-wide testing notes. Conventions live in the company repo
(`software/conventions/testing.md`); this is the local, hard-won part.

## Mutate before you believe a test

**A test whose outcome depends on a `select`, on a goroutine finishing, or on
an interleaving proves nothing until a mutation says otherwise.**

That rule cost three tests in one week, all written by someone who thought they
were writing a guard:

- A concurrent check on a Prometheus gauge set *outside* its mutex. It passed
  just as happily against the broken version, because after `WaitGroup.Wait()`
  every writer has finished and whichever `Set` ran last wins — there is no
  deterministic trigger for the interleaving it claimed to catch. **Deleted**,
  not fixed: a test that cannot fail is worse than no test, because it reports
  coverage that does not exist. The lock placement is defended by construction,
  and what replaced it checks the part that can be checked.
- A cancellation test that waited through a `Stop()` which also closed the stop
  channel. With both `select` branches ready, Go picks either, so coverage of
  the cancellation path moved between runs. Fixed by waiting on the component
  directly.
- A cache test asserting an entry merely *existed*, where the real defect was
  that its index labels were silently empty. Fixed by asserting the value.

The generalisable part is not the three fixes. It is that **each was caught by
mutating the implementation, never by re-reading the test.** Reading a test
tells you what it says; mutating tells you what it enforces, and those differ
more often than is comfortable.

## How to mutate

Remove the behaviour the test claims to guard, run only that test, and require
it to fail. If it passes, the test is decoration.

Do it for each behaviour independently. A mutation that breaks eleven unrelated
tests has proved nothing about the one you care about — that happened here too,
and the second, narrower mutation is what showed which test was real.

Mutate *faithfully*. An approximation of the old behaviour can pass a test that
the real old behaviour would have failed: a mutation that carried missing map
*keys* forward tripped one test, while the original defect carried missing
*fields* forward and tripped four. The weak mutation would have certified
coverage that was not there.

## Fixture correctness is not optional

Three independent faults in one fixture each guaranteed the same wrong result,
and any one of them alone would have hidden a broken feature indefinitely:

- a parser pointed at an empty directory, so no module ever resolved;
- a Prometheus stub returning the label-values response shape where the code
  parsed the query shape, so the decode failed and *no* metric names were ever
  available;
- a metric named in lowercase that the exporter never emits.

The feature returned nothing in production for months with every test green.
When a test asserts a *count*, make the count non-zero and assert on the
content — `pollable > 0` would have caught all three.

## A test that predetermines its answer

The most persistent family of unfailable test here. Six in one day, across this
repo and go-sysmon. They differ in mechanism and the mechanism is worth keeping
straight, because the fix differs too:

**The probe manufactures the answer.** `deviceUnreachable.WithLabelValues(name)`
returns the child and *creates* it, initialized to zero, if it is absent. A test
asserting "the series is published and reads 0" got exactly that from a series
that did not exist, and passed against the code it was written to catch. The fix
is to observe without touching: `prometheus.DefaultGatherer.Gather()` collects
from the registered collectors, so an absent child is absent rather than
conjured.

**The probe computes the wrong answer.** A three-number regex over
`getComputedStyle().color` parses `oklch(0.76 0.177 163.223)` as
`r=0.76 g=0.177 b=163.223`. Compositing a translucent layer over white instead
of its real ancestor stack reported 2.89:1 for something that is 4.57:1 — nearly
"fixing" a passing colour. The fix is to measure the way the thing is actually
produced: read the pixel back from a canvas, composite the whole stack.

**The fixture never reaches the path.** Three of the six:

- a test that *cleared* the `localStorage` it needed to seed, so the restored
  selection it was asserting about could not have been replayed either way;
- a mocked import plan with no `changed` count, so the browser's import handler
  answered "nothing to import" and returned before doing anything;
- a silent SNMP device, which produces no results, in a test asserting that
  nothing was *exported*.

Nothing here manufactures a value — the exercised path is simply empty, and an
empty path satisfies a negative assertion for free.

### Finding them

The decisive check is a faithful mutation: break the production behaviour the
test claims to pin and watch it fail. If it still passes, ask what supplied the
answer — the probe, or the emptiness of the path.

Mutation is the best discriminator but not the only one, and the cheaper habits
catch most of these before they are written:

- assert the fixture reached the path — that the apply call happened, that the
  saved state exists, that at least one element was measured;
- assert on something the probe cannot create, and prefer a negative assertion
  that is paired with a positive one: "the departed device exported nothing" is
  worth little without "the current device still does".

## A test that documents the bug as intended

The families above are tests that cannot fail. This one is worse: it fails
correctly, on exactly the behaviour it names, and the behaviour is a defect.

`TestTwoReadingsForOneInstantAreCollapsed` asserted that when a batch carried
two readings for one series at one millisecond, the encoder kept the second and
dropped the first. That is what the code did. It is also a sample discarded
with no counter moved and the flush returning nil — the silent loss the whole
package exists to end — and the test held it in place by name, with a comment
explaining why it was right.

It cost nothing to write and it would have cost the next person a lot. Whoever
fixed `orderedSamples` properly would have watched a well-named test go red and
concluded they had broken something. A passing test is normally evidence; this
one was a lock on the defect, and it looked like coverage the entire time.

The check is not mechanical, because mutation does not help — mutate the code
and the test fails, which is precisely the problem. Ask instead:

- **does this assertion describe an outcome anyone would want?** Not "does the
  code do this", which is how it got written, but "if a reviewer read only this
  assertion, would they call it correct?" "Kept 150 and dropped 100" does not
  survive that question.
- **name the outcome, not the mechanism.** A test called "two readings for one
  instant are collapsed" is a test about an implementation detail, and an
  implementation detail cannot be wrong on its own terms. A test called "two
  readings for one instant are one delivery and one discard" states a claim that
  can be disagreed with, which is what makes it worth asserting.

A test is a claim about what SHOULD happen. Writing down what does happen,
carefully, is a different activity, and the more careful it looks the longer it
survives.

## A bound test whose threshold equals the unbounded behaviour

`TestShutdownIsBounded` used a handler that answered in 200ms and asserted that
`Close` returned within 30 seconds. The unbounded behaviour it existed to catch
is a 30-second stall — one request running to its own `sendTimeout`. So the
assertion was satisfied by the defect. It passed for weeks while the bound did
not work.

A bound is only tested by a case that would exceed it. Here that means a request
that never returns on its own -- a handler blocking until its context is
cancelled -- and a ceiling well below the stall: about 10 seconds against 30.
Both halves matter. With the 200ms handler no threshold would have caught it,
because nothing was ever slow.

The check to run against any test with a number in it: **what is the failing
behaviour worth, and is my threshold below it?** If the defect produces 30
seconds and the assertion allows 30 seconds, the test is decoration.

Two related traps came out of writing that test, both of which cost a run:

- **Fifty queued batches at 30 seconds each** is not a better test than one, it
  is the same test taking half an hour.
- **A server does not always notice that a client has gone.** `net/http` cancels
  a request context when the connection closes, but a handler that is merely not
  answering may never be told, so `httptest.Server.Close()` waited forever on it
  -- the teardown hung even though the code under test worked. Give the handler
  a release channel and close it before the server, or the test cannot
  distinguish "the fix failed" from "the fixture will not let go".

## A test whose measurement is shared with the rest of the suite

The sibling of the family above, and the first one seen here that fails for the
wrong reason rather than passing for one.

`outOfOrderTotal`, `exportsTotal` and `droppedSamples` are process-global
counters — they have to be, because `/metrics` publishes them. Five tests read
them as a before/after delta, which is only meaningful if nothing else is
writing. Something else was: an exporter from an earlier test keeps a sender
goroutine alive, and its increment lands inside a later test's window. The later
test then fails on an assertion about something it never did.

The failure message cannot say so. `counted 1 out-of-order samples, want 0`
reads exactly like a real defect — it was reported as one, twice, and a
watermark rewrite was nearly started on the strength of it.

Two habits, in order of preference:

- **Give the code under test its own counters.** The exporter now holds the set
  it increments; production uses the shared one, a test installs its own. Then
  the assertion is absolute rather than a delta, and it is about that exporter
  and nothing else.
- **A delta is a workaround, not a fix.** A comment saying "this counter is
  shared, so measure the delta" — which this package carried — documents the
  hazard without removing it.

The tell is a test that passes alone and fails in the suite, or vice versa. That
is never about the code under test.

## Three green runs of a flaky test is not evidence

Measured on `TestAFailedWriteDoesNotCountAsWritten`, which CI failed on two
consecutive heads while three local `go test ./... -race -shuffle=on` runs
passed:

| how it was run | failure rate |
|---|---|
| the test alone, `-count=1` | **12 of 20** |
| the test alone, `-count=1 -race` | 10 of 20 |
| the whole package, `-race -shuffle=on` | **1 of 5** |

Three passes at a 20% failure rate is a coin flip — about 51%. The local runs
were not measuring something different from CI; they were the same measurement,
taken three times, and it came up heads.

Two things worth keeping from that:

- **Isolation is more sensitive than the suite here, not less.** Running the one
  test alone tripled the failure rate. The instinct to reproduce a suite failure
  by running the suite is backwards for timing-dependent tests: run the single
  test in a loop with `-count=1` to defeat the cache.
- **`-race` did not mask it** and the test cache was not involved (a repeated
  `-race -shuffle=on` run reports no `(cached)` and takes the full time). Both
  were plausible explanations and both were wrong; the answer came from
  measuring each one rather than picking the likeliest.

If a test's outcome depends on timing at all, "it passed" needs a count beside
it. `-count=20` on the one test, in isolation, is the cheap version.

## A flaky test can be the only evidence you have of a race

`TestASampleTooOldToLandIsDroppedRatherThanRetriedForever` failed about 1 run
in 30 under load. Everything about it said timing: a background sender, an age
horizon, a barrier waited on with a deadline. The obvious response to a
1-in-30 timing-shaped failure is to widen the margin, and it would have worked
— the test would have gone green and stayed green.

It was a data-loss bug in production code. `Flush` enqueued its batch and its
barrier as two separate steps under two separate holds of the buffer lock. The
sender could take the batch in between, expire the aged sample, and find no
barrier behind it to charge — so the `Flush` responsible for that sample
returned **nil having lost it**. The intermittent failure was the exporter
reporting success while discarding, caught in the act.

**"Make the flake stop" and "fix the bug" are opposite actions that produce
identical green suites.** Widening the margin would have shipped the race and
permanently silenced the only thing reporting it.

So before touching a flaky test:

- **ask what the failure would mean if it were not a coincidence.** Here it
  meant a barrier that should have been charged was not — which is a sentence
  about the exporter, not about the test.
- **fix it by construction rather than by widening the margin.** The remedy was
  `enqueueAll`, submitting batch and barrier under one hold so no interleaving
  exists. The test was not changed at all.
- **measure at power, before and after.** 1/30 is a rate, not a diagnosis, and
  0/30 afterwards proves nothing at a 1.7% event. The evidence was 2/120 with
  the cause restored against 0/120 with it fixed.

## A race test that cannot reach the race

The retirement-gate test in `internal/poller` was named after the defect it
existed to catch, asserted exactly the right invariant, and passed with the fix
in place. Mutating the fix away produced **0 detections in 2,000 iterations**.

In the broken gate the window between checking `retiring[name]` and installing
the worker is *one statement wide*. One registration racing one removal
essentially never lands in it, so the test would have been green forever
against both the fixed and the broken code, while carrying the name of the race
it could not see. Nothing about reading it would have suggested that.

**Sixteen registrations against one removal reach the window about once in 45
iterations.** At 600 iterations that is 10 detections in 10 mutated runs, worst
case at iteration 149, against 0 in 4,800 with the gate fixed — and the whole
test costs 1.3 seconds.

The reason raising contention is a fix rather than a way of making a flaky test
flakier is worth stating on its own:

> **Reaching the violating state is probabilistic. Observing it must not be.**

The removal is parked at its commit barrier, so a name it has claimed stays
claimed and a worker listed under a claimed name stays listed. Both halves of
the violating state persist until the test releases them, which means a single
check sees it — there is no sampling window to get wrong and no sleep to tune.
Sixteen adders raise the probability of *reaching* the state without touching
the reliability of *seeing* it. A change that raised both would be a coin flip
dressed as coverage.

Two habits follow:

- **Mutate before you believe a concurrency test, and read the count.** An
  expected failure that does not arrive is the finding. Here it was the only
  instrument that worked: reading the test, reasoning about the interleaving and
  a second reviewer's eye all agreed it was a good test.
- **Freeze the state you assert on, then race for it.** Park the other party
  somewhere it must stop — a held lock, a barrier — so the state under test
  cannot dissolve while you look at it. Then add contention until the
  interleaving is reliably reached, and report the rate.

## A fixture too regular to tell the rules apart

The uniformity rule behind the dashboard's table asks, per column, whether the
rows disagree. The first implementation asked whether the **table** had more
than one row and applied that answer to every column — so a column that had
answered for a single row was judged uniform on its *neighbours'* data and left
out of the table as though its rows had agreed.

Five tests covered that rule. None of them could see it, and the reason is the
transferable part:

> **Every column in every fixture had the same number of rows. The fixture
> agreed with the bug.**

Not a test that cannot fail, not a test that fails for the wrong reason, not a
test whose threshold is looser than the defect. Each of these tests *could*
fail, on the behaviour it named. They were blind because the input was too
regular to distinguish the rule that was implemented from the rule that was
meant — three columns of three rows each cannot tell "per column" from "per
table", because the two agree on every such input.

The defence is ragged fixtures, and the general form is:

> **Make the fixture disagree with itself along every axis the rule reads.**

The rule here reads two things — a column's row count and its distinct-value
count — so the fixture must vary both independently. The test that found it has
one column with four rows that agree and another with a single row; under the
table-wide count the second is dropped, under the per-column count it is kept,
and no amount of symmetric data separates those.

A useful smell: if you can describe your fixture as "N things, each with M
values", it probably cannot distinguish per-thing logic from per-set logic. Give
one of them a different M.

### Worked example: the author sprang this trap within the hour

The section above was written on the afternoon of 2026-09-02. That same evening,
its author wrote this test for a websocket backoff defect:

```go
for i := 0; i < SteadyRoundsBeforeBackoff+1; i++ {
    h.streamMetrics(context.Background())     // warm up past the backoff
}
joined := subscriber()
h.Subscribe(joined, "q")
h.streamMetrics(context.Background())         // and join
// assert the new subscriber got a value
```

It passed. The defect was present.

A backed-off query is asked on **even rounds only**, so whether a new subscriber
is served depends on the parity of the round it joins on. Seven warm-up rounds
put the join on round eight, which is even, which is a round the query is asked
on anyway. **The test measured the arithmetic of its own warm-up, not the rule.**
Measured afterwards, at matching effort: the even arm passes with the defect
present, and only the odd arm catches it.

Round parity is an axis the rule reads. The section above says to vary every
axis the rule reads. Knowing that did not help, and the fix is the same one this
file keeps arriving at: the test now runs **both** parities in a loop, so the
question cannot be decided by how many warm-up rounds someone happened to write.

Two things worth taking from it:

- **Knowing a trap does not protect you from it.** That is the argument for
  construction over documentation, applied to this document. A section naming a
  hazard did not stop its own author walking into the hazard the same day; what
  stops it is a fixture that varies the axis, or a loop that covers both cases.
- **"Warm up N times, then act" is a smell of its own.** Any fixed N lands on
  one phase of whatever cycle the code under test has. If the code has a cycle
  — a parity, a modulus, a batch boundary, a retry counter — iterate over the
  phases instead of picking one.

### The other way a fixture fails: it cannot say the thing

The parity case above is a fixture too **regular** to discriminate. This is a
fixture not **expressive** enough to state the hazard, and it is worth telling
apart because the remedy is different: raggedness does not help if the failing
case cannot be written down at all.

The dashboard's column rule decides whether a column's rows disagree. It read
each row's SAMPLE. Prometheus info metrics carry their reading in a label and
their sample is `1` on every row:

```
snmp_ifalias_info{ifindex="1",value="uplink to core"} 1
snmp_ifalias_info{ifindex="2",value="server rack 3"} 1
```

So the rule found one distinct value across a hundred differently named
interfaces, called the column uniform, and dropped it — the column whose entire
purpose is naming rows, removed from a table whose entire purpose is naming
rows.

Seven tests covered that rule. None could fail, because the test stub could set
**samples only**: `Values map[string][]float64`. "Two info rows that differ" was
not a sentence the fixture could utter. The single input class the rule got
wrong was the single input class the tests could not construct.

> **A fixture that cannot express the failure cannot be evidence against it.**

The fix is to the fixture's vocabulary, not to any test: the stub grew
`Labels map[string][]string`, and only then could a test be written that fails.

How to notice it before a reviewer does: look at what your fixture's *type* can
represent, and compare that against the shapes the production code accepts. A
stub taking `[]float64` cannot represent a metric whose reading is not a float,
and the code under test accepted exactly such metrics. That gap is visible
without knowing the defect — it is a question about the fixture's type, not
about the bug.


Two related distinctions worth keeping beside this, because they are the same
shape and this repository has now met all three in one day:

- **"nothing to compare" is not "everything agrees."** Only the second justifies
  leaving a column out. Reading the first as the second is what the bug did.
- **absent is not zero.** A counter that does not exist yet is not a counter
  reading nothing; see the export-counter work and `#73`.

## An assertion that reaches its evidence through the code under test

A test can fail to fail for the *same reason* the code is wrong. If the
observation and the behaviour share a dependency, a defect in the shared thing
is invisible from both sides at once.

Two instances, a day apart in the same repository:

**A shared constant.** `TestEveryLabeledExportCounterPublishesZeroBeforeAnythingHappens`
compared the children a helper created against `dropReasons` — the very list the
helper reads. Removing a reason moved both sides of the assertion together and
the test passed. Fixed by spelling the expected set out literally.

**A shared accessor**, which is harder to see because it looks like good
practice. The websocket sweep uses `steadyQueryRounds.queries()` to decide what
to drop, and the test asserted through `queries()` too. A `queries()` that
reported only one of the tracker's two maps would hide a leaked entry from the
sweep **and** from the assertion — measured: that version of the test passed
under exactly that mutation. Fixed by reading the maps directly, under the
tracker's own lock.

Reaching through the public accessor is normally the right instinct. It is the
wrong one here, and the rule is short:

> **An assertion must not reach its evidence through the machinery it is
> testing.**

Get the observation from somewhere the defect cannot reach: the underlying
field, a second independent path, or a value written out by hand. A test in the
same package can read unexported state, and should when the alternative is
asking the suspect for its own alibi.

The tell is that a mutation you expect to fail does not. That is a finding about
the *claim*, not evidence that the test is merely weak — see "Mutate before you
believe a test", which is the instrument that caught both of these.

## Two suites that verify each other's assumptions

The row table shipped **inert**. The server emitted no `tables` on the endpoint
the browser fetches, so the page drew nothing — while nine Go tests and nine
Playwright specs passed.

Neither suite was weak. They were pointed at each other:

- **Every Go test asked `/api/mibs/{id}/dashboard`**, the endpoint where the
  tables were built. The browser never calls it; it fetches
  `/api/devices/{id}/dashboard` once and splits the payload itself.
- **Every Playwright spec mocked `tables` onto the device endpoint** — the shape
  the frontend needs, and a shape the server did not produce at that scope.

So the mock asserted what the server *ought* to send, the Go tests asserted what
a *different* endpoint *did* send, and **nothing compared them**. Both ends were
correct against the other's assumptions, and the contract between them was never
exercised against the running product.

> **A mock is a claim about the server. Something has to check the claim.**

The cheap defences, in order of what they cost:

- **Ask the URL the product asks.** The test that would have caught this differs
  from nine that did not by one path string. Before mocking an endpoint, grep the
  client for the URL it actually requests.
- **Distinguish absent from empty.** The new test decodes into a raw map rather
  than the typed struct, because a missing key and an empty array are the same
  `nil` slice once typed — and "the field is absent" was the entire failure.
- **Render the page, once, against a real server.** A payload check proves a
  payload. The owner was looking at a browser; nobody had opened one.

## Fixing a bug in a mechanism is evidence about how the mechanism works

The restriction that caused the above — tables built only at module scope — rested
on the belief that the browser fetched a dashboard per module tab.

**That belief had already been disproved by the author's own earlier fix.** A bug
where `tables` was dropped from the browser's per-module rebuild had been found
and repaired *days* before. That rebuild exists precisely because the browser
fetches **one device payload and splits it itself** — which is the fact that makes
the module-scope restriction wrong.

The evidence was in hand, examined closely enough to fix code inside it, and
filed as a different bug.

> **When you fix something, ask what its existence tells you. A mechanism you had
> to repair is a mechanism you now have evidence about — update the beliefs that
> mention it, not just the lines you changed.**

The tell is a comment justifying a decision by describing behaviour elsewhere in
the system. That comment is a claim about code you are not editing, it ages
silently, and nothing fails when it stops being true.

## Say what a number means

An assertion tolerance is a claim. `domTolerance` in the optical DOM tests is
an epsilon on deterministic arithmetic, not a measurement tolerance, and it is
documented as such — because a tolerance loose enough to absorb real optical
drift would also absorb a factor-of-ten precision error on a near-zero reading.
