# Web UI Poller Integration

> **Status: REFERENCE** · Scope: go-netmon · Last verified: 2026-10-09

## Overview
This document describes the integration between the Web UI and the main go-netmon polling service. The integration enables the Web UI to manage device lifecycle (create, update, delete) and retrieve device status from the poller service.

## Components

### 1. Poller Client (`internal/webui/client/poller_client.go`)
HTTP client for interacting with the main poller service API.

**Features:**
- Configurable base URL and timeout
- Exponential backoff retry logic (max 3 attempts)
- Context propagation and cancellation support
- Graceful error handling

**Methods:**
- `RegisterDevice(ctx, device, mibNames, filters)` - Register a device with the poller
- `UpdateDevice(ctx, device, mibNames, filters)` - Update an existing device
- `RemoveDevice(ctx, deviceName)` - Remove a device from the poller
- `GetDeviceStatus(ctx, deviceName)` - Fetch device status
- `ListDevices(ctx)` - List all devices from the poller

`filters` maps a MIB module name to its `config.ModuleFilter`, which carries the per-device assignment filters to the poller.

**Configuration:**
- Poller URL: `-poller` flag, `webui.poller_url` in the config file, or `NETMON_WEBUI_POLLER_URL`; the default is `http://localhost:9080` (the Compose file points it at the poller service)
- Per-request timeout: the web UI passes `PollerRequestTimeout` (3 seconds); `NewPollerClient` itself falls back to 10 seconds when given zero
- Device changes are pushed within a `PollerSyncBudget` of 5 seconds
- Max attempts: 3, with exponential backoff between them (100ms, then 200ms)

### 2. Device Lifecycle Hooks (`internal/webui/api/device.go`)
Integration points in device CRUD handlers that synchronize with the poller service.

