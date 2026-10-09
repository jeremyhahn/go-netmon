# Integrating Web UI Backend into go-netmon

> **Status: ARCHIVE** · Scope: go-netmon · Superseded by [webui.md](webui.md) and [webui-poller-integration.md](webui-poller-integration.md)

This page described embedding the Web UI backend in the `cmd/go-netmon` process, with a `WebUI` config block (`Address`, `DBPath`), a MIB-parser provider and a single-service Docker Compose file. That design no longer exists.

## What replaced it

- The web UI is its own binary, `cmd/webui`, and its own Compose service. `cmd/go-netmon` does not start it.
- The two services talk over HTTP. The web UI calls the poller's API (see [webui-poller-integration.md](webui-poller-integration.md)), and the poller can pull its device configuration from the web UI when `WEBUI_URL` is set.
- The web UI stores its records in PebbleDB, not SQLite.
- The import path is `github.com/jeremyhahn/go-netmon/...`, not `github.com/jeremyhahn/go-netmon/...`.

For running and configuring the service, see [webui.md](webui.md). For the REST API and the `api.Config` the server takes, see [webui-backend.md](webui-backend.md).
