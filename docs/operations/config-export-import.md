# Configuration Export and Import

> **Status: REFERENCE** · Scope: go-netmon · Last verified: 2026-10-09

Move a go-netmon instance's **logical configuration** — the devices it polls, the
profiles that bundle MIBs, and every per-assignment rule — between instances, or
keep a reviewable copy of it.

```bash
netmon config export --output netmon-config.tar.gz    # take a copy
netmon config import netmon-config.tar.gz             # show what would change
netmon config import netmon-config.tar.gz --yes       # actually change it
```

This is the logical path. It is not a replacement for `make backup`; see
[Relationship to `make backup`](#relationship-to-make-backup).

> **An export is a credential.** It carries SNMP community strings in clear, as
> does `GET /api/poller/config` on the same server. `netmon config export
> --output` writes mode 0600. Treat the file accordingly.

---

## What is and is not covered

| Covered — the export carries it | Not covered |
|---|---|
| Devices: name, address, port, SNMP version, community, poll interval | Metric history (Prometheus TSDB) |
| Profiles: name, description | The parsed MIB catalog (derived; rebuilt on import) |
| Device → profile assignments | Grafana dashboards (generated output) |
| Device → MIB direct assignments | `config.yaml`, `prometheus.yml` (deployment files) |
| Profile → MIB assignments | Poller runtime state |
| Every `MIBAssignment`: `enabled`, `include_oids`, `exclude_oids`, `include_names`, `exclude_names`, `max_oids` | |
| **The MIB source files** the configuration references | |

A device's record ID and creation time are **not** in the document. IDs are local
to one instance — the same module is id 12 here and id 140 there — so everything
is keyed by name. An import preserves the ID and creation time of a record whose
name it already knows, and allocates a new ID only for one it does not.

---

## The bundle

An export is a **gzipped tar** with a fixed layout:

```
netmon-config-20260810T154420Z.tar.gz
├── config.json          the configuration, human-readable and diffable
└── mibs/
    ├── IF-MIB.txt       the MIB source files config.json names
    ├── HOST-RESOURCES-MIB.txt
    └── ...
```

Two things, and no more:

* **`config.json` alone is the reviewable artifact.** It is what you read, diff
  between two instances and attach to a change request. `netmon config export
  --document` prints it by itself, and `GET /api/config/export?format=json`
  returns it. Every array in it is sorted by name and every empty list is
  omitted, so two exports of the same configuration are byte-identical and a
  diff shows only real differences.
* **`mibs/` is what makes the archive self-sufficient.** Restoring onto an empty
  instance must not be a scavenger hunt for MIB files.

`config.json` records each module's file and its SHA-256, so the manifest and the
payload are one document: the import knows what should be there, and detects a
file that has been damaged or swapped before it changes anything. `netmon-backup`'s
`manifest.json` likewise records a checksum per member.

### Why the source files and not the parsed catalog

The catalog is derived data — it is whatever *this build's* parser makes of these
files. Carrying it would pin a restore to the parser that ran at export time and
quietly reinstate every parsing bug fixed since. Carrying the sources and
re-parsing on import costs seconds and produces the catalog the target build
would have produced anyway, which is also what makes an import benefit from a
later parser fix.

### Size

By default the bundle carries every MIB source in the catalog. MIB sources are
text and compress well, so this is normally fine to carry.

`--referenced-mibs` (API: `?mibs=referenced`) narrows the bundle to only the
modules some device or profile actually assigns. On a fleet that assigns a small
part of a large catalog, that is much smaller — at the cost of not reproducing
the browsable catalog on the target.

The server refuses an upload over **64 MB compressed**, an archive that unpacks
to more than **256 MB**, more than **8192 files**, or any single file over
**10 MB** (the same cap the MIB upload endpoint uses). A deployment with
thousands of modules would hit the file-count limit long before the size limits;
if you get there, use `--referenced-mibs`, or use `make backup`, which has no
such ceiling because it copies volumes rather than streaming an archive through
an HTTP request.

---

## Example

```json
{
  "schema_version": 1,
  "exported_at": "2026-08-10T15:44:20Z",
  "mibs": [
    {
      "name": "HOST-RESOURCES-MIB",
      "file": "mibs/HOST-RESOURCES-MIB.txt",
      "sha256": "9f2b1c0e5d4a3b2c1d0e9f8a7b6c5d4e3f2a1b0c9d8e7f6a5b4c3d2e1f0a9b8c"
    },
    {
      "name": "IF-MIB",
      "file": "mibs/IF-MIB.txt",
      "sha256": "1a0b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f9",
      "tags": ["interfaces"]
    }
  ],
  "profiles": [
    {
      "name": "core",
      "description": "core routers",
      "mibs": [
        {
          "module": "IF-MIB",
          "enabled": true,
          "exclude_oids": ["^1\\.3\\.6\\.1\\.2\\.1\\.2\\.2\\.1\\.1$"],
          "include_names": ["^ifIn.*"],
          "max_oids": 50
        }
      ]
    }
  ],
  "devices": [
    {
      "name": "bastion",
      "address": "192.0.2.14",
      "port": 1161,
      "snmp_version": "v2c"
    },
    {
      "name": "core-01",
      "address": "192.0.2.252",
      "port": 161,
      "snmp_version": "2c",
      "community": "public",
      "poll_interval": 45,
      "profiles": ["core"],
      "mibs": [
        { "module": "HOST-RESOURCES-MIB", "enabled": true }
      ]
    }
  ]
}
```

### Field reference

| Field | Meaning |
|---|---|
| `schema_version` | Currently `1`. A document with any other value is **rejected whole**, not read for the parts that still parse. |
| `exported_at` | Provenance. Ignored on import and excluded from every comparison. |
| `mibs[]` | The modules this configuration depends on. A **prerequisite list, not a configuration to replace**: an import adds or refreshes these and never deletes a module the document omits. |
| `mibs[].file`, `mibs[].sha256` | Set when the bundle carries the module's source. Absent in a bare `config.json`, which then requires the module to be imported on the target already. |
| `mibs[].tags` | The operator's tags. Applied when the module is newly imported; a module already on the target keeps its own tags, because those are the target operator's. |
| `devices[].port` | Omitted or `0` becomes `161`, the same rule the device create endpoint applies. |
| `devices[].poll_interval` | **Absent means "inherit the poller's global default"**, which is different from pinning the number the default happens to be today. Present, it must be at least 1. |
| `devices[].profiles` | Profile names. Each must be defined by the same document. |
| `devices[].mibs`, `profiles[].mibs` | Assignments. `enabled: false` keeps the assignment and its filters while stopping the module being polled. |
| `*.include_*` / `*.exclude_*` | Go regular expressions matched against an object's OID or name. Excludes win over includes. |
| `*.max_oids` | Caps how many objects the assignment contributes; `0` is no cap. |

---

## Import: replace, not merge

**An import makes the instance an exact copy of the file.** A device, profile or
MIB assignment the file does not contain is **deleted**.

The one qualification, stated plainly: **MIB modules are never deleted.** The
catalog is a library the configuration draws on, not a piece of it. An imported
module that nothing references polls nothing and costs nothing, while deleting
one would throw away parsed metadata and the target operator's tags on the
strength of a document that never claimed to describe the catalog. So the
configuration is replaced exactly; the catalog is only ever added to or
refreshed.

### Dry run is the default

Nothing changes without `--yes` (CLI) or `?apply=true` (API). The precedents in
this repository are `netmon mib delete --force` and the `--apply` flag of
`netmon-backup restore`; the CLI follows the same dry-run-by-default idea, and the API follows
`POST /api/mibs/import?replace=true`.

A dry run computes the diff against the configuration **actually stored**, not
against anything a previous run reported:

```
$ netmon config import netmon-config.tar.gz
Configuration import — DRY RUN, nothing has been changed

MIB modules
  ACTION   MODULE              SOURCE
  refresh  IF-MIB              IF-MIB.txt
  import   HOST-RESOURCES-MIB  HOST-RESOURCES-MIB.txt

Profiles
  update  core (id 1)
            mibs[IF-MIB].include_names: [^ifIn.*] -> [^ifIn.* ^ifOut.*]
            mibs[IF-MIB].max_oids: 50 -> 0

Devices
  delete  bastion (id 4)
            192.0.2.14:161, v2c, poll inherit, 0 MIB(s)
  update  core-01 (id 1)
            address: 192.0.2.252 -> 192.0.2.253
            mibs[HOST-RESOURCES-MIB]: (none) -> enabled
  create  newswitch
            192.0.2.9:161, 2c, poll inherit, 1 MIB(s)

1 device deleted, 1 device updated, 1 device created, 1 profile updated, 1 MIB module imported, 1 MIB module refreshed.

Nothing has been changed. Re-run with --yes to apply.
```

Deletions are listed first, because that is the part you have to agree to. An
update lists the fields that move; a create or delete lists the record's summary,
since enumerating every field of a record that is wholly arriving or wholly going
says nothing extra.

A per-assignment filter change is reported field by field —
`mibs[IF-MIB].include_names` — because "IF-MIB changed" does not tell you whether
a device started polling more or stopped polling most of it.

The community string is reported like any other field. Redacting it would only
hide a change of credential from the person approving it; the document being
imported carries it in clear anyway.

**Importing the file you just exported is a no-op**: the plan is empty, and the
store is not written at all.

### Order of operations

An apply runs in this order, and the order is the whole safety argument:

1. **Validate everything.** The document's own fields, the bundle's checksums,
   and every module the configuration references. Nothing has been written, so a
   document that is going to be rejected costs nothing.
2. **Save the current configuration** to a file (see below). If that fails the
   import stops — the same rule `netmon-backup restore` applies when its
   safety backup fails.
3. **Import the MIB source files**, before any assignment refers to them. This is
   additive: it creates modules the instance lacks and refreshes ones it has, and
   deletes nothing.
4. **Replace the configuration** in one store batch.
5. **Push the result to the poller**, so it matches storage without a restart.

### Is it atomic?

**The destructive half is atomic. The additive half is idempotent and harmless if
interrupted.**

Step 4 is a single PebbleDB batch: every device, profile, assignment, name index
and counter is staged and committed once. It either all lands or none of it does,
and no reader can observe a half-applied fleet. All validation completes before
the batch is opened.

Step 3 is not atomic across modules — each module is its own parse-and-store. That
is deliberate, and it is why it comes third:

* A failure there aborts before step 4, so **the configuration is not touched.**
* Modules imported before the failure remain imported. That changes nothing about
  what is collected: a module nothing references is not polled.
* The error names the module that failed and says how many were imported before
  it. The CLI and API report it verbatim.

So a partial apply cannot leave the configuration half-written. The only residue
a failed import can leave is extra modules in the catalog, and it tells you which.

### Validation, in order, before anything is written

| Order | Check | Failure |
|---|---|---|
| 1 | `schema_version` is one this build reads | `ConfigSchemaError` — the document is refused **whole**, so a file from a newer build is never reported as a list of individually invalid fields |
| 2 | Names non-empty and unique; addresses non-empty; ports 0–65535; SNMP version one of `1, 2c, 3, v1, v2c, v3`; poll interval ≥ 1; every filter compiles as a Go regular expression; `max_oids` ≥ 0; no module assigned twice to one owner; every profile a device names is defined by the document | `ConfigValidationError` carrying **every** problem, each naming scope, subject, field, value and reason — an operator fixing a file by hand should see the whole list once, not discover it one failed import at a time |
| 3 | Bundle members are present and match their recorded SHA-256 | `BundleEntryError` / `BundleChecksumError` |
| 4 | Every module the configuration **assigns** is either carried by the bundle or already imported here | `ConfigMIBMissingError`, naming every missing module |
| 5 | The plan is computed | — |

Only after all five does anything get written.

A module the configuration references but that neither the bundle carries nor the
target holds **fails the whole import and names the module**. It is never silently
dropped: dropping it is precisely how a device ends up polling 38 OIDs instead of
155 with nobody noticing.

A module listed in `mibs[]` that nothing assigns and the target does not have is
*not* an error. A document exported from a large instance lists its whole catalog;
demanding all of it would make every restore onto a smaller instance fail for no
reason.

### The safety export

Before an apply changes anything, the current configuration is exported to a full
bundle:

```
<directory of the web UI record store>/config-backups/pre-import-<UTC timestamp>.tar.gz
```

Under the shipped `docker-compose.yml` (`-db=/app/data/webui/webui.db`) that is:

```
/app/data/webui/config-backups/pre-import-20260810T154420Z.tar.gz
```

which lives inside the `webui-data` volume, so it survives a container being
recreated and is picked up by `make backup` with no extra configuration. The
directory can be overridden with `api.Config.ConfigBackupDir`.

The path is in the report — `safety_export` in JSON, printed by the CLI — so
undoing a wrong import is:

```bash
netmon config import /app/data/webui/config-backups/pre-import-20260810T154420Z.tar.gz --yes
```

It is a full bundle rather than a note because recovery should be one command
against one file, with nothing else needing to be present. The naming follows
`netmon-backup restore`, whose safety backup is `pre-restore-<timestamp>`.

These accumulate. Pruning them is an operator concern, exactly as it is for
`backups/`.

### The poller stays consistent

After the batch commits, every device now in storage is pushed to the poller —
newly created ones with `RegisterDevice`, the rest with `UpdateDevice` — and every
deleted one is removed with `RemoveDevice`. This is the same machinery a single
edit through the API uses (`syncDeviceWithPoller`), so there is one way a device
reaches the poller and not two.

Every device is pushed, not only the ones whose own record changed: a profile's
assignments changing alters what an untouched device polls. Each push is bounded
by `PollerSyncBudget` and its outcome is logged rather than returned — the
configuration is already durable, and the poller picks up anything missed on its
next configuration sync. **No restart is required.**

---

## CLI

### `netmon config export`

```
netmon config export [--output FILE] [--document] [--referenced-mibs]
```

| Flag | Effect |
|---|---|
| `-o`, `--output FILE` | Write to a file (mode 0600) instead of stdout |
| `--document` | Print `config.json` alone instead of a bundle |
| `--referenced-mibs` | Carry only the modules some device or profile assigns |

Output goes to **stdout by default so it pipes**. Because a bundle is a
compressed archive, the command refuses to write one to a terminal and tells you
to use `--output`, redirect, or `--document`. (The check is whether stdout is a
character device, which also catches `/dev/null`; use `--output /dev/null` if you
really want that.)

`--json` applies to the confirmation, not the payload: with `--output` it emits
`{"action":"exported","kind":"config","ref":"<path>"}`. Without `--output` the
payload *is* the output.

```bash
netmon config export > netmon-config.tar.gz
netmon config export --document | jq '.devices[] | {name, address}'
diff <(netmon --server old config export --document) \
     <(netmon --server new config export --document)
```

### `netmon config import`

```
netmon config import <file|-> [--yes]
```

`-` reads the bundle or document from stdin, so one instance can be copied onto
another in a pipeline:

```bash
netmon --server http://old:8080 config export \
  | netmon --server http://new:8080 config import - --yes
```

`--json` emits the whole report instead of the table.

### Timeouts

Both commands default to a **15 minute** deadline instead of the usual 60
seconds, because an import re-parses every MIB module the bundle carries. An
explicit `--timeout` still wins.

---

## API

Everything above is reachable over HTTP. This is the contract.

### `GET /api/config/export`

| Parameter | Values | Default |
|---|---|---|
| `format` | `bundle`, `json` | `bundle` |
| `mibs` | `all`, `referenced` | `all` |

**`200 OK`**

| | `format=bundle` | `format=json` |
|---|---|---|
| `Content-Type` | `application/gzip` | `application/json` |
| Body | the tar.gz bundle | `config.json` |
| `Content-Disposition` | `attachment; filename="netmon-config-<UTC>.tar.gz"` | `...json"` |
| `Content-Length` | set | set |
| `X-Content-Type-Options` | `nosniff` | `nosniff` |

`X-Netmon-Missing-MIB-Sources` is present, as a comma-separated list of module
names, when a module is in the catalog but its source file could not be read. The
bundle still exports; those modules are recorded as prerequisites with no `file`,
and a restore onto an instance that lacks them will fail naming them. The export
says so at the time it is taken rather than producing an archive that quietly
cannot restore.

Because the whole artifact is built before anything is written, a failure is a
`500` with a JSON error body, never a truncated download that looks complete.

**`400 Bad Request`** — unknown `format` or `mibs` value. Body is a
`ConfigErrorResponse` with `kind: "bundle"`.

A browser can download an export with nothing but a link:

```html
<a href="/api/config/export" download>Export configuration</a>
```

### `POST /api/config/import`

| Parameter | Values | Default |
|---|---|---|
| `apply` | `true`, `false` | `false` — **dry run** |

**Request body.** Either form is accepted, and the **bytes decide** what it is —
a gzip magic number means a bundle, anything else is parsed as `config.json`:

* Raw body, with `Content-Type: application/gzip`, `application/x-tar`,
  `application/octet-stream` or `application/json`.
* `multipart/form-data` with the archive in a field named **`file`** — which is
  what a `<input type="file">` plus `FormData` produces, and matches
  `POST /api/mibs/import`.

Maximum 64 MB.

```js
// Either of these works.
await fetch('/api/config/import?apply=true', { method: 'POST', body: file });

const form = new FormData();
form.append('file', file);
await fetch('/api/config/import', { method: 'POST', body: form });
```

**`200 OK`** — the plan was computed (dry run) or applied. Body:

```json
{
  "applied": false,
  "schema_version": 1,
  "changed": true,
  "counts": {
    "devices_created": 1, "devices_updated": 1, "devices_deleted": 1,
    "profiles_created": 0, "profiles_updated": 1, "profiles_deleted": 0,
    "mibs_imported": 1, "mibs_refreshed": 1, "mibs_present": 0
  },
  "mibs": [
    { "module": "HOST-RESOURCES-MIB", "action": "import",  "file": "HOST-RESOURCES-MIB.txt" },
    { "module": "IF-MIB",             "action": "refresh", "file": "IF-MIB.txt" }
  ],
  "profiles": [
    {
      "action": "update", "name": "core", "id": 1,
      "fields": [
        { "field": "mibs[IF-MIB].max_oids", "from": "50", "to": "0" }
      ]
    }
  ],
  "devices": [
    { "action": "delete", "name": "bastion", "id": 4, "detail": "192.0.2.14:161, v2c, poll inherit, 0 MIB(s)" },
    {
      "action": "update", "name": "core-01", "id": 1,
      "fields": [
        { "field": "address", "from": "192.0.2.252", "to": "192.0.2.253" },
        { "field": "mibs[HOST-RESOURCES-MIB]", "from": "", "to": "enabled" }
      ]
    },
    { "action": "create", "name": "newswitch", "detail": "192.0.2.9:161, 2c, poll inherit, 1 MIB(s)" }
  ],
  "safety_export": ""
}
```

* `applied` — `false` for a dry run, `true` when `?apply=true` succeeded. **This
  is the only structural difference between the two.**
* `changed` — `false` when the instance already matches the file. `devices` and
  `profiles` are then empty.
* `mibs[].action` — `import` (bundle carries it, target lacks it), `refresh`
  (bundle carries it, target has it), `present` (bundle does not carry it, target
  has it). `missing` never appears in a `200`; it is a `409`.
* `devices[].action` / `profiles[].action` — `create`, `update` or `delete`,
  ordered deletions first, then updates, then creates, each group by name.
  `id` is the stored record's ID, present on `update` and `delete`.
  `fields` is present on `update`; `detail` on `create` and `delete`.
* `safety_export` — the path of the bundle holding the replaced configuration.
  Present only on an apply; omitted otherwise.

**Failures** all carry the same JSON body, `ConfigErrorResponse`:

```json
{
  "kind": "missing_mibs",
  "message": "config: 2 MIB module(s) referenced by this configuration are not imported ...",
  "problems": [],
  "missing_modules": ["CISCO-MEMORY-POOL-MIB", "ENTITY-MIB"],
  "schema_version": 0,
  "supported_schema_version": 0
}
```

Switch on `kind`, not on the message.

| Status | `kind` | Cause | What the client should show |
|---|---|---|---|
| `400` | `bundle` | Not a readable archive or document; a member outside the `mibs/` layout; a file over the size cap; a bad `apply`, `format` or `mibs` parameter; a MIB source that does not match its recorded checksum | "This file is not a usable configuration export." Show `message`. |
| `400` | `schema` | `schema_version` is not one this build reads. `schema_version` and `supported_schema_version` are set | "This file was written by a different version of go-netmon." Do not offer to edit it. |
| `400` | `validation` | The document's own fields. **`problems[]` is populated** — each has `scope` (`document`/`device`/`profile`/`mib`), `subject`, `field`, `value`, `reason` | A list, one row per problem, pointing at the record and field to fix. |
| `400` | `mib_import` | A MIB source the bundle carries could not be parsed. `message` names the module and says how many were imported before it, and that **the configuration was not changed** | Show `message` verbatim. |
| `409` | `missing_mibs` | Modules the configuration assigns are neither carried by the file nor imported here. **`missing_modules[]` is populated** | "Import these MIB modules first, or use an export bundle that carries them", listing them. This is a prerequisite to satisfy, **not a document to correct** — which is exactly why it is not a 400. |
| `500` | `safety_export` | The configuration about to be replaced could not be saved, so the import stopped. Nothing was changed | Show `message`. Retrying will not help until the server's backup directory is writable. |
| `500` | `storage` | The record store failed, or the server has no MIB parser | Generic failure. |

A dry run and an apply produce the same status codes for the same file: a
document that survives a dry run is a document the apply accepts for the same
reasons.

### Timeouts

`POST /api/config/import` extends its own connection deadline to
`ConfigImportBudget` (10 minutes) with `http.ResponseController`, because parsing
a few hundred MIB modules takes far longer than the server's
`ServerWriteTimeout` of 15 s. The rest of the server keeps its tight budget. A
client should allow for a long request; the SDK and CLI default to 15 minutes.

---

## SDK

```go
import "github.com/jeremyhahn/go-netmon/pkg/client"

api, _ := client.New("http://198.51.100.91:8082")

// Export. Bytes, not a struct: the file is the contract, and an SDK build
// older than the server must not silently drop a field it has not been
// taught about.
bundle, err := api.ExportConfigBundle(ctx, client.ConfigExportOptions{})
os.WriteFile("netmon-config.tar.gz", bundle, 0o600)

// Read the document for inspection.
doc, err := api.ExportConfig(ctx, client.ConfigExportOptions{})

// Dry run, then apply.
plan, err := api.ImportConfigBundle(ctx, bundle, client.ConfigImportOptions{})
if plan.Changed {
    report, err := api.ImportConfigBundle(ctx, bundle,
        client.ConfigImportOptions{Apply: true})
    log.Printf("previous configuration saved to %s", report.SafetyExport)
}

// The refusal carries its detail.
var configErr *client.ConfigDocumentError
if errors.As(err, &configErr) {
    if configErr.MissingMIBs() {
        log.Printf("import these first: %v", configErr.MissingModules)
    }
    for _, p := range configErr.Problems {
        log.Printf("%s", p.Error())
    }
}
```

`ImportConfig` takes a `client.Config` for callers that build one in memory;
prefer `ImportConfigBundle` for anything read from disk.

---

## Restoring onto a fresh instance

The whole procedure. It needs the bundle and nothing else.

```bash
# On the source instance
netmon --server http://old:8080 config export --output netmon-config.tar.gz

# Stand up the new deployment
docker compose up -d

# Confirm it is empty and reachable
netmon --server http://new:8080 config export --document | jq '.devices | length'   # 0

# See exactly what the import will do. Changes nothing.
netmon --server http://new:8080 config import netmon-config.tar.gz

# Apply
netmon --server http://new:8080 config import netmon-config.tar.gz --yes

# Verify: this must print an empty plan
netmon --server http://new:8080 config import netmon-config.tar.gz
```

The MIB modules are imported during the apply, before any assignment refers to them. You
do **not** import MIBs first by hand.

The last step is the check that matters: re-importing the same file must report
`changed: false`. If it does not, the target did not end up matching the file, and
the diff says where.

### What this does not restore

* **Metric history.** A fresh instance starts collecting from the moment its
  devices are registered. Use `make restore` if you need the TSDB back.
* **Grafana dashboards.** Regenerated from the MIB catalog and device
  assignments, both of which the import restores.
* **`config.yaml` / `prometheus.yml`.** Deployment files, not configuration.
  `make backup` copies them; a logical import does not touch them.

---

## Relationship to `make backup`

They solve different problems and neither replaces the other.

| | `netmon config export/import` | `make backup` / `make restore` |
|---|---|---|
| **What** | Logical configuration + MIB sources | Whole Docker volumes, byte for byte |
| **Includes metric history** | No | Yes |
| **Downtime** | None | The stack is stopped for the copy (a cold backup) |
| **Can run while the web UI is up** | Yes — it *is* the web UI | No, it stops the web UI too |
| **Readable / diffable** | Yes | No, opaque tarballs |
| **Survives a storage-format change** | Yes, it is re-imported through the API | No, it is the on-disk format |
| **Portable to another instance** | Yes | Only to a matching compose project |
| **Restores partially** | No, it is all-or-nothing by design | Yes, `--only` / `--skip` per component |

Use the logical path to move configuration between instances, to review what
changed, and to recover from a bad configuration edit. Use the physical path for
disaster recovery of a whole deployment including metrics.

They compose: `netmon-backup backup` already writes a REST logical export into
`logical/` on every physical backup as a review artifact — see
[Backup and Restore](backup-restore.md). `netmon-backup restore` never applies
it; that is what `netmon config import` is for.

The safety export an apply takes lands inside the `webui-data` volume, so it is
itself captured by the next `make backup`.

---

## Related

* [Backup and Restore](backup-restore.md) — the physical path
* [CLI reference](../cli.md)
* [Configuration Synchronization](../CONFIG_SYNC.md) — how the poller fetches its
  configuration from the web UI
