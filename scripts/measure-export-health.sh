#!/usr/bin/env bash
# Measure what a device edit does to the exporter's health counters, and judge
# the result against docs/operations/export-health.md.
#
# The expectations are written down BEFORE the run and this script prints
# observed against expected, line by line, marking each. That is the whole
# point: nearly every measurement fault in go-netmon#69 was a number that
# looked plausible and let its reader supply the meaning. A run that cannot
# come out wrong is not a measurement.
#
# It changes nothing on the target except the device edit it drives, and it
# restores that: the MIB assignment it creates is deleted again, and the
# assignment endpoint is checked to be back where it started.
#
# A counter that is ABSENT is reported as absent, never as zero. An older build
# does not publish the per-reason counters at all, and reading those as 0 would
# report a successful deploy that had not happened -- the exact failure family
# this measurement exists to close.
#
# Usage:
#   measure-export-health.sh baseline          # before the deploy
#   measure-export-health.sh edit              # after: drive the edit and judge
#
# Environment:
#   METRICS_URL   default http://127.0.0.1:9100/metrics
#   API_URL       default http://127.0.0.1:8080
#   DEVICE_ID     default 3        (pdu-03: small, answers in milliseconds)
#   MIB_ID        default 171
#   STATE_FILE    default ${TMPDIR:-/tmp}/measure-export-health.state
#   UNIT          default go-netmon        (whose running image is identified)
#   EXPECT_SHA256 optional: the sha256 of the binary that was deployed. When
#                 set, the edit arm refuses to measure anything unless the
#                 RUNNING image matches it.

set -uo pipefail

METRICS_URL="${METRICS_URL:-http://127.0.0.1:9100/metrics}"
API_URL="${API_URL:-http://127.0.0.1:8080}"
DEVICE_ID="${DEVICE_ID:-3}"
MIB_ID="${MIB_ID:-171}"
STATE_FILE="${STATE_FILE:-${TMPDIR:-/tmp}/measure-export-health.state}"
UNIT="${UNIT:-go-netmon}"
EXPECT_SHA256="${EXPECT_SHA256:-}"

# ABSENT is what a counter reads when the build does not publish it. It is not
# a number and no arithmetic is done on it; every comparison checks for it
# first, so a missing counter can only ever be reported as missing.
ABSENT="absent"

# --- what is actually running ---------------------------------------------

# running_image_sha prints the sha256 of the image the service is EXECUTING,
# read through /proc/<pid>/exe rather than from the path on disk.
#
# The distinction is the point: a deploy that copied a new binary but failed to
# restart leaves the right file on disk and the old code running, and only the
# executing image can tell those apart. Prints ABSENT when it cannot be read,
# never a guess.
running_image_sha() {
    local pid exe
    pid="$(systemctl show "$UNIT" -p ExecMainPID --value 2>/dev/null)" || true
    case "$pid" in
    '' | 0) printf '%s\n' "$ABSENT"; return ;;
    esac
    exe="/proc/$pid/exe"
    [ -r "$exe" ] || { printf '%s\n' "$ABSENT"; return; }
    sha256sum "$exe" 2>/dev/null | awk '{print $1}' | grep . || printf '%s\n' "$ABSENT"
}

# assert_build refuses to measure anything unless the running image is the one
# that was deployed.
#
# Counters cannot answer this. "The new code ran and nothing moved" and "the new
# code was never there" produce identical counter movements -- none -- and the
# second is the one that would have this harness certify a deploy that did not
# happen.
assert_build() {
    local sha
    sha="$(running_image_sha)"
    printf 'running image: %s (unit %s)\n' "$sha" "$UNIT"

    if [ -z "$EXPECT_SHA256" ]; then
        printf '  EXPECT_SHA256 is unset, so the build was NOT verified.\n'
        printf '  Set it to the sha256 of what you deployed to make this a check.\n'
        return 0
    fi
    if [ "$sha" = "$ABSENT" ]; then
        printf '  ABORT: could not read the running image; the build cannot be verified.\n' >&2
        return 1
    fi
    if [ "$sha" != "$EXPECT_SHA256" ]; then
        printf '  ABORT: the running image is not the one deployed.\n' >&2
        printf '         expected %s\n         running  %s\n' "$EXPECT_SHA256" "$sha" >&2
        return 1
    fi
    printf '  matches EXPECT_SHA256.\n'
}

