# Configuration Synchronization

> **Status: REFERENCE** · Scope: go-netmon · Last verified: 2026-10-09

## Overview

The poller (`go-netmon`) can fetch its device and MIB configuration from the web
UI when it starts. The web UI is then the source of truth for what is monitored.
The sync runs once, at startup; changes made in the web UI afterwards reach a
running poller through the poller's control API (`syncDeviceWithPoller` in
`internal/webui/api/device.go`), not through this mechanism.

## Architecture

```
┌─────────────┐                    ┌─────────────┐
│             │   GET /api/        │             │
│   Poller    │   poller/config    │   WebUI     │
│   Service   │──────────────────▶ │   Service   │
│             │                    │             │
└─────────────┘                    └─────────────┘
       │                                  │
       ▼                                  ▼
  Polls devices                   Stores config
  configured by                   in PebbleDB
  WebUI
```

## Components

### 1. WebUI API endpoint

`GET /api/poller/config`, handled by `handleGetPollerConfig` in
`internal/webui/api/poller_config.go`. It lists every device in the web UI's
storage and every MIB the web UI knows, and stamps the response with the current
Unix time as `version`.

For each device, `buildPollerDeviceConfig`:

- reports the modules the device's assignments leave enabled (a paused MIB is
  not there), with per-module filters in `module_filters`;
- reports the first assigned profile's name as `profile`, or an empty string;
- appends the port to `address` unless it is 0 or 161;
- sends `poll_interval` as the device's own interval, or 0 when the device
  inherits. The poller applies `global.default_poll_interval` itself;
- sets `enabled` to `true` and `tags` to `{"source": "webui"}`. Tags become
  Prometheus labels, so only values that are stable for the life of the device
  belong in them.

Every MIB is sent with `enabled: true` and an empty `oids` list; the OIDs come
from the poller's own MIB parser.

### 2. Poller configuration sync client

`ConfigSyncClient` in `internal/poller/config_sync.go`:

- fetches the configuration over HTTP and decodes it into `ConfigResponse`;
- tries at most 5 times, with exponential backoff between attempts of 2s, 4s,
  8s and 16s (capped at 30s);
- stops at once when the context is canceled;
- uses a per-request timeout, 30 seconds when the caller passes 0.

```go
func NewConfigSyncClient(baseURL string, timeout time.Duration) *ConfigSyncClient
func (c *ConfigSyncClient) SyncConfiguration(ctx context.Context) (*ConfigResponse, error)
func ApplyConfiguration(ctx context.Context, p *Poller, syncedConfig *ConfigResponse) error
```

`ApplyConfiguration` loads each enabled global MIB module, then for each device:
skips it if `enabled` is false, converts it (`snmp_version` `v2c`/`v3` becomes
`2c`/`3`), validates it, and adds it to the poller. A device that fails
validation or cannot be added is logged and skipped; the rest continue.

### 3. Poller integration

`syncConfigurationFromWebUI` in `cmd/go-netmon/main.go`, enabled by an
environment variable:

```bash
WEBUI_URL=http://webui:8082
```

When `WEBUI_URL` is set, the poller fetches the configuration after it has
loaded its MIB loader, exporter and logger and before it starts its workers. If
the sync fails it logs a warning and continues with the devices in its local
configuration.

## Usage

Set `WEBUI_URL` when starting the poller:

```bash
# Docker Compose
environment:
  - WEBUI_URL=http://webui:8082

# Command line
export WEBUI_URL=http://webui:8082
./go-netmon
```

### Startup flow

1. The poller reads `WEBUI_URL`, loads its configuration, and initializes the
   MIB loader and the Prometheus exporter.
2. It builds the logger and the poller.
3. The configuration sync runs: fetch with backoff, then apply.
4. The poller starts.

### Logging

A successful sync logs (timestamps trimmed):

```log
go-netmon v... - SNMP Monitoring Service
WebUI configuration sync enabled: http://webui:8082
Loaded configuration: monitoring 0 devices
Initializing MIB loader...
Synchronizing configuration from WebUI: http://webui:8082
Starting configuration synchronization with WebUI at http://webui:8082
Successfully synchronized configuration: 3 devices, 5 MIB modules (version: 1730000000)
Applying synchronized configuration to poller...
Added device router1 (address: 203.0.113.1:161, profile: cisco)
Successfully applied configuration: 3/3 devices enabled and added
Successfully synchronized 3 devices from WebUI
Poller started successfully with 3 enabled devices
```

