# Web UI Quick Start Guide

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
- Start Prometheus (on port 9091)
- Start Web UI (on port 8081)

### 3. Access the Web UI

Open your browser and navigate to:

```
http://localhost:8081
```

You should see the go-netmon Web UI dashboard.

### 4. Import a MIB

1. Click on **MIB Manager** in the navigation
2. Click **Import MIB** button
3. Upload a `.mib` or `.txt` file (e.g., SNMPv2-MIB)
4. The MIB will be parsed and stored

### 5. Add a Device

1. Click on **Devices** in the navigation
2. Click **+ Add Device**
3. Fill in the form:
   - Name: `test-router`
   - IP Address: `203.0.113.1`
   - SNMP Version: `v2c`
   - Community: `public`
   - Port: `161`
4. Click **Save**

### 6. Create a Profile

1. Click on **Profiles** in the navigation
2. Click **+ Add Profile**
3. Enter:
   - Name: `Router Profile`
   - Description: `Standard router monitoring`
4. Click **Save**

### 7. Assign MIBs to Profile

1. Click on the profile you just created
2. In the "Assigned MIBs" section, click **+ Assign MIB**
3. Select the MIBs you want to monitor
4. Click **Assign**

### 8. Assign Profile to Device

1. Navigate back to **Devices**
2. Click on your device
3. In the "Assigned Profiles" section, click **+ Assign Profile**
4. Select your profile
5. Click **Assign**

### 9. Generate Dashboard

1. In the device details view, scroll to "All Applied MIBs"
2. Click **Generate Dashboard**
3. A Grafana-compatible dashboard JSON will be generated

### 10. View Real-time Metrics

1. Navigate to **Dashboard**
2. Select your device from the dropdown
3. Charts will update automatically every 5 seconds via WebSocket

## Full Stack

To run the complete go-netmon stack (poller + Prometheus + Grafana + Web UI):

```bash
make run
```

This starts:
- **go-netmon**: SNMP poller on port 8080
- **Web UI**: Management interface on port 8081
- **Prometheus**: Metrics storage on port 9091
- **Grafana**: Visualization on port 3000 (admin/admin)

Access the services:
- Web UI: http://localhost:8081
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

Check if port 8081 is already in use:

```bash
# Check what's using port 8081
lsof -i :8081

# Or use a different port by editing docker-compose.yml
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
- Set up alerting in Grafana
- Explore the REST API

## Support

For issues or questions:
- Check the [troubleshooting guide](webui.md#troubleshooting)
- View logs: `make logs-webui`
- Open an issue on GitHub

## Clean Up

To stop and remove everything:

```bash
# Stop all containers
make stop

# Remove all containers, volumes, and images
make clean
```

This will remove:
- All Docker containers
- All Docker volumes (database data will be lost)
- Built binaries
- Coverage reports
