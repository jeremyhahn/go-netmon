# go-netmon System Architecture

## Overview

go-netmon is a comprehensive SNMP network monitoring solution that provides real-time device monitoring, MIB parsing, metric collection, and dashboard visualization.

## Component Diagram

```
                                    External Network
                                          │
                                          ▼
                    ┌─────────────────────────────────────────────┐
                    │               SNMP Devices                   │
                    │     (Routers, Switches, Firewalls)          │
                    └──────────────────┬──────────────────────────┘
                                       │ SNMP v2c/v3
                                       │ (UDP 161)
                                       ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│                           Docker Network (netmon-network)                     │
│                                                                               │
│  ┌────────────────────────────────────────────────────────────────────────┐  │
│  │                          go-netmon Container                            │  │
│  │                                                                         │  │
│  │  ┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐    │  │
│  │  │   SNMP Poller   │    │  MIB Loader     │    │  Config Manager │    │  │
│  │  │                 │◄───│                 │◄───│                 │    │  │
│  │  │ • Device polling│    │ • gosmi parser  │    │ • YAML config   │    │  │
│  │  │ • Worker pool   │    │ • Module loading│    │ • Device list   │    │  │
│  │  │ • Connection    │    │ • OID metadata  │    │ • Poll intervals│    │  │
│  │  │   pooling       │    │                 │    │                 │    │  │
│  │  └────────┬────────┘    └─────────────────┘    └─────────────────┘    │  │
│  │           │                      ▲                                     │  │
│  │           │                      │                                     │  │
│  │           ▼                      │                                     │  │
│  │  ┌─────────────────┐    ┌─────────────────┐                           │  │
│  │  │ Metrics Exporter│    │   MIB Storage   │                           │  │
│  │  │                 │    │   (PebbleDB)    │                           │  │
│  │  │ • Remote write  │    │                 │                           │  │
│  │  │ • Batching      │    │ • OID metadata  │                           │  │
│  │  │ • Compression   │    │ • Module info   │                           │  │
│  │  │ • Table index   │    │ • Table info    │                           │  │
│  │  │   cache         │    │                 │                           │  │
│  │  └────────┬────────┘    └─────────────────┘                           │  │
│  │           │                                                            │  │
│  │  ┌────────┴────────┐    ┌─────────────────┐                           │  │
│  │  │   API Server    │    │  OID Learner    │                           │  │
│  │  │   (:9080)       │    │                 │                           │  │
│  │  │                 │    │ • Dynamic table │                           │  │
│  │  │ • Device CRUD   │    │   discovery     │                           │  │
│  │  │ • Status API    │    │ • Index learning│                           │  │
│  │  │ • Dashboard gen │    │                 │                           │  │
│  │  └─────────────────┘    └─────────────────┘                           │  │
│  │                                                                         │  │
│  │  Exposed Ports: 9080 (API), 9090 (Metrics)                             │  │
│  └────────────────────────────────────────────────────────────────────────┘  │
│                    │                                                          │
│                    │ remote_write                                            │
│                    ▼                                                          │
│  ┌────────────────────────────────────────────────────────────────────────┐  │
│  │                       Prometheus Container                              │  │
│  │                                                                         │  │
│  │  ┌─────────────────────────────────────────────────────────────────┐  │  │
│  │  │                    Prometheus Server (:9090)                     │  │  │
│  │  │                                                                   │  │  │
│  │  │  • Remote write receiver                                         │  │  │
│  │  │  • Time series storage (TSDB)                                    │  │  │
│  │  │  • PromQL query engine                                           │  │  │
│  │  │  • 15-day retention                                              │  │  │
│  │  │                                                                   │  │  │
│  │  └─────────────────────────────────────────────────────────────────┘  │  │
│  │                                                                         │  │
│  │  Exposed Ports: 9091 (mapped from 9090)                                │  │
│  └────────────────────────────────────────────────────────────────────────┘  │
│                    │                                                          │
│                    │ PromQL queries                                          │
│                    ▼                                                          │
│  ┌────────────────────────────────────────────────────────────────────────┐  │
│  │                         Grafana Container                               │  │
│  │                                                                         │  │
│  │  ┌─────────────────────────────────────────────────────────────────┐  │  │
│  │  │                    Grafana Server (:3000)                        │  │  │
│  │  │                                                                   │  │  │
│  │  │  • Dashboard visualization                                       │  │  │
│  │  │  • Auto-provisioned dashboards                                   │  │  │
│  │  │  • Prometheus data source                                        │  │  │
│  │  │  • Default login: admin/admin                                    │  │  │
│  │  │                                                                   │  │  │
│  │  └─────────────────────────────────────────────────────────────────┘  │  │
│  │                                                                         │  │
│  │  Exposed Ports: 3000                                                   │  │
│  └────────────────────────────────────────────────────────────────────────┘  │
│                                                                               │
│  ┌────────────────────────────────────────────────────────────────────────┐  │
│  │                         WebUI Container                                 │  │
│  │                                                                         │  │
│  │  ┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐    │  │
│  │  │   HTTP Server   │    │  MIB Loader     │    │   Dashboard     │    │  │
│  │  │    (:8080)      │    │                 │    │   Generator     │    │  │
│  │  │                 │    │ • gosmi parser  │    │                 │    │  │
│  │  │ • Static files  │    │ • MIB import    │    │ • Widget specs  │    │  │
│  │  │ • REST API      │    │ • Parse/store   │    │ • Chart configs │    │  │
│  │  │ • WebSocket     │    │                 │    │                 │    │  │
│  │  └─────────────────┘    └─────────────────┘    └─────────────────┘    │  │
│  │           │                                                            │  │
│  │           │                                                            │  │
│  │  ┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐    │  │
│  │  │  Device Storage │    │   MIB Storage   │    │ Prometheus      │    │  │
│  │  │   (PebbleDB)    │    │   (PebbleDB)    │    │ Client          │    │  │
│  │  │                 │    │                 │    │                 │    │  │
│  │  │ • Device CRUD   │    │ • Shared with   │    │ • Query metrics │    │  │
│  │  │ • Profiles      │    │   go-netmon     │    │ • Real-time     │    │  │
│  │  │ • MIB mappings  │    │                 │    │   updates       │    │  │
│  │  └─────────────────┘    └─────────────────┘    └─────────────────┘    │  │
│  │                                                                         │  │
│  │  Exposed Ports: 8081 (mapped from 8080)                                │  │
│  └────────────────────────────────────────────────────────────────────────┘  │
│                                                                               │
│  ┌────────────────────────────────────────────────────────────────────────┐  │
│  │                         Shared Volumes                                  │  │
│  │                                                                         │  │
│  │  go-netmon-data    - MIB storage shared between go-netmon and WebUI   │  │
│  │  webui-data        - WebUI device/profile database                     │  │
│  │  prometheus-data   - Time series data storage                          │  │
│  │  grafana-data      - Grafana dashboards and settings                   │  │
│  │                                                                         │  │
│  └────────────────────────────────────────────────────────────────────────┘  │
│                                                                               │
└──────────────────────────────────────────────────────────────────────────────┘
                                       │
                                       ▼
                    ┌─────────────────────────────────────────────┐
                    │                  Browser                     │
                    │                                              │
                    │  http://localhost:8081  - WebUI Dashboard   │
                    │  http://localhost:3000  - Grafana           │
                    │  http://localhost:9091  - Prometheus        │
                    │                                              │
                    └─────────────────────────────────────────────┘
```

