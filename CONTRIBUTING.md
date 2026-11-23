# Contributing to go-netmon

Thanks for helping. This page covers setting up, testing and submitting a
change. By taking part you agree to the [Code of Conduct](CODE_OF_CONDUCT.md).

## Before you start

- **Bugs and features:** open an issue first for anything larger than a small
  fix, so the approach can be agreed before you write it.
- **Security issues:** never in a public issue or pull request. Follow
  [SECURITY.md](SECURITY.md).

## Development setup

You need:

- Go, at the version in [go.mod](go.mod)
- `make`
- Docker with the Compose plugin, for the integration suite and the local stack
- `shellcheck`, for `make lint-scripts`

```sh
git clone https://github.com/jeremyhahn/go-netmon.git
cd go-netmon
make install-tools   # the pinned golangci-lint
make build-all       # all binaries into ./bin
```

`make help` lists every target.

## Testing

| Command | Runs | Needs |
|---|---|---|
| `make test` | unit tests with `-race` | Go only |
| `make lint` | golangci-lint | `make install-tools` |
| `make fmt-check` | fails on any file that is not gofmt-clean | Go only |
| `make security` | govulncheck and trivy | `govulncheck` and `trivy` on `PATH` |
| `make integration-test` | the integration suite against real Prometheus and SNMP simulators | Docker |
| `make ci-fast` | every check that needs no Docker | as above |
| `make ci` | everything a pull request must pass | Docker |

Run a single package's tests while you work, for example `make test-poller`
or `go test ./internal/poller/ -run TestName`, and `make ci` before you open
the pull request. [docs/testing.md](docs/testing.md) explains what a test here
has to prove.

Do not run a formatter on `internal/webui/static/js/app.js`. It is
hand-formatted and the repository has no prettier configuration, so a formatter
rewrites thousands of lines and buries the real change. Check
`git diff --stat` before committing any change to it.

## Making a change

1. Branch from `main`.
2. Write the change with tests. Go code is `gofmt`-clean and passes `make lint`.
3. Update the documentation under [docs/](docs/README.md) that the change makes
   wrong.
4. If users will notice the change, add a changelog fragment (below).
5. Open a pull request against `main` and fill in the template.

### Commit messages

Use [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/)
subjects: `type(scope): summary`, in the imperative, under about 72
characters. Types in use: `feat`, `fix`, `docs`, `test`, `refactor`, `perf`,
`build`, `ci`, `chore`. Reference the issue in the body or the PR (`#123`).

```text
fix(poller): stop a removed device's worker before deleting it
```

### Changelog fragments

Do not edit `CHANGELOG.md`. Add or edit `changelog.d/<feature>.md`, where
`<feature>` is the [docs/SPEC.md](docs/SPEC.md) feature the change belongs to.
[changelog.d/README.md](changelog.d/README.md) gives the format. CI, test,
refactor and docs-only changes get no entry.

## Review

A maintainer reviews every pull request. CI must be green before merge. Keep
pull requests to one concern; a focused change is reviewed faster.

## License

Contributions are accepted under the [Apache License 2.0](LICENSE), the
project's license, as section 5 of that license describes.
