# Configuration Synchronization

## Overview

The configuration synchronization feature allows the poller service to fetch its device and MIB configuration from the WebUI on startup. This enables centralized configuration management where the WebUI acts as the source of truth for monitoring configuration.

## Architecture

```
┌─────────────┐                    ┌─────────────┐
│             │   GET /api/        │             │
│   Poller    │   poller/config    │   WebUI     │
│   Service   │──────────────────▶ │   Service   │
│             │                    │             │
└─────────────┘                    └─────────────┘
       │                                  │
       │                                  │
       ▼                                  ▼
  Polls devices                   Stores config
  configured by                   in PebbleDB
  WebUI
```

## Components

### 1. WebUI API Endpoint

**Endpoint:** `GET /api/poller/config`

**Response Format:**
```json
{
  "devices": [
    {
      "name": "router1",
      "address": "203.0.113.1:161",
      "snmp_version": "2c",
      "community": "public",
      "poll_interval": 30,
      "enabled": true,
      "profile": "cisco",
      "modules": ["IF-MIB", "CISCO-IF-MIB"],
      "tags": {
        "source": "webui",
        "device_id": "1",
        "created_at": "2025-10-25T10:00:00Z"
      }
    }
  ],
  "mib_modules": [
    {
      "name": "IF-MIB",
      "enabled": true,
      "oids": []
    }
  ],
  "version": 1730000000
}
```

**Implementation:**
- File: `/home/user/sources/go-netmon/internal/webui/api/poller_config.go`
- Handler: `handleGetPollerConfig()`
- Fetches devices from WebUI storage
- Includes both direct MIB assignments and profile-based MIBs
- Returns formatted configuration suitable for poller consumption

### 2. Poller Configuration Sync Client

**Implementation:**
- File: `/home/user/sources/go-netmon/internal/poller/config_sync.go`
- Client: `ConfigSyncClient`

**Features:**
- HTTP client for fetching configuration from WebUI
- Exponential backoff retry mechanism (1s, 2s, 4s, 8s, 16s, max 30s)
- Context-aware for graceful cancellation
- Configurable timeout (default: 30 seconds)
- Maximum 5 retry attempts

**Key Functions:**

```go
// Create new sync client
func NewConfigSyncClient(baseURL string, timeout time.Duration) *ConfigSyncClient

// Fetch configuration with retry
func (c *ConfigSyncClient) SyncConfiguration(ctx context.Context) (*PollerConfigResponse, error)

// Apply synced configuration to poller
func ApplyConfiguration(ctx context.Context, p *Poller, syncedConfig *PollerConfigResponse) error
```

### 3. Poller Integration

**Implementation:**
- File: `/home/user/sources/go-netmon/cmd/go-netmon/main.go`
- Function: `syncConfigurationFromWebUI()`

**Environment Variable:**
```bash
WEBUI_URL=http://webui:8080
```

When `WEBUI_URL` is set, the poller will:
1. Fetch configuration from WebUI on startup
2. Apply the configuration before starting workers
3. Log sync status
4. Continue with local configuration if sync fails

## Usage

### Enable Configuration Sync

Set the `WEBUI_URL` environment variable when starting the poller:

```bash
# Docker Compose
environment:
  - WEBUI_URL=http://webui:8080

# Command Line
export WEBUI_URL=http://webui:8080
./go-netmon
```

### Startup Flow

1. **Poller starts** and detects `WEBUI_URL` environment variable
2. **MIB loader initializes** and loads MIB modules
3. **Prometheus exporter initializes**
4. **Configuration sync executes**:
   - Creates sync client with 30s timeout
   - Fetches config from WebUI with exponential backoff
   - Applies configuration to poller
   - Logs sync results
5. **Poller starts** with synchronized devices
6. **Workers begin polling** configured devices

### Logging

Configuration sync produces detailed logs:

```log
2025/10/25 10:00:00 go-netmon v0.1.0 - SNMP Monitoring Service
2025/10/25 10:00:00 WebUI configuration sync enabled: http://webui:8080
2025/10/25 10:00:00 Loaded configuration: monitoring 0 devices
2025/10/25 10:00:00 Initializing MIB loader...
2025/10/25 10:00:00 Synchronizing configuration from WebUI: http://webui:8080
2025/10/25 10:00:00 Starting configuration synchronization with WebUI at http://webui:8080
2025/10/25 10:00:01 Successfully synchronized configuration: 3 devices, 5 MIB modules (version: 1730000000)
2025/10/25 10:00:01 Applying synchronized configuration to poller...
2025/10/25 10:00:01 Added device router1 (address: 203.0.113.1:161, profile: cisco)
2025/10/25 10:00:01 Added device switch1 (address: 203.0.113.2:161, profile: default)
2025/10/25 10:00:01 Added device firewall1 (address: 203.0.113.3:161, profile: default)
2025/10/25 10:00:01 Successfully applied configuration: 3/3 devices enabled and added
2025/10/25 10:00:01 Successfully synchronized 3 devices from WebUI
2025/10/25 10:00:01 Poller started successfully with 3 enabled devices
```

### Error Handling

#### WebUI Unavailable

If WebUI is unavailable on startup, the poller will retry with exponential backoff:

```log
2025/10/25 10:00:00 Synchronizing configuration from WebUI: http://webui:8080
2025/10/25 10:00:00 Starting configuration synchronization with WebUI at http://webui:8080
2025/10/25 10:00:00 Configuration sync attempt 1/5 failed, retrying in 1s: request failed: dial tcp: connection refused
2025/10/25 10:00:01 Configuration sync attempt 2/5 failed, retrying in 2s: request failed: dial tcp: connection refused
2025/10/25 10:00:03 Successfully synchronized configuration: 3 devices, 5 MIB modules (version: 1730000000)
```

#### Max Retries Exceeded

If all retries fail, the poller continues with local configuration:

```log
2025/10/25 10:00:00 Synchronizing configuration from WebUI: http://webui:8080
2025/10/25 10:00:00 Starting configuration synchronization with WebUI at http://webui:8080
2025/10/25 10:00:00 Configuration sync attempt 1/5 failed, retrying in 1s: request failed: dial tcp: connection refused
...
2025/10/25 10:00:30 Warning: failed to sync configuration from WebUI: failed to fetch configuration: failed to sync configuration after 5 attempts: request failed: dial tcp: connection refused
2025/10/25 10:00:30 Continuing with local configuration...
2025/10/25 10:00:30 Poller started successfully with 0 enabled devices
```

## Configuration Priority

When both local configuration (config.yaml) and WebUI sync are available:

1. **Base settings** (Global, Prometheus, API, Metrics, MIB, Logging) come from local config
2. **Device configuration** is fetched from WebUI when `WEBUI_URL` is set
3. If WebUI sync fails, local device configuration is used as fallback

## Testing

Comprehensive tests cover all sync scenarios:

```bash
# Run all config sync tests
go test -v ./internal/poller -run "TestSyncConfiguration|TestApplyConfiguration"
```

**Test Coverage:**
- ✓ Successful configuration sync
- ✓ Retry on transient errors
- ✓ Context cancellation handling
- ✓ Maximum retries exceeded
- ✓ Configuration conversion
- ✓ Device application to poller
- ✓ Disabled device filtering
- ✓ Invalid device handling

## Security Considerations

1. **Network Communication**: HTTP traffic between poller and WebUI
   - Consider using HTTPS in production
   - Use internal network or VPN for communication

2. **Credentials**: SNMP community strings and credentials are transmitted
   - Ensure secure network communication
   - Consider encryption at rest in WebUI storage

3. **Authentication**: No authentication on `/api/poller/config` endpoint
   - Add API authentication if exposing WebUI externally
   - Use network-level security (firewall rules)

## Performance

- **Sync Time**: Typically < 1 second for configuration fetch
- **Retry Delays**: Exponential backoff up to 30 seconds
- **Timeout**: 30 second default per request
- **Impact**: Configuration sync only occurs on startup
- **Resources**: Minimal memory and CPU overhead

