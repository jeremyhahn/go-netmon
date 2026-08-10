#!/usr/bin/env bash
# Restore a go-netmon stack from a backup produced by scripts/backup.sh.
#
# THIS IS DESTRUCTIVE. It replaces the contents of Docker volumes and the
# repository configuration files bind-mounted into the stack. It is built so
# that every way it can fail leaves the current data intact:
#
#   1. The whole archive is verified first - manifest, per-file checksums and a
#      full decompression of every member. A truncated or corrupt backup is
#      rejected before anything is stopped or overwritten.
#   2. The backup's compose project must match the target project. A backup
#      taken from a different deployment is refused rather than half-applied.
#   3. The target project must contain no container this repository's compose
#      file did not create, and every volume must carry the project's compose
#      label, so a name collision cannot redirect the restore onto someone
#      else's data.
#   4. Nothing happens without --yes. Without it the plan is printed and the
#      command exits non-zero.
#   5. Unless --no-safety-backup is given, the current state is backed up first,
#      so a restore of the wrong archive is itself recoverable.
#   6. The "mibs" component writes into the operator's working tree rather than
#      into a Docker volume. When that directory already holds files, replacing
#      it needs --allow-tree-overwrite on top of --yes. See below.
#
# THE MIB SOURCE TREE AND ITS GATE
#
# Every other component lands inside a Docker volume: a named object this
# tooling owns, whose contents no one edits by hand. The MIB source tree is a
# directory in the operator's checkout, and this repository has no version
# control - files deleted there are gone. Overwriting it is therefore a
# categorically different act from overwriting a volume, and it is gated
# separately:
#
#   * The destination is empty or absent -> nothing is destroyed, so the tree is
#     simply created. This is the fresh-host disaster path and it needs no extra
#     flag.
#   * The destination already holds files -> --allow-tree-overwrite is required.
#     Without it the restore fails, loudly, before anything is stopped or
#     written. It does not "skip the component and carry on": that would print
#     "restore complete" over a deployment whose MIB sources were never put
#     back, which is the exact half-restore this component exists to prevent.
#
# The replacement is wholesale, matching volume_import and the exact-copy
# semantics of the config files: the directory is emptied, then extracted into.
# Merging would leave modules from a newer tree alongside the restored ones,
# and the deployment would resolve against a set matching neither.

set -euo pipefail

netmon_prog="restore.sh"
# shellcheck source=lib/netmon-stack.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/netmon-stack.sh"

usage() {
    cat <<'EOF'
Usage: restore.sh --from DIR [options]

Restores Docker volumes and bind-mounted configuration from a backup directory
created by backup.sh. Refuses to change anything unless --yes is given.

Options:
  -f, --from DIR         Backup directory to restore from (required)
      --yes              Actually perform the restore (required to change data)
      --verify-only      Verify the backup's integrity and exit
      --only LIST        Comma-separated components to restore
      --skip LIST        Comma-separated components to omit
      --no-safety-backup Do not back up current state before overwriting it
      --force-project    Allow restoring a backup taken from another project
      --allow-tree-overwrite
                         Permit replacing a non-empty MIB source tree on the
                         host. Required, in addition to --yes, whenever the
                         'mibs' component is restored over existing files.
      --mib-tree DIR     Restore the MIB source tree into DIR instead of the
                         directory the compose file bind-mounts
  -h, --help             Show this help

Components: config, mibs, webui, poller, metrics, grafana

  config   bind-mounted repository config (config.yaml, prometheus.yml)
  mibs     MIB source tree bind-mounted read-only into the poller and web UI
  webui    devices, profiles, MIB assignments, poll intervals, MIB catalog
  poller   poller MIB metadata store and uploaded MIB files
  metrics  Prometheus TSDB - all metric history
  grafana  Grafana database and dashboards

Restoring 'mibs' writes into the host directory the stack bind-mounts, i.e.
your working copy - not a Docker volume. The directory's contents are replaced
outright. See the header of this script.

Environment:
  NETMON_PROJECT       Compose project name (default: go-netmon)
  NETMON_HELPER_IMAGE  Image used to stream volume contents
  NETMON_MIB_TREE      Host MIB source directory (default: read from the
                       compose file's bind mount at /app/mibs)
EOF
}

# --- arguments ------------------------------------------------------------

backup_dir=""
assume_yes=0
verify_only=0
only_list=""
skip_list=""
safety_backup=1
force_project=0
allow_tree_overwrite=0
mib_tree_opt=""

while [ $# -gt 0 ]; do
    case "$1" in
    -f | --from)
        backup_dir="${2:-}"
        shift 2
        ;;
    --yes)
        assume_yes=1
        shift
        ;;
    --verify-only)
        verify_only=1
        shift
        ;;
    --only)
        only_list="${2:-}"
        shift 2
        ;;
    --skip)
        skip_list="${2:-}"
        shift 2
        ;;
    --no-safety-backup)
        safety_backup=0
        shift
        ;;
    --force-project)
        force_project=1
        shift
        ;;
    --allow-tree-overwrite)
        allow_tree_overwrite=1
        shift
        ;;
    --mib-tree)
        mib_tree_opt="${2:-}"
        shift 2
        ;;
    -h | --help)
        usage
        exit 0
        ;;
    *) die "unknown argument: $1 (try --help)" ;;
    esac
