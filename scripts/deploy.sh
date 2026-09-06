#!/usr/bin/env bash
# Deploy go-netmon to a host as native systemd services.
#
# The target needs an SSH account that can sudo, and nothing else — no Docker,
# no container runtime, no Go toolchain, no checkout of this repository. The
# binaries are built here and streamed over the same SSH connection. That is
# deliberate: the deployment target is a small monitoring appliance, and
# compiling on it is the one thing guaranteed to make it a bad one.
#
# Re-running is the normal way to use this. Every step converges rather than
# repeats: binaries are replaced in place, an existing config.yaml is never
# rewritten, and units are only reinstalled when they actually differ.
#
# WHAT THIS WILL NOT OVERWRITE: /etc/go-netmon/config.yaml, prometheus.yml, and
# everything under /var/lib. Those are operator data — see deploy/install.sh.

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

usage() {
    cat <<'EOF'
Usage: deploy.sh --host HOST [options]

Options:
  --host HOST           Target hostname or address (required)
  --user USER           SSH user on the target (default: $USER)
  --with-prometheus     Also install Prometheus, the metrics backend
  --prometheus-bin PATH Prometheus binary to ship (default: dist/prometheus,
                        produced by `make prometheus-bin`)
  --skip-build          Use the binaries already in bin/
  --force-config        Overwrite config.yaml and prometheus.yml on the target
  --no-start            Install and enable the units, but do not start them
  --dry-run             Print what would happen and change nothing
  -h, --help            Show this help

Environment equivalents:
  HOST USER WITH_PROMETHEUS PROMETHEUS_BIN SKIP_BUILD FORCE_CONFIG
  NO_START DRY_RUN SSH_OPTS WEBUI_READY_TIMEOUT

Examples:
  deploy.sh --host monitor.example --user ops --with-prometheus
  make deploy HOST=monitor.example USER=ops
EOF
}

host="${HOST:-}"
ssh_user="${USER:-$(id -un)}"
with_prometheus="${WITH_PROMETHEUS:-}"
prometheus_bin="${PROMETHEUS_BIN:-$repo_root/dist/prometheus}"
skip_build="${SKIP_BUILD:-}"
force_config="${FORCE_CONFIG:-}"
no_start="${NO_START:-}"
dry_run="${DRY_RUN:-}"
ssh_opts="${SSH_OPTS:-}"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --host) host="$2"; shift 2 ;;
        --user) ssh_user="$2"; shift 2 ;;
        --with-prometheus) with_prometheus=1; shift ;;
        --prometheus-bin) prometheus_bin="$2"; with_prometheus=1; shift 2 ;;
        --skip-build) skip_build=1; shift ;;
        --force-config) force_config=1; shift ;;
        --no-start) no_start=1; shift ;;
        --dry-run) dry_run=1; shift ;;
        -h|--help) usage; exit 0 ;;
        *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
    esac
done

[[ -n "$host" ]] || { echo "--host is required" >&2; usage >&2; exit 2; }

target="$ssh_user@$host"
# shellcheck disable=SC2206  # word splitting of SSH_OPTS is the point
ssh_args=($ssh_opts)

say()  { printf '\n== %s\n' "$*"; }
step() { printf '  %s\n' "$*"; }
# Command strings are built here and expand on this side before being sent.
# That is intended: every value interpolated into one is a path or flag this
# script owns, never anything that came from the target.
# shellcheck disable=SC2029
run_remote() { ssh "${ssh_args[@]}" "$target" "$@"; }

if [[ -n "$dry_run" ]]; then
    say "DRY RUN — nothing will be built, copied or changed"
fi

# --- preflight --------------------------------------------------------------
say "Preflight against $target"
if [[ -z "$dry_run" ]]; then
    run_remote 'true' || { echo "cannot ssh to $target" >&2; exit 1; }
    step "ssh ok"
    run_remote 'sudo -n true' || {
        echo "$target cannot sudo without a password prompt" >&2; exit 1; }
    step "passwordless sudo ok"
    remote_arch="$(run_remote 'uname -m')"
    [[ "$remote_arch" == "x86_64" ]] || {
        echo "target is $remote_arch; this script builds linux/amd64" >&2; exit 1; }
    step "architecture $remote_arch ok"
    run_remote 'command -v systemctl >/dev/null' || {
        echo "target has no systemd" >&2; exit 1; }
    step "systemd present"
fi

# --- build ------------------------------------------------------------------
# CGO off so the result is a static binary that does not care what libc the
# appliance ships. -trimpath keeps build-host paths out of the artifact.
if [[ -z "$skip_build" ]]; then
    say "Building linux/amd64 binaries"
    if [[ -z "$dry_run" ]]; then
        version="$(cat "$repo_root/VERSION" 2>/dev/null || echo dev)"
        cd "$repo_root"
        mkdir -p bin
        for target_bin in \
            "go-netmon:./cmd/go-netmon" \
            "go-netmon-webui:./cmd/webui" \
            "netmon:./cmd/netmon" \
            "netmon-backup:./cmd/netmon-backup" \
            "mib-importer:./cmd/mib-importer"
        do
            out="${target_bin%%:*}"
            pkg="${target_bin##*:}"
            CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
                go build -trimpath -ldflags "-s -w -X main.version=$version" \
                -o "bin/$out" "$pkg"
            step "built bin/$out"
        done
    else
        step "would build go-netmon, go-netmon-webui, netmon, netmon-backup, mib-importer"
    fi
else
    say "Skipping build, using existing bin/"
fi

# --- stage the payload ------------------------------------------------------
say "Staging payload"
if [[ -n "$with_prometheus" ]]; then
    [[ -f "$prometheus_bin" ]] || {
        echo "prometheus binary not found at $prometheus_bin" >&2
        echo "run 'make prometheus-bin' first, or pass --prometheus-bin PATH" >&2
        exit 1; }
    step "prometheus binary: $prometheus_bin"
