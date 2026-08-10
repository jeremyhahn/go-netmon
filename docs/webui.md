# Web UI Documentation

The go-netmon Web UI provides a complete web-based interface for managing SNMP monitoring configuration, MIB imports, and real-time metrics visualization.

## Features

- **Device Management**: Add, edit, and delete SNMP devices
- **Device Profiles**: Create reusable collections of MIBs
- **MIB Management**: Import, browse, and manage MIB definitions
- **Real-time Dashboards**: Live metric streaming via WebSocket
- **Automatic Dashboard Generation**: Generate dashboards from MIB definitions
- **Dark Theme**: Modern, responsive UI with dark color scheme

## Architecture

### Backend Components

- **Storage Layer** (`internal/webui/storage/`): SQLite database with WAL mode for concurrent access
- **REST API** (`internal/webui/api/`): 30+ endpoints for CRUD operations
- **WebSocket Hub** (`internal/webui/websocket/`): Real-time metric streaming
- **MIB Provider** (`internal/webui/provider/`): Integration with MIB loader
- **Dashboard Generator** (`internal/webui/api/dashboard_gen.go`): Automatic dashboard creation

### Frontend Components

- **HTML** (`internal/webui/static/index.html`): Single-page application structure
- **CSS** (`internal/webui/static/css/style.css`): Dark theme styling with animations
- **JavaScript** (`internal/webui/static/js/app.js`): Vanilla JS with class-based architecture
- **ECharts**: Real-time charting library

### Database Schema

Six tables with proper relationships:

1. `devices` - SNMP device configurations
2. `device_profiles` - Reusable MIB collections
3. `mibs` - Imported MIB metadata
4. `device_mibs` - Direct MIB assignments to devices
5. `profile_mibs` - MIB assignments to profiles
6. `device_profiles_assigned` - Many-to-many device-profile relationships

## Building

```bash
# Build the Web UI server
make build-webui

# Or build all binaries
make build-all
```

## Running

### Docker (Recommended)

The easiest way to run the Web UI is using Docker Compose:

```bash
# Build and run just the Web UI with Prometheus
make run-webui

# Or run the entire stack (go-netmon + Prometheus + Grafana + Web UI)
make run
```

Then open http://localhost:8081 in your browser.

**Docker Commands:**

```bash
# Build Web UI Docker image
make docker-build-webui

# Stop Web UI
make stop-webui

# Restart Web UI
make restart-webui

# View Web UI logs
make logs-webui

# Run the full stack
make run
# Services available at:
#   - Web UI:     http://localhost:8081
#   - go-netmon:  http://localhost:8080
#   - Prometheus: http://localhost:9091
#   - Grafana:    http://localhost:3000
```

### Binary (Local Development)

Run the Web UI binary directly:

```bash
# Start with defaults
./bin/webui

# Customize settings
./bin/webui \
  -listen :8080 \
  -prometheus http://localhost:9091 \
  -db data/webui.db \
  -mib-storage data/mibs
```

### Command-line Flags

- `-listen` - HTTP listen address (default: `:8080`)
- `-prometheus` - Prometheus URL for querying metrics (default: `http://localhost:9091`)
- `-db` - Database path for Web UI storage (default: `data/webui.db`)
- `-mib-storage` - Path to store uploaded MIB files (default: `data/mibs`)

### With go-netmon Stack (Manual)

The Web UI works alongside the main go-netmon services:

```bash
# Terminal 1: Start Prometheus
docker run -p 9091:9090 prom/prometheus

# Terminal 2: Start go-netmon poller
./bin/go-netmon

# Terminal 3: Start Web UI
./bin/webui
```

Then open http://localhost:8080 in your browser.

## Usage Guide

### 1. Import MIBs

Navigate to **MIB Manager** and upload MIB files:

1. Click "Import MIB"
2. Select a `.mib` or `.txt` file
3. The MIB will be parsed and stored
4. View imported MIBs in the table

### 2. Create Device Profile

Create reusable MIB collections:

