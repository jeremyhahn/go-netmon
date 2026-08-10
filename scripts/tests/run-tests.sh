#!/usr/bin/env bash
# Tests for scripts/backup.sh and scripts/restore.sh.
#
# These run entirely against a stub Docker CLI (scripts/tests/fake-docker) and
# a throwaway repository tree under $TMPDIR. They never contact a Docker
# daemon, never read or write the real repository, and never touch a deployed
# stack - which is the point: tooling that destroys data must be testable
# without risking data.
#
# Usage: run-tests.sh [test-name-substring]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPTS_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

BACKUP="$SCRIPTS_DIR/backup.sh"
RESTORE="$SCRIPTS_DIR/restore.sh"
FAKE_DOCKER="$SCRIPT_DIR/fake-docker"

PASS=0
FAIL=0
FAILED_TESTS=()

# --- assertions -----------------------------------------------------------

fail() {
    printf '    FAIL: %s\n' "$*" >&2
    return 1
}

assert_ok() {
    local desc="$1"
    shift
    "$@" >/dev/null 2>&1 || fail "$desc: expected success, got exit $?"
}

# expect_failure runs a command that must fail, capturing its combined output
# into $OUT. It returns non-zero if the command unexpectedly succeeded, so
# callers can "|| return 1" and stop there.
#
# The obvious "out=$(cmd) && fail ..." spelling is a trap: fail() only returns
# 1, so a later passing assertion in the same test masks it and the test
# reports PASS while asserting nothing.
OUT=""
expect_failure() {
    local desc="$1"
    shift
    if OUT="$("$@" 2>&1)"; then
        fail "$desc: expected failure, but the command succeeded"
        return 1
    fi
    return 0
}

assert_eq() {
    [ "$2" = "$3" ] || fail "$1: expected '$3', got '$2'"
}

assert_contains() {
    case "$2" in
    *"$3"*) : ;;
    *) fail "$1: output did not contain '$3'
--- output ---
$2
--------------" ;;
    esac
}

assert_file() {
    [ -f "$2" ] || fail "$1: expected file to exist: $2"
}

assert_no_file() {
    [ ! -e "$2" ] || fail "$1: expected path NOT to exist: $2"
}

# --- fixture --------------------------------------------------------------

# new_fixture builds a throwaway repository and fake Docker state, and exports
# the environment the scripts read. Every test gets a fresh one.
new_fixture() {
    TEST_TMP="$(mktemp -d)"
    export FAKE_DOCKER_ROOT="$TEST_TMP/docker"
    export NETMON_REPO_ROOT="$TEST_TMP/repo"
    export NETMON_COMPOSE_FILE="$TEST_TMP/repo/docker-compose.yml"
    export NETMON_PROJECT="go-netmon"
    export DOCKER="$FAKE_DOCKER"
    # Unroutable port: the logical export fails instantly rather than waiting
    # on a timeout, which also exercises the "web UI unavailable" path.
    export NETMON_WEBUI_URL="http://127.0.0.1:1"
    export NETMON_HELPER_IMAGE="alpine:3.19"

    mkdir -p "$NETMON_REPO_ROOT"
    printf 'global:\n  default_poll_interval: 30\n' >"$NETMON_REPO_ROOT/config.yaml"
    printf 'global:\n  scrape_interval: 30s\n' >"$NETMON_REPO_ROOT/prometheus.yml"
    printf 'services: {}\n' >"$NETMON_COMPOSE_FILE"

    # The MIB source tree: a host directory, not a volume, bind-mounted into
    # two services. Nested, with a dotfile, so "replaced entirely" is something
    # the round trip can actually prove.
    MIB_TREE="$NETMON_REPO_ROOT/mibs"
    mkdir -p "$MIB_TREE/standard" "$MIB_TREE/cisco"
    printf 'IF-MIB DEFINITIONS ::= BEGIN\n' >"$MIB_TREE/standard/IF-MIB.txt"
    printf 'INET-ADDRESS-MIB DEFINITIONS ::= BEGIN\n' >"$MIB_TREE/standard/INET-ADDRESS-MIB.txt"
    printf 'CISCO-BGP4-MIB DEFINITIONS ::= BEGIN\n' >"$MIB_TREE/cisco/CISCO-BGP4-MIB.my"
    printf 'MIB sources\n' >"$MIB_TREE/README.md"
    printf 'imported\n' >"$MIB_TREE/.import-marker"

    mkdir -p "$FAKE_DOCKER_ROOT"
    printf '%s\n' webui-data go-netmon-data prometheus-data grafana-data \
        >"$FAKE_DOCKER_ROOT/compose-volumes"
    write_services_json "$MIB_TREE"
    printf '%s\n' go-netmon webui prometheus grafana >"$FAKE_DOCKER_ROOT/running"
    : >"$FAKE_DOCKER_ROOT/foreign"

    local v
    for v in webui-data go-netmon-data prometheus-data grafana-data; do
        mkdir -p "$FAKE_DOCKER_ROOT/volumes/go-netmon_$v/data"
        printf 'go-netmon' >"$FAKE_DOCKER_ROOT/volumes/go-netmon_$v/project"
    done

    # Recognisable contents so a round trip can be checked by value, not just
    # by "the command exited zero".
    printf 'devices=4 profiles=1 mibs=170\n' \
        >"$FAKE_DOCKER_ROOT/volumes/go-netmon_webui-data/data/webui.db"
    mkdir -p "$FAKE_DOCKER_ROOT/volumes/go-netmon_go-netmon-data/data/uploaded"
    printf 'IF-MIB source\n' \
        >"$FAKE_DOCKER_ROOT/volumes/go-netmon_go-netmon-data/data/uploaded/IF-MIB.txt"
    printf 'tsdb head\n' \
        >"$FAKE_DOCKER_ROOT/volumes/go-netmon_prometheus-data/data/wal"
    printf 'grafana.db\n' \
        >"$FAKE_DOCKER_ROOT/volumes/go-netmon_grafana-data/data/grafana.db"

    # An unlabelled look-alike, mirroring the stray volume on the real host.
    # No test may ever cause this to be read or written.
    mkdir -p "$FAKE_DOCKER_ROOT/volumes/go-netmon-data/data"
    printf 'DO NOT TOUCH\n' >"$FAKE_DOCKER_ROOT/volumes/go-netmon-data/data/stray"
}

