#!/usr/bin/env bash
# Usage: scripts/lint-early-exit-pipes.sh FILE... -- flags pipes into grep -q or head under pipefail; allow a site with '# pipefail-ok: <reason>'; exit 0 clean, 1 found, 2 usage error.
set -euo pipefail

if [ "$#" -eq 0 ]; then
    printf 'usage: %s FILE...\n' "${0##*/}" >&2
    exit 2
fi

# "set -euo pipefail", "set -o pipefail", "set -e -o pipefail", indented or not.
sets_pipefail_re='^[[:space:]]*set[[:space:]]+(-[[:alnum:]]+[[:space:]]+)*-[[:alpha:]]*o[[:space:]]+pipefail([[:space:]]|;|$)'
# The consumer, anchored where the pipe left off.
consumer='(grep([[:space:]]+-[^[:space:]]*)*[[:space:]]+(-[[:alnum:]]*q[[:alnum:]]*|--quiet|--silent)|head)([[:space:]]|;|\)|$)' # pipefail-ok: a regex, not a pipe
same_line_re="(^|[^|])\\|[[:space:]]*${consumer}"
next_line_re="^[[:space:]]*(\\|[[:space:]]*)?${consumer}"
trailing_pipe_re='(^|[^|])\|[[:space:]]*$'
comment_re='^[[:space:]]*#'
marker_re='#[[:space:]]*pipefail-ok:[[:space:]]*[^[:space:]]'

found=0
for file in "$@"; do
    if [ ! -r "$file" ]; then
        printf 'lint-early-exit-pipes: cannot read %s\n' "$file" >&2
        exit 2
    fi

    pipefail=0
    while IFS= read -r line || [ -n "$line" ]; do
        if [[ $line =~ $sets_pipefail_re ]]; then
            pipefail=1
            break
        fi
    done <"$file"
    [ "$pipefail" -eq 1 ] || continue

    n=0
    prev_pipe=0
    prev_marked=0
    while IFS= read -r line || [ -n "$line" ]; do
        n=$((n + 1))
        if [[ $line =~ $comment_re ]]; then
            continue
        fi
        marked=0
        [[ $line =~ $marker_re ]] && marked=1

        hit=0
        if [[ $line =~ $same_line_re ]]; then
            hit=1
        elif [ "$prev_pipe" -eq 1 ] && [[ $line =~ $next_line_re ]]; then
            hit=1
            [ "$prev_marked" -eq 1 ] && marked=1
        fi
        if [ "$hit" -eq 1 ] && [ "$marked" -eq 0 ]; then
            printf '%s:%d: early-exit consumer piped to under pipefail: %s\n' \
                "$file" "$n" "${line#"${line%%[![:space:]]*}"}"
            found=1
        fi

        prev_pipe=0
        [[ $line =~ $trailing_pipe_re ]] && prev_pipe=1
        prev_marked=$marked
    done <"$file"
done

if [ "$found" -ne 0 ]; then
    printf 'Pipe to a file, count to EOF, or capture first; or mark a safe site with "# pipefail-ok: <reason>".\n' >&2
    exit 1
fi
