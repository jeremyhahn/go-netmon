#!/usr/bin/env bash
# Tests for run-tests.sh's compose namespacing and teardown.
#
# The behaviour under test is destructive and concurrent, which is exactly the
# combination CI cannot exercise: the integration job runs the script once, so a
# regression back to a shared project name would look green until two runs
# landed on one host again and one deleted the other's stack. That is how the
# original defect reached main.
#
# So these run the real script against a stub `docker` placed first on PATH,
# recording every invocation. No daemon, no containers, no possibility of
# touching a live stack.
#
# Usage: harness-tests.sh [test-name-substring]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUN_TESTS="$SCRIPT_DIR/run-tests.sh"
COMPOSE_FILE="$(cd "$SCRIPT_DIR/../.." && pwd)/docker-compose.test.yml"

PASS=0
FAIL=0
FAILED_TESTS=()

# --- stub docker ----------------------------------------------------------

# make_fake_docker writes a stub CLI that appends each invocation to $1/log and
# succeeds. run-tests.sh calls docker directly rather than through a $DOCKER
# variable, so it is injected by PATH rather than by environment -- which also
# means the test covers the real command lines, not a rewritten form of them.
make_fake_docker() {
    local bin="$1" log="$2"
    mkdir -p "$bin"
    cat >"$bin/docker" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$*" >>"$log"
case "\$1 \$2" in
    "compose version") echo "Docker Compose version v2.0.0" ;;
esac
# "docker ps -aq" must print nothing: an empty result is "no foreign
# containers", which is what lets the preflight pass.
exit 0
EOF
    chmod +x "$bin/docker"
    # run-tests.sh vendors the modules for the image build before compose.
    # The stub logs that call and vendors nothing: no build happens here.
    cat >"$bin/go" <<EOF
#!/usr/bin/env bash
printf 'go %s\\n' "\$*" >>"$log"
exit 0
EOF
    chmod +x "$bin/go"
}

# run_harness runs run-tests.sh with the stub on PATH and the given run id,
# leaving its docker invocations in the named log.
run_harness() {
    local log="$1" run_id="${2-}"
    local bin
    bin="$(mktemp -d)"
    make_fake_docker "$bin" "$log"

    if [ -n "$run_id" ]; then
        env PATH="$bin:$PATH" GITHUB_RUN_ID="$run_id" "$RUN_TESTS" >/dev/null 2>&1
    else
        env -u GITHUB_RUN_ID PATH="$bin:$PATH" "$RUN_TESTS" >/dev/null 2>&1
    fi
    rm -rf "$bin"
}

# projects_in prints the distinct project names a log's compose calls used.
projects_in() {
    grep -oE -- '-p [^ ]+' "$1" | awk '{print $2}' | sort -u
}

fail() {
    printf '    FAIL: %s\n' "$*" >&2
    return 1
}


# --- stub docker for clean.sh --------------------------------------------

# make_container_docker writes a stub that MODELS CONTAINERS, for the recovery
# tests. clean.sh selects projects from container labels, so a stub replaying a
# fixed answer could not tell a safe selection from one that picks the
# deployment.
#
# State, one container per line of $root/containers:  <id> <project> <config_files>
# Optional: $root/ps-status (force docker ps to fail), $root/down-status
# (force compose down to fail).
make_container_docker() {
    local bin="$1" root="$2"
    mkdir -p "$bin" "$root"
    : >"$root/log"; : >"$root/containers"
    cat >"$bin/docker" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$*" >>"$root/log"
case "\$1" in
    ps)
        if [ -f "$root/ps-status" ]; then
            echo "Cannot connect to the Docker daemon" >&2
            exit "\$(cat "$root/ps-status")"
        fi
        want_config=""; want_project=""
        for a in "\$@"; do
            case "\$a" in
                label=com.docker.compose.project.config_files=*)
                    want_config="\${a#label=com.docker.compose.project.config_files=}" ;;
                label=com.docker.compose.project=*)
                    want_project="\${a#label=com.docker.compose.project=}" ;;
            esac
        done
        # The race window. clean.sh discovers candidates by config_files and
        # then asks, per project, who its members are. Between those calls the
        # host keeps running: containers exit, and a name can be taken by
        # something else. The mutation is applied to the MEMBERSHIP query
        # specifically, because that is the answer the ownership proof rests
        # on -- and because discovery's own "docker inspect" already fails
        # closed if the container it just listed has gone, so a state change
        # before that point never reaches the check under test.
        if [ -n "\$want_project" ]; then
            if [ -f "$root/vanish-before-membership" ]; then
                : >"$root/containers"
            fi
            if [ -f "$root/swap-before-membership" ]; then
                cat "$root/swap-before-membership" >"$root/containers"
            fi
        fi
        while read -r id project config; do
            [ -n "\$id" ] || continue
            [ -z "\$want_config" ] || [ "\$config" = "\$want_config" ] || continue
            [ -z "\$want_project" ] || [ "\$project" = "\$want_project" ] || continue
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
        [ -f "$root/down-status" ] || exit 0
        case "\$*" in *down*) echo "teardown failed" >&2; exit "\$(cat "$root/down-status")" ;; esac
        exit 0
        ;;
