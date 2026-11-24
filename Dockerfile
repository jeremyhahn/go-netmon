# Build stage: pure Go cross-compile on the build platform; base pinned by tag and index digest.
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

# Runtime stage: pinned by index digest, with apk upgrade picking up current security patches at build time.
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

# Only the IETF standard MIBs are baked in; device MIBs arrive by import or restore into the volume.
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
