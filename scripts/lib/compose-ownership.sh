#!/usr/bin/env bash
# Compose-project ownership check shared by scripts/lib/netmon-stack.sh and test/integration/compose-guard.sh; sourced, never executed.

# compose_foreign_containers <docker> <project> <compose-file> prints containers in project not created from compose-file; exit 2 means docker failed.
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

    # An array, so paths with spaces print intact.
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
