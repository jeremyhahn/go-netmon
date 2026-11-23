#!/usr/bin/env bash
# lint-early-exit-pipes.sh: refuse a pipe into a consumer that exits early
# (grep -q, grep -...q..., head) in any shell file that sets pipefail (#120).
#
# Usage: scripts/lint-early-exit-pipes.sh FILE...
#
# Why: grep -q exits at its first match and head at its last wanted line. That
# closes the pipe; a producer still writing takes SIGPIPE and exits 141, and
# pipefail reports the 141. So the pipeline fails exactly when the answer is
# yes -- but only once the producer writes more than a pipe buffer, which is
# why such a check passes on a small fixture and inverts on a real one. Written
# "if ! producer | grep -q x; then abort" it ships what it was meant to stop.
#
# Instead, point the tool at the file (grep -q x file), count to EOF
# ([ "$(producer | grep -c x)" -gt 0 ]), capture first and test the capture
# (out="$(producer)"; grep -q x <<<"$out"), or use the producer's own limit
# (find ... -print -quit, grep -m1).
#
# A site that is safe for a reason the lint cannot see is allowed by an inline
# marker carrying that reason, on the consumer's line or the line its pipe is on:
#
#     printf '%s\n' "$one_word" | grep -qx foo  # pipefail-ok: one short write
#
# A marker with no reason is not an allowance.
#
# What it sees: a "|" (not "||") followed on the same line, or at the start of
# the next line, by grep with a q-carrying short option, --quiet or --silent, or
# by head. Whole-line comments are skipped. It is a line scanner, not a parser:
# it errs towards flagging, and the marker is the answer to a false positive.
#
# Exits 0 when clean, 1 when a site is found (each printed as file:line: text),
# 2 on a usage error or an unreadable file.
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