1. Navigate to **Profiles**
2. Click "+ Add Profile"
3. Enter name and description
4. Click on the profile to view details
5. Click "+ Assign MIB" to add MIBs to the profile

### 3. Add Device

Configure SNMP devices:

1. Navigate to **Devices**
2. Click "+ Add Device"
3. Fill in device details:
   - Name (e.g., "core-router-01")
   - IP Address
   - SNMP Version (v2c or v3)
   - Community String
   - Port (default: 161)
   - Poll interval (optional; blank inherits the global default)
4. Click "Save"

### 4. Assign MIBs to Device

Two ways to assign MIBs:

**Option 1: Via Profile**
1. Open device details
2. Click "+ Assign Profile" under "Assigned Profiles"
3. Select a profile
4. All MIBs from the profile will be applied

**Option 2: Direct Assignment**
1. Open device details
2. Click "+ Assign MIB" under "Direct MIB Assignments"
3. Select individual MIBs

### 5. Choose which objects a device polls

Each MIB row on a device's page carries an **Objects** action, on both direct
assignments and the ones a profile supplies. It opens a picker for that device
and that module.

**The unit of configuration is the scalar or the table, not the column.** A
table walk returns every column of every row in one request, so a table's
columns are folded under it behind a `Show N columns` disclosure. IP-MIB on a
live router is 293 objects but only 24 rows an operator can act on: 10 scalars
and 14 tables. The list opens on exactly those, in the poller's own order, with
a running `Selected N of M configurable objects → ~S series` total.

A box is ticked if and only if `GET /api/devices/:id/mibs/:mibId/objects` said
`polled`. The verdict is never recomputed in the browser: after every save the
plan is fetched again, and the ticks that come back are the poller's answer
rather than a prediction of it.

**Only rows that can be operated get a checkbox.** Everything else shows the
state it is in. A disabled checkbox is not a control, and 269 of them was the
whole of the reported bug:

| reason | control | what the picker shows |
| --- | --- | --- |
| `polled` | checkbox, ticked | Unticking adds an anchored `^name$` exclude. |
| `covered-by-table-walk` | **none — a green ✓ state marker** | The object *is* being collected, so the row reads on, next to its **Collected** badge. It sits under its table, and the table's checkbox (plus a `Stop collecting <table>` action) is the control. Unticking the table flips its columns to an amber "will stop" marker until the change is saved. |
| `not-accessible`, `not-current` | **none — a grey — state marker** | Reference-only: no filter can make the agent return them. Hidden from the default view, with a footer that counts them and reveals them in one click. |
| `excluded-by-filter` | checkbox, unticked | Names the pattern that removed it, and how many other objects that pattern also matches. Ticking deletes the pattern. |
| `not-included-by-filter` | checkbox, unticked | Ticking adds an anchored `^name$` to the include list. |
| `capped-by-max-oids` | checkbox, unticked | Ticking raises `max_oids` to the value the object needs. |
| `module-disabled` | none | One banner over a greyed list, instead of the same tag on every row. |

A banner over the list states which of the two actions are available, counted
from the server's verdicts. With no filters stored every pollable object is
already polled, so the only action is to remove one — correct behaviour, but
invisible until it is said, and hunting for a control that cannot exist is what
made the picker read as broken:

> **Everything IP-MIB can poll on core-01 is being polled.** All 24
> configurable objects are on, so there is nothing left to turn on — untick one
> to stop collecting it, and it becomes tickable again the moment it is
> excluded. The other 269 of 293 objects are not controls: 193 table columns
> arrive with their table's walk and are listed under it — the table is the
> control for them; 76 objects can never be polled (deprecated, or not readable
> by SNMP) and are hidden until you ask for all objects.

The **Show** filter defaults to `Configurable objects`; `Polled only`,
`Not polled` and `All objects` are the other views, and the footer offers
`Show all N objects` / `Back to configurable objects only` directly. Search
covers names, OIDs and descriptions, reaches inside tables, and opens the
table it matched in.

