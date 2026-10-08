#!/usr/bin/env bash
# Tests for the golangci-lint pin guards in the Makefile: the "lint" and
# "install-tools" targets.
#
# Those targets exist to make a lint run at the wrong version impossible, so
# the guards themselves have to be exercised rather than assumed -- an
# unverified fail-closed check is indistinguishable from a fail-open one until
# the day it matters. Every case below runs against fake binaries in $TMPDIR
# and an installer URL that never reaches the network, so nothing here touches
# the shared GOPATH/bin or downloads anything.
#
# Usage: toolchain-tests.sh [test-name-substring]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

# The version the Makefile pins, read from the Makefile so this test cannot
# drift from it: hardcoding it here would mean a pin bump silently stops being
# covered.
WANT_VERSION="$(make -C "$REPO_DIR" --no-print-directory print-golangci-version)"

PASS=0
FAIL=0
FAILED_TESTS=()

OUT=""

# run_make invokes a target with the fake toolchain wired in, capturing
# combined output into $OUT and returning the target's exit status.
run_make() {
    local target="$1"
    shift
    set +e
    OUT="$(make -C "$REPO_DIR" --no-print-directory "$target" "$@" 2>&1)"
    local status=$?
    set -e
    return $status
}

expect_success() {
    local desc="$1"
    shift
    if ! run_make "$@"; then
        printf '    FAIL: %s: expected success, got failure\n%s\n' "$desc" "$OUT" >&2
        return 1
    fi
}

expect_failure() {
    local desc="$1"
    shift
    if run_make "$@"; then
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

# fake_golangci writes a stub binary that prints a version banner and exits
# with the given status. Both halves matter: a binary can report a plausible
# version and still be broken.
fake_golangci() {
    local path="$1" version="$2" exit_status="$3"
    cat >"$path" <<EOF
#!/bin/sh
if [ "\$1" = "version" ]; then
    echo "golangci-lint has version $version built with go1.27.1"
    exit $exit_status
fi
exit 0
EOF
    chmod +x "$path"
}

# fake_installer writes a stand-in for the upstream install.sh that plants a
# binary reporting the given version, so "the download worked but delivered the
# wrong build" is testable without the network.
fake_installer() {
    local path="$1" version="$2" exit_status="${3:-0}"
    cat >"$path" <<EOF
#!/bin/sh
while [ \$# -gt 0 ]; do
    case "\$1" in
        -b) dir="\$2"; shift 2 ;;
        *) shift ;;
    esac
done
EOF
    {
        # shellcheck disable=SC2016  # writing a script; $dir expands when IT runs, not here
        printf 'cat > "$dir/golangci-lint" <<INNER\n'
        printf '#!/bin/sh\n'
        printf 'echo "golangci-lint has version %s built with go1.27.1"\n' "$version"
        printf 'exit %s\n' "$exit_status"
        printf 'INNER\n'
        # shellcheck disable=SC2016  # same: $dir belongs to the generated script
        printf 'chmod +x "$dir/golangci-lint"\n'
    } >>"$path"
    chmod +x "$path"
}

# --- lint guard -----------------------------------------------------------

test_lint_accepts_the_pinned_version() {
    local dir="$TMPDIR_TEST/accept"
    mkdir -p "$dir"
    fake_golangci "$dir/golangci-lint" "$WANT_VERSION" 0
    expect_success "pinned version runs" lint "GOLANGCI_LINT=$dir/golangci-lint"
}

test_lint_refuses_a_mismatched_version() {
    local dir="$TMPDIR_TEST/mismatch"
    mkdir -p "$dir"
    fake_golangci "$dir/golangci-lint" "2.0.0" 0
    expect_failure "mismatched version refused" lint "GOLANGCI_LINT=$dir/golangci-lint" || return 1
    expect_output "mismatched version names both" "but this repository pins $WANT_VERSION"
}

# The regression this file was added for: the version command's exit status was
# lost to the awk pipeline, so a binary printing the right banner and then
# failing was accepted as the pinned tool.
test_lint_refuses_a_binary_that_reports_the_pin_then_fails() {
    local dir="$TMPDIR_TEST/rightbanner-badexit"
    mkdir -p "$dir"
    fake_golangci "$dir/golangci-lint" "$WANT_VERSION" 1
    expect_failure "banner ok but nonzero exit refused" lint "GOLANGCI_LINT=$dir/golangci-lint" || return 1
    expect_output "nonzero exit reported as unidentified" "Refusing to lint with an unidentified binary"
}

