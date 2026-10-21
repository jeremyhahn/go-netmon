#!/usr/bin/env bash
# Publish a tag as a release on the Gitea forge, with the built binaries.
#
# Usage: scripts/release-publish.sh   (from the repository root)
#
# Environment (release.yml sets it; `make release-publish` passes it through):
#   TAG        the tag, vX.Y.Z, vX.Y.Z-alpha or vX.Y.Z-rc.N
#   REPO_API   <server>/api/v1/repos/<owner>/<repo>
#   TOKEN      a token allowed to create releases (the project agent's)
#   ASSET_DIR  the directory of files to attach (default bin, `make build-all`)
#   CURL       the curl to run (default curl; the tests substitute a fake)
#
# The release body is the tag's CHANGELOG.md section (release-notes.sh). An
# -alpha or -rc.N tag is published as a pre-release. A SHA256SUMS of the
# assets is written and attached with them.
#
# The release is created as a draft, which nobody but the agent sees; every
# asset is uploaded, the draft's asset list is checked against the files
# (name and size), and only then is the draft published. A run interrupted
# before that leaves a draft, never a published release with missing assets,
# and the next run takes that draft over: it deletes whatever assets the draft
# holds and attaches the set again. A published release for the tag is
# refused, never edited: what was published stays published.

set -euo pipefail

# shellcheck source=lib/release.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/release.sh"
script_dir="$(dirname "${BASH_SOURCE[0]}")"

[ $# -eq 0 ] || die "usage: $0 (TAG, REPO_API and TOKEN come from the environment)"
tag="${TAG:-}"
repo_api="${REPO_API:-}"
token="${TOKEN:-}"
asset_dir="${ASSET_DIR:-bin}"
curl="${CURL:-curl}"

[[ "$tag" =~ $RELEASE_TAG_RE ]] || die "TAG '$tag' is not vX.Y.Z, vX.Y.Z-alpha or vX.Y.Z-rc.N"
[[ "$repo_api" =~ ^https?://[^[:space:]]+/api/v1/repos/[^/[:space:]]+/[^/[:space:]]+$ ]] ||
    die "REPO_API '$repo_api' is not <server>/api/v1/repos/<owner>/<repo>"
# A blank token makes the forge answer as an anonymous caller, which reads as
# a permissions bug; it never falls back to another one.
[ -n "$token" ] || die "TOKEN is not set; the release is created as the project agent"
[ -d "$asset_dir" ] || die "ASSET_DIR '$asset_dir' does not exist; run make build-all first"

notes="$("$script_dir/release-notes.sh" "$tag")"
prerelease=false
[ "$(release_kind_of "$tag")" = release ] || prerelease=true

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
# The token reaches curl through a file, never its argv, where ps shows it.
printf 'Authorization: token %s\n' "$token" >"$work/auth"
chmod 600 "$work/auth"

# json_string prints its argument as a JSON string literal.
json_string() {
    printf '%s' "$1" | awk '
        BEGIN { RS = "\001"; ORS = "" }
        {
            gsub(/\\/, "\\\\"); gsub(/"/, "\\\""); gsub(/\t/, "\\t"); gsub(/\r/, "\\r"); gsub(/\n/, "\\n")
            print "\"" $0 "\""
        }
        END { if (NR == 0) print "\"\"" }
    '
}

# api METHOD PATH [curl args...] prints the response body and fails on any
# status outside 2xx.
api() {
    local method="$1" path="$2"
    shift 2
    local code
    code="$("$curl" -sS -o "$work/response" -w '%{http_code}' -X "$method" -H "@$work/auth" "$@" "$repo_api$path")" ||
        die "$method $path: curl failed"
    case "$code" in
        2??) cat "$work/response" ;;
        *) die "$method $path: HTTP $code: $(head -c 500 "$work/response")" ;;
    esac
}

# json_bool NAME prints the first boolean field NAME of the JSON on stdin.
json_bool() {
    awk -v key="\"$1\":" '{ i = index($0, key); if (i) { v = substr($0, i + length(key)); sub(/[^a-z].*/, "", v); print v; exit } }'
}

# release_id prints the first "id" of the JSON on stdin: a release's own id
# comes before those of its author and assets.
release_id() {
    awk 'match($0, /"id":[0-9]+/) { print substr($0, RSTART + 5, RLENGTH - 5); exit }'
}

# asset_list prints "<id> <size> <name>" for each attachment of the release.
# An attachment carries no nested object, so each one is the text after a "{".
asset_list() {
    api GET "/releases/$id/assets" | awk '
        BEGIN { RS = "{" }
        match($0, /"id":[0-9]+/) {
            aid = substr($0, RSTART + 5, RLENGTH - 5)
            size = ""; name = ""
            if (match($0, /"size":[0-9]+/)) size = substr($0, RSTART + 7, RLENGTH - 7)
            if (match($0, /"name":"[^"]*"/)) name = substr($0, RSTART + 8, RLENGTH - 9)
            print aid, size, name
        }'
}

code="$("$curl" -sS -o "$work/existing" -w '%{http_code}' -H "@$work/auth" "$repo_api/releases/tags/$tag")" ||
    die "could not ask the forge whether $tag is released"
id=""
case "$code" in
    404) ;;
    200)
        [ "$(json_bool draft <"$work/existing")" = true ] ||
            die "a release for $tag is already published; a published release is never edited"
        id="$(release_id <"$work/existing")"
        [ -n "$id" ] || die "the forge holds a draft release for $tag but its answer carries no id"
        echo "release: taking over the draft release $id that an earlier run left for $tag"
        ;;
    *) die "GET /releases/tags/$tag: HTTP $code" ;;