Ticking and unticking edit the module's filter patterns, which are visible and
directly editable under **Advanced**. Each line is validated with `RegExp`;
nothing is sent while a pattern is invalid, and fields are never comma-split
(`udp{1,2}` is one pattern). Editing a pattern by hand can catch objects that
were never ticked, so the picker says the ticks are the last server verdict
until the next save re-evaluates them.

Writes always go to the **device** assignment
(`PUT /api/devices/:id/mibs/:mibId/assignment`, which replaces it wholesale).
When the rule in force came from a profile or from the default, saving is
confirmed first: it creates a device-level assignment that shadows the shared
rule for that one device.

### 6. Generate Dashboard

Once MIBs are assigned to a device:

1. Open device details
2. Scroll to "All Applied MIBs" card
3. Click "Generate Dashboard"
4. Dashboard JSON will be generated and available for download

### 7. View Real-time Metrics

Navigate to **Dashboard** to view real-time charts:

- Select device from dropdown
- Select interface (if applicable)
- Charts update automatically every 5 seconds via WebSocket
- No page refreshes required

**Widgets have three states, not two.** A chart card and a "Status &
Information" tile describe the same kind of object in different shapes, so they
share one first-sample window (30s) and one terminal vocabulary:

| state | chart card | status tile |
| --- | --- | --- |
| waiting | `Waiting for data…` / *Listening for the first sample.* | `Loading...` |
| showing | the plotted series | the value |
| terminal | `No data reported` / *The device returned no samples for this object.* | `No data reported`, tile dashed and muted, hover for the same detail |

The terminal state says what is known — nothing arrived in the window — and not
why, because the UI cannot distinguish "the device does not implement this
object" from "no sample yet". A late first sample still replaces it. Neither
shape hides itself: a widget that disappears leaves no way to see which object
was promised, so both keep their place in the grid.

### 8. Browse MIBs

Navigate to **MIB Browser** to explore MIB definitions:

- View all available MIBs
- Click on a MIB to see detailed information
- View OIDs, types, and descriptions

## REST API Endpoints

### Devices

- `GET /api/devices?stored=true` - List stored devices
- `POST /api/devices` - Create device
- `GET /api/devices/:id` - Get device details
- `PUT /api/devices/:id` - Update device
- `DELETE /api/devices/:id` - Delete device
- `GET /api/devices/:id/mibs` - Get device MIBs (direct + profile)
- `POST /api/devices/:id/mibs` - Assign MIB to device
- `DELETE /api/devices/:id/mibs/:mibId` - Remove MIB from device
- `GET /api/devices/:id/mibs/:mibId/estimate` - Estimate the Prometheus series a MIB would cost on a device
- `GET /api/devices/:id/mibs/:mibId/objects` - List a MIB's objects with whether the device polls each one, and why not; accepts `search`, `limit`, `offset` and `polled` (see *Narrowing a list*)

### Per-device poll interval

A device may carry its own polling schedule. It is a whole number of **seconds**
in the `poll_interval` field, and it is present on every device write and read:

```
POST /api/devices          {"name":"core-01","address":"198.51.100.252",
                            "snmp_version":"v2c","community":"public",
                            "poll_interval":300}
PUT  /api/devices/:id      the same body; omit poll_interval to inherit
GET  /api/devices/:id      {"...":"...", "poll_interval":300,
                            "effective_poll_interval":300}
```

**Inherit versus explicit.** `poll_interval` is *absent* on a device that has no
schedule of its own; that device is polled at `global.default_poll_interval` and
follows it when it changes. Any device that has one carries the number. Absent
and "explicitly the same number the default happens to be today" are different
configurations, and the wire format keeps them apart: to hand a device back to
the default, `PUT` it without a `poll_interval`.

**`effective_poll_interval`** is on every device response and is always the
number of seconds actually in force — the device's own, or the global default.
A client renders "default (30s)" from `poll_interval` being absent while
`effective_poll_interval` is 30, without needing to know the server's config.

