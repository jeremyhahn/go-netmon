# changelog.d

A change records its CHANGELOG entry here instead of editing `CHANGELOG.md`: a
shared `## [Unreleased]` section would make every open pull request conflict
with its siblings.

Each fragment is `changelog.d/<feature>.md`, where `<feature>` is the
[docs/SPEC.md](../docs/SPEC.md) feature the change belongs to. Every pull
request on that feature edits the same fragment. It holds only entries, under
the [Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/) headings
they land under:

```markdown
### Added

- **devices**: Registering a device that is already registered returns the existing device instead of an error. ([#N](../../issues/N))

### Changed

- **BREAKING** api: <what stops working, and what to do instead>. ([#N](../../issues/N))
```

- Only `### ` headings: `Added`, `Changed`, `Deprecated`, `Removed`, `Fixed`,
  `Security`. Every line belongs under a heading; text outside one is refused.
- One or two sentences per entry, describing the outcome for a user, not how it
  was built. End with a link to the issue.
- A breaking change goes under `### Changed` and starts `**BREAKING** <feature>:`.
- No entry for changes a user cannot act on: CI, lint, test-only, refactor and
  docs-only changes. A raised Go version, a security fix, a dependency change
  that affects users or a change to supported platforms does get one.

The release targets (`make release-alpha`, `make release-candidate`,
`make release`; [docs/operations/releasing.md](../docs/operations/releasing.md))
fold every fragment into the version's section, grouped by heading, and
delete them. This README is not a fragment.
