#!/usr/bin/env bash
# Usage: scripts/release-notes.sh <vX.Y.Z[-alpha|-rc.N]> -- prints that version's CHANGELOG section; fails if missing, duplicated or empty.

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
