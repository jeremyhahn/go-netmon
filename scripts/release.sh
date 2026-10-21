#!/usr/bin/env bash
# Cut a release of one kind (release-versioning.md §1a):
#
#   make release-alpha     [VERSION=vX.Y.Z-alpha]
#   make release-candidate VERSION=vX.Y.Z-rc.N
#   make release           VERSION=vX.Y.Z
#
# Usage: scripts/release.sh <alpha|candidate|release>   (from the repository root)
#
# The argument is the kind the Makefile target stands for. The version must be
# of exactly that kind; the other two are refused. A candidate or a production
# release names its version explicitly -- the recipe never guesses a minor or
# a major. An alpha may leave it out: the next alpha is then the VERSION
# file's patch plus one (0.1.3 -> v0.1.4-alpha), as devops.md §4 has it.
#
# Environment (exported by the Makefile, read raw, never interpolated):
#   VERSION          the version; empty means "not given" (the Makefile
#                    blanks its own VERSION-file default)
#   DRY_RUN          1 to run every check and print the tag, writing nothing
#   RELEASE_REMOTE   the remote to release to (default dev, the Gitea plane;
#                    agents never push to github, version-control.md)
#   RELEASE_GIT      the command that writes: commits, tags and pushes
#                    (default git; on a host, "<company>/software/scripts/
#                    agent-git.sh go-netmon", which signs as the agent)
#
# Every check runs before anything is written: the version's form and kind,
# the branch (main), a clean tree, local main equal to the remote's, the tag
# free locally and on the remote (ls-remote exit 2 is the only "free";
# anything else fails closed), the version moving forward
# (release-version-check.sh), and something to release that folds cleanly
# (changelog-assemble.sh --check). Then it folds
# changelog.d/ into CHANGELOG.md, writes VERSION, checks the result
# (release-check-tag.sh), commits, tags, and pushes main and the tag in one
# atomic push. The tag starts release.yml.

set -euo pipefail

# shellcheck source=lib/release.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/release.sh"
script_dir="$(dirname "${BASH_SOURCE[0]}")"

[ $# -eq 1 ] || die "usage: $0 <alpha|candidate|release>"
kind="$1"
case "$kind" in
    alpha) name="make release-alpha"; target="$name [VERSION=vX.Y.Z-alpha]"; forms="vX.Y.Z-alpha" ;;
    candidate) name="make release-candidate"; target="$name VERSION=vX.Y.Z-rc.N"; forms="vX.Y.Z-rc.N" ;;
    release) name="make release"; target="$name VERSION=vX.Y.Z"; forms="vX.Y.Z" ;;
    *) die "usage: $0 <alpha|candidate|release>" ;;
esac

version="${VERSION:-}"
remote="${RELEASE_REMOTE:-dev}"
case "${DRY_RUN:-}" in
    '' | 0) dry_run=0 ;;
    1) dry_run=1 ;;
    *) die "DRY_RUN is '${DRY_RUN}'; it is 1 or unset" ;;
esac
read -r -a write_git <<<"${RELEASE_GIT:-git}"
[ "${#write_git[@]}" -gt 0 ] || die "RELEASE_GIT is blank"

if [ -z "$version" ]; then
    [ "$kind" = alpha ] ||
        die "state the version explicitly: $target (release-versioning.md decides patch, minor or major; the recipe refuses to guess)"
    [ -f VERSION ] || die "VERSION does not exist, so there is no next alpha to derive; state it: make release-alpha VERSION=vX.Y.Z-alpha"
    current="$(tr -d '[:space:]' <VERSION)"
    [[ "v$current" =~ $RELEASE_TAG_RE ]] || die "VERSION holds '$current', which is no release version; state the alpha explicitly"
    core="${current%%-*}"
    version="v${core%.*}.$((${core##*.} + 1))-alpha"
fi

case "$version" in
    *[!0-9A-Za-z.-]*) die "the version holds characters no release version does; refusing it" ;;
esac
[[ "$version" =~ $RELEASE_TAG_RE ]] || die "'$version' is not vX.Y.Z, vX.Y.Z-alpha or vX.Y.Z-rc.N"
if [ "$(release_kind_of "$version")" != "$kind" ]; then
    case "$(release_kind_of "$version")" in
        alpha) hint="make release-alpha" ;;
        candidate) hint="make release-candidate" ;;
        release) hint="make release" ;;
    esac
    die "'$version' is not $forms; $name cuts only that kind ($hint cuts $version)"
fi

branch="$(git symbolic-ref --quiet --short HEAD || true)"
[ "$branch" = main ] || die "releases are cut from main; you are on ${branch:-a detached HEAD}"

[ -z "$(git status --porcelain)" ] || die "working tree not clean; commit or stash first"

git remote get-url "$remote" >/dev/null 2>&1 || die "no '$remote' remote; releases go to the Gitea plane (set RELEASE_REMOTE to its name)"

git rev-parse --verify --quiet "refs/tags/$version" >/dev/null &&
    die "tag $version already exists locally; a tag is never moved (release-versioning.md §3)"

set +e
git ls-remote --exit-code --tags "$remote" "refs/tags/$version" >/dev/null 2>&1
status=$?
set -e
case "$status" in
    0) die "tag $version already exists on $remote; a tag is never moved (release-versioning.md §3)" ;;
    2) ;;
    *) die "could not query $remote for tag $version (git ls-remote exit $status); refusing to release blind" ;;
esac

remote_main="$(git ls-remote --heads "$remote" refs/heads/main)" ||
    die "could not query $remote for main"
remote_main="${remote_main%%[[:space:]]*}"
[ "$remote_main" = "$(git rev-parse HEAD)" ] ||
    die "local main is not $remote's main (${remote_main:-absent}); pull or push first, so the release is exactly what main holds"

"$script_dir/release-version-check.sh" "$version"

mapfile -t fragments < <(changelog_fragments)
if [ "${#fragments[@]}" -eq 0 ] && [ -z "$(changelog_unreleased_body)" ]; then
    die "nothing to release: changelog.d/ holds no fragment and [Unreleased] is empty"
fi
# The fold itself, checked without writing: a malformed or empty fragment, a
# section that already exists for the version or a CHANGELOG.md with no
# [Unreleased] fails here, a dry run included.
"$script_dir/changelog-assemble.sh" --check "$version"

if [ "$dry_run" = 1 ]; then
    echo "release: would cut $version ($kind) from main at $(git rev-parse --short HEAD) and push it to $remote"
    if [ "${#fragments[@]}" -gt 0 ]; then
        printf 'release: folding %s\n' "${fragments[@]}"
    fi
    echo "release: DRY_RUN=1, nothing was written"
    exit 0
fi

"$script_dir/changelog-assemble.sh" "$version"
printf '%s\n' "${version#v}" >VERSION
"$script_dir/release-check-tag.sh" "$version"

"${write_git[@]}" add -A VERSION CHANGELOG.md changelog.d
"${write_git[@]}" commit -q -m "release: $version"
"${write_git[@]}" tag -a -m "go-netmon $version" "$version"
if ! "${write_git[@]}" push --atomic "$remote" HEAD:refs/heads/main "refs/tags/$version"; then
    die "the push failed; the release commit and tag $version exist only here. Undo with: git tag -d $version && git reset --hard HEAD~1"
fi
echo "release: $version pushed to $remote; release.yml takes it from here"
