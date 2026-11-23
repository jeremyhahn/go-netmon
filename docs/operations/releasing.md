# Cutting a release

> **Status: REFERENCE** · Scope: go-netmon

Every release is a tag on `main`, of one of three kinds. `main` takes changes
only through pull requests, so a release is a pull request too: a release
target prepares it, merging it is the release, and the workflows tag and
publish it. Nothing is tagged or published by hand.

| Kind | Tag | Target |
|---|---|---|
| Alpha | `vX.Y.Z-alpha` | `make release-alpha [VERSION=vX.Y.Z-alpha]` |
| Release candidate | `vX.Y.Z-rc.N`, `N` from 1 | `make release-candidate VERSION=vX.Y.Z-rc.N` |
| Production | `vX.Y.Z` | `make release VERSION=vX.Y.Z` |

Each target refuses a version of the other two kinds. A candidate or a
production release names its version; an alpha without one is the next patch
of `VERSION` (`0.1.3` gives `v0.1.4-alpha`). A version never moves backward,
and a published tag is never moved or deleted: a wrong release is fixed by
cutting the next one.

## Recording a change

A change never edits `CHANGELOG.md`. It adds a fragment,
`changelog.d/<issue>-<slug>.md`, holding only its entry under the Keep a
Changelog headings it lands under; [changelog.d/README.md](../../changelog.d/README.md)
has the format.

## Cutting it

```text
make release-… VERSION=…  ->  release/vX.Y.Z branch  ->  PR  ->  merge
  ->  CI green on main  ->  auto-release.yml tags  ->  release.yml publishes
```

From a clean `main` equal to the GitHub remote's:

```sh
make release-candidate VERSION=v0.3.0-rc.1 DRY_RUN=1   # every check, nothing written
make release-candidate VERSION=v0.3.0-rc.1
```

`DRY_RUN=1` runs every check, the changelog fold included, and prints the
branch it would prepare. Without it the target:

1. refuses a version of the wrong kind or form, any branch but `main`, a dirty
   tree, a local `main` that differs from the remote's, a tag that exists
   locally or on the remote, a `release/<version>` branch that exists locally
   or on the remote (or a remote it cannot reach), a version not strictly
   above `VERSION` and every release tag, a release with no entry, and a fold
   that would fail: a malformed or empty fragment, a section that already
   exists for the version, or a `CHANGELOG.md` with no `## [Unreleased]`;
2. creates `release/<version>`, folds the fragments (and any
   `## [Unreleased]` entry) into a `## [X.Y.Z] - <date>` section of
   `CHANGELOG.md`, deletes the fragments and writes `VERSION`;
3. checks the result (`scripts/release-check-tag.sh`, the check auto-release
   and release.yml make), commits `chore(release): <version>` with
   `RELEASE_GIT`, pushes the branch, and prints the `gh pr create` command
   that opens the PR.

It pushes nothing to `main` and creates no tag. If the push is rejected, the
commit exists only on the local branch; the error prints how to retry or undo.

| Variable | Default | Meaning |
|---|---|---|
| `VERSION` | none (alpha: next patch) | The version to cut |
| `DRY_RUN` | unset | `1`: check and print, write nothing |
| `RELEASE_REMOTE` | the remote `main` tracks, else `origin` | The GitHub remote to push the branch to |
| `RELEASE_GIT` | `git` | The command that commits and pushes |

`main` accepts only signed commits. Maintainers sign the release commit
through their signing wrapper:

```sh
make release VERSION=v1.0.0 RELEASE_GIT="/path/to/signing-wrapper"
```

## After the merge

`.github/workflows/auto-release.yml` runs when CI completes. It acts only on
a successful CI run for a push to `main`, and reads `VERSION` at that commit:

- if `v$VERSION` is already a tag, the merge changed no version and nothing is
  published;
- if the commit is no longer `main`'s tip, the newer commit's CI run decides;
- otherwise it runs `make release-verify` (the check release.yml's verify job
  makes) and `scripts/release-version-check.sh` against the previous commit,
  tags the commit as `github-actions[bot]`, pushes the tag, and calls
  `release.yml` with the version.

