#!/usr/bin/env bash
# Shared helpers for the release scripts (scripts/release*.sh,
# scripts/changelog-assemble.sh). Sourced, never run.
#
# The version forms are release-versioning.md §1 and §1a: vX.Y.Z (a
# production release), vX.Y.Z-rc.N (a release candidate, N from 1) and
# vX.Y.Z-alpha (an internal alpha). Nothing else is a release version here: a
# -beta, build metadata or a leading zero is refused rather than tagged.

# Byte-wise comparison and sorting: release_sort_key's keys are compared as
# strings, and a locale's collation may skip the dots in them.
export LC_ALL=C

# shellcheck disable=SC2034  # read by the scripts that source this file
RELEASE_TAG_RE='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-alpha|-rc\.[1-9][0-9]*)?$'

die() {
    printf 'release: %s\n' "$*" >&2
    exit 1
}

# release_kind_of prints the kind of a well-formed tag: release, candidate or
# alpha.
release_kind_of() {
    case "$1" in
        *-alpha) echo alpha ;;
        *-rc.*) echo candidate ;;
        *) echo release ;;
    esac
}

# release_sort_key prints a key for a well-formed tag such that the keys of
# two tags compare as strings in SemVer precedence order: the core numerically,
# then alpha < rc.1 < rc.2 < ... < the production release of the same core
# ("alpha" sorts before "rc" as SemVer compares identifiers ASCII-wise).
release_sort_key() {
    local v="${1#v}" core pre rank=2 n=0
    core="${v%%-*}"
    pre=""
    [ "$core" = "$v" ] || pre="${v#*-}"
    case "$pre" in
        alpha) rank=0 ;;
        rc.*) rank=1; n="${pre#rc.}" ;;
    esac
    local IFS=.
    # shellcheck disable=SC2086  # split on dots is the point
    set -- $core
    printf '%010d.%010d.%010d.%d.%010d\n' "$1" "$2" "$3" "$rank" "$n"
}

# changelog_fragments lists the pending fragments in changelog.d/, sorted.
# README.md is the directory's documentation, not an entry.
changelog_fragments() {
    local f
    [ -d changelog.d ] || return 0
    for f in changelog.d/*.md; do
        [ -e "$f" ] || continue
        [ "$f" = changelog.d/README.md ] && continue
        printf '%s\n' "$f"
    done
}

# changelog_unreleased_body prints the non-blank lines under
# "## [Unreleased]" in CHANGELOG.md, up to the next "## " heading.
changelog_unreleased_body() {
    [ -f CHANGELOG.md ] || return 0
    awk '
        /^## / { in_u = ($0 ~ /^## \[Unreleased\][[:space:]]*$/); next }
        in_u && NF { print }
    ' CHANGELOG.md
}
