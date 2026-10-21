#!/usr/bin/env bash
# Refuse a release version that does not move forward.
#
# Usage: scripts/release-version-check.sh <vX.Y.Z[-alpha|-rc.N]>   (from the repository root)
#
# release-versioning.md §3: a VERSION file is never lowered and a published
# tag is never superseded by a lower one -- a backward version silently loses
# whatever the intervening releases fixed, and Go's MVS keeps selecting the
# higher one anyway. So the version must be strictly greater, in SemVer
# precedence, than the VERSION file and than every release tag in the local
# repository. The release targets run this before they write anything.
#
# Tags of another form (a v0.0.0-placeholder, a -beta) are not release tags
# and are not compared.

set -euo pipefail

# shellcheck source=lib/release.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/release.sh"

[ $# -eq 1 ] || die "usage: $0 <vX.Y.Z[-alpha|-rc.N]>"
version="$1"
[[ "$version" =~ $RELEASE_TAG_RE ]] || die "'$version' is not vX.Y.Z, vX.Y.Z-alpha or vX.Y.Z-rc.N"
want="$(release_sort_key "$version")"

# check_above refuses unless $version is strictly above $1; $2 says where $1
# came from.
check_above() {
    local other="$1" from="$2"
    [[ "$other" =~ $RELEASE_TAG_RE ]] || return 0
    if [[ ! "$want" > "$(release_sort_key "$other")" ]]; then
        die "$version is not greater than $other ($from); a release never moves backward (release-versioning.md §3)"
    fi
}

if [ -f VERSION ]; then
    current="$(tr -d '[:space:]' <VERSION)"
    check_above "v$current" "the VERSION file"
fi

while IFS= read -r tag; do
    [ -n "$tag" ] || continue
    check_above "$tag" "tag $tag"
done < <(git tag -l 'v*')
