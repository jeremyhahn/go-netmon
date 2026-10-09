# Backup and Restore

> **Status: REFERENCE** · Scope: go-netmon · Last verified: 2026-10-09

Tooling to back up a deployed go-netmon stack, redeploy it from scratch, and
restore the data exactly as it was.

```
make backup                                   # back everything up
make backup-verify FROM=backups/netmon-...    # check an archive, change nothing
make restore FROM=backups/netmon-...          # print the plan, change nothing
make restore FROM=backups/netmon-... YES=1    # restore volumes and config
make restore FROM=backups/netmon-... YES=1 ALLOW_TREE_OVERWRITE=1
                                              # also replace ./mibs (see below)
make restore-help                             # explain the restore flags
```

> **A bind-mounted MIB tree is covered, and restoring it needs a second flag.**
> The `mibs` component is a host directory bind-mounted at `/app/mibs`, not a
> Docker volume, so restoring it writes into your checkout.
> `ALLOW_TREE_OVERWRITE=1` is required whenever that directory already holds
> files. **The shipped `docker-compose.yml` does not bind-mount one** (only
> `docker-compose.prod.yml` mounts `./mibs:/app/mibs`), so against it a backup
> must be told `--skip mibs` or `--mib-tree DIR`. See
> [The MIB source tree](#the-mib-source-tree).

The tool is `netmon-backup`, built by `make build-backup` into
`bin/netmon-backup`. It is a single static binary: the only thing it needs at
recovery time is `docker` and the binary itself. Copy `bin/netmon-backup` off the
host alongside your backups and a restore needs no Go toolchain, no `jq`, and no
shell — which matters precisely because a disaster that takes out the deployment
may well have taken out the build environment too.

> **Why a binary and not shell.** Every path this tool creates, empties or
> overwrites is resolved by the kernel relative to an open directory handle
> (`os.Root`, i.e. `openat2`), so a path that would leave the chosen directory is
> refused by the operating system rather than sanitized and used anyway. The
> shell implementation this replaces expressed the same intent as string
> comparisons against a list of directories it hoped never to be pointed at; its
> test harness translated a container's `find /dst -mindepth 1 -delete` into a
> delete on the host, where a wrong path was a delete aimed at the operator's
> filesystem. (`scripts/backup.sh` and `scripts/restore.sh` are still in the
> tree and still have their own tests, but no Makefile target calls them; the
> targets above run `netmon-backup`.) That class of mistake is now
> unrepresentable in the binary, and a test
> (`TestNothingBypassesTheContainment`) fails the build if any code in the tool
> reaches the filesystem another way or hands a command line to a shell.

## The consistency guarantee

**Backups are cold.** `netmon-backup backup` stops every service in the compose project,
copies the volumes at rest, then starts exactly the services that were running.
Downtime is the length of the copy, so it grows with the size of the volumes.

There is no hot mode. Both storage engines in this stack are unsafe to copy
while running, and neither offers a usable online snapshot:

| Store | Used by | Why a live copy is unsafe | Online snapshot? |
|---|---|---|---|
| PebbleDB | web UI records, MIB catalog, poller MIB metadata | Single-writer with an exclusive `LOCK`. A copy can catch a `MANIFEST` referencing sstables that were not yet durable, or a half-written WAL tail, which replays into a corrupt store on open. | `pebble.DB.Checkpoint()` exists in-process, but neither `cmd/webui` nor `cmd/go-netmon` exposes an endpoint that calls it. Nothing to ask over the wire. |
| Prometheus TSDB | metric history | The head block is memory plus a WAL; on-disk state is not self-consistent at an arbitrary instant. | `POST /api/v1/admin/tsdb/snapshot` requires `--web.enable-admin-api`. **This deployment does not pass it** — `docker-compose.yml` passes `--web.enable-lifecycle` and `--web.enable-remote-write-receiver` only. The endpoint answers `{"errorType":"unavailable","error":"admin APIs disabled"}`. |

A hot copy of either store is not merely risky, it is *unverifiable*: the damage
is invisible until the restore, which is the worst moment to discover it. So the
tool stops the stack instead, and the archive's consistency becomes a property
of the method rather than of timing luck.

Stopping the whole project at once also buys a single consistency point **across**
volumes. Web UI device records reference MIB ids in the catalog, whose source
files live in the poller's volume. Copying those volumes at different instants
can produce an archive whose devices reference MIBs it does not contain.

> **If you want a hot Prometheus path**, add `--web.enable-admin-api` to the
> prometheus service in `docker-compose.yml` and the snapshot API becomes
> available. That still leaves the three Pebble stores with no safe online
> mechanism, so the stack as a whole would still need stopping.

## What is backed up

Physical volume contents are the default and the thing that actually restores.
A logical REST export is included alongside as a portable, reviewable artifact.

| Component | Where it lives | Contents |
|---|---|---|
| `config` | bind-mounted files | `config.yaml`, `prometheus.yml` |
| `mibs` | **host directory** (read from the compose bind mount at `/app/mibs`) | MIB source tree bind-mounted into the services; not present in the shipped `docker-compose.yml` |
| `webui` | `<project>_webui-data` | devices, profiles, MIB assignments, poll intervals, MIB catalog |
| `poller` | `<project>_go-netmon-data` | poller MIB metadata store, uploaded MIB files |
| `metrics` | `<project>_prometheus-data` | all metric history |
| `grafana` | `<project>_grafana-data` | Grafana database and dashboards |

### The MIB source tree

Where a deployment bind-mounts a host MIB directory at `/app/mibs` (for example
`./mibs:/app/mibs` in `docker-compose.prod.yml`), that directory lives in **no
Docker volume**, so a volume-only backup would not carry it:

* It is the source the imported modules were parsed from. The archive carries
  the *parsed catalogs* (`mib.db`, `mib-storage/`) and the files under
  `uploaded/` on the poller volume, but those are derived artifacts. A restore
  that brings back a catalog whose sources are gone resolves fine until
  something triggers a re-parse or a re-import, and then cannot.
* Files that were added to the tree by hand and never committed are recoverable
  from nowhere else, so the backup carries the directory itself.

The shipped `docker-compose.yml` mounts no host MIB directory: the web UI and
poller share MIB storage through the `go-netmon-data` volume instead. Against
that file there is no tree to find, the backup stops with an error naming
`--skip mibs` and `--mib-tree DIR`, and the rest of this section applies only
once one of them is given or a compose file with the mount is used
(`NETMON_COMPOSE_FILE`).

#### Why its own component, not part of `config`

`config` carries `config.yaml` and `prometheus.yml`: a few KB of text, restored
as two individually named files. Folding 22 MB of MIBs into it would silently
change what `--only config` means and what it costs for anyone already using it.

The two also restore differently in kind. `config` replaces two files an
operator listed by name; `mibs` replaces a whole directory tree, wholesale, in
the operator's working copy. That deserves its own selector, its own manifest
member and its own gate, so `mibs` is a component in its own right and is
recorded in `trees[]` rather than `volumes[]` — nothing in a recovery should be
able to mistake a host path for a Docker volume.

#### Overwrite semantics: replacement, not merge

Restoring `mibs` **empties the destination directory and then extracts into
it**. Files added since the backup are deleted, not kept.

This matches `volume_import` and the exact-copy semantics chosen for `config`,
and it matters more for MIBs than it might look: a module left behind from a
newer tree keeps being parsed alongside the restored one, so the deployment
resolves against a set matching neither the backup nor what the operator had.
A half-matching MIB path is exactly the failure this tooling exists to avoid.

#### The gate

| Destination state | What happens |
|---|---|
| Empty or absent | Created and filled. **No extra flag** — nothing is destroyed. This is the fresh-host disaster path. |
| Holds files | `--allow-tree-overwrite` (`ALLOW_TREE_OVERWRITE=1`) is **required in addition to `--apply`**. |

Without the flag the restore **fails**, before the stack is stopped and before
the safety backup is taken, so refusing costs nothing but a re-run. It does not
skip the component and carry on: that would print "restore complete" over a
deployment whose MIB sources were never put back, which is the same half-restore
in a different disguise.

The plan always shows the destination, that it is a host directory, how many
files are there now, and whether the gate is satisfied (illustrative output;
the paths and counts are from one deployment):

```
    tree    /home/user/sources/go-netmon/mibs
              holds:   MIB source tree bind-mounted read-only into the poller and web UI
              source:  compose bind mount at /app/mibs
              NOTE:    this is a HOST DIRECTORY in your working tree,
                       not a Docker volume
              current: 212 files, 20.7 MB
              restore: 222 entries from the backup
                       (the directory is EMPTIED first, then extracted
                       into - existing files are deleted, not merged)
              gate:    REFUSED - replacing 212 existing files
                       requires --allow-tree-overwrite
```

The safety backup covers it too, so even an intended overwrite is reversible:
`pre-restore-<timestamp>/trees/mibs.tar.gz` holds the tree as it was. (When the
destination is empty there is nothing to preserve, so `mibs` is omitted from the
safety backup and the restore says so rather than failing.)

#### Where the path comes from

The host directory is **not** assumed to be `<repo>/mibs`. It is read back out
of the rendered compose file — the bind mount whose target is `/app/mibs` — so a
deployment that keeps its MIBs somewhere else is handled without changing this
tooling:

* Both services must agree on the source. If they name different directories the
  backup is refused, because archiving one and restoring over the other would
  silently give the two services different MIB sources.
* If no service mounts anything at `/app/mibs`, that is fatal rather than
  guessed at. The error offers `--skip mibs` for deployments that genuinely have
  no MIB tree, and `--mib-tree /path` to point at one explicitly.
* `netmon-backup restore --mib-tree DIR` (`MIB_TREE=DIR`) sends the tree somewhere else
  entirely — useful for inspecting it before committing to an overwrite.
* Destinations that a wholesale delete must never be aimed at — `/`, `/usr`,
  `$HOME`, the repository root, anything one level from the filesystem root — are
  refused outright, in both backup and restore.

### Physical *and* logical, and why

A volume tarball is exact, includes metric history, and restores byte-for-byte —
but it is opaque and tied to the on-disk format of the version that wrote it. A
logical export through the REST API is portable, diffable and survives a schema
change — but it cannot carry the TSDB's metric history.

Since "metrics" is a stated requirement, **physical is the default and is what
`netmon-backup restore` applies**. The logical export is written to `logical/` on every
backup as a review and disaster artifact: it is what you read to answer "what
was configured on the 9th?", and what you would replay by hand if a future
schema change made an old volume unreadable. `netmon-backup restore` never applies it.

If the web UI is unreachable the logical export is skipped, the manifest records
`logical.status: "unavailable"`, and the physical backup continues — the part
that restores does not depend on the part that documents.

> **There is now a logical path that *does* restore.** `netmon config export`
> produces a self-contained bundle — `config.json` plus the MIB source files it
> references — and `netmon config import` replays it onto any instance, with a
> dry run by default and a safety copy taken before it applies. It carries no
> metric history, so it does not replace `make restore`; it is what you use to
> move a configuration between instances, to review what changed, or to undo a
> bad configuration edit without stopping the stack. See
> [Configuration Export and Import](config-export-import.md).
>
> The `logical/` directory written here is still the older per-endpoint dump,
> unchanged, and `netmon-backup restore` still never applies it.

### Deliberately NOT backed up

* **Docker images.** The manifest records image names and ids; rebuilding or
  pulling them is a deployment concern. Restoring data does not pin an image.
* **`docker-compose.yml`.** Copied to `config/reference/` for provenance and
  recorded by SHA-256 in the manifest, but never restored. Overwriting the
  deployment definition beneath a running deployment is a deployment action, not
  a data restore.
* **`./grafana/provisioning`, `./data`.** Build inputs and provisioning
  definitions that live in the repository, not runtime state. (`./mibs` used to
  be listed here. It is now backed up — see
  [The MIB source tree](#the-mib-source-tree).)
* **Generated dashboards** (`grafana/provisioning/dashboards/_generated`). Output,
  regenerated from the MIB catalog and device assignments that *are* backed up.
* **Container logs.**
* **Volumes not labelled with the compose project.** See "Safety model".

## Archive layout

A backup is a **directory**, not a single file:

```
backups/netmon-20260810T145103Z/
├── manifest.json               what was backed up, when, from what
├── checksums.sha256            SHA-256 of every other file in the tree
├── config/
│   ├── config.yaml
│   ├── prometheus.yml
│   └── reference/
│       └── docker-compose.yml  recorded, never restored
├── logical/
│   ├── devices.json            device records
│   ├── profiles.json           profile records
│   ├── mibs.json               MIB catalog listing
│   ├── device-mibs.json        per-device MIBs, profiles, assignment records
│   ├── profile-mibs.json       per-profile MIBs and assignment records
│   └── poller-config.json      config the poller is actually running
├── trees/
│   └── mibs.tar.gz             the MIB source tree (a HOST directory)
└── volumes/
    ├── webui.tar.gz            one gzipped tar per component
    ├── poller.tar.gz
    ├── metrics.tar.gz
    └── grafana.tar.gz
```

`trees/` is separate from `volumes/` on purpose: its members restore to paths on
the host, not into Docker volumes, and during a recovery nothing should have to
infer which is which from a filename.

A directory rather than one monolithic archive, because:

* **Selective restore stays cheap.** Restoring only metrics is a file read, not
  a scan through one large stream to find one member.
* **Damage is localised.** Per-member checksums say *which* component is bad.
* It rsyncs and dedupes offsite; a single recompressed blob does neither.

For transport, `tar -C backups -cf netmon-backup.tar netmon-20260810T145103Z`.
`netmon-backup restore` takes the extracted directory.

Members are named after the **component** (`poller.tar.gz`), not the compose
volume. The compose volume `go-netmon-data` is name-identical to an unrelated
stray volume on the deployment host, and a filename indistinguishable from an
object the tooling refuses to touch is a filename waiting to be misread during a
recovery.

### Manifest

`manifest.json` records everything needed to detect a restore into an
incompatible deployment:

| Field | Purpose |
|---|---|
| `schema_version` | Restore refuses an archive newer than it understands. **Now 2**: schema 1 had no `trees[]`, and a schema-1 restore handed a schema-2 archive would ignore the MIB tree and still report success, so it must refuse instead. This tool still restores schema-1 archives, which simply carry no tree. |
| `backup_name`, `created_at`, `created_by`, `tool` | Provenance |
| `consistency` | `cold-offline` — how the copy was taken |
| `components` | Which components the archive actually holds |
| `compose.project` | **Restore refuses a project mismatch** |
| `compose.file`, `compose.file_sha256`, `compose.volumes` | The deployment shape it was taken from |
| `images[]` | `service`, `image`, `image_id` — drift is reported on restore |
| `volumes[]` | `component`, `compose_volume`, `docker_volume`, `member`, `bytes`, `entries`, `sha256` |
| `trees[]` | `component`, `source_path`, `mount_target`, `member`, `bytes`, `entries`, `files`, `source_bytes`, `sha256` |
| `config_files[]` | `path`, `sha256`, `restorable` |
| `logical.status`, `logical.counts` | Whether the logical export succeeded, and its record counts |
| `host.docker_version`, `host.compose_version` | Environment it was taken on |

## Restore safety gates

Restoring is destructive, so `netmon-backup restore` is built so every way it fails leaves
current data intact. In order:

1. **Verify the archive first.** Manifest parses and has required fields;
   `checksums.sha256` verifies the whole tree; then every member — volume
   members and tree members alike, in one pass over one list — is checked
   against the manifest's size and SHA-256 *and fully decompressed* to confirm
   its entry count. All of this runs before the first Docker call, so a damaged
   archive cannot get as far as stopping the stack. The full decompression is
   the point: a truncated member passes a "does the file exist" check and then
   fails halfway through wiping a live volume.
2. **Project match.** The manifest's `compose.project` must equal the target
   project, or the restore is refused with both names printed. `--force-project`
   overrides, loudly.
3. **Ownership.** The target project must contain no container this repository's
   compose file did not create, and every volume must carry the project's
   compose label.
4. **Compatibility.** The target compose file must define every volume the
   selected components need. Image drift is reported as a warning, not enforced —
   restoring onto a newer build is a normal upgrade path.
5. **Explicit confirmation.** Without `--apply` the plan is printed — every volume
   to be overwritten, what it holds, its current size, and the entry count coming
   in — and the command exits non-zero having changed nothing. (Precedent:
   `netmon mib delete --force`.)
6. **Safety backup.** The current state is backed up first, scoped to exactly the
   components about to be destroyed, so a restore of the wrong archive is itself
   recoverable. If it fails, the restore aborts. `--no-safety-backup` skips it.
7. **The host-directory gate.** Restoring `mibs` over a non-empty directory
   needs `--allow-tree-overwrite` on top of `--apply`, and the destination is
   checked against a deny list of paths a wholesale delete must never touch.
   See [The MIB source tree](#the-mib-source-tree).
8. **Replacement, not merge.** Each volume, and the MIB tree, is emptied before
   extraction, so files the backup does not contain do not survive. A
   half-matching restore is worse than one that refuses.

### Selective restore

`--only` and `--skip` take a comma-separated component list. An unknown name is
fatal rather than ignored — a typo in `--only` would otherwise silently restore
more, or less, than intended.

```bash
# roll back configuration, keep the metric history collected since
netmon-backup restore --from backups/netmon-... --skip metrics --apply

# recover only the TSDB, leave current device configuration alone
netmon-backup restore --from backups/netmon-... --only metrics --apply

# restore everything EXCEPT the MIB source tree (no gate needed)
netmon-backup restore --from backups/netmon-... --skip mibs --apply

# put the MIB tree back over the working copy (gate required)
netmon-backup restore --from backups/netmon-... --only mibs --apply --allow-tree-overwrite

# unpack the MIB tree somewhere harmless to look at it first
netmon-backup restore --from backups/netmon-... --only mibs --apply --mib-tree /tmp/mibs-check
```

Restoring `webui` without `poller` (or vice versa) warns: device MIB assignments
reference MIB files in the poller volume, so the two can end up describing
different sets. It warns rather than refuses, because it is a legitimate thing to
want.

## Safety model

This tooling stops containers and destroys volume contents, on a host that also
runs unrelated workloads. It follows the same discipline as
`test/integration/run-tests.sh`:

* The compose project is **pinned** (`NETMON_PROJECT`, default `go-netmon`),
  never inherited from the working directory.
* Every compose call goes through one wrapper that always passes `-p` and `-f`.
* Volume names are **derived** (`<project>_<compose volume>`), never searched or
  pattern-matched.
* Before any volume is read or written, its `com.docker.compose.project` label
  must equal the pinned project. A look-alike volume cannot be reached even by
  exact name.
* `--remove-orphans` is never used, and `down` is never used — services are
  `stop`ped and `start`ed, so containers and networks survive.
* Volumes resembling stack volumes but lacking the label are **reported** as
  ignored, so an operator knows they exist and that the tooling will not act on
  them.

## Procedure: full redeploy and restore

```bash
# 1. Record the current state, and back up.
scripts/verify-stack.sh capture /tmp/before.json
make backup                                      # -> backups/netmon-<timestamp>

# 2. Confirm the archive is sound BEFORE destroying anything.
make backup-verify FROM=backups/netmon-<timestamp>

# 3. Tear down. Scoped to the project; no --remove-orphans.
docker compose -p go-netmon -f docker-compose.yml down -v

# 4. Redeploy.
docker compose -p go-netmon -f docker-compose.yml up -d

# 5. Review the plan, then apply.
make restore FROM=backups/netmon-<timestamp>          # prints plan, exits 1

#    If ./mibs still holds files (a "down -v" does not remove it - it is a bind
#    mount, not a volume), the restore refuses until you say so explicitly:
make restore FROM=backups/netmon-<timestamp> YES=1 ALLOW_TREE_OVERWRITE=1

#    Or leave the working copy's MIBs exactly as they are:
make restore FROM=backups/netmon-<timestamp> YES=1 RESTORE_ARGS='--skip mibs'

# 6. Prove it.
scripts/verify-stack.sh capture /tmp/after.json
make stack-verify BEFORE=/tmp/before.json AFTER=/tmp/after.json
```

`verify-stack.sh compare` requires the configuration section to match **exactly**
and exits non-zero otherwise. Metric counts are reported with their deltas rather
than compared for equality, since a live stack keeps scraping; what it gates on
is that the **oldest retained sample did not move forward**. That is the signal
that matters, because a stack whose TSDB came back empty looks perfectly healthy
and starts scraping immediately.

### After the restore

Backup downtime and restore time scale with the size of the volumes. The web UI
is not ready until it has loaded its MIB catalog, which is slow on a cold
catalog (see [restart-and-startup.md](restart-and-startup.md)).

> The web UI image's healthcheck (`Dockerfile.webui`) uses `--start-period=5s`
> with a 30 s interval and 3 retries, so a cold start that takes longer than
> that to load a full MIB catalog will report `unhealthy` until it finishes
> and recovers on its own. This is a healthcheck tuning issue in the image, not
> a restore failure. Wait for `healthy` rather than acting on it.

## Restoring onto a fresh host

`netmon-backup restore` creates missing volumes through `docker compose up --no-start`, so
they are created with the labels the ownership checks depend on. Creating them
by hand produces unlabelled volumes the tooling will then refuse to touch.

```bash
git clone <repo> && cd go-netmon
cp -r /media/offsite/netmon-<timestamp> backups/
docker compose -p go-netmon -f docker-compose.yml up -d       # build/pull images
make restore FROM=backups/netmon-<timestamp> YES=1
```

On a genuinely fresh host the MIB tree needs no gate: the destination is empty
or absent, nothing is destroyed, and the tree is simply created. The gate only
appears once that directory holds files.

If the backup came from a differently named project, either set
`NETMON_PROJECT` to the original or pass `--force-project`.

## Testing

```bash
make test              # unit: the tool against a fake runtime, no daemon
make integration-test  # the same operations against a real Docker daemon
make test-scripts      # shell-script tests, no Docker daemon, no live stack
make lint-scripts      # shellcheck, including test/integration/run-tests.sh
```

### Against a real daemon, without handing anything the host's

Most of this tool's coverage runs against a fake runtime implementing the same
interface, which proves the right `docker` argument vectors are built. It cannot
prove the daemon does what those vectors say — that a tar written by one
container reads back byte for byte in another, that a volume is genuinely
emptied before it is extracted into, that numeric ownership survives. Those are
what a restore rests on and they only exist at the daemon.

Getting a daemon into the test container has two obvious answers and both are
bad. Mounting `/var/run/docker.sock` gives the test container effective root on
the host — exactly what this package's containment tests exist to prevent.
Running the suite on the host instead breaks the rule that integration tests run
in a container.

The third answer is a **sibling daemon**: `docker-compose.test.yml` runs a
`dind` service and points the test container at it with
`DOCKER_HOST=tcp://dind:2375`. The test container stays unprivileged and has no
host socket; only the throwaway daemon is privileged, and it starts empty.

That is a stronger guarantee than a convention. The inner daemon cannot see the
host's containers or volumes at all, so a backup test cannot reach the live
`go-netmon` deployment even if its project resolution were wrong — the isolation
does not depend on the code under test being correct. Everything it creates dies
with the stack; after a run, `docker volume ls` on the host shows none of it.

`test/integration/backup_test.go` covers the volume round trip, replace-rather-
than-merge, numeric ownership surviving, and volume ownership read from labels a
daemon actually applied.

The `dind` service sits behind a compose profile and `run-tests.sh` **tries** to
start it. A runner that is itself unprivileged or rootless cannot start a
privileged container, and that is a fact about where the suite is running rather
than a defect in the code — so it degrades instead of failing: the script warns,
`DOCKER_HOST` is not passed, and those four tests skip. The rest of the
integration suite has nothing to do with Docker and still runs.

That distinction is deliberate. A skipped test that announces itself is honest;
a suite that goes red because an optional capability is unavailable trains
people to ignore it.

`scripts/tests/run-tests.sh` (`make test-scripts`) tests the legacy shell scripts
`scripts/backup.sh` and `scripts/restore.sh`, and the checks below describe that
suite. It runs everything against a stub Docker CLI
(`scripts/tests/fake-docker`) and a throwaway repository tree under `$TMPDIR`.
Tooling that destroys data has to be testable without risking data, so the
tests never contact a Docker daemon or read the real repository. They cover the
round trip, selective restore, and every refusal path — truncated member,
corrupted member body, tampered tree, missing member, absent checksums, future
schema, project mismatch, foreign containers, unlabelled volumes, and missing
`--apply` — asserting in each case that volume contents are unchanged.

For the MIB source tree specifically they cover: the tree is carried and its
member reproduces it exactly, including nested directories and dotfiles; a
truncated tree member is caught before anything is written; the overwrite gate
refuses a populated destination and leaves the stack unstopped; the replacement
is wholesale rather than a merge; `--skip mibs` really leaves it alone; the
safety backup captures it; the path is derived from the compose file rather than
assumed, with conflicting or absent mounts refused; unsafe destinations are
refused; and schema-1 archives with no `trees[]` still restore.

The two tests covering unsafe destinations deliberately never reach a stage that
could delete anything — one names the throwaway fixture's repository root, the
other runs without `--apply`. A test that only fails safely because of filesystem
permissions is a test of who you are running as, not of the guard.

## Environment variables

| Variable | Default | Purpose |
|---|---|---|
| `NETMON_PROJECT` | `go-netmon` | Compose project to operate on |
| `NETMON_COMPOSE_FILE` | `<repo>/docker-compose.yml` | Compose file defining the stack |
| `NETMON_HELPER_IMAGE` | `alpine:3.19` | Image used to stream volume contents |
| `NETMON_REPO_ROOT` | current directory | Repository root holding `config.yaml` and `docker-compose.yml` |
| `NETMON_WEBUI_URL` | `http://localhost:8082` | Web UI, for the logical export (and for `scripts/verify-stack.sh`) |
| `NETMON_PROM_URL` | `http://localhost:9091` | Prometheus; read by `scripts/verify-stack.sh` only, not by `netmon-backup` |
| `NETMON_HISTORY_LOOKBACK_DAYS` | `30` | `scripts/verify-stack.sh`: window used to find the oldest retained sample |
| `NETMON_MIB_TREE` | *(read from the compose bind mount)* | Host directory holding the MIB source tree |
| `NETMON_MIB_MOUNT_TARGET` | `/app/mibs` | Container path the MIB tree is mounted at, used to find its host path |
| `DOCKER` | `docker` | Docker CLI (the test suite substitutes a stub) |
