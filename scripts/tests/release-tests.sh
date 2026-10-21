#!/usr/bin/env bash
# Tests for the release scripts and their Makefile targets (#179):
# scripts/release.sh behind make release-alpha, release-candidate and release;
# changelog-assemble.sh, release-version-check.sh, release-notes.sh,
# release-check-tag.sh and release-publish.sh.
#
# Every release runs in a scratch repository whose "dev" remote is a bare
# repository beside it, so a test can see exactly what a release pushed; the
# forge is a fake curl that records its calls. Nothing here touches this
# checkout, the network or a real remote. The Makefile targets are driven
# against this checkout only with versions they must refuse before looking at
# anything else.
#
# Usage: release-tests.sh [test-name-substring]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
SCRIPTS="$REPO_DIR/scripts"

PASS=0
FAIL=0
FAILED_TESTS=()

OUT=""

# run captures a command's combined output into $OUT and returns its status.
run() {
    set +e
    OUT="$("$@" 2>&1)"
    local status=$?
    set -e
    return $status
}

expect_success() {
    local desc="$1"
    shift
    if ! run "$@"; then
        printf '    FAIL: %s: expected success, got failure\n%s\n' "$desc" "$OUT" >&2
        return 1
    fi
}

expect_failure() {
    local desc="$1"
    shift
    if run "$@"; then
        printf '    FAIL: %s: expected failure, but it succeeded\n%s\n' "$desc" "$OUT" >&2
        return 1
    fi
}

expect_output() {
    local desc="$1" needle="$2"
    case "$OUT" in
        *"$needle"*) ;;
        *)
            printf '    FAIL: %s: output did not contain %q\n%s\n' "$desc" "$needle" "$OUT" >&2
            return 1
            ;;
    esac
}

expect_equal() {
    local desc="$1" want="$2" got="$3"
    if [ "$want" != "$got" ]; then
        printf '    FAIL: %s:\n      want %q\n      got  %q\n' "$desc" "$want" "$got" >&2
        return 1
    fi
}

# --- fixtures -------------------------------------------------------------

# new_repo makes a released-at-v0.1.3 repository on main in $REPO, with one
# pending fragment and a "dev" remote ($BARE) holding main and the tag.
new_repo() {
    local name="${1:-repo}"
    REPO="$TMPDIR_TEST/$name"
    BARE="$TMPDIR_TEST/$name.git"
    rm -rf "$REPO" "$BARE"
    git init -q --bare "$BARE"
    git init -q "$REPO"
    (
        cd "$REPO"
        echo 0.1.3 >VERSION
        mkdir changelog.d
        echo "# changelog.d" >changelog.d/README.md
        printf '### Added\n\n- release targets\n' >changelog.d/179-release.md
        git add -A
        git commit -q -m initial
        git tag v0.1.3
        git remote add dev "$BARE"
        git push -q dev main refs/tags/v0.1.3
    )
}

# state prints everything a refused release must leave alone.
state() {
    git -C "$REPO" rev-parse HEAD
    git -C "$REPO" status --porcelain
    git -C "$REPO" tag -l
    git -C "$BARE" rev-parse main
    git -C "$BARE" tag -l
}

# release KIND VERSION runs scripts/release.sh in $REPO.
release() {
    local kind="$1" version="$2"
    shift 2
    (cd "$REPO" && env VERSION="$version" "$@" "$SCRIPTS/release.sh" "$kind")
}

# refused KIND VERSION NEEDLE: the release is refused, says NEEDLE, and the
# repository and its remote are exactly as they were.
refused() {
    local kind="$1" version="$2" needle="$3"
    shift 3
    local before
    before="$(state)"
    expect_failure "$kind $version refused" release "$kind" "$version" "$@" || return 1
    expect_output "$kind $version refusal" "$needle" || return 1
    expect_equal "$kind $version left the repository alone" "$before" "$(state)"
}

# --- kinds ----------------------------------------------------------------

test_each_kind_refuses_the_other_kinds() {
    new_repo
    refused alpha v0.1.4-rc.1 "'v0.1.4-rc.1' is not vX.Y.Z-alpha; make release-alpha cuts only that kind" || return 1
    refused alpha v0.1.4 "'v0.1.4' is not vX.Y.Z-alpha;" || return 1
    refused candidate v0.1.4-alpha "'v0.1.4-alpha' is not vX.Y.Z-rc.N; make release-candidate cuts only that kind" || return 1
    refused candidate v0.1.4 "'v0.1.4' is not vX.Y.Z-rc.N;" || return 1
    refused release v0.1.4-alpha "'v0.1.4-alpha' is not vX.Y.Z; make release cuts only that kind" || return 1
    refused release v0.1.4-rc.1 "'v0.1.4-rc.1' is not vX.Y.Z;"
}

