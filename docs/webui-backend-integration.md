# Integrating Web UI Backend into go-netmon

## Overview

This guide shows how to integrate the new Web UI backend into the existing go-netmon application.

## Changes Required

### 1. Update Main Application

Modify `cmd/go-netmon/main.go` to initialize the new Web UI backend:

```go
package main

import (
    "context"
    "log"
    "os"
    "os/signal"
    "syscall"

    "github.com/jeremyhahn/go-netmon/internal/config"
    "github.com/jeremyhahn/go-netmon/internal/webui/api"
    "github.com/jeremyhahn/go-netmon/internal/mib/parser"
    // ... other imports
)

func main() {
    // Load configuration
    cfg, err := config.Load("config.yaml")
    if err != nil {
        log.Fatalf("Failed to load config: %v", err)
    }

    // Initialize MIB parser
    mibParser := parser.New(cfg.MIBPath)
    if err := mibParser.Init(); err != nil {
        log.Fatalf("Failed to initialize MIB parser: %v", err)
    }
    defer mibParser.Close()

    // Create MIB provider
    mibProvider := &mibProviderImpl{parser: mibParser}

    // Initialize Prometheus client
    promClient := api.NewPrometheusClient(cfg.PrometheusURL)

    // Initialize Web UI backend
    webUIConfig := api.Config{
        QueryFunc:      promClient.Query,
        MIBProvider:    mibProvider,
        DBPath:         cfg.WebUI.DBPath,
        MIBStoragePath: cfg.MIBPath,
    }

    webUIServer, err := api.NewServer(webUIConfig)
    if err != nil {
        log.Fatalf("Failed to create Web UI server: %v", err)
    }
    defer webUIServer.Close()

    // Setup context with cancellation
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    // Handle shutdown signals
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

    // Start Web UI server
    go func() {
        log.Printf("Starting Web UI server on %s", cfg.WebUI.Address)
        if err := webUIServer.Start(ctx, cfg.WebUI.Address); err != nil {
            log.Printf("Web UI server error: %v", err)
        }
    }()

    // ... Start other services (SNMP poller, etc.)

    // Wait for shutdown signal
    <-sigChan
    log.Println("Shutting down...")
    cancel()
}
```

### 2. Implement MIB Provider

Create a file `cmd/go-netmon/mib_provider.go`:

```go
package main

import (
    "github.com/jeremyhahn/go-netmon/internal/mib"
    "github.com/jeremyhahn/go-netmon/internal/mib/parser"
    "github.com/jeremyhahn/go-netmon/internal/webui/api"
)

type mibProviderImpl struct {
    parser *parser.Parser
}

func (m *mibProviderImpl) ListMIBs() ([]api.MIBInfo, error) {
    modules := m.parser.GetModules()
    mibs := make([]api.MIBInfo, 0, len(modules))

    for _, moduleName := range modules {
        info, err := m.parser.LoadModule(moduleName)
        if err != nil {
            continue
        }

        mibs = append(mibs, api.MIBInfo{
            Name:        info.Name,
            Description: info.Description,
            ObjectCount: info.OIDCount,
        })
    }

    return mibs, nil
}

func (m *mibProviderImpl) GetMIB(name string) (*api.MIBDetail, error) {
    // Load module
    info, err := m.parser.LoadModule(name)
    if err != nil {
        return nil, err
    }

    // Parse all OIDs
    oids, err := m.parser.ParseModule(name)
    if err != nil {
        return nil, err
    }

    // Convert to API format
    objects := make([]api.MIBObject, 0, len(oids))
    for _, oid := range oids {
        if !oid.Pollable {
            continue
        }

        objects = append(objects, api.MIBObject{
            OID:         oid.OID,
            Name:        oid.Name,
            Type:        oid.Type,
            Description: oid.Description,
            Unit:        oid.Units,
        })
    }

    return &api.MIBDetail{
        Name:        info.Name,
        Description: info.Description,
        Objects:     objects,
    }, nil
}
```

### 3. Update Configuration

Add Web UI configuration to your `config.yaml`:

```yaml
# Existing configuration...
prometheus_url: "http://localhost:9090"
mib_path: "./mibs"

# New Web UI configuration
webui:
  address: ":8080"
  db_path: "./data/webui.db"

# ... rest of configuration
```

Update `internal/config/config.go` to include Web UI settings:

```go
package config

type Config struct {
    // Existing fields...
    PrometheusURL string `yaml:"prometheus_url"`
    MIBPath       string `yaml:"mib_path"`

    // New Web UI configuration
    WebUI WebUIConfig `yaml:"webui"`
}

type WebUIConfig struct {
    Address string `yaml:"address"`
    DBPath  string `yaml:"db_path"`
}
```

### 4. Update Docker Configuration

Update `Dockerfile` to ensure data directory exists:

```dockerfile
FROM golang:1.21 AS builder

WORKDIR /app
COPY . .
RUN go mod download
RUN CGO_ENABLED=1 go build -o go-netmon ./cmd/go-netmon

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y ca-certificates sqlite3 && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /app/go-netmon .
COPY --from=builder /app/mibs ./mibs

# Create data directory for database
RUN mkdir -p /app/data

EXPOSE 8080 9100
CMD ["./go-netmon"]
```

Update `docker-compose.yml`:

```yaml
version: '3.8'

services:
  go-netmon:
    build: .
    ports:
      - "8080:8080"  # Web UI API
      - "9100:9100"  # Metrics endpoint
    volumes:
      - ./data:/app/data           # Persist database
      - ./mibs:/app/mibs           # MIB files
      - ./config.yaml:/app/config.yaml
    environment:
      - CONFIG_PATH=/app/config.yaml
    depends_on:
      - prometheus

  prometheus:
    image: prom/prometheus:latest
    ports:
      - "9090:9090"
    volumes:
      - ./prometheus.yml:/etc/prometheus/prometheus.yml
      - prometheus-data:/prometheus
    command:
      - '--config.file=/etc/prometheus/prometheus.yml'
      - '--storage.tsdb.path=/prometheus'

  grafana:
    image: grafana/grafana:latest
    ports:
      - "3000:3000"
    volumes:
      - grafana-data:/var/lib/grafana
    environment:
      - GF_SECURITY_ADMIN_PASSWORD=admin
    depends_on:
      - prometheus

volumes:
  prometheus-data:
  grafana-data:
```

### 5. Testing the Integration

#### Start the Application

```bash
# Build and start services
docker-compose up --build

# Or run locally
go run cmd/go-netmon/main.go
```

#### Test API Endpoints

```bash
# Check health
curl http://localhost:8080/api/devices?stored=true

# Create a device
curl -X POST http://localhost:8080/api/devices \
  -H "Content-Type: application/json" \
  -d '{
    "name": "test-router",
    "address": "203.0.113.1",
    "snmp_version": "2c",
    "community": "public"
  }'

# Import a MIB
curl -X POST http://localhost:8080/api/mibs/import \
  -F "file=@./mibs/IF-MIB.txt"

# List imported MIBs
curl http://localhost:8080/api/mibs?stored=true

# Generate dashboard
curl http://localhost:8080/api/devices/1/dashboard
```

### 6. Frontend Integration Example

Create a simple HTML dashboard (`static/index.html`):

```html
<!DOCTYPE html>
<html>
<head>
    <title>go-netmon Dashboard</title>
    <style>
        body { font-family: Arial, sans-serif; margin: 20px; }
        .device { border: 1px solid #ccc; padding: 10px; margin: 10px 0; }
        button { margin: 5px; padding: 5px 10px; }
    </style>
</head>
<body>
    <h1>go-netmon Device Management</h1>

    <div id="devices"></div>

    <h2>Add Device</h2>
    <form id="addDeviceForm">
        <input type="text" id="name" placeholder="Name" required>
        <input type="text" id="address" placeholder="Address" required>
        <select id="version">
            <option value="2c">SNMP v2c</option>
            <option value="3">SNMP v3</option>
        </select>
        <input type="text" id="community" placeholder="Community" value="public">
        <button type="submit">Add Device</button>
    </form>

    <script>
        const API_URL = 'http://localhost:8080/api';

        async function loadDevices() {
            const response = await fetch(`${API_URL}/devices?stored=true`);
            const data = await response.json();

            const container = document.getElementById('devices');
            container.innerHTML = '<h2>Devices</h2>';

            data.devices.forEach(device => {
                const div = document.createElement('div');
                div.className = 'device';
                div.innerHTML = `
                    <strong>${device.name}</strong> - ${device.address}
                    <br>Version: ${device.snmp_version}, Port: ${device.port}
                    <br>
                    <button onclick="deleteDevice(${device.id})">Delete</button>
                    <button onclick="generateDashboard(${device.id})">Generate Dashboard</button>
                `;
                container.appendChild(div);
            });
        }

        document.getElementById('addDeviceForm').addEventListener('submit', async (e) => {
            e.preventDefault();

            const device = {
                name: document.getElementById('name').value,
                address: document.getElementById('address').value,
                snmp_version: document.getElementById('version').value,
                community: document.getElementById('community').value,
                port: 161
            };

            await fetch(`${API_URL}/devices`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(device)
            });

            e.target.reset();
            loadDevices();
        });

        async function deleteDevice(id) {
            if (confirm('Delete device?')) {
                await fetch(`${API_URL}/devices/${id}`, { method: 'DELETE' });
                loadDevices();
            }
        }

        async function generateDashboard(id) {
            const response = await fetch(`${API_URL}/devices/${id}/dashboard`);
            const dashboard = await response.json();
            console.log('Dashboard:', dashboard);
            alert('Dashboard generated! Check console for details.');
        }

        // Load devices on page load
        loadDevices();
    </script>
</body>
</html>
```

