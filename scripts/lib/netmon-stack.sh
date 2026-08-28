#!/usr/bin/env bash
# Shared helpers for the go-netmon backup and restore tooling.
#
# This library is sourced, never executed. Callers are responsible for
# "set -euo pipefail".
#
# SAFETY MODEL
#
# Everything here addresses Docker objects by explicit, derived name and then
# proves ownership through the com.docker.compose.project label before acting.
# Nothing is ever matched by pattern, prefix or guesswork. A stray volume whose
# name merely resembles a stack volume (this host has an unlabelled
# "go-netmon-data" that nothing mounts, distinct from the project's
# "go-netmon_go-netmon-data") must never be reachable by this tooling, and the
# label check is what makes that a checked fact rather than a naming convention.
#
# The same discipline as test/integration/run-tests.sh applies: the compose
# project is pinned rather than inherited from the working directory, and a
# project holding containers this repository's compose file did not create is
# treated as a fatal collision rather than something to operate on.
#
# HOST DIRECTORIES
#
# One backed-up component, "mibs", is not a Docker volume but a directory in the
# operator's working tree, bind-mounted into two services. Writing there is a
# different and more surprising act than writing into a volume, so it carries
# its own resolution, its own safety checks (assert_safe_tree_dir) and, in
# restore.sh, its own gate. The host path is never assumed: it is read back out
# of the rendered compose file, so a deployment that mounts its MIBs from
# somewhere else is handled without editing this tooling.

# --- configuration --------------------------------------------------------

# Compose project the stack occupies. Pinned, never inherited.
NETMON_PROJECT="${NETMON_PROJECT:-go-netmon}"

# Docker client. Overridable so the test suite can substitute a stub and run
# without a Docker daemon or the live stack.
DOCKER="${DOCKER:-docker}"

# The compose-project ownership check. This file is its only caller today: the
# integration harness's guard still carries its own enumeration, and whether it
# should source this library is the open question in #44. See the header of that
# file for the two defects a second copy here used to carry.
# shellcheck source=lib/compose-ownership.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/compose-ownership.sh"

# Image used for the throwaway containers that stream volume contents in and
# out. It needs nothing but a POSIX tar; it is pinned so a backup and the
# restore that follows it cannot disagree about tar behaviour.
NETMON_HELPER_IMAGE="${NETMON_HELPER_IMAGE:-alpine:3.19}"

# Web UI base URL used for the logical export. The compose file publishes the
# web UI on 8082.
NETMON_WEBUI_URL="${NETMON_WEBUI_URL:-http://localhost:8082}"

# Prometheus base URL used for state verification. Published on 9091.
NETMON_PROM_URL="${NETMON_PROM_URL:-http://localhost:9091}"

# Manifest schema. Bump only for changes a previous restore cannot understand.
#
# 1: volumes[] and config_files[].
# 2: adds trees[] - host directories carried as members. A schema-1 restore
#    would ignore trees[] entirely and report "restore complete" having left
#    the MIB source tree unrestored, so it must refuse the archive instead.
#    Restores still accept schema 1; trees[] is simply absent there.
# shellcheck disable=SC2034  # consumed by backup.sh and restore.sh
NETMON_MANIFEST_SCHEMA=2

# Restorable components, in the order they are applied. Host-side content
# (config files, the MIB source tree) is restored before the volumes, so the
# sources are in place by the time the stack comes back up and re-reads them.
NETMON_COMPONENTS=(config mibs webui poller metrics grafana)

# Repository files bind-mounted into the running stack. These are data in the
# operational sense: the stack behaves differently if they change.
NETMON_CONFIG_FILES=(config.yaml prometheus.yml)

# Recorded in the backup for provenance and compatibility checking, never
# restored automatically. Overwriting the deployment definition beneath a
# running deployment is a deployment action, not a data restore.
# shellcheck disable=SC2034  # consumed by backup.sh
NETMON_REFERENCE_FILES=(docker-compose.yml)