done

[ -n "$backup_dir" ] || die "--from is required (try --help)"
[ -n "$only_list" ] && [ -n "$skip_list" ] && die "--only and --skip are mutually exclusive"
[ -d "$backup_dir" ] || die "backup directory not found: $backup_dir"
backup_dir="$(cd "$backup_dir" && pwd)"

require_tools jq tar gzip sha256sum awk
manifest="$(manifest_path "$backup_dir")"
[ -f "$manifest" ] || die "not a backup directory (no manifest.json): $backup_dir"

# --- stage 1: verify the archive -----------------------------------------
#
# Everything here reads the backup only. It runs before any Docker call, so a
# damaged archive cannot get as far as stopping the stack.

info "verifying backup: $backup_dir"

jq -e . "$manifest" >/dev/null 2>&1 || die "manifest.json is not valid JSON"

schema="$(jq -r '.schema_version // empty' "$manifest")"
[ -n "$schema" ] || die "manifest has no schema_version"
[ "$schema" -le "$NETMON_MANIFEST_SCHEMA" ] 2>/dev/null ||
    die "backup schema_version $schema is newer than this tool supports ($NETMON_MANIFEST_SCHEMA)"

for field in backup_name created_at consistency compose volumes; do
    jq -e "has(\"$field\")" "$manifest" >/dev/null ||
        die "manifest is missing required field: $field"
done

backup_project="$(jq -r '.compose.project // empty' "$manifest")"
[ -n "$backup_project" ] || die "manifest does not record a compose project"

# checksums.sha256 covers every file including the manifest. Verifying it first
# means a manifest edited to match a tampered member is still caught.
if [ -f "$backup_dir/checksums.sha256" ]; then
    info "  checking file checksums"
    (cd "$backup_dir" && sha256sum --quiet -c checksums.sha256) ||
        die "checksum verification failed: the backup is corrupt or has been modified"
else
    die "backup has no checksums.sha256; refusing to restore an unverifiable archive"
fi

