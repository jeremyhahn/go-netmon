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
# The compose project name is chosen here rather than inherited from the
# directory name. Inherited, the project is "go-netmon" — the same project a
# deployed stack from this repository occupies. Compose would then treat the
# running go-netmon, go-netmon-webui and grafana containers as orphans of this
# file and remove them, "down -v" would delete their volumes (MIB catalog,
# device configuration, metric history), and the live prometheus container
# would be recreated against the test configuration. Choosing the name makes
# the test stack a separate namespace; the preflight below makes that a checked
# fact rather than an assumption a later edit can quietly break.
#
# It is also UNIQUE PER RUN. A fixed name was a separate namespace from the
# deployment but the same namespace as every other concurrent test run, and
# containers are shared per Docker daemon, so two runs on one host adopted each
# other's stack and the first to finish tore it down under the second. Measured
# on 2026-08-21: main run 4905 and PR run 4902 landed on build-01-docker together,
# 4902's cleanup ran at 22:48:24, and 4905 failed 2.5s later with
# "integration-test is missing dependency prometheus" — a stack that was healthy
# until another job deleted it.
#
# The suffix prefers the CI run id, so a failed run's containers are
# identifiable, and falls back to the PID for local runs.

set -euo pipefail

# Unique per PROCESS, identifiable per CI run.
#
# GITHUB_RUN_ID alone was not enough. It is shared by every job in a workflow
# run AND by every re-run attempt of it, so a matrix, a second integration job,
# or a re-run started while the first attempt was still going would reproduce
# exactly the adopt-and-tear-down collision the header above documents from
# 2026-08-21 -- the defect this file was changed to fix. The PID is what
# guarantees uniqueness; the run id and attempt are carried so a failed run's
# containers can still be traced back to the job that left them.
#
# There is deliberately NO environment override here, and stack.sh's
# NETMON_INTEGRATION_PROJECT does not reach this script.
#
# One was added and removed within a day. It was introduced to make the guard
# below reachable from this call site -- rules 1 and 2 can never fire against a
# name this script generates -- and it did that by replacing the WHOLE default,
# including the -$$ two lines above. That deleted the only thing keeping a test
# run off a stack somebody else is using: with the variable exported and a
# manual stack up, this script passed all three guard rules and its EXIT trap
# then ran "down -v --remove-orphans" over it. The guard cannot catch that, and
# compose-guard.sh's header says why -- rule 3 is satisfied by any project
# built from this compose file, which the manual stack is.
#
# The lesson generalises: do not add a production knob to make a control
# reachable. The property that needed testing was "the guard runs with the
# repository root current", and that is observable at the stub, where
# guard-tests.sh now asserts it. Reachability of the guard's rules is tested by
# calling the library directly. Neither needs this script to accept a project
# name from the environment.
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

# --- preflight ------------------------------------------------------------

command -v docker >/dev/null 2>&1 || die "docker is not installed or not on PATH"
docker compose version >/dev/null 2>&1 || die "the docker compose plugin is not available"
[ -f "$COMPOSE_FILE" ] || die "compose file not found: $COMPOSE_FILE"

# cd FIRST. The guard's rule 2 refuses "the project compose would infer from
# the working directory", and it computes that from pwd -- so which directory
# is current when it runs decides which name it refuses.
#
# This used to sit 32 lines further down, after the guard. test/integration's
# own Makefile invokes this script as "./run-tests.sh" with cwd
# test/integration, so the name the guard refused there was "integration" and
# not "go-netmon", and the rule that exists to keep the DEPLOYED project out of
# scope was inert at this call site. stack.sh cds at :64 and guards at :69 and
# was right all along, which is the shape this library's own header calls out:
# the same check behaving differently at its two callers is not a safety
# property. Measured before the fix: default "integration" from
# test/integration, "go-netmon" from the repository root.
cd "$PROJECT_ROOT"

# The shared guard: refuses an empty project, refuses the directory default
# (which is a deployed stack's project), and refuses a project holding
# containers built from a different compose file. It lives in
# compose-guard.sh because the four "make integration-*" targets need exactly
# the same check, and a guard remembered at one call site out of five is how
# "make integration-down" came to be "down -v" against a deployment.
#
# It is deliberately NOT what keeps concurrent runs apart: two runs of THIS
# file match each other and pass it. That is the per-run PROJECT_NAME's job.
# _disposable_, not _safe_: this script destroys its project on the way out, so
# it must additionally refuse the MANUAL stack. That refusal is a production
# rule rather than a test fixture on purpose -- it holds whatever the name came
# from, which an environment-dimension test never can.
integration_require_disposable_project "$PROJECT_NAME" "$COMPOSE_FILE" || exit 1

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
