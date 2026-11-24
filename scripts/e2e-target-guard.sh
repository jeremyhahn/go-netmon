#!/usr/bin/env bash
# Refuse an e2e suite that names production or a non-disposable address; exit 0 pass, 1 violation, 2 scan failure. Usage: e2e-target-guard.sh [ROOT]

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
    done < <(grep -nE '(^|[^0-9.])198\.51\.100\.50($|[^0-9])|://monitor-01([:/.]|$|[^A-Za-z0-9-])' "$path" | cut -d: -f1)

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
