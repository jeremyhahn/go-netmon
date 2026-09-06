#!/usr/bin/env bash
# Shared safety for every entry point that drives docker-compose.test.yml.
#
# This library is sourced, never executed. Callers are responsible for
# "set -euo pipefail".
#
# WHY THIS IS A LIBRARY
#
# run-tests.sh has carried a careful SAFETY section for a long time: pin the
# compose project, because the name compose infers from this directory is
# "go-netmon" — the project a DEPLOYED stack occupies — and "down -v" against
# that deletes the MIB catalog, the device configuration and the metric
# history.
#
# The four "make integration-*" targets drove the same compose file with no -p
# at all, so every one of them addressed the deployment, and "make
# integration-down" was "down -v" against it. The guard existed; it just lived
# at one call site out of five.
#
# So it lives here now, and every entry point calls it. A safety property that
# has to be remembered separately at each call site is not a safety property.
#
# WHY THE OWNERSHIP CHECK IS SOURCED RATHER THAN REIMPLEMENTED (#44)
#
# This file used to carry its own copy of the ownership enumeration. The
# argument for keeping it was real and is worth stating before it is dismissed:
# if the harness sources the library it guards against, a bug in that library is
# present on BOTH sides of every test that exercises the guard -- the check and
# the thing checking it fail together -- so an independent second implementation
# is one of the few ways a test can catch a library bug rather than inherit it.
#
# It does not hold here, and it is measurable rather than arguable. Inject a
# fail-open defect into compose_foreign_containers, then run both suites:
#
#   defect in scripts/lib/compose-ownership.sh   backup 59/2 FAILED
#                                                guard  29/0 passed
#   defect in this file's own former copy        backup 61/0 passed
#                                                guard  27/2 FAILED
#
# The two copies were covered by two DISJOINT suites. The guard suite was
# already blind to a library defect -- it passed 29 of 29 with the library
# failing open completely -- because it never executed the library at all. The
# independence was not protecting anything, because you cannot lose a protection
# you do not have.
#
# The argument would apply if guard-tests.sh used the library as its ORACLE,
# comparing this guard's answer against the library's. It does not: its oracle
# is fixtures with expected refusals written out by hand. So sourcing the
# library does not contaminate the yardstick -- it just means a library defect
# now turns BOTH suites red instead of one.
#
# Recorded as a project-agent decision, not an owner ruling. The reasoning is
# above so the next reader can check it rather than take it; if the oracle in
# guard-tests.sh ever becomes the library itself, this decision is wrong and
# should be revisited.

# shellcheck source=scripts/lib/compose-ownership.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/../../scripts/lib" && pwd)/compose-ownership.sh"

# integration_directory_project prints the project name compose would infer if
# no -p were given: the working directory's base name, lowercased, with
# everything outside [a-z0-9_-] removed.
#
# It is derived rather than hardcoded to "go-netmon" so that a checkout in a
# differently named directory is still checked against ITS default rather than
# against ours.
integration_directory_project() {
    basename "$(pwd)" | tr '[:upper:]' '[:lower:]' | tr -cd 'a-z0-9_-'
}

# integration_manual_project prints the project the MANUAL stack occupies --
# what "make integration-up/down/logs/shell" create and address.
#
# It lives here rather than in stack.sh because clean.sh needs the same answer
# for the opposite reason: stack.sh uses it to decide what to act on, and
# clean.sh uses it to decide what to leave alone. Two copies of a name where
# one side destroys what the other side keeps is not a constant worth
# duplicating.
#
# "-" rather than ":-", matching stack.sh: an explicitly EMPTY override stays
# empty and is refused by rule 1, instead of being silently replaced.
integration_manual_project() {
    printf '%s' "${NETMON_INTEGRATION_PROJECT-go-netmon-integration-manual}"
}