# write_services_json renders what "docker compose config --format json" would
# emit, with the MIB tree bind-mounted at /app/mibs in the two services that
# mount it. The tooling derives the host MIB directory from exactly this, so
# the tests drive the same derivation the real deployment does.
#
# Called with no argument, it writes a compose rendering with no MIB mount at
# all, which is how the "cannot locate the tree" path is exercised.
write_services_json() {
    local mib="${1:-}" mount=""
    [ -n "$mib" ] && mount="$(printf '"volumes":[{"type":"bind","source":"%s","target":"/app/mibs","read_only":true}],' "$mib")"
    cat >"$FAKE_DOCKER_ROOT/services.json" <<EOF
{"services":{"go-netmon":{${mount}"image":"go-netmon:latest"},
             "webui":{${mount}"image":"go-netmon-webui:latest"},
             "prometheus":{"image":"prom/prometheus:latest"},
             "grafana":{"image":"grafana/grafana:latest"}}}
EOF
}

# tree_fingerprint hashes a host directory's relative paths and contents, so
# "the tree came back exactly" is a checked fact.
tree_fingerprint() {
    (cd "$1" 2>/dev/null &&
        find . \( -type f -o -type d \) -print0 | sort -z |
        xargs -0 -I{} sh -c 'printf "%s " "{}"; [ -f "{}" ] && sha256sum <"{}" || echo dir' |
        sha256sum) || printf 'absent'
}

drop_fixture() {
    [ -n "${TEST_TMP:-}" ] && rm -rf "$TEST_TMP"
    TEST_TMP=""
}

# volume_fingerprint hashes a fake volume's contents so "unchanged" is a
# checked fact rather than an assumption.
volume_fingerprint() {
    (cd "$FAKE_DOCKER_ROOT/volumes/$1/data" 2>/dev/null &&
        find . -type f -print0 | sort -z | xargs -0 sha256sum 2>/dev/null | sha256sum) || printf 'absent'
}

make_backup() {
    "$BACKUP" --name "$1" "${@:2}" >/dev/null 2>&1
}

# --- tests: backup --------------------------------------------------------

test_backup_creates_verifiable_archive() {
    make_backup b1 || fail "backup failed"
    local dir="$NETMON_REPO_ROOT/backups/b1"

    assert_file "manifest" "$dir/manifest.json" || return 1
    assert_file "checksums" "$dir/checksums.sha256" || return 1

    local v
    for v in webui poller metrics grafana; do
        assert_file "member $v" "$dir/volumes/$v.tar.gz" || return 1
    done
    assert_file "config.yaml" "$dir/config/config.yaml" || return 1
    assert_file "compose reference" "$dir/config/reference/docker-compose.yml" || return 1

    assert_eq "schema" "$(jq -r .schema_version "$dir/manifest.json")" "2" || return 1
    assert_eq "consistency" "$(jq -r .consistency "$dir/manifest.json")" "cold-offline" || return 1
    assert_eq "project" "$(jq -r .compose.project "$dir/manifest.json")" "go-netmon" || return 1
    assert_eq "volume count" "$(jq '.volumes | length' "$dir/manifest.json")" "4" || return 1
    assert_eq "components" "$(jq -r '.components | join(",")' "$dir/manifest.json")" \
        "config,mibs,webui,poller,metrics,grafana" || return 1
    assert_eq "images recorded" "$(jq '.images | length' "$dir/manifest.json")" "4" || return 1

    # The web UI is deliberately unreachable in the fixture; the manifest must
    # say so rather than implying a logical export happened.
    assert_eq "logical status" "$(jq -r .logical.status "$dir/manifest.json")" "unavailable" || return 1

    (cd "$dir" && sha256sum --quiet -c checksums.sha256) ||
        fail "checksums.sha256 does not verify"
}

test_backup_restarts_the_stack_afterwards() {
    make_backup b1 || fail "backup failed"
    assert_eq "running services restored" \
        "$(sort "$FAKE_DOCKER_ROOT/running" | tr '\n' ' ')" "go-netmon grafana prometheus webui " || return 1
    assert_contains "stopped then started" "$(cat "$FAKE_DOCKER_ROOT/actions")" "stopped"
}

test_backup_keep_stopped_leaves_stack_down() {
    make_backup b1 --keep-stopped || fail "backup failed"
    assert_eq "stack left stopped" "$(tr -d '\n' <"$FAKE_DOCKER_ROOT/running")" "" || return 1
}

test_backup_never_touches_unlabelled_lookalike() {
    local before after
    before="$(volume_fingerprint go-netmon-data)"
    make_backup b1 || fail "backup failed"
    after="$(volume_fingerprint go-netmon-data)"
    assert_eq "stray volume untouched" "$after" "$before" || return 1
    # Members are named by component, so no archive member can be confused with
    # the stray volume, and the manifest names only project-labelled volumes.
    assert_file "poller member named by component" \
        "$NETMON_REPO_ROOT/backups/b1/volumes/poller.tar.gz" || return 1
    assert_eq "manifest lists only project volumes" \
        "$(jq -r '[.volumes[].docker_volume] | sort | join(",")' \
            "$NETMON_REPO_ROOT/backups/b1/manifest.json")" \
        "go-netmon_go-netmon-data,go-netmon_grafana-data,go-netmon_prometheus-data,go-netmon_webui-data"
}

test_backup_rejects_unlabelled_target_volume() {
    : >"$FAKE_DOCKER_ROOT/volumes/go-netmon_webui-data/project"
    expect_failure "unlabelled volume" "$BACKUP" --name b1 || return 1
    assert_contains "refusal message" "$OUT" "refusing to touch it" || return 1
    assert_no_file "no output written" "$NETMON_REPO_ROOT/backups/b1"
}

test_backup_rejects_foreign_containers_in_project() {
    printf 'deadbeef\n' >"$FAKE_DOCKER_ROOT/foreign"
    expect_failure "foreign containers" "$BACKUP" --name b1 || return 1
    assert_contains "refusal message" "$OUT" "does not own" || return 1
    assert_no_file "no output written" "$NETMON_REPO_ROOT/backups/b1"
}

test_backup_rejects_unknown_component() {
    expect_failure "unknown component" "$BACKUP" --name b1 --only webui,bogus || return 1
    assert_contains "names the bad component" "$OUT" "unknown component 'bogus'" || return 1
    assert_no_file "no output written" "$NETMON_REPO_ROOT/backups/b1"
}