# Container path the MIB source tree is bind-mounted at. This is the contract
# between the images and config.yaml's mib.paths (all under /app/mibs), and it
# is what the host-side directory is identified by - the host path itself is
# whatever the compose file says it is.
NETMON_MIB_MOUNT_TARGET="${NETMON_MIB_MOUNT_TARGET:-/app/mibs}"

# Host directory holding the MIB source tree. Left empty here on purpose:
# resolve_mib_tree() fills it in from the rendered compose file. Setting it in
# the environment (or via restore.sh --mib-tree) overrides that derivation.
NETMON_MIB_TREE="${NETMON_MIB_TREE:-}"

# How NETMON_MIB_TREE was arrived at, for the operator-facing output. A path
# that appears in a destructive plan should say where it came from.
NETMON_MIB_TREE_SOURCE=""

# Set by assert_safe_tree_dir to the fully resolved, checked directory.
NETMON_SAFE_TREE_DIR=""

# --- output ---------------------------------------------------------------

netmon_prog="${netmon_prog:-${0##*/}}"

log() { printf '%s\n' "$*" >&2; }
info() { printf '[%s] %s\n' "$netmon_prog" "$*" >&2; }
warn() { printf '[%s] WARNING: %s\n' "$netmon_prog" "$*" >&2; }
die() {
    printf '[%s] ERROR: %s\n' "$netmon_prog" "$*" >&2
    exit 1
}

# rule prints a horizontal separator, used to make destructive plans legible.
rule() { printf '%s\n' "------------------------------------------------------------" >&2; }

# human_bytes renders a byte count in units an operator can sanity-check at a
# glance. Plans that destroy data are read by humans under pressure.
human_bytes() {
    local b="${1:-0}"
    if [ "$b" -ge 1073741824 ]; then
        awk -v b="$b" 'BEGIN { printf "%.1f GB", b / 1073741824 }'
    elif [ "$b" -ge 1048576 ]; then
        awk -v b="$b" 'BEGIN { printf "%.1f MB", b / 1048576 }'
    elif [ "$b" -ge 1024 ]; then
        awk -v b="$b" 'BEGIN { printf "%.1f KB", b / 1024 }'
    else
        printf '%s B' "$b"
    fi
}

# --- preflight ------------------------------------------------------------

# require_tools fails unless every named executable is on PATH.
require_tools() {
    local tool missing=()
    for tool in "$@"; do
        command -v "$tool" >/dev/null 2>&1 || missing+=("$tool")
    done
    if [ "${#missing[@]}" -gt 0 ]; then
        die "required tool(s) not found on PATH: ${missing[*]}"
    fi
}

# require_docker fails unless the Docker CLI and the compose plugin both work.
require_docker() {
    command -v "$DOCKER" >/dev/null 2>&1 || die "docker not found on PATH (DOCKER=$DOCKER)"
    "$DOCKER" compose version >/dev/null 2>&1 ||
        die "the docker compose plugin is not available"
}

# netmon_repo_root echoes the repository root, derived from this library's own
# location so the scripts work from any working directory.
netmon_repo_root() {
    local lib_dir
    lib_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    (cd "$lib_dir/../.." && pwd)
}

NETMON_REPO_ROOT="${NETMON_REPO_ROOT:-$(netmon_repo_root)}"
NETMON_COMPOSE_FILE="${NETMON_COMPOSE_FILE:-$NETMON_REPO_ROOT/docker-compose.yml}"

# --- compose --------------------------------------------------------------

# compose runs docker compose against the pinned project and compose file.
# Every compose invocation in this tooling goes through here, so no code path
# can accidentally inherit the directory-derived project name.
compose() {
    "$DOCKER" compose -p "$NETMON_PROJECT" -f "$NETMON_COMPOSE_FILE" "$@"
}

