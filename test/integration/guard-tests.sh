#!/usr/bin/env bash
# Usage: guard-tests.sh [test-name-substring] -- tests compose-guard.sh and stack.sh against a stub docker on PATH.

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

# make_fake_docker writes a stub CLI modelling containers ("<id> <project> <config_files>") that refuses anything it does not model.
make_fake_docker() {
    local bin="$1" root="$2"
    mkdir -p "$bin" "$root"
    : >"$root/log"
    : >"$root/cwd-log"
    : >"$root/containers"
    cat >"$bin/docker" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$*" >>"$root/log"
# Where each call ran, tagged with the call.
printf '%s\t%s\n' "\$(pwd)" "\$*" >>"$root/cwd-log"

# die refuses an invocation this stub does not model instead of answering it.
# The guard standing behind this stub is what keeps an integration run from
# deleting a deployed stack's volumes, so an unmodelled argument must cost a
# red test, not a plausible default.
die() {
    printf 'fake-docker: %s\n' "\$*" >&2
    exit 1
}

# A forced failure, for the fail-open case.
if [ -f "$root/ps-status" ] && [ "\$1" = "ps" ]; then
    # To stderr, like real docker: on stdout the text would be parsed as
    # container ids and fail in the inspect branch, so the ps-status branch
    # of compose_foreign_containers would pin nothing.
    cat "$root/ps-error" >&2 2>/dev/null || true
    exit "\$(cat "$root/ps-status")"
fi

# Filter state; the seen flag is separate from the value.
want_project=""; want_project_seen=0
want_config="";  want_config_seen=0

# ps_filter validates and records one docker ps filter value following --filter or -f.
ps_filter() {
    case "\$1" in
        label!=*)
            # Real docker rejects this outright.
            echo "Error response from daemon: invalid filter 'label!'" >&2
            exit 1
            ;;
        label=*!=*)
            # Refuse the malformed label!= negation on any key; real docker matches nothing.
            echo "fake-docker: refusing malformed label filter: \$1" >&2
            exit 1
            ;;
        label=com.docker.compose.project=*)
            [ "\$want_project_seen" -eq 0 ] || die "'docker ps' given two project filters"
            want_project_seen=1
            want_project="\${1#label=com.docker.compose.project=}"
            ;;
        label=com.docker.compose.project.config_files=*)
            [ "\$want_config_seen" -eq 0 ] || die "'docker ps' given two config_files filters"
            want_config_seen=1
            want_config="\${1#label=com.docker.compose.project.config_files=}"
            ;;
        *)
            die "unsupported 'docker ps' filter '\$1'. This stub understands only
  label=com.docker.compose.project=<name> and
  label=com.docker.compose.project.config_files=<path>. A mistyped label key
  matches nothing on a real daemon, which looks exactly like 'no foreign
  containers' to the caller."
            ;;
    esac
}