test_backup_rejects_existing_destination() {
    make_backup b1 || fail "first backup failed"
    expect_failure "existing destination" "$BACKUP" --name b1 || return 1
    assert_contains "refusal message" "$OUT" "already exists"
}

test_backup_only_selects_requested_components() {
    make_backup b1 --only metrics || fail "backup failed"
    local dir="$NETMON_REPO_ROOT/backups/b1"
    assert_eq "one volume" "$(jq '.volumes | length' "$dir/manifest.json")" "1" || return 1
    assert_eq "component" "$(jq -r '.volumes[0].component' "$dir/manifest.json")" "metrics" || return 1
    # config is not selected, so its files are captured for reference but
    # flagged as not restorable.
    assert_eq "config not restorable" \
        "$(jq -r '[.config_files[].restorable] | unique | join(",")' "$dir/manifest.json")" "false"
}

# --- tests: verification --------------------------------------------------

test_verify_accepts_a_good_backup() {
    make_backup b1 || fail "backup failed"
    local out
    out="$("$RESTORE" --from "$NETMON_REPO_ROOT/backups/b1" --verify-only 2>&1)" ||
        fail "verification of a good backup failed"
    assert_contains "reports success" "$out" "backup verified" || return 1
    assert_contains "says nothing changed" "$out" "nothing was changed"
}

test_verify_rejects_truncated_member() {
    make_backup b1 || fail "backup failed"
    local dir="$NETMON_REPO_ROOT/backups/b1" before out
    before="$(volume_fingerprint go-netmon_webui-data)"

    # Truncate the member and repair checksums.sha256 so only the manifest's
    # own record of the member can catch it. This is the case a naive
    # "does the file exist" check waves through.
    truncate -s 20 "$dir/volumes/webui.tar.gz"
    (cd "$dir" && find . -type f ! -name checksums.sha256 -print0 |
        sort -z | xargs -0 sha256sum >"$TEST_TMP/sums" && mv "$TEST_TMP/sums" checksums.sha256)

    expect_failure "truncated member" "$RESTORE" --from "$dir" --yes --no-safety-backup || return 1
    out="$OUT"
    assert_contains "explains the truncation" "$out" "truncated or modified" || return 1
    assert_eq "volume untouched" "$(volume_fingerprint go-netmon_webui-data)" "$before" || return 1
    assert_contains "stack was never stopped" "$(cat "$FAKE_DOCKER_ROOT/actions" 2>/dev/null || true)" ""
}

test_verify_rejects_corrupt_member_body() {
    make_backup b1 || fail "backup failed"
    local dir="$NETMON_REPO_ROOT/backups/b1" size before out
    before="$(volume_fingerprint go-netmon_prometheus-data)"

    # Same length, different bytes, checksums regenerated: only the manifest's
    # SHA-256 distinguishes this from the real member.
    size="$(stat -c %s "$dir/volumes/metrics.tar.gz")"
    head -c "$size" /dev/zero >"$dir/volumes/metrics.tar.gz"
    (cd "$dir" && find . -type f ! -name checksums.sha256 -print0 |
        sort -z | xargs -0 sha256sum >"$TEST_TMP/sums" && mv "$TEST_TMP/sums" checksums.sha256)

    expect_failure "truncated member" "$RESTORE" --from "$dir" --yes --no-safety-backup || return 1
    out="$OUT"
    assert_contains "reports the SHA mismatch" "$out" "failed its SHA-256 check" || return 1
    assert_eq "volume untouched" "$(volume_fingerprint go-netmon_prometheus-data)" "$before"
}

test_verify_rejects_tampered_tree() {
    make_backup b1 || fail "backup failed"
    local dir="$NETMON_REPO_ROOT/backups/b1" out
    printf 'tampered\n' >>"$dir/config/config.yaml"
    expect_failure "bad archive" "$RESTORE" --from "$dir" --verify-only || return 1
    out="$OUT"
    assert_contains "reports tampering" "$out" "corrupt or has been modified"
}

test_verify_rejects_missing_member() {
    make_backup b1 || fail "backup failed"
    local dir="$NETMON_REPO_ROOT/backups/b1" out
    rm "$dir/volumes/grafana.tar.gz"
    expect_failure "bad archive" "$RESTORE" --from "$dir" --verify-only || return 1
    out="$OUT"
    assert_contains "reports the loss" "$out" "corrupt or has been modified"
}

test_verify_rejects_backup_without_checksums() {
    make_backup b1 || fail "backup failed"
    local dir="$NETMON_REPO_ROOT/backups/b1" out
    rm "$dir/checksums.sha256"
    expect_failure "bad archive" "$RESTORE" --from "$dir" --verify-only || return 1
    out="$OUT"
    assert_contains "refuses unverifiable archive" "$out" "unverifiable archive"
}

test_verify_rejects_future_schema() {
    make_backup b1 || fail "backup failed"
    local dir="$NETMON_REPO_ROOT/backups/b1" out
    jq '.schema_version = 99' "$dir/manifest.json" >"$TEST_TMP/m" && mv "$TEST_TMP/m" "$dir/manifest.json"
    (cd "$dir" && find . -type f ! -name checksums.sha256 -print0 |
        sort -z | xargs -0 sha256sum >"$TEST_TMP/sums" && mv "$TEST_TMP/sums" checksums.sha256)
    expect_failure "bad archive" "$RESTORE" --from "$dir" --verify-only || return 1
    out="$OUT"
    assert_contains "reports the schema" "$out" "newer than this tool supports"
}

test_verify_rejects_non_backup_directory() {
    local out
    expect_failure "not a backup" "$RESTORE" --from "$NETMON_REPO_ROOT" --verify-only || return 1
    out="$OUT"
    assert_contains "explains why" "$out" "no manifest.json"
}

# --- tests: restore -------------------------------------------------------

test_restore_refuses_without_yes() {
    make_backup b1 || fail "backup failed"
    printf 'MUTATED\n' >"$FAKE_DOCKER_ROOT/volumes/go-netmon_webui-data/data/webui.db"
    local before out
    before="$(volume_fingerprint go-netmon_webui-data)"

    expect_failure "no --yes" "$RESTORE" --from "$NETMON_REPO_ROOT/backups/b1" || return 1
    out="$OUT"
    assert_contains "prints the plan" "$out" "RESTORE PLAN" || return 1
    assert_contains "names what it would destroy" "$out" "go-netmon_webui-data" || return 1
    assert_contains "says what the volume holds" "$out" "devices, profiles, MIB assignments" || return 1
    assert_contains "tells the operator how to proceed" "$out" "Re-run with --yes" || return 1
    assert_eq "nothing changed" "$(volume_fingerprint go-netmon_webui-data)" "$before"
}

