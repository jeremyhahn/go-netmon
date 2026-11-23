#!/usr/bin/env bash
# Capture and compare the observable state of a deployed go-netmon stack.
#
# Used to prove that a backup/restore cycle put the deployment back exactly as
# it was, and as a general post-deployment sanity check. Everything it reads
# comes from the running services' HTTP APIs, so it verifies what the stack
# actually serves rather than what is on disk.
#
# Configuration state (devices, profiles, MIB catalog, assignments, poll
# intervals) must match a previous capture exactly. Metric state is reported
# with its deltas rather than compared for equality: a live stack keeps
# scraping, so series and sample counts legitimately grow between captures.
# What must hold for metric HISTORY is that the oldest retained sample did not
# move forward - that is what tells you the TSDB came back rather than
# restarting empty.

set -euo pipefail

netmon_prog="verify-stack.sh"
# shellcheck source=lib/netmon-stack.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/netmon-stack.sh"

usage() {
    cat <<'EOF'
Usage: verify-stack.sh capture [FILE]
       verify-stack.sh compare BEFORE.json AFTER.json

  capture   Read the stack's current state and write it as JSON
            (to FILE, or stdout when FILE is omitted or "-").
  compare   Compare two captures. Configuration must match exactly; metric
            history must not have lost its oldest sample. Exits non-zero on
            any mismatch.

Environment:
  NETMON_WEBUI_URL  Web UI base URL   (default: http://localhost:8082)
  NETMON_PROM_URL   Prometheus URL    (default: http://localhost:9091)
  NETMON_HISTORY_LOOKBACK_DAYS  Window used to locate the oldest retained
                                sample (default: 30)
EOF
}

lookback_days="${NETMON_HISTORY_LOOKBACK_DAYS:-30}"

get() { curl -fsS --max-time 60 "$@"; }

# capture_state emits a normalised JSON document describing the stack. Fields
# that change on every read for uninteresting reasons - record creation
# timestamps, MIB descriptions - are deliberately excluded so two captures of
# an unchanged stack are byte-identical in the configuration section.
capture_state() {
    local devices profiles mibs

    devices="$(get "$NETMON_WEBUI_URL/api/devices")" ||
        die "cannot reach the web UI at $NETMON_WEBUI_URL"
    profiles="$(get "$NETMON_WEBUI_URL/api/profiles")" ||
        die "cannot read profiles from $NETMON_WEBUI_URL"
    mibs="$(get "$NETMON_WEBUI_URL/api/mibs")" ||
        die "cannot read the MIB catalog from $NETMON_WEBUI_URL"

    local device_state='[]' id
    while read -r id; do
        [ -z "$id" ] && continue
        local dm dp name
        dm="$(get "$NETMON_WEBUI_URL/api/devices/$id/mibs")" || dm='{}'
        dp="$(get "$NETMON_WEBUI_URL/api/devices/$id/profiles")" || dp='{}'
        name="$(jq -r --argjson i "$id" '[.devices[]? | select(.id == $i) | .name] | first // ""' <<<"$devices")"

        # Assignment records carry enablement and OID filters. They are part of
        # the configuration a restore has to bring back, so they are captured
        # per assigned MIB rather than summarised.
        local assignments='[]' mid
        while read -r mid; do
            [ -z "$mid" ] && continue
            local rec
            rec="$(get "$NETMON_WEBUI_URL/api/devices/$id/mibs/$mid/assignment")" || rec='null'
            assignments="$(jq -c --argjson m "$mid" --argjson r "${rec:-null}" \
                '. + [{mib_id: $m, assignment: $r}]' <<<"$assignments")"
        done < <(jq -r '.direct_mibs[]?.id // empty' <<<"$dm")

        device_state="$(jq -c \
            --arg name "$name" \
            --argjson all "$(jq -c '[.all_mibs[]?.id] | sort' <<<"$dm")" \
            --argjson direct "$(jq -c '[.direct_mibs[]?.id] | sort' <<<"$dm")" \
            --argjson profiles "$(jq -c '[.profiles[]?.name] | sort' <<<"$dp")" \
            --argjson assignments "$assignments" \
            --argjson poll "$(jq -r --arg n "$name" \
                '[.devices[]? | select(.name == $n) | .effective_poll_interval] | first // null' <<<"$devices")" \
            --arg address "$(jq -r --arg n "$name" \
                '[.devices[]? | select(.name == $n) | .address] | first // ""' <<<"$devices")" \
            '. + [{name: $name, address: $address, poll_interval: $poll,
                   all_mib_ids: $all, direct_mib_ids: $direct,
                   profiles: $profiles, direct_assignments: $assignments}]' <<<"$device_state")"
    done < <(jq -r '.devices[]?.id // empty' <<<"$devices")

    # Every collection is dereferenced with "[]?" because a freshly deployed
    # stack answers {"mibs":null} rather than {"mibs":[]}, and the empty stack
    # is precisely the state an operator captures just before a restore.
    local config
    config="$(jq -n \
        --argjson devices "$(jq -c '[.devices[]? | {name, address, snmp_version, port,
                                                    poll: .effective_poll_interval}] | sort_by(.name)' <<<"$devices")" \
        --argjson profiles "$(jq -c '[.profiles[]? | {name}] | sort_by(.name)' <<<"$profiles")" \
        --argjson mib_count "$(jq '(.mibs // []) | length' <<<"$mibs")" \
        --argjson mib_names "$(jq -c '[.mibs[]?.name] | sort' <<<"$mibs")" \
        --argjson device_state "$(jq -c 'sort_by(.name)' <<<"$device_state")" \
        '{devices: $devices, profiles: $profiles, mib_count: $mib_count,
          mib_names: $mib_names, device_state: $device_state}')"

    # --- metrics ---

    local metric_names head_series oldest='null' now start
    metric_names="$(get "$NETMON_PROM_URL/api/v1/label/__name__/values" |
        jq '.data | length')" || die "cannot reach Prometheus at $NETMON_PROM_URL"
    head_series="$(get "$NETMON_PROM_URL/api/v1/status/tsdb" |
        jq '.data.headStats.numSeries')" || head_series=null

    now="$(date +%s)"
    start="$((now - lookback_days * 86400))"
    oldest="$(get --get "$NETMON_PROM_URL/api/v1/query_range" \
        --data-urlencode 'query=count(up)' \
        --data-urlencode "start=$start" \
        --data-urlencode "end=$now" \
        --data-urlencode 'step=3600' |
        jq '[.data.result[]?.values[0][0]] | first // null')" || oldest=null

    local samples
    samples="$(get --get "$NETMON_PROM_URL/api/v1/query" \
        --data-urlencode "query=count_over_time(up[${lookback_days}d])" |
        jq '[.data.result[]?.value[1] | tonumber] | add // 0')" || samples=0

    jq -n \
        --arg captured_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
        --argjson config "$config" \
        --argjson metric_names "$metric_names" \
        --argjson head_series "$head_series" \
        --argjson oldest_sample "$oldest" \
        --argjson up_samples "$samples" \
        '{captured_at: $captured_at,
          config: $config,
          metrics: {distinct_metric_names: $metric_names,
                    head_series: $head_series,
                    oldest_up_sample_unix: $oldest_sample,
                    up_samples_in_window: $up_samples}}'
}

