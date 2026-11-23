# Cutting a release

> **Status: REFERENCE** · Scope: go-netmon

Every release is a tag on `main`, of one of three kinds, each cut by its own
target. Pushing the tag publishes the release on GitHub: nothing is created by
hand.

| Kind | Tag | Target | Published as |
|---|---|---|---|
| Alpha | `vX.Y.Z-alpha` | `make release-alpha [VERSION=vX.Y.Z-alpha]` | pre-release |
| Release candidate | `vX.Y.Z-rc.N`, `N` from 1 | `make release-candidate VERSION=vX.Y.Z-rc.N` | pre-release |
| Production | `vX.Y.Z` | `make release VERSION=vX.Y.Z` | release |

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

From a clean `main` equal to the GitHub remote's:

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
| `RELEASE_REMOTE` | the remote `main` tracks, else `origin` | The GitHub remote to push to |
| `RELEASE_GIT` | `git` | The command that commits, tags and pushes, e.g. a wrapper that signs |

## After the tag

The tag starts `.github/workflows/release.yml`:

| Job | Does |
|---|---|
| verify | `make release-verify`: `VERSION` is the tag without its `v`, `CHANGELOG.md` has exactly one non-empty section for it, no fragment or `[Unreleased]` entry is left, and the commit is on `origin/main` |
| the gate | every job `ci.yml`'s CI Passed needs (lint, security, unit tests, build, shell suites, FIPS, integration), by the same names and commands, on the tagged commit |
| binaries | `make release-build`, `release-build-fips`, `release-sbom`, `release-checksums` (in parallel with the gate; nothing leaves the workflow) |
| image | after the gate: each of `go-netmon` and `go-netmon-webui`, standard and FIPS, on linux/amd64 and linux/arm64 (native runners), built, scanned with `make image-scan` (trivy: a fixed HIGH or CRITICAL fails it), then pushed to GHCR by digest |
| manifest | the multi-arch tags over those digests, and a build-provenance attestation pushed to the registry |
| publish | checks every asset against `SHA256SUMS`, attests the archives' provenance and SBOM, and creates the GitHub release as a draft with `make release-notes` as its body; publishes it only once the draft holds every asset |

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
| `ghcr.io/jeremyhahn/go-netmon` | `<ver>`; `X.Y` and `latest` for a production release only |
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
`release.yml` to its invariants: image and publish need verify and every gate
job, each gate job runs what `ci.yml`'s does, and every action is pinned to a
commit. CI runs it in the Shell Suites job.
