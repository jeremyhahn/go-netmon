#!/bin/bash
set -e

echo "🚀 Starting go-netmon SNMP Monitoring System"
echo "=============================================="
echo ""

# Check if config.yaml exists
if [ ! -f config.yaml ]; then
    echo "⚠️  config.yaml not found!"
    echo "📝 Creating from example..."
    cp config.example.yaml config.yaml
    echo ""
    echo "✅ Created config.yaml"
    echo "⚠️  IMPORTANT: Edit config.yaml with your router details before continuing!"
    echo ""
    echo "Steps:"
    echo "1. Edit config.yaml and add your Cisco router IPs"
    echo "2. Update SNMP community strings or SNMPv3 credentials"
    echo "3. Run this script again: ./start.sh"
    exit 1
fi

echo "✅ Found config.yaml"
echo ""

# Check if docker is running
if ! docker info > /dev/null 2>&1; then
    echo "❌ Docker is not running!"
    echo "Please start Docker and try again."
    exit 1
fi

echo "✅ Docker is running"
echo ""

# Build and start services
echo "🔨 Building and starting services..."
# The image takes its Go modules from this host; see scripts/docker-vendor.sh.
./scripts/docker-vendor.sh
docker compose -f docker-compose.prod.yml up -d --build

echo ""
echo "⏳ Waiting for services to be ready..."
sleep 10

# Check service health
echo ""
echo "🔍 Checking service status..."
docker compose -f docker-compose.prod.yml ps

echo ""
echo "✅ Services started successfully!"
echo ""
echo "📊 Access your monitoring interfaces:"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "🎯 Prometheus:  http://localhost:9090"
echo "📈 Grafana:     http://localhost:3000  (admin/admin)"
echo "🔧 API:         http://localhost:8080"
echo "📡 Metrics:     http://localhost:9100/metrics"
echo ""
echo "📋 Useful commands:"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  View logs:     docker compose -f docker-compose.prod.yml logs -f"
echo "  Stop services: docker compose -f docker-compose.prod.yml down"
echo "  Restart:       docker compose -f docker-compose.prod.yml restart go-netmon"
echo ""
echo "📖 For detailed setup guide, see QUICKSTART.md"
echo ""