## Data Flow

### 1. Configuration Flow

```
config.yaml ──► Config Manager ──► SNMP Poller
                     │
                     ▼
                MIB Loader ──► MIB Storage (PebbleDB)
                     │
                     ▼
               gosmi Parser ──► Parse MIB files
```

### 2. SNMP Polling Flow

```
SNMP Device ◄──── SNMP Request ◄──── SNMP Pool ◄──── Worker Pool ◄──── Poller
     │                                                                     │
     │                                                                     │
     ▼                                                                     │
SNMP Response ──► Worker ──► PollResult ──► Metrics Exporter              │
                                  │                                        │
                                  ▼                                        │
                         Table Index Cache                                 │
                                  │                                        │
                                  ▼                                        │
                         Prometheus (remote_write) ◄───────────────────────┘
```

### 3. WebUI Data Flow

```
Browser ──► WebUI Server ──► REST API
                │                │
                │                ▼
                │          Device Storage ──► MIB Associations
                │                │
                │                ▼
                │          MIB Loader ──► MIB Storage
                │                │
                │                ▼
                │          Dashboard Generator
                │                │
                │                ▼
                └──► Prometheus Client ──► Query Metrics
                           │
                           ▼
                     WebSocket ──► Real-time Updates ──► Browser
```

### 4. Dashboard Generation Flow

