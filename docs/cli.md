# netmon CLI

`netmon` manages and monitors SNMP devices from the command line. It is a client
of the go-netmon management API — the same HTTP API the web UI calls — so every
operation available in the GUI is available here, and the two cannot drift apart.

## Install

```bash
make build-cli      # produces ./bin/netmon
make install-cli    # installs to /usr/local/bin/netmon
```

## Connecting

Point the CLI at a server with `--server`, or set `NETMON_SERVER` once:

```bash
export NETMON_SERVER=http://198.51.100.91:8082
netmon device list
```

Global flags, accepted by every command:

| Flag | Default | Purpose |
|------|---------|---------|
| `--server` | `http://localhost:8081` | Management API URL (env `NETMON_SERVER`) |
| `--json` | off | Emit JSON instead of tables, for scripting |
| `--refresh` | `0` | Redraw every N seconds; `0` prints once |
| `--timeout` | `1m` | Timeout for a single API request |

Anywhere a device, profile or MIB is expected, you may pass either its **name**
or its **numeric ID**. Names match case-insensitively. A name matching more than
one record is rejected rather than guessed.

Resolving a name costs one listing, so each kind of name — device, profile, MIB —
is listed at most once per process. `netmon profile mib add core A B C D` fetches
the MIB catalogue once, not four times. A name that is not in what was already
fetched is re-checked against the server before the CLI reports it missing, so a
record created moments earlier is still found.

## Checking the server

```bash
netmon health                                # is the API, the poller and Prometheus up?
netmon health --json
netmon version                               # version, commit, build date
```

`health` probes each service and exits non-zero if any is **down**, which makes it
usable as a gate:

```bash
netmon health || page-someone
```

A service reads `unknown` when nothing could probe it — the poller cannot be
probed on an install with no devices, and nothing behind the API can be probed
when the API itself is unreachable. Only `down` fails the command.

When the CLI cannot reach the server at all it says so, and names the two things
worth checking:

```
Error: netmon api: cannot reach the server: GET http://localhost:8081/api/devices:
dial tcp: connection refused; check that go-netmon is running and that --server
(or NETMON_SERVER) points at its management API
```

## Shell completion

```bash
netmon completion bash > /etc/bash_completion.d/netmon
netmon completion zsh  > "${fpath[1]}/_netmon"
netmon completion fish > ~/.config/fish/completions/netmon.fish
```

Device, profile and MIB arguments complete from the server, so `netmon device show
core<TAB>` offers the devices that exist. Completion uses the same `--server` or
`NETMON_SERVER` the commands do, and fails silently: an unreachable server offers
no suggestions rather than printing an error into your command line.

## Live views

`--refresh` turns any read-only command into a live view, clearing the screen
between frames. This mirrors the `sysmon` CLI, so the two behave the same way.

```bash
netmon monitor --refresh 2                   # fleet health, redrawn every 2s
netmon monitor core-01 --refresh 2        # one device and its current values
netmon device status --refresh 5             # polling status only
```

A transient API failure prints an error line inside the frame and keeps
refreshing, rather than exiting and losing the session. Ctrl-C stops.

## Finding things: `--search`, `--limit` and `--offset`

Every command that prints a list accepts the same flags, with the same meaning
everywhere.

| Flag | Meaning |
|------|---------|
| `--search <text>` | keep only rows containing `<text>`, as a case-insensitive substring |
| `--limit <n>` | print at most `n` rows; `--limit 0` prints all |
| `--offset <n>` | skip the first `n` matching rows; with `--limit`, pages through them |

`--search` is applied first, then `--offset` and `--limit` select a page of what
survived. A negative `--limit` or `--offset` is rejected before any request is
made.

`--offset` is offered only by the listings the **server** narrows — `mib list`,
`mib show`, `mib objects` and `device mib objects`. For those, `--search`,
`--limit` and `--offset` are sent to the API as query parameters and only the
rows that will be printed cross the wire:

```bash
netmon mib objects CISCO-ENTITY-VENDORTYPE-OID-MIB --search cevChassis --limit 3
```

used to transfer 1.6 MiB to print three rows; it now transfers under a kilobyte.
Paging through a module visits every object exactly once, because the server
pages an order it does not change between requests: the metadata store's own
order for a module's objects, and the poller's order for an object plan. The
remaining listings are small enough to return whole, and are narrowed in the
CLI; they take `--search` and `--limit` only.

