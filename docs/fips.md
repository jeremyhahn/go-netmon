# FIPS 140-3

go-netmon builds in two configurations. Both are supported; they differ only in
which cryptographic algorithms exist in the binary.

| | Build | Run |
|---|---|---|
| **Ordinary** | `go build ./...` | every feature, every protocol |
| **FIPS** | `go build -tags fips ./...` | `GOFIPS140=latest`, `GODEBUG=fips140=only` |

CI runs both. Every job except `FIPS` builds untagged, so nothing the tag
removes stops being compiled, vetted, linted, scanned and unit-tested.

## Why `fips140=only`, not `fips140=on`

`fips140=on` loads the Go Cryptographic Module but still **permits**
non-approved algorithms. `fips140=only` makes them error or panic.

That is not a preference, and it is not theoretical. Measured on this
repository at go1.26.7, with one line added to `authProtocol` that hashes its
argument with MD5 — a regression of exactly the shape this gate exists to
catch, on a path the suite already covers:

```
GOFIPS140=latest GODEBUG=fips140=only go test -tags fips ./internal/snmp/
  rc=1   panic: crypto/md5: use of MD5 is not allowed in FIPS 140-only mode

GOFIPS140=latest GODEBUG=fips140=on   go test -tags fips ./internal/snmp/
  rc=0   ok      github.com/jeremyhahn/go-netmon/internal/snmp
```

The same defect, in the same binary, on the same test. `on` reports success.
A gate that cannot fail on the thing it exists to detect is decoration, which
is why `only` is mandatory (company convention `configuration.md`, owner ruling
2026-08-17).

**If the FIPS job fails, that failure is the finding.** Do not weaken it back
to `on`. Fix the call site, or put the feature behind the tag.

## What is absent from a FIPS build

Two things, and both are absent because an external protocol *names* a
non-approved algorithm — not because this repository chose one. Where an
approved primitive would do, the code uses it and the feature exists in both
builds.

### SNMPv3: MD5, SHA-1 and DES

**SNMPv3 itself is not affected.** A FIPS build authenticates with SHA224,
SHA256, SHA384 and SHA512 and encrypts with AES, AES192 and AES256, exactly as
an ordinary build does. Three protocol names are missing:

| Setting | Value | Why |
|---|---|---|
| `auth_protocol` | `MD5` | HMAC-MD5-96, RFC 3414 §6 |
| `auth_protocol` | `SHA` | HMAC-SHA-96 — SNMPv3's "SHA" **is SHA-1**, RFC 3414 §6 |
| `priv_protocol` | `DES` | CBC-DES, RFC 3414 §8 |

A deployment subject to FedRAMP, PCI DSS or HIPAA could not lawfully use any of
the three for protecting regulated data, so their absence from the FIPS build
costs such a deployment nothing.

They are registered by `internal/snmp/v3_legacy.go`, which carries
`//go:build !fips`. A FIPS build does not compile that file and cannot select
them. Configuring one produces a `*V3ConfigError` that says so (when the
runtime profile below is also active, the `*fipsprofile.RefusedError` answers
first):

```
snmp: auth_protocol "SHA": SNMPv3 "SHA" is HMAC-SHA-96, that is SHA-1
(RFC 3414 section 6), which is not a FIPS 140-3 approved algorithm and is
absent from FIPS builds of go-netmon; configure the device for SHA224,
SHA256, SHA384 or SHA512
```

rather than the generic "unknown protocol" answer. The difference matters at
three in the morning: "want SHA224, SHA256, …" reads as a typo in the config
file and sends an operator looking for a mistake that is not there. The real
problem is that this binary was built with `-tags fips`, and the fix is to
deploy the ordinary build or reconfigure the device.

**The ordinary build is unchanged and keeps all three.** The product exists to
monitor legacy gear, a great deal of which offers nothing else, and a
deployment not subject to FIPS is entitled to them. `internal/snmp/v3_legacy_test.go`
covers them so that "compiled out" does not drift into "deleted".

### The web UI's live metric stream

`GET /ws` is **not available in a FIPS build.**

