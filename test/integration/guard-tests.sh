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
RUN_TESTS="$SCRIPT_DIR/run-tests.sh"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
COMPOSE_FILE="$REPO_ROOT/docker-compose.test.yml"
# Read from the library the code under test reads it from, not written out a
# second time here: a yardstick copied from the value under test stops
# measuring the moment the value moves.
# shellcheck source=test/integration/compose-guard.sh
source "$SCRIPT_DIR/compose-guard.sh"
MANUAL_PROJECT="$(integration_manual_project)"

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
    : >"$root/cwd-log"
    : >"$root/containers"
    cat >"$bin/docker" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$*" >>"$root/log"
# Where each call ran, TAGGED WITH THE CALL, so the reader selects by what the
# call was rather than by its position. Pairing two logs by line number is
# sound only while both writes stay unconditional, and its failure mode is
# silent: make one conditional and the assertion quietly moves to a different
# call, which may happen to be the right directory and pass while measuring
# nothing. $root/log stays byte-identical, so nothing else is affected.
printf '%s\t%s\n' "\$(pwd)" "\$*" >>"$root/cwd-log"

# A forced failure, for the fail-open case.
if [ -f "$root/ps-status" ] && [ "\$1" = "ps" ]; then
    cat "$root/ps-error" 2>/dev/null || true
    exit "\$(cat "$root/ps-status")"
fi

case "\$1" in
    ps)
        # A warning on stderr from a command that SUCCEEDS. Real docker does
        # this for config-load problems and DOCKER_HOST/context deprecations,
        # and it is the input that made 2>&1 dangerous: the warning words end
        # up in the variable the caller parses as container ids.
        [ -f "$root/ps-warning" ] && cat "$root/ps-warning" >&2
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
        *) fail "$desc: output did not mention '$needle'; got: $OUT"; return 1 ;;
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

# THE CALL SITE THE SUITE COULD NOT SEE.
#
# Every directory-default case above drives stack.sh, which cds to the repo
# root BEFORE calling the guard. run-tests.sh called the guard 32 lines BEFORE
# its cd, and rule 2 computes the name it refuses from pwd -- so invoked from
# test/integration, as test/integration/Makefile invokes it, the name it
# refused was "integration" and the DEPLOYED project sailed through. The suite
# was green throughout: a guard whose test only exercises the caller where the
# guard works is not evidence about the caller where it does not.
#
# This asserts the PROPERTY, not a proxy for it: the guard's first docker call
# must happen with the repository root current. The first version of this test
# instead gave run-tests.sh an environment override so a bad project name could
# be fed in -- which tested the right thing and, to do it, deleted the per-run
# uniqueness that keeps the script off a stack in use. Observing pwd at the
# stub costs production nothing.
#
# Driven from test/integration specifically, because that is the directory the
# regression needed; from the repository root the pre-fix script passes.
test_run_tests_guards_with_the_repository_root_current() {
    BIN="$(mktemp -d)"; ROOT="$(mktemp -d)"
    make_fake_docker "$BIN" "$ROOT"

    set +e
    (cd "$SCRIPT_DIR" && env PATH="$BIN:$PATH" "$RUN_TESTS" >/dev/null 2>&1)
    set -e

    # Selected by what the call WAS, not by where it sat. The guard's ownership
    # query is the first "docker ps"; "command -v docker" never executes the
    # stub and "docker compose version" logs as "compose version", so the first
    # "ps " line is unambiguous.
    local cwd
    cwd="$(awk -F'\t' '$2 ~ /^ps /{print $1; exit}' "$ROOT/cwd-log")"
    # The anti-vacuity check: if the script exits before the guard ever runs,
    # there is nothing to assert about and this must say so rather than pass.
    [ -n "$cwd" ] || fail "run-tests.sh never issued the guard's ownership query" || return 1
    [ "$cwd" = "$REPO_ROOT" ] \
        || fail "the guard ran with cwd $cwd, so rule 2 refused $(basename "$cwd") instead of $(basename "$REPO_ROOT")" \
        || return 1
}

