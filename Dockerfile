# Build stage.
#
# Pinned to the patch AND to the manifest digest. "1.27-alpine" floats onto
# every Go patch release and every alpine rebuild underneath it; the tag alone
# still floats, because a tag is a mutable pointer the registry can re-aim with
# no commit behind it. The digest is the index (multi-arch) digest, so the pin
# does not cost the manifest list.
FROM golang:1.27.1-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS builder

# Install build dependencies
RUN apk add --no-cache git make

# Set working directory
WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN make build && make build-mib-importer && make build-webui

# Runtime stage.
#
# PINNED BY DIGEST, and then UPGRADED ON PURPOSE. Two separate defects lived
# here, and each fix alone leaves the other half open:
#
#  1. "FROM alpine:latest" means the artifact can change with no commit behind
#     it -- whether a build is vulnerable depends on the day it ran, and on the
#     build host the cached "latest" was two months old, so a rebuild would not
#     even have produced that day's latest. A version TAG narrows this and does
#     not close it: alpine:3.24 is a mutable pointer, and the image it names in
#     six months is not the image it names today.
#  2. A pinned base never receives security patches on its own. Measured
#     2026-08-28: the image built from this file carried libcrypto3/libssl3
#     3.5.7-r0 while 3.5.8-r0 was published, which trivy reports as
#     CVE-2026-14456 HIGH. The Go binaries in the same image scan CLEAN -- a
#     binary-only scan cannot see this by construction, which is why it sat
#     here unnoticed.
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

# Install runtime dependencies
RUN apk upgrade --no-cache && \
    apk add --no-cache ca-certificates tzdata curl

# Create non-root user
RUN addgroup -g 1000 netmon && \
    adduser -D -u 1000 -G netmon netmon

# Set working directory
WORKDIR /app

# Copy binaries from builder
COPY --from=builder /build/bin/go-netmon /app/go-netmon
COPY --from=builder /build/bin/mib-importer /app/mib-importer

# Copy MIBs directory
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

# Create directories for config and data
RUN mkdir -p /app/config /app/data && \
    chown -R netmon:netmon /app

# Switch to non-root user
USER netmon

# Expose ports (9080: API, 9100: Metrics)
EXPOSE 9080 9100

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD curl -f http://localhost:9080/health || exit 1

# Run the application
ENTRYPOINT ["/app/go-netmon"]
