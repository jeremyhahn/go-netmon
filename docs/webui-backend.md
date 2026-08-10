# Web UI Backend System

## Overview

The Web UI backend provides a complete REST API and data persistence layer for managing SNMP monitoring devices, MIB profiles, and dashboard generation. Built on SQLite with comprehensive CRUD operations and relationship management.

## Architecture

### Components

1. **Storage Layer** (`internal/webui/storage/`)
   - SQLite database with WAL mode for concurrency
   - Foreign key constraints for referential integrity
   - Automatic migrations on startup
   - Connection pooling and timeout management

2. **API Layer** (`internal/webui/api/`)
   - RESTful endpoints with proper HTTP methods
   - JSON request/response handling
   - Input validation and error handling
   - Context propagation for cancellation

3. **MIB Integration** (`internal/webui/api/mib_import.go`)
   - File upload handling (multipart/form-data)
   - MIB parsing using existing parser
   - Metadata storage in database

4. **Dashboard Generation** (`internal/webui/api/dashboard_gen.go`)
   - Automatic Grafana dashboard creation
   - Panel generation based on MIB objects
   - Prometheus query generation

## Database Schema

### Tables

**devices**
```sql
CREATE TABLE devices (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT UNIQUE NOT NULL,
    address TEXT NOT NULL,
    snmp_version TEXT NOT NULL,
    community TEXT,
    port INTEGER DEFAULT 161,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

**device_profiles**
```sql
CREATE TABLE device_profiles (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT UNIQUE NOT NULL,
    description TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

**mibs**
```sql
CREATE TABLE mibs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT UNIQUE NOT NULL,
    file_path TEXT NOT NULL,
    description TEXT,
    object_count INTEGER,
    imported_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

**profile_mibs** (Many-to-Many)
```sql
CREATE TABLE profile_mibs (
    profile_id INTEGER,
    mib_id INTEGER,
    PRIMARY KEY (profile_id, mib_id),
    FOREIGN KEY (profile_id) REFERENCES device_profiles(id) ON DELETE CASCADE,
    FOREIGN KEY (mib_id) REFERENCES mibs(id) ON DELETE CASCADE
);
```

**device_profiles_assigned** (Many-to-Many)
```sql
CREATE TABLE device_profiles_assigned (
    device_id INTEGER,
    profile_id INTEGER,
    PRIMARY KEY (device_id, profile_id),
    FOREIGN KEY (device_id) REFERENCES devices(id) ON DELETE CASCADE,
    FOREIGN KEY (profile_id) REFERENCES device_profiles(id) ON DELETE CASCADE
);
```

**device_mibs** (Many-to-Many)
```sql
CREATE TABLE device_mibs (
    device_id INTEGER,
    mib_id INTEGER,
    PRIMARY KEY (device_id, mib_id),
    FOREIGN KEY (device_id) REFERENCES devices(id) ON DELETE CASCADE,
    FOREIGN KEY (mib_id) REFERENCES mibs(id) ON DELETE CASCADE
);
```

## REST API Endpoints

### Device Management

#### List All Devices
```
GET /api/devices?stored=true
```

**Response:**
```json
{
  "devices": [
    {
      "id": 1,
      "name": "router-1",
      "address": "203.0.113.1",
      "snmp_version": "2c",
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
  "snmp_version": "2c",
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
  "snmp_version": "2c",
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
  "snmp_version": "2c",
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

Returns all MIBs associated with the device (both direct and via profiles).

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
GET /api/mibs?stored=true
```

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

**Response:**
```json
{
  "dashboard": {
    "title": "Router Dashboard",
    "tags": ["snmp", "auto-generated"],
    "panels": [...]
  }
}
```

#### Generate Device Dashboard
```
GET /api/devices/{id}/dashboard
```

Automatically generates a dashboard using all MIBs assigned to the device.

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
        QueryFunc:      prometheusClient.Query,
        MIBProvider:    mibProvider,
        DBPath:         "./data/webui.db",
        MIBStoragePath: "./mibs",
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
curl -X POST http://localhost:8080/api/devices \
  -H "Content-Type: application/json" \
  -d '{
    "name": "router-1",
    "address": "203.0.113.1",
    "snmp_version": "2c",
    "community": "public"
  }'

# Create profile
curl -X POST http://localhost:8080/api/profiles \
  -H "Content-Type: application/json" \
  -d '{
    "name": "cisco-router",
    "description": "Cisco Router Profile"
  }'

# Assign profile to device
curl -X POST http://localhost:8080/api/devices/1/profiles/1
```

### Import and Assign MIB

```bash
# Import MIB
curl -X POST http://localhost:8080/api/mibs/import \
  -F "file=@IF-MIB.txt"

# Re-import it later, keeping its id and every assignment
curl -X POST "http://localhost:8080/api/mibs/import?replace=true" \
  -F "file=@IF-MIB.txt"

# Assign MIB to device
curl -X POST http://localhost:8080/api/devices/1/mibs \
  -H "Content-Type: application/json" \
  -d '{"mib_id": 1}'

# Generate dashboard
curl http://localhost:8080/api/devices/1/dashboard
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

### Test Coverage Goals
- Storage layer: 90%+
- API handlers: 85%+
- Integration tests: 80%+

## Error Handling

All endpoints return appropriate HTTP status codes:

- **200 OK** - Successful GET/PUT requests
- **201 Created** - Successful POST requests
- **204 No Content** - Successful DELETE requests
- **400 Bad Request** - Invalid input or missing required fields
- **404 Not Found** - Resource not found
- **500 Internal Server Error** - Server-side errors

Error responses include a message:
```json
{
  "error": "device not found"
}
```

## Performance Considerations

1. **Database Optimization**
   - Indexes on all foreign keys
   - WAL mode for concurrent reads/writes
   - Connection pooling (max 25 connections)
   - 5-minute connection lifetime

2. **API Performance**
   - 15-second read/write timeouts
   - Context cancellation support
   - Prepared statements for all queries

3. **Concurrency**
   - Thread-safe database operations
   - Atomic transactions for multi-step operations
   - No blocking operations in handlers

## Security Considerations

1. **Input Validation**
   - All inputs validated before database operations
   - SQL injection prevention via prepared statements
   - File upload size limits (10MB)

2. **File Upload Security**
   - Restricted upload directory
   - Filename sanitization
   - MIME type validation

3. **CORS**
   - Currently allows all origins (development mode)
   - Should be restricted in production

## Future Enhancements

1. Authentication and authorization
2. Rate limiting
3. Audit logging
4. Bulk operations
5. Search and filtering
6. Pagination for large result sets
7. WebSocket notifications for device state changes
8. Export/import configuration