test_restore_round_trip_restores_contents() {
    make_backup b1 || fail "backup failed"
    local dir="$NETMON_REPO_ROOT/backups/b1"
    local want_webui want_poller want_metrics want_grafana
    want_webui="$(volume_fingerprint go-netmon_webui-data)"
    want_poller="$(volume_fingerprint go-netmon_go-netmon-data)"
    want_metrics="$(volume_fingerprint go-netmon_prometheus-data)"
    want_grafana="$(volume_fingerprint go-netmon_grafana-data)"

    # Simulate a redeploy: every volume wiped, plus a stray file that must not
    # survive - a restore is a replacement, not a merge.
    local v
    for v in webui-data go-netmon-data prometheus-data grafana-data; do
        rm -rf "${FAKE_DOCKER_ROOT:?}/volumes/go-netmon_$v/data"
        mkdir -p "$FAKE_DOCKER_ROOT/volumes/go-netmon_$v/data"
    done
    printf 'leftover\n' >"$FAKE_DOCKER_ROOT/volumes/go-netmon_webui-data/data/leftover"
    printf 'wrong config\n' >"$NETMON_REPO_ROOT/config.yaml"

    "$RESTORE" --from "$dir" --yes --no-safety-backup --allow-tree-overwrite \
        >/dev/null 2>&1 || fail "restore failed"

    assert_eq "webui restored" "$(volume_fingerprint go-netmon_webui-data)" "$want_webui" || return 1
    assert_eq "poller restored" "$(volume_fingerprint go-netmon_go-netmon-data)" "$want_poller" || return 1
    assert_eq "metrics restored" "$(volume_fingerprint go-netmon_prometheus-data)" "$want_metrics" || return 1
    assert_eq "grafana restored" "$(volume_fingerprint go-netmon_grafana-data)" "$want_grafana" || return 1
    assert_no_file "leftover removed" "$FAKE_DOCKER_ROOT/volumes/go-netmon_webui-data/data/leftover" || return 1
    assert_eq "config restored" "$(cat "$NETMON_REPO_ROOT/config.yaml")" \
        "$(printf 'global:\n  default_poll_interval: 30')" || return 1
    assert_contains "stack brought back up" "$(cat "$FAKE_DOCKER_ROOT/actions")" "up"
}

test_restore_recreates_missing_volumes() {
    make_backup b1 || fail "backup failed"
    local want
    want="$(volume_fingerprint go-netmon_metrics 2>/dev/null || true)"
    want="$(volume_fingerprint go-netmon_prometheus-data)"

    # A full redeploy destroys the volumes entirely, not just their contents.
    rm -rf "${FAKE_DOCKER_ROOT:?}/volumes/go-netmon_prometheus-data"

    "$RESTORE" --from "$NETMON_REPO_ROOT/backups/b1" --yes --no-safety-backup \
        --allow-tree-overwrite >/dev/null 2>&1 || fail "restore failed"
    assert_eq "volume recreated and filled" "$(volume_fingerprint go-netmon_prometheus-data)" "$want"
}

test_restore_only_metrics_leaves_config_alone() {
    make_backup b1 || fail "backup failed"
    printf 'MUTATED\n' >"$FAKE_DOCKER_ROOT/volumes/go-netmon_webui-data/data/webui.db"
    rm -rf "${FAKE_DOCKER_ROOT:?}/volumes/go-netmon_prometheus-data/data"
    mkdir -p "$FAKE_DOCKER_ROOT/volumes/go-netmon_prometheus-data/data"
    local keep
    keep="$(volume_fingerprint go-netmon_webui-data)"

    "$RESTORE" --from "$NETMON_REPO_ROOT/backups/b1" --only metrics --yes --no-safety-backup \
        >/dev/null 2>&1 || fail "restore failed"

    assert_file "metrics restored" "$FAKE_DOCKER_ROOT/volumes/go-netmon_prometheus-data/data/wal" || return 1
    assert_eq "webui deliberately untouched" "$(volume_fingerprint go-netmon_webui-data)" "$keep"
}

test_restore_skip_metrics_preserves_metric_history() {
    make_backup b1 || fail "backup failed"
    printf 'NEWER HISTORY\n' >"$FAKE_DOCKER_ROOT/volumes/go-netmon_prometheus-data/data/wal"
    local keep
    keep="$(volume_fingerprint go-netmon_prometheus-data)"

    "$RESTORE" --from "$NETMON_REPO_ROOT/backups/b1" --skip metrics --yes --no-safety-backup \
        --allow-tree-overwrite >/dev/null 2>&1 || fail "restore failed"

    assert_eq "metric history preserved" "$(volume_fingerprint go-netmon_prometheus-data)" "$keep" || return 1
    assert_file "webui still restored" "$FAKE_DOCKER_ROOT/volumes/go-netmon_webui-data/data/webui.db"
}

test_restore_rejects_project_mismatch() {
    make_backup b1 || fail "backup failed"
    local dir="$NETMON_REPO_ROOT/backups/b1" before out
    before="$(volume_fingerprint go-netmon_webui-data)"

    export NETMON_PROJECT="go-netmon-staging"
    expect_failure "project mismatch" "$RESTORE" --from "$dir" --yes --no-safety-backup || return 1
    export NETMON_PROJECT="go-netmon"
    assert_contains "explains the mismatch" "$OUT" "different compose project" || return 1
    assert_contains "names both projects" "$OUT" "go-netmon-staging" || return 1
    assert_eq "nothing changed" "$(volume_fingerprint go-netmon_webui-data)" "$before"
}

test_restore_allows_project_mismatch_with_force() {
    make_backup b1 || fail "backup failed"
    local dir="$NETMON_REPO_ROOT/backups/b1" v

    # Stand up an equivalently labelled second project for the override to
    # land in, which is what --force-project is actually for.
    for v in webui-data go-netmon-data prometheus-data grafana-data; do
        mkdir -p "$FAKE_DOCKER_ROOT/volumes/go-netmon-staging_$v/data"
        printf 'go-netmon-staging' >"$FAKE_DOCKER_ROOT/volumes/go-netmon-staging_$v/project"
    done

    NETMON_PROJECT=go-netmon-staging "$RESTORE" --from "$dir" --yes --no-safety-backup \
        --force-project --allow-tree-overwrite >/dev/null 2>&1 || fail "forced restore failed"
    assert_file "staging volume filled" \
        "$FAKE_DOCKER_ROOT/volumes/go-netmon-staging_webui-data/data/webui.db"
}