compare_states() {
    local before="$1" after="$2" failed=0
    [ -f "$before" ] || die "capture not found: $before"
    [ -f "$after" ] || die "capture not found: $after"

    rule
    log "STACK VERIFICATION"
    rule

    local b_dev a_dev b_prof a_prof b_mib a_mib
    b_dev="$(jq '.config.devices | length' "$before")"
    a_dev="$(jq '.config.devices | length' "$after")"
    b_prof="$(jq '.config.profiles | length' "$before")"
    a_prof="$(jq '.config.profiles | length' "$after")"
    b_mib="$(jq '.config.mib_count' "$before")"
    a_mib="$(jq '.config.mib_count' "$after")"

    log "  devices:      before=$b_dev  after=$a_dev"
    log "  profiles:     before=$b_prof after=$a_prof"
    log "  MIB catalog:  before=$b_mib after=$a_mib"

    while IFS=$'\t' read -r name all direct poll; do
        log "  device $name: all_mibs=$all direct_mibs=$direct poll=${poll}s"
    done < <(jq -r '.config.device_state[] |
        [.name, (.all_mib_ids | length), (.direct_mib_ids | length), .poll_interval] | @tsv' "$after")

    # Exact structural comparison. jq's -e on a boolean returns a shell exit
    # status, which is what makes this a gate rather than a report.
    if jq -es '.[0].config == .[1].config' "$before" "$after" >/dev/null; then
        log "  configuration: IDENTICAL"
    else
        log "  configuration: MISMATCH"
        log ""
        log "  differences (before -> after):"
        diff <(jq -S '.config' "$before") <(jq -S '.config' "$after") |
            sed 's/^/    /' >&2 || true
        failed=1
    fi

    local b_names a_names b_old a_old b_samp a_samp
    b_names="$(jq '.metrics.distinct_metric_names' "$before")"
    a_names="$(jq '.metrics.distinct_metric_names' "$after")"
    b_old="$(jq -r '.metrics.oldest_up_sample_unix // "null"' "$before")"
    a_old="$(jq -r '.metrics.oldest_up_sample_unix // "null"' "$after")"
    b_samp="$(jq '.metrics.up_samples_in_window' "$before")"
    a_samp="$(jq '.metrics.up_samples_in_window' "$after")"

    log ""
    log "  metric names: before=$b_names after=$a_names"
    log "  up samples:   before=$b_samp after=$a_samp  (window ${lookback_days}d)"
    log "  oldest 'up' sample:"
    log "    before: $(fmt_unix "$b_old")"
    log "    after:  $(fmt_unix "$a_old")"

    # Metric history is the one thing a restore can silently lose: a stack that
    # comes back with an empty TSDB looks perfectly healthy and starts scraping
    # immediately. The oldest retained sample moving forward is the signal.
    if [ "$b_old" = "null" ] || [ "$a_old" = "null" ]; then
        log "  metric history: INCONCLUSIVE (no 'up' series in the window)"
        failed=1
    elif awk -v b="$b_old" -v a="$a_old" 'BEGIN { exit !(a <= b + 3600) }'; then
        log "  metric history: PRESERVED (oldest sample did not move forward)"
    else
        log "  metric history: LOST (oldest sample moved forward by $((a_old - b_old))s)"
        failed=1
    fi

    rule
    if [ "$failed" -eq 0 ]; then
        log "RESULT: stack matches the reference capture"
    else
        log "RESULT: stack DIFFERS from the reference capture"
    fi
    rule
    return "$failed"
}

fmt_unix() {
    [ "$1" = "null" ] && {
        printf 'n/a'
        return
    }
    date -u -d "@${1%.*}" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || printf '%s' "$1"
}

# --- entry point ----------------------------------------------------------

require_tools jq curl awk date diff

case "${1:-}" in
capture)
    out="${2:--}"
    if [ "$out" = "-" ]; then
        capture_state
    else
        mkdir -p "$(dirname "$out")"
        capture_state >"$out"
        info "captured stack state -> $out"
    fi
    ;;
compare)
    [ $# -eq 3 ] || die "compare needs exactly two capture files (try --help)"
    compare_states "$2" "$3"
    ;;
-h | --help | "")
    usage
    [ -z "${1:-}" ] && exit 1
    exit 0
    ;;
*)
    die "unknown command: $1 (try --help)"
    ;;
esac
