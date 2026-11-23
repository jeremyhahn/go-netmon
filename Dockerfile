# Build stage.
#
# Runs on the build host's own platform and cross-compiles for the target:
# every binary is pure Go (CGO_ENABLED=0), so a linux/arm64 image builds on an
# amd64 host at native speed instead of under emulation.
#
# Pinned to the patch AND to the manifest digest. "1.27-alpine" floats onto
# every Go patch release and every alpine rebuild underneath it; the tag alone
# still floats, because a tag is a mutable pointer the registry can re-aim with
# no commit behind it. The digest is the index (multi-arch) digest, so the pin
# does not cost the manifest list.
FROM --platform=$BUILDPLATFORM golang:1.27.4-alpine@sha256:0a5f7fe796599ff487605987432e7619ae45beeb2fba4c2ce0b907879932aebf AS builder

RUN apk add --no-cache make

WORKDIR /build

# Modules first, in their own layer: a source change does not download them
# again.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# The build stamp and variant. release.yml passes all of them; the Makefile
# owns the flags (LDFLAGS, -trimpath and, for FIPS=1, GOFIPS140 and -tags fips).
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG REVISION=unknown
ARG BUILD_DATE=unknown
ARG FIPS=0

RUN CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    make build build-mib-importer BUILD_FLAGS=-trimpath \
        VERSION="$VERSION" GIT_COMMIT="$REVISION" BUILD_DATE="$BUILD_DATE" FIPS="$FIPS"

# Runtime stage.
#
# PINNED BY DIGEST, and then UPGRADED ON PURPOSE. Two separate defects lived
# here, and each fix alone leaves the other half open:
#
#  1. "FROM alpine:latest" means the artifact can change with no commit behind
#     it -- whether a build is vulnerable depends on the day it ran. A version
#     TAG narrows this and does not close it: alpine:3.24 is a mutable pointer,
#     and the image it names in six months is not the image it names today.
#  2. A pinned base never receives security patches on its own: an image built
#     from it carried an OpenSSL with a published HIGH fix (CVE-2026-14456)
#     while the Go binaries in it scanned clean. A binary-only scan cannot see
#     that by construction.
#
# The digest does not freeze the patches, which is why both halves fit
# together: "apk upgrade --no-cache" re-reads the alpine mirror's CURRENT
# package index at build time, whatever layer the build started from. The
# digest fixes only the starting layer's identity. So the base is auditable and
# the packages are current, and the pin is bumped deliberately in a reviewed
# commit.
#
# Digest below is docker.io/library/alpine:3.24 as of 2026-09-06 (alpine
# 3.24.1); it is the index digest, so linux/arm64 still resolves.
FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b

# curl serves the HEALTHCHECK.
RUN apk upgrade --no-cache && \
    apk add --no-cache ca-certificates tzdata curl

# Create non-root user
RUN addgroup -g 1000 netmon && \
    adduser -D -u 1000 -G netmon netmon

WORKDIR /app

COPY --from=builder /build/bin/go-netmon /app/go-netmon
COPY --from=builder /build/bin/mib-importer /app/mib-importer

# Only the IETF standard modules are baked in. They are the base every SNMP
# deployment needs before it can parse anything, so a fresh container can
# bootstrap with an empty volume.
#
# The device-specific trees (asr1001, cisco, r640, vendor) are NOT shipped: they
# are 20 MB of one site's equipment, they are testdata for the parser rather
# than a runtime dependency, and baking them in put a module on the search path
# that no backup covered and no operator had imported. Device MIBs arrive by
# import or restore, and are then held in the volume.
COPY --from=builder /build/mibs/standard /app/mibs/standard

RUN mkdir -p /app/config /app/data && \
    chown -R netmon:netmon /app

USER netmon

# 9080: API, 9100: metrics
EXPOSE 9080 9100

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD curl -f http://localhost:9080/health || exit 1

# OCI metadata. Declared last: the build arguments change on every release,
# and declaring them earlier would invalidate the cached layers above.
ARG VERSION=dev
ARG REVISION=unknown
ARG BUILD_DATE=unknown
LABEL org.opencontainers.image.title="go-netmon" \
      org.opencontainers.image.description="SNMP poller: polls network devices and writes their metrics to Prometheus" \
      org.opencontainers.image.source="https://github.com/jeremyhahn/go-netmon" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="$VERSION" \
      org.opencontainers.image.revision="$REVISION" \
      org.opencontainers.image.created="$BUILD_DATE"

ENTRYPOINT ["/app/go-netmon"]
