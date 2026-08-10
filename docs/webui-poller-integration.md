# Web UI Poller Integration

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
- `RegisterDevice(ctx, device, mibNames)` - Register a device with the poller
- `UpdateDevice(ctx, device, mibNames)` - Update an existing device
- `RemoveDevice(ctx, deviceName)` - Remove a device from the poller
- `GetDeviceStatus(ctx, deviceName)` - Fetch device status
- `ListDevices(ctx)` - List all devices from the poller

**Configuration:**
- Default base URL: `http://go-netmon:8080`
- Default timeout: 10 seconds
- Max retries: 3 with exponential backoff (100ms, 200ms, 400ms)

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
- Deletes device from PebbleDB
- Removes device from poller
- Uses graceful degradation

**MIB/Profile Changes:**
- When MIBs or profiles are assigned/removed
- Automatically updates the poller with new MIB list
- Uses graceful degradation

### 3. Device Status Endpoint (`internal/webui/api/device_status.go`)
REST API endpoints for retrieving device status from the poller service.

**Endpoints:**
- `GET /api/devices/{id}/status` - Get status by device ID
- `GET /api/devices/status/{name}` - Get status by device name

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
  }
}
```

### 4. Server Updates (`internal/webui/api/server.go`)
- Added `PollerClient` interface to Server struct
- Added `pollerClient` field to Server
- Updated Config to accept PollerClient
- Added status routes to router

### 5. Main Updates (`cmd/webui/main.go`)
- Added `--poller` flag for poller URL configuration
- Initializes PollerClient with configured URL and timeout
- Passes client to API server during initialization

## Usage

### Starting the Web UI

```bash
./bin/webui \
  --listen :8081 \
  --prometheus http://localhost:9091 \
  --poller http://go-netmon:8080 \
  --db data/webui \
  --mib-storage data/mibs
```

### API Examples

**Create a device:**
```bash
curl -X POST http://localhost:8081/api/devices \
  -H "Content-Type: application/json" \
  -d '{
    "name": "router-01",
    "address": "203.0.113.1",
    "snmp_version": "2c",
    "community": "public"
  }'
```

**Get device status:**
```bash
curl http://localhost:8081/api/devices/1/status

# Or by name:
curl http://localhost:8081/api/devices/status/router-01
```

**Update device:**
```bash
curl -X PUT http://localhost:8081/api/devices/1 \
  -H "Content-Type: application/json" \
  -d '{
    "name": "router-01",
    "address": "203.0.113.2",
    "snmp_version": "2c",
    "community": "private"
  }'
```

**Delete device:**
```bash
curl -X DELETE http://localhost:8081/api/devices/1
```

## Error Handling

### Graceful Degradation
The integration uses graceful degradation to ensure the Web UI continues functioning even if the poller service is unavailable:

1. **Device Operations**: If poller is down, devices are still saved/updated/deleted in PebbleDB
2. **Logging**: All poller errors are logged at WARNING level
3. **User Experience**: Users can still manage devices via the Web UI
4. **Status Endpoint**: Returns 503 Service Unavailable if poller is down

### Retry Logic
The client automatically retries failed requests with exponential backoff:
- Attempt 1: Immediate
- Attempt 2: 100ms delay
- Attempt 3: 200ms delay

Client errors (4xx) are not retried.

## Testing

### Unit Tests
- `internal/webui/client/poller_client_test.go` - 92% coverage
  - Tests all client methods
  - Tests retry logic and timeout
  - Tests context cancellation
  - Tests error handling

- `internal/webui/api/device_status_test.go`
  - Tests status endpoints with mocked client
  - Tests error scenarios
  - Tests response format

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

# View HTML coverage report
open coverage/coverage-webui.html
```

## Architecture

```
┌─────────────┐         ┌──────────────┐         ┌───────────────┐
│   Web UI    │         │  Poller      │         │  Prometheus   │
│   (8081)    │         │  Service     │         │    (9091)     │
└──────┬──────┘         │  (8080)      │         └───────────────┘
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

## Profile Resolution

When a device is created or updated, the Web UI:
1. Queries PebbleDB for all MIBs associated with the device
   - Direct MIB assignments
   - MIBs from assigned profiles
2. Extracts MIB names
3. Passes them to the poller as the `modules` array
4. Poller uses these modules to determine what OIDs to poll

## Future Enhancements

1. **Batch Operations**: Support bulk device registration
2. **Webhook Notifications**: Poller notifies Web UI of status changes
3. **Caching**: Cache device status to reduce poller load
4. **Health Checks**: Periodic health checks between Web UI and poller
5. **Metrics**: Export integration metrics (success/failure rates)
6. **Rate Limiting**: Protect poller from excessive requests

## Troubleshooting

### Poller Connection Issues

**Symptom**: Device operations succeed but status is unavailable

**Check:**
1. Poller service is running: `docker ps | grep go-netmon`
2. Poller URL is correct: Check `--poller` flag
3. Network connectivity: `curl http://go-netmon:8080/health`
4. Check logs: `make logs-webui`

### Device Not Showing in Poller

**Symptom**: Device created in Web UI but not polling

**Check:**
1. Check Web UI logs for registration errors
2. Verify poller received the device: `curl http://go-netmon:8080/api/poller/devices`
3. Check device is enabled in poller
4. Verify MIBs/profiles are correctly assigned

### Status Returns 404

**Symptom**: GET /api/devices/{id}/status returns 404

**Possible Causes:**
1. Device not registered with poller
2. Device name mismatch between Web UI and poller
3. Poller service not running

**Solution:**
1. Update the device to re-register: `PUT /api/devices/{id}`
2. Check device name in both systems
3. Start poller service

## References

- Main Poller API: `/home/user/sources/go-netmon/internal/api/poller_control.go`
- Device Status Types: `/home/user/sources/go-netmon/internal/api/device_status.go`
- Web UI API: `/home/user/sources/go-netmon/internal/webui/api/`
- Storage Layer: `/home/user/sources/go-netmon/internal/webui/storage/`