```
User selects device ──► Fetch device MIBs
                              │
                              ▼
                       For each MIB:
                              │
                              ▼
                    GET /api/mibs/{id}/dashboard
                              │
                              ▼
                    Dashboard Generator:
                    ├── Load OID metadata
                    ├── Classify objects (scalar/table)
                    ├── Determine chartability
                    └── Generate widget specs
                              │
                              ▼
                    Frontend renders:
                    ├── Status widgets (scalars)
                    ├── Chart widgets (time series)
                    └── Table widgets (multi-instance)
                              │
                              ▼
                    Subscribe to WebSocket:
                    ├── Metric updates
                    └── Real-time chart data
```

## Port Mapping

| Service     | Internal Port | External Port | Description                    |
|-------------|--------------|---------------|--------------------------------|
| go-netmon   | 9080         | 9080          | REST API for poller control    |
| go-netmon   | 9090         | 9090          | Prometheus metrics endpoint    |
| Prometheus  | 9090         | 9091          | Prometheus UI and API          |
| Grafana     | 3000         | 3000          | Grafana dashboards             |
| WebUI       | 8080         | 8081          | Web UI for device management   |

## Key Components

### go-netmon Service

| Component        | Purpose                                            |
|------------------|---------------------------------------------------|
| Config Manager   | Load and validate YAML configuration              |
| MIB Loader       | Parse MIB files using gosmi library               |
| MIB Storage      | PebbleDB-based storage for OID metadata           |
| SNMP Pool        | Connection pooling with health checks             |
| Poller           | Multi-device parallel polling with worker pool    |
| Metrics Exporter | Prometheus remote_write with batching             |
| Table Index Cache| Cache table indices for label enrichment          |
| OID Learner      | Dynamic discovery of table structures             |
| API Server       | REST API for device management and status         |

### WebUI Service

| Component          | Purpose                                          |
|--------------------|--------------------------------------------------|
| HTTP Server        | Serve static files and REST API                  |
| Device Storage     | PebbleDB-based device and profile storage        |
| MIB Import         | Upload and parse MIB files                       |
| Dashboard Generator| Generate widget specifications from MIB metadata |
| Prometheus Client  | Query metrics for display                        |
| WebSocket Hub      | Real-time metric updates to browser              |

## Storage Architecture

### PebbleDB Key Prefixes

**MIB Storage (shared):**
- `oid:{oid}` - OID metadata
- `mod:{name}` - Module information
- `tbl:{oid}` - Table information
- `name:{name}` - Reverse name lookup

**WebUI Storage:**
- `dev:{id}` - Device records
- `prof:{id}` - Profile records
- `mib:{id}` - MIB records
- `dev-mib:{deviceId}:{mibId}` - Device-MIB associations
- `prof-mib:{profileId}:{mibId}` - Profile-MIB associations
- `dev-prof:{deviceId}:{profileId}` - Device-Profile assignments

## Quick Start Commands

```bash
# Start full stack
make run

# Start WebUI only (development mode)
make run-webui

# View logs
make logs
make logs-webui

# Stop services
make stop

# Check status
make status

# Rebuild and restart
make restart
```

## Access URLs

- **WebUI**: http://localhost:8081
- **Prometheus**: http://localhost:9091
- **Grafana**: http://localhost:3000 (admin/admin)
- **go-netmon API**: http://localhost:9080
