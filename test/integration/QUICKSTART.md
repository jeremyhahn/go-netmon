# Integration Tests - Quick Start

## The Fixes

### Fix 1: Docker Buildx Support

**Issue:** Docker Compose v1 (`docker-compose`) doesn't properly support Docker buildx.

**Solution:** Updated Makefile to use Docker Compose v2 (`docker compose`) which has native buildx support.

### Fix 2: Container Hang Issue

**Issue:** `docker compose up --abort-on-container-exit` doesn't immediately exit after test completion.

**Solution:** Created `run-tests.sh` script that:
- Starts services in detached mode (`-d`)
- Waits for health checks
- Runs tests with `docker compose run`
- Always cleans up via trap (even if interrupted)

## Changes Made

1. **Makefile** - Changed all `docker-compose` to `docker compose`, simplified test-integration target
2. **docker-compose.test.yml** - Removed obsolete `version` field, improved health checks
3. **test/integration/run-tests.sh** - New script for robust test execution with cleanup

## Running Tests

### Full Test Suite (Recommended)

```bash
make test-integration
```

This will:
- Build all containers using buildx
- Start Prometheus and SNMP simulator
- Wait for services to be healthy
- Run integration tests
- Cleanup everything

**Expected Output:**
```
Running integration tests in Docker...
Starting test services (Prometheus + SNMP simulator)...
[+] Running 3/3
 ✔ Network go-netmon_test-network     Created
 ✔ Container go-netmon-test-prometheus Started
 ✔ Container go-netmon-test-snmp      Started

=== RUN   TestEndToEndIntegration
--- PASS: TestEndToEndIntegration (20.3s)
=== RUN   TestSNMPConnectivity
--- PASS: TestSNMPConnectivity (0.5s)
=== RUN   TestPrometheusConnectivity
--- PASS: TestPrometheusConnectivity (0.1s)
PASS

Cleaning up test services...
```

### Manual Testing

```bash
# Start services in background
make integration-up

# Services are now running:
# - Prometheus: http://localhost:9090
# - SNMP: localhost:1161

# Run tests manually
go test -v -tags=integration ./test/integration/...

# Stop services
make integration-down
```

### Debug Failed Tests

```bash
# Start services
make integration-up

# View logs in real-time
make integration-logs

# Open shell in test environment
make integration-shell

# Inside shell:
$ go test -v -tags=integration ./test/integration/...
$ snmpwalk -v2c -c public snmp-simulator system
$ curl http://prometheus:9090/-/healthy

# Exit shell and cleanup
make integration-down
```

## Troubleshooting

### "docker compose: command not found"

**Solution:** Install Docker Compose v2

```bash
# Check if installed
docker compose version

# Ubuntu/Debian
sudo apt-get update
sudo apt-get install docker-compose-plugin

# Or update Docker Desktop
# Docker Desktop includes Compose v2 by default
```

### "Cannot connect to Docker daemon"

**Solution:** Start Docker service

```bash
sudo systemctl start docker
sudo systemctl enable docker  # Start on boot
```

### Ports Already in Use

**Solution:** Stop conflicting containers

```bash
# Find what's using port 9090
sudo lsof -i :9090

# Or check Docker
docker ps | grep 9090

# Stop the container
docker stop <container-id>
```

### Services Not Healthy

**Solution:** Check service logs

```bash
make integration-logs

# Or check specific service
docker compose -f docker-compose.test.yml logs prometheus
docker compose -f docker-compose.test.yml logs snmp-simulator
```

## Requirements

- **Docker Engine** 20.10+ with buildx support
- **Docker Compose** v2.0+ (not v1)
- **Make** (usually pre-installed on Linux/Mac)

**Check versions:**
```bash
docker version          # Should be 20.10+
docker compose version  # Should be v2.x
docker buildx version   # Should be available
make --version          # Any version
```

## CI/CD Usage

### GitHub Actions

```yaml
name: Integration Tests
on: [push, pull_request]

jobs:
  integration-tests:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3

      - name: Set up Docker Buildx
        uses: docker/setup-buildx-action@v2

      - name: Run Integration Tests
        run: make test-integration
```

### GitLab CI

```yaml
integration-tests:
  stage: test
  image: docker:latest
  services:
    - docker:dind
  before_script:
    - apk add --no-cache make
  script:
    - make test-integration
```

## What Gets Tested

✅ **Full Pipeline** - SNMP poll → MIB processing → Prometheus export
✅ **Real Services** - Actual Prometheus and SNMP simulator
✅ **End-to-End** - Complete data flow validation
✅ **Network Operations** - Real HTTP and SNMP connections
✅ **Database Operations** - PebbleDB storage with real data

## Performance

- **Build time:** 30-60 seconds (first time, then cached)
- **Service startup:** 10-15 seconds
- **Test execution:** 20-30 seconds
- **Total:** ~45-60 seconds (first run), ~30-40 seconds (cached)

## Next Steps

1. Run `make test-integration` to verify everything works
2. Check `test/integration/README.md` for detailed documentation
3. View `docs/INTEGRATION_TESTS.md` for architecture overview
4. Add your own test scenarios to `integration_test.go`

## Quick Commands Reference

```bash
# Run full test suite
make test-integration

# Start services for manual testing
make integration-up

# Stop and cleanup
make integration-down

# View logs
make integration-logs

# Debug shell
make integration-shell

# Run unit tests
make test-unit

# Run all tests
make test
```

## Success Criteria

You know it's working when:
1. No build errors
2. Services start and become healthy
3. All 3 tests pass (TestEndToEndIntegration, TestSNMPConnectivity, TestPrometheusConnectivity)
4. Metrics visible in Prometheus at http://localhost:9090
5. Clean shutdown with no errors

**Happy Testing!** 🎉
