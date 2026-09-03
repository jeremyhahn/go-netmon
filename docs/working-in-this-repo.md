# Traps in this repository

Three ways to destroy work or reviewability here, each met in one day
(2026-09-02) and each cheap to avoid once named.

## Do not point a formatter at the web UI's JavaScript

`internal/webui/static/js/app.js` is hand-formatted. There is no prettier
config, and running `prettier --write` on it rewrote **8,304 lines** against a
change of 120.

The cost is not cosmetic. A 120-line change inside a 15,000-line diff cannot be
reviewed, and the review gate on this repository has found a real defect in
nearly every round — a blocking data-loss path, a lifecycle race, a column rule
that dropped the column naming its own rows. Burying a change is the same as
turning the gate off, while looking like a tidy-up.

Caught on `git diff --stat` before pushing. Check the stat line on any commit
touching this file.

## Blunt git commands in a deliberately dirty tree

Three incidents in one day, two agents, one shape: **a command whose blast
radius is "whatever is dirty", used in a tree that deliberately holds
unfinished work.**

- `git add -A` swept unfinished, known-flaky frontend work into a commit that
  was meant to carry one test, and pushed it.
- `git checkout <file>` inside a mutation loop deleted a test that had just
  been written and not yet committed. Twice.
- `rm -rf` on a worktree whose contents had not been checked.

Long sessions keep work in progress on purpose: a mutation loop leaves the tree
modified by design, and staged work often sits beside an experiment.

> **When the tree deliberately holds unfinished work, name paths. Never `-A`,
> never a bare `checkout`, never a recursive delete of a path you have not
> listed first.**

**That rule is narrower than it sounds, and reading it as broader cost a fourth
incident the same day it was written.** `git checkout <branch> -- internal/poller/`
names a path. It also destroyed an entire uncommitted refactor, because **the
blast radius is not the path — it is whatever is dirty under the path**, and the
whole change was under it. Naming a path bounds the damage; it does not prevent
it.

So the rule that actually holds is procedural rather than syntactic:

> **Commit before a destructive operation, not after.**

A WIP commit on your own branch costs nothing, is revertible, and is a backup.
The `/tmp` copy that saved the refactor above had been left by a mutation loop
for an unrelated reason: that is a coincidence, not a backup, and it had been
relied on twice before anyone noticed.

The reasoning error underneath all of it, stated by the person who made it three
times in one day:

> *"I have been treating 'I can recover it' as equivalent to 'it is safe,' three
> times, and only luck has made those the same."*

For mutation testing specifically, copy the file to `/tmp` and restore with
`cp`, which touches exactly what you named — and commit first anyway.

## A rule that reads as a guarantee and is not one

The most dangerous thing in this document is a sentence that sounds like it
closes a hazard and closes only part of it, because it converts caution into
confidence. Three instances, all met on 2026-09-02 and 2026-09-03, and **every
one of them caught someone who had already written it down**:

- **`pgrep -x`** fixes a script matching its own name. It does nothing about a
  *different* binary with the same name, which is the case that actually
  mattered.
- **The bracket trick** (`[g]o-netmon`) is offered as defeating self-match in a
  `ps | grep` pipeline. It defeats one form of it and not the general case.
- **"Name paths"**, above. It bounds a destructive command; it does not stop one
  from eating uncommitted work under that path.

The pattern: a real mitigation for a real failure, read as covering the
*neighbouring* failure it does not touch. The test is to ask **what exactly does
this rule make impossible**, and then whether the thing you are about to do is
inside that set — rather than whether it feels like the kind of thing the rule
was written about.


## A count is the most convincing form of a wrong answer

`grep -l 'page.route' tests/ | wc -l` returned 12 of 19, and that was read as
"12 specs are self-contained". It is not what it measured: those specs mock
*some* endpoints and still require real devices to exist. Running them showed 3
suites failing on a host with no devices.

The number was the problem. An impression gets checked; a figure feels
measured, and gets used. Ask what the probe counted, not what the question was
— they are rarely the same thing, and the gap is invisible precisely when the
answer arrives with a number attached.
