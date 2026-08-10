# Web UI Backend - Quick Start Guide

## 5-Minute Setup

### 1. Run the Server (Standalone Test)

```go
// test-server.go
package main

import (
    "context"
    "log"

    "github.com/jeremyhahn/go-netmon/internal/webui/api"
)

func main() {
    // Simple MIB provider (returns empty for testing)
    mibProvider := &emptyMIBProvider{}

    // Simple Prometheus query function (returns empty for testing)
    queryFunc := func(ctx context.Context, query string) ([]api.MetricValue, error) {
        return []api.MetricValue{}, nil
    }

    // Create server
    cfg := api.Config{
        QueryFunc:      queryFunc,
        MIBProvider:    mibProvider,
        DBPath:         "./webui-test.db",
        MIBStoragePath: "./mibs",
    }

    server, err := api.NewServer(cfg)
    if err != nil {
        log.Fatal(err)
    }
    defer server.Close()

    log.Println("Server starting on :8080")
    log.Fatal(server.Start(context.Background(), ":8080"))
}

type emptyMIBProvider struct{}

func (e *emptyMIBProvider) ListMIBs() ([]api.MIBInfo, error) {
    return []api.MIBInfo{}, nil
}

func (e *emptyMIBProvider) GetMIB(name string) (*api.MIBDetail, error) {
    return &api.MIBDetail{Name: name, Objects: []api.MIBObject{}}, nil
}
```

Run it:
```bash
go run test-server.go
```

### 2. Test the API

```bash
# Create a device
curl -X POST http://localhost:8080/api/devices \
  -H "Content-Type: application/json" \
  -d '{
    "name": "test-router",
    "address": "203.0.113.1",
    "snmp_version": "2c",
    "community": "public"
  }'

# List devices
curl http://localhost:8080/api/devices?stored=true

# Get device
curl http://localhost:8080/api/devices/1
```

### 3. Complete Workflow

```bash
#!/bin/bash
API="http://localhost:8080/api"

# Create profile
PROFILE=$(curl -s -X POST $API/profiles \
  -H "Content-Type: application/json" \
  -d '{"name":"cisco-router","description":"Cisco Router Profile"}')

PROFILE_ID=$(echo $PROFILE | jq -r '.id')
echo "Created profile: $PROFILE_ID"

# Create device
DEVICE=$(curl -s -X POST $API/devices \
  -H "Content-Type: application/json" \
  -d '{"name":"router-1","address":"203.0.113.1","snmp_version":"2c","community":"public"}')

DEVICE_ID=$(echo $DEVICE | jq -r '.id')
echo "Created device: $DEVICE_ID"

# Assign profile to device
curl -X POST "$API/devices/$DEVICE_ID/profiles/$PROFILE_ID"

# List everything
echo "Devices:"
curl -s "$API/devices?stored=true" | jq '.devices'

echo "Profiles:"
curl -s "$API/profiles" | jq '.profiles'
```

## Common Operations

### Device Management

```bash
# Create device
curl -X POST $API/devices \
  -H "Content-Type: application/json" \
  -d '{"name":"NAME","address":"IP","snmp_version":"2c","community":"public"}'

# Update device
curl -X PUT $API/devices/1 \
  -H "Content-Type: application/json" \
  -d '{"name":"NAME","address":"NEW_IP","snmp_version":"2c","community":"private"}'

# Delete device
curl -X DELETE $API/devices/1
```

### Profile Management

```bash
# Create profile
curl -X POST $API/profiles \
  -H "Content-Type: application/json" \
  -d '{"name":"NAME","description":"DESC"}'

# List profiles
curl $API/profiles
```

### MIB Management

```bash
# Import MIB
curl -X POST $API/mibs/import -F "file=@/path/to/MIB.txt"

# List MIBs
curl "$API/mibs?stored=true"

# Assign MIB to profile
curl -X POST $API/profiles/1/mibs \
  -H "Content-Type: application/json" \
  -d '{"mib_id":1}'

# Assign MIB to device
curl -X POST $API/devices/1/mibs \
  -H "Content-Type: application/json" \
  -d '{"mib_id":1}'
```

### Dashboard Generation

```bash
# Generate dashboard for device
curl $API/devices/1/dashboard | jq '.' > dashboard.json

# Custom dashboard
curl -X POST $API/dashboards/generate \
  -H "Content-Type: application/json" \
  -d '{"device_id":1,"mib_ids":[1,2,3],"title":"My Dashboard"}' \
  | jq '.' > custom-dashboard.json
```

## Running Tests

```bash
# All tests
go test ./internal/webui/...

# Storage tests only
make test-webui-storage

# With coverage
make coverage-webui-storage

# With race detector
go test -race ./internal/webui/storage/...
```

## Building

```bash
# Build everything
go build ./...

# Build specific package
go build ./internal/webui/storage/...
go build ./internal/webui/api/...
```

## Docker Quick Start

```yaml
# docker-compose.yml
services:
  go-netmon:
    build: .
    ports:
      - "8080:8080"
    volumes:
      - ./data:/app/data
      - ./mibs:/app/mibs
    environment:
      - WEBUI_DB_PATH=/app/data/webui.db
```

```bash
docker-compose up
```

## Environment Variables

```bash
export WEBUI_DB_PATH=./data/webui.db
export MIB_STORAGE_PATH=./mibs
export PROMETHEUS_URL=http://localhost:9090
export API_LISTEN_ADDR=:8080
```

## Troubleshooting

### Database locked
```bash
# Check if database exists
ls -la data/webui.db

# Check permissions
chmod 644 data/webui.db
```

### Port already in use
```bash
# Find process
lsof -i :8080

# Kill process
kill -9 <PID>
```

### CGO build errors
```bash
# Enable CGO
CGO_ENABLED=1 go build ./...

# Install SQLite dev package (Ubuntu/Debian)
sudo apt-get install libsqlite3-dev
```

## Quick Reference - File Locations

```
internal/webui/
├── api/            # API handlers
│   ├── server.go      # Main server
│   ├── device.go      # Device endpoints
│   ├── profile.go     # Profile endpoints
│   └── mib_import.go  # MIB import
└── storage/        # Database layer
    ├── storage.go     # DB initialization
    ├── device.go      # Device CRUD
    ├── profile.go     # Profile CRUD
    └── mib.go        # MIB CRUD

docs/
├── webui-backend.md              # Full API docs
├── webui-backend-example.md      # Examples
├── webui-backend-integration.md  # Integration guide
└── QUICK-START.md               # This file
```

## Next Steps

1. Read the [full documentation](webui-backend.md)
2. Check [integration examples](webui-backend-example.md)
3. Follow [integration guide](webui-backend-integration.md) to add to your app
4. Review [implementation summary](IMPLEMENTATION-SUMMARY.md) for details

## Getting Help

Check the documentation:
- API Reference: `docs/webui-backend.md`
- Examples: `docs/webui-backend-example.md`
- Integration: `docs/webui-backend-integration.md`

All code is well-documented with inline comments.
