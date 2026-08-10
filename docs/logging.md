# Logging

`internal/logging` builds the application's `*slog.Logger` from the `logging`
section of `config.yaml`. Before this package the section was validated but
never read: 31 `log.Printf("DEBUG: ...")` statements in the web UI and the
poller fired unconditionally, several hundred lines a second with a dashboard
open. Those statements are now level-gated, so the default level of `info`
produces none of them.

## Configuration

```yaml
logging:
  level: "info"    # debug | info | warn | error
  format: "json"   # json | text
  output: "stdout" # stdout | file
  file: ""         # required when output is "file"
```

| Key      | Meaning                                                        |
| -------- | -------------------------------------------------------------- |
| `level`  | Lowest severity kept. `debug` turns on the tracing below.        |
| `format` | `json` for machine consumption, `text` for a terminal.           |
| `output` | `stdout`, or `file` to append to the path in `file`.             |
| `file`   | Log file path. Required when `output` is `file`; there is no default, because a log landing somewhere the operator did not choose is worse than a startup failure naming the key to set. |

All four are validated by `config.Config.Validate`, so a bad value fails at
startup rather than at the first record.

## Use

```go
logger, closer, err := logging.New(cfg.Logging)
if err != nil {
    return fmt.Errorf("failed to initialize logging: %w", err)
}
defer closer.Close() // no-op for stdout
```

`logging.NewWithWriter(cfg, w)` builds the same logger over a caller-owned
writer, which is what tests use to capture output in a buffer.

Components take the logger by injection and fall back to a discarding logger
when none is given, so a caller that never configures logging pays nothing:

```go
server, err := api.NewServer(api.Config{ /* ... */ Logger: logger})
poll := poller.NewWithExporter(cfg, mibLoader, exporter, poller.WithLogger(logger))
```

## What `debug` turns on

| Component | Tracing |
| --------- | ------- |
| `internal/webui/api` | Query construction, `label_values` resolution, `QueryRange` result shapes, and the first raw metric returned. |
| `internal/webui/websocket` | One record per streaming tick, plus a capped three-metric sample per query. |
| `internal/poller` | Per-OID classification (scalar, table, organizational node), scalar chunking, and per-chunk result counts. |

## Cost when the level is off

Two techniques, chosen per site:

- **Structured attributes.** `logger.DebugContext(ctx, "msg", "labels", m)`
  hands `slog` the value; nothing is formatted unless a handler keeps the
  record. This is used wherever the arguments are already in hand.
- **An explicit `Enabled` check.** Where the *call site itself* is expensive —
  a loop over every OID on a device, a loop over every metric in a query
  result, slicing a sample, or building a key list — the whole block is guarded
  with `logger.Enabled(ctx, slog.LevelDebug)`. Structured attributes would not
  help there, because the loop runs before `slog` is ever reached.

`TestDebugTracingIsSilentAtInfo`, `TestStreamTracingIsSilentAtInfo` and
`TestWithLoggerGatesPollTracing` assert that `info` produces no output on these
paths and `debug` does.

## Running the tests

```
make test-logging
make coverage-logging
```
