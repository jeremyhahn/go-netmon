# Web UI Backend System

> **Status: REFERENCE** · Scope: go-netmon · Last verified: 2026-10-09

## Overview

The Web UI backend provides a REST API and a persistence layer for managing SNMP monitoring devices, MIB profiles, and dashboards. It is served by the standalone `cmd/webui` binary and stores its records in an embedded PebbleDB key-value store (no SQL database).

This page covers the REST API and the server package. For running the service, the UI, the WebSocket protocol and the key layout, see [webui.md](webui.md). For the link to the poller, see [webui-poller-integration.md](webui-poller-integration.md).

## Architecture

### Components

1. **Storage Layer** (`internal/webui/storage/`)
   - PebbleDB key-value store, opened from the `-db` path (default `data/webui`)
   - Records are JSON values under per-entity key prefixes; relationships (profile MIBs, device MIBs, device profiles) are separate keys
   - The key layout is in [webui.md](webui.md#storage-layout)

2. **API Layer** (`internal/webui/api/`)
   - gorilla/mux routes under `/api`
   - JSON request/response handling
   - Input validation; errors are plain text (see Error Handling)
   - Context propagation for cancellation

3. **MIB Integration** (`internal/webui/api/mib_import.go`)
   - File upload handling (multipart/form-data, 10 MB limit)
   - MIB parsing, with the parsed objects stored beside the record

4. **Dashboard Generation** (`internal/webui/api/dashboard_gen.go`)
   - Builds a dashboard (panels with their PromQL) from the metric catalog in Prometheus
   - Not a Grafana JSON model; the web UI renders it

## REST API Endpoints

### Device Management

#### List All Devices
```
GET /api/devices
```

Each device also carries `poll_interval` (seconds; present only when the device overrides the global default), `effective_poll_interval` (what the poller uses), and, on an SNMPv3 device, `username`, `auth_protocol`, `auth_password`, `priv_protocol` and `priv_password`. Credentials are stored in clear text; see [security-posture.md](operations/security-posture.md).

**Response:**
```json
{
  "devices": [
    {
      "id": 1,
      "name": "router-1",
      "address": "203.0.113.1",
      "snmp_version": "v2c",
      "community": "public",
      "port": 161,
      "created_at": "2025-10-24T12:00:00Z"
    }
  ]
}
```

#### Create Device
```
POST /api/devices
Content-Type: application/json

{
  "name": "router-1",
  "address": "203.0.113.1",
  "snmp_version": "v2c",
  "community": "public",
  "port": 161
}
```

**Response:** 201 Created
```json
{
  "id": 1,
  "name": "router-1",
  "address": "203.0.113.1",
  "snmp_version": "v2c",
  "community": "public",
  "port": 161,
  "created_at": "2025-10-24T12:00:00Z"
}
```

#### Get Device
```
GET /api/devices/{id}
```

#### Update Device
```
PUT /api/devices/{id}
Content-Type: application/json

{
  "name": "router-1",
  "address": "203.0.113.100",
  "snmp_version": "v2c",
  "community": "private",
  "port": 161
}
```

#### Delete Device
```
DELETE /api/devices/{id}
```

**Response:** 204 No Content

### Device MIB Management

#### Get Device MIBs
```
GET /api/devices/{id}/mibs
```

Returns the MIBs assigned directly to the device (`direct_mibs`) and every MIB that applies to it, directly or through its profiles (`all_mibs`). Each entry is the MIB record plus its assignment state.

**Response:**
```json
{
  "direct_mibs": [ { "id": 1, "name": "IF-MIB", "...": "..." } ],
  "all_mibs": [ { "id": 1, "name": "IF-MIB", "...": "..." } ]
}
```

#### Assign MIB to Device
```
POST /api/devices/{id}/mibs
Content-Type: application/json

{
  "mib_id": 1
}
```

**Response:** 204 No Content

#### Remove MIB from Device
```
DELETE /api/devices/{id}/mibs/{mibId}
```

**Response:** 204 No Content

### Device Profile Management

#### List All Profiles
```
GET /api/profiles
```

**Response:**
```json
{
  "profiles": [
    {
      "id": 1,
      "name": "cisco-router",
      "description": "Standard Cisco Router Profile",
      "created_at": "2025-10-24T12:00:00Z"
    }
  ]
}
```

#### Create Profile
```
POST /api/profiles
Content-Type: application/json

{
  "name": "cisco-router",
  "description": "Standard Cisco Router Profile"
}
```

#### Get Profile
```
GET /api/profiles/{id}
```

#### Update Profile
```
PUT /api/profiles/{id}
Content-Type: application/json

{
  "name": "cisco-router",
  "description": "Updated description"
}
```

#### Delete Profile
```
DELETE /api/profiles/{id}
```

#### Get Profile MIBs
```
GET /api/profiles/{id}/mibs
```

#### Assign MIB to Profile
```
POST /api/profiles/{id}/mibs
Content-Type: application/json

{
  "mib_id": 1
}
```

#### Remove MIB from Profile
```
DELETE /api/profiles/{id}/mibs/{mibId}
```

### Profile-Device Association

#### Assign Profile to Device
```
POST /api/devices/{deviceId}/profiles/{profileId}
```

#### Remove Profile from Device
```
DELETE /api/devices/{deviceId}/profiles/{profileId}
```

### MIB Management

#### List All MIBs
```
GET /api/mibs
```

Optional query parameters: `search` (case-folded text match), `tag`, `limit` and `offset` (non-negative integers), and `filter=available` with `device=<name>` to keep only modules that hold data in Prometheus (for that device, when given). A request with none of `search`, `tag`, `limit` or `offset` returns the plain `mibs` list; with any of them the response also carries a `page` block.

**Response:**
```json
{
  "mibs": [
    {
      "id": 1,
      "name": "IF-MIB",
      "file_path": "/mibs/IF-MIB",
      "description": "Interface MIB",
      "object_count": 150,
      "imported_at": "2025-10-24T12:00:00Z"
    }
  ]
}
```

#### Import MIB
```
POST /api/mibs/import
Content-Type: multipart/form-data

file: <MIB file>
```

**Response:** 201 Created
```json
{
  "mib": {
    "id": 1,
    "name": "IF-MIB",
    "file_path": "/mibs/uploaded/IF-MIB",
    "description": "Interface MIB",
    "object_count": 150,
    "imported_at": "2025-10-24T12:00:00Z"
  },
  "message": "Successfully imported MIB IF-MIB with 150 objects",
  "replaced": false
}
```

**Response:** 409 Conflict, when the module is already imported. The body names
the module, the id already holding it, and the way to refresh it:

```
MIB IF-MIB is already imported as id 1. To refresh its objects in place,
keeping its id, tags and every device and profile assignment, re-import it
with POST /api/mibs/import?replace=true (CLI: netmon mib import --update).
DELETE /api/mibs/1 first only if you mean to drop those assignments.
```

#### Re-import a MIB (refresh in place)
```
POST /api/mibs/import?replace=true
Content-Type: multipart/form-data

file: <MIB file>
```

Re-parses the module and rewrites only what comes from the file — its objects
and their metadata (type, syntax, units, display hint, enum values, access,
status, index columns), its object count and its description. Objects the module
no longer defines are removed.

The record keeps its `id` — device and profile assignments are keyed by it — its
name, its tags and its `imported_at`, and no assignment row is touched, so every
assignment's enable state and filters survive. A `refreshed_at` timestamp is
added, which is how a fleet-wide refresh is audited.

A module the server has not seen before is created, so a refresh across a whole
MIB tree does not fail on a module that was never imported.

A file that does not parse is rejected with 400 before anything is written; the
stored module, its objects and the file it points at are left unchanged.

**Response:** 200 OK
```json
{
  "mib": {
    "id": 1,
    "name": "IF-MIB",
    "file_path": "/mibs/uploaded/IF-MIB",
    "description": "Interface MIB",
    "object_count": 152,
    "imported_at": "2025-10-24T12:00:00Z",
    "refreshed_at": "2025-11-02T09:15:00Z"
  },
  "message": "Successfully updated MIB IF-MIB with 152 objects",
  "replaced": true
}
```

#### Get MIB Details
```
GET /api/mibs/{id}
```

**Response:**
```json
{
  "id": 1,
  "name": "IF-MIB",
  "file_path": "/mibs/IF-MIB",
  "description": "Interface MIB",
  "object_count": 150,
  "imported_at": "2025-10-24T12:00:00Z",
  "objects": [
    {
      "oid": "1.3.6.1.2.1.2.2.1.1",
      "name": "ifIndex",
      "type": "Integer32",
      "description": "A unique value for each interface",
      "unit": ""
    }
  ]
}
```

#### Delete MIB
```
DELETE /api/mibs/{id}
```

### Dashboard Generation

#### Generate Dashboard
```
POST /api/dashboards/generate
Content-Type: application/json

{
  "device_id": 1,
  "mib_ids": [1, 2, 3],
  "title": "Router Dashboard"
}
```

The device and every MIB id must exist (404 otherwise); `device_id` and at least one `mib_id` are required (400).

**Response:** the dashboard object itself (not wrapped):
```json
{
  "device": "router-1",
  "title": "Router Dashboard",
  "panels": [ { "id": "...", "title": "...", "type": "line" } ],
  "mibs": [],
  "warnings": []
}
```

Each panel carries its PromQL. `tables` (indexed panels grouped by index), `mib` (set when narrowed to one module) and `catalog_unavailable` (set when Prometheus refused the catalog query) appear when they apply. A device that reports nothing gets an empty dashboard, not an error.

#### Generate Device Dashboard
```
GET /api/devices/{id}/dashboard
```

Generates a dashboard from all MIBs that apply to the device, in the same shape. `GET /api/mibs/{id}/dashboard?device=<name>` narrows it to one module on one device (`device` is required; 400 without it, 404 for an unknown device).

## Usage Examples

### Initialize Server

```go
package main

import (
    "context"
    "log"

    "github.com/jeremyhahn/go-netmon/internal/webui/api"
)

func main() {
    cfg := api.Config{
        PromClient:      api.NewPrometheusClient("http://localhost:9091"),
        GoNetmonBaseURL: "http://localhost:9080",
        DBPath:          "./data/webui",
        MIBStoragePath:  "./mibs",
        // Assets, PollerClient, Logger and the rest are optional; a nil
        // Assets serves no UI. cmd/webui wires all of them.
    }

    server, err := api.NewServer(cfg)
    if err != nil {
        log.Fatalf("Failed to create server: %v", err)
    }
    defer server.Close()

    ctx := context.Background()
    if err := server.Start(ctx, ":8080"); err != nil {
        log.Fatalf("Server error: %v", err)
    }
}
```

### Create Device with Profile

```bash
# Create device
curl -X POST http://localhost:8082/api/devices \
  -H "Content-Type: application/json" \
  -d '{
    "name": "router-1",
    "address": "203.0.113.1",
    "snmp_version": "v2c",
    "community": "public"
  }'

# Create profile
curl -X POST http://localhost:8082/api/profiles \
  -H "Content-Type: application/json" \
  -d '{
    "name": "cisco-router",
    "description": "Cisco Router Profile"
  }'

# Assign profile to device
curl -X POST http://localhost:8082/api/devices/1/profiles/1
```

### Import and Assign MIB

```bash
# Import MIB
curl -X POST http://localhost:8082/api/mibs/import \
  -F "file=@IF-MIB.txt"

# Re-import it later, keeping its id and every assignment
curl -X POST "http://localhost:8082/api/mibs/import?replace=true" \
  -F "file=@IF-MIB.txt"

# Assign MIB to device
curl -X POST http://localhost:8082/api/devices/1/mibs \
  -H "Content-Type: application/json" \
  -d '{"mib_id": 1}'

# Generate dashboard
curl http://localhost:8082/api/devices/1/dashboard
```

## Testing

### Run Unit Tests
```bash
make test-webui-storage
```

### Run All Web UI Tests
```bash
make test-webui
```

### Generate Coverage Report
```bash
make coverage-webui-storage
```

The `test-webui-api`, `test-webui-client` and `test-webui-websocket` targets (and matching `coverage-webui-*` targets) run the other packages.

## Error Handling

Endpoints use these status codes:

- **200 OK** - Successful GET/PUT requests
- **201 Created** - Successful creation (device, profile, MIB import)
- **204 No Content** - Successful DELETE and assignment requests
- **400 Bad Request** - Invalid input or missing required fields
- **404 Not Found** - Resource not found
- **409 Conflict** - A MIB import of a module that is already imported
- **500 Internal Server Error** - Server-side errors

Errors are plain text, written with `http.Error`, not JSON:

```
HTTP/1.1 404 Not Found
Content-Type: text/plain; charset=utf-8

device not found
```

## Server Limits

- 15-second read and write timeouts (`ServerReadTimeout`, `ServerWriteTimeout` in `internal/webui/api/timeouts.go`); handler budgets are kept below the write timeout
- MIB uploads are capped at 10 MB; configuration bundle uploads have their own cap
- Request contexts are propagated to storage and upstream calls

## Security Considerations

- Request bodies are decoded and validated before they are stored
- The upload file name is reduced to its base name (`filepath.Base`)
- There is no authentication or authorization; see [operations/security-posture.md](operations/security-posture.md) for what that means for a deployment
- Device community strings and SNMPv3 passwords are stored in clear text

## Not Implemented

1. Authentication and authorization
2. Rate limiting
3. Audit logging
4. Bulk operations
