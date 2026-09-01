# Build stage
FROM golang:1.27.0-alpine AS builder

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

# Runtime stage
FROM alpine:latest

# Install runtime dependencies
RUN apk add --no-cache ca-certificates tzdata curl

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
