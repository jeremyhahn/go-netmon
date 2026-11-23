#!/usr/bin/env bash
# Tests for scripts/e2e-target-guard.sh (#111).
#
# Each test builds a throwaway e2e tree and runs the guard against it, so a
# case is a statement about the rule rather than about today's specs. One test
# runs it against this repository, which is what keeps the real suite honest.
#
# Usage: e2e-target-guard-tests.sh [filter]

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
GUARD="$REPO_ROOT/scripts/e2e-target-guard.sh"

PASS=0
FAIL=0
FAILED_TESTS=()
OUT=""
STATUS=0
WORK=""

fail() {
    printf '    FAIL: %s\n' "$*" >&2
    return 1
}

# new_tree makes a minimal suite: a target.ts on the disposable stack and one
# spec that imports it. Tests then add or change what they are about.
new_tree() {
    WORK="$(mktemp -d)"
    mkdir -p "$WORK/tests/e2e/tests"
    printf "export const DISPOSABLE_URL = 'http://localhost:8083';\nexport const BASE = process.env.NETMON_URL || DISPOSABLE_URL;\n" \
        >"$WORK/tests/e2e/target.ts"
    printf "import { BASE } from '../target';\n" >"$WORK/tests/e2e/tests/a.spec.ts"
}

run_guard() {
    OUT="$("$GUARD" "$1" 2>&1)"
    STATUS=$?
}

expect_status() {
    local want="$1" what="$2"
    if [ "$STATUS" -ne "$want" ]; then
        fail "$what: exit $STATUS, want $want; output: $OUT"
        return 1
    fi
}

expect_output() {
    case "$OUT" in
        *"$1"*) ;;
        *) fail "output does not mention '$1': $OUT"; return 1 ;;
    esac
}

test_this_repository_passes() {
    run_guard "$REPO_ROOT"
    expect_status 0 "the repository's own e2e suite"
}

test_a_suite_on_the_disposable_stack_passes() {
    new_tree
    printf "await page.goto('http://localhost:8083/devices');\n" >>"$WORK/tests/e2e/tests/a.spec.ts"
    run_guard "$WORK"
    expect_status 0 "a URL on the disposable stack"
}

test_the_production_address_anywhere_fails() {
    new_tree
    printf "// try 198.51.100.50 if it is slow\n" >>"$WORK/tests/e2e/tests/a.spec.ts"
    run_guard "$WORK"
    expect_status 1 "production's address in a comment" || return 1
    expect_output "a.spec.ts:2 names production"
}

test_a_production_default_fails() {
    new_tree
    printf "const BASE = process.env.NETMON_URL || 'http://198.51.100.50:8080';\n" \
        >"$WORK/tests/e2e/tests/b.spec.ts"
    run_guard "$WORK"
    expect_status 1 "a spec defaulting to production" || return 1
    expect_output "b.spec.ts:1 names production"
}

test_the_production_hostname_fails() {
    new_tree
    printf "const BASE = process.env.NETMON_URL || 'http://monitor-01:8080';\n" >"$WORK/tests/e2e/tests/b.spec.ts"
    run_guard "$WORK"
    expect_status 1 "a spec defaulting to http://monitor-01" || return 1
    expect_output "names production"
}

test_a_comment_mentioning_monitor01_passes() {
    new_tree
    printf "// Observed on monitor-01 after the first deploy.\n" >>"$WORK/tests/e2e/tests/a.spec.ts"
    run_guard "$WORK"
    expect_status 0 "prose naming monitor-01, with no address"
}

test_any_other_stack_fails() {
    new_tree
    printf "const BASE = process.env.NETMON_URL || 'http://198.51.100.91:8082';\n" \
        >"$WORK/tests/e2e/tests/b.spec.ts"
    run_guard "$WORK"
    expect_status 1 "a default that is not the disposable stack" || return 1
    expect_output "b.spec.ts:1 names http://198.51.100.91:8082"
}

test_another_port_on_localhost_fails() {
    new_tree
    printf "const BASE = 'http://localhost:8082';\n" >"$WORK/tests/e2e/tests/b.spec.ts"
    run_guard "$WORK"
    expect_status 1 "a local stack other than the disposable one"
}

test_the_config_file_is_checked_too() {
    new_tree
    printf "export default { use: { baseURL: 'http://198.51.100.91:9090' } };\n" \
        >"$WORK/tests/e2e/playwright.config.ts"
    run_guard "$WORK"
    expect_status 1 "playwright.config.ts naming another stack" || return 1
    expect_output "playwright.config.ts:1"
}

test_node_modules_is_not_scanned() {
    new_tree
    mkdir -p "$WORK/tests/e2e/node_modules/x"
    printf "const u = 'https://example.com';\n" >"$WORK/tests/e2e/node_modules/x/index.js"
    run_guard "$WORK"
    expect_status 0 "installed packages naming their own URLs"
}

test_the_exempt_specs_may_keep_only_the_old_disposable_stack() {
    new_tree
    printf "const BASE = process.env.NETMON_URL || 'http://198.51.100.91:8082';\n" \
        >"$WORK/tests/e2e/tests/ui-scale.spec.ts"
    run_guard "$WORK"
    expect_status 0 "an exempt spec on 198.51.100.91:8082" || return 1

    printf "const BASE = process.env.NETMON_URL || 'http://198.51.100.91:9999';\n" \
        >"$WORK/tests/e2e/tests/ui-scale.spec.ts"
    run_guard "$WORK"
    expect_status 1 "an exempt spec on any other address" || return 1

    printf "const BASE = process.env.NETMON_URL || 'http://198.51.100.50:8080';\n" \
        >"$WORK/tests/e2e/tests/ui-scale.spec.ts"
    run_guard "$WORK"
    expect_status 1 "an exempt spec defaulting to production"
}

test_a_missing_target_fails() {
    new_tree
    rm "$WORK/tests/e2e/target.ts"
    run_guard "$WORK"
    expect_status 1 "no target.ts" || return 1
    expect_output "target.ts is missing"
}

test_a_target_that_is_not_local_fails() {
    new_tree
    printf "export const DISPOSABLE_URL = 'http://198.51.100.50:8080';\n" >"$WORK/tests/e2e/target.ts"
    run_guard "$WORK"
    expect_status 1 "DISPOSABLE_URL on production" || return 1
    expect_output "not a local stack"
}

test_an_unreadable_suite_is_not_a_pass() {
    WORK="$(mktemp -d)"
    run_guard "$WORK"
    expect_status 2 "a root with no tests/e2e" || return 1

    mkdir -p "$WORK/tests/e2e"
    run_guard "$WORK"
    expect_status 2 "a suite with no sources"
}

main() {
    local filter="${1:-}" t
    [ -x "$GUARD" ] || { echo "not executable: $GUARD" >&2; exit 1; }

    local tests
    mapfile -t tests < <(declare -F | cut -d' ' -f3 | grep '^test_' | sort)

    for t in "${tests[@]}"; do
        if [ -n "$filter" ] && [[ "$t" != *"$filter"* ]]; then
            continue
        fi
        printf '  %s\n' "$t"
        WORK=""
        if "$t"; then
            PASS=$((PASS + 1))
        else
            FAIL=$((FAIL + 1))
            FAILED_TESTS+=("$t")
        fi
        [ -n "$WORK" ] && rm -rf "$WORK"
    done

    printf '\ne2e target guard: %d passed, %d failed\n' "$PASS" "$FAIL"
    if [ "$FAIL" -gt 0 ]; then
        printf 'failed:\n'
        printf '  %s\n' "${FAILED_TESTS[@]}"
        return 1
    fi
}

main "$@"