# The real targets, in this checkout: the kind is refused before the branch,
# the tree or the remote is looked at, so nothing here is touched.
test_make_targets_refuse_the_other_kinds() {
    local target version want
    while read -r target version want; do
        expect_failure "make $target VERSION=$version" make -C "$REPO_DIR" --no-print-directory "$target" "VERSION=$version" || return 1
        expect_output "make $target VERSION=$version" "is not $want; make $target cuts only that kind" || return 1
    done <<'EOF'
release-alpha v0.1.4-rc.1 vX.Y.Z-alpha
release-alpha v0.1.4 vX.Y.Z-alpha
release-candidate v0.1.4-alpha vX.Y.Z-rc.N
release-candidate v0.1.4 vX.Y.Z-rc.N
release v0.1.4-alpha vX.Y.Z
release v0.1.4-rc.1 vX.Y.Z
EOF
}

test_unknown_or_missing_kind_is_a_usage_error() {
    new_repo
    local kind
    for kind in "" any rc Release; do
        refused "$kind" v0.1.4 "usage:" || return 1
    done
    expect_failure "no kind" bash -c "cd '$REPO' && VERSION=v0.1.4 '$SCRIPTS/release.sh'" || return 1
    expect_output "no kind" "usage:"
}

test_candidate_and_release_require_an_explicit_version() {
    new_repo
    refused candidate "" "state the version explicitly: make release-candidate VERSION=vX.Y.Z-rc.N" || return 1
    refused release "" "state the version explicitly: make release VERSION=vX.Y.Z" || return 1
    expect_failure "make release-candidate with no VERSION" make -C "$REPO_DIR" --no-print-directory release-candidate || return 1
    expect_output "make release-candidate with no VERSION" "state the version explicitly"
}

test_malformed_versions_are_refused() {
    new_repo
    local forms="is not vX.Y.Z, vX.Y.Z-alpha or vX.Y.Z-rc.N"
    refused candidate v0.1.4-rc.0 "$forms" || return 1
    refused candidate v0.1.4-rc.01 "$forms" || return 1
    refused candidate v0.1.4-rc "$forms" || return 1
    refused candidate v0.1.4-beta "$forms" || return 1
    refused release v0.1.4-beta "$forms" || return 1
    refused candidate v0.1.4-rc.1-alpha "$forms" || return 1
    refused candidate 0.1.4-rc.1 "$forms" || return 1
    refused release v0.1 "$forms" || return 1
    refused release v0.01.4 "$forms" || return 1
    refused release v0.1.4+build "characters no release version does" || return 1
    refused candidate $'v0.1.4-rc.1\nv0.1.4-rc.2' "characters no release version does"
}

# --- dry run and the default alpha ----------------------------------------

test_dry_run_prints_the_tag_and_writes_nothing() {
    new_repo
    local before
    before="$(state)"
    expect_success "dry run" release candidate v0.1.4-rc.1 DRY_RUN=1 || return 1
    expect_output "dry run names the tag" "would cut v0.1.4-rc.1 (candidate)" || return 1
    expect_output "dry run names the fragment" "folding changelog.d/179-release.md" || return 1
    expect_equal "dry run wrote nothing" "$before" "$(state)"
}

test_alpha_defaults_to_the_next_patch() {
    new_repo
    expect_success "alpha dry run without a version" release alpha "" DRY_RUN=1 || return 1
    expect_output "next patch alpha" "would cut v0.1.4-alpha (alpha)"
}

test_dry_run_takes_only_one() {
    new_repo
    refused alpha v0.1.4-alpha "DRY_RUN is 'yes'" DRY_RUN=yes
}

# commit_and_push commits whatever the test changed in $REPO and pushes main,
# so the release's clean-tree and remote-main checks pass.
commit_and_push() {
    git -C "$REPO" add -A
    git -C "$REPO" commit -q -m "$1"
    git -C "$REPO" push -q dev main
}