test_restore_rejects_target_missing_a_required_volume() {
    make_backup b1 || fail "backup failed"
    # A target deployment that no longer defines grafana-data is an
    # incompatible deployment, not something to half-apply.
    printf '%s\n' webui-data go-netmon-data prometheus-data >"$FAKE_DOCKER_ROOT/compose-volumes"
    local out
    expect_failure "guarded restore" "$RESTORE" --from "$NETMON_REPO_ROOT/backups/b1" --yes --no-safety-backup || return 1
    out="$OUT"
    assert_contains "names the missing volume" "$out" "does not define volume 'grafana-data'"
}

test_restore_rejects_component_absent_from_backup() {
    make_backup b1 --only metrics || fail "backup failed"
    local out
    expect_failure "absent component" "$RESTORE" --from "$NETMON_REPO_ROOT/backups/b1" --only webui --yes --no-safety-backup || return 1
    out="$OUT"
    assert_contains "explains what is present" "$out" "is not present in this backup"
}

test_restore_takes_a_safety_backup_first() {
    make_backup b1 || fail "backup failed"
    printf 'STATE TO PRESERVE\n' >"$FAKE_DOCKER_ROOT/volumes/go-netmon_webui-data/data/webui.db"

    "$RESTORE" --from "$NETMON_REPO_ROOT/backups/b1" --yes --allow-tree-overwrite \
        >/dev/null 2>&1 || fail "restore failed"

    local safety
    safety="$(find "$NETMON_REPO_ROOT/backups" -maxdepth 1 -name 'pre-restore-*' | head -1)"
    [ -n "$safety" ] || fail "no safety backup was created"
    assert_file "safety manifest" "$safety/manifest.json" || return 1

    # The pre-restore state must be recoverable from the safety backup, which
    # is the entire point of taking it.
    tar -xzOf "$safety/volumes/webui.tar.gz" ./webui.db |
        grep -q 'STATE TO PRESERVE' || fail "safety backup does not hold the pre-restore state"
}

test_restore_warns_when_webui_and_poller_are_split() {
    make_backup b1 || fail "backup failed"
    local out
    out="$("$RESTORE" --from "$NETMON_REPO_ROOT/backups/b1" --only webui 2>&1)" || true
    assert_contains "warns about the coupling" "$out" "restoring 'webui' and 'poller' separately"
}

test_restore_rejects_foreign_containers_in_project() {
    make_backup b1 || fail "backup failed"
    printf 'deadbeef\n' >"$FAKE_DOCKER_ROOT/foreign"
    local before out
    before="$(volume_fingerprint go-netmon_webui-data)"
    expect_failure "guarded restore" "$RESTORE" --from "$NETMON_REPO_ROOT/backups/b1" --yes --no-safety-backup || return 1
    out="$OUT"
    assert_contains "refusal message" "$out" "does not own" || return 1
    assert_eq "nothing changed" "$(volume_fingerprint go-netmon_webui-data)" "$before"
}

test_restore_never_touches_unlabelled_lookalike() {
    make_backup b1 || fail "backup failed"
    local before
    before="$(volume_fingerprint go-netmon-data)"
    "$RESTORE" --from "$NETMON_REPO_ROOT/backups/b1" --yes --no-safety-backup \
        --allow-tree-overwrite >/dev/null 2>&1 || fail "restore failed"
    assert_eq "stray volume untouched" "$(volume_fingerprint go-netmon-data)" "$before" || return 1
    assert_contains "stray still holds its marker" \
        "$(cat "$FAKE_DOCKER_ROOT/volumes/go-netmon-data/data/stray")" "DO NOT TOUCH"
}

test_restore_requires_a_source() {
    local out
    expect_failure "no source" "$RESTORE" --yes || return 1
    out="$OUT"
    assert_contains "asks for --from" "$out" "--from is required"
}

test_restore_rejects_conflicting_selectors() {
    make_backup b1 || fail "backup failed"
    local out
    expect_failure "conflicting selectors" "$RESTORE" --from "$NETMON_REPO_ROOT/backups/b1" --only webui --skip metrics || return 1
    out="$OUT"
    assert_contains "explains the conflict" "$out" "mutually exclusive"
}

# --- tests: the MIB source tree -------------------------------------------
#
# The tree is a host directory, not a Docker volume: bind-mounted read-only at
# /app/mibs, living in the operator's working copy, under no version control.
# It is the source most of the imported catalog was parsed from, so an archive
# holding the parsed catalogs without it restores a deployment that resolves
# until something re-parses. These tests cover that it is carried, that it
# comes back byte for byte, and that overwriting a populated one cannot happen
# by accident.

test_backup_includes_the_mib_tree() {
    make_backup b1 || fail "backup failed"
    local dir="$NETMON_REPO_ROOT/backups/b1" m

    assert_file "tree member" "$dir/trees/mibs.tar.gz" || return 1
    assert_eq "one tree recorded" "$(jq '.trees | length' "$dir/manifest.json")" "1" || return 1

    m="$(jq -r '.trees[0]' "$dir/manifest.json")"
    assert_eq "component" "$(jq -r .component <<<"$m")" "mibs" || return 1
    assert_eq "source path" "$(jq -r .source_path <<<"$m")" "$MIB_TREE" || return 1
    assert_eq "mount target" "$(jq -r .mount_target <<<"$m")" "/app/mibs" || return 1
    assert_eq "member" "$(jq -r .member <<<"$m")" "trees/mibs.tar.gz" || return 1
    assert_eq "file count" "$(jq -r .files <<<"$m")" "5" || return 1

    # The manifest's checksum must be the checksum of the member as written,
    # or verification is checking the archive against itself.
    assert_eq "sha256 matches the member" "$(jq -r .sha256 <<<"$m")" \
        "$(sha256sum "$dir/trees/mibs.tar.gz" | cut -d' ' -f1)" || return 1
    assert_eq "entry count matches the member" "$(jq -r .entries <<<"$m")" \
        "$(tar -tzf "$dir/trees/mibs.tar.gz" | wc -l)" || return 1

    assert_eq "mibs is a component of the archive" \
        "$(jq -r '.components | index("mibs") != null' "$dir/manifest.json")" "true" || return 1
    (cd "$dir" && sha256sum --quiet -c checksums.sha256) ||
        fail "checksums.sha256 does not cover the tree member correctly"
}