case "\$1" in
    ps)
        # A stderr warning from a successful command, as real docker emits.
        [ -f "$root/ps-warning" ] && cat "$root/ps-warning" >&2
        shift
        while [ "\$#" -gt 0 ]; do
            case "\$1" in
                -a|-q|-aq|-qa|--all|--quiet) ;;
                --filter|-f)
                    [ "\$#" -ge 2 ] || die "'docker ps \$1' with no filter value"
                    shift
                    ps_filter "\$1"
                    ;;
                --filter=*) ps_filter "\${1#--filter=}" ;;
                *)
                    die "unsupported 'docker ps' argument '\$1'. This stub models
  -a/-q and --filter only. An option it does not model changes what a real
  daemon returns, so answering anyway would let the guard's query drift without
  a single test noticing."
                    ;;
            esac
            shift
        done
        while read -r id project config; do
            [ -n "\$id" ] || continue
            [ "\$want_project_seen" -eq 0 ] || [ "\$project" = "\$want_project" ] || continue
            [ "\$want_config_seen" -eq 0 ] || [ "\$config" = "\$want_config" ] || continue
            echo "\$id"
        done <"$root/containers"
        exit 0
        ;;
    inspect)
        # The template is compared exactly.
        shift
        fmt=""; fmt_seen=0; target=""; target_seen=0
        while [ "\$#" -gt 0 ]; do
            case "\$1" in
                --format)
                    [ "\$#" -ge 2 ] || die "'docker inspect --format' with no template"
                    shift; fmt="\$1"; fmt_seen=1
                    ;;
                --format=*) fmt="\${1#--format=}"; fmt_seen=1 ;;
                -*) die "unsupported 'docker inspect' option '\$1'" ;;
                *)
                    [ "\$target_seen" -eq 0 ] || die "'docker inspect' named two objects ('\$target' then '\$1')"
                    target_seen=1; target="\$1"
                    ;;
            esac
            shift
        done
        [ "\$target_seen" -eq 1 ] || die "'docker inspect' named no object"
        [ "\$fmt_seen" -eq 1 ] || die "'docker inspect' without --format: this stub answers
  only the two label templates the guard asks for, and a bare inspect returns a
  JSON document it does not model."
        case "\$fmt" in
            '{{index .Config.Labels "com.docker.compose.project.config_files"}}') field=config ;;
            '{{index .Config.Labels "com.docker.compose.project"}}')              field=project ;;
            *) die "unsupported 'docker inspect --format' template: \$fmt" ;;
        esac
        while read -r cid project config; do
            if [ "\$cid" = "\$target" ]; then
                case "\$field" in
                    config)  echo "\$config" ;;
                    project) echo "\$project" ;;
                esac
                exit 0
            fi
        done <"$root/containers"
        echo "Error: No such object: \$target" >&2
        exit 1
        ;;
    compose)
        # Permissive by design: callers assert on $root/log; only version needs an answer.
        case "\$2" in version) echo "Docker Compose version v2.0.0" ;; esac
        exit 0
        ;;
    *)
        die "unsupported docker command '\$1'. Nothing outside ps, inspect and
  compose is issued anywhere in this suite, so a new one is a change that
  should be modelled here before it is trusted."
        ;;
esac
EOF
    chmod +x "$bin/docker"
    # run-tests.sh vendors the modules for the image build before compose;
    # nothing is built here, so the stub vendors nothing.
    printf '#!/usr/bin/env bash\nexit 0\n' >"$bin/go"
    chmod +x "$bin/go"
}

# run_stack runs a stack.sh verb against a fresh stub, output in $OUT; extra NAME=VALUE arguments are exported.
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

# --- the instrument -------------------------------------------------------