# Per-member verification. The checksum proves the bytes are the bytes that
# were written; decompressing proves those bytes are a readable archive whose
# shape matches what the backup recorded. A member truncated at write time
# would pass a naive "file exists" check and fail halfway through wiping a
# live volume.
# Volume members and host-tree members are verified by one loop over one list.
# They are separate arrays in the manifest so nothing can confuse a host path
# with a Docker volume, but their integrity fields are identical and a member
# that skipped a check because it lived in the "other" array would be a gap
# nobody notices until a restore. ".trees // []" keeps schema-1 archives, which
# have no trees at all, working unchanged.
member_count="$(jq '(.volumes | length) + ((.trees // []) | length)' "$manifest")"
info "  checking $member_count member(s)"
while IFS=$'\t' read -r member sha bytes entries; do
    [ -z "$member" ] && continue
    path="$backup_dir/$member"
    [ -f "$path" ] || die "manifest references a missing member: $member"

    actual_bytes="$(stat -c %s "$path")"
    [ "$actual_bytes" = "$bytes" ] ||
        die "member $member is $actual_bytes bytes, manifest says $bytes (truncated or modified)"

    actual_sha="$(sha256_of "$path")"
    [ "$actual_sha" = "$sha" ] ||
        die "member $member failed its SHA-256 check (manifest $sha, actual $actual_sha)"

    actual_entries="$(tar_entry_count "$path")" ||
        die "member $member is not a readable gzip tar stream"
    [ "$actual_entries" = "$entries" ] ||
        die "member $member holds $actual_entries entries, manifest says $entries"

    info "    ok  $member  ($entries entries, $(human_bytes "$bytes"))"
done < <(jq -r '(.volumes + (.trees // []))[] | [.member, .sha256, .bytes, .entries] | @tsv' "$manifest")

info "backup verified"

if [ "$verify_only" -eq 1 ]; then
    info "--verify-only: nothing was changed"
    exit 0
fi

# --- stage 2: component selection ----------------------------------------

mapfile -t available < <(jq -r '.components[]' "$manifest")

validate_component_list "${only_list:-}"
validate_component_list "${skip_list:-}"

selected=()
if [ -n "$only_list" ]; then
    mapfile -t requested < <(parse_component_list "$only_list")
    for r in "${requested[@]}"; do
        found=0
        for a in "${available[@]}"; do [ "$a" = "$r" ] && found=1; done
        [ "$found" -eq 1 ] || die "component '$r' is not present in this backup (has: ${available[*]})"
        selected+=("$r")
    done
else
    mapfile -t skipped < <(parse_component_list "${skip_list:-}")
    for a in "${available[@]}"; do
        keep=1
        for s in "${skipped[@]:-}"; do [ "$a" = "$s" ] && keep=0; done
        [ "$keep" -eq 1 ] && selected+=("$a")
    done
fi
[ "${#selected[@]}" -eq 0 ] && die "no components selected"

# The web UI's device records reference MIB ids in its catalog, and those MIBs'
# source files live in the poller volume. Restoring one without the other can
# leave devices pointing at MIBs whose files are absent. It is a legitimate
# thing to want, so it warns rather than refuses.
has_webui=0 has_poller=0
for c in "${selected[@]}"; do
    [ "$c" = "webui" ] && has_webui=1
    [ "$c" = "poller" ] && has_poller=1
done
if [ "$has_webui" -ne "$has_poller" ]; then
    warn "restoring 'webui' and 'poller' separately: device MIB assignments reference"
    warn "MIB files in the poller volume, so the two can end up describing different sets"
fi

# --- stage 3: target compatibility ---------------------------------------

require_docker
[ -f "$NETMON_COMPOSE_FILE" ] || die "compose file not found: $NETMON_COMPOSE_FILE"

if [ "$backup_project" != "$NETMON_PROJECT" ]; then
    if [ "$force_project" -eq 1 ]; then
        warn "backup was taken from project '$backup_project', restoring into '$NETMON_PROJECT'"
        warn "--force-project given: proceeding despite the mismatch"
    else
        log ""
        log "Project mismatch:"
        log "  backup was taken from compose project: $backup_project"
        log "  target compose project:                $NETMON_PROJECT"
        log ""
        log "Restoring across projects writes one deployment's data into another."
        log "If that is genuinely intended, re-run with --force-project, or set"
        log "NETMON_PROJECT=$backup_project to target the original deployment."
        die "refusing to restore a backup from a different compose project"
    fi
fi

assert_project_ownership

