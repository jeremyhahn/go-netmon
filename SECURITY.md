# Security policy

## Reporting a vulnerability

Report vulnerabilities privately through GitHub:
[Report a vulnerability](https://github.com/jeremyhahn/go-netmon/security/advisories/new)
(repository **Security** tab, **Report a vulnerability**). Do not open a public
issue, pull request or discussion for a suspected vulnerability.

Include the affected version (`netmon version`, or the release archive or image
tag), the build (ordinary or FIPS), how to reproduce the issue, and its impact.
The advisory stays private while a fix is prepared, and is published with the
release that fixes it.

## Supported versions

go-netmon is pre-1.0. Security fixes go into the latest minor release only.

| Version | Supported |
|---|---|
| latest `0.y.z` minor | yes |
| older minors | no — upgrade to the latest release |

## Security posture

Read this before you expose go-netmon to any network.

- **The HTTP API and web UI are unauthenticated by design.** There is no
  login, no token and no authorization on any endpoint.
- **They listen on every interface by default.** The web UI and the poller
  API default to `0.0.0.0:8080` and the poller's metrics endpoint to
  `0.0.0.0:9100`.
- **`GET /api/devices` returns SNMP credentials in clear text.** Anyone who
  can reach the web UI can read the communities and SNMPv3 passphrases of every
  monitored device, and can add, change or delete devices. The fix is tracked
  in [#1](https://github.com/jeremyhahn/go-netmon/issues/1).

Run go-netmon on a trusted management network, or bind it to `127.0.0.1`
(`listen_address` in the config, or the `-listen`, `-api-listen` and
`-metrics-listen` flags) and put a reverse proxy that terminates TLS and
authenticates users in front of it. Never publish these ports to the internet.

[docs/operations/security-posture.md](docs/operations/security-posture.md)
describes what is and is not defended, including how SNMP credentials are held
in memory.

Release archives come with a `SHA256SUMS` file and build provenance
attestations; see [Install](README.md#install) for how to verify them.
