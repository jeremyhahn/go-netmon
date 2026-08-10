# Web UI Backend

Complete backend system for go-netmon Web UI with REST API, SQLite storage, and dashboard generation.

## Features

- **Device Management**: CRUD operations for SNMP devices
- **Profile System**: Reusable MIB profiles for device groups
- **MIB Management**: Import, store, and assign MIBs
- **Dashboard Generation**: Automatic Grafana dashboard creation
- **WebSocket Streaming**: Real-time metrics updates
- **SQLite Storage**: Lightweight, embedded database with WAL mode
- **RESTful API**: Clean, documented endpoints

## Quick Start

### Initialize Server

```go
import "github.com/jeremyhahn/go-netmon/internal/webui/api"

cfg := api.Config{
    QueryFunc:      prometheusClient.Query,
    MIBProvider:    mibProvider,
    DBPath:         "./data/webui.db",
    MIBStoragePath: "./mibs",
}

server, err := api.NewServer(cfg)
if err != nil {
    log.Fatal(err)
}
defer server.Close()

server.Start(context.Background(), ":8080")
```

### Basic Usage

```bash
# Create device
curl -X POST http://localhost:8080/api/devices \
  -H "Content-Type: application/json" \
  -d '{"name":"router-1","address":"203.0.113.1","snmp_version":"2c","community":"public"}'

# List devices
curl http://localhost:8080/api/devices?stored=true

# Import MIB
curl -X POST http://localhost:8080/api/mibs/import \
  -F "file=@IF-MIB.txt"

# Generate dashboard
curl http://localhost:8080/api/devices/1/dashboard
```

## Architecture

```
internal/webui/
├── api/                    # REST API handlers
│   ├── server.go          # Main server and routing
│   ├── device.go          # Device endpoints
│   ├── profile.go         # Profile endpoints
│   ├── mib_handler.go     # MIB management endpoints
│   ├── mib_import.go      # MIB import handling
│   ├── dashboard_gen.go   # Dashboard generation
│   └── prometheus.go      # Prometheus client
├── storage/                # Data persistence layer
│   ├── storage.go         # Database initialization
│   ├── device.go          # Device operations
│   ├── profile.go         # Profile operations
│   └── mib.go            # MIB operations
├── websocket/             # WebSocket streaming
│   └── hub.go            # WebSocket hub
└── static/               # Static files

docs/
├── webui-backend.md         # Complete documentation
└── webui-backend-example.md # Integration examples
```

## API Endpoints

### Devices
- `GET /api/devices?stored=true` - List all devices
- `POST /api/devices` - Create device
- `GET /api/devices/:id` - Get device
- `PUT /api/devices/:id` - Update device
- `DELETE /api/devices/:id` - Delete device

### Device MIBs
- `GET /api/devices/:id/mibs` - Get device MIBs
- `POST /api/devices/:id/mibs` - Assign MIB to device
- `DELETE /api/devices/:id/mibs/:mibId` - Remove MIB from device

### Profiles
- `GET /api/profiles` - List all profiles
- `POST /api/profiles` - Create profile
- `GET /api/profiles/:id` - Get profile
- `PUT /api/profiles/:id` - Update profile
- `DELETE /api/profiles/:id` - Delete profile

### Profile MIBs
- `GET /api/profiles/:id/mibs` - Get profile MIBs
- `POST /api/profiles/:id/mibs` - Assign MIB to profile
- `DELETE /api/profiles/:id/mibs/:mibId` - Remove MIB from profile

### Profile-Device Association
- `POST /api/devices/:deviceId/profiles/:profileId` - Assign profile to device
- `DELETE /api/devices/:deviceId/profiles/:profileId` - Remove profile from device

### MIBs
- `GET /api/mibs?stored=true` - List all MIBs
- `POST /api/mibs/import` - Import MIB file
- `GET /api/mibs/:id` - Get MIB details
- `DELETE /api/mibs/:id` - Delete MIB

### Dashboards
- `POST /api/dashboards/generate` - Generate custom dashboard
- `GET /api/devices/:id/dashboard` - Generate device dashboard

### Metrics
- `POST /api/metrics/query` - Execute Prometheus query

### WebSocket
- `WS /ws` - WebSocket connection for real-time metrics

## Database Schema

The system uses SQLite with the following tables:
- `devices` - SNMP device configurations
- `device_profiles` - Reusable monitoring profiles
- `mibs` - Imported MIB modules
- `profile_mibs` - Profile-MIB associations
- `device_profiles_assigned` - Device-Profile associations
- `device_mibs` - Direct Device-MIB associations

Foreign keys with CASCADE delete ensure referential integrity.

## Testing

```bash
# Run all Web UI tests
make test-webui

# Run storage tests
make test-webui-storage

# Run API tests
make test-webui-api

# Generate coverage report
make coverage-webui-storage
```

## Performance

- **Database**: SQLite with WAL mode, connection pooling (max 25)
- **Concurrency**: Thread-safe operations, context cancellation
- **API**: 15s read/write timeouts, prepared statements
- **WebSocket**: Buffered channels, automatic cleanup

## Security

- Input validation on all endpoints
- SQL injection prevention via prepared statements
- File upload size limits (10MB)
- Filename sanitization
- Foreign key constraints

## Configuration

```go
type Config struct {
    QueryFunc      ws.QueryFunc     // Prometheus query function
    MIBProvider    MIBProvider      // MIB information provider
    DBPath         string           // SQLite database path
    MIBStoragePath string           // MIB file storage path
}
```

## Error Handling

All endpoints return appropriate HTTP status codes:
- `200 OK` - Successful GET/PUT
- `201 Created` - Successful POST
- `204 No Content` - Successful DELETE
- `400 Bad Request` - Invalid input
- `404 Not Found` - Resource not found
- `500 Internal Server Error` - Server error

## Documentation

See `/docs` directory for:
- `webui-backend.md` - Complete API documentation
- `webui-backend-example.md` - Integration examples

## Dependencies

- `github.com/gorilla/mux` - HTTP routing
- `github.com/gorilla/websocket` - WebSocket support
- `github.com/mattn/go-sqlite3` - SQLite driver
- `github.com/jeremyhahn/go-netmon/internal/mib/parser` - MIB parsing

## License

Part of go-netmon project