esac
exit 0
EOF
    chmod +x "$bin/docker"
}

# run_clean runs clean.sh against a container-modelling stub.
run_clean() {
    CBIN="$(mktemp -d)"; CROOT="$(mktemp -d)"
    make_container_docker "$CBIN" "$CROOT"
    # A trailing newline is required: "read" drops a final line without one,
    # which silently truncated the fixtures and made a mixed project look
    # exclusively ours.
    printf '%s\n' "$1" >"$CROOT/containers"
    shift
    # A bare name writes a flag file; "name=value" writes that value, which is
    # what the container-replacement fixtures need.
    local f
    for f in "$@"; do
        case "$f" in
            *=*) printf '%s\n' "${f#*=}" >"$CROOT/${f%%=*}" ;;
            *)   printf '1\n' >"$CROOT/$f" ;;
        esac
    done

    set +e
    CLEAN_OUT="$(env PATH="$CBIN:$PATH" "$SCRIPT_DIR/clean.sh" 2>&1)"
    local status=$?
    set -e
    return $status
}

downed_projects() {
    grep -oE -- '-p [^ ]+' "$CROOT/log" 2>/dev/null | awk '{print $2}' | sort -u
}

# --- tests ----------------------------------------------------------------

# The core claim: two runs never share a namespace, so neither can tear down
# the other's containers.
test_concurrent_runs_use_distinct_projects() {
    local a="$TMP/a.log" b="$TMP/b.log"
    run_harness "$a" aaa111
    run_harness "$b" bbb222

    local pa pb
    pa="$(projects_in "$a")"
    pb="$(projects_in "$b")"

    # A PREFIX match, because the run id is no longer the whole suffix: the
    # PID follows it, so that two jobs of the SAME workflow run -- which share
    # GITHUB_RUN_ID, as do two attempts of it -- still get separate projects.
    # Asserting the exact string here would have forced that fix to weaken the
    # test rather than the test to check the property.
    case "$pa" in go-netmon-integration-aaa111-*) ;; *) fail "run A used projects: $pa"; return 1 ;; esac
    case "$pb" in go-netmon-integration-bbb222-*) ;; *) fail "run B used projects: $pb"; return 1 ;; esac
    [ "$pa" != "$pb" ] || fail "two runs shared the project $pa" || return 1

    # The assertion that actually encodes the bug: neither run may ever name the
    # other's project, because naming it is what let one delete the other.
    if grep -q -- "-p $pb" "$a"; then
        fail "run A referenced run B's project" || return 1
    fi
    if grep -q -- "-p $pa" "$b"; then
        fail "run B referenced run A's project" || return 1
    fi
}

# GITHUB_RUN_ID is shared by every job in a workflow run and by every re-run
# attempt of it, so it cannot be the whole suffix. Two runs carrying the SAME
# run id -- a matrix leg, a second integration job, or a re-run overlapping a
# still-running first attempt -- must still land in separate projects, or the
# 2026-08-21 adopt-and-tear-down collision reproduces inside one workflow run.
test_same_run_id_still_yields_distinct_projects() {
    local a="$TMP/same-a.log" b="$TMP/same-b.log"
    run_harness "$a" shared999
    run_harness "$b" shared999

    local pa pb
    pa="$(projects_in "$a")"
    pb="$(projects_in "$b")"

    case "$pa" in go-netmon-integration-shared999-*) ;; *) fail "run A project: $pa"; return 1 ;; esac
    [ "$pa" != "$pb" ] \
        || fail "two runs with the same GITHUB_RUN_ID shared the project $pa" || return 1
}

