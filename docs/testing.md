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

## Say what a number means

An assertion tolerance is a claim. `domTolerance` in the optical DOM tests is
an epsilon on deterministic arithmetic, not a measurement tolerance, and it is
documented as such — because a tolerance loose enough to absorb real optical
drift would also absorb a factor-of-ten precision error on a near-zero reading.