**Validation.** `poll_interval` must be at least 1 second. `0` and negatives are
rejected with `400` and a message naming the value and saying to omit the field
to inherit; nothing is stored and nothing is pushed to the poller.

**Warnings.** An interval shorter than the SNMP timeout budget — `snmp_timeout x
(snmp_retries + 1)`, 40s with the shipped configuration — is **accepted** and
reported in a `warnings` array on the device:

```json
{
  "id": 4, "name": "bastion", "poll_interval": 5, "effective_poll_interval": 5,
  "warnings": ["poll interval 5s is shorter than this device's SNMP timeout budget of 40s (snmp_timeout 10s x 4 attempts); a device that stops answering cannot finish a poll within it and will be polled on the poller's backoff instead"]
}
```

It is a warning and not an error because it is only self-defeating while the
device is already silent: a healthy device answers in milliseconds, the poller
widens the interval on consecutive failures anyway, and the shipped
configuration itself pairs a 30s default with a 40s budget — a rule that rejects
the product's own defaults is a rule about the wrong thing.

`warnings` and `effective_poll_interval` are computed on every read and ignored
on a write, so a client may safely `GET`, edit one field and `PUT` the whole
document back.

**What reaches the poller.** A device that inherits is pushed as
`poll_interval: 0`, which is what the poller's control API and configuration
document both read as "use `global.default_poll_interval`". The web UI does not
resolve the default at push time; doing so would freeze today's default into
every device and make a later change to it silently ineffective.

### Fleet status

`GET /api/devices/status` returns live status for **every** configured device in
one request. It is the collection counterpart of `GET /api/devices/:id/status`,
and exists so a fleet overview costs one request rather than one per device: at
four devices the difference is invisible, at a hundred it is the page load.

```json
{
  "devices": [
    {
      "id": 4,
      "name": "bastion",
      "address": "198.51.100.14",
      "state": "unreachable",
      "poll_interval": 5,
      "effective_poll_interval": 5,
      "status": {
        "name": "bastion",
        "connected": false,
        "last_poll_time": "2026-08-10T03:56:52.550541906Z",
        "last_poll_success": false,
        "last_error": "poller: device bastion answered none of 4 SNMP operations ...",
        "metrics_collected": 0,
        "poll_interval": "",
        "unreachable": true,
        "consecutive_failures": 8,
        "next_poll_time": "2026-08-10T04:11:52.550525383Z"
      }
    },
    {
      "id": 1,
      "name": "core-01",
      "address": "198.51.100.252",
      "state": "ok",
      "status": { "name": "core-01", "connected": true, "last_poll_success": true, "...": "..." }
    },
    {
      "id": 3,
      "name": "pdu-03",
      "address": "198.51.100.10",
      "state": "never_polled",
      "status": { "name": "pdu-03", "last_poll_time": "0001-01-01T00:00:00Z", "...": "..." }
    },
    {
      "id": 5,
      "name": "newswitch-05",
      "address": "198.51.100.20",
      "state": "not_registered",
      "effective_poll_interval": 30
    },
    {
      "id": 2,
      "name": "core-02",
      "address": "198.51.100.253",
      "state": "unknown",
      "error": "poller: status for device \"core-02\" is unavailable: connection refused"
    }
  ]
}
```

**`status`** is verbatim what `GET /api/devices/:id/status` returns for the same
device — the same assembler produces both, and a test pins them together — so
the fleet view and the device view cannot describe a device differently. It is
absent when no status could be obtained.

**`poll_interval`** and **`effective_poll_interval`** mean exactly what they mean
on a device record, and come from the same place: the device listing this
endpoint already reads. They are therefore present on every row, including rows
whose `status` is absent — the schedule of a device the poller has never heard
of is precisely what an operator looking at a `not_registered` card wants to
know.

Note that `status.poll_interval` is a different field: it is the poller's own
runtime string (`"30s"`), reported by the poller for a device it is actively
tracking. The two `*poll_interval` fields at row level are the *configuration*,
in seconds, and are the ones to render a schedule from.

