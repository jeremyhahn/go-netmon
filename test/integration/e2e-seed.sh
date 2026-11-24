#!/usr/bin/env bash
# Seed the e2e web UI by importing test/integration/snmp-data/e2e-config.json via POST /api/config/import?apply=true; idempotent.

set -euo pipefail

WEBUI_URL="${WEBUI_URL:-http://webui:8080}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DOCUMENT="$ROOT/test/integration/snmp-data/e2e-config.json"
MIB_SOURCES="$ROOT/mibs/standard"

die() {
    echo "e2e-seed: $*" >&2
    exit 1
}

[ -f "$DOCUMENT" ] || die "the configuration document $DOCUMENT is missing"

bundle_dir="$(mktemp -d)"
trap 'rm -rf "$bundle_dir"' EXIT
mkdir "$bundle_dir/mibs"
cp "$DOCUMENT" "$bundle_dir/config.json"

# The bundle carries exactly the sources the document names; the import checks
# each against the document's sha256.
files="$(grep -oE '"file": *"mibs/[^"]+"' "$DOCUMENT" | grep -oE 'mibs/[^"]+')" ||
    die "$DOCUMENT names no MIB source files"
for file in $files; do
    [ -f "$MIB_SOURCES/${file#mibs/}" ] || die "$DOCUMENT names $file, which mibs/standard does not hold"
    cp "$MIB_SOURCES/${file#mibs/}" "$bundle_dir/$file"
done
tar -czf "$bundle_dir/bundle.tar.gz" -C "$bundle_dir" config.json mibs

ready=""
for _ in $(seq 1 30); do
    if curl -fsS -o /dev/null "$WEBUI_URL/api/devices"; then
        ready=yes
        break
    fi
    sleep 1
done
[ -n "$ready" ] || die "the web UI at $WEBUI_URL did not answer GET /api/devices"

if ! response="$(curl -sS -w '\n%{http_code}' -X POST \
    -H 'Content-Type: application/gzip' --data-binary "@$bundle_dir/bundle.tar.gz" \
    "$WEBUI_URL/api/config/import?apply=true")"; then
    die "POST /api/config/import did not complete"
fi
status="${response##*$'\n'}"
case "$status" in
    2??) ;;
    *) die "POST /api/config/import?apply=true answered $status: ${response%$'\n'*}" ;;
esac
echo "e2e-seed: restored $DOCUMENT ($WEBUI_URL)"
