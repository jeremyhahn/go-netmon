#!/usr/bin/env bash
# Recovery cleanup: remove integration stacks left behind by a run that was
# killed hard enough to skip its trap.
#
# Usage: clean.sh
#
# THE HAZARD THIS IS BUILT AROUND
#
# "Find the stacks by ownership label and tear them down" is not safe on its
# own, and the unsafe version was measured, not imagined: `make integration-up`
# can create test services inside the project compose infers from the working
# directory -- "go-netmon" -- which is the project a DEPLOYED stack occupies.
# Selecting projects by "contains a container from our compose file" then
# returns "go-netmon", and `down -v --remove-orphans` over it removes the
# deployment's containers as orphans and deletes its volumes: the MIB catalog,
# the device configuration, the metric history.
#
# A per-container match proves that ONE container is ours. It does not prove
# the PROJECT is. So two independent conditions must both hold before anything
# is removed:
#
#   1. the project name is inside the namespace this repository generates
#      (go-netmon-integration*), which the deployment's name is not; and
#   2. EVERY container in that project carries our compose file's label, so a
#      project holding anything else is left alone rather than cleaned.
#
# Either one alone is insufficient. (1) without (2) would tear down a mixed
# project that merely happens to be named like ours; (2) without (1) is the
# defect described above.
#
# (2) is a claim about every member, so it is TRUE OF A PROJECT WITH NO
# MEMBERS. That is not a quibble: discovery and the membership query are two
# separate docker calls, and a project emptied in between passes a check that
# looked at nothing. verify_exclusive therefore also requires the project to
# be non-empty and to still contain the container discovery found it by.
#
# Every docker call is checked. A discovery or teardown failure exits nonzero
# rather than reporting "nothing to clean", which would be the same
# silent-success this script exists to correct.

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
COMPOSE_FILE="$PROJECT_ROOT/docker-compose.test.yml"

# The namespace run-tests.sh and stack.sh generate. Matching on it is what
# keeps the deployment's project ("go-netmon") out of scope: it is a prefix
# match on a name no deployment uses.
PROJECT_PREFIX="go-netmon-integration"

# The manual stack is IN that namespace and is not an orphan. "make
# integration-up" creates go-netmon-integration-manual and a person leaves it
# up on purpose -- stack.sh's header says in terms that two people on one host
# share it. A prefix match alone selects it, so "make -C test/integration
# clean" would run "down -v --remove-orphans" over a stack someone is using
# and delete its volumes underneath them. It has its own teardown, "make
# integration-down"; recovery cleanup is for runs killed past their trap.
#
# Read from the shared library so this and stack.sh cannot drift, and so an
# operator who overrides NETMON_INTEGRATION_PROJECT is protected by the same
# override on both sides.
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

# verify_exclusive <project> <anchor> decides whether a project may be
# destroyed. It prints its reason and returns:
#
#   0  proven ours
#   1  refused -- the answer was "no", which is a normal outcome, not an error
#   2  the question could not be asked -- a docker failure, which is an
#      error and must not read as "nothing to clean"
#
# Three conditions must ALL hold, and the first two exist because the third is
# a universal quantifier: "every member came from our compose file" is
# satisfied vacuously by a project with no members at all. If the containers
# disappear between discovery and this query the loop runs zero times and
# reports an exclusivity it never observed, and "down -v --remove-orphans"
# then runs over a name whose current contents were never looked at. A foreign
# container that took that name in the gap goes with it, and so do the
# project's volumes.
#
#   1. the project still HAS containers;
#   2. the container that identified it at discovery is still one of them, so
#      this is the same project that was looked at and not a reuse of the name;
#   3. every member carries our compose file's label.
#
# For the DECISION, (1) is subsumed by (2): an empty project cannot contain the
# anchor, so (2) alone already refuses it -- measured by deleting (1) and
# watching the teardown still be refused. (1) is kept because the two
# situations are not the same thing to the person reading the output. "the
# stack you asked me to remove no longer exists" and "this name now holds a
# different container" call for different responses, and collapsing them
# reports the second when the first is what happened.
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

    # Asked again immediately before the destructive call. This NARROWS the
    # window between proving ownership and acting on it; it cannot close it,
    # because the docker CLI offers no way to make the two atomic. It is worth
    # the extra query because everything between the two checks -- the label
    # reads over every member -- is time the first answer was ageing.
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