# A dry run validates the fold the release would make, and still writes
# nothing when it fails.
test_dry_run_refuses_what_the_fold_would_refuse() {
    local file content needle
    while IFS='|' read -r file content needle; do
        new_repo
        printf '%b' "$content" >"$REPO/$file"
        commit_and_push "$file"
        refused candidate v0.1.4-rc.1 "$needle" DRY_RUN=1 || return 1
    done <<'EOF'
changelog.d/180-bad.md|- no heading\n|"- no heading" is outside a "### " heading
changelog.d/180-bad.md|## Added\n\n- wrong level\n|is not a "### " subsection
changelog.d/179-release.md|\n\n|nothing to release
CHANGELOG.md|# Changelog\n\n## [Unreleased]\n\n## [0.1.4-rc.1] - 2026-01-01\n\n### Added\n\n- x\n|already has a section for 0.1.4-rc.1
CHANGELOG.md|# Changelog\n\n## [0.1.3] - 2026-01-01\n\n### Added\n\n- x\n|has no '## [Unreleased]' heading
EOF
}

# --- refusals before anything is written ----------------------------------

test_another_branch_is_refused() {
    new_repo
    git -C "$REPO" checkout -q -b topic
    refused candidate v0.1.4-rc.1 "releases are cut from main; you are on topic"
}

test_dirty_tree_is_refused() {
    new_repo
    echo x >"$REPO/stray.txt"
    refused candidate v0.1.4-rc.1 "working tree not clean"
}

test_existing_tag_is_refused() {
    new_repo
    git -C "$REPO" tag v0.1.4-rc.1
    refused candidate v0.1.4-rc.1 "already exists locally"
}

test_tag_on_the_remote_is_refused() {
    new_repo
    git -C "$REPO" tag v0.1.4-rc.1
    git -C "$REPO" push -q dev refs/tags/v0.1.4-rc.1
    git -C "$REPO" tag -d v0.1.4-rc.1 >/dev/null
    refused candidate v0.1.4-rc.1 "already exists on dev"
}

test_unreachable_remote_fails_closed() {
    new_repo
    git -C "$REPO" remote set-url dev "$TMPDIR_TEST/missing.git"
    refused candidate v0.1.4-rc.1 "could not query dev"
}

test_missing_remote_is_refused() {
    new_repo
    git -C "$REPO" remote remove dev
    refused candidate v0.1.4-rc.1 "no 'dev' remote"
}

test_local_main_must_equal_the_remote() {
    new_repo
    git -C "$REPO" commit -q --allow-empty -m unpushed
    refused candidate v0.1.4-rc.1 "local main is not dev's main"
}

test_version_must_move_forward() {
    new_repo
    refused alpha v0.1.3-alpha "v0.1.3-alpha is not greater than v0.1.3 (the VERSION file)" || return 1
    refused release v0.1.2 "v0.1.2 is not greater than v0.1.3" || return 1
    # The tag check comes first for a version that was already released.
    refused release v0.1.3 "tag v0.1.3 already exists locally"
}

test_version_must_be_above_every_release_tag() {
    new_repo
    git -C "$REPO" tag v0.1.4-rc.1
    git -C "$REPO" push -q dev refs/tags/v0.1.4-rc.1
    refused alpha v0.1.4-alpha "v0.1.4-alpha is not greater than v0.1.4-rc.1 (tag v0.1.4-rc.1)"
}

test_nothing_to_release_is_refused() {
    new_repo
    git -C "$REPO" rm -q changelog.d/179-release.md
    git -C "$REPO" commit -q -m "drop the fragment"
    git -C "$REPO" push -q dev main
    refused candidate v0.1.4-rc.1 "nothing to release"
}

# --- releasing --------------------------------------------------------------

# released VERSION: the remote holds the tag on its main, VERSION and the
# CHANGELOG carry it, and no fragment is left.
released() {
    local version="$1"
    expect_equal "$version: tag pushed on main" "$(git -C "$BARE" rev-parse main)" "$(git -C "$BARE" rev-parse "$version^{commit}")" || return 1
    expect_equal "$version: VERSION" "${version#v}" "$(git -C "$BARE" show "$version:VERSION")" || return 1
    expect_equal "$version: fragments folded" "changelog.d/README.md" "$(git -C "$BARE" ls-tree -r --name-only "$version" changelog.d)" || return 1
    run git -C "$BARE" show "$version:CHANGELOG.md" || return 1
    expect_output "$version: CHANGELOG section" "## [${version#v}] - " || return 1
    expect_output "$version: CHANGELOG entry" "- release targets" || return 1
    expect_equal "$version: tree clean" "" "$(git -C "$REPO" status --porcelain)"
}