**`state`** summarizes that status for a card. It adds no new information; it
makes explicit the distinctions a fleet view has to draw, in particular the two
that are easy to conflate.

| State | Meaning |
| --- | --- |
| `ok` | The most recent poll succeeded. |
| `failing` | The device answers SNMP but its most recent poll did not fully succeed. It is still collecting. |
| `unreachable` | The poller is getting no SNMP answers at all and is retrying on a widening interval. |
| `never_polled` | The poller has the device but has produced no poll result yet — `last_poll_time` is the zero time. Distinct from `failing`: a failed poll is still a poll and stamps the time. |
| `not_registered` | The device is in the web UI's records but the poller has no record of it. A configuration problem, not a device problem. |
| `unknown` | The status could not be obtained. `error` says why; there is no `status`. |

Guarantees:

- **Every configured device gets a row**, including one the poller has never
  picked up. A device the poller 404s becomes a `not_registered` row, never a
  404 on the whole request.
- **Deterministic order.** Rows come back sorted by device name on every call,
  from the same ordering the device list uses.
- **One slow device cannot delay or fail the response.** Each device has its own
  deadline (`FleetStatusDeviceBudget`, 2s) inside one budget for the whole
  request (`FleetStatusBudget`, 8s), and requests run concurrently up to
  `FleetStatusConcurrency` (16). The per-device deadline is deliberately shorter
  than the poller client's own timeout so a hung poller is cut short once per
  device instead of running the client's retry ladder once per device. A device
  that runs out of budget is reported as `unknown` — it is never dropped.
- **`devices` is always an array**, empty when nothing is configured, never
  `null`.

The row carries `id`, `name` and `address` alongside the status because the
device listing has already read them. It deliberately does **not** carry MIB
count or assigned profile: each would cost a per-device store walk, turning one
request into an N+1 inside the handler.

### Profiles

- `GET /api/profiles` - List profiles
- `POST /api/profiles` - Create profile
- `GET /api/profiles/:id` - Get profile details
- `PUT /api/profiles/:id` - Update profile
- `DELETE /api/profiles/:id` - Delete profile
- `POST /api/profiles/:id/mibs` - Assign MIB to profile
- `DELETE /api/profiles/:id/mibs/:mibId` - Remove MIB from profile

### Device-Profile Associations

- `POST /api/devices/:deviceId/profiles/:profileId` - Assign profile to device
- `DELETE /api/devices/:deviceId/profiles/:profileId` - Remove profile from device

### MIB Cost Estimation

`GET /api/devices/:id/mibs/:mibId/estimate` answers how many Prometheus **series**
enabling a MIB would add to a device. Object count is misleading: on a live
router a module offering 624 pollable objects produced 31,836 series, because
every table column costs one series per row.

The MIB does not have to be assigned yet — asking before committing is the point.

```json
{
  "mib": "IF-MIB",
  "device": "core-01",
  "series": 528,
  "scalars": 0,
  "columns": 22,
  "instances": 24,
  "exact": false,
  "explanation": "estimated from a median of 24 instances across 3 tables observed on this device"
}
```

Rules, applied in order:

1. **Exact.** If the device already reports series for the MIB, they are counted
   from Prometheus and `exact` is `true`.
2. **Estimate.** Otherwise each pollable scalar costs 1 series and each pollable
   column costs one series per table row, where the row count is the median
   across the tables this device already reports. If the device has no series,
   the poller's learned tables supply the ratio instead; if nothing is known,
   the estimate assumes 1 row per table and says so.
3. **Free objects.** Objects the poller never fetches — table nodes, row nodes
   and not-accessible index columns — cost nothing. An accessible index column
   such as `ifIndex` *is* walked and does cost a series per row.

### Which objects a device polls

`GET /api/devices/:id/mibs/:mibId/objects` answers, for one device and one MIB,
which of the module's objects are being polled right now and why the rest are
not. It is what a per-object checkbox list is drawn from.

