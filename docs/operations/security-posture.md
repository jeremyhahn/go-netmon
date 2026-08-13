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

### The recommended deployment

**Keep the ports private, or put an authenticating reverse proxy in front of
them.** This is the same posture as go-sysmon, and for the same reason: the
service is a component, not a product with its own identity system, and building
a second-rate one into it would be worse than deferring to something that does
authentication properly.

Either of these is a supported way to run it:

* **Private ports.** Bind 8082, 9080 and 9100 to an interface only trusted
  clients can reach — a management VLAN, a WireGuard interface, or `127.0.0.1`
  with an SSH tunnel for occasional access. This is what the deployment here
  does.
* **An authenticating reverse proxy.** Terminate TLS and authenticate at nginx,
  Caddy, Authelia, oauth2-proxy or equivalent, and let only that proxy reach the
  service ports. The API is stateless and has no session of its own, so a proxy
  that injects an identity header or simply gates access works without any
  change here.

What is **not** supported is publishing these ports directly. Everything the API
offers — reading credentials, adding and deleting devices, importing MIBs — is
available to whatever reaches it.

### Why authentication is not built in

go-netmon is intended to become a plugin for **go-trusted-platform**, which owns
identity, authorization and attestation for the components that run under it.
Authentication belongs at that layer. Implementing a private scheme here would
mean either throwing it away at integration, or carrying two identity systems
that disagree — and a monitoring tool is a poor place to invent credential
handling.

Until then the network is the boundary, as above.

If that changes before the platform integration lands,
`internal/webui/api/server.go` already routes through a middleware chain
(`noCacheMiddleware`), so a check has a natural place to go. Options considered,
for whoever picks this up:

| Approach | Cost | Notes |
|---|---|---|
| Authenticating reverse proxy | None in this codebase | The recommendation above. No code change, and the proxy is replaceable. |
| Bearer token from config, plus a login page for the browser | Moderate | Self-contained; no dependency on other services. The CLI is already an HTTP client and would carry the token as a flag. Likely thrown away at platform integration. |
| Redact credentials from the API and bind to `127.0.0.1` | Small | Removes the credential leak and the remote surface, but anything on the host still has full control. Worth doing independently of authentication. |
| Identity from go-trusted-platform | Large, but the intended end state | Matches the platform standard and is where this is going. Not worth building against before the plugin interface exists. |

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
