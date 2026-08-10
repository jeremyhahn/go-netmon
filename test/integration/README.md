# Integration Tests

This directory contains end-to-end integration tests for go-netmon that run in a Docker environment.

## Overview

The integration tests spin up a complete test environment including:
- **Prometheus** - Receives metrics via remote write
- **SNMP Simulator** - Simulates a Cisco router with SNMP v2c
- **Integration Test Runner** - Runs the full pipeline test

## Architecture

```
┌─────────────────────────────────────────┐
│     Integration Test Environment       │
├─────────────────────────────────────────┤
│                                         │
│  ┌──────────────────────────────────┐  │
│  │   go-netmon (test runner)        │  │
│  │   • Loads MIBs                   │  │
│  │   • Creates profiles             │  │
│  │   • Polls SNMP device            │  │
│  │   • Exports to Prometheus        │  │
│  └───────┬──────────────────────┬───┘  │
│          │                      │       │
│          v                      v       │
│  ┌──────────────┐      ┌──────────────┐│
│  │SNMP Simulator│      │  Prometheus  ││
│  │  (port 161)  │      │ (port 9090)  ││
│  │              │      │ Remote Write ││
│  │ Cisco Router │      │   Enabled    ││
│  │   Mockup     │      │              ││
│  └──────────────┘      └──────────────┘│
└─────────────────────────────────────────┘
```

## Quick Start

### Run All Integration Tests

```bash
make test-integration
```

This will:
1. Build the test container
2. Start Prometheus and SNMP simulator
3. Wait for services to be healthy
4. Run integration tests
5. Clean up all containers and volumes

### Manual Testing

Start services for manual testing:

```bash
# Start Prometheus and SNMP simulator
make integration-up

# Access services:
# - Prometheus UI: http://localhost:9090
# - SNMP simulator: localhost:1161

# Run tests manually
go test -v -tags=integration ./test/integration/...

# Or open a shell in the test environment
make integration-shell

# Stop services when done
make integration-down
```

### View Logs

```bash
make integration-logs
```

## Test Scenarios

### TestEndToEndIntegration

Tests the complete pipeline:
1. Initializes MIB loader with standard MIBs
2. Loads built-in profiles (8 profiles)
3. Creates Prometheus exporter
4. Creates poller with test device configuration
5. Starts polling the SNMP simulator
6. Verifies poll results are collected
7. Verifies metrics are exported to Prometheus
8. Queries Prometheus to confirm metrics exist

**Expected Results:**
- At least 2 poll cycles complete successfully
- SNMP results contain multiple metrics
- Metrics are visible in Prometheus with correct labels

### TestSNMPConnectivity

Basic connectivity test to SNMP simulator.

### TestPrometheusConnectivity

Verifies Prometheus is accessible and healthy.

## Test Data

### SNMP Simulator Data

The SNMP simulator uses data from `snmp-data/public.snmprec`:
- System group (sysDescr, sysUpTime, sysName, etc.)
- Interface group (ifTable with 2 interfaces)
- IP-MIB counters

The simulated device appears as a Cisco IOS router.

## Environment Variables

Tests use these environment variables (automatically set by docker-compose):

- `PROMETHEUS_URL` - Prometheus endpoint (default: http://localhost:9090)
- `SNMP_HOST` - SNMP simulator hostname (default: localhost)
- `SNMP_PORT` - SNMP port (default: 1161)

## Docker Compose Services

### prometheus

- Image: `prom/prometheus:v2.45.0`
- Port: `9090:9090`
- Remote write receiver enabled
- Health check: `/-/healthy` endpoint

### snmp-simulator

- Image: `tandrup/snmpsim:latest`
- Port: `1161:161/udp`
- Community: `public`
- Data: `./snmp-data/public.snmprec`
- Health check: snmpwalk command

### integration-test

- Built from: `Dockerfile.test`
- Depends on: prometheus, snmp-simulator
- Runs: `go test -v -tags=integration ./test/integration/...`

## Troubleshooting

### Services Not Ready

If tests fail with "service not ready" errors:

```bash
# Check service health
docker-compose -f docker-compose.test.yml ps

# View logs
make integration-logs

# Restart services
make integration-down
make integration-up
```

### SNMP Connection Failures

Verify SNMP simulator is responding:

```bash
# From host (requires snmpwalk)
snmpwalk -v2c -c public localhost:1161 system

# From within docker network
docker-compose -f docker-compose.test.yml exec snmp-simulator \
  snmpwalk -v2c -c public localhost system
```

### Prometheus Not Receiving Metrics

Check Prometheus:

```bash
# View Prometheus UI
open http://localhost:9090

# Query for metrics
curl 'http://localhost:9090/api/v1/query?query={device="test-router"}'

# Check remote write endpoint
curl -X POST http://localhost:9090/api/v1/write \
  -H "Content-Type: application/x-protobuf"
```

### Build Failures

Clear Docker build cache:

```bash
docker-compose -f docker-compose.test.yml down -v
docker system prune -f
make test-integration
```

## Adding New Tests

1. Create new test functions in `integration_test.go`
2. Use build tag: `//go:build integration`
3. Access services via environment variables
4. Add test data to `snmp-data/` if needed

Example:

```go
//go:build integration
// +build integration

func TestNewFeature(t *testing.T) {
    prometheusURL := os.Getenv("PROMETHEUS_URL")
    // ... test implementation
}
```

## CI/CD Integration

### GitHub Actions Example

```yaml
name: Integration Tests
on: [push, pull_request]
jobs:
  integration:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      - uses: docker/setup-buildx-action@v2
      - name: Run Integration Tests
        run: make test-integration
```

### GitLab CI Example

```yaml
integration-test:
  stage: test
  image: docker:latest
  services:
    - docker:dind
  script:
    - apk add --no-cache make
    - make test-integration
```

## Performance

Typical test execution time:
- Service startup: ~10-15 seconds
- Test execution: ~20-30 seconds
- Total: ~30-45 seconds

## Cleanup

Integration tests automatically clean up:
- All containers are stopped
- Volumes are removed
- Network is deleted

Manual cleanup if needed:

```bash
make integration-down
docker system prune -f
```