The answer cannot be worked out from the assignment alone. It depends on the
assignment's filters *and* on the poller's rules about tables, columns and
access, so it is computed on the server by the same code the poller runs —
`selector.Plan` in `internal/mib/selector`, of which the poller's own selection
(`selector.Polled`) is the polled subset. There is one rule and one
implementation of it; a client must not reproduce it.

The MIB has to reach the device, directly or through a profile. A device, MIB or
pairing that does not exist is a `404`.

```json
{
  "device_id": 1,
  "device": "core-01",
  "mib_id": 12,
  "mib": "IF-MIB",
  "assignment": { "enabled": true, "exclude_names": ["^ifSpecific$"] },
  "source": "profile",
  "total": 91,
  "polled": 8,
  "estimate": {
    "mib": "IF-MIB", "device": "core-01", "series": 528,
    "scalars": 3, "columns": 22, "instances": 24, "exact": false,
    "explanation": "estimated from a median of 24 instances across 3 tables observed on this device"
  },
  "objects": [
    {
      "oid": "1.3.6.1.2.1.2.2", "name": "ifTable", "type": "", "access": "not-accessible",
      "kind": "table", "polled": true, "reason": "polled", "series": 528
    },
    {
      "oid": "1.3.6.1.2.1.2.2.1.10", "name": "ifInOctets", "type": "Counter32",
      "access": "read-only", "is_column": true, "table_oid": "1.3.6.1.2.1.2.2",
      "kind": "column", "polled": false, "reason": "covered-by-table-walk", "series": 0
    }
  ]
}
```

`source` is `device`, `profile` or `default`, and says where the assignment in
force comes from — a rule inherited from a profile is shared with every device
using it.

`kind` is `scalar`, `column`, `table` or `entry`: how the poller would fetch the
object. It is the classification the rules themselves branched on.

`reason` is one of:

| reason | meaning |
| --- | --- |
| `polled` | fetched every poll |
| `covered-by-table-walk` | its table is walked, which already returns it |
| `not-accessible` | MAX-ACCESS is neither read-only nor read-write |
| `not-current` | STATUS is deprecated or obsolete |
| `excluded-by-filter` | an exclude pattern removed it |
| `not-included-by-filter` | the module has include patterns and none matched |
| `capped-by-max-oids` | the module's `max_oids` was already spent |
| `module-disabled` | the MIB is assigned but not being polled |

`series` is the object's share of the module estimate: 1 for a polled scalar,
one per row for a polled column, and one per row per pollable column for a
walked table. Covered columns cost 0, so the per-object costs sum to the
module's total rather than counting the same data twice. The estimate is the
same one `/estimate` returns.

`objects` is in the order the poller sees them, which is the order a `max_oids`
cap is spent in. Re-sorting the list client-side changes which objects appear
capped, so sort a *copy* for display.

The list can be narrowed with `search`, `polled`, `offset` and `limit` — see
*Narrowing a list: search and paging*. A narrowed response carries a `page`
block; `total` and `polled` above keep describing the whole module either way.

### Narrowing a list: search and paging

`GET /api/mibs`, `GET /api/mibs/:id` and
`GET /api/devices/:id/mibs/:mibId/objects` accept the same query parameters, so
a client asks for the rows it is going to draw instead of downloading a module
to draw fifty of it. `GET /api/mibs/57` (CISCO-ENTITY-VENDORTYPE-OID-MIB, 10,539
objects) is **1.6 MiB**; the same request with `?search=cevChassis&limit=3` is
**937 bytes**.

| Parameter | Applies to | Meaning |
| --- | --- | --- |
| `search` | all three | case-insensitive substring; matches an object's `name`, `oid` or `description`, and a module's `name`, `description` or tags |
| `limit` | all three | maximum records to return; `0` or absent means all |
| `offset` | all three | how many *matching* records to skip; page N of size L is `offset=(N-1)*L` |
| `pollable` | `/api/mibs/:id` | `true` keeps only the objects whose `pollable` is true |
| `polled` | `/api/devices/:id/mibs/:mibId/objects` | `true` keeps only the objects the device is polling |
| `tag` | `/api/mibs` | keeps only the modules carrying the tag, ignoring case |
| `filter=available` | `/api/mibs` | unchanged: only the modules that have data flowing |