fi

if [[ -z "$dry_run" ]]; then
    stage="$(mktemp -d)"
    trap 'rm -rf "$stage"' EXIT

    mkdir -p "$stage/bin" "$stage/mibs"
    for b in go-netmon go-netmon-webui netmon netmon-backup mib-importer; do
        install -m 0755 "$repo_root/bin/$b" "$stage/bin/$b"
    done
    cp -a "$repo_root/mibs/standard" "$stage/mibs/standard"
    install -m 0644 "$repo_root/deploy/config.yaml"             "$stage/config.yaml"
    install -m 0644 "$repo_root/deploy/prometheus.yml"          "$stage/prometheus.yml"
    # Alert rules are shipped as a directory rather than seeded like the two
    # configs above: they carry no site-specific settings, so replacing them
    # wholesale is right and losing an edit is not a risk.
    cp -a "$repo_root/deploy/rules" "$stage/rules"
    install -m 0644 "$repo_root/deploy/go-netmon.service"       "$stage/go-netmon.service"
    install -m 0644 "$repo_root/deploy/go-netmon-webui.service" "$stage/go-netmon-webui.service"
    install -m 0644 "$repo_root/deploy/prometheus.service"      "$stage/prometheus.service"
    install -m 0755 "$repo_root/deploy/install.sh"              "$stage/install.sh"
    [[ -n "$with_prometheus" ]] && install -m 0755 "$prometheus_bin" "$stage/bin/prometheus"
    step "payload staged ($(du -sh "$stage" | cut -f1))"
else
    step "would stage binaries, MIBs, configs, units and install.sh"
fi

# --- ship and install -------------------------------------------------------
say "Shipping to $target and installing"
if [[ -z "$dry_run" ]]; then
    remote_stage="$(run_remote 'mktemp -d /tmp/go-netmon-deploy.XXXXXX')"
    # tar over the existing SSH connection: no scp recursion quirks, no
    # dependency on rsync being present on a minimal appliance.
    tar -C "$stage" -cf - . | run_remote "tar -C '$remote_stage' -xf -"
    step "payload delivered to $remote_stage"

    run_remote "sudo env \
        FORCE_CONFIG='${force_config}' \
        WITH_PROMETHEUS='${with_prometheus}' \
        START_SERVICES='$([[ -n "$no_start" ]] || echo 1)' \
        bash '$remote_stage/install.sh'"

    run_remote "rm -rf '$remote_stage'"
    step "remote staging directory removed"
else
    step "would ship the payload and run install.sh with sudo"
fi

# --- verify -----------------------------------------------------------------
if [[ -n "$dry_run" || -n "$no_start" ]]; then
    say "Done"
    exit 0
fi

# webui_ready_timeout bounds the wait for the web UI to answer. It matches
# TimeoutStartSec in deploy/go-netmon-webui.service so that this script never
# gives up on a start systemd is still prepared to wait for — the two giving
# different answers about the same start is its own kind of false report.
webui_ready_timeout="${WEBUI_READY_TIMEOUT:-900}"

# wait_serving polls a URL on the target until it answers 200, or until the
# timeout. It is what "deployed" is allowed to mean.
wait_serving() {
    local url="$1" timeout="$2" deadline code started
    started=$SECONDS
    deadline=$(( started + timeout ))

    while (( SECONDS < deadline )); do
        code="$(run_remote "curl -s -o /dev/null -w '%{http_code}' --max-time 5 '$url'" 2>/dev/null || true)"
        if [[ "$code" == "200" ]]; then
            step "$url answered 200 after $(( SECONDS - started ))s"
            return 0
        fi
        sleep 5
    done

    step "$url did not answer 200 within ${timeout}s (last status: ${code:-none})"
    return 1
}

say "Verifying"
# The units are Restart=always, so a service that is going to fail will be
# mid-restart rather than inactive. Give it a moment to settle before judging.
sleep 5
units="go-netmon.service go-netmon-webui.service"
[[ -n "$with_prometheus" ]] && units="prometheus.service $units"

failed=
for u in $units; do
    if run_remote "systemctl is-active --quiet $u"; then
        step "$u active"
    else
        step "$u NOT active"
        failed=1
    fi
done

# THE PORT, NOT THE UNIT. `systemctl is-active` was the only check this script
# made, and for the web UI it was worse than no check at all: the unit was
# Type=simple, so it reported active for the ~3.5 minutes the process spent
# parsing MIBs with nothing listening, and this script printed "Deployed" into
# that window. The unit is Type=notify now and is-active is honest again — but
# what a deploy has to assert is that the service SERVES, and the only thing
# that can assert that is a request that gets an answer. Both checks stay: the
# unit check catches a process that died, the request catches everything else.
not_serving=
if [[ -z "$failed" ]]; then
    step "waiting for the web UI to serve (up to ${webui_ready_timeout}s; a cold MIB catalog takes minutes)"
    if ! wait_serving "http://127.0.0.1:8080/" "$webui_ready_timeout"; then
        failed=1
        not_serving=go-netmon-webui.service
    fi
fi

if [[ -n "$failed" ]]; then
    echo
    echo "The deployment is not serving. Recent logs:" >&2
    for u in $units; do
        # A unit that is up but not answering has to have its journal printed
        # too: that is the whole shape of failure this check exists to catch,
        # and it is invisible to is-active.
        if [[ "$u" != "$not_serving" ]] && run_remote "systemctl is-active --quiet $u"; then
            continue
        fi
        echo "--- $u ---" >&2
        run_remote "sudo journalctl -u $u -n 30 --no-pager" >&2 || true
    done
    exit 1
fi

say "Deployed"