test_each_kind_tags_and_pushes() {
    local kind version
    while read -r kind version; do
        new_repo
        expect_success "$kind $version" release "$kind" "$version" || return 1
        expect_output "$kind $version pushed" "$version pushed to dev" || return 1
        released "$version" || return 1
    done <<'EOF'
alpha v0.1.4-alpha
candidate v0.1.4-rc.1
candidate v0.1.4-rc.12
release v0.1.4
release v0.2.0
EOF
}

test_default_alpha_releases_the_next_patch() {
    new_repo
    expect_success "alpha without a version" release alpha "" || return 1
    released v0.1.4-alpha
}

test_candidates_follow_one_another() {
    new_repo
    expect_success "rc.1" release candidate v0.1.4-rc.1 || return 1
    refused candidate v0.1.4-rc.1 "already exists locally" || return 1
    printf '### Fixed\n\n- a second candidate\n' >"$REPO/changelog.d/180-fix.md"
    git -C "$REPO" add -A
    git -C "$REPO" commit -q -m fix
    git -C "$REPO" push -q dev main
    refused alpha v0.1.4-alpha "is not greater than" || return 1
    expect_success "rc.2" release candidate v0.1.4-rc.2 || return 1
    expect_success "production" bash -c "cd '$REPO' && printf '### Fixed\n\n- final\n' >changelog.d/181-final.md && git add -A && git commit -q -m final && git push -q dev main" || return 1
    expect_success "v0.1.4" release release v0.1.4 || return 1
    expect_equal "tags on the remote" "$(printf 'v0.1.3\nv0.1.4\nv0.1.4-rc.1\nv0.1.4-rc.2')" "$(git -C "$BARE" tag -l)"
}

test_failed_push_says_how_to_undo() {
    new_repo
    # A remote that answers ls-remote but refuses the push.
    printf '#!/bin/sh\nexit 1\n' >"$BARE/hooks/pre-receive"
    chmod +x "$BARE/hooks/pre-receive"
    expect_failure "rejected push" release candidate v0.1.4-rc.1 || return 1
    expect_output "undo instructions" "git tag -d v0.1.4-rc.1 && git reset --hard HEAD~1" || return 1
    expect_equal "nothing reached the remote" "v0.1.3" "$(git -C "$BARE" tag -l)"
}

# --- changelog-assemble.sh --------------------------------------------------

test_assemble_groups_headings_and_keeps_history() {
    new_repo
    (
        cd "$REPO"
        printf '# Changelog\n\n## [Unreleased]\n\n### Fixed\n\n- from unreleased\n\n## [0.1.3] - 2026-01-01\n\n### Added\n\n- old entry\n' >CHANGELOG.md
        printf '### Fixed\n\n- a fix with a \\\\backslash\n\n### Changed — BREAKING: wire format\n\n- breaking\n' >changelog.d/180-fix.md
        printf '### Security\n\n- tls\n\n### Added\n\n- second add\n' >changelog.d/181-sec.md
    )
    expect_success "assemble" bash -c "cd '$REPO' && '$SCRIPTS/changelog-assemble.sh' v0.1.4 2026-10-11" || return 1
    local want
    want="$(printf '%s\n' \
        '# Changelog' '' '## [Unreleased]' '' '## [0.1.4] - 2026-10-11' '' \
        '### Added' '' '- release targets' '- second add' '' \
        '### Fixed' '' '- from unreleased' '- a fix with a \\backslash' '' \
        '### Security' '' '- tls' '' \
        '### Changed — BREAKING: wire format' '' '- breaking' '' \
        '## [0.1.3] - 2026-01-01' '' '### Added' '' '- old entry')"
    expect_equal "assembled CHANGELOG" "$want" "$(cat "$REPO/CHANGELOG.md")" || return 1
    expect_equal "fragments deleted" "changelog.d/README.md" "$(cd "$REPO" && echo changelog.d/*)"
}

test_assemble_refuses_text_outside_a_heading() {
    new_repo
    printf -- '- no heading\n' >"$REPO/changelog.d/180-bad.md"
    expect_failure "malformed fragment" bash -c "cd '$REPO' && '$SCRIPTS/changelog-assemble.sh' v0.1.4" || return 1
    expect_output "names the fragment" "changelog.d/180-bad.md" || return 1
    expect_equal "nothing changed" "?? changelog.d/180-bad.md" "$(git -C "$REPO" status --porcelain)"
}