The rows are the same whichever side narrowed them: both run one definition of
a search, `internal/mib/search`. A response that arrives without the server's
page block is the whole list — which is also what a server predating these
parameters returns — and the flags are then applied here, so `--limit` is a
promise to the terminal rather than to a particular server build.

Asking for a page past the end of what matched is an error rather than an empty
table, because a blank table reads like an empty module.

**Nothing is ever truncated silently.** When `--limit` holds rows back, the
listing ends with a footer that counts the *searched* set:

```
showing 50 of 293 objects; use --limit 0 for all
```

A search that matches nothing says so, rather than printing a bare header:

```
No MIBs match --search "no-such-module"; searched 170.
```

### What each command searches

| Command | `--search` matches | Default `--limit` | Narrowed by |
|---------|--------------------|-------------------|-------------|
| `mib list` | name, description, tags | 50 | server |
| `mib show <mib>` | object name, OID, description | 50 | server |
| `mib objects <mib>` | object name, OID, description | 50 | server |
| `mib tables <mib>` | table name, table OID, entry name, index column | 50 | CLI |
| `device mib objects <dev> <mib>` | object name, OID, description | 50 | server |
| `metrics query <metric>` | metric name, labels | 50 | CLI |
| `metrics instant <promql>` | rendered `name{labels}` | 50 | CLI |
| `poller tables` | table name, entry OID, index names, source | 50 | CLI |
| `device list` | name, address | 0 (all) | CLI |
| `device status` | name, address, last error | 0 (all) | CLI |
| `device mib list <dev>` | module name, description, tags | 0 (all) | CLI |
| `device profile list <dev>` | profile name, description | 0 (all) | CLI |
| `profile list` | name, description | 0 (all) | CLI |
| `profile mib list <profile>` | module name, description, tags | 0 (all) | CLI |
| `profile devices <profile>` | device name, address | 0 (all) | CLI |

The listings marked *server* are the ones whose payload is unbounded: a module
has thousands of objects and the catalog grows with every vendor bundle. They
take `--offset` as well. The CLI and the API run the *same* search — one
definition, in `internal/mib/search` — so a search finds the same objects
whichever tool, and whichever side, ran it.

The defaults split on one question: can this list grow beyond a screen without
anyone deciding it should? A MIB catalog grows with every vendor bundle
imported, a module has thousands of objects, and the OID learner accumulates a
table for everything it has ever walked — those are capped. A device list, a
profile list and a set of MIB assignments are things an operator built by hand
and reads to confirm; capping those would hide more than it helps, so they
print whole until you ask otherwise.

Object listings do not cap a conceptual table's columns: the columns are the
reason the table is being shown.

`--search`, `--limit` and `--offset` compose with `--refresh` and `--timeout`.

A page that does not start at the first row says where it sits:

```
showing 51-100 of 293 objects
```

### Under `--json`

A list command emits a document that always reports what you received:

```bash
netmon mib objects CISCO-ENTITY-VENDORTYPE-OID-MIB --search cevChassis --limit 3 --json
```

```json
{
  "total": 10539,
  "matched": 2086,
  "shown": 3,
  "truncated": true,
  "limit": 3,
  "search": "cevChassis",
  "items": [ ... ]
}
```

`total` is how many rows exist before any narrowing — the module's whole object
count, the whole MIB catalog — `matched` is how many `--search` and the
`--pollable-only`/`--polled-only`/`--tag` selections kept, and `shown` is how
many are in `items`. `truncated` saves a consumer from comparing counts. `items`
is always an array, even when nothing matched.

A page other than the first also carries `offset`, the number of matching rows
skipped to reach it, so `offset + shown` of `matched` describes the page
exactly. The counts come from the server for the listings it narrows, so they
stay honest about a list the client never received.

`mib show`, `device mib objects` and `device mib list` keep their existing
document shape and gain the same counts under a `listing` key, so a consumer
reading only the module or plan fields is unaffected.

## Devices

```bash
netmon device list
netmon device list --search core          # match name or address
netmon device show core-01                # config, profiles and effective MIBs
netmon device status                         # polling health for every device
netmon device status --search timeout        # which devices are failing this way
netmon device status core-01

netmon device add --name core-01 --address 198.51.100.252 \
    --version 2c --community public --poll-interval 60
netmon device update core-01 --community newsecret
netmon device delete core-01 --force      # --force is required

# Probe a configured device with its stored credentials:
netmon device test core-01
# Or probe an address that is not configured yet:
netmon device test --address 198.51.100.254 --community public
```

