#!/usr/bin/env bash
# Shared safety for every entry point that drives docker-compose.test.yml; sourced. Ownership comes from scripts/lib/compose-ownership.sh.

# shellcheck source=scripts/lib/compose-ownership.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/../../scripts/lib" && pwd)/compose-ownership.sh"

# integration_directory_project prints the project name compose would infer from the working directory.
integration_directory_project() {
    basename "$(pwd)" | tr '[:upper:]' '[:lower:]' | tr -cd 'a-z0-9_-'
}

# integration_manual_project prints the manual stack's project, shared by stack.sh and clean.sh; an empty override stays empty.
integration_manual_project() {
    printf '%s' "${NETMON_INTEGRATION_PROJECT-go-netmon-integration-manual}"
}

# integration_require_disposable_project <project> <compose-file>: integration_require_safe_project plus refusing the manual project.
integration_require_disposable_project() {
    local project="$1" compose_file="$2"

    local manual
    manual="$(integration_manual_project)"
    # Check non-empty first so an empty override reaches rule 1's message.
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

# integration_require_safe_project <project> <compose-file>: refuse an empty project, the directory default, or one with foreign containers.
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

    # Rule 3 is compose_foreign_containers; exit 2 means docker failed and is a refusal.
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
