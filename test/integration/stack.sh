#!/usr/bin/env bash
# Manual lifecycle for the integration stack: what "make integration-up",
# "integration-down", "integration-logs" and "integration-shell" run.
#
# Usage: stack.sh up | down | logs | shell
#
# WHY THIS EXISTS
#
# Those four targets used to run "docker compose -f docker-compose.test.yml"
# with no -p. Without it compose infers the project from the working directory
# — "go-netmon", which is the project a DEPLOYED stack occupies — so
# "make integration-down" was "down -v" against the deployment: its containers
# removed as orphans of a file that does not define them, and its volumes (MIB
# catalog, device configuration, metric history) deleted with them.
#
# The project name below is fixed rather than per-run, unlike run-tests.sh.
# That is deliberate: these commands are a PAIR a person uses across separate
# invocations — bring the stack up, poke at it, tear it down — so the second
# command has to be able to name what the first created. It is still never the
# directory default, and compose-guard.sh checks that rather than trusting it.
#
# Concurrency is therefore not solved here the way it is for test runs: two
# people running "make integration-up" on one host share this stack. That is
# the honest trade for a usable manual workflow, and it is safe in the way that
# matters — the stack they share is theirs, not the deployment's.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
COMPOSE_FILE="$PROJECT_ROOT/docker-compose.test.yml"

# shellcheck source=test/integration/compose-guard.sh
source "$SCRIPT_DIR/compose-guard.sh"

# The name comes from the shared library rather than being written here,
# because clean.sh needs the SAME answer in order to leave this stack alone.
# Two copies of a name where one side destroys what the other keeps is not a
# constant worth duplicating. The library preserves the "-" rather than ":-"
# form: an explicitly EMPTY override stays empty and is refused by the guard
# instead of being silently replaced. Setting the variable to nothing reads
# like "do not set -p", which is precisely the behaviour that made
# integration-down dangerous, so it fails closed.
PROJECT_NAME="$(integration_manual_project)"

SERVICES=(prometheus snmp-simulator)

die() {
    echo "stack.sh: $*" >&2
    exit 1
}

usage() {
    echo "usage: stack.sh up | down | logs | shell" >&2
    exit 2
}

compose() {
    docker compose -p "$PROJECT_NAME" -f "$COMPOSE_FILE" "$@"
}

[ $# -eq 1 ] || usage

command -v docker >/dev/null 2>&1 || die "docker is not installed or not on PATH"
docker compose version >/dev/null 2>&1 || die "the docker compose plugin is not available"
[ -f "$COMPOSE_FILE" ] || die "compose file not found: $COMPOSE_FILE"

cd "$PROJECT_ROOT"

# Checked before every verb, not just the destructive one: "up" against the
# deployment's project is how its prometheus gets recreated against the test
# configuration, which is its own kind of damage.
integration_require_safe_project "$PROJECT_NAME" "$COMPOSE_FILE" || exit 1

case "$1" in
    up)
        echo "Starting integration test services (project $PROJECT_NAME)..."
        # --wait blocks until every started service reports healthy, and fails
        # if any does not. The sleep this replaces reported success while the
        # simulator was still coming up.
        compose up -d --wait "${SERVICES[@]}"
        echo ""
        echo "Services are up. They publish on ephemeral host ports:"
        compose port prometheus 9090 2>/dev/null | sed 's/^/  prometheus:     /' || true
        # UDP needs --protocol; "161/udp" is not accepted as the port argument
        # and returns nothing, which silently printed no SNMP line at all.
        compose port --protocol udp snmp-simulator 161 2>/dev/null | sed 's/^/  snmp-simulator: /' || true
        echo ""
        echo "Tear down with: make integration-down"
        ;;
    down)
        echo "Stopping integration test services (project $PROJECT_NAME)..."
        compose down -v --remove-orphans
        ;;
    logs)
        compose logs -f
        ;;
    shell)
        compose run --rm integration-test /bin/sh
        ;;
    *)
        usage
        ;;
esac
