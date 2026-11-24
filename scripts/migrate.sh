#!/usr/bin/env bash
# Restore a netmon-backup archive of a containerised deployment onto a native appliance under /var/lib; the source is never touched, grafana is not restored.

set -euo pipefail

usage() {
    cat <<'EOF'
Usage: migrate.sh --host HOST --backup DIR [options]

Options:
  --host HOST        Target appliance, already deployed to (required)
  --user USER        SSH user on the target (default: $USER)
  --backup DIR       Backup directory to restore from (required)
  --remote-backup    DIR is a path ON THE TARGET, not here; skip the upload
  --replace          Replace target data that is already present
  --keep-going       Restore what is present instead of requiring every member
  --dry-run          Print the plan and change nothing
  -h, --help         Show this help

Environment equivalents:
  HOST USER BACKUP_DIR REMOTE_BACKUP REPLACE KEEP_GOING DRY_RUN SSH_OPTS

Examples:
  migrate.sh --host monitor.example --backup ./backups/prod-premigration
  migrate.sh --host monitor.example --remote-backup \
      --backup /var/backups/go-netmon-migration/prod-premigration
EOF
}

host="${HOST:-}"
ssh_user="${USER:-$(id -un)}"
backup_dir="${BACKUP_DIR:-}"
remote_backup="${REMOTE_BACKUP:-}"
replace="${REPLACE:-}"
keep_going="${KEEP_GOING:-}"
dry_run="${DRY_RUN:-}"
ssh_opts="${SSH_OPTS:-}"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --host) host="$2"; shift 2 ;;
        --user) ssh_user="$2"; shift 2 ;;
        --backup) backup_dir="$2"; shift 2 ;;
        --remote-backup) remote_backup=1; shift ;;
        --replace) replace=1; shift ;;
        --keep-going) keep_going=1; shift ;;
        --dry-run) dry_run=1; shift ;;
        -h|--help) usage; exit 0 ;;
        *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
    esac
done

[[ -n "$host" ]] || { echo "--host is required" >&2; usage >&2; exit 2; }
[[ -n "$backup_dir" ]] || { echo "--backup is required" >&2; usage >&2; exit 2; }

target="$ssh_user@$host"
# shellcheck disable=SC2206
ssh_args=($ssh_opts)

say()  { printf '\n== %s\n' "$*"; }
step() { printf '  %s\n' "$*"; }
# Command strings are built here and expand on this side before being sent.
# That is intended: every value interpolated into one is a path or flag this
# script owns, never anything that came from the target.
# shellcheck disable=SC2029
run_remote() { ssh "${ssh_args[@]}" "$target" "$@"; }

# component:archive member:destination:owner
members=(
    "poller:volumes/poller.tar.gz:/var/lib/go-netmon/poller:go-netmon"
    "webui:volumes/webui.tar.gz:/var/lib/go-netmon/webui:go-netmon"
    "metrics:volumes/metrics.tar.gz:/var/lib/prometheus:prometheus"
)

[[ -n "$dry_run" ]] && say "DRY RUN — nothing will be copied or changed"

# --- verify the backup ------------------------------------------------------
# A migration that restores a truncated archive is worse than one that refuses
# to start, so the checksums are checked before anything is stopped.
say "Verifying backup at $backup_dir"
if [[ -n "$remote_backup" ]]; then
    run_remote "test -d '$backup_dir'" || {
        echo "backup directory $backup_dir not found on $target" >&2; exit 1; }
    run_remote "cd '$backup_dir' && sha256sum -c checksums.sha256 >/dev/null" || {
        echo "checksum verification FAILED on $target — refusing to migrate" >&2; exit 1; }
    step "checksums verified on the target"
else
    [[ -d "$backup_dir" ]] || { echo "no such directory: $backup_dir" >&2; exit 1; }
    (cd "$backup_dir" && sha256sum -c checksums.sha256 >/dev/null) || {
        echo "checksum verification FAILED — refusing to migrate" >&2; exit 1; }
    step "checksums verified locally"
fi

