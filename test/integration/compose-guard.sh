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

    # Ownership is established in two steps -- list the project's containers,
    # then ask each one which compose file made it -- rather than with a single
    # negated-label filter, because docker has no such filter. Measured against
    # the daemon this runs on:
    #
    #   --filter "label=KEY!=VALUE"   parses KEY's value as the literal "!=VALUE",
    #                                 matches nothing, and reports a clean bill
    #                                 of health for a project full of foreign
    #                                 containers -- a silent fail-open;
    #   --filter "label!=KEY=VALUE"   is rejected outright: "invalid filter 'label!'".
    #
    # So the comparison is done here, where it can be read and tested, and both
    # docker calls capture their exit status BEFORE their output is judged.
    # A query that fails refuses; it never counts as "nothing foreign".
    local ids status
    ids="$(docker ps -aq --filter "label=com.docker.compose.project=$project" 2>&1)" || status=$?
    if [ -n "${status-}" ]; then
        echo "compose-guard: could not list the containers in project '$project'." >&2
        echo "  docker ps failed (exit $status): $ids" >&2
        echo "  Refusing to act on a project whose ownership could not be checked." >&2
        return 1
    fi

    local id owner foreign=""
    for id in $ids; do
        unset status
        owner="$(docker inspect \
            --format '{{index .Config.Labels "com.docker.compose.project.config_files"}}' \
            "$id" 2>&1)" || status=$?
        if [ -n "${status-}" ]; then
            echo "compose-guard: could not read the compose file behind container $id." >&2
            echo "  docker inspect failed (exit $status): $owner" >&2
            echo "  Refusing to act on a project whose ownership could not be checked." >&2
            return 1
        fi
        if [ "$owner" != "$compose_file" ]; then
            foreign="$foreign $id ($owner)"
        fi
    done

    if [ -n "$foreign" ]; then
        echo "Containers in project '$project' that were not created from $compose_file:" >&2
        for id in $foreign; do echo "  $id" >&2; done
        echo "compose-guard: refusing to run: tearing this project down would remove" >&2
        echo "  containers it does not own." >&2
        return 1
    fi
}
