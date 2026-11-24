#!/usr/bin/env bash
# Refuse a release version not strictly greater than VERSION and every local release tag. Usage: scripts/release-version-check.sh <vX.Y.Z[-alpha|-rc.N]>

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
        die "$version is not greater than $other ($from); a release never moves backward"
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