# assert_project_ownership refuses to continue if the target compose project
# holds a container that was not created from our compose file. Acting on such
# a project would mean stopping, and potentially overwriting the data of, a
# deployment this repository does not own.
#
# The check itself lives in compose-ownership.sh, which exists to be the single
# implementation of this property -- today this function is its only caller, and
# that file's header says so. It used to be a second implementation here, and it
# carried two defects the harness copy had already been corrected for and
# documents in its own comments: a "label=KEY!=VALUE" filter that docker matches
# nothing with, and "2>/dev/null || true" that turned a FAILED query into
# "nothing foreign". Between them this function could not refuse -- not for a
# project full of foreign containers, and not when docker was unreachable --
# while two tests asserted that it could.
assert_project_ownership() {
    local foreign status=0
    foreign="$(compose_foreign_containers "$DOCKER" "$NETMON_PROJECT" "$NETMON_COMPOSE_FILE")" || status=$?

    # Exit 2 is "the question could not be answered", which is a refusal. It is
    # deliberately NOT the same as an empty answer: treating an unanswered
    # question as a clean bill of health is how this function came to be
    # unable to fail.
    if [ "$status" -ne 0 ]; then
        die "refusing to operate: could not establish ownership of project '$NETMON_PROJECT'"
    fi

    [ -z "$foreign" ] && return 0

    log "Containers in project '$NETMON_PROJECT' not created from $NETMON_COMPOSE_FILE:"
    printf '  %s\n' "$foreign" >&2
    die "refusing to operate: this project contains containers this compose file does not own"
}

# compose_volume_names lists the logical volume names the compose file defines.
compose_volume_names() {
    compose config --volumes
}

# compose_running_services lists services with a running container.
compose_running_services() {
    compose ps --services --filter status=running 2>/dev/null || true
}

# compose_service_images emits "service<TAB>image<TAB>image-id" for every
# service, recording exactly what the backed-up data was produced by.
compose_service_images() {
    local svc image id
    while IFS=$'\t' read -r svc image; do
        [ -z "$svc" ] && continue
        id="$("$DOCKER" image inspect "$image" --format '{{.Id}}' 2>/dev/null || echo "")"
        printf '%s\t%s\t%s\n' "$svc" "$image" "$id"
    done < <(compose config --format json 2>/dev/null |
        jq -r '.services | to_entries[] | "\(.key)\t\(.value.image // "")"')
}

# compose_bind_sources lists the distinct host paths bind-mounted at a given
# container path, across every service in the rendered compose file. Compose
# resolves relative bind sources to absolute paths, so what comes back is the
# real directory regardless of how the file spells it.
compose_bind_sources() {
    local target="$1"
    compose config --format json 2>/dev/null | jq -r --arg t "$target" '
        [.services[]?.volumes[]?
         | select((.type == "bind") and (.target == $t))
         | .source]
        | unique | .[]'
}

# --- volumes --------------------------------------------------------------

# component_volume maps a restorable component to the compose volume it holds.
# "config" and "mibs" have no volume; they cover host-side content.
component_volume() {
    case "$1" in
    webui) printf 'webui-data' ;;
    poller) printf 'go-netmon-data' ;;
    metrics) printf 'prometheus-data' ;;
    grafana) printf 'grafana-data' ;;
    config) printf '' ;;
    mibs) printf '' ;;
    *) return 1 ;;
    esac
}

# component_description explains, in operator terms, what a component holds.
# Restore plans must say what is being destroyed, not just name a volume.
component_description() {
    case "$1" in
    config) printf 'bind-mounted repository config (%s)' "${NETMON_CONFIG_FILES[*]}" ;;
    mibs) printf 'MIB source tree bind-mounted read-only into the poller and web UI' ;;
    webui) printf 'devices, profiles, MIB assignments, poll intervals, MIB catalog' ;;
    poller) printf 'poller MIB metadata store and uploaded MIB files' ;;
    metrics) printf 'Prometheus TSDB - all metric history' ;;
    grafana) printf 'Grafana database and dashboards' ;;
    *) return 1 ;;
    esac
}

# is_component returns success if the name is a known component.
is_component() {
    local c
    for c in "${NETMON_COMPONENTS[@]}"; do
        [ "$c" = "$1" ] && return 0
    done
    return 1
}

