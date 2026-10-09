# Web UI Backend Integration Example

> **Status: ARCHIVE** · Scope: go-netmon · Superseded by [webui.md](webui.md) and [webui-backend.md](webui-backend.md)

This page was a worked example of embedding the Web UI backend in a go-netmon application: a `parser.New` MIB provider, an `api.Config` with a SQLite `DBPath`, and a one-service Docker Compose file. None of that matches the code now.

## What replaced it

- The web UI is the standalone `cmd/webui` binary. Read it for a complete, current example of building an `api.Config` and starting the server.
- Storage is PebbleDB, opened from the `-db` path.
- The Compose file runs the web UI as a separate service on host port 8082.
- The import path is `github.com/jeremyhahn/go-netmon/...`.

See [webui.md](webui.md) for running the service and [webui-backend.md](webui-backend.md) for the REST API and a short `api.NewServer` example.
