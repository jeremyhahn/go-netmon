#!/usr/bin/env bash
# Refuse an e2e suite that could point at production by default (#111).
#
# Usage: e2e-target-guard.sh [ROOT]     (ROOT defaults to this repository)
#
# Exit 0: every address the suite names is on the disposable stack.
# Exit 1: something names production, or an address that is not disposable.
# Exit 2: the suite could not be scanned at all -- never read as a pass.
#
# WHY THIS EXISTS
#
# monitor-01 (198.51.100.50) is the production monitoring server and is read-only
# to the factory: assigning MIBs there once degraded monitoring for fifteen
# hours (#70). The rendering specs used to have no other working target,
# because no disposable stack polled anything, so whether a spec defaulted to
# production was maintained by whoever remembered it. The e2e profile of
# docker-compose.test.yml now gives every spec a disposable stack with polled
# series, and this makes the split a rule instead of a memory.
#
# THE RULE
#
#   * tests/e2e/target.ts holds DISPOSABLE_URL, the one default every spec
#     imports. It must be a local address: the stack "make e2e-up" brings up
#     on the machine running the browser.
#   * No file of the suite names 198.51.100.50 or the host monitor-01, anywhere.
#   * Every URL the suite names is on DISPOSABLE_URL's host and port. An
#     operator reaches another stack through NETMON_URL, which is a decision
#     made at the command line, not a default checked in.
#
# The three specs open PR #199 removes (#70 ruling, 2026-10-14) may not be
# edited, and still default to the older disposable stack on 198.51.100.91:8082.
# They are exempt from the third rule only, for that address only. Delete the
# exemption with them.

set -uo pipefail

ROOT="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
E2E="$ROOT/tests/e2e"
TARGET="$E2E/target.ts"

EXEMPT_FILES=(tests/mib-controls.spec.ts tests/mib-object-picker.spec.ts tests/ui-scale.spec.ts)
EXEMPT_ORIGIN='198.51.100.91:8082'

die() {
    echo "e2e-target-guard: $*" >&2
    exit 2
}

[ -d "$E2E" ] || die "no e2e suite at $E2E"

offences=()

# origin_of prints host:port of a URL, with the scheme's default port filled in,
# so http://localhost and http://localhost:80 are the same place.
origin_of() {
    local url="$1" scheme rest host port
    scheme="${url%%://*}"
    rest="${url#*://}"
    host="${rest%%[:/]*}"
    port=""
    case "$rest" in
        "$host":*) port="${rest#"$host":}"; port="${port%%/*}" ;;
    esac
    if [ -z "$port" ]; then
        case "$scheme" in
            https | wss) port=443 ;;
            *) port=80 ;;
        esac
    fi
    printf '%s:%s\n' "$host" "$port"
}

is_exempt() {
    local f
    for f in "${EXEMPT_FILES[@]}"; do
        [ "$f" = "$1" ] && return 0
    done
    return 1
}

disposable=""
if [ ! -f "$TARGET" ]; then
    offences+=("tests/e2e/target.ts is missing: there is no single default target to check")
else
    disposable="$(grep -m1 -oE "DISPOSABLE_URL = '[^']+'" "$TARGET" | cut -d"'" -f2)"
    case "$disposable" in
        http://localhost:[0-9]* | http://127.0.0.1:[0-9]*) ;;
        "") offences+=("tests/e2e/target.ts sets no DISPOSABLE_URL = '...'") ;;
        *) offences+=("tests/e2e/target.ts: DISPOSABLE_URL is $disposable, which is not a local stack") ;;
    esac
fi
allowed=""
[ -n "$disposable" ] && allowed="$(origin_of "$disposable")"

files=()
while IFS= read -r f; do
    files+=("${f#./}")
done < <(cd "$E2E" && find . -name node_modules -prune -o -type f \
    \( -name '*.ts' -o -name '*.js' -o -name '*.mjs' -o -name '*.cjs' \) -print | sort)
[ "${#files[@]}" -gt 0 ] || die "found no spec sources under $E2E"

for f in "${files[@]}"; do
    path="$E2E/$f"
    while IFS= read -r hit; do
        [ -n "$hit" ] && offences+=("tests/e2e/$f:$hit names production")
    done < <(grep -nE '198\.51\.100\.150|://monitor-01([:/.]|$|[^A-Za-z0-9-])' "$path" | cut -d: -f1)

    while IFS= read -r hit; do
        [ -n "$hit" ] || continue
        line="${hit%%:*}"
        url="${hit#*:}"
        origin="$(origin_of "$url")"
        [ -n "$allowed" ] && [ "$origin" = "$allowed" ] && continue
        if is_exempt "$f" && [ "$origin" = "$EXEMPT_ORIGIN" ]; then
            continue
        fi
        offences+=("tests/e2e/$f:$line names $url, which is not the disposable stack (${disposable:-unset})")
    done < <(grep -noE '(https?|wss?)://[A-Za-z0-9._-]+(:[0-9]+)?' "$path")
done

if [ "${#offences[@]}" -gt 0 ]; then
    echo "e2e-target-guard: the e2e suite can reach something that is not the disposable stack:" >&2
    printf '  %s\n' "${offences[@]}" >&2
    echo "Default every spec to BASE from tests/e2e/target.ts; reach any other stack with NETMON_URL." >&2
    exit 1
fi
echo "e2e-target-guard: every spec defaults to the disposable stack ($disposable)"
