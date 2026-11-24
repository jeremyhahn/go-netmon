#!/usr/bin/env bash
# On-target installer run by scripts/deploy.sh; idempotent, and never overwrites existing config or /var/lib data unless FORCE_CONFIG=1.

set -euo pipefail

payload_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

svc_user="${SVC_USER:-go-netmon}"
prom_user="${PROM_USER:-prometheus}"
bin_dir="${BIN_DIR:-/usr/local/bin}"
conf_dir="${CONF_DIR:-/etc/go-netmon}"
data_dir="${DATA_DIR:-/var/lib/go-netmon}"
prom_data_dir="${PROM_DATA_DIR:-/var/lib/prometheus}"
share_dir="${SHARE_DIR:-/usr/local/share/go-netmon}"
unit_dir="${UNIT_DIR:-/etc/systemd/system}"
force_config="${FORCE_CONFIG:-}"
with_prometheus="${WITH_PROMETHEUS:-}"
start_services="${START_SERVICES:-1}"

log() { printf '  %s\n' "$*"; }

if [[ $EUID -ne 0 ]]; then
    echo "install.sh must run as root (deploy.sh invokes it with sudo)" >&2
    exit 1
fi

# --- service accounts -------------------------------------------------------
# System accounts with no shell and no home: these exist to own files and run
# processes, and a login shell on either is only ever a way in.
ensure_user() {
    local user="$1" home="$2"
    if id -u "$user" >/dev/null 2>&1; then
        log "user $user already exists"
        return
    fi
    useradd --system --no-create-home --home-dir "$home" --shell /usr/sbin/nologin "$user"
    log "created system user $user"
}

ensure_user "$svc_user" "$data_dir"
if [[ -n "$with_prometheus" ]]; then
    ensure_user "$prom_user" "$prom_data_dir"
fi

# --- directories ------------------------------------------------------------
# 0750: the service user reads and writes, root administers, nobody else looks.
# The web UI store holds SNMP community strings in clear text.
install -d -o "$svc_user" -g "$svc_user" -m 0750 \
    "$data_dir" "$data_dir/poller" "$data_dir/poller/uploaded" \
    "$data_dir/webui" "$data_dir/dashboards"
# 0755: two service accounts read this directory; secrets live under $data_dir and files are restricted individually.
install -d -o root -g root -m 0755 "$conf_dir"
install -d -o root -g root -m 0755 "$share_dir"
log "directories under $data_dir and $conf_dir in place"

if [[ -n "$with_prometheus" ]]; then
    install -d -o "$prom_user" -g "$prom_user" -m 0750 "$prom_data_dir"
    log "directory $prom_data_dir in place"
fi

# --- binaries: install(1) replaces atomically.
for b in "$payload_dir"/bin/*; do
    [[ -f "$b" ]] || continue
    install -o root -g root -m 0755 "$b" "$bin_dir/$(basename "$b")"
    log "installed $bin_dir/$(basename "$b")"
done

# --- bundled standard MIBs: root-owned, read-only to the service.
if [[ -d "$payload_dir/mibs/standard" ]]; then
    rm -rf "$share_dir/mibs/standard.new"
    install -d -m 0755 "$share_dir/mibs"
    cp -a "$payload_dir/mibs/standard" "$share_dir/mibs/standard.new"
    rm -rf "$share_dir/mibs/standard"
    mv "$share_dir/mibs/standard.new" "$share_dir/mibs/standard"
    chown -R root:root "$share_dir/mibs/standard"
    log "installed bundled standard MIBs to $share_dir/mibs/standard"
fi

# --- configuration: each file is group-owned by the account that reads it.
seed_config() {
    local src="$1" dst="$2" group="$3"
    if [[ -e "$dst" && -z "$force_config" ]]; then
        log "kept existing $dst (FORCE_CONFIG=1 to replace)"
        return
    fi
    [[ -e "$dst" ]] && cp -a "$dst" "$dst.replaced-$(date -u +%Y%m%dT%H%M%SZ)"
    install -o root -g "$group" -m 0640 "$src" "$dst"
    log "wrote $dst (readable by $group)"
}

seed_config "$payload_dir/config.yaml" "$conf_dir/config.yaml" "$svc_user"
if [[ -n "$with_prometheus" ]]; then
    seed_config "$payload_dir/prometheus.yml" "$conf_dir/prometheus.yml" "$prom_user"

    # Alert rules are replaced, not seeded: they hold no site settings.
    if [[ -d "$payload_dir/rules" ]]; then
        install -d -o root -g "$prom_user" -m 0750 "$conf_dir/rules"
        for rule in "$payload_dir"/rules/*.yml; do
            [[ -e "$rule" ]] || continue
            install -o root -g "$prom_user" -m 0640 "$rule" "$conf_dir/rules/$(basename "$rule")"
        done
        log "wrote $conf_dir/rules"
    fi
fi

# --- systemd units ----------------------------------------------------------
units=(go-netmon.service go-netmon-webui.service)
[[ -n "$with_prometheus" ]] && units+=(prometheus.service)

units_changed=
for u in "${units[@]}"; do
    if ! cmp -s "$payload_dir/$u" "$unit_dir/$u"; then
        install -o root -g root -m 0644 "$payload_dir/$u" "$unit_dir/$u"
        units_changed=1
        log "installed $unit_dir/$u"
    else
        log "$u unchanged"
    fi
done

if [[ -n "$units_changed" ]]; then
    systemctl daemon-reload
    log "systemd daemon reloaded"
fi

for u in "${units[@]}"; do
    systemctl enable "$u" >/dev/null 2>&1
done
log "units enabled: ${units[*]}"

if [[ -z "$start_services" ]]; then
    log "START_SERVICES empty — units installed and enabled but not started"
    exit 0
fi

# A unit that fails to start is reported, not fatal; deploy.sh checks every unit after. Restarting the Type=notify web UI blocks until it serves.
start_unit() {
    local u="$1"
    if systemctl restart "$u"; then
        log "$u started"
    else
        log "$u FAILED to start — see 'journalctl -u $u'"
    fi
}

# Start order: Prometheus, then the web UI (device registry), then the poller.
if [[ -n "$with_prometheus" ]]; then
    start_unit prometheus.service
fi
start_unit go-netmon-webui.service
start_unit go-netmon.service