# The guard's rules themselves, exercised from both directories by calling the
# library directly rather than through an entry point. This is what the
# environment override was reaching for, without giving production a knob:
# whatever the working directory, rule 2 must refuse the project name that
# directory implies.
test_guard_rules_hold_from_any_directory() {
    BIN="$(mktemp -d)"; ROOT="$(mktemp -d)"
    make_fake_docker "$BIN" "$ROOT"

    local dir out
    for dir in "$REPO_ROOT" "$SCRIPT_DIR"; do
        # Rule 2: the directory's own default is refused, whatever it is.
        #
        # The expected name comes from basename, NOT from
        # integration_directory_project. Passing that function's own output
        # would make the assertion X == X: a version returning a constant would
        # still be refused and this would still pass.
        set +e
        out="$(cd "$dir" && env PATH="$BIN:$PATH" bash -c '
            source "$1/compose-guard.sh"
            integration_require_safe_project "$3" "$2"
        ' _ "$SCRIPT_DIR" "$COMPOSE_FILE" "$(basename "$dir")" 2>&1)"
        local rc=$?
        set -e
        [ "$rc" -ne 0 ] || fail "from $dir the guard accepted that directory's own default" || return 1
        case "$out" in
            *"refusing to use compose project"*) ;;
            *) fail "from $dir the refusal did not name rule 2: $out"; return 1 ;;
        esac

        # Rule 1: an empty name is how compose falls back to that default.
        set +e
        out="$(cd "$dir" && env PATH="$BIN:$PATH" bash -c '
            source "$1/compose-guard.sh"
            integration_require_safe_project "" "$2"
        ' _ "$SCRIPT_DIR" "$COMPOSE_FILE" 2>&1)"
        rc=$?
        set -e
        [ "$rc" -ne 0 ] || fail "from $dir the guard accepted an empty project" || return 1
        case "$out" in
            *"empty compose project name"*) ;;
            *) fail "from $dir the refusal did not name rule 1: $out"; return 1 ;;
        esac
    done
}

