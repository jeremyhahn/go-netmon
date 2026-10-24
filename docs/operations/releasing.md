# Cutting a release

> **Status: REFERENCE** · Scope: go-netmon

Every release is a tag on `main`, of one of three kinds, each cut by its own
target; the contract is handbook `software/conventions/release-versioning.md`
§1a.

| Kind | Tag | Target |
|---|---|---|
| Alpha (internal) | `vX.Y.Z-alpha` | `make release-alpha [VERSION=vX.Y.Z-alpha]` |
| Release candidate | `vX.Y.Z-rc.N`, `N` from 1 | `make release-candidate VERSION=vX.Y.Z-rc.N` |
| Production | `vX.Y.Z` | `make release VERSION=vX.Y.Z` |

Each target refuses a version of the other two kinds. A candidate or a
production release names its version; an alpha without one is the next patch
of `VERSION` (`0.1.3` gives `v0.1.4-alpha`).

## Recording a change

A change never edits `CHANGELOG.md`. It adds a fragment,
`changelog.d/<issue>-<slug>.md`, holding only its entry under the Keep a
Changelog headings it lands under; [changelog.d/README.md](../../changelog.d/README.md)
has the format.

## Cutting it

From a clean `main` equal to the `dev` remote's:

```sh
make release-candidate VERSION=v0.2.0-rc.1 DRY_RUN=1   # every check, nothing written
make release-candidate VERSION=v0.2.0-rc.1
```

`DRY_RUN=1` runs every check, the changelog fold included, and prints the tag
it would cut. Without it the target:

1. refuses a version of the wrong kind or form, any branch but `main`, a dirty
   tree, a local `main` that differs from the remote's, a tag that exists
   locally or on the remote (or a remote it cannot reach), a version not
   strictly above `VERSION` and every release tag, a release with no entry,
   and a fold that would fail: a malformed or empty fragment, a section that
   already exists for the version, or a `CHANGELOG.md` with no
   `## [Unreleased]`;
2. folds the fragments (and any `## [Unreleased]` entry) into a
   `## [X.Y.Z] - <date>` section of `CHANGELOG.md`, deletes the fragments and
   writes `VERSION`;
3. checks the result (`scripts/release-check-tag.sh`), commits
   `release: vX.Y.Z`, tags it, and pushes `main` and the tag in one atomic
   push.

If the push is rejected, nothing reached the remote; the error prints the two
commands that undo the local commit and tag.

| Variable | Default | Meaning |
|---|---|---|
| `VERSION` | none (alpha: next patch) | The version to cut |
| `DRY_RUN` | unset | `1`: check and print, write nothing |
| `RELEASE_REMOTE` | `dev` | The remote to push to. Never `github` |
| `RELEASE_GIT` | `git` | The command that commits, tags and pushes; on a host, `../claude-agents/scripts/agent-git.sh go-netmon`, which signs as the project agent |

## After the tag

The tag starts `.github/workflows/release.yml`:

- **verify** (`make release-verify TAG=...`): `VERSION` is the tag without its
  `v`, `CHANGELOG.md` has exactly one non-empty section for it, no fragment
  or `[Unreleased]` entry is left, and the commit is on `origin/main`.
- **the gate**: every job `ci.yml`'s CI Passed needs (lint, security, unit
  tests, build, the shell suites and shell lint, FIPS, integration), by the
  same names and commands, on the tagged commit. `make test-release` fails if
  the two workflows drift apart.
- **publish** (Gitea only), once verify and every gate job succeeded:
  `make build-all`, then `make release-publish` creates the forge release as
  a draft with the tag's CHANGELOG section as its body
  (`make release-notes TAG=...` prints it), attaches the binaries and their
  `SHA256SUMS`, checks the draft holds exactly those files at their sizes,
  and only then publishes it, `-alpha` and `-rc.N` as pre-releases.

A publish that fails part way leaves a draft, never a published release with
missing assets: re-run the job, and it takes the draft over, replaces its
assets and publishes it. A release already published is refused, never
edited.

A wrong tag is never moved or deleted: cut the next version
(release-versioning.md §3).

## Testing the release tooling

`make test-release` drives every script and target against scratch
repositories and a fake forge; CI runs it in the Shell Suites job.
