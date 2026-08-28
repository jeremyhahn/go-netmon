#!/usr/bin/env bash
# Tests for compose-guard.sh and the stack.sh lifecycle verbs.
#
# What these protect is the difference between "tears down the test stack" and
# "tears down the deployment". That is a data-loss boundary, and the only
# evidence it holds is a test that tries to cross it: shellcheck and a syntax
# check cannot tell a guard that refuses from a guard that returns success
# without looking.
#
# Everything runs against a stub `docker` first on PATH, so no daemon is
# contacted and no container is ever created or removed.
#
# Usage: guard-tests.sh [test-name-substring]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
STACK="$SCRIPT_DIR/stack.sh"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
COMPOSE_FILE="$REPO_ROOT/docker-compose.test.yml"
MANUAL_PROJECT="go-netmon-integration-manual"

PASS=0
FAIL=0
FAILED_TESTS=()
OUT=""

# --- stub docker ----------------------------------------------------------

# make_fake_docker writes a stub CLI that MODELS CONTAINERS AND THEIR LABELS
# rather than replaying a fixed answer.
#
# That distinction is the point. The first version returned its configured
# output whatever filter it was handed, so it could not tell a correct
# ownership query from a malformed one -- and the guard shipped with a filter
# ("label=KEY!=VALUE") that real docker silently matches nothing with. A stub
# that answers regardless of the question cannot catch a wrong question.
#
# State, one container per line of $root/containers:
#
#   <id> <project> <config_files>
#
# The stub understands only what the guard is allowed to use: "docker ps -aq
# --filter label=com.docker.compose.project=<p>" and "docker inspect --format
# ... <id>". Anything else -- notably docker's unsupported "label!=" -- is
# rejected the way the daemon rejects it, so a future rewrite reaching for a
# filter that does not exist fails here instead of in production.
make_fake_docker() {
    local bin="$1" root="$2"
    mkdir -p "$bin" "$root"
    : >"$root/log"
    : >"$root/containers"
    cat >"$bin/docker" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$*" >>"$root/log"

# A forced failure, for the fail-open case.
if [ -f "$root/ps-status" ] && [ "\$1" = "ps" ]; then
    cat "$root/ps-error" 2>/dev/null || true
    exit "\$(cat "$root/ps-status")"
fi

case "\$1" in
    ps)
        want_project=""; want_config=""
        for a in "\$@"; do
            case "\$a" in
                label!=*)
                    echo "Error response from daemon: invalid filter 'label!'" >&2
                    exit 1
                    ;;
                label=com.docker.compose.project=*)
                    want_project="\${a#label=com.docker.compose.project=}"
                    ;;
                label=com.docker.compose.project.config_files=*)
                    want_config="\${a#label=com.docker.compose.project.config_files=}"
                    ;;
                label=*!=*)
                    # The malformed negation. Real docker treats the whole tail
                    # as the label VALUE and matches nothing; say so loudly
                    # instead, so the bug cannot hide behind an empty result.
                    echo "fake-docker: refusing malformed label filter: \$a" >&2
                    exit 1
                    ;;
            esac
        done
        while read -r id project config; do
            [ -n "\$id" ] || continue
            [ -z "\$want_project" ] || [ "\$project" = "\$want_project" ] || continue
            [ -z "\$want_config" ] || [ "\$config" = "\$want_config" ] || continue
            echo "\$id"
        done <"$root/containers"
        exit 0
        ;;
    inspect)
        id="\${@: -1}"
        while read -r cid project config; do
            if [ "\$cid" = "\$id" ]; then
                case "\$*" in
                    *config_files*) echo "\$config" ;;
                    *) echo "\$project" ;;
                esac
                exit 0
            fi
        done <"$root/containers"
        echo "Error: No such object: \$id" >&2
        exit 1
        ;;
    compose)
        case "\$2" in version) echo "Docker Compose version v2.0.0" ;; esac
        exit 0
        ;;
esac
exit 0
EOF
    chmod +x "$bin/docker"
}

