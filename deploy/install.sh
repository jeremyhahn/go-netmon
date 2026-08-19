#!/usr/bin/env bash
# On-target installer for the go-netmon monitoring appliance.
#
# Shipped inside the deployment payload by scripts/deploy.sh and run there with
# sudo. It is not meant to be invoked by hand, but it is safe to: it is
# idempotent, it converges rather than repeats, and it never touches operator
# data.
#
# WHAT IT WILL NOT OVERWRITE: /etc/go-netmon/config.yaml and prometheus.yml
# once they exist, and anything under /var/lib. Those are operator data — a
# migration restores them and a later deploy clobbering them would silently
# undo it. FORCE_CONFIG=1 overrides that when replacement is actually meant.

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
# 0755, not 0750 group-owned by the service: two different service accounts
# read out of this directory, and a group-restricted directory would let
# whichever one is not in the group fail to traverse it. Nothing secret lives
# here — device credentials are in the web UI's store under $data_dir, which is
# 0750. Access is restricted per file below.
install -d -o root -g root -m 0755 "$conf_dir"
install -d -o root -g root -m 0755 "$share_dir"
log "directories under $data_dir and $conf_dir in place"

if [[ -n "$with_prometheus" ]]; then
    install -d -o "$prom_user" -g "$prom_user" -m 0750 "$prom_data_dir"
    log "directory $prom_data_dir in place"
fi

# --- binaries ---------------------------------------------------------------
# install(1) writes through a temporary and renames, so a running service is
# never reading a half-written file. Replacing a busy binary is fine on Linux:
# the running process keeps its inode until it restarts.
for b in "$payload_dir"/bin/*; do
    [[ -f "$b" ]] || continue
    install -o root -g root -m 0755 "$b" "$bin_dir/$(basename "$b")"
    log "installed $bin_dir/$(basename "$b")"
done

# --- bundled standard MIBs --------------------------------------------------
# The IETF base set, so a fresh appliance with an empty store can still parse
# the standard modules. Owned by root and read-only to the service: these are
# shipped artifacts, not the operator's uploaded tree under $data_dir.
if [[ -d "$payload_dir/mibs/standard" ]]; then
    rm -rf "$share_dir/mibs/standard.new"
    install -d -m 0755 "$share_dir/mibs"
    cp -a "$payload_dir/mibs/standard" "$share_dir/mibs/standard.new"
    rm -rf "$share_dir/mibs/standard"
    mv "$share_dir/mibs/standard.new" "$share_dir/mibs/standard"
    chown -R root:root "$share_dir/mibs/standard"
    log "installed bundled standard MIBs to $share_dir/mibs/standard"
fi

# --- configuration ----------------------------------------------------------
# Each config file is group-owned by the account that reads it: config.yaml by
# the go-netmon services, prometheus.yml by prometheus. Getting this wrong is
# silent until the service starts and cannot open its own configuration.
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

# A unit that fails to start is reported here but does not abort the install:
# deploy.sh checks every unit afterwards and prints the journal for the ones
# that are down, which is far more use to an operator than this script dying
# with the reason still sitting in the journal unread.
start_unit() {
    local u="$1"
    if systemctl restart "$u"; then
        log "$u started"
    else
        log "$u FAILED to start — see 'journalctl -u $u'"
    fi
}

# Prometheus first: the poller's remote-write target has to be listening before
# the poller has anything to write to. Then the web UI, which owns the device
# registry, and only then the poller that reads it. Neither service hard-depends
# on another at the unit level, so the ordering is enforced here rather than by
# a Requires= that would take the whole appliance down with one component.
if [[ -n "$with_prometheus" ]]; then
    start_unit prometheus.service
fi
start_unit go-netmon-webui.service
start_unit go-netmon.service
