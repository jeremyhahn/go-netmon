#!/usr/bin/env bash
# Usage: stack.sh up | e2e-up | down | logs | shell -- manual integration stack lifecycle under a fixed project name that is never the directory default.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
COMPOSE_FILE="$PROJECT_ROOT/docker-compose.test.yml"

# shellcheck source=test/integration/compose-guard.sh
source "$SCRIPT_DIR/compose-guard.sh"

# The name comes from the shared library, shared with clean.sh; an explicitly empty override stays empty and is refused.
PROJECT_NAME="$(integration_manual_project)"

SERVICES=(prometheus snmp-simulator)

# The e2e profile: the same two, plus a poller and a web UI polling the
# simulator, for the browser specs. It is the same project, so one "down"
# removes both.
E2E_SERVICES=("${SERVICES[@]}" poller webui)

die() {
    echo "stack.sh: $*" >&2
    exit 1
}

usage() {
    echo "usage: stack.sh up | e2e-up | down | logs | shell" >&2
    exit 2
}

# The e2e profile is active for every verb, not only e2e-up: a profile's
# services are invisible to a compose command that does not name it, so
# "down" without it would leave the poller and web UI running -- and their
# volumes behind.
compose() {
    docker compose -p "$PROJECT_NAME" -f "$COMPOSE_FILE" --profile e2e "$@"
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
    e2e-up)
        echo "Starting the e2e stack (project $PROJECT_NAME)..."
        compose up -d --build --wait "${E2E_SERVICES[@]}"
        # A one-shot, run rather than waited on: --wait treats a service that
        # has exited as one that failed to come up.
        compose run --rm --build e2e-seed
        echo ""
        echo "The e2e stack's web UI: http://localhost:${NETMON_E2E_PORT:-8083}"
        echo "Run the specs with: make test-e2e"
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