`search` and the selections above are applied first; `offset` and `limit` then
cut a page out of what matched.

**Paging is offset-and-limit, not a cursor**, because a paging control has to
jump to page seven, not only step forward one page at a time.

**The counts.** A request that carried any of these parameters is answered with
a `page` block beside the records:

```json
{
  "objects": [ ... 3 objects ... ],
  "page": { "total": 10539, "matched": 2086, "count": 3, "offset": 0, "limit": 3 }
}
```

| field | meaning |
| --- | --- |
| `total` | records before any narrowing: the module's whole object count, or the whole catalog. A search does not move it. |
| `matched` | records the query selected, before `offset` and `limit` |
| `count` | records in this response |
| `offset`, `limit` | echoed from the request |

That is everything needed to render "51–100 of 293 matching, 10,539 in the
module" and to enable or disable a *next* control (`offset + count < matched`)
without the client counting anything or keeping a running total.

The module's own `object_count`, and the plan's `total` and `polled`, always
describe the **whole** module and are unaffected by the query, so a paged view
can still say "12 polled of 293" above the table.

**Ordering.** Records are returned in an order that does not change between
requests, so paging visits each record exactly once:

- `/api/mibs` is in module-name order (`sortByName` in
  `internal/webui/storage/order.go`), which is the order it has always been in.
- `/api/mibs/:id` is in the metadata store's own order — a key scan of the
  module's OID index — which is the order it has always been in.
- `/api/devices/:id/mibs/:mibId/objects` is in the **poller's** order, which
  `capped-by-max-oids` is only meaningful in. A page is a slice of that order
  and never a re-sort of it. Sort a *copy* for display.

**Backward compatibility.** A request carrying none of these parameters is
answered exactly as it was before they existed: every record, in the same order,
and **no `page` block at all**. `filter=available` alone is not a narrowing for
this purpose, so the existing MIB-catalog calls are untouched.

**Errors.** All of these are `400` with a message naming the parameter:

| request | response |
| --- | --- |
| `limit=-1`, `limit=abc`, `offset=-3`, `offset=x` | `400`, refused before the store is read |
| `pollable=maybe`, `polled=yes` | `400` — a flag that is not a boolean is not read as `false` |
| `offset` past the end of what matched | `400` — a page that does not exist is not an empty page |

`offset=0` is never out of range: page one of an empty result is a legitimate
request and returns an empty list with `matched: 0`.

**What the web UI should call.**

```
# MIB browser, page 3 of a 50-per-page catalog, filtered by a search box:
GET /api/mibs?search=cisco&limit=50&offset=100
GET /api/mibs?tag=cisco&limit=50            # tag chips
GET /api/mibs?filter=available&limit=50     # "only modules with data"

# Object list for one module, page 2 of 100 per page:
GET /api/mibs/57?limit=100&offset=100
GET /api/mibs/57?search=cevChassis&limit=100
GET /api/mibs/57?pollable=true&limit=100

# Per-object checkbox list for a device, page 2, only what is polled:
GET /api/devices/1/mibs/12/objects?limit=100&offset=100
GET /api/devices/1/mibs/12/objects?polled=true&limit=100
```

Render the pager from `page`: `matched` is the denominator, `offset/limit + 1`
is the current page, `ceil(matched/limit)` is the last one. Do not compute the
page count from the length of the array.

### MIBs

- `GET /api/mibs?stored=true` - List stored MIBs
- `POST /api/mibs/import` - Import MIB file (multipart/form-data); 409 if the
  module is already imported
- `POST /api/mibs/import?replace=true` - Re-import a module in place: its
  objects, object count and description are refreshed, while its id, tags and
  every device and profile assignment (with their filters) are kept