# Without a CI run id the suffix comes from the PID, which must still differ
# between two invocations -- a developer running the suite twice at once on a
# laptop is the same hazard.
test_local_runs_use_distinct_projects() {
    local a="$TMP/local-a.log" b="$TMP/local-b.log"
    run_harness "$a"
    run_harness "$b"

    local pa pb
    pa="$(projects_in "$a")"
    pb="$(projects_in "$b")"

    case "$pa" in go-netmon-integration-*) ;; *) fail "local run A project: $pa"; return 1 ;; esac
    case "$pb" in go-netmon-integration-*) ;; *) fail "local run B project: $pb"; return 1 ;; esac
    [ "$pa" != "$pb" ] || fail "two local runs shared the project $pa" || return 1
}

# A run must never address the bare, shared name the defect used.
test_no_run_uses_the_shared_project_name() {
    local log="$TMP/shared.log"
    run_harness "$log" ccc333

    if grep -qE -- '-p go-netmon-integration( |$)' "$log"; then
        fail "a run addressed the unsuffixed shared project" || return 1
    fi
}

# Teardown must reap the profile-gated dind service. Without --profile the
# privileged container survives "down": eight were found running on one host,
# one per integration run of the day.
test_teardown_removes_the_dind_profile_and_volumes() {
    local log="$TMP/teardown.log"
    run_harness "$log" ddd444

    local down
    down="$(grep -E -- 'compose -p go-netmon-integration-ddd444-[^ ]+ .* down' "$log" || true)"
    [ -n "$down" ] || fail "no teardown was issued" || return 1

    case "$down" in
        *"--profile dind"*) ;;
        *) fail "teardown omits --profile dind, which leaves the privileged container running: $down"; return 1 ;;
    esac
    case "$down" in
        *" -v"*) ;;
        *) fail "teardown omits -v, leaving volumes behind: $down"; return 1 ;;
    esac
    case "$down" in
        *"--remove-orphans"*) ;;
        *) fail "teardown omits --remove-orphans: $down"; return 1 ;;
    esac
}

# make_failing_save_docker writes a stub that behaves like the normal one except
# that "docker save" fails, which models the seed step failing after dind is
# already up -- the case that must degrade to "not covered" rather than to a
# suite that fails on a missing image.
make_failing_save_docker() {
    local bin="$1" log="$2"
    mkdir -p "$bin"
    cat >"$bin/docker" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$*" >>"$log"
case "\$1 \$2" in
    "compose version") echo "Docker Compose version v2.0.0" ;;
esac
[ "\$1" = "save" ] && exit 1
exit 0
EOF
    chmod +x "$bin/docker"
}

# run_harness_with runs run-tests.sh against a named stub factory.
run_harness_with() {
    local factory="$1" log="$2" run_id="$3"
    local bin
    bin="$(mktemp -d)"
    "$factory" "$bin" "$log"
    env PATH="$bin:$PATH" GITHUB_RUN_ID="$run_id" "$RUN_TESTS" >/dev/null 2>&1
    rm -rf "$bin"
}

# The helper image must be BUILT here and LOADED into the sibling daemon, never
# pulled from a registry inside it. dind starts with an empty image store on
# every run, so a registry-hosted helper is a fresh third-party pull every time
# -- #37, where a TLS handshake timeout to auth.docker.io failed the suite for a
# reason unrelated to the code under test.
test_the_helper_image_is_built_and_loaded_into_the_sibling_daemon() {
    local log="$TMP/seed.log"
    run_harness "$log" hhh888

    grep -qE -- '^build .*-f .*/test/integration/Dockerfile\.helper' "$log" \
        || fail "the helper image is never built" || return 1
    grep -qE -- '^save ' "$log" \
        || fail "the helper image is never exported from the host daemon" || return 1
    grep -qE -- 'exec -T dind docker load' "$log" \
        || fail "the helper image is never loaded into the sibling daemon" || return 1

    # And the tests must be told which image to use, or they cannot find it.
    grep -qE -- 'run --rm --build .*NETMON_HELPER_IMAGE=' "$log" \
        || fail "the helper image name is not passed to the tests" || return 1
}

# A daemon that came up but could not be seeded is the same situation as one
# that never came up. It must NOT be offered to the tests: with DOCKER_HOST set
# and no helper image present, every backup test fails on a missing image
# instead of reporting that the coverage was not obtained.
test_a_daemon_that_cannot_be_seeded_is_not_offered_to_the_tests() {
    local log="$TMP/seedfail.log"
    run_harness_with make_failing_save_docker "$log" iii999

    local run
    run="$(grep -E -- 'run --rm --build' "$log" || true)"
    [ -n "$run" ] || fail "the test runner was never invoked" || return 1

    case "$run" in
        *DOCKER_HOST*) fail "a daemon that could not be seeded was still offered to the tests: $run"; return 1 ;;
    esac
    case "$run" in
        *NETMON_HELPER_IMAGE*) fail "a helper image that was never loaded was still named: $run"; return 1 ;;
    esac

    grep -qE -- 'rm -sf dind' "$log" \
        || fail "the unusable privileged container was left running" || return 1
}