# run_stack runs a stack.sh verb against a fresh stub, capturing combined
# output in $OUT and returning the verb's exit status. Extra NAME=VALUE
# arguments are exported for the run.
run_stack() {
    local verb="$1"; shift
    BIN="$(mktemp -d)"
    ROOT="$(mktemp -d)"
    make_fake_docker "$BIN" "$ROOT"

    set +e
    OUT="$(env PATH="$BIN:$PATH" "$@" "$STACK" "$verb" 2>&1)"
    local status=$?
    set -e
    return $status
}

fail() {
    printf '    FAIL: %s\n' "$*" >&2
    return 1
}

expect_refused() {
    local desc="$1" needle="$2"
    case "$OUT" in
        *"$needle"*) ;;
        *) fail "$desc: output did not mention %q; got: $OUT"; return 1 ;;
    esac
    # A refusal that still ran a destructive command is not a refusal.
    if grep -qE 'compose .* down' "$ROOT/log" 2>/dev/null; then
        fail "$desc: refused but still issued a compose down"
        return 1
    fi
}

# --- guard rules ----------------------------------------------------------

# The headline case: the project a deployed stack occupies must be refused.
test_guard_refuses_the_directory_default_project() {
    local default
    default="$(basename "$REPO_ROOT")"
    if run_stack down "NETMON_INTEGRATION_PROJECT=$default"; then
        fail "down succeeded against the directory-default project" || return 1
    fi
    expect_refused "directory default" "refusing to use compose project"
}

test_guard_refuses_an_empty_project() {
    if run_stack down "NETMON_INTEGRATION_PROJECT="; then
        fail "down succeeded with an empty project" || return 1
    fi
    expect_refused "empty project" "empty compose project name"
}

# The case the malformed filter could not see: a container in the SAME project
# whose config_files label names a DIFFERENT compose file. It is foreign, and
# tearing the project down would destroy it.
test_guard_refuses_a_project_holding_foreign_containers() {
    BIN="$(mktemp -d)"; ROOT="$(mktemp -d)"
    make_fake_docker "$BIN" "$ROOT"
    printf '%s %s %s\n' deadbeef1234 "$MANUAL_PROJECT" /somewhere/else/docker-compose.yml \
        >"$ROOT/containers"

    set +e
    OUT="$(env PATH="$BIN:$PATH" "$STACK" down 2>&1)"
    local status=$?
    set -e

    [ $status -ne 0 ] || fail "down succeeded on a project holding a container from another compose file" || return 1
    expect_refused "foreign containers" "refusing to run"
    case "$OUT" in
        *"/somewhere/else/docker-compose.yml"*) ;;
        *) fail "the refusal does not name the foreign container's compose file: $OUT"; return 1 ;;
    esac
}

# The complement: containers that ARE ours must not be mistaken for foreign, or
# the guard refuses every legitimate teardown and gets removed by the next
# person to hit it.
test_guard_permits_a_project_holding_only_our_containers() {
    BIN="$(mktemp -d)"; ROOT="$(mktemp -d)"
    make_fake_docker "$BIN" "$ROOT"
    printf '%s %s %s\n' cafe0001 "$MANUAL_PROJECT" "$COMPOSE_FILE" >"$ROOT/containers"
    printf '%s %s %s\n' cafe0002 "$MANUAL_PROJECT" "$COMPOSE_FILE" >>"$ROOT/containers"

    set +e
    OUT="$(env PATH="$BIN:$PATH" "$STACK" down 2>&1)"
    local status=$?
    set -e

    [ $status -eq 0 ] || fail "down refused a project holding only our own containers: $OUT" || return 1
}

# A container of ours in a DIFFERENT project must not make this project look
# foreign -- the project filter has to be applied, not ignored.
test_guard_ignores_containers_in_other_projects() {
    BIN="$(mktemp -d)"; ROOT="$(mktemp -d)"
    make_fake_docker "$BIN" "$ROOT"
    printf '%s %s %s\n' beef0001 some-other-project /somewhere/else/docker-compose.yml \
        >"$ROOT/containers"

    set +e
    OUT="$(env PATH="$BIN:$PATH" "$STACK" down 2>&1)"
    local status=$?
    set -e

    [ $status -eq 0 ] || fail "down refused because of a container in an unrelated project: $OUT" || return 1
}

