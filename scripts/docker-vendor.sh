#!/usr/bin/env bash
# Vendors the module's dependencies into .docker-vendor/ for the image builds.
#
# WHY THE MODULES COME FROM THE HOST
#
# go-netmon depends on a private module, github.com/jeremyhahn/go-enclave (#8).
# "go mod download" inside "docker build" cannot fetch it, because the build has
# no credential -- and it must never be given one: a token in a build arg, a
# layer or the image is a token in every copy of that image. The host already
# has the access (GOPRIVATE and git credentials on a runner, the developer's own
# on a workstation), so the modules are vendored here and handed to the build
# as the named build context "go-vendor". The Dockerfiles copy it to vendor/
# and build with -mod=vendor and GOPROXY=off, so a module missing from it fails
# the build rather than being fetched.
#
# The directory is NOT vendor/: a vendor/ at the module root switches every host
# go command to -mod=vendor, and a stale one breaks them all at the next go.mod
# change. A dot-directory is invisible to "./..." patterns, and .dockerignore
# keeps it out of the main build context.
#
# It is rebuilt on every call, so it always matches go.mod and go.sum.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="$ROOT/.docker-vendor"

if ! command -v go >/dev/null 2>&1; then
    echo "docker-vendor.sh: go is not installed. The image builds take their modules" >&2
    echo "from the host (see this script), so building an image needs Go here." >&2
    exit 1
fi

cd "$ROOT"
echo "Vendoring modules for the image builds into $OUT..."
GOWORK=off go mod vendor -o "$OUT"
