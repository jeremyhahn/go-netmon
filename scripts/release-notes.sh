#!/usr/bin/env bash
# Print a release's CHANGELOG section, the body of its forge release.
#
# Usage: scripts/release-notes.sh <vX.Y.Z[-alpha|-rc.N]>   (from the repository root)
#
# Prints everything after the "## [X.Y.Z] - <date>" heading up to the next
# "## " heading, without leading or trailing blank lines. The heading is
# matched up to its closing bracket, so a version is never matched by a longer
# one that starts with it (0.1.4 is not 0.1.40, 0.1.4-rc.1 is not 0.1.4-rc.10).
# Fails when the section is missing, appears twice, or is empty.

set -euo pipefail

# shellcheck source=lib/release.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/release.sh"

[ $# -eq 1 ] || die "usage: $0 <vX.Y.Z[-alpha|-rc.N]>"
tag="$1"
[[ "$tag" =~ $RELEASE_TAG_RE ]] || die "'$tag' is not vX.Y.Z, vX.Y.Z-alpha or vX.Y.Z-rc.N"
[ -f CHANGELOG.md ] || die "CHANGELOG.md does not exist"

awk -v want="## [${tag#v}]" '
    /^## / {
        in_s = (index($0, want) == 1)
        if (in_s) sections++
        next
    }
    in_s {
        if (NF) { for (; blanks > 0; blanks--) if (printed) print ""; print; printed = 1 }
        else blanks++
    }
    END {
        if (sections == 0) { print "release: CHANGELOG.md has no section for " want > "/dev/stderr"; exit 1 }
        if (sections > 1) { print "release: CHANGELOG.md has " sections " sections for " want > "/dev/stderr"; exit 1 }
        if (!printed) { print "release: the CHANGELOG.md section for " want " is empty" > "/dev/stderr"; exit 1 }
    }
' CHANGELOG.md