# The fail-open case: a docker that ERRORS must not read as "nothing foreign".
test_guard_refuses_when_the_ownership_query_fails() {
    BIN="$(mktemp -d)"; ROOT="$(mktemp -d)"
    make_fake_docker "$BIN" "$ROOT"
    printf 'Cannot connect to the Docker daemon\n' >"$ROOT/ps-error"
    printf '1\n' >"$ROOT/ps-status"

    set +e
    OUT="$(env PATH="$BIN:$PATH" "$STACK" down 2>&1)"
    local status=$?
    set -e

    [ $status -ne 0 ] || fail "down succeeded although the ownership query failed" || return 1
    expect_refused "failed ownership query" "ownership could not be checked"
}

# --- lifecycle verbs ------------------------------------------------------

# Every verb must pass the dedicated project. One that does not gets the
# directory default, which is the deployment.
test_every_verb_names_the_manual_project() {
    local verb
    for verb in up down logs shell; do
        run_stack "$verb" || true
        local bare
        bare="$(grep -E '^compose ' "$ROOT/log" \
            | grep -vE '^compose version' \
            | grep -vE -- "-p $MANUAL_PROJECT" || true)"
        [ -z "$bare" ] || fail "verb '$verb' issued compose without the project: $bare" || return 1
    done
}

test_down_removes_volumes_and_orphans() {
    run_stack down || fail "down failed against the default project" || return 1

    local down
    down="$(grep -E -- "compose -p $MANUAL_PROJECT .* down" "$ROOT/log" || true)"
    [ -n "$down" ] || fail "no compose down was issued" || return 1
    case "$down" in *" -v"*) ;; *) fail "down omits -v: $down"; return 1 ;; esac
    case "$down" in *"--remove-orphans"*) ;; *) fail "down omits --remove-orphans: $down"; return 1 ;; esac
}

# The guard runs before every verb, not only the destructive one: "up" against
# the deployment recreates its prometheus against the test configuration.
test_guard_runs_before_up_not_only_down() {
    local default
    default="$(basename "$REPO_ROOT")"
    if run_stack up "NETMON_INTEGRATION_PROJECT=$default"; then
        fail "up succeeded against the directory-default project" || return 1
    fi
    if grep -qE 'compose .* up' "$ROOT/log" 2>/dev/null; then
        fail "up was issued despite the guard refusing" || return 1
    fi
}

test_unknown_verb_is_rejected() {
    if run_stack frobnicate; then
        fail "an unknown verb was accepted" || return 1
    fi
    case "$OUT" in
        *"usage:"*) ;;
        *) fail "unknown verb did not print usage; got: $OUT"; return 1 ;;
    esac
}


# --- every entry point, one table ----------------------------------------

# The deployment's project must be unreachable from EVERY entry point, not just
# the one that was last found reaching it. Two of tonight's defects were the
# same mistake in different files -- the Makefile targets, then the recovery
# target -- so this asserts the property once, over all of them, rather than
# leaving each new entry point to be audited by hand.
#
# TWO fixtures, because they exercise different halves of the protection and
# the weaker one alone passes vacuously:
#
#   mixed  - the deployment project holds a container from another compose file
#            as well as one of ours. Here clean.sh's exclusivity check refuses,
#            so the namespace check is never reached.
#   ours   - the deployment project holds ONLY containers from our compose file.
#            This is the arrangement measured on 2026-08-21, where an unscoped
#            "make integration-up" put test services into project "go-netmon";
#            exclusivity passes and ONLY the namespace check stands between the
#            recovery target and the deployment's volumes.
#
# Running just "mixed" was the first version of this test, and it passed with
# the namespace check deleted.
test_no_entry_point_ever_addresses_the_deployment_project() {
    local deployment
    deployment="$(basename "$REPO_ROOT")"

    local entry fixture failed=0
    for fixture in mixed ours; do
    for entry in "stack.sh up" "stack.sh down" "stack.sh logs" "stack.sh shell" \
                 "clean.sh" "run-tests.sh"; do
        BIN="$(mktemp -d)"; ROOT="$(mktemp -d)"
        make_fake_docker "$BIN" "$ROOT"
        if [ "$fixture" = mixed ]; then
            {
                printf '%s %s %s\n' dep1 "$deployment" /opt/go-netmon/docker-compose.yml
                printf '%s %s %s\n' dep2 "$deployment" "$COMPOSE_FILE"
            } >"$ROOT/containers"
        else
            printf '%s %s %s\n' dep1 "$deployment" "$COMPOSE_FILE" >"$ROOT/containers"
        fi

        # shellcheck disable=SC2086
        set +e
        env PATH="$BIN:$PATH" GITHUB_RUN_ID=tabletest "$SCRIPT_DIR"/$entry >/dev/null 2>&1
        set -e

        # Exact match on the -p VALUE: "go-netmon-integration-manual" contains
        # "go-netmon", so a substring test here would pass vacuously.
        local addressed p
        addressed="$(grep -oE -- '-p [^ ]+' "$ROOT/log" 2>/dev/null | awk '{print $2}' | sort -u)"
        for p in $addressed; do
            if [ "$p" = "$deployment" ]; then
                printf '    FAIL: [%s] %s addressed the deployment project %q\n' \
                    "$fixture" "$entry" "$deployment" >&2
                failed=1
            fi
        done
        rm -rf "$BIN" "$ROOT"
    done
    done

    [ "$failed" -eq 0 ] || return 1
}