## Troubleshooting

### Problem: Poller not syncing configuration

**Check:**
1. Is `WEBUI_URL` environment variable set?
   ```bash
   echo $WEBUI_URL
   ```

2. Is WebUI service accessible?
   ```bash
   curl http://webui:8080/api/poller/config
   ```

3. Check poller logs for sync errors

### Problem: Devices not polling after sync

**Check:**
1. Verify devices are enabled in WebUI
2. Check device SNMP credentials
3. Verify network connectivity to devices
4. Review poller worker status logs

### Problem: Sync taking too long

**Check:**
1. Network latency to WebUI
2. WebUI database performance
3. Number of devices being synced
4. Consider increasing timeout if needed

## Future Enhancements

Potential improvements for configuration synchronization:

1. **Periodic Re-sync**: Poll WebUI for configuration changes at runtime
2. **WebSocket Updates**: Real-time configuration push from WebUI
3. **Delta Sync**: Only sync changed devices/MIBs
4. **Configuration Versioning**: Track and rollback configuration changes
5. **Multi-Poller Support**: Distribute devices across multiple pollers
6. **Health Checks**: Monitor WebUI availability continuously
7. **Metrics**: Expose sync metrics (last sync time, success rate, etc.)
8. **Authentication**: Add API key or JWT-based authentication

## Files

| File | Purpose |
|------|---------|
| `/home/user/sources/go-netmon/internal/poller/config_sync.go` | Configuration sync client implementation |
| `/home/user/sources/go-netmon/internal/poller/config_sync_test.go` | Comprehensive test suite |
| `/home/user/sources/go-netmon/internal/webui/api/poller_config.go` | WebUI API endpoint implementation |
| `/home/user/sources/go-netmon/cmd/go-netmon/main.go` | Poller integration (lines 63-68, 152-157, 293-312) |

## API Reference

### GET /api/poller/config

Retrieves the complete polling configuration for a poller service.

**Request:**
```http
GET /api/poller/config HTTP/1.1
Host: webui:8080
Accept: application/json
```

**Response:** `200 OK`
```json
{
  "devices": [
    {
      "name": "string",
      "address": "string",
      "snmp_version": "2c|3",
      "community": "string",
      "username": "string",
      "auth_protocol": "string",
      "auth_password": "string",
      "priv_protocol": "string",
      "priv_password": "string",
      "poll_interval": 30,
      "enabled": true,
      "profile": "string",
      "modules": ["string"],
      "tags": {
        "key": "value"
      }
    }
  ],
  "mib_modules": [
    {
      "name": "string",
      "enabled": true,
      "oids": ["string"]
    }
  ],
  "version": 1730000000
}
```

**Fields:**

- `devices`: Array of device configurations
  - `name`: Unique device identifier
  - `address`: IP address with port (e.g., "203.0.113.1:161")
  - `snmp_version`: SNMP version ("2c" or "3")
  - `community`: SNMPv2c community string
  - `poll_interval`: Polling interval in seconds
  - `enabled`: Whether device is enabled for polling
  - `profile`: MIB profile name
  - `modules`: List of MIB module names
  - `tags`: Key-value metadata tags

- `mib_modules`: Array of MIB module configurations
  - `name`: MIB module name
  - `enabled`: Whether module is enabled
  - `oids`: Optional list of specific OIDs

- `version`: Unix timestamp of configuration version

## Example Docker Compose

```yaml
version: '3.8'

services:
  webui:
    image: go-netmon-webui
    ports:
      - "8080:8080"
    volumes:
      - webui-data:/app/data
    environment:
      - PROMETHEUS_URL=http://prometheus:9090
      - POLLER_URL=http://poller:8080

  poller:
    image: go-netmon-poller
    ports:
      - "9090:9090"
    environment:
      - WEBUI_URL=http://webui:8080
      - CONFIG_PATH=/app/config.yaml
    volumes:
      - ./config.yaml:/app/config.yaml
    depends_on:
      - webui

volumes:
  webui-data:
```