# integration_require_disposable_project <project> <compose-file>
#
# For an entry point that CREATES AND DESTROYS a stack of its own -- today,
# run-tests.sh, whose EXIT trap runs "down -v --remove-orphans" over whatever
# project it was given. Everything integration_require_safe_project refuses,
# plus the MANUAL project.
#
# This is a rule, not a knob, and the distinction is the whole reason it lives
# here rather than in a test. run-tests.sh was briefly given an environment
# override so a bad project name could be fed to it; that WIDENED what a caller
# could set, and it promptly let a test run adopt the manual stack's name and
# delete it. A refusal NARROWS what a caller can get away with: no environment
# variable under any name, no flag and no future typo can produce the manual
# project at a call site that checks this, because the rule does not care where
# the name came from.
#
# stack.sh deliberately calls the base function instead. It is the one entry
# point entitled to the manual name -- creating and removing that stack is its
# entire job.
integration_require_disposable_project() {
    local project="$1" compose_file="$2"

    local manual
    manual="$(integration_manual_project)"
    # -n "$project" first: an EXPLICITLY EMPTY override makes
    # integration_manual_project empty too, so a bare "" would match it and be
    # refused as "that is the manual stack" when the operator in fact set
    # nothing. The empty case is one this codebase deliberately preserves --
    # the "-" rather than ":-" exists for it -- so it is the one case where
    # rule 1's message is load-bearing, and it must reach rule 1 to get it.
    if [ -n "$project" ] && [ "$project" = "$manual" ]; then
        echo "compose-guard: refusing to use compose project '$project'." >&2
        echo "  That is the MANUAL stack: what 'make integration-up' creates, and" >&2
        echo "  what someone may be part way through using -- stack.sh's header" >&2
        echo "  notes that two people on one host share it." >&2
        echo "  This entry point tears its project down on the way out, so running" >&2
        echo "  here would delete that stack and its volumes underneath them." >&2
        echo "  Remove the manual stack with 'make integration-down' instead." >&2
        return 1
    fi

    integration_require_safe_project "$project" "$compose_file"
}

# integration_require_safe_project <project> <compose-file>
#
# Refuses to proceed unless the project is safe to create and destroy:
#
#   1. it is set at all — an empty -p makes compose fall back to the directory
#      default, which is the very thing being avoided;
#   2. it is not the directory default, which is a deployed stack's project;
#   3. it holds no containers built from a different compose file, which would
#      mean the name collides with something this file does not own.
#
# Rule 3 is deliberately not sufficient on its own: two runs of THIS file match
# each other and pass it, which is why run-tests.sh also gives every run its own
# project name.
integration_require_safe_project() {
    local project="$1" compose_file="$2"

    if [ -z "$project" ]; then
        echo "compose-guard: refusing to run with an empty compose project name;" >&2
        echo "  compose would fall back to the working directory's name, which is" >&2
        echo "  a deployed stack's project." >&2
        return 1
    fi

    local default
    default="$(integration_directory_project)"
    if [ "$project" = "$default" ]; then
        echo "compose-guard: refusing to use compose project '$project'." >&2
        echo "  That is the name compose infers from this directory, and it is the" >&2
        echo "  project a deployed go-netmon stack occupies. Acting on it would put" >&2
        echo "  the live containers in scope, and 'down -v' would delete their" >&2
        echo "  volumes: the MIB catalog, the device configuration, the metric" >&2
        echo "  history." >&2
        return 1
    fi

    # Rule 3 is compose_foreign_containers, in scripts/lib/compose-ownership.sh
    # -- THE single implementation of this property. This file carried a second
    # one until #44 decided it should not; the header records why, and the
    # library's header carries the same decision from its side.
    #
    # Exit 2 is "the question could not be answered", which is a REFUSAL and is
    # deliberately not the same as an empty answer. Treating an unanswered
    # question as a clean bill of health is one of the two defects that produced
    # #41, and the library's header documents both. The library prints what
    # actually failed -- which docker call, its exit status, its stderr -- and
    # this adds what that means for this caller.
    local foreign status=0
    foreign="$(compose_foreign_containers docker "$project" "$compose_file")" || status=$?
    if [ "$status" -ne 0 ]; then
        echo "compose-guard: refusing to act on a project whose ownership could not be checked." >&2
        return 1
    fi

    if [ -n "$foreign" ]; then
        # read -r line by line rather than "for id in $foreign": the report is
        # the text a human reads to decide whether the refusal was right, and an
        # unquoted expansion puts every path segment on its own line, splits a
        # compose path containing a space, and is subject to globbing.
        echo "Containers in project '$project' that were not created from $compose_file:" >&2
        local id
        while IFS= read -r id; do
            [ -n "$id" ] || continue
            echo "  $id" >&2
        done <<<"$foreign"
        echo "compose-guard: refusing to run: tearing this project down would remove" >&2
        echo "  containers it does not own." >&2
        return 1
    fi
}