### 7. Complete Workflow Example

```bash
#!/bin/bash
# complete-workflow.sh

API="http://localhost:8080/api"

echo "1. Import MIBs..."
curl -X POST "$API/mibs/import" -F "file=@mibs/IF-MIB.txt"
curl -X POST "$API/mibs/import" -F "file=@mibs/SNMPv2-MIB.txt"

echo "2. Create device profile..."
PROFILE_ID=$(curl -s -X POST "$API/profiles" \
  -H "Content-Type: application/json" \
  -d '{"name":"standard-router","description":"Standard router monitoring"}' \
  | jq -r '.id')

echo "Profile ID: $PROFILE_ID"

echo "3. Get MIB IDs..."
IF_MIB_ID=$(curl -s "$API/mibs?stored=true" | jq -r '.mibs[] | select(.name=="IF-MIB") | .id')
SNMPv2_MIB_ID=$(curl -s "$API/mibs?stored=true" | jq -r '.mibs[] | select(.name=="SNMPv2-MIB") | .id')

echo "4. Assign MIBs to profile..."
curl -X POST "$API/profiles/$PROFILE_ID/mibs" \
  -H "Content-Type: application/json" \
  -d "{\"mib_id\":$IF_MIB_ID}"

curl -X POST "$API/profiles/$PROFILE_ID/mibs" \
  -H "Content-Type: application/json" \
  -d "{\"mib_id\":$SNMPv2_MIB_ID}"

echo "5. Create devices..."
for i in {1..3}; do
  DEVICE_ID=$(curl -s -X POST "$API/devices" \
    -H "Content-Type: application/json" \
    -d "{\"name\":\"router-$i\",\"address\":\"203.0.113.$i\",\"snmp_version\":\"2c\",\"community\":\"public\"}" \
    | jq -r '.id')

  echo "Created device router-$i (ID: $DEVICE_ID)"

  # Assign profile to device
  curl -X POST "$API/devices/$DEVICE_ID/profiles/$PROFILE_ID"

  # Generate dashboard
  curl -s "$API/devices/$DEVICE_ID/dashboard" > "dashboard-router-$i.json"
  echo "Generated dashboard for router-$i"
done

echo "Done!"
```

## Verification

After integration, verify:

1. Database is created: `ls -la data/webui.db`
2. API is accessible: `curl http://localhost:8080/api/devices?stored=true`
3. MIB import works: Test with a sample MIB file
4. Dashboard generation works: Check generated JSON
5. All tests pass: `make test-webui`

## Migration Notes

If you have existing device configurations:

1. Existing Prometheus-based device discovery still works (legacy endpoints)
2. New database-based device management is separate
3. You can gradually migrate devices to the new system
4. Both systems can coexist during transition

## Troubleshooting

### Database Permission Issues
```bash
chmod 755 data/
chmod 644 data/webui.db
```

### CGO Build Issues
```bash
CGO_ENABLED=1 go build ./cmd/go-netmon
```

### Port Conflicts
Ensure port 8080 is available or change in config:
```yaml
webui:
  address: ":8081"
```