# THE INVARIANT THAT BROKE, pinned as a comparison between the two names.
#
# clean.sh was taught to PROTECT the manual project and run-tests.sh was
# simultaneously given a seam that let it DESTROY that project -- through the
# very variable introduced to keep the two in agreement. Neither side's own
# test could see it, because each only checked its own half: clean.sh's tests
# proved clean.sh protects the name, run-tests.sh's proved run-tests.sh refuses
# bad names, and nothing compared the two names to each other.
#
# So the assertion is the comparison itself: the project run-tests.sh actually
# addresses must never equal the project clean.sh protects. Both sides are
# READ FROM THE RUNNING CODE under each environment rather than assumed --
# clean.sh's from the shared library, run-tests.sh's from the -p it really
# passed to docker -- so a change to either side is caught by the same test.
#
# The environments are a table because the variable is the mechanism that broke
# them apart: it moves the manual project's name, so a formulation that only
# checked the default name would miss an operator who overrode it, which is
# precisely the person clean.sh's comment promises to protect.
#
# This table covers the environment dimension, which no test can cover
# exhaustively -- an override reintroduced under a DIFFERENT variable name
# would pass every row here. That hole is closed in production instead, by
# integration_require_disposable_project refusing the manual project whatever
# the name came from; test_run_tests_refuses_the_manual_project_by_rule below
# is the control for it. This table remains as the end-to-end check that the
# rule is actually wired into the script.
test_run_tests_can_never_address_the_project_clean_protects() {
    local env_desc env_assign protected addressed
    local failed=0

    # description | assignments passed to env(1)
    local -a cases=(
        "no override|-u NETMON_INTEGRATION_PROJECT"
        "override set to the default manual name|NETMON_INTEGRATION_PROJECT=go-netmon-integration-manual"
        "override set to a custom name|NETMON_INTEGRATION_PROJECT=go-netmon-integration-someone-else"
        "override set, and a CI run id present|NETMON_INTEGRATION_PROJECT=go-netmon-integration-manual GITHUB_RUN_ID=4242 GITHUB_RUN_ATTEMPT=2"
    )

    local c
    for c in "${cases[@]}"; do
        env_desc="${c%%|*}"
        env_assign="${c#*|}"

        # What clean.sh would refuse to remove, under this environment.
        # shellcheck disable=SC2086
        protected="$(env $env_assign bash -c 'source "$1/compose-guard.sh"; integration_manual_project' _ "$SCRIPT_DIR")"

        # What run-tests.sh actually passes to docker, under the same
        # environment. Taken from the -p it used, not from re-deriving the
        # name here -- a yardstick computed the same way as the value under
        # test cannot disagree with it.
        BIN="$(mktemp -d)"; ROOT="$(mktemp -d)"
        make_fake_docker "$BIN" "$ROOT"
        printf '%s %s %s\n' man1 "$protected" "$COMPOSE_FILE" >"$ROOT/containers"

        local run_out
        set +e
        # shellcheck disable=SC2086
        run_out="$(cd "$SCRIPT_DIR" && env $env_assign PATH="$BIN:$PATH" "$RUN_TESTS" 2>&1)"
        set -e

        # Every distinct project, not the first: two of them in one run would
        # itself be a defect, and "head -1" would hide the second silently.
        local count
        addressed="$(grep -oE -- '-p [^ ]+' "$ROOT/log" 2>/dev/null | awk '{print $2}' | sort -u)"
        count="$(printf '%s' "$addressed" | grep -c . || true)"

        # Refusing outright is a SAFE outcome, and the expected one once the
        # disposable-project rule applies: a script that declines to run has
        # destroyed nothing. It is distinguished from "the harness invoked
        # nothing", which looks identical in the docker log and is how a row
        # passes vacuously -- the env-option ordering bug did exactly that.
        if [ -z "$addressed" ]; then
            case "$run_out" in
                *"refusing to use compose project"*) ;;
                *) fail "[$env_desc] run-tests.sh neither drove compose nor refused; got: $run_out" || failed=1 ;;
            esac
        else
            # MEMBERSHIP over the whole set, tested before anything else and
            # independently of the count. A run that addressed a safe generated
            # name AND the manual project would otherwise be judged on whichever
            # one an equality test happened to hold -- which is how the same
            # comparison, written against "the first project", would pass over
            # precisely the case it exists to catch. The teardown drifting from
            # the setup is a realistic way to get there.
            if printf '%s\n' "$addressed" | grep -qxF -- "$protected"; then
                fail "[$env_desc] run-tests.sh addressed the project clean.sh protects: $protected" || failed=1
                printf '      %s\n' "$(grep -E -- "-p $protected .* down" "$ROOT/log" || true)" >&2
            fi
            # And one project per run regardless: two is a defect in itself.
            if [ "$count" -ne 1 ]; then
                fail "[$env_desc] run-tests.sh addressed $count distinct projects: $(printf '%s' "$addressed" | tr '\n' ' ')" || failed=1
            fi
        fi
        rm -rf "$BIN" "$ROOT"
    done

    [ "$failed" -eq 0 ] || return 1
}

