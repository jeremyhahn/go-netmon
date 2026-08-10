# Web UI Backend Integration Example

## Complete Integration Example

This example shows how to integrate the Web UI backend into your go-netmon application.

### Step 1: Initialize the Server

```go
package main

import (
    "context"
    "log"
    "os"
    "os/signal"
    "syscall"

    "github.com/jeremyhahn/go-netmon/internal/webui/api"
    "github.com/jeremyhahn/go-netmon/internal/mib/parser"
)

func main() {
    // Initialize MIB parser for the MIB provider
    mibParser := parser.New("./mibs")
    if err := mibParser.Init(); err != nil {
        log.Fatalf("Failed to initialize MIB parser: %v", err)
    }
    defer mibParser.Close()

    // Create MIB provider (you'll need to implement this interface)
    mibProvider := &SimpleMIBProvider{parser: mibParser}

    // Initialize Prometheus client
    promClient := api.NewPrometheusClient("http://localhost:9090")

    // Create server configuration
    cfg := api.Config{
        QueryFunc:      promClient.Query,
        MIBProvider:    mibProvider,
        DBPath:         "./data/webui.db",
        MIBStoragePath: "./mibs",
    }

    // Create server
    server, err := api.NewServer(cfg)
    if err != nil {
        log.Fatalf("Failed to create server: %v", err)
    }
    defer server.Close()

    // Setup graceful shutdown
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

    go func() {
        <-sigChan
        log.Println("Shutting down server...")
        cancel()
    }()

    // Start server
    log.Println("Starting Web UI backend on :8080")
    if err := server.Start(ctx, ":8080"); err != nil {
        log.Fatalf("Server error: %v", err)
    }
}
```

### Step 2: Implement MIB Provider (if needed)

```go
package main

import (
    "github.com/jeremyhahn/go-netmon/internal/mib/parser"
    "github.com/jeremyhahn/go-netmon/internal/webui/api"
)

type SimpleMIBProvider struct {
    parser *parser.Parser
}

func (p *SimpleMIBProvider) ListMIBs() ([]api.MIBInfo, error) {
    modules := p.parser.GetModules()
    mibs := make([]api.MIBInfo, 0, len(modules))

    for _, moduleName := range modules {
        info, err := p.parser.LoadModule(moduleName)
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

func (p *SimpleMIBProvider) GetMIB(name string) (*api.MIBDetail, error) {
    // Load module
    _, err := p.parser.LoadModule(name)
    if err != nil {
        return nil, err
    }

    // Parse all OIDs
    oids, err := p.parser.ParseModule(name)
    if err != nil {
        return nil, err
    }

    // Convert to MIBObjects
    objects := make([]api.MIBObject, 0, len(oids))
    for _, oid := range oids {
        objects = append(objects, api.MIBObject{
            OID:         oid.OID,
            Name:        oid.Name,
            Type:        oid.Type,
            Description: oid.Description,
            Unit:        oid.Units,
        })
    }

    return &api.MIBDetail{
        Name:        name,
        Description: "MIB module " + name,
        Objects:     objects,
    }, nil
}
```

### Step 3: API Usage Examples

#### Create a Device Profile Workflow

```bash
#!/bin/bash

API_URL="http://localhost:8080/api"

# 1. Import MIBs
echo "Importing IF-MIB..."
curl -X POST $API_URL/mibs/import \
  -F "file=@./mibs/IF-MIB.txt"

echo "Importing CISCO-MEMORY-POOL-MIB..."
curl -X POST $API_URL/mibs/import \
  -F "file=@./mibs/CISCO-MEMORY-POOL-MIB.txt"

# 2. Create device profile
echo "Creating Cisco Router profile..."
PROFILE_ID=$(curl -s -X POST $API_URL/profiles \
  -H "Content-Type: application/json" \
  -d '{
    "name": "cisco-router",
    "description": "Standard Cisco Router monitoring profile"
  }' | jq -r '.id')

echo "Profile ID: $PROFILE_ID"

# 3. Get MIB IDs
IF_MIB_ID=$(curl -s "$API_URL/mibs?stored=true" | jq -r '.mibs[] | select(.name=="IF-MIB") | .id')
CISCO_MIB_ID=$(curl -s "$API_URL/mibs?stored=true" | jq -r '.mibs[] | select(.name=="CISCO-MEMORY-POOL-MIB") | .id')

# 4. Assign MIBs to profile
echo "Assigning MIBs to profile..."
curl -X POST "$API_URL/profiles/$PROFILE_ID/mibs" \
  -H "Content-Type: application/json" \
  -d "{\"mib_id\": $IF_MIB_ID}"

curl -X POST "$API_URL/profiles/$PROFILE_ID/mibs" \
  -H "Content-Type: application/json" \
  -d "{\"mib_id\": $CISCO_MIB_ID}"

# 5. Create device
echo "Creating device..."
DEVICE_ID=$(curl -s -X POST $API_URL/devices \
  -H "Content-Type: application/json" \
  -d '{
    "name": "core-router-1",
    "address": "203.0.113.1",
    "snmp_version": "2c",
    "community": "public",
    "port": 161
  }' | jq -r '.id')

echo "Device ID: $DEVICE_ID"

# 6. Assign profile to device
echo "Assigning profile to device..."
curl -X POST "$API_URL/devices/$DEVICE_ID/profiles/$PROFILE_ID"

# 7. Generate dashboard
echo "Generating dashboard..."
curl -s "$API_URL/devices/$DEVICE_ID/dashboard" | jq '.' > dashboard.json

echo "Dashboard saved to dashboard.json"
```

#### Query Device Information