# The stub's refusals, each paired with a positive control returning the right value.
test_fake_docker_refuses_what_it_does_not_model() {
    local bin root failed=0 out
    bin="$(mktemp -d)"; root="$(mktemp -d)"
    make_fake_docker "$bin" "$root"
    local proj="go-netmon-integration-stubtest"
    printf '%s %s %s\n' \
        ours1   "$proj" "$COMPOSE_FILE" \
        ours2   "$proj" "$COMPOSE_FILE" \
        theirs1 "$proj" /elsewhere/docker-compose.yml \
        >"$root/containers"
    local d="$bin/docker"

    # --- refusals ---------------------------------------------------------
    # Each is a real shape somebody could write: an option docker has that this
    # stub does not model, a mistyped key, a filter in a position docker does
    # not read one from, the two negation spellings, a duplicate, and a missing
    # value.
    local -a bad_ps=(
        "--no-trunc:an option the stub does not model"
        "MISTYPED:a mistyped label key"
        "POSITIONAL:a filter passed as a bare positional"
        "NEGATED:the malformed label negation"
        "LABELBANG:docker's invalid 'label!=' spelling"
        "DUPLICATE:two project filters"
        "NOVALUE:--filter with no value"
    )
    local case_desc
    for case_desc in "${bad_ps[@]}"; do
        local what="${case_desc%%:*}" desc="${case_desc#*:}"
        case "$what" in
            --no-trunc) out="$("$d" ps -aq --no-trunc --filter "label=com.docker.compose.project=$proj" 2>&1)" ;;
            MISTYPED)   out="$("$d" ps -aq --filter "label=com.docker.compose.project_name=$proj" 2>&1)" ;;
            POSITIONAL) out="$("$d" ps -aq "label=com.docker.compose.project=$proj" 2>&1)" ;;
            NEGATED)    out="$("$d" ps -aq --filter "label=com.docker.compose.project.config_files!=$COMPOSE_FILE" 2>&1)" ;;
            LABELBANG)  out="$("$d" ps -aq --filter "label!=com.docker.compose.project=$proj" 2>&1)" ;;
            DUPLICATE)  out="$("$d" ps -aq --filter "label=com.docker.compose.project=a" --filter "label=com.docker.compose.project=b" 2>&1)" ;;
            NOVALUE)    out="$("$d" ps -aq --filter 2>&1)" ;;
        esac
        # shellcheck disable=SC2181  # the status is of the case arm above, not of a simple command
        if [ $? -eq 0 ]; then
            fail "the stub answered 'docker ps' with $desc: $out" || failed=1
        fi
    done

    if out="$("$d" inspect ours1 2>&1)"; then
        fail "the stub answered 'docker inspect' with no --format: $out" || failed=1
    fi
    if out="$("$d" inspect --format '{{.Id}}' ours1 2>&1)"; then
        fail "the stub answered an inspect template it does not model: $out" || failed=1
    fi
    if out="$("$d" inspect --format '{{index .Config.Labels "com.docker.compose.project"}}' ours1 ours2 2>&1)"; then
        fail "the stub answered an inspect naming two objects: $out" || failed=1
    fi
    if out="$("$d" volume ls 2>&1)"; then
        fail "the stub answered a top-level command it does not model: $out" || failed=1
    fi

    # An empty project filter selects containers with an empty project label, not all.
    if ! out="$("$d" ps -aq --filter "label=com.docker.compose.project=" 2>&1)"; then
        fail "the stub refused an empty project filter, which is a legal query: $out" || failed=1
    elif [ -n "$out" ]; then
        fail "an empty project filter matched containers it should not have: $out" || failed=1
    fi

    # Positive controls: the guard's real query shapes answer correctly.
    if ! out="$("$d" ps -aq --filter "label=com.docker.compose.project=$proj" 2>&1)" \
        || [ "$(echo "$out" | tr '\n' ' ')" != "ours1 ours2 theirs1 " ]; then
        fail "the project filter did not return the project's containers: $out" || failed=1
    fi
    if ! out="$("$d" ps -aq --filter "label=com.docker.compose.project.config_files=$COMPOSE_FILE" 2>&1)" \
        || [ "$(echo "$out" | tr '\n' ' ')" != "ours1 ours2 " ]; then
        fail "the config_files filter did not select by compose file: $out" || failed=1
    fi
    if ! out="$("$d" inspect --format '{{index .Config.Labels "com.docker.compose.project.config_files"}}' theirs1 2>&1)" \
        || [ "$out" != "/elsewhere/docker-compose.yml" ]; then
        fail "inspect did not return the config_files label: $out" || failed=1
    fi
    if ! out="$("$d" inspect --format '{{index .Config.Labels "com.docker.compose.project"}}' theirs1 2>&1)" \
        || [ "$out" != "$proj" ]; then
        fail "inspect did not return the project label: $out" || failed=1
    fi
    # The two templates must not be interchangeable: reading the argv as a
    # substring returned the config field for both, which is what the exact
    # comparison replaced.
    if [ "$("$d" inspect --format '{{index .Config.Labels "com.docker.compose.project"}}' ours1 2>&1)" \
       = "$("$d" inspect --format '{{index .Config.Labels "com.docker.compose.project.config_files"}}' ours1 2>&1)" ]; then
        fail "the two inspect templates returned the same field" || failed=1
    fi
    # A container the fixture does not have is still an error, not an answer.
    if out="$("$d" inspect --format '{{index .Config.Labels "com.docker.compose.project"}}' nosuch 2>&1)"; then
        fail "the stub answered for a container it does not have: $out" || failed=1
    fi
    # The compose arm stays permissive; the suite depends on it.
    if ! out="$("$d" compose version 2>&1)" || [ "$out" != "Docker Compose version v2.0.0" ]; then
        fail "the compose version arm stopped answering: $out" || failed=1
    fi
    if ! "$d" compose -p x -f /x.yml down -v --remove-orphans >/dev/null 2>&1; then
        fail "the compose arm refused a command the suite relies on it recording" || failed=1
    fi

    rm -rf "$bin" "$root"
    [ "$failed" -eq 0 ]
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