The release is called rather than started by the tag, because a tag pushed
with the workflow's own token starts no workflow.

### Re-running or releasing by hand

- Re-run a release whose tag exists:
  `gh workflow run release.yml -f version=vX.Y.Z`.
- Pushing a `v*` tag on a `main` commit by hand still starts `release.yml`.

Runs for the same tag, however they started, wait for one another.

## release.yml

| Job | Does |
|---|---|
| verify | checks out the tag (the input of a call or a manual run, else the pushed tag); `make release-verify`: `VERSION` is the tag without its `v`, `CHANGELOG.md` has exactly one non-empty section for it, no fragment or `[Unreleased]` entry is left, and the commit is on `origin/main`; decides whether the release is latest |
| the gate | every job `ci.yml`'s CI Passed needs (lint, security, unit tests, build, shell suites, FIPS, integration), by the same names and commands, on verify's commit |
| binaries | `make release-build`, `release-build-fips`, `release-sbom`, `release-checksums` (in parallel with the gate; nothing leaves the workflow) |
| image | after the gate: each of `go-netmon` and `go-netmon-webui`, standard and FIPS, on linux/amd64 and linux/arm64 (native runners), built, scanned with `make image-scan` (trivy: a fixed HIGH or CRITICAL fails it), then pushed to GHCR by digest |
| manifest | the multi-arch tags over those digests, and a build-provenance attestation pushed to the registry |
| publish | checks every asset against `SHA256SUMS`, attests the archives' provenance and SBOM, and creates the GitHub release as a draft with `make release-notes` as its body; publishes it only once the draft holds every asset |

Every job after verify builds and publishes verify's tag and commit.

### Latest

Verify decides once whether a release is the latest, and the images and the
GitHub release both follow it:

- before 1.0, every release is, `-alpha` and `-rc.N` included;
- from 1.0 on, only a plain `vX.Y.Z` is; an `-alpha` or `-rc.N` is published
  as a GitHub pre-release.

A latest release becomes GitHub's Latest release (chosen by date and SemVer,
so re-running an older release never takes it from a newer one) and moves the
images' `X.Y` and `latest` tags.

### What gets published

GitHub release `vX.Y.Z`, with `<ver>` = `X.Y.Z`:

- `go-netmon_<ver>_linux_{amd64,arm64}.tar.gz`: `go-netmon`, `webui`,
  `netmon`, `netmon-backup`, `mib-importer` (pure Go, `-trimpath`, stamped
  with version, commit and commit date), plus `LICENSE`, `README.md`,
  `config.example.yaml` and `deploy/*.service`;
- `go-netmon_<ver>_linux_{amd64,arm64}_fips.tar.gz`: the same, built with
  `GOFIPS140=latest -tags fips`;
- `go-netmon_<ver>_sbom.cdx.json` (CycloneDX, `cyclonedx-gomod`);
- `SHA256SUMS` over all of the above.

Images, linux/amd64 + linux/arm64:

| Image | Tags |
|---|---|
| `ghcr.io/jeremyhahn/go-netmon` | `<ver>`; `X.Y` and `latest` for a latest release only ([above](#latest)) |
| `ghcr.io/jeremyhahn/go-netmon-webui` | the same |
| either, FIPS build | the same tags with `-fips`: `<ver>-fips`, `X.Y-fips`, `latest-fips` |

The archives are reproducible: building the same commit with
`make release-build VERSION=<ver>` gives byte-identical files.

### When a run fails

Nothing is published until the gate passes, and the release is a draft until
every asset is attached. Re-run the failed jobs. A publish that stopped after
creating the draft leaves it on the Releases page: delete the draft, then
re-run publish. Images pushed by digest without a tag are inert until
manifest tags them.

## Testing the release tooling

`make test-release` drives every script and target against scratch
repositories, checks the release artifact recipes with `make -n`, and holds
the workflows to their invariants: image and publish need verify and every
gate job, each gate job runs what `ci.yml`'s does, every job after verify uses
verify's tag and commit, the latest decision, auto-release acting only on a
green push to `main` and granting exactly what `release.yml` needs, and every
action pinned to a commit. CI runs it in the Shell Suites job.
