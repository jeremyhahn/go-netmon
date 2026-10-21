# changelog.d

A change records its CHANGELOG entry here instead of editing `CHANGELOG.md`
(handbook `software/conventions/release-versioning.md` §5): a shared
`## [Unreleased]` section makes every open pull request conflict with its
siblings.

Add one file per change, `changelog.d/<issue>-<slug>.md`, holding only its
entries under the [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
headings they land under:

```markdown
### Added

- **`netmon thing`** (#123). What it does, for whom.

### Fixed

- The poller no longer ... (#123).
```

- Only `### ` headings: `Added`, `Changed`, `Deprecated`, `Removed`, `Fixed`,
  `Security`. Mark a breaking change in the heading:
  `### Changed — BREAKING: <what breaks>`.
- Every line belongs under a heading; text outside one is refused.

The release targets (`make release-alpha`, `make release-candidate`,
`make release`; [docs/operations/releasing.md](../docs/operations/releasing.md))
fold every fragment into the version's section, grouped by heading, and
delete them. This README is not a fragment.