test_assemble_refuses_a_version_already_in_the_changelog() {
    new_repo
    printf '# Changelog\n\n## [Unreleased]\n\n## [0.1.4] - 2026-01-01\n\n### Added\n\n- x\n' >"$REPO/CHANGELOG.md"
    expect_failure "duplicate section" bash -c "cd '$REPO' && '$SCRIPTS/changelog-assemble.sh' v0.1.4" || return 1
    expect_output "duplicate section" "already has a section for 0.1.4"
}

# --- release-notes.sh and release-check-tag.sh -----------------------------

test_notes_match_the_exact_version() {
    new_repo
    printf '# Changelog\n\n## [Unreleased]\n\n## [0.1.40] - 2026-02-01\n\n### Added\n\n- forty\n\n## [0.1.4] - 2026-01-01\n\n### Added\n\n- four\n' >"$REPO/CHANGELOG.md"
    expect_success "notes" bash -c "cd '$REPO' && '$SCRIPTS/release-notes.sh' v0.1.4" || return 1
    expect_equal "notes body" "$(printf '### Added\n\n- four')" "$OUT" || return 1
    expect_failure "no section" bash -c "cd '$REPO' && '$SCRIPTS/release-notes.sh' v0.1.5" || return 1
    expect_output "no section" "no section for ## [0.1.5]"
}

test_notes_refuse_an_empty_section() {
    new_repo
    printf '# Changelog\n\n## [Unreleased]\n\n## [0.1.4] - 2026-01-01\n\n## [0.1.3] - 2026-01-01\n\n- x\n' >"$REPO/CHANGELOG.md"
    expect_failure "empty section" bash -c "cd '$REPO' && '$SCRIPTS/release-notes.sh' v0.1.4" || return 1
    expect_output "empty section" "is empty"
}

# check_tag runs release-check-tag.sh in $REPO.
check_tag() {
    (cd "$REPO" && "$SCRIPTS/release-check-tag.sh" "$@")
}

test_check_tag_reconciles_tag_version_and_changelog() {
    new_repo
    expect_success "release" release candidate v0.1.4-rc.1 || return 1
    expect_success "consistent" check_tag v0.1.4-rc.1 refs/heads/main || return 1
    expect_failure "other tag" check_tag v0.1.4-rc.2 || return 1
    expect_output "other tag" "VERSION holds '0.1.4-rc.1', the tag v0.1.4-rc.2 needs '0.1.4-rc.2'" || return 1

    echo "- leftover" >"$REPO/changelog.d/182-late.md"
    expect_failure "leftover fragment" check_tag v0.1.4-rc.1 || return 1
    expect_output "leftover fragment" "fragment changelog.d/182-late.md was never folded" || return 1
    rm "$REPO/changelog.d/182-late.md"

    (cd "$REPO" && awk '{ print } /^## \[Unreleased\]/ { print ""; print "- pending" }' CHANGELOG.md >c.tmp && mv c.tmp CHANGELOG.md)
    expect_failure "unreleased entries" check_tag v0.1.4-rc.1 || return 1
    expect_output "unreleased entries" "still has entries under [Unreleased]" || return 1
    git -C "$REPO" checkout -q -- CHANGELOG.md

    git -C "$REPO" checkout -q -b topic
    git -C "$REPO" commit -q --allow-empty -m "off main"
    expect_failure "not on main" check_tag v0.1.4-rc.1 refs/heads/main || return 1
    expect_output "not on main" "is not on refs/heads/main"
}

# --- release-publish.sh -----------------------------------------------------

# fake_curl writes a curl that logs each call's arguments (one per line, then
# a "--" line) to $TMPDIR_TEST/curl.log, appends each --data-binary body to
# curl.body, and answers like the forge for release 42, whose state it keeps
# in $TMPDIR_TEST/forge: "release" holds "draft true" or "draft false" once
# the release exists, "assets" one "<id> <size> <name>" line per attachment.
# FAKE_FAIL_UPLOAD=<name> fails that upload with a 500; FAKE_SHORT_UPLOAD=<name>
# stores it one byte short.
fake_curl() {
    cat >"$TMPDIR_TEST/curl" <<'EOF'
#!/usr/bin/env bash
dir="$(dirname "$0")"
forge="$dir/forge"
log="$dir/curl.log"
out=/dev/stdout url="" method=GET data="" file=""
for a in "$@"; do printf '%s\n' "$a" >>"$log"; done
echo -- >>"$log"
while [ $# -gt 0 ]; do
    case "$1" in
        -o) out="$2"; shift 2 ;;
        -X) method="$2"; shift 2 ;;
        --data-binary) data="$2"; printf '%s\n' "$2" >>"$dir/curl.body"; shift 2 ;;
        -F) file="${2#attachment=@}"; shift 2 ;;
        -w|-H) shift 2 ;;
        -*) shift ;;
        *) url="$1"; shift ;;
    esac
