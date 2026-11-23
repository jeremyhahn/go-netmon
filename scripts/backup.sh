#!/usr/bin/env bash
# Back up a deployed go-netmon stack: every Docker volume it owns, the
# repository configuration bind-mounted into it, and a logical export of the
# web UI's records.
#
# CONSISTENCY
#
# The backup is COLD. Every service in the compose project is stopped, the
# volumes are copied at rest, and the services that were running are started
# again. There is no hot mode, and that is deliberate:
#
#   * The web UI catalog, the web UI records and the poller's MIB metadata are
#     all PebbleDB stores. Pebble is single-writer and holds an exclusive LOCK.
#     Copying a live store can capture a MANIFEST that names sstables the copy
#     took before they were durable, or a half-written WAL tail, which replays
#     into a corrupt store on open. Pebble exposes Checkpoint() in process, but
#     neither the poller nor the web UI offers an endpoint that calls it, so
#     there is nothing to ask over the wire.
#
#   * The Prometheus TSDB head block lives in memory plus a WAL. The supported
#     hot mechanism is POST /api/v1/admin/tsdb/snapshot, which requires
#     --web.enable-admin-api. This deployment's docker-compose.yml passes
#     --web.enable-lifecycle and --web.enable-remote-write-receiver but NOT
#     --web.enable-admin-api, and the endpoint answers
#     {"errorType":"unavailable","error":"admin APIs disabled"}.
#
# A hot copy of these stores is not "slightly risky", it is unverifiable: the
# damage is invisible until the restore, which is the worst possible time to
# find it. Stopping the stack for the length of a volume copy is the honest
# trade, and it makes the archive's consistency a property of the method rather
# than of luck.
#
# The stop also gives one consistency point ACROSS volumes. The web UI's device
# records reference MIB ids in its catalog, whose files live in the poller's
# volume; copying those volumes at different instants can produce a backup
# whose devices reference MIBs the archive does not contain.
#
# THE MIB SOURCE TREE
#
# One component, "mibs", is not a volume at all: it is the host directory
# bind-mounted read-only at /app/mibs in both the poller and the web UI. It is
# the source most of the imported catalog was parsed from. Without it the
# archive carries the parsed catalogs but not the text they came from, so a
# restored deployment resolves fine until something triggers a re-parse or a
# re-import and then cannot. This repository has no version control, so those
# files exist in exactly one place.
#
# It is copied inside the stopped window with everything else. It costs a second
# or two of extra downtime and buys one "as of" instant for the whole archive,
# which is the property the paragraphs above promise.

set -euo pipefail

netmon_prog="backup.sh"
# shellcheck source=lib/netmon-stack.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/netmon-stack.sh"

usage() {
    cat <<'EOF'
Usage: backup.sh [options]

Creates a backup directory containing one compressed tar per Docker volume,
the bind-mounted repository configuration, the MIB source tree, a logical
export of the web UI's records, and a manifest describing all of it.

Options:
  -o, --output DIR    Parent directory for backups (default: <repo>/backups)
  -n, --name NAME     Backup directory name (default: netmon-<UTC timestamp>)
      --only LIST     Comma-separated components to back up
      --skip LIST     Comma-separated components to omit
      --no-logical    Skip the REST logical export
      --keep-stopped  Do not start the stack again afterwards
  -h, --help          Show this help

Components: config, mibs, webui, poller, metrics, grafana

  config   bind-mounted repository config (config.yaml, prometheus.yml)
  mibs     MIB source tree bind-mounted read-only into the poller and web UI
  webui    devices, profiles, MIB assignments, poll intervals, MIB catalog
  poller   poller MIB metadata store and uploaded MIB files
  metrics  Prometheus TSDB - all metric history
  grafana  Grafana database and dashboards

Environment:
  NETMON_PROJECT       Compose project name (default: go-netmon)
  NETMON_WEBUI_URL     Web UI base URL for the logical export
  NETMON_HELPER_IMAGE  Image used to stream volume contents
  NETMON_MIB_TREE      Host MIB source directory (default: read from the
                       compose file's bind mount at /app/mibs)

The stack is stopped for the duration of the volume copy. See the header of
this script for why a hot backup is not offered.
EOF
}

# --- arguments ------------------------------------------------------------

output_dir=""
backup_name=""
only_list=""
skip_list=""
do_logical=1
keep_stopped=0

while [ $# -gt 0 ]; do
    case "$1" in
    -o | --output)
        output_dir="${2:-}"
        shift 2
        ;;
    -n | --name)
        backup_name="${2:-}"
        shift 2
        ;;
    --only)
        only_list="${2:-}"
        shift 2
        ;;
    --skip)
        skip_list="${2:-}"
        shift 2
        ;;
    --no-logical)
        do_logical=0
        shift
        ;;
    --keep-stopped)
        keep_stopped=1
        shift
        ;;
    -h | --help)
        usage
        exit 0
        ;;
    *) die "unknown argument: $1 (try --help)" ;;
    esac