`device update` changes only the flags you actually pass; every other field
keeps its stored value.

### How often a device is polled

`--poll-interval` sets a device's own schedule, in seconds. A device without one
is polled at the server's `global.default_poll_interval` and keeps following it
when that changes.

```bash
netmon device add --name core-01 --address 198.51.100.252 --poll-interval 60
netmon device update core-01 --poll-interval 300   # every five minutes
netmon device update core-01 --poll-interval 0     # back to the global default
```

`--poll-interval 0` clears a device's own schedule rather than asking for a
zero-second one — the same reading `--limit 0` has elsewhere in this CLI. A
negative value is refused before any request is made.

The INTERVAL column of `device list` and the "Poll interval" line of
`device show` distinguish the two cases:

```
ID  NAME        ADDRESS          PORT  VERSION  INTERVAL       ADDED
1   core-01  198.51.100.252   161   2c       5m0s           3 days ago
2   core-02  198.51.100.253   161   2c       30s (default)  3 days ago
```

`5m0s` is the device's own schedule; `30s (default)` is the server's, inherited.
Under `--json` the same distinction is `poll_interval` being present or absent,
with `effective_poll_interval` always giving the number in force.

An interval shorter than the SNMP timeout budget — `snmp_timeout x
(snmp_retries + 1)`, 40s with the shipped configuration — is accepted with a
warning rather than refused. It is only self-defeating while the device is
already silent, and a healthy device answers in milliseconds:

```
$ netmon device add --name bastion --address 198.51.100.14 --poll-interval 5
Added device bastion (id 4) at 198.51.100.14, polled every 5s
Warning: poll interval 5s is shorter than this device's SNMP timeout budget of 40s ...
```

### Assigning MIBs and profiles

A device polls the union of its directly assigned MIBs and the MIBs of every
profile assigned to it. `netmon device mib list` marks which is which, and
whether each one is currently polled.

```bash
netmon device mib list core-01
netmon device mib list core-01 --search cisco
netmon device mib add core-01 IF-MIB CISCO-PROCESS-MIB
netmon device mib remove core-01 IF-MIB

netmon device profile list core-01
netmon device profile list core-01 --search core
netmon device profile add core-01 asr1000-core
netmon device profile remove core-01 asr1000-core
```

## Controlling what a MIB polls

Assigning a MIB is a coarse decision: a module can define hundreds of objects,
most of which nobody wants. Every assignment therefore carries an **enabled**
flag and a set of **filters**, and both are edited without detaching the MIB.
The same four commands exist for devices and for profiles.

```bash
netmon device mib enable  core-01 IF-MIB
netmon device mib disable core-01 CISCO-PROCESS-MIB
netmon device mib filter  core-01 IF-MIB --include-names '^ifHC(In|Out)Octets$'
netmon device mib cost    core-01 IF-MIB

netmon profile mib enable  asr1000-core IF-MIB
netmon profile mib disable asr1000-core CISCO-PROCESS-MIB
netmon profile mib filter  asr1000-core IF-MIB --max-oids 200
```

`disable` pauses a module without forgetting how it was configured: the
assignment and its filters survive, and `enable` brings them back unchanged.
`enable` and `disable` accept several MIBs at once.

A device's own assignment wins over the ones it inherits, so
`netmon device mib disable` stops a module that a profile enables. Editing a
profile's assignment reaches every device using that profile.

### Filters

| Flag | Matches against |
|------|-----------------|
| `--include-names` | object name; poll only what matches |
| `--exclude-names` | object name; never poll what matches |
| `--include-oids` | object OID; poll only what matches |
| `--exclude-oids` | object OID; never poll what matches |
| `--max-oids` | cap on how many objects the MIB contributes (`0` = no cap) |
| `--clear` | remove every filter, leaving the enabled state alone |

Patterns are Go regular expressions and excludes win over includes. Each flag
**replaces** the stored list for its field; fields you do not name keep their
current value, so one command states the whole rule for what it touches. Passing
`--clear` alongside other flags wipes first and then applies them, which is how
you say "these filters and nothing else".