# selection_has returns success if a component name appears in the arguments
# after the first. Used as: selection_has mibs "${selected[@]}".
selection_has() {
    local want="$1" c
    shift
    for c in "$@"; do
        [ "$c" = "$want" ] && return 0
    done
    return 1
}

# docker_volume_for derives the Docker volume name compose gives a logical
# volume in the pinned project. Derivation, never search.
docker_volume_for() {
    printf '%s_%s' "$NETMON_PROJECT" "$1"
}

# volume_exists returns success if the named Docker volume exists.
volume_exists() {
    "$DOCKER" volume inspect "$1" >/dev/null 2>&1
}

# assert_volume_owned refuses to touch a volume that does not carry the pinned
# project's compose label. This is the check that keeps the tooling away from
# the unlabelled look-alike volume on this host.
assert_volume_owned() {
    local vol="$1" project
    volume_exists "$vol" || die "volume not found: $vol"
    project="$("$DOCKER" volume inspect "$vol" \
        --format '{{index .Labels "com.docker.compose.project"}}' 2>/dev/null || true)"
    if [ "$project" != "$NETMON_PROJECT" ]; then
        die "volume '$vol' is labelled project '${project:-<none>}', not '$NETMON_PROJECT'; refusing to touch it"
    fi
}

# volume_bytes reports the on-disk size of a volume's contents, via a helper
# container so it works regardless of storage driver and without root on the
# host. Prints 0 for a volume that does not exist.
volume_bytes() {
    local vol="$1"
    volume_exists "$vol" || {
        printf '0'
        return 0
    }
    "$DOCKER" run --rm --network none -v "$vol":/src:ro "$NETMON_HELPER_IMAGE" \
        sh -c 'du -sb /src 2>/dev/null | cut -f1' 2>/dev/null || printf '0'
}

# report_unmanaged_volumes lists volumes whose name resembles a stack volume
# but which carry no compose project label. They are reported so an operator
# knows they exist and are never acted upon by this tooling.
report_unmanaged_volumes() {
    local vol project found=0
    while read -r vol; do
        [ -z "$vol" ] && continue
        project="$("$DOCKER" volume inspect "$vol" \
            --format '{{index .Labels "com.docker.compose.project"}}' 2>/dev/null || true)"
        [ "$project" = "$NETMON_PROJECT" ] && continue
        if [ "$found" -eq 0 ]; then
            warn "volumes resembling stack volumes but not labelled project '$NETMON_PROJECT' (ignored, never touched):"
            found=1
        fi
        warn "  $vol  (project=${project:-<none>})"
    done < <("$DOCKER" volume ls --format '{{.Name}}' 2>/dev/null | grep -E 'netmon|prometheus-data|grafana-data|webui-data' || true)
}

# --- volume streaming -----------------------------------------------------

# volume_export streams a volume's contents to stdout as an uncompressed tar.
#
# Read-only mount, no network, numeric owner. Numeric ownership matters: the
# Prometheus and Grafana images run as non-root uids (65534 and 472) and a
# restore that loses ownership produces a container that cannot write its own
# data directory.
volume_export() {
    local vol="$1"
    "$DOCKER" run --rm --network none -v "$vol":/src:ro "$NETMON_HELPER_IMAGE" \
        tar -C /src --numeric-owner -cf - .
}

# volume_import replaces a volume's contents with the tar stream on stdin.
#
# The destination is emptied first so the restore is a replacement rather than
# a merge; a merge would leave behind files the backup does not contain, which
# is precisely the "silently half-matches" outcome a restore must not produce.
# "find -mindepth 1 -delete" is used rather than a glob so dotfiles are removed
# and the mount point itself survives.
volume_import() {
    local vol="$1"
    "$DOCKER" run --rm -i --network none -v "$vol":/dst "$NETMON_HELPER_IMAGE" \
        sh -c 'find /dst -mindepth 1 -delete && tar -C /dst --numeric-owner -xf -'
}

# --- host directory trees -------------------------------------------------