done
touch "$forge/assets"
release_json() { printf '{"id":42,"tag_name":"x","%s":%s,"author":{"id":7}}' $(cat "$forge/release"); }
case "$method $url" in
    "GET "*/releases/tags/*)
        if [ -f "$forge/release" ]; then release_json >"$out"; printf 200; else printf 404; fi ;;
    "POST "*/releases)
        case "$data" in *'"draft":true'*) echo "draft true" ;; *) echo "draft false" ;; esac >"$forge/release"
        release_json >"$out"; printf 201 ;;
    "POST "*/releases/42/assets*)
        name="${url##*name=}"
        if [ "$name" = "${FAKE_FAIL_UPLOAD:-}" ]; then printf 'boom' >"$out"; printf 500; exit 0; fi
        size="$(stat -c %s "$file")"
        [ "$name" != "${FAKE_SHORT_UPLOAD:-}" ] || size=$((size - 1))
        n=$(($(cat "$forge/next" 2>/dev/null || echo 100) + 1))
        echo "$n" >"$forge/next"
        echo "$n $size $name" >>"$forge/assets"
        printf '{"id":%s}' "$n" >"$out"; printf 201 ;;
    "GET "*/releases/42/assets)
        awk 'BEGIN { printf "[" } { printf "%s{\"id\":%s,\"name\":\"%s\",\"size\":%s,\"uuid\":\"u\"}", (NR > 1 ? "," : ""), $1, $3, $2 } END { printf "]" }' "$forge/assets" >"$out"
        printf 200 ;;
    "DELETE "*/releases/42/assets/*)
        aid="${url##*/}"
        awk -v aid="$aid" '$1 != aid' "$forge/assets" >"$forge/assets.new" && mv "$forge/assets.new" "$forge/assets"
        : >"$out"; printf 204 ;;
    "PATCH "*/releases/42)
        case "$data" in *'"draft":false'*) echo "draft false" >"$forge/release" ;; esac
        release_json >"$out"; printf 200 ;;
    *) printf 'unexpected' >"$out"; printf 500 ;;
esac
EOF
    chmod +x "$TMPDIR_TEST/curl"
    rm -rf "$TMPDIR_TEST/curl.log" "$TMPDIR_TEST/curl.body" "$TMPDIR_TEST/forge"
    mkdir "$TMPDIR_TEST/forge"
}

# forge_assets prints the fake forge's attachments as "<size> <name>" lines.
forge_assets() {
    awk '{ print $2, $3 }' "$TMPDIR_TEST/forge/assets"
}

# publish runs release-publish.sh in $REPO with the fake forge.
publish() {
    (cd "$REPO" && env TAG="$1" REPO_API=https://forge.example/api/v1/repos/o/go-netmon \
        TOKEN=s3cret CURL="$TMPDIR_TEST/curl" "${@:2}" "$SCRIPTS/release-publish.sh")
}

published_repo() {
    new_repo
    expect_success "release $1" release "$2" "$1" || return 1
    mkdir -p "$REPO/bin"
    echo poller >"$REPO/bin/go-netmon"
    echo cli >"$REPO/bin/netmon"
    fake_curl
}

test_publish_marks_a_candidate_as_prerelease() {
    published_repo v0.1.4-rc.1 candidate || return 1
    expect_success "publish" publish v0.1.4-rc.1 || return 1
    expect_output "published" "published v0.1.4-rc.1 (prerelease=true)" || return 1
    local body
    body="$(cat "$TMPDIR_TEST/curl.body")"
    OUT="$body"
    expect_output "tag" '"tag_name":"v0.1.4-rc.1"' || return 1
    expect_output "prerelease" '"prerelease":true' || return 1
    expect_output "notes as body" '"body":"### Added\n\n- release targets"' || return 1
    run cat "$TMPDIR_TEST/curl.log"
    expect_output "binary attached" "assets?name=netmon" || return 1
    expect_output "checksums attached" "assets?name=SHA256SUMS" || return 1
    case "$OUT" in
        *s3cret*) printf '    FAIL: the token reached curl'"'"'s argv\n' >&2; return 1 ;;
    esac
    expect_equal "published, not a draft" "draft false" "$(cat "$TMPDIR_TEST/forge/release")"
}