done

[ -n "$only_list" ] && [ -n "$skip_list" ] && die "--only and --skip are mutually exclusive"

output_dir="${output_dir:-$NETMON_REPO_ROOT/backups}"
timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
backup_name="${backup_name:-netmon-$timestamp}"
backup_dir="$output_dir/$backup_name"

# --- component selection --------------------------------------------------

validate_component_list "${only_list:-}"
validate_component_list "${skip_list:-}"

selected=()
if [ -n "$only_list" ]; then
    mapfile -t selected < <(parse_component_list "$only_list")
else
    mapfile -t skipped < <(parse_component_list "${skip_list:-}")
    for c in "${NETMON_COMPONENTS[@]}"; do
        keep=1
        for s in "${skipped[@]:-}"; do
            [ "$c" = "$s" ] && keep=0
        done
        [ "$keep" -eq 1 ] && selected+=("$c")
    done
fi
[ "${#selected[@]}" -eq 0 ] && die "no components selected"

# --- preflight ------------------------------------------------------------

require_tools jq tar gzip sha256sum awk date
require_docker
[ -f "$NETMON_COMPOSE_FILE" ] || die "compose file not found: $NETMON_COMPOSE_FILE"

assert_project_ownership

[ -e "$backup_dir" ] && die "backup directory already exists: $backup_dir"

info "project:    $NETMON_PROJECT"
info "compose:    $NETMON_COMPOSE_FILE"
info "components: ${selected[*]}"
info "destination: $backup_dir"

# Resolve and prove ownership of every volume BEFORE creating any output or
# stopping anything. A backup that discovers a problem after taking the stack
# down has already caused an outage for nothing.
declare -a vol_components=() vol_docker=()
for c in "${selected[@]}"; do
    logical_vol="$(component_volume "$c")"
    [ -z "$logical_vol" ] && continue
    dvol="$(docker_volume_for "$logical_vol")"
    assert_volume_owned "$dvol"
    vol_components+=("$c")
    vol_docker+=("$dvol")
done

report_unmanaged_volumes

# Same principle for the MIB source tree: locate it, and prove it is there and
# non-empty, before anything is stopped. The path is read out of the rendered
# compose file rather than assumed, and a deployment that mounts nothing at the
# MIB target fails here with instructions rather than being guessed at.
want_mibs=0
if selection_has mibs "${selected[@]}"; then
    want_mibs=1
    resolve_mib_tree
    # Normalises the path and rejects absurd ones. Backing up is not
    # destructive, but "tar -C / -cf -" on a mistyped override is its own kind
    # of accident, and the manifest should record a resolved path.
    assert_safe_tree_dir "$NETMON_MIB_TREE"
    NETMON_MIB_TREE="$NETMON_SAFE_TREE_DIR"
    [ -d "$NETMON_MIB_TREE" ] ||
        die "MIB source tree not found: $NETMON_MIB_TREE ($NETMON_MIB_TREE_SOURCE)"
    mib_tree_files="$(tree_file_count "$NETMON_MIB_TREE")"
    [ "$mib_tree_files" -gt 0 ] ||
        die "MIB source tree holds no files: $NETMON_MIB_TREE"
    info "mib tree:   $NETMON_MIB_TREE ($mib_tree_files files, $NETMON_MIB_TREE_SOURCE)"
fi

# --- restart guard --------------------------------------------------------

# Services running when we started. The stack is returned to exactly this set,
# including on any failure or interrupt: a backup must never be the reason
# monitoring stayed down.
mapfile -t running_services < <(compose_running_services)

