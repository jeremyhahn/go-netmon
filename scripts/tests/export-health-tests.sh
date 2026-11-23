#!/usr/bin/env bash
# Tests for scripts/measure-export-health.sh.
#
# They exercise the parsing and the judging only. Nothing here contacts a
# metrics endpoint, an API, or a deployed appliance: the fixtures are captured
# exposition text of the two shapes that matter, a build that publishes the
# per-reason counters and one that does not.
#
# The property worth most: a counter the build does not publish must read as
# ABSENT and never as zero. A harness that reports 0 for a counter that is not
# there would say a deploy succeeded when it had not, which is the failure
# family the measurement exists to close.
#
# Usage: export-health-tests.sh [test-name-substring]

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPTS_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
MEASURE="$SCRIPTS_DIR/measure-export-health.sh"
FIXTURES="$SCRIPT_DIR/fixtures"

PASS=0
FAIL=0
FAILED_TESTS=()

fail() {
    printf '    FAIL: %s\n' "$*" >&2
    return 1
}

assert_eq() {
    [ "$2" = "$3" ] || fail "$1: expected '$3', got '$2'"
}

assert_ok() {
    local desc="$1"
    shift
    "$@" >/dev/null 2>&1 || fail "$desc: expected success, got exit $?"
}

assert_contains() {
    if [ -z "$3" ]; then
        fail "$1: an empty needle is contained by every string, so this could never fail"
        return 1
    fi
    case "$2" in
    *"$3"*) : ;;
    *) fail "$1: output did not contain '$3'
--- output ---
$2
--------------" ;;
    esac
}

assert_not_contains() {
    if [ -z "$3" ]; then
        fail "$1: an empty needle is contained by every string"
        return 1
    fi
    case "$2" in
    *"$3"*) fail "$1: output should not have contained '$3'
--- output ---
$2
--------------" ;;
    *) : ;;
    esac
}

# The functions under test are sourced rather than re-implemented, so a change
# to the script is a change to what these assert.
# shellcheck source=/dev/null
load_measure() {
    # main() runs on source unless argv makes it print usage and exit, so the
    # script body is taken up to main's invocation instead.
    local body
    body="$(sed '/^main "\$@"$/d' "$MEASURE")"
    eval "$body"
}

# --- parsing --------------------------------------------------------------

test_a_published_counter_reads_its_value() {
    load_measure
    local text
    text="$(cat "$FIXTURES/metrics-new-build.txt")"
    assert_eq "superseded" \
        "$(metric_value "$text" 'gonetmon_export_dropped_samples_total{reason="superseded"}')" "4" || return 1
    assert_eq "repaired" \
        "$(metric_value "$text" 'gonetmon_export_repaired_order_total')" "11" || return 1
}

test_a_counter_the_build_does_not_publish_reads_absent_not_zero() {
    load_measure
    local text value
    text="$(cat "$FIXTURES/metrics-old-build.txt")"

    value="$(metric_value "$text" 'gonetmon_export_dropped_samples_total{reason="superseded"}')"
    assert_eq "superseded on the old build" "$value" "absent" || return 1
    # The point of the test: absent must not be spelled the same as a real zero,
    # or a harness reports a deploy that did not happen as a success.
    [ "$value" != "0" ] || fail "an absent counter read as 0"

    assert_eq "out_of_order on the old build" \
        "$(metric_value "$text" 'gonetmon_export_out_of_order_total')" "absent" || return 1
}

test_a_zero_counter_is_zero_not_absent() {
    load_measure
    local text
    text="$(cat "$FIXTURES/metrics-new-build.txt")"
    # A published counter sitting at zero is a fact; conflating it with absent
    # would lose the distinction in the other direction.
    assert_eq "aged" \
        "$(metric_value "$text" 'gonetmon_export_dropped_samples_total{reason="aged"}')" "0" || return 1
}

# --- deltas ---------------------------------------------------------------

test_a_delta_across_two_present_readings_is_the_difference() {
    load_measure
    assert_eq "delta" "$(delta 4 11)" "7" || return 1
}