test_lint_refuses_an_unreadable_version() {
    local dir="$TMPDIR_TEST/silent"
    mkdir -p "$dir"
    printf '#!/bin/sh\nexit 0\n' >"$dir/golangci-lint"
    chmod +x "$dir/golangci-lint"
    expect_failure "silent binary refused" lint "GOLANGCI_LINT=$dir/golangci-lint" || return 1
    expect_output "silent binary reported as unidentified" "Refusing to lint with an unidentified binary"
}

test_lint_refuses_a_missing_binary() {
    expect_failure "missing binary refused" lint "GOLANGCI_LINT=$TMPDIR_TEST/does-not-exist" || return 1
    expect_output "missing binary points at install-tools" "Run: make install-tools"
}

# --- install-tools guard --------------------------------------------------

test_install_skips_when_the_pin_is_present() {
    local dir="$TMPDIR_TEST/present"
    mkdir -p "$dir"
    fake_golangci "$dir/golangci-lint" "$WANT_VERSION" 0
    expect_success "pin present installs nothing" install-tools \
        "GOBIN_DIR=$dir" "GOLANGCI_LINT_INSTALLER_URL=file:///does-not-exist" || return 1
    expect_output "pin present says so" "already installed"
}

# A failed download must not report success. curl piped into sh reported sh's
# status, and sh given empty stdin exits 0, so this printed "Tools installed"
# with nothing installed.
test_install_fails_when_the_download_fails() {
    local dir="$TMPDIR_TEST/nodownload"
    mkdir -p "$dir"
    expect_failure "failed download refused" install-tools \
        "GOBIN_DIR=$dir" "GOLANGCI_LINT_INSTALLER_URL=file:///nonexistent-installer.sh" || return 1
    expect_output "failed download says so" "Could not download the golangci-lint installer"
    if [ -e "$dir/golangci-lint" ]; then
        printf '    FAIL: a failed download left a binary behind\n' >&2
        return 1
    fi
}

test_install_fails_when_the_installer_delivers_the_wrong_version() {
    local dir="$TMPDIR_TEST/wrongversion"
    mkdir -p "$dir"
    fake_installer "$TMPDIR_TEST/installer.sh" "2.0.0"
    expect_failure "wrong delivered version refused" install-tools \
        "GOBIN_DIR=$dir" "GOLANGCI_LINT_INSTALLER_URL=file://$TMPDIR_TEST/installer.sh" || return 1
    expect_output "wrong delivered version names both" "reports '2.0.0', want '$WANT_VERSION'"
}

# The install-path half of the exit-status regression: a delivered binary can
# report the right version and still be broken, and "it printed the pin" is not
# the same claim as "it works".
test_install_fails_when_the_delivered_binary_reports_the_pin_then_fails() {
    local dir="$TMPDIR_TEST/deliveredbadexit"
    mkdir -p "$dir"
    fake_installer "$TMPDIR_TEST/badexit-installer.sh" "$WANT_VERSION" 1
    expect_failure "delivered binary with nonzero exit refused" install-tools \
        "GOBIN_DIR=$dir" "GOLANGCI_LINT_INSTALLER_URL=file://$TMPDIR_TEST/badexit-installer.sh" || return 1
    expect_output "delivered binary with nonzero exit says so" "want '$WANT_VERSION'"
}

test_install_succeeds_when_the_installer_delivers_the_pin() {
    local dir="$TMPDIR_TEST/rightversion"
    mkdir -p "$dir"
    fake_installer "$TMPDIR_TEST/good-installer.sh" "$WANT_VERSION"
    expect_success "correct delivered version accepted" install-tools \
        "GOBIN_DIR=$dir" "GOLANGCI_LINT_INSTALLER_URL=file://$TMPDIR_TEST/good-installer.sh" || return 1
    expect_output "correct delivered version confirmed" "Installed golangci-lint $WANT_VERSION"
}

# --- runner ---------------------------------------------------------------

main() {
    local filter="${1:-}"

    TMPDIR_TEST="$(mktemp -d)"
    trap 'rm -rf "$TMPDIR_TEST"' EXIT INT TERM

    local tests
    mapfile -t tests < <(declare -F | awk '{print $3}' | grep '^test_' | sort)

    for t in "${tests[@]}"; do
        if [ -n "$filter" ] && [[ "$t" != *"$filter"* ]]; then
            continue
        fi
        printf '  %s\n' "$t"
        if "$t"; then
            PASS=$((PASS + 1))
        else
            FAIL=$((FAIL + 1))
            FAILED_TESTS+=("$t")
        fi
    done

    printf '\ntoolchain guards: %d passed, %d failed\n' "$PASS" "$FAIL"
    if [ "$FAIL" -gt 0 ]; then
        printf 'failed:\n'
        printf '  %s\n' "${FAILED_TESTS[@]}"
        return 1
    fi
}

main "$@"