# A same-project container from a different compose file is foreign.
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

# Our container in another project must not make this one look foreign.
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
    # The ps-status branch specifically, not the inspect one.
    expect_refused "failed ownership query" "ps failed (exit 1): Cannot connect to the Docker daemon"
}

# --- lifecycle verbs ------------------------------------------------------

# Every verb must pass the dedicated project. One that does not gets the
# directory default, which is the deployment.
test_every_verb_names_the_manual_project() {
    local verb
    for verb in up e2e-up down logs shell; do
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

# The e2e profile's poller and web UI are invisible to a compose command that
# does not name the profile, so a "down" without it would leave them running
# and their volumes behind.
test_down_reaches_the_e2e_profile() {
    run_stack down || fail "down failed" || return 1

    local down
    down="$(grep -E -- "compose -p $MANUAL_PROJECT .* down" "$ROOT/log" || true)"
    case "$down" in *"--profile e2e"*) ;; *) fail "down omits --profile e2e: $down"; return 1 ;; esac
}

test_e2e_up_starts_the_poller_and_seeds_only_through_the_config_import() {
    run_stack e2e-up || fail "e2e-up failed: $OUT" || return 1

    local up
    up="$(grep -E -- "compose -p $MANUAL_PROJECT .* up " "$ROOT/log" || true)"
    case "$up" in *" poller"*" webui"*) ;; *) fail "e2e-up does not start poller and webui: $up"; return 1 ;; esac
    grep -qE -- "compose -p $MANUAL_PROJECT .* run --rm .*e2e-seed" "$ROOT/log" \
        || fail "e2e-up does not run the seed" || return 1

    # The seed's one write is the operator's restore path, the config import,
    # against the disposable stack. It reads the device list to wait for
    # the web UI and calls no other endpoint: no device or MIB write.
    local seed="$REPO_ROOT/test/integration/e2e-seed.sh"
    local doc="$REPO_ROOT/test/integration/snmp-data/e2e-config.json"
    local writes
    writes="$(grep -cE -- '-X (POST|PUT|PATCH|DELETE)' "$seed" || true)"
    [ "$writes" = 1 ] || fail "e2e-seed.sh makes $writes writes, want the one import" || return 1
    grep -qE -- '/api/config/import\?apply=true' "$seed" \
        || fail "e2e-seed.sh does not write through /api/config/import?apply=true" || return 1
    local path
    while read -r path; do
        case "$path" in
            /api/config/import | /api/devices) ;;
            *) fail "e2e-seed.sh calls $path" || return 1 ;;
        esac
    done < <(grep -oE "/api/[A-Za-z0-9_/.{}\$-]*" "$seed")
    local device_calls
    device_calls="$(grep -E -- '/api/devices' "$seed" || true)"
    case "$device_calls" in
        *"-X "* | *"-d "* | *"--data"*) fail "e2e-seed.sh writes to /api/devices" || return 1 ;;
    esac

    # The document restores the disposable stack's devices and nothing else:
    # every device is the simulator, and every MIB source is mibs/standard's,
    # byte for byte, so the import cannot refuse it as altered.
    local address
    while read -r address; do
        [ "$address" = '"snmp-simulator"' ] \
            || fail "e2e-config.json names a device at $address" || return 1
    done < <(grep -oE '"address": *"[^"]*"' "$doc" | grep -oE '"[^"]*"$')
    local field value file=""
    while IFS=: read -r field value; do
        value="${value//[\" ]/}"
        case "$field" in
            *file*) file="${value#mibs/}" ;;
            *sha256*)
                [ -f "$REPO_ROOT/mibs/standard/$file" ] \
                    || fail "e2e-config.json names $file, not in mibs/standard" || return 1
                [ "$(sha256sum "$REPO_ROOT/mibs/standard/$file" | cut -d' ' -f1)" = "$value" ] \
                    || fail "e2e-config.json's sha256 for $file is not mibs/standard's" || return 1
                ;;
        esac
    done < <(grep -oE '"(file|sha256)": *"[^"]*"' "$doc")

    # No spec writes to a MIB endpoint. mib-controls and mib-object-picker are
    # the suites of the assignment controls themselves, and stay as they are.
    local writer
    writer="$(grep -lE "request\.(post|put|patch|delete)\([^)]*/mibs" \
        "$REPO_ROOT"/tests/e2e/*.ts "$REPO_ROOT"/tests/e2e/tests/*.ts 2>/dev/null \
        | grep -vE '/(mib-controls|mib-object-picker)\.spec\.ts$' || true)"
    [ -z "$writer" ] || fail "specs write to a MIB endpoint: $writer" || return 1
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