### Error handling

#### WebUI unavailable

The poller retries with exponential backoff:

```log
Starting configuration synchronization with WebUI at http://webui:8082
Configuration sync attempt 1/5 failed, retrying in 2s: request failed: dial tcp: connection refused
Configuration sync attempt 2/5 failed, retrying in 4s: request failed: dial tcp: connection refused
Successfully synchronized configuration: 3 devices, 5 MIB modules (version: 1730000000)
```

#### Max retries exceeded

After 5 failed attempts (30 seconds of waiting in total, plus the requests
themselves) the poller continues with its local configuration:

```log
Warning: failed to sync configuration from WebUI: failed to fetch configuration: failed to sync configuration after 5 attempts: request failed: dial tcp: connection refused
Continuing with local configuration...
```

## Configuration priority

1. **Base settings** (Global, Prometheus, API, Metrics, MIB, Logging) come from
   the local configuration (`ConfigResponse.ToConfig` copies them across).
2. **Devices and MIB modules** come from the web UI when `WEBUI_URL` is set.
3. If the sync fails, the devices in the local configuration are used.

## Testing

```bash
make test-poller
```

Tests are in `internal/poller/config_sync_test.go` and cover: client defaults,
a successful sync, retry on error, context cancellation, retries exhausted,
conversion to `config.Config`, applying devices, skipping disabled devices and
rejecting an invalid device.

## Security considerations

- Traffic between the poller and the web UI is plain HTTP, and the response
  carries SNMP community strings and SNMPv3 credentials in clear text.
- `/api/poller/config` has no authentication, like the rest of the web UI's
  API. The network is the access control; see
  [operations/security-posture.md](operations/security-posture.md).

## Troubleshooting

### The poller is not syncing

1. Is `WEBUI_URL` set in the poller's environment?
2. Is the web UI reachable from the poller?
   ```bash
   curl http://webui:8082/api/poller/config
   ```
3. Look for `Configuration sync attempt` lines in the poller's log.

### Devices are not polling after the sync

1. Check the device's SNMP credentials.
2. Check network connectivity from the poller to the device.
3. Look for `Warning: invalid device configuration` or `Warning: failed to add
   device` lines from `ApplyConfiguration`.

## Files

| File | Purpose |
|------|---------|
| `internal/poller/config_sync.go` | Sync client, `ConfigResponse`, `ApplyConfiguration` |
| `internal/poller/config_sync_test.go` | Tests |
| `internal/webui/api/poller_config.go` | The `/api/poller/config` handler |
| `cmd/go-netmon/main.go` | `WEBUI_URL` handling and `syncConfigurationFromWebUI` |

## API reference

### GET /api/poller/config

**Response:** `200 OK`

```json
{
  "devices": [
    {
      "name": "router1",
      "address": "203.0.113.1",
      "snmp_version": "v2c",
      "community": "public",
      "poll_interval": 30,
      "enabled": true,
      "profile": "cisco",
      "modules": ["IF-MIB", "CISCO-IF-MIB"],
      "module_filters": {
        "IF-MIB": { "include_names": ["^if"], "max_oids": 100 }
      },
      "tags": { "source": "webui" }
    }
  ],
  "mib_modules": [
    { "name": "IF-MIB", "enabled": true }
  ],
  "version": 1730000000
}
```

**Device fields:**

- `name`: unique device name
- `address`: host, with `:port` appended unless the port is 161
- `snmp_version`: the web UI's value (`v1`, `v2c` or `v3`); the poller converts `v2c`/`v3` to `2c`/`3`
- `community`: SNMP v1/v2c community string
- `username`, `auth_protocol`, `auth_password`, `priv_protocol`, `priv_password`: SNMPv3 credentials, omitted when empty
- `poll_interval`: seconds; 0 means "use `global.default_poll_interval`"
- `enabled`: always `true` from the web UI
- `profile`: name of the first assigned MIB profile, or empty
- `modules`: MIB modules the device polls
- `module_filters`: per-module `include_oids`, `exclude_oids`, `include_names`, `exclude_names` and `max_oids`; a module with no entry is polled in full
- `tags`: metadata labels (`source: webui`)

**MIB module fields:** `name`, `enabled` (always `true`) and `oids` (omitted when empty).

**`version`:** Unix time at which the response was built.

On a storage error listing devices the endpoint returns `500`.