test_a_delta_from_absent_says_appeared_rather_than_inventing_a_baseline() {
    load_measure
    local d
    d="$(delta absent 11)"
    assert_contains "appeared" "$d" "appeared" || return 1
    # 11 is not a delta: the baseline is unknown, not zero.
    [ "$d" != "11" ] || fail "an appeared counter was reported as a delta from zero"
}

test_a_delta_to_absent_stays_absent() {
    load_measure
    # The counter went away, which after a deploy means the wrong build.
    assert_eq "delta to absent" "$(delta 4 absent)" "absent" || return 1
}

# --- judging --------------------------------------------------------------

test_a_counter_that_must_stay_flat_and_did_matches() {
    load_measure
    local out
    out="$(judge aged 0 zero 'must not move')"
    assert_contains "flat" "$out" "MATCH" || return 1
}

test_a_counter_that_must_stay_flat_and_moved_is_unexpected() {
    load_measure
    local out
    out="$(judge aged 12 zero 'must not move')"
    assert_contains "moved" "$out" "UNEXPECTED" || return 1
}

test_an_absent_counter_is_never_a_match() {
    load_measure
    local out
    # This is the deploy check: after deploying, absent means the wrong binary
    # is running, and it must not pass because "0 == 0".
    out="$(judge superseded absent zero 'must not move')"
    assert_contains "absent" "$out" "UNEXPECTED" || return 1
    assert_contains "absent explains itself" "$out" "does not publish"
}

test_out_of_order_matching_superseded_is_expected() {
    load_measure
    local out
    out="$(judge out_of_order 4 matches:4 'flat, or equal to superseded')"
    assert_contains "matching" "$out" "MATCH" || return 1
}

test_out_of_order_exceeding_superseded_is_unexpected() {
    load_measure
    local out
    # Samples the endpoint would refuse that were not also dropped means
    # something was sent that should not have been.
    out="$(judge out_of_order 9 matches:4 'flat, or equal to superseded')"
    assert_contains "mismatch" "$out" "UNEXPECTED" || return 1
}

# --- preflight: the checks that stop a failed deploy reading as a pass ------

test_a_missing_series_aborts_the_post_deploy_arm() {
    load_measure
    local out
    # The old build published none of the per-reason counters. Every
    # expectation would pass against it -- "must not move" is satisfied by a
    # series that is not there -- so the arm has to stop before evaluating one.
    # shellcheck disable=SC2317,SC2329  # invoked indirectly, through assert_counters_present
    fetch_metrics() { cat "$FIXTURES/metrics-old-build.txt"; }
    if out="$(assert_counters_present 2>&1)"; then
        fail "assert_counters_present accepted a build with no per-reason counters"
        return 1
    fi
    assert_contains "names what is missing" "$out" "ABSENT" || return 1
    assert_contains "says what it means" "$out" "old build is running" || return 1
}

test_every_series_present_passes_the_preflight() {
    load_measure
    # shellcheck disable=SC2317,SC2329  # invoked indirectly, through assert_counters_present
    fetch_metrics() { cat "$FIXTURES/metrics-new-build.txt"; }
    assert_ok "the new build publishes every expected series" assert_counters_present || return 1
}

test_a_running_image_that_is_not_the_deployed_one_aborts() {
    load_measure
    local out
    EXPECT_SHA256="0000000000000000000000000000000000000000000000000000000000000000"
    # shellcheck disable=SC2317,SC2329  # invoked indirectly, through assert_build
    running_image_sha() { printf 'ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff\n'; }
    if out="$(assert_build 2>&1)"; then
        fail "assert_build accepted an image that is not the deployed one"
        return 1
    fi
    assert_contains "aborts" "$out" "not the one deployed" || return 1
}

test_an_unreadable_running_image_aborts_rather_than_assuming() {
    load_measure
    local out
    EXPECT_SHA256="0000000000000000000000000000000000000000000000000000000000000000"
    # shellcheck disable=SC2317,SC2329  # invoked indirectly, through assert_build
    running_image_sha() { printf '%s\n' "$ABSENT"; }
    if out="$(assert_build 2>&1)"; then
        fail "assert_build passed when it could not read the running image"
        return 1
    fi
    assert_contains "says it could not verify" "$out" "cannot be verified" || return 1
}