# The deployment project is unreachable from every entry point, with mixed and ours-only fixtures.
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

        set +e
        # shellcheck disable=SC2086  # $entry is a verb list ("stack.sh up") and must word-split
        env PATH="$BIN:$PATH" GITHUB_RUN_ID=tabletest "$SCRIPT_DIR"/$entry >/dev/null 2>&1
        set -e

        # Exact match on the -p value.
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

# scan_compose_docs <repo-root> prints tracked files starting a docker-compose.test.yml invocation without -p; non-zero means the scan could not run.
scan_compose_docs() {
    local root="$1"
    local matched status=0

    # Exit codes: 3 cannot cd, 4 git ls-files failed, 5 empty corpus, 6 search failed, 0 searched.
    matched="$(
        cd "$root" || exit 3
        tracked="$(git ls-files | wc -l)" || exit 4
        [ "${tracked:-0}" -gt 0 ] || exit 5
        git grep -nE -- '^[[:space:]]*(\$ )?docker[- ]compose '
        rc=$?
        [ "$rc" -le 1 ] || exit 6
        exit 0
    )" || status=$?

    case "$status" in
        0) ;;
        3) echo "cannot enter $root; the scan did not run" >&2; return 1 ;;
        4) echo "could not list tracked files under $root; the scan has no corpus" >&2; return 1 ;;
        5) echo "no tracked files under $root, so the scan would search nothing" >&2; return 1 ;;
        6) echo "the search failed under $root; it searched nothing" >&2; return 1 ;;
        *) echo "the scan failed under $root (exit $status)" >&2; return 1 ;;
    esac

    # Filtering a string already in hand: nothing here can fail in a way that
    # means "did not search".
    printf '%s\n' "$matched" \
        | grep -F -- 'docker-compose.test.yml' \
        | grep -vE -- '-p [^-][^ ]*' || true
}

test_no_documented_command_addresses_the_deployment_project() {
    local offenders
    offenders="$(scan_compose_docs "$REPO_ROOT")" || {
        fail "the documentation scan could not run over this repository" || return 1
    }

    if [ -n "$offenders" ]; then
        printf '    FAIL: compose commands on the test compose file with no -p:\n' >&2
        printf '      %s\n' "$offenders" >&2
        return 1
    fi
}

# Fail-closed paths of scan_compose_docs, each in its own repository.
test_scan_refuses_an_unreachable_root() {
    local out
    if out="$(scan_compose_docs /nonexistent-scan-root 2>&1)"; then
        fail "the scan accepted an unreachable root; got: $out" || return 1
    fi
    # A message distinct from the non-repository case.
    case "$out" in
        *"cannot enter"*) ;;
        *) fail "wrong reason for an unreachable root: $out"; return 1 ;;
    esac
}