# THE RULE, tested where it lives, independent of any environment.
#
# The table above can only ever cover the variable names it knows about. This
# does not care: it hands the guard the manual project directly and requires a
# refusal. A future override under any name, a flag, or a typo that happens to
# produce that string is refused at the call site, because the rule is about
# the NAME, not about where the name came from.
#
# Both directions, because a rule that refuses everything is not a rule: the
# manual project must be refused for a disposable entry point and PERMITTED for
# stack.sh, which owns that stack.
test_run_tests_refuses_the_manual_project_by_rule() {
    BIN="$(mktemp -d)"; ROOT="$(mktemp -d)"
    make_fake_docker "$BIN" "$ROOT"

    local manual out rc
    manual="$(integration_manual_project)"

    set +e
    out="$(cd "$REPO_ROOT" && env PATH="$BIN:$PATH" bash -c '
        source "$1/compose-guard.sh"
        integration_require_disposable_project "$3" "$2"
    ' _ "$SCRIPT_DIR" "$COMPOSE_FILE" "$manual" 2>&1)"
    rc=$?
    set -e

    [ "$rc" -ne 0 ] || fail "the disposable-project rule accepted the manual project '$manual'" || return 1
    case "$out" in
        *"That is the MANUAL stack"*) ;;
        *) fail "the refusal did not say why: $out"; return 1 ;;
    esac

    # And the same name is fine for stack.sh's guard, which owns it.
    set +e
    out="$(cd "$REPO_ROOT" && env PATH="$BIN:$PATH" bash -c '
        source "$1/compose-guard.sh"
        integration_require_safe_project "$3" "$2"
    ' _ "$SCRIPT_DIR" "$COMPOSE_FILE" "$manual" 2>&1)"
    rc=$?
    set -e

    [ "$rc" -eq 0 ] \
        || fail "the base guard refused the manual project, which stack.sh legitimately owns: $out" \
        || return 1
}

# THAT THE RULE IS WIRED IN, which no dynamic test in this suite can reach.
#
# Found by mutation: switching run-tests.sh back to the base
# integration_require_safe_project left all 21 other cases green. The reason is
# structural rather than an oversight in them -- run-tests.sh generates
# go-netmon-integration-<run>-<attempt>-<pid>, which cannot equal the manual
# project's name, so the refusal never fires in a passing run and nothing
# observes whether it was even consulted.
#
# That is what the rule is FOR: it is defence in depth against a future change
# that makes a collision possible again, exactly like the override that made
# one possible last time. A guarantee whose violation is currently unreachable
# still has to be pinned, or the next refactor quietly drops it.
#
# So this is a static check, deliberately, and it is honest about being one: it
# asserts the wiring, not the behaviour, because the behaviour has no reachable
# path to assert. Both directions, since "everything calls the strict variant"
# would be wrong -- stack.sh owns the manual stack and must keep the base rule.
test_disposable_entry_points_use_the_disposable_rule() {
    # Permissive about PLACEMENT, strict about the NAME. Anchoring at column 0
    # pins formatting rather than wiring: rewriting the call as
    # "if ! integration_require_disposable_project ...; then exit 1; fi" leaves
    # the guarantee identical, and an anchored match reports "<nothing>" --
    # failing as though the guard had been deleted. Comment lines are stripped
    # first so the surrounding prose, which names both functions, is not read
    # as a call.
    guards_called_by() {
        grep -vE '^[[:space:]]*#' "$1" \
            | grep -oE 'integration_require_[a-z_]+_project' \
            | sort -u
    }

    local call count

    # run-tests.sh destroys its project on the way out.
    call="$(guards_called_by "$RUN_TESTS")"
    count="$(printf '%s' "$call" | grep -c . || true)"
    [ "$count" -le 1 ] \
        || fail "run-tests.sh calls more than one guard: $(printf '%s' "$call" | tr '\n' ' ')" \
        || return 1
    [ "$call" = "integration_require_disposable_project" ] \
        || fail "run-tests.sh tears its project down but calls ${call:-<nothing>}, not integration_require_disposable_project" \
        || return 1

    # stack.sh owns the manual stack and must NOT refuse it.
    call="$(guards_called_by "$STACK")"
    count="$(printf '%s' "$call" | grep -c . || true)"
    [ "$count" -le 1 ] \
        || fail "stack.sh calls more than one guard: $(printf '%s' "$call" | tr '\n' ' ')" \
        || return 1
    [ "$call" = "integration_require_safe_project" ] \
        || fail "stack.sh owns the manual stack but calls ${call:-<nothing>}, which would refuse it" \
        || return 1
}