# resolve_mib_tree sets NETMON_MIB_TREE and NETMON_MIB_TREE_SOURCE, or dies.
#
# It must run in the caller's own shell, never inside a command substitution:
# die() in a subshell would exit only that subshell and leave the caller
# carrying on with an unset path. The same trap validate_component_list
# documents.
#
# The path is read from the rendered compose file rather than assumed to be
# <repo>/mibs, because that is where the deployment's own answer lives. A
# compose file that mounts its MIBs from elsewhere therefore needs no change
# here, and one that mounts nothing at the target is a fatal ambiguity rather
# than something to guess at.
resolve_mib_tree() {
    if [ -n "$NETMON_MIB_TREE" ]; then
        NETMON_MIB_TREE_SOURCE="explicit override"
        return 0
    fi

    local sources count
    sources="$(compose_bind_sources "$NETMON_MIB_MOUNT_TARGET" || true)"
    count="$(printf '%s' "$sources" | grep -c . || true)"

    case "$count" in
    0)
        log ""
        log "No service in this compose file bind-mounts a host directory at"
        log "$NETMON_MIB_MOUNT_TARGET, so the MIB source tree cannot be located."
        log ""
        log "Either this deployment genuinely has no MIB source tree - in which"
        log "case pass '--skip mibs' - or point the tooling at it explicitly with"
        log "NETMON_MIB_TREE=/path/to/mibs."
        die "cannot resolve the MIB source tree for component 'mibs'"
        ;;
    1)
        NETMON_MIB_TREE="$sources"
        # shellcheck disable=SC2034  # consumed by backup.sh and restore.sh
        NETMON_MIB_TREE_SOURCE="compose bind mount at $NETMON_MIB_MOUNT_TARGET"
        ;;
    *)
        log ""
        log "Services disagree about the host directory mounted at $NETMON_MIB_MOUNT_TARGET:"
        printf '%s\n' "$sources" | sed 's/^/  /' >&2
        log ""
        log "Backing up one of them and restoring over the other would silently"
        log "give the two services different MIB sources. Resolve the compose file,"
        log "or choose explicitly with NETMON_MIB_TREE=/path/to/mibs."
        die "conflicting MIB source directories in the compose file"
        ;;
    esac
}