# Every volume this restore needs must be defined by the target compose file.
# A target that no longer defines a volume the backup carries is an
# incompatible deployment, not something to partially apply.
mapfile -t target_volumes < <(compose_volume_names)
for c in "${selected[@]}"; do
    lv="$(component_volume "$c")"
    [ -z "$lv" ] && continue
    found=0
    for tv in "${target_volumes[@]}"; do [ "$tv" = "$lv" ] && found=1; done
    [ "$found" -eq 1 ] ||
        die "target compose file does not define volume '$lv' needed by component '$c'"
done

# Resolve where the MIB source tree would land, and how much is already there.
# This is the target deployment's answer, not the backup's: restoring into a
# different checkout is normal, so the destination comes from the compose file
# being restored into (or --mib-tree), and a difference from the recorded
# origin is reported rather than silently followed.
mib_tree_dest=""
mib_tree_files_now=0
mib_tree_bytes_now=0
mib_tree_existing=0
if selection_has mibs "${selected[@]}"; then
    jq -e '(.trees // []) | map(select(.component == "mibs")) | length == 1' \
        "$manifest" >/dev/null ||
        die "manifest lists component 'mibs' but carries no MIB tree member"

    [ -n "$mib_tree_opt" ] && NETMON_MIB_TREE="$mib_tree_opt"
    resolve_mib_tree
    assert_safe_tree_dir "$NETMON_MIB_TREE"
    mib_tree_dest="$NETMON_SAFE_TREE_DIR"

    mib_tree_files_now="$(tree_file_count "$mib_tree_dest")"
    mib_tree_bytes_now="$(tree_bytes "$mib_tree_dest")"
    [ "$mib_tree_files_now" -gt 0 ] && mib_tree_existing=1

    mib_tree_origin="$(jq -r '.trees[] | select(.component == "mibs") | .source_path' "$manifest")"
    if [ "$mib_tree_origin" != "$mib_tree_dest" ]; then
        warn "MIB tree was backed up from '$mib_tree_origin' and will be restored into '$mib_tree_dest'"
        warn "  ($NETMON_MIB_TREE_SOURCE)"
    fi
fi

# Image drift is reported, not enforced. Restoring data onto a newer build is a
# normal upgrade path; restoring onto a build that cannot read it is not
# something an image tag can tell us, so the operator gets the facts.
while IFS=$'\t' read -r svc image; do
    [ -z "$svc" ] && continue
    backup_image="$(jq -r --arg s "$svc" '.images[]? | select(.service == $s) | .image' "$manifest")"
    if [ -n "$backup_image" ] && [ "$backup_image" != "$image" ]; then
        warn "service '$svc' image differs: backup '$backup_image', target '$image'"
    fi
done < <(compose config --format json 2>/dev/null |
    jq -r '.services | to_entries[] | "\(.key)\t\(.value.image // "")"')

# --- stage 4: the plan ----------------------------------------------------

rule
log "RESTORE PLAN"
rule
log "  backup:      $(jq -r '.backup_name' "$manifest")"
log "  taken:       $(jq -r '.created_at' "$manifest") by $(jq -r '.created_by // "unknown"' "$manifest")"
log "  consistency: $(jq -r '.consistency' "$manifest")"
log "  project:     $NETMON_PROJECT"
log ""
log "  The following will be OVERWRITTEN:"