# The other half of the same hazard, and the half no stub can reach: a command
# somebody COPIES OUT OF THE DOCUMENTATION. Codex found "docker-compose -f
# docker-compose.test.yml up -d" and a matching "down" in the E2E example in
# docs/architecture/testing-strategy.md, plus three more in
# docs/INTEGRATION_TESTS.md. Every entry point in the tree can be watertight and
# a developer following the docs still runs "down" against the deployment.
#
# So the rule is scanned rather than remembered: any line that STARTS a compose
# invocation naming docker-compose.test.yml must carry -p. Starting the line is
# what separates a command from prose about a command -- stack.sh's own header
# quotes the dangerous form in order to explain it, and a substring scan would
# have to be taught to ignore that, which is the kind of exception that grows
# until the scan means nothing.
#
# It reads the file list from git rather than a hardcoded set of directories,
# because the defect being prevented is a NEW file with a bad example in it.
# That means TRACKED files: a doc written and not yet added is invisible to
# this scan locally. It is not invisible in CI, where the checkout is what git
# knows about, so the gate still catches it before the change lands -- just one
# step later than the author would like.
test_no_documented_command_addresses_the_deployment_project() {
    local offenders
    offenders="$(cd "$REPO_ROOT" && git ls-files -z \
        | xargs -0 grep -nE '^[[:space:]]*(\$ )?docker[- ]compose ' 2>/dev/null \
        | grep -F -- 'docker-compose.test.yml' \
        | grep -vE -- '-p [^-][^ ]*' \
        || true)"

    if [ -n "$offenders" ]; then
        printf '    FAIL: compose commands on the test compose file with no -p:\n' >&2
        printf '      %s\n' "$offenders" >&2
        return 1
    fi
}

# --- runner ---------------------------------------------------------------

main() {
    local filter="${1:-}"

    [ -x "$STACK" ] || { echo "not executable: $STACK" >&2; exit 1; }
    [ -f "$COMPOSE_FILE" ] || { echo "compose file not found: $COMPOSE_FILE" >&2; exit 1; }

    local tests
    mapfile -t tests < <(declare -F | awk '{print $3}' | grep '^test_' | sort)

    for t in "${tests[@]}"; do
        if [ -n "$filter" ] && [[ "$t" != *"$filter"* ]]; then
            continue
        fi
        printf '  %s\n' "$t"
        if "$t"; then
            PASS=$((PASS + 1))
        else
            FAIL=$((FAIL + 1))
            FAILED_TESTS+=("$t")
        fi
    done

    printf '\ncompose guard: %d passed, %d failed\n' "$PASS" "$FAIL"
    if [ "$FAIL" -gt 0 ]; then
        printf 'failed:\n'
        printf '  %s\n' "${FAILED_TESTS[@]}"
        return 1
    fi
}

main "$@"