- `GET /api/mibs/:id` - Get stored MIB details and its objects; accepts
  `search`, `limit`, `offset` and `pollable` (see *Narrowing a list*). Each
  conceptual row object carries an `index` array: the names of its `INDEX`
  columns, in `INDEX` order, which is what tells a series' genuine index labels
  from the enrichment labels (`ifname`, `ifdescr`, ...) beside them
- `DELETE /api/mibs/:id` - Delete stored MIB
- `GET /api/mibs/:name` - Get MIB from provider (legacy)

### Dashboards

- `POST /api/dashboards/generate` - Generate dashboard from MIBs
- `GET /api/devices/:id/dashboard` - Generate dashboard for device

### WebSocket

- `GET /ws` - WebSocket endpoint for real-time metrics

## WebSocket Protocol

The WebSocket connection at `/ws` supports bidirectional communication:

### Client → Server Messages

```json
{
  "action": "subscribe",
  "query": "up{device='core-router-01'}"
}
```

```json
{
  "action": "unsubscribe",
  "query": "up{device='core-router-01'}"
}
```

### Server → Client Messages

Metrics are pushed every 5 seconds:

```json
{
  "query": "up{device='core-router-01'}",
  "metrics": [
    {
      "metric": {
        "device": "core-router-01",
        "job": "snmp"
      },
      "value": 1,
      "timestamp": 1234567890
    }
  ],
  "timestamp": 1234567890
}
```

## Configuration

The Web UI uses the following directory structure:

```
data/
├── webui.db          # SQLite database
├── mib.db            # MIB storage database
└── mibs/             # Uploaded MIB files
    └── <mib-name>/
        └── <mib-file>
```

Ensure these directories exist or the Web UI will create them automatically.

## Development

### Frontend Development

The frontend uses vanilla JavaScript with no build step required:

- Edit files in `internal/webui/static/`
- Refresh browser to see changes
- Use browser DevTools for debugging

### Backend Development

```bash
# Run tests
make test-webui

# Run specific package tests
make test-webui-storage
make test-webui-api

# Generate coverage
make coverage-webui
```

### Database Migrations

Database schema is automatically created on first run. To reset:

```bash
rm data/webui.db
./bin/webui  # Will recreate schema
```

## Troubleshooting

### WebSocket Not Connecting

Check that:
1. Web UI server is running
2. Browser console shows no CORS errors
3. Prometheus is accessible at configured URL

### No Data in Charts

Verify:
1. Device is configured in go-netmon
2. go-netmon is polling the device
3. Metrics are visible in Prometheus
4. Correct device/interface selected in UI

### MIB Import Fails

Ensure:
1. MIB file is valid ASN.1 format
2. File size is under 10MB limit
3. Required MIB dependencies are imported first

### Dashboard Generation Issues

Check:
1. At least one MIB is assigned to device
2. MIBs are successfully parsed
3. Check browser console for errors

## Security Considerations

The Web UI is designed for internal network use:

- No authentication/authorization (add reverse proxy for production)
- CORS allows all origins (configure for production)
- File uploads limited to 10MB
- SQL injection protection via parameterized queries
- XSS protection via HTML escaping

For production deployment, consider:
- Adding authentication (OAuth, JWT, etc.)
- Configuring CORS whitelist
- Using HTTPS with TLS certificates
- Rate limiting API endpoints
- Input validation and sanitization

## Performance

The Web UI is optimized for:

- Concurrent connections: 100+ simultaneous WebSocket clients
- Database connections: Pool of 25 connections with WAL mode
- File uploads: 10MB limit per file
- Query timeout: 10 seconds for Prometheus queries
- WebSocket ping/pong: 60-second intervals

## Future Enhancements

Potential improvements:

- [ ] User authentication and authorization
- [ ] Multi-tenancy support
- [ ] Advanced search and filtering
- [ ] Dashboard sharing and export
- [ ] Alerting configuration
- [ ] Historical data visualization
- [ ] Custom metric queries
- [ ] Bulk device import (CSV)
- [ ] API documentation (OpenAPI/Swagger)
- [ ] Audit logging