A pattern that will not compile is rejected in the CLI, naming the offending
pattern, before any request is sent. Each flag takes one pattern verbatim —
repeat the flag to supply several. Patterns are never split on commas, so a
repetition like `if{2,4}` survives intact:

```bash
netmon device mib filter core-01 IF-MIB \
    --include-names '^ifHCIn' --include-names '^ifHCOut'
```

```bash
# Poll only the 64-bit interface counters, and never the null interface:
netmon device mib filter core-01 IF-MIB \
    --include-names '^ifHC(In|Out)Octets$' --exclude-names '^ifNull'

# Start over:
netmon device mib filter core-01 IF-MIB --clear
```

### Series cost

Object count is the wrong number to reason about a MIB with. Every pollable
table column costs one series *per table row*, so a module offering 624 pollable
objects produced 31,836 series on a live router. `cost` asks the server what a
module would actually cost on a device, using that device's observed tables:

```bash
netmon device mib cost core-01 IF-MIB
```

`device mib enable` prints the same figure for every MIB it is about to enable,
before it changes anything:

```
IF-MIB: ~960 series (estimated from a median of 24 instances across 3 tables observed on this device)
Enabled IF-MIB
```

A `~` marks an estimate; a count Prometheus already confirms is printed without
one. If the command's projected total exceeds **5000 series** it is refused, and
nothing is written until you re-run it with `--yes`. If the server cannot
produce an estimate the command says so and proceeds: not knowing the price is
not a reason to block a legitimate change.

`profile mib enable` quotes no cost and takes no `--yes`, because an estimate
needs a device's tables to size the multiplier and a profile has none. Check the
cost against a representative device first.

### Which objects are actually polled

Filters describe intent; `objects` reports the result. It lists a MIB's objects
with, for each, whether the device is polling it right now and why not when it
is not:

```bash
netmon device mib objects core-01 IF-MIB
netmon device mib objects core-01 IF-MIB --polled-only
netmon device mib objects core-01 IF-MIB --search octets --limit 0
```

```
Device:      core-01
MIB:         IF-MIB
Assignment:  enabled, 1 exclude (from profile)
Objects:     8 polled of 91 (8 shown by filter)
Series:      ~528

Objects:
  OID                   NAME         TYPE       KIND    ACCESS         POLLED  SERIES  REASON
  1.3.6.1.2.1.2.2       ifTable      -          table   not-accessible yes     528     polled
  1.3.6.1.2.1.2.2.1.10  ifInOctets   Counter32  column  read-only      no      0       covered-by-table-walk
  1.3.6.1.2.1.2.2.1.1   ifIndex      -          column  not-accessible no      0       not-accessible
```

The verdict comes from the server, computed by the same code the poller runs, so
it is what is actually collected rather than what the filters appear to say. The
reasons are `polled`, `covered-by-table-walk`, `not-accessible`, `not-current`,
`excluded-by-filter`, `not-included-by-filter`, `capped-by-max-oids` and
`module-disabled`.

Two surprises this view exists to explain:

- A **column of a walked table** is not polled individually. One walk of the
  table returns every column of every row, so asking for each column separately
  would turn one request into dozens. To stop collecting a table, exclude the
  *table*, not its columns.
- An object polled through a **table walk** carries the whole table's cost. That
  is why the table row shows 528 series and its columns show 0: the costs sum to
  the module total instead of counting the same data twice.

`--json` emits the whole plan, with `objects` filtered exactly as the table is
but the `total` and `polled` counts still describing the whole module. The list
is in the order the poller sees it, which is the order a `--max-oids` cap is
spent in.

Profiles have no `objects` command: a profile polls nothing itself, so there is
no "right now" for it to report. Ask a device that uses the profile.

## Profiles

A profile is a reusable bundle of MIBs. Assigning one profile to many devices
beats assigning the same MIB list repeatedly.

```bash
netmon profile list
netmon profile list --search core            # match name or description
netmon profile show asr1000-core
netmon profile create --name asr1000-core --description "Cisco ASR1000 core routers"
netmon profile update asr1000-core --description "Core routers"
netmon profile delete asr1000-core --force

netmon profile mib list asr1000-core
netmon profile mib list asr1000-core --search interface
netmon profile mib add asr1000-core IF-MIB SNMPv2-MIB CISCO-PROCESS-MIB
netmon profile mib remove asr1000-core IF-MIB
netmon profile mib disable asr1000-core CISCO-PROCESS-MIB

netmon profile devices asr1000-core          # which devices use it
netmon profile devices asr1000-core --search 198.51.100.
```