# --- the scope of the run -------------------------------------------------

# go_test_command prints the "go test" command line the runner handed to
# compose run, which is the only place the suite's scope is decided.
go_test_command() {
    # -m1, not "| head -1": a log with many matches would SIGPIPE grep and
    # pipefail would fail the function on exactly the line it found (#120).
    grep -m1 -oE 'go test .*' "$1"
}

# tagged_dirs prints every directory holding a file behind //go:build
# integration.
#
# Derived here independently of tagged-packages.sh -- a different traversal, not
# a call to the code under test -- because a test that asks the implementation
# what the answer is agrees with it by construction, including when both are
# wrong.
tagged_dirs() {
    local root file
    root="$(cd "$SCRIPT_DIR/../.." && pwd)"
    while IFS= read -r file; do
        dirname "${file#"$root"/}"
    done < <(find "$root" \( -name .docker-vendor -o -name vendor \) -prune -o -name '*.go' -type f \
        -exec grep -l '^//go:build integration' {} +) | sort -u
}

# The defect this guards: the scope was the literal "./test/integration/...",
# which does not match ./internal/..., so internal/mib/parser's two tagged
# files were never compiled and never run by any target or CI job (#30). A
# package that grows a tagged file and is not run is a suite a reader counts as
# coverage and nothing executes.
test_the_run_covers_every_integration_tagged_package() {
    local log="$TMP/scope.log"
    run_harness "$log" fff666

    local cmd
    cmd="$(go_test_command "$log")"
    [ -n "$cmd" ] || fail "the runner issued no go test command at all" || return 1

    local dir missing=()
    while IFS= read -r dir; do
        case "$cmd" in
            *"./$dir/..."*) ;;
            *) missing+=("$dir") ;;
        esac
    done < <(tagged_dirs)

    if [ ${#missing[@]} -ne 0 ]; then
        fail "these packages hold integration-tagged tests but the run does not include them: ${missing[*]}; command was: $cmd" || return 1
    fi
}

# Pinned separately from the derived check above, so that a discovery which
# silently returns a shorter list still fails here by name. This is the package
# #30 found sitting unrun.
test_the_run_includes_the_mib_parser_suite() {
    local log="$TMP/parser.log"
    run_harness "$log" ggg777

    local cmd
    cmd="$(go_test_command "$log")"
    case "$cmd" in
        *"./internal/mib/parser/..."*) ;;
        *) fail "the MIB parser's integration tests are not in the run: $cmd"; return 1 ;;
    esac
}

# Every compose call must name the project explicitly. One that does not gets
# the directory default -- "go-netmon", a deployed stack's project.
test_every_compose_call_names_the_project() {
    local log="$TMP/explicit.log"
    run_harness "$log" eee555

    # "compose version" is a capability probe, not an operation on a project,
    # so it is the one compose call that legitimately carries no -p.
    local bare
    bare="$(grep -E '^compose ' "$log" \
        | grep -vE '^compose version' \
        | grep -vE -- '-p go-netmon-integration-eee555-[^ ]+' || true)"
    [ -z "$bare" ] || fail "compose calls without an explicit project: $bare" || return 1
}


# --- recovery cleanup -----------------------------------------------------

# The case Codex named: make integration-up can put test services inside the
# project compose infers from the directory -- "go-netmon", the DEPLOYMENT's
# project. Selecting on "holds a container of ours" alone returns it, and
# down -v over it deletes the deployment's volumes.
test_clean_never_tears_down_the_deployment_project() {
    local rows
    rows="$(printf '%s %s %s\n' aaa1 go-netmon "$COMPOSE_FILE"; \
            printf '%s %s %s\n' aaa2 go-netmon /opt/go-netmon/docker-compose.yml)"
    run_clean "$rows" || fail "clean failed on a deployment-shaped project: $CLEAN_OUT" || return 1

    local downed
    downed="$(downed_projects)"
    if [ -n "$downed" ]; then
        fail "clean passed a project to compose down: $downed" || return 1
    fi
    case "$CLEAN_OUT" in
        *"skipping project 'go-netmon'"*) ;;
        *) fail "clean did not report skipping the deployment project: $CLEAN_OUT"; return 1 ;;
    esac
}