esac

(cd "$asset_dir" && find . -maxdepth 1 -type f ! -name SHA256SUMS -printf '%f\n' | sort | xargs -r sha256sum) >"$work/SHA256SUMS"
[ -s "$work/SHA256SUMS" ] || die "ASSET_DIR '$asset_dir' holds no file to attach"
cp "$work/SHA256SUMS" "$asset_dir/SHA256SUMS"

# The release as it is published; the draft carries the same fields.
fields="$(printf '"name":%s,"body":%s,"prerelease":%s' \
    "$(json_string "go-netmon $tag")" "$(json_string "$notes")" "$prerelease")"

if [ -z "$id" ]; then
    created="$(api POST /releases -H 'Content-Type: application/json' \
        --data-binary "{\"tag_name\":$(json_string "$tag"),$fields,\"draft\":true}")"
    id="$(printf '%s' "$created" | release_id)"
    [ -n "$id" ] || die "the forge created a draft release for $tag but its answer carries no id"
else
    # What an interrupted run attached is not trusted: a file may be partial
    # or from an earlier build.
    asset_list >"$work/stale"
    while read -r aid _; do
        api DELETE "/releases/$id/assets/$aid" >/dev/null
    done <"$work/stale"
fi

(cd "$asset_dir" && find . -maxdepth 1 -type f -printf '%s %f\n' | sort -k2) >"$work/want"
while read -r _ name; do
    api POST "/releases/$id/assets?name=$name" -F "attachment=@$asset_dir/$name" >/dev/null
    echo "release: attached $name"
done <"$work/want"

# The draft holds exactly the files, each at its size, or it stays a draft.
asset_list >"$work/attached"
awk '{ print $2, $3 }' "$work/attached" | sort -k2 >"$work/got"
cmp -s "$work/want" "$work/got" ||
    die "the draft release $id for $tag does not hold exactly the built files (want: $(tr '\n' ' ' <"$work/want"); got: $(tr '\n' ' ' <"$work/got")); it stays a draft, and a rerun takes it over"

api PATCH "/releases/$id" -H 'Content-Type: application/json' --data-binary "{$fields,\"draft\":false}" >/dev/null
echo "release: published $tag (prerelease=$prerelease)"