# An explicitly EMPTY project must reach rule 1, even through the strict wrapper.
#
# This codebase deliberately preserves the empty case -- "-" rather than ":-"
# in integration_manual_project and stack.sh exists precisely so that setting
# the variable to nothing stays nothing and is refused, rather than being
# silently replaced by a default. That makes rule 1's message load-bearing
# there: it is the one path where an operator has to be told they set nothing.
#
# The wrapper compares against the manual project first, and with the override
# set empty integration_manual_project returns empty too, so "" matched it and
# was refused as "that is the MANUAL stack" -- correct refusal, wrong reason,
# and the wrong reason is the whole content of the message.
test_an_empty_project_is_refused_as_empty_not_as_the_manual_stack() {
    BIN="$(mktemp -d)"; ROOT="$(mktemp -d)"
    make_fake_docker "$BIN" "$ROOT"

    local out rc
    set +e
    out="$(cd "$REPO_ROOT" && env PATH="$BIN:$PATH" NETMON_INTEGRATION_PROJECT= bash -c '
        source "$1/compose-guard.sh"
        integration_require_disposable_project "" "$2"
    ' _ "$SCRIPT_DIR" "$COMPOSE_FILE" 2>&1)"
    rc=$?
    set -e

    [ "$rc" -ne 0 ] || fail "an empty project was accepted" || return 1
    case "$out" in
        *"empty compose project name"*) ;;
        *) fail "an empty project was refused for the wrong reason: $out"; return 1 ;;
    esac
}

# The other half of the pair, so the comparison above cannot be satisfied by a
# clean.sh that protects nothing.
test_clean_still_protects_the_manual_project_under_an_override() {
    # INSIDE PROJECT_PREFIX, deliberately. clean.sh has two independent reasons
    # to leave a project alone -- the manual-stack skip and the namespace
    # filter -- so an out-of-namespace name is skipped either way and the test
    # cannot tell which branch saved it. Measured: with the manual-skip
    # deleted, "someone-elses-stack" is still left alone (control vacuous)
    # while "go-netmon-integration-someone-else" is torn down (control works).
    # An override name that is also in the namespace has both properties at
    # once, which is what makes this a control rather than a coincidence.
    local manual="go-netmon-integration-someone-else"

    BIN="$(mktemp -d)"; ROOT="$(mktemp -d)"
    make_fake_docker "$BIN" "$ROOT"
    printf '%s %s %s\n' man1 "$manual" "$COMPOSE_FILE" >"$ROOT/containers"

    set +e
    env PATH="$BIN:$PATH" NETMON_INTEGRATION_PROJECT="$manual" "$SCRIPT_DIR/clean.sh" >/dev/null 2>&1
    set -e

    if grep -qE 'compose .* down' "$ROOT/log" 2>/dev/null; then
        fail "clean.sh tore down the overridden manual project: $(grep -E 'compose .* down' "$ROOT/log")" || return 1
    fi
}

# The manual stack is in the recovery namespace and is NOT an orphan. A person
# leaves it up on purpose -- stack.sh's header says two people on one host
# share it -- so a prefix match that selects it turns "make clean" into
# "delete the stack your colleague is using", volumes included.
test_clean_leaves_the_manual_stack_alone() {
    BIN="$(mktemp -d)"; ROOT="$(mktemp -d)"
    make_fake_docker "$BIN" "$ROOT"
    printf '%s %s %s\n' man1 "$MANUAL_PROJECT" "$COMPOSE_FILE" >"$ROOT/containers"

    set +e
    OUT="$(env PATH="$BIN:$PATH" "$SCRIPT_DIR/clean.sh" 2>&1)"
    set -e

    if grep -qE 'compose .* down' "$ROOT/log" 2>/dev/null; then
        fail "clean.sh tore down the manual stack: $(grep -E 'compose .* down' "$ROOT/log")" || return 1
    fi
    case "$OUT" in
        *"that is the manual stack"*) ;;
        *) fail "clean.sh did not report skipping the manual stack; got: $OUT"; return 1 ;;
    esac
}