## MIBs

```bash
netmon mib list                              # newest 50 of the catalog
netmon mib list --search cisco               # match name, description or tags
netmon mib list --limit 0                    # the whole catalog
netmon mib list --available                  # only modules with data flowing
netmon mib list --tag cisco

netmon mib import mibs/asr1001/*.my --tags cisco,asr1000
netmon mib import mibs/asr1001/*.my --update  # refresh modules already imported
netmon mib tags IF-MIB --set standard,interfaces
netmon mib delete OLD-MIB --force
```

`mib import` accepts many files. Each file is uploaded under its own `--timeout`,
so one module that stalls costs that module rather than the batch. A file that
fails to parse is reported and the import continues with the rest; the command
exits non-zero and prints an `N imported, M updated, K failed` summary.

### Re-importing a module

A module that is already imported is refused with a 409, because importing it
again would otherwise mean deleting it first — and deleting a MIB drops it from
every device and profile that polls it, along with each assignment's enable
state and filters.

`--update` refreshes it in place instead:

```bash
netmon mib import --update mibs/asr1001/ENTITY-SENSOR-MIB.my
```

| Refreshed from the file | Kept |
| --- | --- |
| Objects and all their metadata: type, syntax, units, display hint, enum values, access, status, index columns | The module's id, so every assignment stays attached |
| Object count | Device and profile assignments |
| Description | Each assignment's enable state, include/exclude filters and `max-oids` |
| | Tags — change them with `netmon mib tags` |

Objects the module no longer defines are removed rather than left behind, so a
retired OID cannot stay in the polling set.

The refresh is safe to run while the poller is collecting, and a file that does
not parse is rejected before anything is written: the stored module and the file
it points at are left exactly as they were.

Use it to pick up a parser fix across every imported module:

```bash
netmon mib import --update mibs/*/*.my --json | jq -r '.[] | select(.error) | .file'
```

### Browsing objects

```bash
netmon mib show IF-MIB                       # module header plus objects
netmon mib show IF-MIB --search octets       # match name, OID or description
netmon mib show IF-MIB --pollable-only
netmon mib show IF-MIB --limit 0             # no limit (default is 50)
netmon mib objects IF-MIB --json | jq .      # objects only, for piping
netmon mib tables IF-MIB                     # conceptual tables and their columns
netmon mib tables IP-MIB --search ipAddress  # match table name, OID, entry or index
```

Search is the way through a large module. `IP-MIB` defines 293 objects and
`CISCO-ENTITY-VENDORTYPE-OID-MIB` defines 10,539, so scrolling is not a plan:

```bash
netmon mib objects CISCO-ENTITY-VENDORTYPE-OID-MIB --search cevChassisASR1001
```

Object listings are capped at `--limit` rows and the CLI always states how many
were held back — it never truncates silently.

`--pollable-only` and `--polled-only` are applied before `--search`, so the
counts in the footer describe the list you asked for.

## Monitoring

```bash
netmon monitor                               # one row per device
netmon monitor core-01                    # status, then current values
netmon monitor core-01 --top 40           # show 40 series; 0 shows all
netmon monitor core-01 --raw              # keep the stored metric names
```

The device view names every series through the MIBs the device polls. A sample
is stored under its MIB object's name, with the row it came from in the `oid`
label, so the CLI resolves that label back to an object and its instance
(see [metric naming](metric-naming.md)):

| Stored series | Shown as | Value |
|---------------|----------|-------|
| `snmp_ifInOctets{oid=".1.3.6.1.2.1.2.2.1.10.10"}` | `ifInOctets[10]` | `1107275976 octets` |
| `snmp_ifOperStatus{oid=".1.3.6.1.2.1.2.2.1.8.10"}` | `ifOperStatus[10]` | `up(1)` |
| `snmp_sysUpTime{oid=".1.3.6.1.2.1.1.3.0"}` | `sysUpTime` | `4d 6h 12m 3s` |

The brackets carry the table index, so one row per interface stays
distinguishable. Which object a sample belongs to is read from the `oid` label
rather than from the metric name, since every row of a column shares one name.