test_clean_removes_an_orphaned_run_in_our_namespace() {
    run_clean "$(printf '%s %s %s\n' bbb1 go-netmon-integration-4242 "$COMPOSE_FILE")" \
        || fail "clean failed on our own orphan: $CLEAN_OUT" || return 1

    [ "$(downed_projects)" = "go-netmon-integration-4242" ] \
        || fail "clean did not tear down the orphan; downed: $(downed_projects)" || return 1
    case "$(grep -E -- '-p go-netmon-integration-4242' "$CROOT/log")" in
        *"--profile dind"*) ;;
        *) fail "orphan teardown omits --profile dind, leaving the privileged container"; return 1 ;;
    esac
}

# A project in our namespace that also holds someone else's container is not
# ours to remove: the name matching is necessary but not sufficient.
test_clean_refuses_a_mixed_project_in_our_namespace() {
    local rows
    rows="$(printf '%s %s %s\n' ccc1 go-netmon-integration-9 "$COMPOSE_FILE"; \
            printf '%s %s %s\n' ccc2 go-netmon-integration-9 /elsewhere/docker-compose.yml)"
    run_clean "$rows" || true

    if [ -n "$(downed_projects)" ]; then
        fail "clean tore down a mixed project: $(downed_projects)" || return 1
    fi
    case "$CLEAN_OUT" in
        *"refusing to clean project"*) ;;
        *) fail "clean did not report refusing the mixed project: $CLEAN_OUT"; return 1 ;;
    esac
}

# A discovery failure must not read as "nothing to clean".
test_clean_fails_when_discovery_fails() {
    if run_clean "$(printf '%s %s %s\n' ddd1 go-netmon-integration-1 "$COMPOSE_FILE")" ps-status; then
        fail "clean reported success although docker ps failed" || return 1
    fi
    case "$CLEAN_OUT" in
        *"no integration stacks to clean"*) fail "clean claimed nothing to do after a failed query"; return 1 ;;
    esac
}

# The vacuity case. Discovery finds a candidate; by the time the membership
# query runs, the project is empty. "every member is ours" is then true of
# nothing, and the pre-fix script tore the project down having verified no
# container at all -- taking with it anything that had claimed the name in the
# gap, and the project's volumes.
test_clean_refuses_a_project_that_emptied_after_discovery() {
    run_clean "$(printf '%s %s %s\n' fff1 go-netmon-integration-7 "$COMPOSE_FILE")" \
        vanish-before-membership || true

    if [ -n "$(downed_projects)" ]; then
        fail "clean tore down a project it could not see any container in: $(downed_projects)" || return 1
    fi
    case "$CLEAN_OUT" in
        *"holds no containers now"*) ;;
        *) fail "clean did not report refusing the emptied project: $CLEAN_OUT"; return 1 ;;
    esac
}

# The name-reuse case, which condition 1 alone does not catch: the project is
# not empty, and everything in it does come from our compose file -- but it is
# a DIFFERENT container. The project discovery looked at is gone and this one
# merely inherited its name, so the ownership that was established does not
# transfer to it.
test_clean_refuses_a_project_whose_discovered_container_is_gone() {
    run_clean "$(printf '%s %s %s\n' ggg1 go-netmon-integration-8 "$COMPOSE_FILE")" \
        "swap-before-membership=ggg2 go-netmon-integration-8 $COMPOSE_FILE" || true

    if [ -n "$(downed_projects)" ]; then
        fail "clean tore down a project whose discovered container had gone: $(downed_projects)" || return 1
    fi
    case "$CLEAN_OUT" in
        *"no longer in it"*) ;;
        *) fail "clean did not report the missing anchor container: $CLEAN_OUT"; return 1 ;;
    esac
}

# A teardown failure must surface, not be swallowed.
test_clean_fails_when_teardown_fails() {
    if run_clean "$(printf '%s %s %s\n' eee1 go-netmon-integration-2 "$COMPOSE_FILE")" down-status; then
        fail "clean reported success although the teardown failed" || return 1
    fi
}

# --- runner ---------------------------------------------------------------

main() {
    local filter="${1:-}"

    TMP="$(mktemp -d)"
    trap 'rm -rf "$TMP"' EXIT INT TERM

    [ -x "$RUN_TESTS" ] || { echo "not executable: $RUN_TESTS" >&2; exit 1; }
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

    printf '\nintegration harness: %d passed, %d failed\n' "$PASS" "$FAIL"
    if [ "$FAIL" -gt 0 ]; then
        printf 'failed:\n'
        printf '  %s\n' "${FAILED_TESTS[@]}"
        return 1
    fi
}

main "$@"