restarted=0
restart_stack() {
    [ "$restarted" -eq 1 ] && return 0
    restarted=1
    [ "$keep_stopped" -eq 1 ] && {
        warn "--keep-stopped: leaving the stack stopped"
        return 0
    }
    [ "${#running_services[@]}" -eq 0 ] && return 0
    info "starting services again: ${running_services[*]}"
    compose start "${running_services[@]}" >/dev/null 2>&1 ||
        warn "could not start services again; check 'docker compose -p $NETMON_PROJECT ps'"
}

# --- logical export -------------------------------------------------------

logical_status="skipped"
logical_counts='{}'

fetch_json() {
    curl -fsS --max-time 60 "$1" 2>/dev/null
}

export_logical() {
    local dir="$backup_dir/logical" id
    mkdir -p "$dir"

    local devices
    devices="$(fetch_json "$NETMON_WEBUI_URL/api/devices")" || return 1
    printf '%s' "$devices" | jq '.' >"$dir/devices.json"

    local profiles
    profiles="$(fetch_json "$NETMON_WEBUI_URL/api/profiles")" || return 1
    printf '%s' "$profiles" | jq '.' >"$dir/profiles.json"

    local mibs
    mibs="$(fetch_json "$NETMON_WEBUI_URL/api/mibs")" || return 1
    printf '%s' "$mibs" | jq '.' >"$dir/mibs.json"

    fetch_json "$NETMON_WEBUI_URL/api/poller/config" | jq '.' >"$dir/poller-config.json" || return 1

    # Per-device and per-profile MIB assignments, including the per-assignment
    # record that carries enablement and any OID filters. These are the
    # relationships a logical restore would have to recreate, so they are
    # captured explicitly rather than inferred from the catalog.
    local out='[]'
    while read -r id; do
        [ -z "$id" ] && continue
        local dm dp
        dm="$(fetch_json "$NETMON_WEBUI_URL/api/devices/$id/mibs")" || return 1
        dp="$(fetch_json "$NETMON_WEBUI_URL/api/devices/$id/profiles")" || dp='null'
        local assignments='[]' mid
        while read -r mid; do
            [ -z "$mid" ] && continue
            local rec
            rec="$(fetch_json "$NETMON_WEBUI_URL/api/devices/$id/mibs/$mid/assignment")" || rec='null'
            assignments="$(jq -c --argjson m "$mid" --argjson r "${rec:-null}" \
                '. + [{mib_id: $m, assignment: $r}]' <<<"$assignments")"
        done < <(jq -r '.direct_mibs[]?.id // empty' <<<"$dm")
        out="$(jq -c --argjson i "$id" --argjson m "$dm" --argjson p "${dp:-null}" \
            --argjson a "$assignments" \
            '. + [{device_id: $i, mibs: $m, profiles: $p, direct_assignments: $a}]' <<<"$out")"
    done < <(jq -r '.devices[]?.id // empty' <<<"$devices")
    jq '.' <<<"$out" >"$dir/device-mibs.json"

    out='[]'
    while read -r id; do
        [ -z "$id" ] && continue
        local pm assignments='[]' mid
        pm="$(fetch_json "$NETMON_WEBUI_URL/api/profiles/$id/mibs")" || return 1
        while read -r mid; do
            [ -z "$mid" ] && continue
            local rec
            rec="$(fetch_json "$NETMON_WEBUI_URL/api/profiles/$id/mibs/$mid/assignment")" || rec='null'
            assignments="$(jq -c --argjson m "$mid" --argjson r "${rec:-null}" \
                '. + [{mib_id: $m, assignment: $r}]' <<<"$assignments")"
        done < <(jq -r '.mibs[]?.id // empty' <<<"$pm")
        out="$(jq -c --argjson i "$id" --argjson m "$pm" --argjson a "$assignments" \
            '. + [{profile_id: $i, mibs: $m, assignments: $a}]' <<<"$out")"
    done < <(jq -r '.profiles[]?.id // empty' <<<"$profiles")
    jq '.' <<<"$out" >"$dir/profile-mibs.json"

    logical_counts="$(jq -n \
        --argjson devices "$(jq '.devices | length' "$dir/devices.json")" \
        --argjson profiles "$(jq '.profiles | length' "$dir/profiles.json")" \
        --argjson mibs "$(jq '.mibs | length' "$dir/mibs.json")" \
        '{devices: $devices, profiles: $profiles, mibs: $mibs}')"
    return 0
}