for c in "${selected[@]}"; do
    if [ "$c" = "mibs" ]; then
        incoming="$(jq -r '.trees[] | select(.component == "mibs") | .entries' "$manifest")"
        log "    tree    $mib_tree_dest"
        log "              holds:   $(component_description mibs)"
        log "              source:  $NETMON_MIB_TREE_SOURCE"
        # Spelled out because this is the one destination that is not a Docker
        # volume. An operator scanning the plan must not have to infer it.
        log "              NOTE:    this is a HOST DIRECTORY in your working tree,"
        log "                       not a Docker volume"
        if [ "$mib_tree_existing" -eq 1 ]; then
            log "              current: $mib_tree_files_now files, $(human_bytes "$mib_tree_bytes_now")"
            log "              restore: $incoming entries from the backup"
            log "                       (the directory is EMPTIED first, then extracted"
            log "                       into - existing files are deleted, not merged)"
            if [ "$allow_tree_overwrite" -eq 1 ]; then
                log "              gate:    --allow-tree-overwrite given"
            else
                log "              gate:    REFUSED - replacing $mib_tree_files_now existing files"
                log "                       requires --allow-tree-overwrite"
            fi
        else
            log "              current: empty or absent - nothing will be deleted"
            log "              restore: $incoming entries from the backup"
            log "              gate:    not required (no existing files)"
        fi
        continue
    fi

    lv="$(component_volume "$c")"
    if [ -z "$lv" ]; then
        while read -r f; do
            [ -z "$f" ] && continue
            log "    file    $NETMON_REPO_ROOT/$f"
        done < <(jq -r '.config_files[]? | select(.restorable) | .path' "$manifest")
        continue
    fi
    dvol="$(docker_volume_for "$lv")"
    if volume_exists "$dvol"; then
        assert_volume_owned "$dvol"
        current="$(human_bytes "$(volume_bytes "$dvol")")"
    else
        current="does not exist, will be created"
    fi
    incoming="$(jq -r --arg c "$c" '.volumes[] | select(.component == $c) | .entries' "$manifest")"
    log "    volume  $dvol"
    log "              holds:   $(component_description "$c")"
    log "              current: $current"
    log "              restore: $incoming entries from the backup (contents replaced entirely)"
done

skipped_out=()
for a in "${available[@]}"; do
    keep=0
    for s in "${selected[@]}"; do [ "$a" = "$s" ] && keep=1; done
    [ "$keep" -eq 0 ] && skipped_out+=("$a")
done
if [ "${#skipped_out[@]}" -gt 0 ]; then
    log ""
    log "  Left untouched (present in the backup, not selected): ${skipped_out[*]}"
fi
rule

if [ "$assume_yes" -ne 1 ]; then
    log ""
    if [ "$mib_tree_existing" -eq 1 ] && [ "$allow_tree_overwrite" -ne 1 ]; then
        log "Nothing has been changed. Re-run with --yes --allow-tree-overwrite to"
        log "perform this restore: it would replace the $mib_tree_files_now files in"
        log "  $mib_tree_dest"
        log "Add --skip mibs instead to leave that directory alone."
    else
        log "Nothing has been changed. Re-run with --yes to perform this restore."
    fi
    exit 1
fi

# The gate. Fatal, and reached before the stack is stopped or a safety backup
# is taken, so refusing here costs nothing but a re-run.
if [ "$mib_tree_existing" -eq 1 ] && [ "$allow_tree_overwrite" -ne 1 ]; then
    log ""
    log "Restoring 'mibs' would delete the $mib_tree_files_now files currently in"
    log "  $mib_tree_dest"
    log "and replace them with the $(jq -r '.trees[] | select(.component == "mibs") | .files' "$manifest") files from the backup."
    log ""
    log "That directory is part of your working tree, not a Docker volume, and this"
    log "repository has no version control to recover it from. Choose explicitly:"
    log ""
    log "  --allow-tree-overwrite   replace it with the backup's copy"
    log "  --skip mibs              restore everything else, leave it untouched"
    log ""
    die "refusing to overwrite a non-empty MIB source tree without --allow-tree-overwrite"
fi

# --- stage 5: safety backup ----------------------------------------------

