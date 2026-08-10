#!/bin/bash
# Integration test runner.
#
# Brings up the integration stack (Prometheus + snmpsim) defined by the
# repository-root docker-compose.test.yml, runs the tagged integration tests
# against it, and tears that stack — and only that stack — down again on every
# exit path.
#
# Usage: run-tests.sh [test-name-regex]
#
#   run-tests.sh                        # every integration test
#   run-tests.sh TestSNMPConnectivity   # one test
#
# SAFETY
#
# This script destroys containers and volumes. Everything it destroys must
# belong to a project created by this script and nothing else.
#
# The compose project name is pinned rather than inherited from the directory
# name. Inherited, the project is "go-netmon" — the same project a deployed
# stack from this repository occupies. Compose would then treat the running
# go-netmon, go-netmon-webui and grafana containers as orphans of this file and
# remove them, "down -v" would delete their volumes (MIB catalog, device
# configuration, metric history), and the live prometheus container would be
# recreated against the test configuration. A pinned project makes the test
# stack a separate namespace; the preflight below makes that a checked fact
# rather than an assumption a later edit can quietly break.

set -euo pipefail

PROJECT_NAME="go-netmon-integration"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
COMPOSE_FILE="$PROJECT_ROOT/docker-compose.test.yml"

# Services the tests talk to. The test runner itself is invoked explicitly with
# "run" and must not be started here, or the suite executes twice.
SERVICES=(prometheus snmp-simulator)

die() {
    echo "run-tests.sh: $*" >&2
    exit 1
}

compose() {
    docker compose -p "$PROJECT_NAME" -f "$COMPOSE_FILE" "$@"
}

# --- preflight ------------------------------------------------------------

command -v docker >/dev/null 2>&1 || die "docker is not installed or not on PATH"
docker compose version >/dev/null 2>&1 || die "the docker compose plugin is not available"
[ -f "$COMPOSE_FILE" ] || die "compose file not found: $COMPOSE_FILE"

# Refuse to run if the project we are about to tear down already holds a
# container this script did not create. Compose identifies ownership with the
# com.docker.compose.project label, so a name collision with a real deployment
# is detectable before anything is removed rather than after.
foreign="$(docker ps -aq \
    --filter "label=com.docker.compose.project=$PROJECT_NAME" \
    --filter "label=com.docker.compose.project.config_files!=$COMPOSE_FILE" 2>/dev/null || true)"
if [ -n "$foreign" ]; then
    echo "Containers in project '$PROJECT_NAME' that were not created from $COMPOSE_FILE:" >&2
    docker ps -a --filter "label=com.docker.compose.project=$PROJECT_NAME" \
        --format '  {{.Names}}  ({{.Label "com.docker.compose.project.config_files"}})' >&2
    die "refusing to run: tearing this project down would remove containers it does not own"
fi

# --- teardown -------------------------------------------------------------

cleanup_done=0
# shellcheck disable=SC2317  # invoked through trap, which shellcheck cannot see
cleanup() {
    # A single-shot guard. Without it an interrupt runs cleanup from the INT
    # trap and then again from the EXIT trap, and the second "down -v" reports
    # confusing errors over a project that is already gone.
    [ "$cleanup_done" -eq 1 ] && return
    cleanup_done=1

    echo ""
    echo "Cleaning up test services (project $PROJECT_NAME)..."
    # Scoped to this project by the pinned -p above; --remove-orphans therefore
    # only reaches services this compose file used to define.
    compose down -v --remove-orphans >/dev/null 2>&1 || true
}

trap cleanup EXIT
trap 'trap - EXIT; cleanup; exit 130' INT
trap 'trap - EXIT; cleanup; exit 143' TERM

cd "$PROJECT_ROOT"

echo "Starting integration test environment..."
echo "Project root:    $PROJECT_ROOT"
echo "Compose project: $PROJECT_NAME"
echo "Compose file:    $COMPOSE_FILE"

# --wait blocks until every started service reports healthy and fails if any
# does not. The previous loop polled "docker compose ps | grep healthy", which
# matched as soon as any one service was healthy and let the suite start
# against a simulator that had not come up.
echo "Building and starting services..."
compose up -d --build --wait --remove-orphans "${SERVICES[@]}"

# The test image bakes the source in at build time (the compose bind mount
# lands on /app, which is not the image's working directory), so it has to be
# rebuilt or the run executes whatever the tests looked like when the image was
# last built. "compose run" only builds when no image exists yet, which makes a
# stale image indistinguishable from a passing change.
echo "Building test runner image..."
compose build integration-test

TEST_ARGS=(go test -v -tags=integration ./test/integration/...)
if [ -n "${1:-}" ]; then
    echo "Running integration tests matching: $1"
    TEST_ARGS+=(-run "$1")
else
    echo "Running all integration tests..."
fi

TEST_EXIT_CODE=0
compose run --rm integration-test "${TEST_ARGS[@]}" || TEST_EXIT_CODE=$?

echo "Tests completed with exit code: $TEST_EXIT_CODE"

exit "$TEST_EXIT_CODE"