# --- run ------------------------------------------------------------------

mkdir -p "$backup_dir"
trap 'restart_stack' EXIT
trap 'trap - EXIT; restart_stack; exit 130' INT
trap 'trap - EXIT; restart_stack; exit 143' TERM

# The logical export must run while the stack is up, so it happens first. It is
# a convenience artifact: its failure is recorded and warned about, but it does
# not fail the physical backup, which is the thing that actually restores.
if [ "$do_logical" -eq 1 ]; then
    info "exporting logical records from $NETMON_WEBUI_URL"
    if export_logical; then
        logical_status="ok"
    else
        logical_status="unavailable"
        warn "logical export failed (web UI unreachable or incomplete); physical backup continues"
        warn "the manifest records logical.status=unavailable"
    fi
fi

if [ "${#running_services[@]}" -gt 0 ]; then
    info "stopping services for a consistent copy: ${running_services[*]}"
    compose stop "${running_services[@]}" >/dev/null
else
    info "no running services; copying volumes as they are"
fi

mkdir -p "$backup_dir/volumes"
volumes_json='[]'
for i in "${!vol_components[@]}"; do
    c="${vol_components[$i]}"
    dvol="${vol_docker[$i]}"
    logical_vol="$(component_volume "$c")"
    # Members are named after the component, not the compose volume. The
    # compose volume "go-netmon-data" is name-identical to an unrelated stray
    # volume on the deployment host, and a filename that cannot be told apart
    # from an object the tooling refuses to touch is a filename waiting to be
    # misread during a recovery.
    member="volumes/$c.tar.gz"
    path="$backup_dir/$member"

    info "archiving $dvol -> $member"
    volume_export "$dvol" | gzip -6 >"$path"

    entries="$(tar_entry_count "$path")" ||
        die "member written but unreadable: $member"
    bytes="$(stat -c %s "$path")"
    sum="$(sha256_of "$path")"

    info "  $entries entries, $(human_bytes "$bytes") compressed"
    volumes_json="$(jq -c \
        --arg component "$c" --arg compose_volume "$logical_vol" \
        --arg docker_volume "$dvol" --arg member "$member" \
        --argjson bytes "$bytes" --argjson entries "$entries" --arg sha256 "$sum" \
        '. + [{component: $component, compose_volume: $compose_volume,
               docker_volume: $docker_volume, member: $member,
               bytes: $bytes, entries: $entries, sha256: $sha256}]' <<<"$volumes_json")"
done

# --- MIB source tree ------------------------------------------------------

# Archived exactly like a volume - own member, own entry count, own checksum -
# because it restores like one: as a whole directory, replaced outright. It is
# recorded in trees[] rather than volumes[] so that nothing can mistake a host
# path for a Docker volume during a recovery.
trees_json='[]'
if [ "$want_mibs" -eq 1 ]; then
    mkdir -p "$backup_dir/trees"
    member="trees/mibs.tar.gz"
    path="$backup_dir/$member"

    info "archiving $NETMON_MIB_TREE -> $member"
    tree_export "$NETMON_MIB_TREE" | gzip -6 >"$path"

    entries="$(tar_entry_count "$path")" ||
        die "member written but unreadable: $member"
    bytes="$(stat -c %s "$path")"
    sum="$(sha256_of "$path")"

    info "  $entries entries, $mib_tree_files files, $(human_bytes "$bytes") compressed"
    trees_json="$(jq -c \
        --arg component "mibs" --arg source_path "$NETMON_MIB_TREE" \
        --arg mount_target "$NETMON_MIB_MOUNT_TARGET" --arg member "$member" \
        --argjson bytes "$bytes" --argjson entries "$entries" \
        --argjson files "$mib_tree_files" \
        --argjson source_bytes "$(tree_bytes "$NETMON_MIB_TREE")" \
        --arg sha256 "$sum" \
        '. + [{component: $component, source_path: $source_path,
               mount_target: $mount_target, member: $member,
               bytes: $bytes, entries: $entries, files: $files,
               source_bytes: $source_bytes, sha256: $sha256}]' <<<"$trees_json")"
fi

# --- configuration --------------------------------------------------------

