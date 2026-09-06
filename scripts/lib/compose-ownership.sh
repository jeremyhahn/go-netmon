#!/usr/bin/env bash
# THE compose-project ownership check. One implementation, and since #44 both
# of the callers that need it.
#
# This library is sourced, never executed. Callers are responsible for
# "set -euo pipefail".
#
# WHY IT IS ITS OWN FILE
#
# There were two implementations of this property: scripts/lib/netmon-stack.sh
# guarding backup.sh and restore.sh, and test/integration/compose-guard.sh
# guarding the integration harness. The harness copy was corrected; the backup
# copy was not, and kept both of the defects the harness copy documents in its
# own comments. The un-fixed one was the copy standing in front of a path that
# overwrites volumes.
#
# Writing a third implementation would guarantee the same divergence again, so
# this exists to be the single one. It takes everything it needs as arguments
# and keeps no state, which is what would let the deployment tooling and the
# test harness share it without sharing anything else.
#
# IT NOW HAS TWO CALLERS (#44): scripts/lib/netmon-stack.sh, guarding backup.sh
# and restore.sh, and test/integration/compose-guard.sh, guarding the
# integration harness. The decision is recorded in both files.
#
# The argument for leaving the harness its own copy was real and was taken
# seriously: a harness that sources the library it guards against cannot catch a
# bug in that library, because its check and the thing checking it fail
# together. It was MEASURED rather than argued, by injecting a fail-open defect
# into compose_foreign_containers and running both suites:
#
#   defect here                      backup 59/2 FAILED   guard 29/0 passed
#   defect in the harness's copy      backup 61/0 passed   guard 27/2 FAILED
#
# The two copies sat behind two DISJOINT suites. The guard suite passed 29 of 29
# with this library failing open completely, because it never executed this
# library at all -- so the independence was not buying the protection it was
# credited with. You cannot lose a protection you do not have. Sourcing it means
# a defect here now reddens BOTH suites instead of one.
#
# The argument would bite if guard-tests.sh used this library as its ORACLE,
# comparing the guard's answer against this one. It does not: its oracle is
# hand-written fixtures and expected refusals. If that ever stops being true,
# reopen #44 rather than trusting this paragraph.
#
# An earlier version of this comment said "both callers now use this", which was
# not true when written. A header claiming a consolidation that did not happen
# is the same defect as a guard that does not guard, and review caught it. It is
# true now.
#
# THE TWO DEFECTS IT EXISTS TO NOT HAVE
#
# 1. Docker has NO negated label filter. Measured against a real daemon, on a
#    container that IS in the project and was built from a DIFFERENT compose
#    file:
#
#      --filter "label=<key>=<project>" --filter "label=<key2>!=<file>"
#          -> (empty)        the foreign container is not returned
#      --filter "label=<key>=<project>"
#          -> c9f2344d21da   it is really there
#
#    docker parses "<key2>!=<file>" as a key whose value is the literal
#    "!=<file>", which matches nothing, so the check reports a clean bill of
#    health for a project full of foreign containers. "label!=KEY=VALUE" is
#    rejected outright: "invalid filter 'label!'". There is no filter that
#    expresses this, which is why the comparison happens here in shell, where
#    it can be read and tested.
#
# 2. A query that FAILS must not count as "nothing foreign". Both docker calls
#    capture their exit status before their output is judged, and stderr is
#    kept out of the value: a "docker ps" that exits 0 while warning would
#    otherwise put warning words into a list parsed as container ids.

# compose_foreign_containers <docker> <project> <compose-file>
#
# Prints one line per container in <project> that was NOT created from
# <compose-file>, as "<id> (<the file that did create it>)".
#
# Exit status is the answer to a different question from the output:
#
#   0  the question was answered; output empty means the project is ours
#   2  the question could NOT be answered -- docker failed. Callers must treat
#      this as a refusal, never as "nothing foreign".
#
# The two are separate because conflating them is defect 2 above.
compose_foreign_containers() {
    local docker_bin="$1" project="$2" compose_file="$3"
    local ids status=0 err_file

    # If mktemp fails it normally creates nothing, but a nonstandard or
    # interrupted one can print a path and still exit nonzero; remove it rather
    # than leak it.
    err_file="$(mktemp)" || { [ -n "${err_file:-}" ] && rm -f "$err_file"; return 2; }

    ids="$("$docker_bin" ps -aq \
        --filter "label=com.docker.compose.project=$project" 2>"$err_file")" || status=$?
    if [ "$status" -ne 0 ]; then
        echo "could not list the containers in project '$project'." >&2
        echo "  ${docker_bin} ps failed (exit $status): $(cat "$err_file")" >&2
        rm -f "$err_file"
        return 2
    fi

    # An array, because this text is what a human reads to decide whether a
    # refusal was right. Accumulated into a string and re-split by an unquoted
    # "for", a compose path containing a space breaks across lines and a
    # missing label prints a bare "()".
    local id owner
    local -a foreign=()
    for id in $ids; do
        status=0
        owner="$("$docker_bin" inspect \
            --format '{{index .Config.Labels "com.docker.compose.project.config_files"}}' \
            "$id" 2>"$err_file")" || status=$?
        if [ "$status" -ne 0 ]; then
            echo "could not read the compose file behind container $id." >&2
            echo "  ${docker_bin} inspect failed (exit $status): $(cat "$err_file")" >&2
            rm -f "$err_file"
            return 2
        fi
        if [ "$owner" != "$compose_file" ]; then
            foreign+=("$id (${owner:-<no config_files label>})")
        fi
    done

    rm -f "$err_file"

    if [ "${#foreign[@]}" -gt 0 ]; then
        printf '%s\n' "${foreign[@]}"
    fi
    return 0
}
