# Security Posture

What this deployment does and does not defend against, so neither has to be
rediscovered by reading the code.

## The API is unauthenticated, deliberately

**Every endpoint the web UI serves on port 8082 is open to anyone who can reach
the port, and `GET /api/devices` returns each device's SNMP community string in
clear text.**

```
$ curl -s http://<host>:8082/api/devices | head -c 120
{"devices":[{"id":4,"name":"bastion","address":"198.51.100.14","snmp_version":"2c","community":"public",...
```

The same applies to the poller's control API on 9080 and the metrics endpoint on
9100. There is no authentication, no authorization, and no rate limiting on any
of them.

This is a decision, not an oversight. The stack runs on an internal network
inside the security boundary, and the access control is the network: whoever can
reach the port is already inside. Reviewed and accepted on 2026-08-11.

### What that means in practice

* **Do not publish these ports.** No port forward, no reverse proxy exposing
  them to the internet, no binding to a public interface. Everything the API
  offers — reading credentials, adding and deleting devices, importing MIBs,
  changing poll intervals — is available to any client that reaches it.
* **Reaching the host is enough.** Anything running on the host, in another
  container on a shared network, or on the same LAN segment can call these
  endpoints. There is no second factor behind the network.
* **The community strings are the real prize.** They are read-only SNMP
  credentials for core routers and PDUs. Anyone who can read them can poll those
  devices directly.

### If the boundary changes

The moment this stack is reachable from anywhere less trusted than the internal
network, it needs authentication first. `internal/webui/api/server.go` already
routes through a middleware chain (`noCacheMiddleware`), so a token check has a
natural place to go. Options considered, for whoever picks this up:

| Approach | Cost | Notes |
|---|---|---|
| Bearer token from config, plus a login page for the browser | Moderate | Self-contained; no dependency on other services. The CLI is already an HTTP client and would carry the token as a flag. |
| Redact credentials from the API and bind to `127.0.0.1` | Small | Removes the credential leak and the remote surface, but anything on the host still has full control. |
| JWT via go-quicbaas IAM, per `conventions/api-conventions.md` | Large | Matches the platform standard. Couples go-netmon to the quicbaas / qrdb / xkms stack, which it does not otherwise depend on. |

## What is defended

These are properties the code holds regardless of the network, and they have
tests that fail if they stop holding.

* **Filesystem containment.** `netmon-backup` resolves every path it creates,
  empties or overwrites against an open directory handle (`os.Root`), so a path
  that would leave the chosen directory is refused by the kernel rather than
  sanitized. `TestNothingBypassesTheContainment` fails the build if any code in
  that tool reaches the filesystem another way or hands a command line to a
  shell. See [backup-restore.md](backup-restore.md).
* **MIB uploads.** The upload directory is held open as an `os.Root` and files
  are addressed by name within it, so an uploaded name cannot traverse out and
  cannot be followed through a symbolic link planted in that directory.
  `TestUploadCannotWriteThroughASymlink` plants one and asserts the target is
  untouched.
* **Compose project ownership.** The backup tool refuses to stop, copy or
  restore anything in a project holding a container its own compose file did not
  create, and refuses volumes not labelled for the project.
* **Dependency vulnerabilities.** `make security` runs `govulncheck` over the
  dependencies and the linked standard library, and it is a required CI job.

## Reporting

This is an internal tool on an internal network; there is no external disclosure
process. Findings go in the project's task list.
