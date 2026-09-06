#!/usr/bin/env bash
# Prints the Go package patterns that hold integration-tagged tests, one per
# line, relative to the repository root.
#
# WHY THIS IS DISCOVERED RATHER THAN LISTED
#
# The integration scope used to be the literal "./test/integration/...", in
# three places: run-tests.sh, docker-compose.test.yml's command, and this
# image's CMD. That pattern does not match ./internal/..., so
# internal/mib/parser's two //go:build integration files were never compiled
# and never executed by any target or CI job -- a suite sitting in the tree,
# counted by a reader as coverage of the parser's OID handling, with no path to
# execution at all. One of the two had never compiled since the day it was
# written, and nothing noticed for months, because go build, go vet and go test
# are all scoped to the tag set they are given (#30).
#
# A corrected hardcoded list has the identical failure mode the next time a
# package grows a tagged file, and fails the same silent way. So the list is
# derived from the tag itself: a directory holding a file behind
# //go:build integration is a package the integration runner must run, and it
# becomes one the moment the file lands rather than the moment somebody
# remembers to edit three files.
#
# Callers must treat empty output as an error, not as "nothing to run".

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

cd "$PROJECT_ROOT"

grep -rl --include='*.go' '^//go:build integration' . |
    xargs -r -n1 dirname |
    sort -u |
    sed 's|$|/...|'
