# Web UI Backend - Quick Start Guide

> **Status: REFERENCE** · Scope: go-netmon · Last verified: 2026-10-09

How to run the web UI backend (`cmd/webui`) and drive its REST API with `curl`.
The routes are registered in `internal/webui/api/server.go`.

## 1. Run the server

```bash
make build-webui          # builds cmd/webui into the build directory
# or run it straight from source:
go run ./cmd/webui
```

Flags (`cmd/webui/main.go`):

| Flag            | Default       | Meaning                                              |
| --------------- | ------------- | ---------------------------------------------------- |
| `-listen`       | `:8080`       | Listen address (`webui.listen_address`, env `NETMON_WEBUI_LISTEN_ADDRESS`) |
| `-prometheus`   | `http://localhost:9091` | Prometheus to query (`webui.prometheus_url`, env `NETMON_WEBUI_PROMETHEUS_URL`) |
| `-poller`       | `http://localhost:9080` | Poller control API (`webui.poller_url`, env `NETMON_WEBUI_POLLER_URL`) |
| `-db`           | `data/webui`  | Web UI storage, a PebbleDB directory                 |
| `-mib-storage`  | `data/mibs`   | Where uploaded MIB files are stored                  |
| `-mib-catalog`  | `data/mib.db` | The parsed MIB catalog, a PebbleDB directory         |

The three address flags resolve flag > environment > config file > default.
Device changes are saved first; the push to the poller is best effort and does
not fail the request when the poller is down.

> **Bind the port privately.** The API is unauthenticated by design and
> `GET /api/devices` returns SNMP community strings in clear text. Publish it
> only behind an authenticating reverse proxy, or bind it to an interface only
> trusted clients can reach. See
> [operations/security-posture.md](operations/security-posture.md).

## 2. Try the API

The examples below assume `API=http://localhost:8080/api`.

```bash
API=http://localhost:8080/api

# Create a device (name, address and snmp_version are required)
curl -X POST $API/devices \
  -H "Content-Type: application/json" \
  -d '{"name":"test-router","address":"203.0.113.1","snmp_version":"2c","community":"public"}'

# List devices
curl $API/devices

# Get one device
curl $API/devices/1
```

## 3. A complete workflow

```bash
API="http://localhost:8080/api"

# Create a profile
PROFILE=$(curl -s -X POST $API/profiles \
  -H "Content-Type: application/json" \
  -d '{"name":"cisco-router","description":"Cisco Router Profile"}')
PROFILE_ID=$(echo $PROFILE | jq -r '.id')

# Create a device
DEVICE=$(curl -s -X POST $API/devices \
  -H "Content-Type: application/json" \
  -d '{"name":"router-1","address":"203.0.113.1","snmp_version":"2c","community":"public"}')
DEVICE_ID=$(echo $DEVICE | jq -r '.id')

# Assign the profile to the device
curl -X POST "$API/devices/$DEVICE_ID/profiles/$PROFILE_ID"

# List everything
curl -s "$API/devices" | jq '.devices'
curl -s "$API/profiles" | jq '.profiles'
```

## Common operations

### Devices

```bash
# Update a device
curl -X PUT $API/devices/1 \
  -H "Content-Type: application/json" \
  -d '{"name":"NAME","address":"NEW_IP","snmp_version":"2c","community":"private"}'

# Delete a device
curl -X DELETE $API/devices/1

# Test SNMP connectivity (see timeout-budget.md for the status codes)
curl -X POST $API/devices/1/test-snmp
```

### Profiles

```bash
curl -X POST $API/profiles \
  -H "Content-Type: application/json" \
  -d '{"name":"NAME","description":"DESC"}'
curl $API/profiles
```

### MIBs

```bash
# Import a MIB (add ?replace=true to refresh a module that is already imported;
# without it, re-uploading a stored module is a 409)
curl -X POST $API/mibs/import -F "file=@/path/to/MIB.txt"

# List stored MIBs
curl "$API/mibs"

# Assign a MIB to a profile, or to a device
curl -X POST $API/profiles/1/mibs \
  -H "Content-Type: application/json" -d '{"mib_id":1}'
curl -X POST $API/devices/1/mibs \
  -H "Content-Type: application/json" -d '{"mib_id":1}'
```

### Dashboards

```bash
# Dashboard for a device
curl $API/devices/1/dashboard | jq '.'

# Dashboard for chosen MIBs
curl -X POST $API/dashboards/generate \
  -H "Content-Type: application/json" \
  -d '{"device_id":1,"mib_ids":[1,2,3],"title":"My Dashboard"}'
```

## Running tests

```bash
make test-webui           # web UI packages
make test-webui-storage   # storage only
make coverage-webui-storage
```

## Building

```bash
make build-webui
make build-all            # every binary
```

## Troubleshooting

### The data directory will not open

Storage is PebbleDB, which allows one process per directory. If the server
fails to open `data/webui`, check that no other `webui` process is using it and
that the directory is writable by the user running the server.

### Port already in use

```bash
lsof -i :8080
```

Then stop that process, or start the web UI with a different `-listen`.

## File locations

```
internal/webui/
├── api/            # API handlers
│   ├── server.go      # Server, route table
│   ├── device.go      # Device endpoints
│   ├── profile.go     # Profile endpoints
│   └── mib_import.go  # MIB import
└── storage/        # Database layer
    ├── storage.go     # DB initialization
    ├── device.go      # Device CRUD
    ├── profile.go     # Profile CRUD
    └── mib.go         # MIB CRUD
```

## Next steps

- [webui-backend.md](webui-backend.md) — the full backend description
- [webui-backend-example.md](webui-backend-example.md) — examples
- [webui-backend-integration.md](webui-backend-integration.md) — integration guide