test_scan_refuses_a_directory_that_is_not_a_repository() {
    local dir out
    dir="$(mktemp -d)"
    if out="$(scan_compose_docs "$dir" 2>&1)"; then
        rm -rf "$dir"
        fail "the scan accepted a non-repository; got: $out" || return 1
    fi
    rm -rf "$dir"
    case "$out" in
        *"has no corpus"*) ;;
        *) fail "wrong reason for a non-repository: $out"; return 1 ;;
    esac
}

# The case git grep cannot distinguish on its own: nothing to search looks
# exactly like nothing found.
test_scan_refuses_an_empty_corpus() {
    local dir out
    dir="$(mktemp -d)"
    git init -q "$dir"
    if out="$(scan_compose_docs "$dir" 2>&1)"; then
        rm -rf "$dir"
        fail "the scan accepted a repository with no tracked files; got: $out" || return 1
    fi
    rm -rf "$dir"
    case "$out" in
        *"search nothing"*) ;;
        *) fail "wrong reason for an empty corpus: $out"; return 1 ;;
    esac
}

# git ls-files succeeds and git grep fails: the stub fails only grep.
test_scan_refuses_when_the_search_itself_fails() {
    local dir bin out
    dir="$(mktemp -d)"
    git init -q "$dir"
    printf 'nothing interesting\n' >"$dir/doc.md"
    (cd "$dir" && git add doc.md)

    bin="$(mktemp -d)"
    cat >"$bin/git" <<'STUB'
#!/usr/bin/env bash
# Real git for everything except the search, which fails the way an unreadable
# object or a broken index would.
if [ "${1:-}" = "grep" ]; then
    echo "fatal: unable to read tree" >&2
    exit 128
fi
exec /usr/bin/git "$@"
STUB
    chmod +x "$bin/git"

    if out="$(PATH="$bin:$PATH" scan_compose_docs "$dir" 2>&1)"; then
        rm -rf "$dir" "$bin"
        fail "the scan accepted a failed search; got: ${out:-<empty>}" || return 1
    fi
    rm -rf "$dir" "$bin"
    case "$out" in
        *"searched nothing"*) ;;
        *) fail "wrong reason for a failed search: $out"; return 1 ;;
    esac
}

# The positive control. Without it every case above is satisfied by a scan that
# refuses everything, which is its own kind of useless.
test_scan_reports_a_real_offender() {
    local dir out
    dir="$(mktemp -d)"
    git init -q "$dir"
    printf 'docker compose -f docker-compose.test.yml up -d\n' >"$dir/doc.md"
    (cd "$dir" && git add doc.md)

    out="$(scan_compose_docs "$dir")" || { rm -rf "$dir"; fail "the scan failed on a healthy repository" || return 1; }
    rm -rf "$dir"
    case "$out" in
        *"doc.md"*) ;;
        *) fail "the scan missed an unguarded command; got: ${out:-<nothing>}"; return 1 ;;
    esac
}

# And the other half of the control: a guarded command is NOT an offender, so
# the scan is not simply reporting every compose line it sees.
test_scan_passes_a_guarded_command() {
    local dir out
    dir="$(mktemp -d)"
    git init -q "$dir"
    printf 'docker compose -p go-netmon-integration-manual -f docker-compose.test.yml up -d\n' >"$dir/doc.md"
    (cd "$dir" && git add doc.md)

    out="$(scan_compose_docs "$dir")" || { rm -rf "$dir"; fail "the scan failed on a healthy repository" || return 1; }
    rm -rf "$dir"
    [ -z "$out" ] || { fail "a guarded command was reported as an offender: $out"; return 1; }
}

# The guard's first docker call must run with the repository root current, driven from test/integration.
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
        # Rule 2: the directory default, computed with basename, is refused.
        set +e
        # shellcheck disable=SC2016  # $1/$2/$3 belong to the inner bash -c, not this one
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
        # shellcheck disable=SC2016  # $1/$2 belong to the inner bash -c, not this one
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