test_publish_marks_a_production_release_as_final() {
    published_repo v0.1.4 release || return 1
    expect_success "publish" publish v0.1.4 || return 1
    OUT="$(cat "$TMPDIR_TEST/curl.body")"
    expect_output "not a prerelease" '"prerelease":false'
}

# The release is a draft until every asset is attached and checked: a run
# that fails part way publishes nothing, and the next one takes the draft
# over and finishes it.
test_publish_finishes_an_interrupted_draft() {
    published_repo v0.1.4-rc.1 candidate || return 1
    expect_failure "upload fails" publish v0.1.4-rc.1 FAKE_FAIL_UPLOAD=netmon || return 1
    expect_output "upload fails" "HTTP 500" || return 1
    expect_equal "still a draft" "draft true" "$(cat "$TMPDIR_TEST/forge/release")" || return 1
    OUT="$(cat "$TMPDIR_TEST/curl.body")"
    expect_output "created as a draft" '"draft":true' || return 1
    case "$OUT" in
        *'"draft":false'*) printf '    FAIL: an incomplete release was published\n' >&2; return 1 ;;
    esac

    expect_success "rerun" publish v0.1.4-rc.1 || return 1
    expect_output "takes the draft over" "taking over the draft release 42" || return 1
    expect_output "rerun publishes" "published v0.1.4-rc.1 (prerelease=true)" || return 1
    expect_equal "published" "draft false" "$(cat "$TMPDIR_TEST/forge/release")" || return 1
    expect_equal "each file attached once, whole" \
        "$(cd "$REPO/bin" && find . -type f -printf '%s %f\n' | sort -k2)" "$(forge_assets | sort -k2)" || return 1
    expect_equal "one release created" "1" "$(grep -c '/releases$' "$TMPDIR_TEST/curl.log")"
}

test_publish_keeps_a_draft_whose_assets_do_not_match() {
    published_repo v0.1.4-alpha alpha || return 1
    expect_failure "short asset" publish v0.1.4-alpha FAKE_SHORT_UPLOAD=go-netmon || return 1
    expect_output "short asset" "does not hold exactly the built files" || return 1
    expect_equal "still a draft" "draft true" "$(cat "$TMPDIR_TEST/forge/release")"
}

test_publish_never_edits_a_published_release() {
    published_repo v0.1.4-alpha alpha || return 1
    echo "draft false" >"$TMPDIR_TEST/forge/release"
    expect_failure "existing release" publish v0.1.4-alpha || return 1
    expect_output "existing release" "already published" || return 1
    expect_equal "nothing written" "" "$(cat "$TMPDIR_TEST/curl.body" 2>/dev/null)" || return 1
    expect_equal "nothing attached" "" "$(forge_assets)"
}

test_publish_refuses_a_blank_token() {
    published_repo v0.1.4-alpha alpha || return 1
    expect_failure "blank token" publish v0.1.4-alpha TOKEN= || return 1
    expect_output "blank token" "TOKEN is not set"
}

# --- release.yml --------------------------------------------------------------

# workflow_job_runs FILE JOB prints the one-line `run:` commands of JOB.
workflow_job_runs() {
    awk -v job="  $2:" '
        $0 == job { in_job = 1; next }
        in_job && /^  [^ ]/ { exit }
        in_job && /^ *(- )?run: / { sub(/^ *(- )?run: /, ""); print }
    ' "$1"
}

# workflow_needs FILE JOB prints JOB's needs, one per line.
workflow_needs() {
    awk -v job="  $2:" '
        $0 == job { in_job = 1; next }
        in_job && /^  [^ ]/ { exit }
        in_job && /^    needs: \[/ {
            sub(/^    needs: \[/, ""); sub(/\].*/, ""); gsub(/ /, "")
            n = split($0, a, ","); for (i = 1; i <= n; i++) print a[i]
        }
    ' "$1"
}

# release_gate_matches_ci CI_YML RELEASE_YML: publish needs verify and every
# job CI Passed needs, and each of those runs exactly the commands it runs in
# ci.yml.
release_gate_matches_ci() {
    local ci="$1" rel="$2" job needs want
    local -a gate
    mapfile -t gate < <(workflow_needs "$ci" ci-passed)
    [ "${#gate[@]}" -gt 0 ] || { printf '    ci.yml: CI Passed needs no job this test can read\n' >&2; return 1; }
    needs="$(workflow_needs "$rel" publish)"
    for job in verify "${gate[@]}"; do
        case $'\n'"$needs"$'\n' in
            *$'\n'"$job"$'\n'*) ;;
            *) printf '    release.yml: publish does not need %s\n' "$job" >&2; return 1 ;;
        esac
    done
    for job in "${gate[@]}"; do
        want="$(workflow_job_runs "$ci" "$job")"
        [ -n "$want" ] || { printf '    ci.yml: job %s runs nothing this test can read\n' "$job" >&2; return 1; }
        expect_equal "release.yml job $job runs what ci.yml's does" "$want" "$(workflow_job_runs "$rel" "$job")" || return 1
    done
}

