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

For mutation testing specifically, copy the file to `/tmp` and restore with
`cp`, which touches exactly what you named.

## A count is the most convincing form of a wrong answer

`grep -l 'page.route' tests/ | wc -l` returned 12 of 19, and that was read as
"12 specs are self-contained". It is not what it measured: those specs mock
*some* endpoints and still require real devices to exist. Running them showed 3
suites failing on a host with no devices.

The number was the problem. An impression gets checked; a figure feels
measured, and gets used. Ask what the probe counted, not what the question was
— they are rarely the same thing, and the gap is invisible precisely when the
answer arrives with a number attached.