test_backup_mib_member_reproduces_the_tree_exactly() {
    make_backup b1 || fail "backup failed"
    local out="$TEST_TMP/extracted"
    mkdir -p "$out"
    tar -C "$out" -xzf "$NETMON_REPO_ROOT/backups/b1/trees/mibs.tar.gz" ||
        fail "tree member is not a readable gzip tar"

    # Nested directories and the dotfile must all survive; a tree that loses
    # .import-marker or mibs/cisco/ would still "extract fine".
    assert_file "nested file" "$out/standard/IF-MIB.txt" || return 1
    assert_file "hand-added file" "$out/standard/INET-ADDRESS-MIB.txt" || return 1
    assert_file "vendor file" "$out/cisco/CISCO-BGP4-MIB.my" || return 1
    assert_file "dotfile" "$out/.import-marker" || return 1
    assert_eq "extracted tree is identical to the source" \
        "$(tree_fingerprint "$out")" "$(tree_fingerprint "$MIB_TREE")"
}

test_backup_only_mibs_carries_just_the_tree() {
    make_backup b1 --only mibs || fail "backup failed"
    local dir="$NETMON_REPO_ROOT/backups/b1"
    assert_eq "no volumes" "$(jq '.volumes | length' "$dir/manifest.json")" "0" || return 1
    assert_eq "one tree" "$(jq '.trees | length' "$dir/manifest.json")" "1" || return 1
    assert_no_file "no volume members" "$dir/volumes/webui.tar.gz" || return 1
    # config files are still captured for reference, but not as restorable.
    assert_eq "config not restorable" \
        "$(jq -r '[.config_files[].restorable] | unique | join(",")' "$dir/manifest.json")" "false"
}

test_backup_fails_when_the_mib_tree_is_missing() {
    rm -rf "${MIB_TREE:?}"
    expect_failure "missing tree" "$BACKUP" --name b1 || return 1
    assert_contains "names the path" "$OUT" "MIB source tree not found" || return 1
    assert_no_file "no output written" "$NETMON_REPO_ROOT/backups/b1" || return 1
    # The failure has to land in preflight: a backup that discovers a problem
    # after taking the stack down has caused an outage for nothing.
    assert_contains "stack was never stopped" \
        "$(cat "$FAKE_DOCKER_ROOT/actions" 2>/dev/null || true)" ""
}

test_backup_fails_when_the_mib_tree_is_empty() {
    rm -rf "${MIB_TREE:?}"
    mkdir -p "$MIB_TREE"
    expect_failure "empty tree" "$BACKUP" --name b1 || return 1
    assert_contains "explains why" "$OUT" "holds no files" || return 1
    assert_no_file "no output written" "$NETMON_REPO_ROOT/backups/b1"
}

test_backup_fails_when_compose_mounts_no_mib_tree() {
    write_services_json
    expect_failure "no mount" "$BACKUP" --name b1 || return 1
    assert_contains "names the mount point" "$OUT" "/app/mibs" || return 1
    assert_contains "offers --skip mibs" "$OUT" "--skip mibs" || return 1
    assert_contains "offers the override" "$OUT" "NETMON_MIB_TREE=" || return 1
    assert_contains "is fatal, not a guess" "$OUT" "cannot resolve the MIB source tree" || return 1
    assert_no_file "no output written" "$NETMON_REPO_ROOT/backups/b1"
}

test_backup_without_mibs_ignores_an_unresolvable_tree() {
    # Deployments that mount no MIB tree are legitimate; --skip mibs must be a
    # real way out rather than advice the tool then ignores.
    write_services_json
    make_backup b1 --skip mibs || fail "backup failed"
    assert_eq "no trees" "$(jq '.trees | length' "$NETMON_REPO_ROOT/backups/b1/manifest.json")" "0" || return 1
    assert_no_file "no tree member" "$NETMON_REPO_ROOT/backups/b1/trees"
}

test_backup_rejects_conflicting_mib_mounts() {
    # Two services mounting different host directories at /app/mibs: backing up
    # one and restoring over the other would silently give them different MIB
    # sources, so it is refused rather than resolved by picking.
    mkdir -p "$TEST_TMP/other-mibs"
    cat >"$FAKE_DOCKER_ROOT/services.json" <<EOF
{"services":{
  "go-netmon":{"volumes":[{"type":"bind","source":"$MIB_TREE","target":"/app/mibs","read_only":true}],"image":"go-netmon:latest"},
  "webui":{"volumes":[{"type":"bind","source":"$TEST_TMP/other-mibs","target":"/app/mibs","read_only":true}],"image":"go-netmon-webui:latest"},
  "prometheus":{"image":"prom/prometheus:latest"},
  "grafana":{"image":"grafana/grafana:latest"}}}
EOF
    expect_failure "conflicting mounts" "$BACKUP" --name b1 || return 1
    assert_contains "explains the conflict" "$OUT" "conflicting MIB source directories" || return 1
    assert_contains "lists both candidates" "$OUT" "$TEST_TMP/other-mibs" || return 1
    assert_no_file "no output written" "$NETMON_REPO_ROOT/backups/b1"
}

test_backup_honours_an_explicit_mib_tree_path() {
    mkdir -p "$TEST_TMP/elsewhere/vendor"
    printf 'ELSEWHERE-MIB\n' >"$TEST_TMP/elsewhere/vendor/ELSEWHERE-MIB.txt"

    NETMON_MIB_TREE="$TEST_TMP/elsewhere" make_backup b1 || fail "backup failed"
    local dir="$NETMON_REPO_ROOT/backups/b1"
    assert_eq "records the overridden path" \
        "$(jq -r '.trees[0].source_path' "$dir/manifest.json")" "$TEST_TMP/elsewhere" || return 1
    assert_eq "files from the override" "$(jq -r '.trees[0].files' "$dir/manifest.json")" "1" || return 1
    tar -tzf "$dir/trees/mibs.tar.gz" | grep -q 'ELSEWHERE-MIB.txt' ||
        fail "member does not hold the overridden tree"
}