# --- upload -----------------------------------------------------------------
remote_dir="$backup_dir"
if [[ -z "$remote_backup" ]]; then
    say "Uploading backup to $target"
    if [[ -z "$dry_run" ]]; then
        remote_dir="$(run_remote 'mktemp -d /tmp/go-netmon-migrate.XXXXXX')"
        tar -C "$backup_dir" -cf - . | run_remote "tar -C '$remote_dir' -xf -"
        run_remote "cd '$remote_dir' && sha256sum -c checksums.sha256 >/dev/null" || {
            echo "checksums do not match after upload — aborting" >&2; exit 1; }
        step "uploaded to $remote_dir and re-verified"
    else
        step "would upload $backup_dir and re-verify checksums there"
    fi
fi

# --- refuse to clobber ------------------------------------------------------
say "Checking target state"
for m in "${members[@]}"; do
    IFS=: read -r component member dest _owner <<<"$m"
    # List inside sudo: the login user cannot read the service-owned directories.
    if run_remote "sudo sh -c \"test -n \\\"\\\$(ls -A '$dest' 2>/dev/null)\\\"\""; then
        if [[ -z "$replace" ]]; then
            echo "$dest on $target is not empty. Pass --replace to overwrite it." >&2
            exit 1
        fi
        step "$component: $dest is populated, will be replaced"
    else
        step "$component: $dest is empty"
    fi
done

if [[ -n "$dry_run" ]]; then
    say "Would stop services, restore ${#members[@]} components, then start them"
    exit 0
fi

# Stop: Pebble and the Prometheus TSDB need a cold restore; services restart on every exit path.
say "Stopping services"
restart_services() {
    say "Starting services"
    run_remote "sudo systemctl start prometheus.service go-netmon-webui.service go-netmon.service" || true
}
trap restart_services EXIT

run_remote "sudo systemctl stop go-netmon.service go-netmon-webui.service prometheus.service"
step "go-netmon, go-netmon-webui and prometheus stopped"

# --- restore ----------------------------------------------------------------
for m in "${members[@]}"; do
    IFS=: read -r component member dest owner <<<"$m"
    say "Restoring $component -> $dest"

    if ! run_remote "test -f '$remote_dir/$member'"; then
        if [[ -n "$keep_going" ]]; then
            step "$member absent, skipped"
            continue
        fi
        echo "$member missing from the backup (use --keep-going to skip)" >&2
        exit 1
    fi

    # Extract beside the destination and swap, so an extraction that dies
    # halfway leaves the old directory intact rather than a half-populated one.
    run_remote "sudo rm -rf '$dest.incoming' && sudo mkdir -p '$dest.incoming'"
    run_remote "sudo tar -C '$dest.incoming' -xzf '$remote_dir/$member'"
    step "extracted"

    # Prometheus writes these at startup and refuses nothing if they are
    # missing; carrying a stale lock from another host's filesystem across is
    # only a way to confuse the next operator reading the directory.
    run_remote "sudo rm -f '$dest.incoming/lock' '$dest.incoming/queries.active'"

    run_remote "sudo chown -R '$owner:$owner' '$dest.incoming'"
    run_remote "sudo chmod 0750 '$dest.incoming'"
    step "owned by $owner"

    run_remote "sudo rm -rf '$dest.replaced' && { sudo test -d '$dest' && sudo mv '$dest' '$dest.replaced' || true; }"
    run_remote "sudo mv '$dest.incoming' '$dest'"
    step "swapped into place"
done

# --- start and verify -------------------------------------------------------
trap - EXIT
restart_services

say "Verifying"
# Poll until Prometheus has replayed its WAL and serves.
for _ in $(seq 1 60); do
    if run_remote "curl -fsS http://127.0.0.1:9090/-/ready >/dev/null 2>&1"; then
        step "prometheus ready"
        break
    fi
    sleep 5
done

for u in prometheus.service go-netmon-webui.service go-netmon.service; do
    if run_remote "systemctl is-active --quiet $u"; then
        step "$u active"
    else
        step "$u NOT active — check 'journalctl -u $u'"
    fi
done

say "Migrated"
echo "The previous target data, if any, is beside each directory as *.replaced."
echo "Remove it once the appliance is confirmed good."
