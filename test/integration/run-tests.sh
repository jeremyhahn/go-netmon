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
    #
    # --profile dind is REQUIRED, not tidiness: "compose down" does not remove a
    # profile-gated service, so without it the privileged dind container
    # survives every teardown -- this trap included, on successful runs.
    # Measured 2026-08-21: eight of them up on this workstation (one per
    # integration run of the day), three on the build-01 runner with one up two
    # days, one on gpu-host. They hold a Docker daemon and a storage volume
    # each, and accumulate on a runner until somebody looks.
    compose --profile dind down -v --remove-orphans >/dev/null 2>&1 || true
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

# The sibling Docker daemon, for the backup/restore tests.
#
# It is privileged, and a runner that is itself unprivileged or rootless cannot
# start one. That is a fact about where the suite is running, not a defect in
# the code under test, so it must not fail the run -- the rest of the
# integration tests have nothing to do with Docker.
#
# So it is attempted, and DOCKER_HOST is passed to the tests only if it comes
# up. Without that variable the backup tests skip and say so, which is the
# honest outcome: the coverage was not obtained, and the log says which.
DIND_ENV=()
echo "Starting the sibling Docker daemon (for backup/restore coverage)..."
if compose --profile dind up -d --wait dind 2>/dev/null; then
    DIND_ENV=(-e "DOCKER_HOST=tcp://dind:2375")
    echo "  sibling daemon is up; backup/restore will be tested against a real daemon"
else
    echo "  WARNING: could not start the sibling Docker daemon." >&2
    echo "  WARNING: backup/restore will NOT be covered against a real daemon in this run." >&2
    echo "  WARNING: this usually means the runner cannot start a privileged container." >&2
    compose --profile dind rm -sf dind >/dev/null 2>&1 || true
fi

# The test image bakes the source in at build time, so it has to be rebuilt on
# every run or the suite executes whatever the tests looked like when the image
# was last built. Plain "compose run" only builds when no image exists yet,
# which makes a stale image indistinguishable from a passing change, so the
# build is requested explicitly with --build below.
#
# --build on "run" rather than a separate "compose build integration-test":
# under CI the standalone build resolved the context against the wrong
# directory and failed with
#
#   #2 [internal] load build definition from Dockerfile.test
#   #2 transferring dockerfile: 2B done
#   failed to solve: failed to read dockerfile: open Dockerfile.test: ...
#
# having transferred an empty context, while the "up --build" above built its
# services correctly in the same job. This uses that working path.

TEST_ARGS=(go test -v -tags=integration ./test/integration/...)
if [ -n "${1:-}" ]; then
    echo "Running integration tests matching: $1"
    TEST_ARGS+=(-run "$1")
else
    echo "Running all integration tests..."
fi

TEST_EXIT_CODE=0
compose run --rm --build "${DIND_ENV[@]}" integration-test "${TEST_ARGS[@]}" || TEST_EXIT_CODE=$?

echo "Tests completed with exit code: $TEST_EXIT_CODE"

exit "$TEST_EXIT_CODE"
