#!/bin/bash
# Integration test runner: brings up docker-compose.test.yml under a per-run project, runs the tagged tests, and tears only that project down. Usage: run-tests.sh [test-name-regex]

set -euo pipefail

# Unique per process, traceable per CI run; deliberately no environment override.
PROJECT_NAME="go-netmon-integration-${GITHUB_RUN_ID:-local}-${GITHUB_RUN_ATTEMPT:-1}-$$"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
COMPOSE_FILE="$PROJECT_ROOT/docker-compose.test.yml"

# shellcheck source=test/integration/compose-guard.sh
source "$SCRIPT_DIR/compose-guard.sh"

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

# The helper image name, passed to the tests as NETMON_HELPER_IMAGE; local only, never pushed.
HELPER_IMAGE="netmon-integration-helper:local"

# seed_helper_image builds the helper (Dockerfile.helper) and loads it into dind via compose exec; returns non-zero on failure.
seed_helper_image() {
    echo "  building the volume helper image ($HELPER_IMAGE)..."
    if ! docker build -q -t "$HELPER_IMAGE" \
        -f "$PROJECT_ROOT/test/integration/Dockerfile.helper" "$PROJECT_ROOT" >/dev/null; then
        echo "  WARNING: could not build the volume helper image." >&2
        return 1
    fi

    echo "  loading it into the sibling daemon..."
    if ! docker save "$HELPER_IMAGE" | compose --profile dind exec -T dind docker load >/dev/null; then
        echo "  WARNING: could not load the volume helper image into the sibling daemon." >&2
        return 1
    fi
}

# --- preflight ------------------------------------------------------------

command -v docker >/dev/null 2>&1 || die "docker is not installed or not on PATH"
docker compose version >/dev/null 2>&1 || die "the docker compose plugin is not available"
[ -f "$COMPOSE_FILE" ] || die "compose file not found: $COMPOSE_FILE"

# cd to the repository root first: the guard's rule 2 derives the default project from pwd.
cd "$PROJECT_ROOT"

# Refuse an unsafe or manual project (compose-guard.sh); per-run PROJECT_NAME keeps concurrent runs apart.
integration_require_disposable_project "$PROJECT_NAME" "$COMPOSE_FILE" || exit 1

# --- teardown -------------------------------------------------------------

cleanup_done=0
# shellcheck disable=SC2317,SC2329  # invoked through trap, which shellcheck cannot see
cleanup() {
    # Run cleanup once, from either the INT or EXIT trap.
    [ "$cleanup_done" -eq 1 ] && return
    cleanup_done=1

    echo ""
    echo "Cleaning up test services (project $PROJECT_NAME)..."
    # Scoped by -p; --profile dind is required so the privileged dind service is removed too.
    compose --profile dind down -v --remove-orphans >/dev/null 2>&1 || true
}

trap cleanup EXIT
trap 'trap - EXIT; cleanup; exit 130' INT
trap 'trap - EXIT; cleanup; exit 143' TERM

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

# Start the privileged dind daemon if possible; DOCKER_HOST is passed only when it is up, otherwise backup tests skip.
DIND_ENV=()
echo "Starting the sibling Docker daemon (for backup/restore coverage)..."
if compose --profile dind up -d --wait dind 2>/dev/null && seed_helper_image; then
    DIND_ENV=(-e "DOCKER_HOST=tcp://dind:2375" -e "NETMON_HELPER_IMAGE=$HELPER_IMAGE")
    echo "  sibling daemon is up and seeded; backup/restore will be tested against a real daemon"
else
    echo "  WARNING: could not start the sibling Docker daemon, or could not seed it." >&2
    echo "  WARNING: backup/restore will NOT be covered against a real daemon in this run." >&2
    echo "  WARNING: this usually means the runner cannot start a privileged container." >&2
    compose --profile dind rm -sf dind >/dev/null 2>&1 || true
fi

# Rebuild the test image on every run with run --build, which resolves the build context correctly.

# Packages are discovered from the build constraint (tagged-packages.sh).
mapfile -t INTEGRATION_PKGS < <("$SCRIPT_DIR/tagged-packages.sh")
if [ ${#INTEGRATION_PKGS[@]} -eq 0 ]; then
    echo "No package holds an integration-tagged test file. Refusing to report a" >&2
    echo "green integration run for a suite that does not exist." >&2
    exit 1
fi
echo "Integration-tagged packages: ${INTEGRATION_PKGS[*]}"

TEST_ARGS=(go test -v -tags=integration "${INTEGRATION_PKGS[@]}")
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