# assert_safe_tree_dir refuses destinations that "find -mindepth 1 -delete"
# must never be pointed at, and publishes the checked, symlink-resolved path in
# NETMON_SAFE_TREE_DIR.
#
# The result is a global rather than a printed value on purpose: a caller
# reading it through "$(...)" would run every die() below in a subshell, which
# exits the subshell and lets the caller carry on with an unchecked path - the
# same trap documented on validate_component_list, but with deletion at the end
# of it.
#
# Unlike a Docker volume, a host directory has no ownership label to check and
# no container boundary to contain a mistake: a wrong path here deletes the
# operator's files with no undo, and this repository has no git history to
# recover them from. The checks are deliberately blunt - a legitimate MIB tree
# is a leaf directory well inside a checkout, never a system root, a home
# directory or the repository root itself.
assert_safe_tree_dir() {
    local dir="$1" resolved parent depth

    case "$dir" in
    /*) ;;
    *) die "MIB tree path must be absolute, got: $dir" ;;
    esac

    if [ -e "$dir" ]; then
        [ -d "$dir" ] || die "MIB tree path exists but is not a directory: $dir"
        resolved="$(cd "$dir" && pwd -P)"
    else
        parent="$(dirname "$dir")"
        [ -d "$parent" ] ||
            die "MIB tree path's parent directory does not exist: $parent"
        resolved="$(cd "$parent" && pwd -P)/$(basename "$dir")"
    fi

    case "$resolved" in
    / | /home | /root | /usr | /etc | /var | /opt | /srv | /tmp | /mnt | /media)
        die "refusing to use a system directory as the MIB tree: $resolved"
        ;;
    esac
    [ "$resolved" = "${HOME:-/nonexistent}" ] &&
        die "refusing to use the home directory as the MIB tree: $resolved"
    [ "$resolved" = "$NETMON_REPO_ROOT" ] &&
        die "refusing to use the repository root as the MIB tree: $resolved"

    # A real tree is nested; "/mibs" is far more likely to be a typo than a
    # deployment layout, and the cost of being wrong is the whole directory.
    depth="$(printf '%s' "${resolved#/}" | tr -cd '/' | wc -c)"
    [ "$depth" -ge 1 ] ||
        die "MIB tree path is too close to the filesystem root to be safe: $resolved"

    # shellcheck disable=SC2034  # consumed by backup.sh and restore.sh
    NETMON_SAFE_TREE_DIR="$resolved"
}

# tree_file_count counts regular files in a host directory. Prints 0 for a
# directory that does not exist.
tree_file_count() {
    [ -d "$1" ] || {
        printf '0'
        return 0
    }
    find "$1" -type f | wc -l | tr -d ' '
}

# tree_bytes reports the apparent size of a host directory's contents.
tree_bytes() {
    [ -d "$1" ] || {
        printf '0'
        return 0
    }
    du -sb "$1" | cut -f1
}

# tree_export streams a host directory to stdout as an uncompressed tar.
#
# No --numeric-owner, unlike volume_export: these are the operator's own files
# on the operator's own machine, and the uid that took the backup is not
# necessarily the uid that restores it. Ownership is deliberately not part of
# what is carried; content, layout and mode are.
tree_export() {
    tar -C "$1" -cf - .
}

# tree_import replaces a host directory's contents with the tar stream on
# stdin. Call assert_safe_tree_dir first.
#
# Wholesale replacement, matching volume_import and the exact-copy semantics of
# the config files. Merging would be worse than it sounds for MIBs specifically:
# a module left behind from a newer tree keeps being parsed alongside the
# restored one, so the deployment resolves against a set that matches neither
# the backup nor what the operator had.
tree_import() {
    local dir="$1"
    mkdir -p "$dir"
    find "$dir" -mindepth 1 -delete
    tar -C "$dir" --no-same-owner -xf -
}

# --- integrity ------------------------------------------------------------

# sha256_of prints the SHA-256 of a file with no trailing filename.
sha256_of() {
    sha256sum "$1" | cut -d' ' -f1
}

# tar_entry_count decompresses a member end to end and counts its entries.
#
# This doubles as a full-stream integrity check: a truncated or corrupted
# gzip member fails here rather than halfway through overwriting a live
# volume. It is deliberately a second pass over the data.
tar_entry_count() {
    local member="$1" count
    count="$(tar -tzf "$member" 2>/dev/null | wc -l)" || return 1
    printf '%s' "$count"
}

# --- manifest -------------------------------------------------------------

# manifest_path echoes the manifest location inside a backup directory.
manifest_path() { printf '%s/manifest.json' "$1"; }

# manifest_get reads a jq expression from a backup's manifest.
manifest_get() {
    local backup_dir="$1" expr="$2"
    jq -r "$expr" "$(manifest_path "$backup_dir")"
}

# validate_component_list aborts unless every name in a comma-separated list is
# a known component. An unknown name is fatal: silently ignoring a typo in
# "--only" would back up or restore more, or less, than the operator asked for.
#
# This is deliberately separate from parse_component_list. Callers consume the
# parsed list through "mapfile < <(...)", and a die() inside that process
# substitution would exit only the subshell, leaving the caller to carry on
# with a silently truncated selection. Validation therefore has to happen in
# the caller's own shell, before the list is read.
validate_component_list() {
    local raw="$1" item items
    IFS=',' read -r -a items <<<"$raw"
    for item in "${items[@]}"; do
        [ -z "$item" ] && continue
        is_component "$item" ||
            die "unknown component '$item' (valid: ${NETMON_COMPONENTS[*]})"
    done
}

# parse_component_list echoes a comma-separated component list one per line.
# Call validate_component_list first; this function assumes valid input.
parse_component_list() {
    local raw="$1" item items
    IFS=',' read -r -a items <<<"$raw"
    for item in "${items[@]}"; do
        [ -n "$item" ] && printf '%s\n' "$item"
    done
}