**Create Device:**
- Saves device to PebbleDB
- Retrieves associated MIB names
- Registers device with poller
- Uses graceful degradation (logs errors but doesn't fail)

**Update Device:**
- Updates device in PebbleDB
- Retrieves updated MIB names
- Updates device in poller
- Uses graceful degradation

**Delete Device:**
- Removes the device from the poller **first**
- Deletes it from PebbleDB only once the poller has stopped polling it
- Does **not** use graceful degradation: if the poller cannot be told to stop,
  the request answers 503 and nothing is deleted

The device is not deleted until nothing is polling it. Deleting the record
first and then failing to stop the polling leaves the device simultaneously
deleted and polling — absent from the device list and the fleet view, still
generating SNMP traffic, with nothing left in the UI to act on. Failing the
whole delete instead leaves the device unchanged and retryable.

A poller that reports it does not have the device satisfies the precondition
and the delete proceeds, so a delete that failed once can be retried to
completion.

**MIB/Profile Changes:**
- When MIBs or profiles are assigned/removed
- Automatically updates the poller with new MIB list
- Uses graceful degradation

### 3. Device Status Endpoints (`internal/webui/api/device_status.go`)
REST API endpoints for retrieving device status from the poller service.

**Endpoints:**
- `GET /api/devices/{id}/status` - Get status by device ID
- `GET /api/devices/status/{name}` - Get status by device name
- `GET /api/devices/status` - Fleet status from one poller listing call; see [webui.md](webui.md#fleet-status)

**Response Format:**
```json
{
  "name": "device-name",
  "connected": true,
  "last_poll_time": "2025-10-24T14:00:00Z",
  "last_poll_success": true,
  "last_error": "",
  "metrics_collected": 150,
  "poll_interval": "30s",
  "tags": {
    "environment": "production"
  },
  "unreachable": false,
  "consecutive_failures": 0,
  "next_poll_time": "2025-10-24T14:00:30Z"
}
```

`last_error`, `tags`, `consecutive_failures` and `next_poll_time` are omitted when empty. A missing `next_poll_time` means no retry is scheduled yet (a device on its first poll); it is not an error. `unreachable` is true while the poller gets no SNMP answer at all from the device and is retrying on a widening interval.

### 4. Server Wiring (`internal/webui/api/server.go`)
- `api.Config` takes a `PollerClient` (an interface, so tests can substitute a fake)
- The status routes are registered with the other `/api` routes

### 5. Main (`cmd/webui/main.go`)
- The `-poller` flag (or config/env above) sets the poller URL
- Builds the client with `NewPollerClient(url, api.PollerRequestTimeout)`
- Passes it to the API server

## Usage

### Starting the Web UI

```bash
./bin/webui \
  -listen :8082 \
  -prometheus http://localhost:9091 \
  -poller http://localhost:9080 \
  -db data/webui \
  -mib-storage data/mibs
```

Under Docker Compose these are set for you and the Web UI is published on host port 8082.

### API Examples

**Create a device:**
```bash
curl -X POST http://localhost:8082/api/devices \
  -H "Content-Type: application/json" \
  -d '{
    "name": "router-01",
    "address": "203.0.113.1",
    "snmp_version": "v2c",
    "community": "public"
  }'
```

**Get device status:**
```bash
curl http://localhost:8082/api/devices/1/status

# Or by name:
curl http://localhost:8082/api/devices/status/router-01
```

**Update device:**
```bash
curl -X PUT http://localhost:8082/api/devices/1 \
  -H "Content-Type: application/json" \
  -d '{
    "name": "router-01",
    "address": "203.0.113.2",
    "snmp_version": "v2c",
    "community": "private"
  }'
```

**Delete device:**
```bash
curl -X DELETE http://localhost:8082/api/devices/1
```

## Error Handling

### Graceful Degradation
The integration uses graceful degradation to ensure the Web UI continues functioning even if the poller service is unavailable:

1. **Device Operations**: If poller is down, devices are still saved/updated/deleted in PebbleDB
2. **Logging**: Poller errors are logged
3. **User Experience**: Users can still manage devices via the Web UI
4. **Status Endpoints**: Return 503 Service Unavailable (plain text) if the poller is down or no poller client is configured, and 404 if the poller does not know the device

### Retry Logic
The client automatically retries failed requests with exponential backoff:
- Attempt 1: Immediate
- Attempt 2: 100ms delay
- Attempt 3: 200ms delay

Client errors (4xx) and context cancellation are not retried.

## Testing

### Unit Tests
- `internal/webui/client/poller_client_test.go`
  - Tests the client methods
  - Tests retry logic and timeout
  - Tests context cancellation and error handling

- `internal/webui/api/device_status_test.go`
  - Tests status endpoints with a mocked client
  - Tests error scenarios and response format

### Running Tests

```bash
# Test poller client
make test-webui-client

# Test API
make test-webui-api

# Test all webui components
make test-webui
```

### Coverage

```bash
# Generate coverage report
make coverage-webui
```

The Makefile target names the output; check `make coverage-webui` for where it writes the report.

## Architecture

```
┌─────────────┐         ┌──────────────┐         ┌───────────────┐
│   Web UI    │         │  Poller      │         │  Prometheus   │
│   (8082)    │         │  Service     │         │    (9091)     │
└──────┬──────┘         │  (9080)      │         └───────────────┘
       │                └───────┬──────┘                  ▲
       │                        │                         │
       │ HTTP Client            │ SNMP Polls              │ Metrics
       │ (PollerClient)         │                         │
       │                        ▼                         │
       │                ┌───────────────┐                 │
       │                │   Network     │                 │
       │                │   Devices     │                 │
       │                └───────────────┘                 │
       │                                                  │
       ▼                                                  │
┌─────────────┐                                          │
│  PebbleDB   │                                          │
│  Storage    │                                          │
└─────────────┘                                          │
       │                                                  │
       └─────────────── MIBs, Profiles, ─────────────────┘
                        Device Config
```

Ports are host ports from `docker-compose.yml`; inside the Compose network the services use their container ports.

## Profile Resolution

When a device is created or updated, the Web UI:
1. Queries PebbleDB for all MIBs associated with the device
   - Direct MIB assignments
   - MIBs from assigned profiles
2. Extracts MIB names
3. Passes them to the poller as the `modules` array, with the per-assignment filters
4. Poller uses these modules to determine what OIDs to poll

## Troubleshooting

### Poller Connection Issues

**Symptom**: Device operations succeed but status is unavailable

**Check:**
1. Poller service is running: `docker ps | grep go-netmon`
2. Poller URL is correct: check the `-poller` flag, `webui.poller_url`, or `NETMON_WEBUI_POLLER_URL`
3. Network connectivity: `curl http://localhost:9080/api/poller/devices`
4. Check logs: `make logs-webui`

### Device Not Showing in Poller

**Symptom**: Device created in Web UI but not polling

**Check:**
1. Check Web UI logs for registration errors
2. Verify poller received the device: `curl http://localhost:9080/api/poller/devices`
3. Verify MIBs/profiles are correctly assigned

### Status Returns 404

**Symptom**: GET /api/devices/{id}/status returns 404

**Possible Causes:**
1. The device id is unknown to the Web UI ("device not found")
2. The poller does not know the device ("device not found in poller"): it was not registered, or its name differs between the two systems

**Solution:**
1. Update the device to re-register it: `PUT /api/devices/{id}`
2. Check the device name in both systems

## References

- Poller device control API: `internal/api/poller_control.go`
- Device status types: `internal/api/device_status.go`
- Web UI API: `internal/webui/api/`
- Storage layer: `internal/webui/storage/`