safety_dir=""
if [ "$safety_backup" -eq 1 ]; then
    # Scoped to exactly the components about to be destroyed, so recovering
    # from a bad restore needs no reasoning about what was covered.
    #
    # 'mibs' is dropped when the destination holds nothing: backup.sh refuses an
    # empty or absent MIB tree, correctly, and there is by definition nothing
    # there to preserve. Aborting the restore over it would block precisely the
    # fresh-host recovery this tool exists for.
    safety_components=()
    for c in "${selected[@]}"; do
        if [ "$c" = "mibs" ] && [ "$mib_tree_existing" -eq 0 ]; then
            info "safety backup: omitting 'mibs' (nothing in $mib_tree_dest to preserve)"
            continue
        fi
        safety_components+=("$c")
    done

    if [ "${#safety_components[@]}" -eq 0 ]; then
        info "safety backup: nothing to preserve, skipping"
    else
        safety_name="pre-restore-$(date -u +%Y%m%dT%H%M%SZ)"
        info "taking a safety backup of current state: $safety_name"
        # NETMON_MIB_TREE is passed through explicitly so the safety copy is of
        # the directory this restore is about to empty, even when --mib-tree
        # points somewhere other than the compose file's bind mount.
        if ! NETMON_MIB_TREE="$mib_tree_dest" \
            "$(dirname "${BASH_SOURCE[0]}")/backup.sh" \
            --name "$safety_name" \
            --only "$(
                IFS=,
                printf '%s' "${safety_components[*]}"
            )"; then
            die "safety backup failed; refusing to overwrite data that is not backed up (use --no-safety-backup to override)"
        fi
        safety_dir="$NETMON_REPO_ROOT/backups/$safety_name"
    fi
else
    warn "--no-safety-backup: the current data will not be recoverable after this"
fi

# --- stage 6: apply -------------------------------------------------------

mapfile -t running_services < <(compose_running_services)
if [ "${#running_services[@]}" -gt 0 ]; then
    info "stopping services: ${running_services[*]}"
    compose stop "${running_services[@]}" >/dev/null
fi

# Materialise any missing volumes through compose so they are created with the
# project labels the ownership checks depend on. Creating them by hand would
# produce unlabelled volumes that this tooling would then refuse to touch.
need_create=0
for c in "${selected[@]}"; do
    lv="$(component_volume "$c")"
    [ -z "$lv" ] && continue
    volume_exists "$(docker_volume_for "$lv")" || need_create=1
done
if [ "$need_create" -eq 1 ]; then
    info "creating missing volumes via compose"
    compose up --no-start >/dev/null 2>&1 ||
        die "could not create the stack's volumes; is the compose file valid for this host?"
fi

for c in "${selected[@]}"; do
    if [ "$c" = "mibs" ]; then
        member="$backup_dir/$(jq -r '.trees[] | select(.component == "mibs") | .member' "$manifest")"
        info "restoring MIB source tree -> $mib_tree_dest"
        [ "$mib_tree_existing" -eq 1 ] &&
            info "  removing $mib_tree_files_now existing file(s) first (replacement, not merge)"
        gzip -dc "$member" | tree_import "$mib_tree_dest"
        info "  $(tree_file_count "$mib_tree_dest") file(s) now present"
        continue
    fi

    lv="$(component_volume "$c")"

    if [ -z "$lv" ]; then
        while read -r f; do
            [ -z "$f" ] && continue
            src="$backup_dir/config/$f"
            [ -f "$src" ] || {
                warn "config file '$f' recorded but absent from the backup; skipped"
                continue
            }
            info "restoring config file: $f"
            cp -p "$src" "$NETMON_REPO_ROOT/$f"
        done < <(jq -r '.config_files[]? | select(.restorable) | .path' "$manifest")
        continue
    fi

    dvol="$(docker_volume_for "$lv")"
    assert_volume_owned "$dvol"
    member="$backup_dir/$(jq -r --arg c "$c" '.volumes[] | select(.component == $c) | .member' "$manifest")"

    info "restoring $c -> $dvol"
    gzip -dc "$member" | volume_import "$dvol"
done

info "starting stack"
compose up -d >/dev/null

rule
info "restore complete"
info "  restored: ${selected[*]}"
[ -n "$safety_dir" ] && info "  previous state saved at: $safety_dir"
rule
info "verify with: scripts/verify-stack.sh capture"