# The project run-tests.sh addresses must never equal the one clean.sh protects, across environments.
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
        # shellcheck disable=SC2016  # $1 is the inner shell's positional, passed after --
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

        # Refusing outright is safe; distinguish it from a harness that invoked nothing.
        if [ -z "$addressed" ]; then
            case "$run_out" in
                *"refusing to use compose project"*) ;;
                *) fail "[$env_desc] run-tests.sh neither drove compose nor refused; got: $run_out" || failed=1 ;;
            esac
        else
            # Check membership over every addressed project, independent of count.
            if printf '%s\n' "$addressed" | grep -qxF -- "$protected"; then # pipefail-ok: one printf of a few project names, written before grep can exit
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

# The guard refuses the manual project for disposable entry points and permits it for stack.sh.
test_run_tests_refuses_the_manual_project_by_rule() {
    BIN="$(mktemp -d)"; ROOT="$(mktemp -d)"
    make_fake_docker "$BIN" "$ROOT"

    local manual out rc
    manual="$(integration_manual_project)"

    set +e
    # shellcheck disable=SC2016  # $1/$2/$3 belong to the inner bash -c, not this one
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
    # shellcheck disable=SC2016  # $1/$2/$3 belong to the inner bash -c, not this one
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

# Static check that run-tests.sh uses the disposable-project rule and stack.sh the base rule.
test_disposable_entry_points_use_the_disposable_rule() {
    # Match the call anywhere on a non-comment line, strictly by name.
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

# An explicitly empty project reaches rule 1, even through the strict wrapper.
test_an_empty_project_is_refused_as_empty_not_as_the_manual_stack() {
    BIN="$(mktemp -d)"; ROOT="$(mktemp -d)"
    make_fake_docker "$BIN" "$ROOT"

    local out rc
    set +e
    # shellcheck disable=SC2016  # $1/$2 are the inner shell's positionals, passed after --
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
    # Inside PROJECT_PREFIX, so only the manual-stack skip can save it.
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

# docker stderr must stay out of the parsed container ids.
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

# The stack's Prometheus is the deployed one: its version is pinned once,
# as PROMETHEUS_VERSION in Dockerfile.prometheus, and no other file of the stack
# may name a different one. What a remote-write receiver does with a request it
# refuses is a property of its version, so a second, stale version anywhere in
# the stack is a test or a document describing a receiver the suite does not run.
test_integration_stack_names_one_prometheus_version() {
    local dockerfile="$SCRIPT_DIR/Dockerfile.prometheus"
    local pin
    pin="$(grep -m1 -E '^ARG PROMETHEUS_VERSION=' "$dockerfile" | cut -d= -f2)"
    if [ -z "$pin" ]; then
        fail "Dockerfile.prometheus pins no PROMETHEUS_VERSION" || return 1
    fi
    if ! grep -qE '^FROM prom/prometheus:\$\{PROMETHEUS_VERSION\}$' "$dockerfile"; then
        fail "Dockerfile.prometheus does not build FROM the pinned PROMETHEUS_VERSION" || return 1
    fi

    # Every image reference or version assignment in the stack's files. A
    # reference through the variable is the pin itself and is not a second one.
    # This file is left out: its patterns would match themselves.
    local named rc
    named="$(grep -rnoE --exclude=guard-tests.sh \
        'prom/prometheus:[^[:space:]"'"'"'`]+|PROMETHEUS_VERSION=[^[:space:]"'"'"'`]+' \
        "$COMPOSE_FILE" "$SCRIPT_DIR")"
    rc=$?
    if [ "$rc" -ne 0 ]; then
        fail "found no Prometheus version in the stack at all (grep exit $rc)" || return 1
    fi

    local offenders
    offenders="$(printf '%s\n' "$named" \
        | grep -vE ':(prom/prometheus:\$\{PROMETHEUS_VERSION\}|prom/prometheus:'"$pin"'|PROMETHEUS_VERSION='"$pin"')$' || true)"
    if [ -n "$offenders" ]; then
        fail "the stack pins Prometheus $pin, but these name another version: $offenders" || return 1
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