The index itself is rendered from the **decoded** values the exporter put in
labels, not from the raw sub-identifiers of the OID:

| Stored series | Shown as |
|---------------|----------|
| `snmp_cbgpPeer2AdminStatus{oid="....1.4.203.0.113.218"}` | `cbgpPeer2AdminStatus[cbgpPeer2Type=1,cbgpPeer2RemoteAddr=203.0.113.218]` |
| `snmp_ipAddressIfIndex{oid="....1.4.203.0.113.218"}` | `ipAddressIfIndex[ipAddressAddrType=1,ipAddressAddr=203.0.113.218]` |

An index of one column renders as the bare value — the object already names its
table, so `ifInOctets[10]` says everything `ifInOctets[ifIndex=10]` would. An
index of several names each column, because position alone does not say which of
four values is the peer address. This is the same decoding the web UI shows, so
the two views name a row the same way.

Which labels are the row's identity comes from the MIB's `INDEX` clause, carried
on each conceptual row object in the `index` field of `GET /api/mibs/:id`. That
is why the enrichment labels the exporter attaches beside the index — `ifname`,
`ifdescr`, `ifalias`, `ifadminstatus`, `ifoperstatus`, `iftype` — are never
mistaken for part of the row: they are not in the `INDEX` clause. A list of
label names to ignore would have to be kept in step with the exporter forever,
and would still be wrong for the next label it learns to attach.

When the row cannot be rendered whole — no MIB describes its conceptual row, or
a column the exporter did not decode — the raw sub-identifiers are used instead,
as in `cpmVirtualProcessHCMemAllocated[7.3623.688]`. Half an index is worse than
the raw one, which at least identifies the row exactly.

Values are rendered from the MIB definition: enumerations show their label,
`TimeTicks` become a duration, and a `UNITS` clause is appended.

A series no loaded MIB accounts for keeps its full `name{labels}` identity, and
a MIB module that will not load is skipped rather than failing the command — a
partial naming is still better than none. Use `--raw` to see exactly what is
stored, which is what `netmon metrics query` takes as its argument.

The MIB definitions are fetched once per device and reused, so `--refresh` costs
one metrics query per frame and nothing more.

## Metrics

```bash
netmon metrics query snmp_ifhcinoctets --range 1d
netmon metrics query snmp_ifhcinoctets --search 'ifindex="10"'
netmon metrics instant 'snmp_ifoperstatus{device="core-01"}'
netmon metrics instant '{device="core-01"}' --search ifInOctets
netmon metrics system core-01
netmon metrics system core-01 --fields sys_name,sys_descr
```

`--range` accepts `6h`, `1d`, `7d`, `30d` and `1y`; anything else is rejected
before a request is sent.

A metric spans one series per table row, so these listings are capped at 50 by
default. `--search` matches the rendered labels, which is how one interface is
picked out of a fleet-wide selector.

## Poller

```bash
netmon poller config                         # the configuration the poller consumes
netmon poller tables                         # SNMP tables discovered at runtime
netmon poller tables --search ifTable        # match name, entry OID, index or source
```

The learner keeps a table for every entry OID it has ever walked on any device,
so `poller tables` only grows; it is capped at 50 by default.

## Configuration

```bash
netmon config export --output netmon-config.tar.gz   # a self-contained bundle
netmon config export --document | jq .               # just the JSON, for review
netmon config import netmon-config.tar.gz            # show what would change
netmon config import netmon-config.tar.gz --yes      # actually change it
```

An export carries everything an operator configures — devices, profiles, MIB
assignments and every per-assignment filter — together with the MIB source files
those assignments reference, so it restores onto an empty instance on its own.

An import **replaces**: anything the file does not contain is deleted. It is a
dry run unless you pass `--yes`, it prints the exact add/update/delete diff
first, and applying saves the configuration it replaces and tells you where.

An export contains SNMP community strings in clear. `--output` writes mode 0600;
piping it anywhere else is your responsibility.

Full reference, including the JSON schema and the failure modes:
[Configuration Export and Import](operations/config-export-import.md).

## Scripting

`--json` makes every read command machine-readable:

