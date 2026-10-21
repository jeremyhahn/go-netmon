#!/usr/bin/env bash
# Check that the tree carries the release a tag names.
#
# Usage: scripts/release-check-tag.sh <tag> [main-ref]   (from the repository root)
#
# The release targets run this on the assembled tree before they commit, and
# release.yml runs it (through `make release-verify`) on the tagged commit
# before any gate or publication. The tag and the tree carry the version in
# places nothing else reconciles (release-versioning.md §2):
#
#   - the tag is vX.Y.Z, vX.Y.Z-alpha or vX.Y.Z-rc.N;
#   - VERSION holds the tag without its v;
#   - CHANGELOG.md has exactly one non-empty section for it
#     (scripts/release-notes.sh, which also produces the release body);
#   - no fragment is left in changelog.d/ and "## [Unreleased]" holds no
#     entry: the release targets fold both into the version's section, so
#     either one surviving at a tag is an entry the release leaves out;
#   - with a main-ref, HEAD is on it: every release kind is a tag on main
#     (§1a), and a tag on a branch commit would release code main never got.

set -euo pipefail

# shellcheck source=lib/release.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/release.sh"

[ $# -ge 1 ] && [ $# -le 2 ] || die "usage: $0 <tag> [main-ref]"
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
    git merge-base --is-ancestor HEAD "$main_ref" || die "$tag is not on $main_ref; every release is a tag on main (release-versioning.md §1a)"
fi

echo "release: $tag is consistent with VERSION and CHANGELOG.md"
