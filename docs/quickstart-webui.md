# Web UI Quick Start Guide

> **Status: REFERENCE** · Scope: go-netmon · Last verified: 2026-10-09

Get up and running with the go-netmon Web UI in minutes.

## Prerequisites

- Docker and Docker Compose installed
- Make installed
- Git (to clone the repository)

## Quick Start

### 1. Clone the Repository

```bash
git clone https://github.com/jeremyhahn/go-netmon.git
cd go-netmon
```

### 2. Start the Web UI

```bash
# Build and start Web UI with Prometheus
make run-webui
```

This will:
- Build the Web UI Docker image
- Start Prometheus (on host port 9091)
- Start Web UI (on host port 8082)

`make run-webui` starts the Web UI and Prometheus only; SNMP polling is not running. Use `make run` for the full stack.

### 3. Access the Web UI

Open your browser and navigate to:

```
http://localhost:8082
```

(The banner `make run-webui` prints says port 8081. That is stale: `docker-compose.yml` publishes the Web UI on 8082.)

You should see the go-netmon Web UI dashboard.

### 4. Import a MIB

1. Click on **MIBs** in the navigation
2. Click the **Import MIBs** button
3. Upload one or more `.mib`, `.txt` or `.my` files (e.g., SNMPv2-MIB)
4. Each MIB is parsed and stored

### 5. Add a Device

1. Click on **Devices** in the navigation
2. Click **Add device**
3. Fill in the form:
   - Name: `test-router`
   - IP Address: `203.0.113.1`
   - SNMP Version: `SNMPv2c`
   - Community: `public`
   - Port: `161`
4. Click **Save**

### 6. Create a Profile

1. Click on **Profiles** in the navigation
2. Click **Add profile**
3. Enter:
   - Name: `Router Profile`
   - Description: `Standard router monitoring`
4. Click **Save**

### 7. Assign MIBs to Profile

1. Click on the profile you just created
2. In the "Assigned MIBs" section, click **Assign MIB**
3. Select the MIBs you want to monitor
4. Confirm the assignment

### 8. Assign Profile to Device

1. Navigate back to **Devices**
2. Click on your device
3. In the "Assigned profiles" section, click **Assign profile**
4. Select your profile
5. Confirm the assignment

### 9. Generate Dashboard

1. In the device details view, find the "All applied MIBs" card
2. Click **Generate dashboard**
3. The UI opens a dashboard built from the MIBs applied to the device

### 10. View Real-time Metrics

1. Navigate to **Dashboard**
2. Select your device from the dropdown
3. Charts update automatically over the WebSocket (every 5 seconds by default)

Metrics only appear when the poller is running (see the full stack below).

## Full Stack

To run the complete go-netmon stack (poller + Prometheus + Grafana + Web UI):

```bash
make run
```

This starts:
- **go-netmon**: SNMP poller; API on port 9080, metrics on port 9100
- **Web UI**: Management interface on port 8082
- **Prometheus**: Metrics storage on port 9091
- **Grafana**: Visualization on port 3000 (admin/admin)

Access the services:
- Web UI: http://localhost:8082
- Grafana: http://localhost:3000
- Prometheus: http://localhost:9091

## Common Commands

```bash
# View Web UI logs
make logs-webui

# Restart Web UI
make restart-webui

# Stop Web UI
make stop-webui

# Stop all services
make stop

# View all logs
make logs
```

## Troubleshooting

### Web UI won't start

Check if port 8082 is already in use:

```bash
# Check what's using port 8082
lsof -i :8082

# Or use a different port by editing the webui ports mapping in docker-compose.yml
```

### Can't connect to Prometheus

Verify Prometheus is running:

```bash
# Check Prometheus container
docker ps | grep prometheus

# Check Prometheus logs
docker logs prometheus

# Test Prometheus endpoint
curl http://localhost:9091/-/healthy
```

### MIB import fails

Ensure:
1. MIB file is valid ASN.1 format
2. File size is under 10MB
3. Check Web UI logs for detailed error:
   ```bash
   make logs-webui
   ```

### No data in charts

Verify:
1. go-netmon is running: `docker ps | grep go-netmon`
2. Device is reachable: `docker exec go-netmon-webui ping <device-ip>`
3. SNMP is responding: `snmpwalk -v2c -c public <device-ip> system`
4. Metrics in Prometheus: http://localhost:9091/graph

## Next Steps

- Read the [full Web UI documentation](webui.md)
- Configure SNMP devices in your network
- Create custom device profiles
- Explore the REST API

## Support

For issues or questions:
- Check the [troubleshooting guide](webui.md#troubleshooting)
- View logs: `make logs-webui`
- Open an issue on the project's Gitea repository

## Clean Up

To stop and remove everything:

```bash
# Stop all containers
make stop

# Remove build artifacts
make clean
```

`make stop` stops all Docker services and cleans up the networks. `make clean` removes build artifacts and does not touch Docker or backups.