# assert_counters_present refuses to measure unless every series exists.
#
# After a deploy, an absent counter is not "it did not move" -- it is the wrong
# binary. Evaluating expectations against absent counters passes every one of
# them, because "must not move" is satisfied by a series that is not there, and
# the harness would certify a node that was never updated.
#
# THAT INFERENCE ONLY HOLDS BECAUSE THE EXPORTER PRE-CREATES EVERY LABELLED
# CHILD. It did not, and this check was wrong on the first node it ever ran
# against: monitor-01 published the right binary hash and no
# dropped_samples_total{reason=...} series at all, because a CounterVec child
# does not exist until something increments it and nothing had been dropped
# since the restart. Two of this script's own signals contradicted each other.
#
# The fix went into the exporter rather than here, because absence-means-zero
# is the weaker property: it would have made this script tolerant while leaving
# an alert on the drop counters unable to fire on a fresh process, rate() over
# a series that does not exist being empty rather than 0. With every child
# published at 0 from the first scrape, absence has exactly one cause again and
# the abort below says something true.
assert_counters_present() {
    local text missing=0 name key value
    text="$(fetch_metrics)" || {
        printf 'could not read %s\n' "$METRICS_URL" >&2
        return 1
    }
    while read -r name key; do
        value="$(metric_value "$text" "$key")"
        if [ "$value" = "$ABSENT" ]; then
            printf '  ABSENT: %s\n' "$key" >&2
            missing=$((missing + 1))
        fi
    done < <(metric_keys)

    if [ "$missing" -gt 0 ]; then
        printf '  ABORT: %d expected series are missing. Every one of these publishes 0\n' "$missing" >&2
        printf '         from the first scrape, so after a deploy the old build is running,\n' >&2
        printf '         not that nothing moved.\n' >&2
        return 1
    fi
    printf 'every expected series is published.\n'
}

# --- reading metrics ------------------------------------------------------

# metric_value <exposition-text> <metric-with-labels>
# Prints the value, or ABSENT if the exposition does not carry that series.
metric_value() {
    local text="$1" key="$2"
    printf '%s\n' "$text" | awk -v key="$key" -v absent="$ABSENT" '
        # Defensive, not load-bearing: "$1 == key" already cannot match a
        # comment, whose first field is "#". It is here so that loosening the
        # match later does not silently start reading "counter" as a value.
        /^#/ { next }
        $1 == key { print $2; found = 1; exit }
        END { if (!found) print absent }
    '
}

# The counters this measurement reads, in the order they are reported.
metric_keys() {
    cat <<'KEYS'
dropped_aged gonetmon_export_dropped_samples_total{reason="aged"}
dropped_depth gonetmon_export_dropped_samples_total{reason="depth"}
dropped_rejected gonetmon_export_dropped_samples_total{reason="rejected"}
dropped_superseded gonetmon_export_dropped_samples_total{reason="superseded"}
dropped_shutdown gonetmon_export_dropped_samples_total{reason="shutdown"}
out_of_order gonetmon_export_out_of_order_total
repaired gonetmon_export_repaired_order_total
buffered gonetmon_export_buffered_samples
failures gonetmon_export_consecutive_failures
KEYS
}

fetch_metrics() {
    curl -sf --max-time 20 "$METRICS_URL"
}

# snapshot prints "name value" lines for every counter, from one fetch.
snapshot() {
    local text name key
    text="$(fetch_metrics)" || {
        printf 'could not read %s\n' "$METRICS_URL" >&2
        return 1
    }
    while read -r name key; do
        printf '%s %s\n' "$name" "$(metric_value "$text" "$key")"
    done < <(metric_keys)
}

saved() { awk -v k="$1" '$1 == k { print $2 }' "$STATE_FILE"; }

# delta prints the change, or ABSENT if either end is absent. A counter that
# appeared between the two readings is reported as "appeared" rather than as a
# number, because its baseline is unknown rather than zero.
delta() {
    local before="$1" after="$2"
    if [ "$after" = "$ABSENT" ]; then printf '%s\n' "$ABSENT"; return; fi
    if [ "$before" = "$ABSENT" ]; then printf 'appeared(%s)\n' "$after"; return; fi
    awk -v a="$before" -v b="$after" 'BEGIN { printf "%g", b - a }'
}

# --- judging --------------------------------------------------------------

PASS_COUNT=0
FAIL_COUNT=0

# judge <label> <observed-delta> <rule> <expectation-text>
# rule is one of: zero, non-negative, matches:<other-delta>
judge() {
    local label="$1" observed="$2" rule="$3" expectation="$4" ok=0

    case "$observed" in
    "$ABSENT")
        # Absent is never silently a pass. On the old build it is expected and
        # says the deploy has not happened; after the deploy it is a fault.
        ok=0
        observed="absent (this build does not publish it)"
        ;;
    appeared*) ok=1 ;;
    *)
        case "$rule" in
        zero) awk -v v="$observed" 'BEGIN { exit !(v == 0) }' && ok=1 ;;
        non-negative) awk -v v="$observed" 'BEGIN { exit !(v >= 0) }' && ok=1 ;;
        matches:*)
            local other="${rule#matches:}"
            [ "$other" = "$ABSENT" ] || awk -v a="$observed" -v b="$other" 'BEGIN { exit !(a == b) }' && ok=1
            ;;
        esac
        ;;
    esac

    if [ "$ok" = "1" ]; then
        PASS_COUNT=$((PASS_COUNT + 1))
        printf '  MATCH     %-20s %-28s %s\n' "$label" "$observed" "$expectation"
    else
        FAIL_COUNT=$((FAIL_COUNT + 1))
        printf '  UNEXPECTED %-19s %-28s %s\n' "$label" "$observed" "$expectation"
    fi
}

