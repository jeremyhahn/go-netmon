#!/bin/bash
#
# Configuration Synchronization Example
#
# This script demonstrates how to use the configuration synchronization feature
# to centralize device configuration management in the WebUI.
#

set -e

echo "=== go-netmon Configuration Synchronization Example ==="
echo ""

# Example 1: Running poller without WebUI sync (traditional mode)
echo "Example 1: Traditional mode (local config.yaml)"
echo "-----------------------------------------------"
echo "Command:"
echo "  ./go-netmon"
echo ""
echo "Behavior:"
echo "  - Reads devices from config.yaml"
echo "  - No communication with WebUI"
echo "  - Fully standalone operation"
echo ""

# Example 2: Running poller with WebUI sync
echo "Example 2: WebUI sync mode (centralized config)"
echo "-----------------------------------------------"
echo "Command:"
echo "  export WEBUI_URL=http://webui:8080"
echo "  ./go-netmon"
echo ""
echo "Behavior:"
echo "  - Fetches devices from WebUI on startup"
echo "  - Retries with exponential backoff if WebUI unavailable"
echo "  - Falls back to local config.yaml if sync fails"
echo "  - Logs all sync operations"
echo ""

# Example 3: Docker Compose deployment
echo "Example 3: Docker Compose deployment"
echo "-------------------------------------"
cat << 'EOF'
# docker-compose.yml
version: '3.8'

services:
  webui:
    image: go-netmon-webui:latest
    ports:
      - "8080:8080"
    volumes:
      - webui-data:/app/data
      - ./mibs:/app/mibs
    environment:
      - PROMETHEUS_URL=http://prometheus:9090
      - POLLER_URL=http://poller:8080

  poller:
    image: go-netmon-poller:latest
    ports:
      - "9090:9090"
    environment:
      - WEBUI_URL=http://webui:8080        # Enable config sync
      - CONFIG_PATH=/app/config.yaml
    volumes:
      - ./config.yaml:/app/config.yaml
      - ./mibs:/app/mibs
    depends_on:
      - webui
      - prometheus

  prometheus:
    image: prom/prometheus:latest
    ports:
      - "9091:9090"
    volumes:
      - ./prometheus.yml:/etc/prometheus/prometheus.yml
      - prometheus-data:/prometheus
    command:
      - '--config.file=/etc/prometheus/prometheus.yml'
      - '--storage.tsdb.path=/prometheus'
      - '--web.enable-remote-write-receiver'

volumes:
  webui-data:
  prometheus-data:
EOF
echo ""

# Example 4: Configuration sync workflow
echo "Example 4: Configuration sync workflow"
echo "---------------------------------------"
echo "Step 1: Add devices in WebUI"
echo "  - Open http://localhost:8080"
echo "  - Navigate to Devices"
echo "  - Add device (name: router1, address: 203.0.113.1:161)"
echo "  - Assign MIB profiles (e.g., IF-MIB, SNMPv2-MIB)"
echo ""
echo "Step 2: Start/restart poller with sync enabled"
echo "  $ docker-compose up -d poller"
echo ""
echo "Step 3: Observe sync logs"
echo "  $ docker-compose logs -f poller"
echo ""
echo "Expected output:"
echo "  2025/10/25 10:00:00 WebUI configuration sync enabled: http://webui:8080"
echo "  2025/10/25 10:00:01 Successfully synchronized configuration: 1 devices, 2 MIB modules"
echo "  2025/10/25 10:00:01 Added device router1 (address: 203.0.113.1:161, profile: default)"
echo "  2025/10/25 10:00:01 Poller started successfully with 1 enabled devices"
echo ""

# Example 5: Testing configuration sync manually
echo "Example 5: Testing configuration sync manually"
echo "-----------------------------------------------"
echo "# Query the WebUI config endpoint"
echo 'curl -s http://localhost:8080/api/poller/config | jq .'
echo ""
echo "Expected response:"
cat << 'EOF'
{
  "devices": [
    {
      "name": "router1",
      "address": "203.0.113.1:161",
      "snmp_version": "2c",
      "community": "public",
      "poll_interval": 30,
      "enabled": true,
      "profile": "default",
      "modules": ["IF-MIB", "SNMPv2-MIB"],
      "tags": {
        "source": "webui",
        "device_id": "1",
        "created_at": "2025-10-25T10:00:00Z"
      }
    }
  ],
  "mib_modules": [
    {"name": "IF-MIB", "enabled": true, "oids": []},
    {"name": "SNMPv2-MIB", "enabled": true, "oids": []}
  ],
  "version": 1730000000
}
EOF
echo ""

# Example 6: Troubleshooting
echo "Example 6: Troubleshooting"
echo "--------------------------"
echo ""
echo "Problem: Poller not syncing"
echo "Solution:"
echo "  1. Verify WEBUI_URL is set:"
echo "     $ docker-compose exec poller printenv WEBUI_URL"
echo ""
echo "  2. Test WebUI connectivity:"
echo "     $ docker-compose exec poller curl http://webui:8080/api/poller/config"
echo ""
echo "  3. Check poller logs:"
echo "     $ docker-compose logs poller | grep 'sync'"
echo ""
echo ""

echo "Problem: Sync taking too long"
echo "Solution:"
echo "  - WebUI may be slow or database locked"
echo "  - Check WebUI logs: docker-compose logs webui"
echo "  - Verify WebUI database is not corrupted"
echo ""
echo ""

echo "Problem: Devices not polling after sync"
echo "Solution:"
echo "  - Verify devices are enabled in WebUI"
echo "  - Check SNMP connectivity:"
echo "     $ snmpwalk -v2c -c public 203.0.113.1 system"
echo "  - Review poller worker logs for errors"
echo ""

# Example 7: Production deployment considerations
echo "Example 7: Production deployment"
echo "---------------------------------"
echo ""
echo "Security:"
echo "  - Use HTTPS for WebUI (TLS termination via reverse proxy)"
echo "  - Restrict network access (firewall rules)"
echo "  - Use internal network for poller<->WebUI communication"
echo ""
echo "High Availability:"
echo "  - Multiple poller instances with different device sets"
echo "  - Load balancing via device distribution"
echo "  - WebUI with replicated database"
echo ""
echo "Monitoring:"
echo "  - Monitor sync success via logs"
echo "  - Alert on repeated sync failures"
echo "  - Track configuration version changes"
echo ""

echo ""
echo "=== End of Examples ==="
echo ""
echo "For more information, see:"
echo "  - Documentation: docs/CONFIG_SYNC.md"
echo "  - Implementation: internal/poller/config_sync.go"
echo "  - Tests: internal/poller/config_sync_test.go"
echo ""