```bash
#!/bin/bash

API_URL="http://localhost:8080/api"
DEVICE_ID=1

# Get device details
echo "Device Details:"
curl -s "$API_URL/devices/$DEVICE_ID" | jq '.'

# Get device MIBs (includes MIBs from profiles)
echo "Device MIBs:"
curl -s "$API_URL/devices/$DEVICE_ID/mibs" | jq '.mibs[] | {name, object_count}'

# Get device profiles
echo "Device Profiles:"
curl -s "$API_URL/devices/$DEVICE_ID" | jq '.'
```

#### Bulk Device Creation

```go
package main

import (
    "bytes"
    "encoding/json"
    "fmt"
    "net/http"
)

type Device struct {
    Name        string `json:"name"`
    Address     string `json:"address"`
    SNMPVersion string `json:"snmp_version"`
    Community   string `json:"community"`
    Port        int    `json:"port"`
}

func createDevices(apiURL string, devices []Device) error {
    for _, device := range devices {
        data, err := json.Marshal(device)
        if err != nil {
            return fmt.Errorf("failed to marshal device: %w", err)
        }

        resp, err := http.Post(
            apiURL+"/api/devices",
            "application/json",
            bytes.NewBuffer(data),
        )
        if err != nil {
            return fmt.Errorf("failed to create device %s: %w", device.Name, err)
        }
        resp.Body.Close()

        if resp.StatusCode != http.StatusCreated {
            return fmt.Errorf("failed to create device %s: status %d", device.Name, resp.StatusCode)
        }

        fmt.Printf("Created device: %s\n", device.Name)
    }

    return nil
}

func main() {
    devices := []Device{
        {Name: "router-1", Address: "203.0.113.1", SNMPVersion: "2c", Community: "public", Port: 161},
        {Name: "router-2", Address: "203.0.113.2", SNMPVersion: "2c", Community: "public", Port: 161},
        {Name: "switch-1", Address: "203.0.113.10", SNMPVersion: "2c", Community: "public", Port: 161},
    }

    if err := createDevices("http://localhost:8080", devices); err != nil {
        fmt.Printf("Error: %v\n", err)
    }
}
```

### Step 4: Frontend Integration

Example JavaScript/TypeScript integration:

```typescript
// Device Management API Client
class DeviceAPI {
    private baseURL: string;

    constructor(baseURL: string) {
        this.baseURL = baseURL;
    }

    async listDevices(): Promise<Device[]> {
        const response = await fetch(`${this.baseURL}/api/devices?stored=true`);
        const data = await response.json();
        return data.devices;
    }

    async createDevice(device: CreateDeviceRequest): Promise<Device> {
        const response = await fetch(`${this.baseURL}/api/devices`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(device),
        });
        return await response.json();
    }

    async getDevice(id: number): Promise<Device> {
        const response = await fetch(`${this.baseURL}/api/devices/${id}`);
        return await response.json();
    }

    async updateDevice(id: number, device: UpdateDeviceRequest): Promise<Device> {
        const response = await fetch(`${this.baseURL}/api/devices/${id}`, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(device),
        });
        return await response.json();
    }

    async deleteDevice(id: number): Promise<void> {
        await fetch(`${this.baseURL}/api/devices/${id}`, {
            method: 'DELETE',
        });
    }

    async getDeviceMIBs(id: number): Promise<MIB[]> {
        const response = await fetch(`${this.baseURL}/api/devices/${id}/mibs`);
        const data = await response.json();
        return data.mibs;
    }

    async assignMIBToDevice(deviceId: number, mibId: number): Promise<void> {
        await fetch(`${this.baseURL}/api/devices/${deviceId}/mibs`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ mib_id: mibId }),
        });
    }

    async generateDashboard(deviceId: number): Promise<Dashboard> {
        const response = await fetch(
            `${this.baseURL}/api/devices/${deviceId}/dashboard`
        );
        return await response.json();
    }
}

// Usage example
const api = new DeviceAPI('http://localhost:8080');

// Create device
const device = await api.createDevice({
    name: 'router-1',
    address: '203.0.113.1',
    snmp_version: '2c',
    community: 'public',
    port: 161,
});

// Assign MIB
await api.assignMIBToDevice(device.id, 1);

// Generate dashboard
const dashboard = await api.generateDashboard(device.id);
```

### Step 5: Docker Integration

Update your `docker-compose.yml`:

```yaml
services:
  go-netmon:
    build: .
    ports:
      - "8080:8080"  # Web UI API
      - "9100:9100"  # Metrics
    volumes:
      - ./data:/app/data
      - ./mibs:/app/mibs
    environment:
      - WEBUI_DB_PATH=/app/data/webui.db
      - MIB_STORAGE_PATH=/app/mibs
      - PROMETHEUS_URL=http://prometheus:9090
```

### Testing the Integration

```bash
# Run all Web UI tests
make test-webui

# Run storage tests with coverage
make coverage-webui-storage

# Run the application
go run cmd/go-netmon/main.go

# Test API endpoints
curl http://localhost:8080/api/devices?stored=true
curl http://localhost:8080/api/profiles
curl http://localhost:8080/api/mibs?stored=true
```

## Troubleshooting

### Database Locked Error
If you see "database is locked" errors:
- WAL mode is enabled by default for better concurrency
- Ensure only one process accesses the database
- Check file permissions on the database file

### MIB Import Fails
- Verify the MIB file format is correct
- Check that dependencies (imported MIBs) are available
- Ensure MIB storage path is writable

### Dashboard Generation Issues
- Verify MIBs are properly parsed
- Check that device has assigned MIBs
- Ensure Prometheus is accessible

## Performance Tips

1. Use profile-based MIB assignment for multiple devices
2. Import MIBs once and reuse across devices
3. Cache dashboard configurations
4. Use database indexes (automatically created)
5. Enable database connection pooling (default: 25 connections)