# ...and still removes a genuine orphan, so the skip above is a skip and not a
# blanket refusal. Without this the previous test passes against a clean.sh
# that does nothing at all.
test_clean_still_removes_a_real_orphan_alongside_the_manual_stack() {
    BIN="$(mktemp -d)"; ROOT="$(mktemp -d)"
    make_fake_docker "$BIN" "$ROOT"
    {
        printf '%s %s %s\n' man1 "$MANUAL_PROJECT" "$COMPOSE_FILE"
        printf '%s %s %s\n' orp1 go-netmon-integration-777-1-999 "$COMPOSE_FILE"
    } >"$ROOT/containers"

    set +e
    env PATH="$BIN:$PATH" "$SCRIPT_DIR/clean.sh" >/dev/null 2>&1
    set -e

    local downed
    downed="$(grep -oE -- '-p [^ ]+' "$ROOT/log" 2>/dev/null | awk '{print $2}' | sort -u)"
    [ "$downed" = "go-netmon-integration-777-1-999" ] \
        || fail "expected only the orphan torn down; downed: ${downed:-<nothing>}" || return 1
}

# docker's stderr must stay OUT of the value the guard parses as container ids.
#
# With 2>&1 a warning on a SUCCESSFUL "docker ps" became the ids: the guard then
# ran "docker inspect <warning word>", that failed, and it refused every entry
# point with a message naming a container that does not exist. Fail-closed, and
# unusable -- the operator is told to check the ownership of a container id that
# was never a container id.
test_guard_survives_a_warning_on_dockers_stderr() {
    BIN="$(mktemp -d)"; ROOT="$(mktemp -d)"
    make_fake_docker "$BIN" "$ROOT"
    : >"$ROOT/containers"
    printf 'WARNING: DOCKER_HOST is deprecated, use docker context\n' >"$ROOT/ps-warning"

    set +e
    OUT="$(cd "$REPO_ROOT" && env PATH="$BIN:$PATH" "$STACK" down 2>&1)"
    local status=$?
    set -e

    [ "$status" -eq 0 ] || fail "the guard refused because of a stderr warning: $OUT" || return 1
    case "$OUT" in
        *"could not read the compose file behind container"*)
            fail "docker's warning was parsed as a container id: $OUT"; return 1 ;;
    esac
}

# The refusal text is what a human reads to judge whether the guard was right,
# so it has to survive a path with a space in it. Accumulated into a string and
# re-split by an unquoted "for", each id and each path fragment landed on its
# own line and a container with no config_files label printed a bare "()".
test_guard_reports_each_foreign_container_on_one_line() {
    BIN="$(mktemp -d)"; ROOT="$(mktemp -d)"
    make_fake_docker "$BIN" "$ROOT"
    printf '%s %s %s\n' fgn1 "$MANUAL_PROJECT" "/opt/my stacks/docker-compose.yml" >"$ROOT/containers"

    set +e
    OUT="$(cd "$REPO_ROOT" && env PATH="$BIN:$PATH" "$STACK" down 2>&1)"
    set -e

    local listed
    listed="$(printf '%s\n' "$OUT" | grep -c '^  fgn1 (' || true)"
    [ "$listed" -eq 1 ] \
        || fail "expected one line naming fgn1 with its compose file; got: $OUT" || return 1
    case "$OUT" in
        *"fgn1 (/opt/my stacks/docker-compose.yml)"*) ;;
        *) fail "the foreign container's compose path was split across lines: $OUT"; return 1 ;;
    esac
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
