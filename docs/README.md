# go-netmon documentation index

> **Status: REFERENCE** · Scope: go-netmon · Last verified: 2026-10-09

Every page under `docs/`, grouped by area. Pages in the archive section are
historical and carry an ARCHIVE banner; the rest are current, and are being
verified against the code page by page (#153).

## Current

### Start here

- [cli.md](cli.md) — the `netmon` command-line interface
- [webui.md](webui.md) — web UI documentation
- [fips.md](fips.md) — FIPS 140-3 build and behavior
- [logging.md](logging.md) — logging
- [testing.md](testing.md) — what a test proves
- [INTEGRATION_TESTS.md](INTEGRATION_TESTS.md) — integration test setup
- [working-in-this-repo.md](working-in-this-repo.md) — traps in this repository

### Web UI

- [quickstart-webui.md](quickstart-webui.md) — web UI quick start
- [QUICK-START.md](QUICK-START.md) — web UI backend quick start
- [webui-backend.md](webui-backend.md) — web UI backend system
- [webui-backend-integration.md](webui-backend-integration.md) — integrating the backend into go-netmon
- [webui-backend-example.md](webui-backend-example.md) — backend integration example
- [webui-poller-integration.md](webui-poller-integration.md) — web UI poller integration
- [timeout-budget.md](timeout-budget.md) — the web UI's timeout budget

### Metrics, dashboards, and configuration

- [metric-naming.md](metric-naming.md) — metric naming
- [friendly-name-labels.md](friendly-name-labels.md) — friendly-name labels for SNMP metrics
- [dashboard-generation.md](dashboard-generation.md) — dashboard generation
- [viewing-all-interfaces.md](viewing-all-interfaces.md) — viewing all interfaces in Grafana dashboards
- [unreachable-devices.md](unreachable-devices.md) — unreachable devices
- [CONFIG_SYNC.md](CONFIG_SYNC.md) — configuration synchronization

### MIBs

- [MIB_README.md](MIB_README.md) — MIB parsing and storage documentation
- [MIB_ARCHITECTURE.md](MIB_ARCHITECTURE.md) — MIB system architecture, visual reference
- [MIB_PARSING_AND_STORAGE.md](MIB_PARSING_AND_STORAGE.md) — MIB parsing and storage implementation walkthrough
- [mib-importer.md](mib-importer.md) — the MIB importer tool
- [mib-catalogs-and-on-demand-loading.md](mib-catalogs-and-on-demand-loading.md) — MIB catalogs and on-demand loading

### Architecture ([architecture/](architecture/README.md))

- [architecture/system-architecture.md](architecture/system-architecture.md) — go-netmon system architecture

The other pages in [architecture/](architecture/README.md) are a partly built
MIB dashboard redesign proposal; they are listed under Archive.

### Deployment ([deployment/](deployment/))

- [deployment/architecture.md](deployment/architecture.md) — deployment architecture
- [deployment/engineering.md](deployment/engineering.md) — deployment engineering contract
- [deployment/user.md](deployment/user.md) — deployment user guide
- [deployment/cli.md](deployment/cli.md) — deployment CLI
- [deployment/api.md](deployment/api.md) — deployment API

### Operations ([operations/](operations/))

- [operations/alarms.md](operations/alarms.md) — alarms
- [operations/backup-restore.md](operations/backup-restore.md) — backup and restore
- [operations/config-export-import.md](operations/config-export-import.md) — configuration export and import
- [operations/export-health.md](operations/export-health.md) — reading the export counters
- [operations/releasing.md](operations/releasing.md) — cutting a release
- [operations/restart-and-startup.md](operations/restart-and-startup.md) — restarting the web UI is a dashboard outage
- [operations/scaling.md](operations/scaling.md) — scaling
- [operations/security-posture.md](operations/security-posture.md) — security posture
- [operations/walking-a-device.md](operations/walking-a-device.md) — walking a device

### Metrics reference ([metrics/](metrics/))

- [metrics/metric-catalog.md](metrics/metric-catalog.md) — the metric catalog
- [metrics/sample-labels.md](metrics/sample-labels.md) — sample labels
- [metrics/entity-sensors.md](metrics/entity-sensors.md) — entity sensors and optical DOM
- [metrics/export-ordering.md](metrics/export-ordering.md) — export ordering: what the receiver actually does
- [metrics/mib-index-refresh.md](metrics/mib-index-refresh.md) — MIB index refresh

## Archive

Historical reports and fix notes. Not maintained.

- [PHASE1_SUMMARY.md](PHASE1_SUMMARY.md) — phase 1 completion summary
- [PHASE2_PROGRESS.md](PHASE2_PROGRESS.md) — phase 2 progress: MIB parser and dynamic OID management
- [PHASE2_COMPLETE.md](PHASE2_COMPLETE.md) — phase 2 complete: MIB parser and storage foundation
- [PHASE2_MIB_PROFILES.md](PHASE2_MIB_PROFILES.md) — phase 2: MIB profile system summary
- [PHASE3_PROMETHEUS_INTEGRATION.md](PHASE3_PROMETHEUS_INTEGRATION.md) — phase 3: Prometheus integration summary
- [IMPLEMENTATION-SUMMARY.md](IMPLEMENTATION-SUMMARY.md) — web UI backend implementation summary
- [MIB_EXPLORATION_SUMMARY.md](MIB_EXPLORATION_SUMMARY.md) — MIB parsing and storage exploration summary
- [mib-importer-summary.md](mib-importer-summary.md) — MIB importer implementation summary
- [webui-fixes-2025-11-01.md](webui-fixes-2025-11-01.md) — web UI critical fixes report, 2025-11-01
- [bugfix-mib-parser-empty-oids.md](bugfix-mib-parser-empty-oids.md) — bug fix: MIB parser empty OID fields
- [scalar_oid_fix.md](scalar_oid_fix.md) — scalar OID detection fix
- [architecture/README.md](architecture/README.md) — MIB dashboard redesign proposal: index
- [architecture/mib-dashboard-redesign.md](architecture/mib-dashboard-redesign.md) — MIB dashboard redesign proposal, and what was built
- [architecture/mib-dashboard-diagrams.md](architecture/mib-dashboard-diagrams.md) — MIB dashboard redesign diagrams
- [architecture/implementation-guide.md](architecture/implementation-guide.md) — MIB dashboard redesign implementation guide
- [architecture/testing-strategy.md](architecture/testing-strategy.md) — MIB dashboard redesign testing strategy
- [architecture/problem-solution-comparison.md](architecture/problem-solution-comparison.md) — MIB dashboard redesign problem-solution comparison
- [architecture/quick-reference.md](architecture/quick-reference.md) — MIB dashboard redesign quick reference