RFC 6455 §4.2.2 defines `Sec-WebSocket-Accept` as a SHA-1 digest of the
client's key and a fixed GUID. `gorilla/websocket` therefore hashes with
`crypto/sha1` on both sides of the handshake (`util.go`, `computeAcceptKey`,
called from `Upgrader.Upgrade` and `Dialer.Dial`). Under `fips140=only` the
runtime refuses the operation and the handshake fails. There is no approved
substitute, because the protocol names the algorithm.

The route stays registered and answers **501 Not Implemented** with the reason
and an alternative. An unregistered route would return 404, which reads as a
wrong URL or a routing bug and sends whoever is debugging it looking for a
mistake that is not there.

Everything else in the web UI is unaffected. The REST API is complete, and the
same metrics are available by polling it. Only the push transport is missing —
the `Hub`, its clients, the delta encoding and the query path touch no
non-approved primitive and are compiled into both builds.

## The runtime FIPS profile: SNMP policy

The `fips` tag removes code from the binary. The company enforcement model
(boundary ruling 1, product ruling 7 in the handbook's `04-security-model.md`)
is also **FIPS-capable build plus runtime activation**, so the same policy is
enforced at runtime by `internal/fipsprofile`. The profile is active exactly
when the Go runtime is in FIPS 140-3 mode (`GODEBUG=fips140=only`, set by
configuration or because the host kernel is in FIPS mode). An ordinary build
run under `fips140=only` still contains MD5, SHA-1 and DES, so it needs this
check; a `-tags fips` build gets it as well.

Under the profile, only SNMPv3 with SHA-2 authentication and AES privacy is
permitted. Everything else is refused with a typed `*fipsprofile.RefusedError`
(`Field`, `Value`, `Reason`; reach it with `errors.As`):

| Setting | Refused value | Field |
|---|---|---|
| `snmp_version` | `1`, `2c` (also `v1`, `v2c`) | `snmp_version` |
| `auth_protocol` | `MD5`, `SHA` (SHA-1) | `auth_protocol` |
| `priv_protocol` | `DES` | `priv_protocol` |

Accepted: SNMPv3 with `SHA224`, `SHA256`, `SHA384` or `SHA512` authentication
and `AES`, `AES192` or `AES256` privacy.

The refusal fails closed at the earliest point that sees the setting:
`config.Device.Validate` (so a configuration file with a v2c target does not
load, and `snmp.NewClient` and the poller's device paths refuse it),
`snmp.ConfigureV3` (so the web UI connectivity tester refuses the same
protocols) and the tester's version switch.

**Normal mode is unchanged.** With the profile inactive, every version and
protocol is accepted as before: the product exists to monitor legacy gear.

Tests that exercise v1/v2c and the legacy protocols run with the profile pinned
off (`fipsprofile.Force(false)`), because CI runs the whole suite under
`fips140=only`; tests of the profile itself pin it on.

## What this build does *not* claim

A FIPS build does not contain zero non-approved primitives, and no networked Go
program can. Measured with `go list -deps`, `crypto/md5`, `crypto/sha1`,
`crypto/des` and `crypto/rc4` are in the dependency closure of every binary
here by three independent routes: `crypto/tls`, `crypto/x509`, and `gosnmp`
(which imports them unconditionally, whatever protocol is selected).

So the guarantee is stated precisely:

- **The product cannot select a non-approved algorithm.** That is what
  `-tags fips` delivers — the code that would select one is not compiled.
- **The runtime refuses a non-approved operation.** That is what
  `GODEBUG=fips140=only` delivers, and it applies to the linked standard
  library too.

The two together are what makes the configuration defensible. Neither alone is.

## Adding a feature

If a new feature needs a non-approved primitive, in order of preference:

1. **Use an approved one.** Most features survive intact.
2. **Put it behind `//go:build !fips`**, tests included, and add a row to this
   document. A capability that silently disappears is worse than one declared.
3. **Delete nothing.** The FIPS build is a constrained variant of the product,
   not a replacement for it.

Then check the whole matrix, because a tag added to make one configuration pass
routinely breaks the other and `go test ./...` will not tell you:

```sh
go build ./...                                                    # ordinary
go build -tags fips ./...                                         # FIPS
go test -count=1 ./...
GOFIPS140=latest GODEBUG=fips140=only go test -count=1 -tags fips ./...
```
