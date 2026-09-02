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

## Say what a number means

An assertion tolerance is a claim. `domTolerance` in the optical DOM tests is
an epsilon on deterministic arithmetic, not a measurement tolerance, and it is
documented as such — because a tolerance loose enough to absorb real optical
drift would also absorb a factor-of-ten precision error on a near-zero reading.
