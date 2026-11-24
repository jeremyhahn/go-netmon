#!/usr/bin/env bash
# Shared helpers for backup and restore; sourced. Docker objects are addressed by derived name and checked by compose project label.

# --- configuration --------------------------------------------------------

# Compose project the stack occupies. Pinned, never inherited.
NETMON_PROJECT="${NETMON_PROJECT:-go-netmon}"

# Docker client. Overridable so the test suite can substitute a stub and run
# without a Docker daemon or the live stack.
DOCKER="${DOCKER:-docker}"

# The compose-project ownership check.
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

# Manifest schema: 1 has volumes[] and config_files[]; 2 adds trees[] for host directories. Restore accepts both.
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

# Container path of the MIB source tree bind mount (config.yaml mib.paths are under /app/mibs).
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

# assert_project_ownership refuses a target project holding containers not created from our compose file.
assert_project_ownership() {
    local foreign status=0
    foreign="$(compose_foreign_containers "$DOCKER" "$NETMON_PROJECT" "$NETMON_COMPOSE_FILE")" || status=$?

    # Exit 2 means the question could not be answered: refuse.
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

# compose_bind_sources lists the distinct absolute host paths bind-mounted at a container path.
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

# volume_export streams a volume to stdout as a tar with numeric owners, read-only and offline.
volume_export() {
    local vol="$1"
    "$DOCKER" run --rm --network none -v "$vol":/src:ro "$NETMON_HELPER_IMAGE" \
        tar -C /src --numeric-owner -cf - .
}

# volume_import empties a volume, keeping the mount point and removing dotfiles, then extracts the tar on stdin.
volume_import() {
    local vol="$1"
    "$DOCKER" run --rm -i --network none -v "$vol":/dst "$NETMON_HELPER_IMAGE" \
        sh -c 'find /dst -mindepth 1 -delete && tar -C /dst --numeric-owner -xf -'
}

# --- host directory trees -------------------------------------------------

# resolve_mib_tree sets NETMON_MIB_TREE and NETMON_MIB_TREE_SOURCE from the rendered compose file; call in the caller's shell.
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

# assert_safe_tree_dir refuses unsafe delete targets and sets NETMON_SAFE_TREE_DIR; call in the caller's shell.
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

# tree_export streams a host directory to stdout as a tar, without ownership.
tree_export() {
    tar -C "$1" -cf - .
}

# tree_import replaces a host directory's contents with the tar on stdin; call assert_safe_tree_dir first.
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

# tar_entry_count decompresses a member fully and counts entries, as an integrity check.
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

# validate_component_list aborts on an unknown component name; run it in the caller's shell before parse_component_list.
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