test_backup_rejects_an_unsafe_mib_tree_path() {
    # The repository root, not a system directory: if this guard were ever
    # broken, the worst outcome is that the throwaway fixture gets archived.
    # A test that only fails safely because of filesystem permissions is not a
    # test of the guard, it is a test of who you happen to be running as.
    expect_failure "repository root" env NETMON_MIB_TREE="$NETMON_REPO_ROOT" \
        "$BACKUP" --name b1 || return 1
    assert_contains "refuses the path" "$OUT" "refusing to use the repository root" || return 1
    assert_no_file "no output written" "$NETMON_REPO_ROOT/backups/b1"
}

test_verify_rejects_truncated_mib_member() {
    make_backup b1 || fail "backup failed"
    local dir="$NETMON_REPO_ROOT/backups/b1" before out
    before="$(tree_fingerprint "$MIB_TREE")"

    # Truncate and repair checksums.sha256, so only the manifest's own record of
    # the member can catch it - the case a "does the file exist" check waves
    # through and then fails halfway through deleting the operator's MIBs.
    truncate -s 20 "$dir/trees/mibs.tar.gz"
    (cd "$dir" && find . -type f ! -name checksums.sha256 -print0 |
        sort -z | xargs -0 sha256sum >"$TEST_TMP/sums" && mv "$TEST_TMP/sums" checksums.sha256)

    expect_failure "truncated tree member" "$RESTORE" --from "$dir" --yes \
        --no-safety-backup --allow-tree-overwrite || return 1
    out="$OUT"
    assert_contains "explains the truncation" "$out" "truncated or modified" || return 1
    assert_contains "names the tree member" "$out" "trees/mibs.tar.gz" || return 1
    assert_eq "tree untouched" "$(tree_fingerprint "$MIB_TREE")" "$before" || return 1
    assert_contains "stack was never stopped" \
        "$(cat "$FAKE_DOCKER_ROOT/actions" 2>/dev/null || true)" ""
}

test_restore_refuses_a_populated_tree_without_the_gate() {
    make_backup b1 || fail "backup failed"
    printf 'LOCAL-ONLY-MIB\n' >"$MIB_TREE/standard/LOCAL-ONLY-MIB.txt"
    local before before_vol out
    before="$(tree_fingerprint "$MIB_TREE")"
    before_vol="$(volume_fingerprint go-netmon_webui-data)"

    expect_failure "no --allow-tree-overwrite" "$RESTORE" \
        --from "$NETMON_REPO_ROOT/backups/b1" --yes --no-safety-backup || return 1
    out="$OUT"
    assert_contains "names the gate" "$out" "--allow-tree-overwrite" || return 1
    assert_contains "offers the alternative" "$out" "--skip mibs" || return 1
    assert_contains "says it is a working tree" "$out" "part of your working tree" || return 1
    assert_contains "refuses explicitly" "$out" "refusing to overwrite a non-empty MIB source tree" || return 1

    assert_eq "tree untouched" "$(tree_fingerprint "$MIB_TREE")" "$before" || return 1
    # The gate must fire before anything else is touched, so a missing flag
    # costs a re-run and not a half-applied restore.
    assert_eq "volumes untouched" "$(volume_fingerprint go-netmon_webui-data)" \
        "$before_vol" || return 1
    assert_contains "stack was never stopped" \
        "$(cat "$FAKE_DOCKER_ROOT/actions" 2>/dev/null || true)" ""
    assert_eq "no safety backup was taken" \
        "$(find "$NETMON_REPO_ROOT/backups" -maxdepth 1 -name 'pre-restore-*' | wc -l)" "0"
}

test_restore_plan_states_the_tree_overwrite() {
    make_backup b1 || fail "backup failed"
    local out
    out="$("$RESTORE" --from "$NETMON_REPO_ROOT/backups/b1" 2>&1)" || true

    assert_contains "names the destination" "$out" "tree    $MIB_TREE" || return 1
    assert_contains "says it is not a volume" "$out" "HOST DIRECTORY in your working tree" || return 1
    assert_contains "states wholesale replacement" "$out" "EMPTIED first" || return 1
    assert_contains "says files are deleted, not merged" "$out" "deleted, not merged" || return 1
    assert_contains "shows the current occupancy" "$out" "current: 5 files" || return 1
    assert_contains "says where the path came from" "$out" "compose bind mount at /app/mibs" || return 1
    assert_contains "states the gate" "$out" "requires --allow-tree-overwrite" || return 1
    assert_contains "tells the operator how to proceed" "$out" "Re-run with --yes" || return 1
}

test_restore_replaces_the_mib_tree_wholesale() {
    make_backup b1 || fail "backup failed"
    local want
    want="$(tree_fingerprint "$MIB_TREE")"

    # A working tree that has drifted since the backup: a module added, one the
    # backup holds deleted, and one edited.
    printf 'ADDED-AFTER-BACKUP\n' >"$MIB_TREE/standard/ADDED-MIB.txt"
    rm "$MIB_TREE/standard/INET-ADDRESS-MIB.txt"
    printf 'EDITED\n' >"$MIB_TREE/cisco/CISCO-BGP4-MIB.my"

    "$RESTORE" --from "$NETMON_REPO_ROOT/backups/b1" --yes --no-safety-backup \
        --allow-tree-overwrite >/dev/null 2>&1 || fail "restore failed"

    assert_eq "tree restored exactly" "$(tree_fingerprint "$MIB_TREE")" "$want" || return 1
    assert_file "deleted module came back" "$MIB_TREE/standard/INET-ADDRESS-MIB.txt" || return 1
    assert_eq "edited module reverted" "$(cat "$MIB_TREE/cisco/CISCO-BGP4-MIB.my")" \
        "CISCO-BGP4-MIB DEFINITIONS ::= BEGIN" || return 1
    # Replacement, not merge: a module the backup does not hold must not
    # survive, or the deployment resolves against a set matching neither side.
    assert_no_file "post-backup addition removed" "$MIB_TREE/standard/ADDED-MIB.txt"
}

test_restore_writes_the_tree_to_an_alternate_destination() {
    make_backup b1 || fail "backup failed"
    local want elsewhere="$TEST_TMP/restored-mibs"
    want="$(tree_fingerprint "$MIB_TREE")"
    mkdir -p "$elsewhere"
    printf 'stale\n' >"$elsewhere/stale.txt"

    local out
    out="$("$RESTORE" --from "$NETMON_REPO_ROOT/backups/b1" --only mibs --yes \
        --no-safety-backup --allow-tree-overwrite --mib-tree "$elsewhere" 2>&1)" ||
        fail "restore failed"

    assert_contains "warns the destination differs" "$out" "will be restored into" || return 1
    assert_eq "alternate destination filled" "$(tree_fingerprint "$elsewhere")" "$want" || return 1
    assert_no_file "stale file removed" "$elsewhere/stale.txt" || return 1
    assert_eq "the real tree was left alone" "$(tree_fingerprint "$MIB_TREE")" "$want"
}

