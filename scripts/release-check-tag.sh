#!/usr/bin/env bash
# Check the tree carries the release a tag names: tag form, VERSION, one CHANGELOG section, no pending fragments, HEAD on main-ref. Usage: scripts/release-check-tag.sh <tag> [main-ref]

set -euo pipefail

# shellcheck source=lib/release.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/release.sh"

if [ $# -lt 1 ] || [ $# -gt 2 ]; then
    die "usage: $0 <tag> [main-ref]"
fi
tag="$1"
main_ref="${2:-}"

[[ "$tag" =~ $RELEASE_TAG_RE ]] || die "'$tag' is not vX.Y.Z, vX.Y.Z-alpha or vX.Y.Z-rc.N"

[ -f VERSION ] || die "VERSION does not exist"
current="$(tr -d '[:space:]' <VERSION)"
[ "$current" = "${tag#v}" ] || die "VERSION holds '$current', the tag $tag needs '${tag#v}'"

"$(dirname "${BASH_SOURCE[0]}")/release-notes.sh" "$tag" >/dev/null

while IFS= read -r f; do
    die "fragment $f was never folded into the $tag section (the release targets assemble before tagging)"
done < <(changelog_fragments)

if [ -n "$(changelog_unreleased_body)" ]; then
    die "CHANGELOG.md still has entries under [Unreleased]; they belong in the $tag section"
fi

if [ -n "$main_ref" ]; then
    git rev-parse --verify --quiet "$main_ref^{commit}" >/dev/null || die "cannot resolve $main_ref"
    git merge-base --is-ancestor HEAD "$main_ref" || die "$tag is not on $main_ref; every release is a tag on main"
fi

echo "release: $tag is consistent with VERSION and CHANGELOG.md"