config_json='[]'
want_config=0
selection_has config "${selected[@]}" && want_config=1

mkdir -p "$backup_dir/config" "$backup_dir/config/reference"
for f in "${NETMON_CONFIG_FILES[@]}"; do
    src="$NETMON_REPO_ROOT/$f"
    [ -f "$src" ] || {
        warn "config file missing, not backed up: $f"
        continue
    }
    cp -p "$src" "$backup_dir/config/$f"
    config_json="$(jq -c --arg path "$f" --arg sha256 "$(sha256_of "$src")" \
        --argjson restorable "$([ "$want_config" -eq 1 ] && echo true || echo false)" \
        '. + [{path: $path, sha256: $sha256, restorable: $restorable}]' <<<"$config_json")"
done

# Recorded for provenance and compatibility checking only. Never restored.
for f in "${NETMON_REFERENCE_FILES[@]}"; do
    src="$NETMON_REPO_ROOT/$f"
    [ -f "$src" ] && cp -p "$src" "$backup_dir/config/reference/$f"
done

restart_stack
trap - EXIT INT TERM

# --- manifest -------------------------------------------------------------

images_json='[]'
while IFS=$'\t' read -r svc image id; do
    [ -z "$svc" ] && continue
    images_json="$(jq -c --arg service "$svc" --arg image "$image" --arg image_id "$id" \
        '. + [{service: $service, image: $image, image_id: $image_id}]' <<<"$images_json")"
done < <(compose_service_images)

compose_sha="$(sha256_of "$NETMON_COMPOSE_FILE")"
compose_volumes_json="$(compose_volume_names | jq -R . | jq -sc .)"
selected_json="$(printf '%s\n' "${selected[@]}" | jq -R . | jq -sc .)"

jq -n \
    --argjson schema_version "$NETMON_MANIFEST_SCHEMA" \
    --arg backup_name "$backup_name" \
    --arg created_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --arg created_by "${USER:-unknown}@$(hostname)" \
    --arg consistency "cold-offline" \
    --arg tool "backup.sh" \
    --arg project "$NETMON_PROJECT" \
    --arg compose_file "$(basename "$NETMON_COMPOSE_FILE")" \
    --arg compose_file_sha256 "$compose_sha" \
    --argjson compose_volumes "$compose_volumes_json" \
    --argjson components "$selected_json" \
    --argjson volumes "$volumes_json" \
    --argjson trees "$trees_json" \
    --argjson config_files "$config_json" \
    --argjson images "$images_json" \
    --arg logical_status "$logical_status" \
    --argjson logical_counts "$logical_counts" \
    --arg docker_version "$("$DOCKER" version --format '{{.Server.Version}}' 2>/dev/null || echo unknown)" \
    --arg compose_version "$("$DOCKER" compose version --short 2>/dev/null || echo unknown)" \
    '{
        schema_version: $schema_version,
        backup_name: $backup_name,
        created_at: $created_at,
        created_by: $created_by,
        tool: $tool,
        consistency: $consistency,
        components: $components,
        compose: {
            project: $project,
            file: $compose_file,
            file_sha256: $compose_file_sha256,
            volumes: $compose_volumes
        },
        images: $images,
        volumes: $volumes,
        trees: $trees,
        config_files: $config_files,
        logical: { status: $logical_status, counts: $logical_counts },
        host: { docker_version: $docker_version, compose_version: $compose_version }
    }' >"$(manifest_path "$backup_dir")"

# checksums.sha256 covers every file in the backup including the manifest, so
# tampering or bit rot anywhere in the tree is detectable with sha256sum -c
# alone, without this tooling.
(
    cd "$backup_dir"
    # Written to a temporary file and moved into place so the checksum list is
    # not itself a member of the tree being hashed.
    find . -type f -print0 | sort -z | xargs -0 sha256sum >../."$backup_name".sums
    mv "../.$backup_name.sums" checksums.sha256
)

rule
info "backup complete: $backup_dir"
info "  consistency: cold-offline (stack stopped during volume copy)"
info "  components:  ${selected[*]}"
info "  logical:     $logical_status"
info "  total size:  $(human_bytes "$(du -sb "$backup_dir" | cut -f1)")"
rule
info "verify with: scripts/restore.sh --from '$backup_dir' --verify-only"