# A release is published only after everything CI gates main on passed on
# the tagged commit.
test_release_workflow_gates_on_everything_ci_gates_on() {
    local ci="$REPO_DIR/.github/workflows/ci.yml" rel="$REPO_DIR/.github/workflows/release.yml"
    expect_success "release.yml gate" release_gate_matches_ci "$ci" "$rel" || return 1

    # The check itself catches a dropped command and a dropped need.
    awk '!/run: make security$/' "$rel" >"$TMPDIR_TEST/no-security.yml"
    expect_failure "a gate job without make security" release_gate_matches_ci "$ci" "$TMPDIR_TEST/no-security.yml" || return 1
    expect_output "a gate job without make security" "job security" || return 1
    awk '{ sub(/needs: \[verify, lint, /, "needs: [verify, ") } { print }' "$rel" >"$TMPDIR_TEST/no-lint.yml"
    expect_failure "publish without lint" release_gate_matches_ci "$ci" "$TMPDIR_TEST/no-lint.yml" || return 1
    expect_output "publish without lint" "publish does not need lint"
}

# --- Makefile entry points --------------------------------------------------

# A value is data: make must not expand it and the shell must not run it.
test_make_entry_points_treat_values_as_data() {
    local marker="$TMPDIR_TEST/pwned" target var
    while read -r target var; do
        rm -f "$marker"
        run make -C "$REPO_DIR" --no-print-directory "$target" "$var=\$(shell touch $marker)" || true
        [ ! -e "$marker" ] || { printf '    FAIL: make %s %s expanded its value\n' "$target" "$var" >&2; return 1; }
        run make -C "$REPO_DIR" --no-print-directory "$target" "$var=v0.1.4\`touch $marker\`" || true
        [ ! -e "$marker" ] || { printf '    FAIL: make %s %s ran its value\n' "$target" "$var" >&2; return 1; }
    done <<'EOF'
release VERSION
release-candidate VERSION
release-alpha VERSION
release-notes TAG
release-verify TAG
EOF
}

# --- runner ---------------------------------------------------------------

main() {
    local filter="${1:-}"

    TMPDIR_TEST="$(mktemp -d)"
    trap 'rm -rf "$TMPDIR_TEST"' EXIT INT TERM

    # Scratch repositories get a known identity and no signing, whatever the
    # host's git configuration says.
    export GIT_CONFIG_NOSYSTEM=1
    export GIT_CONFIG_GLOBAL="$TMPDIR_TEST/gitconfig"
    git config --file "$GIT_CONFIG_GLOBAL" user.name "release tests"
    git config --file "$GIT_CONFIG_GLOBAL" user.email "release-tests@example.invalid"
    git config --file "$GIT_CONFIG_GLOBAL" init.defaultBranch main
    git config --file "$GIT_CONFIG_GLOBAL" commit.gpgsign false
    git config --file "$GIT_CONFIG_GLOBAL" tag.gpgSign false
    # The targets' own VERSION, DRY_RUN or RELEASE_* in the caller's
    # environment would change what is tested.
    unset VERSION DRY_RUN TAG RELEASE_VERSION RELEASE_REMOTE RELEASE_GIT

    local tests
    mapfile -t tests < <(declare -F | awk '{print $3}' | grep '^test_' | sort)

    for t in "${tests[@]}"; do
        if [ -n "$filter" ] && [[ "$t" != *"$filter"* ]]; then
            continue
        fi
        printf '  %s\n' "$t"
        if ( "$t" ); then
            PASS=$((PASS + 1))
        else
            FAIL=$((FAIL + 1))
            FAILED_TESTS+=("$t")
        fi
    done

    printf '\nrelease scripts: %d passed, %d failed\n' "$PASS" "$FAIL"
    if [ "$FAIL" -gt 0 ]; then
        printf 'failed:\n'
        printf '  %s\n' "${FAILED_TESTS[@]}"
        return 1
    fi
}

main "$@"
