#!/usr/bin/env bash
# Remove integration stacks left by killed runs: only go-netmon-integration* projects whose every container is ours. Usage: clean.sh

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
COMPOSE_FILE="$PROJECT_ROOT/docker-compose.test.yml"

# The namespace run-tests.sh and stack.sh generate. Matching on it is what
# keeps the deployment's project ("go-netmon") out of scope: it is a prefix
# match on a name no deployment uses.
PROJECT_PREFIX="go-netmon-integration"

# Leave the manual stack alone; its name comes from the shared library, matching stack.sh.
# shellcheck source=test/integration/compose-guard.sh
source "$SCRIPT_DIR/compose-guard.sh"
MANUAL_PROJECT="$(integration_manual_project)"

PROJECT_LABEL="com.docker.compose.project"
CONFIG_LABEL="com.docker.compose.project.config_files"

die() {
    echo "clean.sh: $*" >&2
    exit 1
}

# label_of <container-id> <label> prints a container's label, failing if docker
# cannot be asked.
label_of() {
    local id="$1" label="$2" out status=0
    out="$(docker inspect --format "{{index .Config.Labels \"$label\"}}" "$id" 2>&1)" || status=$?
    if [ "$status" -ne 0 ]; then
        echo "clean.sh: could not inspect container $id (exit $status): $out" >&2
        return 1
    fi
    printf '%s' "$out"
}

command -v docker >/dev/null 2>&1 || die "docker is not installed or not on PATH"
[ -f "$COMPOSE_FILE" ] || die "compose file not found: $COMPOSE_FILE"

# --- discover -------------------------------------------------------------

ours=""
status=0
ours="$(docker ps -aq --filter "label=$CONFIG_LABEL=$COMPOSE_FILE" 2>&1)" || status=$?
[ "$status" -eq 0 ] || die "could not list containers (exit $status): $ours"

# Each candidate is recorded as "<project>=<anchor>": the project name, and the
# id of the container that put it in scope. The anchor is carried through to
# the ownership check, which has to prove the project it is about to destroy is
# still the one discovery looked at. A compose project name is [a-z0-9_-], so
# "=" cannot occur in the first field.
candidates=""
for id in $ours; do
    project="$(label_of "$id" "$PROJECT_LABEL")" || exit 1
    if [ "$project" = "$MANUAL_PROJECT" ]; then
        echo "clean.sh: skipping project '$project': that is the manual stack, torn down with 'make integration-down'" >&2
        continue
    fi
    case "$project" in
        "$PROJECT_PREFIX"*) ;;
        *)
            # Condition 1 refusing. Not an error -- a test container inside a
            # foreign project is a mess to report, not one to resolve by
            # deleting the project around it.
            echo "clean.sh: skipping project '$project': outside the ${PROJECT_PREFIX}* namespace" >&2
            echo "  (container $id was built from this compose file, but the project is not ours to remove)" >&2
            continue
            ;;
    esac
    case " $candidates " in
        *" $project="*) ;;
        *) candidates="$candidates $project=$id" ;;
    esac
done

if [ -z "${candidates// /}" ]; then
    echo "no integration stacks to clean"
    exit 0
fi

# --- verify exclusive ownership, then remove ------------------------------

# verify_exclusive <project> <anchor>: 0 ours, 1 refused, 2 docker failed; the project must be non-empty, hold the anchor and be all ours.
verify_exclusive() {
    local project="$1" anchor="$2"
    local members="" status=0 id owner anchor_present=0

    members="$(docker ps -aq --filter "label=$PROJECT_LABEL=$project" 2>&1)" || status=$?
    if [ "$status" -ne 0 ]; then
        echo "clean.sh: could not list containers in project '$project' (exit $status): $members" >&2
        return 2
    fi

    # Condition 1.
    if [ -z "${members//[[:space:]]/}" ]; then
        echo "clean.sh: refusing to clean project '$project': it holds no containers now, so nothing proves it is ours" >&2
        echo "  (container $anchor put it in scope and has since gone; any volumes are deliberately left in place)" >&2
        return 1
    fi

    # Condition 3, and condition 2 gathered on the way through.
    for id in $members; do
        if [ "$id" = "$anchor" ]; then
            anchor_present=1
        fi
        owner="$(label_of "$id" "$CONFIG_LABEL")" || return 2
        if [ "$owner" != "$COMPOSE_FILE" ]; then
            echo "clean.sh: refusing to clean project '$project': container $id came from $owner" >&2
            return 1
        fi
    done

    # Condition 2.
    if [ "$anchor_present" -ne 1 ]; then
        echo "clean.sh: refusing to clean project '$project': the container that identified it ($anchor) is no longer in it" >&2
        return 1
    fi

    return 0
}

failed=0
for entry in $candidates; do
    project="${entry%%=*}"
    anchor="${entry#*=}"

    verify_exclusive "$project" "$anchor"
    case "$?" in
        0) ;;
        2) failed=1; continue ;;
        *) continue ;;
    esac

    # Re-check ownership immediately before tearing down.
    verify_exclusive "$project" "$anchor"
    case "$?" in
        0) ;;
        2) failed=1; continue ;;
        *) continue ;;
    esac

    echo "removing integration stack $project"
    # --profile dind or the profile-gated privileged container survives.
    if ! docker compose -p "$project" -f "$COMPOSE_FILE" --profile dind down -v --remove-orphans; then
        echo "clean.sh: teardown of project '$project' failed" >&2
        failed=1
    fi
done

exit "$failed"
