#!/usr/bin/env bash
# Usage: teardown-tests.sh [test-name-substring] -- tests run-tests.sh's cleanup trap tears down the dind profile, against a stub docker.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUN_TESTS="$SCRIPT_DIR/run-tests.sh"

PASS=0
FAIL=0
FAILED_TESTS=()

# make_fake_docker writes a stub CLI logging each invocation to $2/log.
#
# $2/run-status, if present, is the exit code `compose run` returns -- which is
# how the failing-suite path is exercised: run-tests.sh records it in
# TEST_EXIT_CODE and still has to tear down on the way out.
make_fake_docker() {
    local bin="$1" root="$2"
    mkdir -p "$bin" "$root"
    : >"$root/log"
    cat >"$bin/docker" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$*" >>"$root/log"
case "\$1 \$2" in
    "compose version") echo "Docker Compose version v2.0.0"; exit 0 ;;
esac
case "\$*" in
    *" run "*)
        [ -f "$root/run-status" ] && exit "\$(cat "$root/run-status")"
        ;;
esac
exit 0
EOF
    chmod +x "$bin/docker"
}

# run_harness runs the script and leaves its docker invocations in $LOG.
# Returns the script's exit status.
run_harness() {
    BIN="$(mktemp -d)"
    ROOT="$(mktemp -d)"
    make_fake_docker "$BIN" "$ROOT"
    LOG="$ROOT/log"
    if [ -n "${1-}" ]; then
        printf '%s\n' "$1" >"$ROOT/run-status"
    fi

    set +e
    env PATH="$BIN:$PATH" "$RUN_TESTS" >/dev/null 2>&1
    local status=$?
    set -e
    return $status
}

fail() {
    printf '    FAIL: %s\n' "$*" >&2
    return 1
}

# assert_teardown checks the single teardown line for the flags that decide
# what survives it.
assert_teardown() {
    local down
    down="$(grep -E -- 'compose .* down' "$LOG" || true)"
    [ -n "$down" ] || fail "no teardown was issued at all" || return 1

    case "$down" in
        *"--profile dind"*) ;;
        *)
            fail "teardown omits --profile dind, so the privileged dind container survives it: $down"
            return 1
            ;;
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

# --- tests ----------------------------------------------------------------

# A fully passing run must still tear down.
test_teardown_reaps_the_dind_profile_after_a_passing_run() {
    run_harness || fail "the harness failed against the stub" || return 1
    assert_teardown
}

# And the path a developer is more likely to be watching: the suite failed, the
# script exits non-zero, and the stack must still go.
test_teardown_reaps_the_dind_profile_after_a_failing_run() {
    if run_harness 1; then
        fail "the harness reported success although the suite failed" || return 1
    fi
    assert_teardown
}

# The teardown is issued once despite traps on EXIT, INT and TERM.
test_teardown_is_issued_once() {
    run_harness || fail "the harness failed against the stub" || return 1

    local count
    count="$(grep -cE -- 'compose .* down' "$LOG" || true)"
    [ "$count" -eq 1 ] || fail "teardown was issued $count times, want exactly 1" || return 1
}

# --- runner ---------------------------------------------------------------

main() {
    local filter="${1:-}"

    [ -x "$RUN_TESTS" ] || { echo "not executable: $RUN_TESTS" >&2; exit 1; }

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

    printf '\nteardown: %d passed, %d failed\n' "$PASS" "$FAIL"
    if [ "$FAIL" -gt 0 ]; then
        printf 'failed:\n'
        printf '  %s\n' "${FAILED_TESTS[@]}"
        return 1
    fi
}

main "$@"
