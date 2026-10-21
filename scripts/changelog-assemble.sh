#!/usr/bin/env bash
# Fold the pending CHANGELOG entries into a release's section.
#
# Usage: scripts/changelog-assemble.sh [--check] <vX.Y.Z[-alpha|-rc.N]> [date]   (from the repository root)
#
# --check runs every validation below and writes nothing: the release
# targets' DRY_RUN=1 runs it, so a dry run fails exactly where the release
# would.
#
# release-versioning.md §5: a change never edits CHANGELOG.md, it adds a
# fragment, changelog.d/<issue>-<slug>.md, holding only its entry under the
# Keep a Changelog subsection headings it lands under. This is the release
# step that turns them into the version's section:
#
#   - the entries under "## [Unreleased]" (if any) and every fragment, in file
#     name order, are merged into one "## [X.Y.Z] - <date>" section placed
#     directly under "## [Unreleased]", which is left empty;
#   - entries under the same "### " heading are grouped under it once, the
#     Keep a Changelog headings first in their usual order, any other heading
#     (a "### Changed — BREAKING: ..." one) after them in the order first seen;
#   - the fragments are deleted.
#
# CHANGELOG.md is created if it does not exist yet. A fragment with text
# outside a "### " heading is refused, as is a release with nothing to
# record. Nothing is committed: the release targets commit the result.

set -euo pipefail

# shellcheck source=lib/release.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/release.sh"

check_only=0
if [ "${1:-}" = --check ]; then
    check_only=1
    shift
fi
[ $# -ge 1 ] && [ $# -le 2 ] || die "usage: $0 [--check] <vX.Y.Z[-alpha|-rc.N]> [date]"
version="$1"
date="${2:-$(date -u +%Y-%m-%d)}"
[[ "$version" =~ $RELEASE_TAG_RE ]] || die "'$version' is not vX.Y.Z, vX.Y.Z-alpha or vX.Y.Z-rc.N"
[[ "$date" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] || die "'$date' is not a YYYY-MM-DD date"
ver="${version#v}"

if [ -f CHANGELOG.md ]; then
    awk '/^## \[Unreleased\][[:space:]]*$/ { found = 1 } END { exit !found }' CHANGELOG.md ||
        die "CHANGELOG.md has no '## [Unreleased]' heading to release from"
    awk -v ver="$ver" 'index($0, "## [" ver "]") == 1 { found = 1 } END { exit !found }' CHANGELOG.md &&
        die "CHANGELOG.md already has a section for $ver"
fi

mapfile -t fragments < <(changelog_fragments)

# The entries to release, one stream: the [Unreleased] body, then each
# fragment, each source introduced by a "\x01<name>" line so the grouping
# below can name the one that is malformed.
entries="$(
    printf '\001CHANGELOG.md [Unreleased]\n'
    changelog_unreleased_body
    for f in "${fragments[@]}"; do
        printf '\001%s\n' "$f"
        cat "$f"
        printf '\n'
    done
)"

section="$(printf '%s\n' "$entries" | awk '
    BEGIN {
        n = split("Added Changed Deprecated Removed Fixed Security", std, " ")
        for (i = 1; i <= n; i++) rank["### " std[i]] = i
    }
    /^\001/ { src = substr($0, 2); head = ""; next }
    /^### / {
        head = $0
        sub(/[[:space:]]+$/, "", head)
        if (!(head in body)) { order[++heads] = head; body[head] = "" }
        # A blank line counts only between two lines of one run under a
        # heading: runs from different sources join without one.
        added = 0
        pending = 0
        next
    }
    /^#/ { printf "release: %s: heading \"%s\" is not a \"### \" subsection\n", src, $0 > "/dev/stderr"; bad = 1; next }
    {
        if (head == "") {
            if (NF) { printf "release: %s: \"%s\" is outside a \"### \" heading\n", src, $0 > "/dev/stderr"; bad = 1 }
            next
        }
        # Blank lines are kept between entries but never lead or trail one.
        if (NF) {
            if (pending) body[head] = body[head] "\n"
            body[head] = body[head] $0 "\n"
            added = 1
            pending = 0
            seen[head] = 1
        } else if (added) {
            pending = 1
        }
    }
    END {
        if (bad) exit 2
        out = ""
        for (r = 1; r <= n; r++)
            if (("### " std[r]) in seen) out = out "\n" "### " std[r] "\n\n" body["### " std[r]]
        for (i = 1; i <= heads; i++)
            if (!(order[i] in rank) && (order[i] in seen)) out = out "\n" order[i] "\n\n" body[order[i]]
        printf "%s", out
    }
')" || die "a CHANGELOG entry is malformed (see above); nothing was changed"

[ -n "$section" ] || die "nothing to release: changelog.d/ holds no fragment and [Unreleased] is empty"
[ "$check_only" = 0 ] || exit 0

tmp="$(mktemp CHANGELOG.md.XXXXXX)"
trap 'rm -f "$tmp"' EXIT

{
    if [ -f CHANGELOG.md ]; then
        # Everything up to and including the [Unreleased] heading, then the new
        # section, then everything from the next "## " heading on: the old
        # [Unreleased] body is what was just folded in.
        # The section goes through the environment: awk -v would interpret
        # backslash escapes in an entry.
        SECT="## [$ver] - $date"$'\n'"$section" awk '
            /^## \[Unreleased\][[:space:]]*$/ { print; print ""; print ENVIRON["SECT"]; print ""; skipping = 1; next }
            skipping && /^## / { skipping = 0 }
            !skipping { print }
        ' CHANGELOG.md
    else
        printf '# Changelog\n\n'
        printf 'All notable changes to go-netmon are recorded here, in the format of\n'
        printf '[Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/); versions follow\n'
        printf '[Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html). A change adds a\n'
        printf 'fragment to changelog.d/ instead of editing this file (changelog.d/README.md).\n\n'
        printf '## [Unreleased]\n\n'
        printf '## [%s] - %s\n%s\n' "$ver" "$date" "$section"
    fi
} >"$tmp"

mv "$tmp" CHANGELOG.md
trap - EXIT
if [ "${#fragments[@]}" -gt 0 ]; then
    rm -f -- "${fragments[@]}"
fi
