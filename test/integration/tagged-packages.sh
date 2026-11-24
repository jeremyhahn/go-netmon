#!/usr/bin/env bash
# Print the Go package patterns holding //go:build integration files, one per line; callers treat empty output as an error.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

cd "$PROJECT_ROOT"

# A vendor tree is other modules' code, which ./... never runs either.
grep -rl --include='*.go' --exclude-dir=vendor \
    '^//go:build integration' . |
    xargs -r -n1 dirname |
    sort -u |
    sed 's|$|/...|'