# --- driving the edit -----------------------------------------------------

drive_edit() {
    local code i enabled
    printf '=== driving the edit: MIB %s on device %s, the path the e2e suite takes ===\n' \
        "$MIB_ID" "$DEVICE_ID"

    code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 25 -X POST \
        -H 'Content-Type: application/json' -d "{\"mib_id\": $MIB_ID}" \
        "$API_URL/api/devices/$DEVICE_ID/mibs")"
    printf '  create assignment -> %s\n' "$code"

    for i in 1 2 3 4 5 6; do
        if [ $((i % 2)) -eq 1 ]; then enabled=true; else enabled=false; fi
        curl -s -o /dev/null --max-time 25 -X PUT -H 'Content-Type: application/json' \
            -d "{\"enabled\": $enabled}" \
            "$API_URL/api/devices/$DEVICE_ID/mibs/$MIB_ID/assignment"
        sleep 1
    done

    code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 25 -X DELETE \
        "$API_URL/api/devices/$DEVICE_ID/mibs/$MIB_ID")"
    printf '  remove assignment -> %s\n' "$code"

    sleep 2
    code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 20 \
        "$API_URL/api/devices/$DEVICE_ID/mibs/$MIB_ID/assignment")"
    printf '  restored: assignment endpoint -> %s (404 is as found)\n' "$code"
    if [ "$code" != "404" ]; then
        printf '  WARNING: the device was not left as it was found\n' >&2
    fi
}

# --- commands -------------------------------------------------------------

cmd_baseline() {
    snapshot >"$STATE_FILE" || return 1
    printf 'baseline at %s\n\n' "$(date -u '+%Y-%m-%d %H:%M:%S UTC')"
    cat "$STATE_FILE"
    printf '\nsaved to %s -- deploy, then: %s edit\n' "$STATE_FILE" "$0"
}

cmd_edit() {
    [ -f "$STATE_FILE" ] || {
        printf 'no baseline at %s; run "%s baseline" first\n' "$STATE_FILE" "$0" >&2
        return 2
    }

    # Both preflights run BEFORE anything is measured or driven. A measurement
    # taken against the wrong build, or against counters that do not exist, is
    # worse than no measurement: it produces a full set of passes.
    printf '=== preflight ===\n'
    assert_build || return 1
    assert_counters_present || return 1
    printf '\n'

    drive_edit
    printf '\n'

    local after name value
    after="$(snapshot)" || return 1

    local d_aged d_depth d_rejected d_superseded d_ooo d_repaired
    while read -r name value; do
        case "$name" in
        dropped_aged) d_aged="$(delta "$(saved "$name")" "$value")" ;;
        dropped_depth) d_depth="$(delta "$(saved "$name")" "$value")" ;;
        dropped_rejected) d_rejected="$(delta "$(saved "$name")" "$value")" ;;
        dropped_superseded) d_superseded="$(delta "$(saved "$name")" "$value")" ;;
        out_of_order) d_ooo="$(delta "$(saved "$name")" "$value")" ;;
        repaired) d_repaired="$(delta "$(saved "$name")" "$value")" ;;
        esac
    done <<<"$after"

    printf '=== judged against docs/operations/export-health.md, written before this run ===\n'
    judge aged "$d_aged" zero 'must not move; a clock step would move it'
    judge depth "$d_depth" zero 'must not move; backpressure would move it'
    judge rejected "$d_rejected" zero 'must not move; unacceptable samples are removed before the write'
    judge superseded "$d_superseded" non-negative 'may be non-zero -- the fix working: one sample, not its batch'
    judge out_of_order "$d_ooo" "matches:$d_superseded" 'flat, or equal to superseded'
    judge repaired "$d_repaired" non-negative 'may rise: the poller reordering, repaired before sending'

    printf '\n=== the go-netmon#69 mechanism ===\n'
    if [ "$d_repaired" = "$ABSENT" ]; then
        printf '  cannot say: this build does not publish the ordering counters\n'
    elif awk -v r="$d_repaired" -v o="$d_ooo" -v s="$d_superseded" \
        'BEGIN { exit !(r > 0 && o == 0 && s == 0) }' 2>/dev/null; then
        printf '  OBSERVED DIRECTLY: the poller emitted a series out of order (%s), the\n' "$d_repaired"
        printf '  encoder repaired it, the endpoint accepted it, and nothing was lost.\n'
    else
        printf '  not observed this run: repaired=%s out_of_order=%s superseded=%s\n' \
            "$d_repaired" "$d_ooo" "$d_superseded"
    fi

    printf '\n%d matched, %d unexpected\n' "$PASS_COUNT" "$FAIL_COUNT"
    [ "$FAIL_COUNT" -eq 0 ]
}

main() {
    case "${1:-}" in
    baseline) cmd_baseline ;;
    edit) cmd_edit ;;
    *)
        printf 'usage: %s baseline | edit\n' "$0" >&2
        exit 2
        ;;
    esac
}

main "$@"