```bash
# Devices that are not currently polling:
netmon device status --json | jq -r '.items[] | select(.connected == false) | .name'

# Every pollable object in a module — note --limit 0, or you get the first 50:
netmon mib objects IF-MIB --limit 0 --json | jq -r '.items[] | select(.pollable) | .name'

# Refuse to act on a partial answer:
netmon mib list --json | jq -e 'if .truncated then error("raise --limit") else .items[] end'
```

Listing commands wrap their rows in `.items` and report `total`, `matched`,
`shown` and `truncated` alongside, so a script can always tell a complete answer
from a subset. Commands that fetch a single object — `device show`,
`device status <device>`, `mib show` — keep their own shape.

Commands that change something answer in JSON too, so a script never has to parse
prose. They report one record per change:

```bash
netmon device mib add core-01 IF-MIB BRIDGE-MIB --json
# [{"action":"assigned","kind":"mib","ref":"IF-MIB","id":7}, ...]

netmon mib delete OLD-MIB --force --json
# {"action":"deleted","kind":"mib","ref":"OLD-MIB","id":42}

# Which files of a bundle failed to parse:
netmon mib import mibs/*.my --json | jq -r '.[] | select(.error) | .file'
```

Exit status is `0` on success and `1` on failure, with the reason on stderr.
`netmon health` uses the same convention, which is what makes it a usable gate.

## Value formatting

The API returns each object's presentation metadata — enumeration labels, the
`DISPLAY-HINT` clause, and `UNITS` — alongside its identity, so raw SNMP values
render as text without a second lookup. `internal/mib.FormatValue` implements
the rendering shared by the CLI, the web UI and the metrics exporter:

| Kind | Raw value | Rendered |
|------|-----------|----------|
| Enumeration | `1` | `up(1)` |
| TimeTicks | `109427307` | `12d 15h 57m 53s` |
| `PhysAddress` (`DISPLAY-HINT "1x:"`) | 6 octets | `00:1b:21:3c:4d:5e` |
| `DisplayString` (`"255a"`) | octets | `GigabitEthernet0/0/0` |
| `IpAddress` | 4 octets | `198.51.100.252` |
| `DateAndTime` | 8 or 11 octets | RFC 3339 timestamp |
| With `UNITS "milliseconds"` | `200` | `200 milliseconds` |

### Octet strings with no DISPLAY-HINT

Vendors define plenty of binary objects without a hint — engine IDs, opaque
cookies, packed protocol fields — so for those the content is the only evidence
available. `mib.FormatOctets` decides:

1. Trailing NUL octets are padding, not content, and are dropped. Agents
   routinely pad a fixed-width string field, and such a value is still text.
2. What remains is text when every octet is printable ASCII or one of `\t`,
   `\n`, `\r`. A NUL anywhere but the tail is content, and content that cannot
   be shown makes the whole value binary.
3. Otherwise the original octets, padding included, render as colon-separated
   hex.

| Raw value | Rendered | Why |
|-----------|----------|-----|
| `eth0\x00\x00` | `eth0` | trailing NULs are padding |
| `ab\x00cd` | `61:62:00:63:64` | embedded NUL is content |
| `\x04\x00` | `04:00` | binary |
| `\x00\x00` | `00:00` | binary, and never empty |
| *(no octets)* | *(empty)* | nothing arrived |

A value that was non-empty on the wire never renders as empty: an empty
rendering is indistinguishable from "the agent returned no value". This is not
hypothetical — `cbgpPeer2LastError` is `OCTET STRING (SIZE (2))` holding a BGP
error code and subcode, and `{0x04, 0x00}`, *Hold Timer Expired*, reached
Prometheus and the web UI as two invisible control characters.

The exporter renders status-column values through the same formatter, so the
`value` label of an info metric such as `snmp_cbgppeer2lasterror_info` reads
`04:00` rather than raw bytes. Table index labels go through it too, so an
index and a value made of the same octets are shown the same way.

Rendering is deliberately generic: `04:00` is honest and looks up in RFC 4271,
whereas a per-MIB error table buried in a shared formatter would be neither.

## Why a CLI and not a TUI

Every operation here is either CRUD or a live stream. CRUD maps cleanly onto
subcommands, and the live stream is handled by `--refresh`. The one genuinely
navigational task — exploring a MIB tree — is better served by search
(`mib show --search`) than by scrolling, and `--json` makes the CLI composable
with `jq` in a way a TUI cannot be. A TUI would also add a third surface to keep
in parity with the GUI and the CLI.