test_a_matching_image_passes_and_says_so() {
    load_measure
    local out
    EXPECT_SHA256="abc123"
    # shellcheck disable=SC2317,SC2329  # invoked indirectly, through assert_build
    running_image_sha() { printf 'abc123\n'; }
    out="$(assert_build 2>&1)" || fail "assert_build rejected the deployed image"
    assert_contains "confirms" "$out" "matches EXPECT_SHA256" || return 1
}

test_an_unset_expectation_says_the_build_was_not_verified() {
    load_measure
    local out
    # shellcheck disable=SC2034  # read by assert_build, which is sourced
    EXPECT_SHA256=""
    # shellcheck disable=SC2317,SC2329  # invoked indirectly, through assert_build
    running_image_sha() { printf 'abc123\n'; }
    out="$(assert_build 2>&1)" || fail "assert_build failed with no expectation set"
    # Not silently a pass: it has to say that nothing was checked, or an
    # operator reads its absence as verification.
    assert_contains "says so" "$out" "NOT verified" || return 1
}

# --- the harness must not touch the target ---------------------------------

# drive_edit_body is the one function that changes anything on the target.
drive_edit_body() {
    awk '/^drive_edit\(\) \{/,/^\}/' "$MEASURE"
}

test_every_mutating_request_targets_the_devices_own_assignment() {
    # A measurement that edits something other than the device it is measuring
    # is not a measurement. The URLs are checked rather than the -X flags,
    # because the method and the URL sit on different lines of a wrapped curl.
    local urls bad
    # Single-quoted deliberately: these patterns are matched against the
    # script's SOURCE TEXT, where "$API_URL" is the literal characters.
    # shellcheck disable=SC2016
    urls="$(drive_edit_body | grep -oE '"\$API_URL[^"]*"' | sort -u)"
    [ -n "$urls" ] || fail "no request URLs found in drive_edit; the extraction is wrong"

    # shellcheck disable=SC2016  # source text again, not an expansion
    bad="$(printf '%s\n' "$urls" | grep -v 'api/devices/\$DEVICE_ID/mibs' || true)"
    assert_eq "requests outside the device's own MIB assignment" "$bad" "" || return 1
}

test_nothing_outside_drive_edit_makes_a_mutating_request() {
    # The rest of the script reads. If a POST, PUT or DELETE appears outside
    # drive_edit, something other than the edit is changing the target.
    local outside
    outside="$(grep -c -- '-X \(POST\|PUT\|DELETE\)' "$MEASURE")"
    local inside
    inside="$(drive_edit_body | grep -c -- '-X \(POST\|PUT\|DELETE\)')"
    assert_eq "mutating requests outside drive_edit" "$((outside - inside))" "0" || return 1
}

test_the_only_file_the_script_writes_is_its_state_file() {
    # Every redirection into a file must name STATE_FILE. Matching on the
    # redirection spelling alone catches "->" in printf and ">=" in awk, which
    # is how the first version of this test failed on its own script.
    local writes
    writes="$(grep -oE '>[[:space:]]*"?\$[A-Za-z_]+' "$MEASURE" | sort -u)"
    local bad
    bad="$(printf '%s\n' "$writes" | grep -v 'STATE_FILE' | grep -v '^$' || true)"
    assert_eq "redirections to something other than the state file" "$bad" "" || return 1
}

# --- runner ---------------------------------------------------------------

run_test() {
    local name="$1"
    printf '  %s\n' "${name#test_}"
    if (
        set -uo pipefail
        "$name"
    ); then
        PASS=$((PASS + 1))
    else
        FAIL=$((FAIL + 1))
        FAILED_TESTS+=("$name")
    fi
}

main() {
    local filter="${1:-}" name
    [ -f "$MEASURE" ] || {
        printf 'not found: %s\n' "$MEASURE" >&2
        exit 1
    }

    printf 'export-health measurement tests\n\n'
    while read -r name; do
        if [ -n "$filter" ]; then
            case "$name" in *"$filter"*) ;; *) continue ;; esac
        fi
        run_test "$name"
    done < <(declare -F | awk '{print $3}' | grep '^test_' | sort)

    printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
    if [ "$FAIL" -gt 0 ]; then
        printf 'failed: %s\n' "${FAILED_TESTS[*]}"
        exit 1
    fi
}

main "$@"