test_restore_into_an_empty_tree_needs_no_gate() {
    make_backup b1 || fail "backup failed"
    local want fresh="$TEST_TMP/fresh-mibs"
    want="$(tree_fingerprint "$MIB_TREE")"

    # The fresh-host disaster path: nothing is destroyed, so nothing is gated.
    local out
    out="$("$RESTORE" --from "$NETMON_REPO_ROOT/backups/b1" --only mibs --yes \
        --mib-tree "$fresh" 2>&1)" || fail "restore into an empty destination failed"

    assert_eq "tree created and filled" "$(tree_fingerprint "$fresh")" "$want" || return 1
    assert_contains "says the safety copy was pointless" "$out" "nothing to preserve" || return 1
}

test_restore_skip_mibs_leaves_the_tree_untouched() {
    make_backup b1 || fail "backup failed"
    printf 'LOCAL-ONLY-MIB\n' >"$MIB_TREE/standard/LOCAL-ONLY-MIB.txt"
    local keep
    keep="$(tree_fingerprint "$MIB_TREE")"

    "$RESTORE" --from "$NETMON_REPO_ROOT/backups/b1" --skip mibs --yes --no-safety-backup \
        >/dev/null 2>&1 || fail "restore failed"

    assert_eq "tree preserved" "$(tree_fingerprint "$MIB_TREE")" "$keep" || return 1
    assert_file "local addition survived" "$MIB_TREE/standard/LOCAL-ONLY-MIB.txt" || return 1
    assert_file "other components still restored" \
        "$FAKE_DOCKER_ROOT/volumes/go-netmon_webui-data/data/webui.db"
}

test_restore_safety_backup_captures_the_mib_tree() {
    make_backup b1 || fail "backup failed"
    printf 'MIB TO PRESERVE\n' >"$MIB_TREE/standard/UNSAVED-MIB.txt"

    "$RESTORE" --from "$NETMON_REPO_ROOT/backups/b1" --yes --allow-tree-overwrite \
        >/dev/null 2>&1 || fail "restore failed"

    local safety
    safety="$(find "$NETMON_REPO_ROOT/backups" -maxdepth 1 -name 'pre-restore-*' | head -1)"
    [ -n "$safety" ] || fail "no safety backup was created"
    assert_file "safety tree member" "$safety/trees/mibs.tar.gz" || return 1

    # The whole point of the gate is that the mistake is recoverable: a module
    # added since the backup must be inside the safety copy.
    tar -xzOf "$safety/trees/mibs.tar.gz" ./standard/UNSAVED-MIB.txt |
        grep -q 'MIB TO PRESERVE' ||
        fail "safety backup does not hold the pre-restore MIB tree"
    assert_no_file "and was genuinely replaced" "$MIB_TREE/standard/UNSAVED-MIB.txt"
}

test_restore_refuses_an_unsafe_tree_destination() {
    make_backup b1 || fail "backup failed"
    local before out
    before="$(tree_fingerprint "$MIB_TREE")"

    # Deliberately WITHOUT --yes. The check lives in stage 3, before the plan is
    # even printed, so a dry run proves it fires - and this test can therefore
    # never reach the code that empties a directory, however the guard is
    # broken. Naming a real system path and relying on being an unprivileged
    # user to contain the damage would be the wrong kind of test.
    expect_failure "system directory" "$RESTORE" --from "$NETMON_REPO_ROOT/backups/b1" \
        --only mibs --mib-tree /usr || return 1
    out="$OUT"
    assert_contains "refuses the path" "$out" "refusing to use a system directory" || return 1
    # Distinguishes the refusal from the ordinary "you did not pass --yes" exit.
    case "$out" in
    *"RESTORE PLAN"*) fail "got as far as printing a plan for /usr" ;;
    esac
    assert_eq "tree untouched" "$(tree_fingerprint "$MIB_TREE")" "$before" || return 1
    assert_contains "stack was never stopped" \
        "$(cat "$FAKE_DOCKER_ROOT/actions" 2>/dev/null || true)" ""
}

test_restore_accepts_a_schema_1_backup_without_trees() {
    # Archives taken before the MIB tree was covered must still restore. They
    # carry no trees[] at all, and nothing may go looking for one.
    make_backup b1 --skip mibs || fail "backup failed"
    local dir="$NETMON_REPO_ROOT/backups/b1"
    jq 'del(.trees) | .schema_version = 1' "$dir/manifest.json" >"$TEST_TMP/m" &&
        mv "$TEST_TMP/m" "$dir/manifest.json"
    (cd "$dir" && find . -type f ! -name checksums.sha256 -print0 |
        sort -z | xargs -0 sha256sum >"$TEST_TMP/sums" && mv "$TEST_TMP/sums" checksums.sha256)

    "$RESTORE" --from "$dir" --yes --no-safety-backup >/dev/null 2>&1 ||
        fail "a schema-1 archive did not restore"
    assert_file "volumes still restored" \
        "$FAKE_DOCKER_ROOT/volumes/go-netmon_webui-data/data/webui.db"
}

# --- runner ---------------------------------------------------------------

run_test() {
    local name="$1"
    printf '  %-58s' "$name"
    new_fixture
    if (
        set +e
        "$name"
    ); then
        printf 'PASS\n'
        PASS=$((PASS + 1))
    else
        printf 'FAIL\n'
        FAIL=$((FAIL + 1))
        FAILED_TESTS+=("$name")
    fi
    drop_fixture
}

main() {
    local filter="${1:-}" name
    command -v jq >/dev/null 2>&1 || {
        printf 'jq is required to run these tests\n' >&2
        exit 1
    }
    [ -x "$FAKE_DOCKER" ] || chmod +x "$FAKE_DOCKER"

    printf 'backup/restore script tests\n\n'
    while read -r name; do
        [ -n "$filter" ] && case "$name" in *"$filter"*) ;; *) continue ;; esac
        run_test "$name"
    done < <(declare -F | awk '{print $3}' | grep '^test_' | sort)

    printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
    if [ "$FAIL" -gt 0 ]; then
        printf 'failed: %s\n' "${FAILED_TESTS[*]}"
        exit 1
    fi
}

main "$@"
