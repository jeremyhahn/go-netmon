# Walking a device

> **Status: REFERENCE** · Scope: go-netmon · Last verified: 2026-10-09

`netmon device walk` asks a device what it actually exposes. It is the answer to
two questions that come up constantly and that nothing else answers:

* **Is this MIB worth importing?** A module the device does not implement
  produces nothing, however correct the file is.
* **Why is this object empty?** A device answering `NoSuchObject` looks exactly
  like the poller never asking for it.

The walk runs on the server using the credentials already stored for the device.
That matters most for SNMPv3, where a user, two protocols and two passphrases
have to agree; re-entering them to troubleshoot is how they drift apart.

## Usage

```
netmon device walk <device> [oid]
```

`snmpwalk` is an alias for `walk`. Giving the OID both ways with different values
is an error.

```
$ netmon device walk bastion 1.3.6.1.4.1.21317.1.3.1.13.1.9
OID                                    NAME         MODULE         TYPE         VALUE
1.3.6.1.4.1.21317.1.3.1.13.1.9.1.0     PSU1Status   KNGeneric-MIB  OctetString  PSU1 is on
1.3.6.1.4.1.21317.1.3.1.13.1.9.2.0     PSU2Status   KNGeneric-MIB  OctetString  PSU2 is abnormal, please check the device
1.3.6.1.4.1.21317.1.3.1.13.1.9.3.0     fanSpeed     KNGeneric-MIB  OctetString  Fan speed detection is disabled
1.3.6.1.4.1.21317.1.3.1.13.1.9.4.0     temperature  KNGeneric-MIB  OctetString  Temperature detection is disabled

4 objects under 1.3.6.1.4.1.21317.1.3.1.13.1.9 in 12ms
```

| Flag | Meaning |
|---|---|
| `--oid` | the subtree, as an alternative to the positional argument |
| `--limit` | stop after this many objects (default 500, maximum 20000) |
| `--named` | show only objects an imported MIB describes (the object count still reports everything the device answered, with `(N shown)` beside it) |
| `--json` | the whole result as JSON |

The OID defaults to `1.3.6.1` — everything the device exposes, standard and
vendor alike. A leading dot is accepted, so an OID pasted from any other tool
works.

`NAME` and `MODULE` are filled in from the imported MIBs where they are known and
left as a dash where they are not, so **a walk is useful before any MIB is
imported**. That is the normal order: walk first, then find the MIB for what you
saw.

## Reading the result

**Objects listed** — the device implements them. Anything with a name is already
understood by an imported MIB and can be polled today.

**Nothing listed** — a successful walk with no objects. The device does not
implement that subtree, and a MIB rooted there will produce no metrics:

```
$ netmon device walk bastion 1.3.6.1.2.1.2.2
bastion (198.51.100.14) answered nothing under 1.3.6.1.2.1.2.2.

The device does not implement anything in that subtree, so a MIB
rooted there would produce no metrics.
```

This is a result, not an error, and the command exits zero.

**A device that did not answer at all** is a failure and exits non-zero, naming
the device and the reason. That is the case worth alerting on; an empty subtree
is not.

**Stopped at the limit** — the subtree continues past the cap. Raise `--limit`.
The objects already read are good.

## Sizing a walk

The default limit of 500 suits a survey. A core router holds tens of thousands of
objects, so walking one whole takes a larger limit and longer:

```
netmon device walk core-01 --limit 20000        # the whole device
netmon device walk core-01 1.3.6.1.2.1.2.2      # just the interface table
```

The server bounds a walk at 60 seconds and 20000 objects whatever is asked for,
so a walk cannot hold an HTTP connection open indefinitely or read a whole
routing table into memory.

## The HTTP API

```
GET  /api/devices/{id}/walk?oid=<root>&limit=<n>
POST /api/snmp/walk          {"address": "...", "snmp_version": "...", "oid": "..."}
```

The `POST` form walks an address that is not a registered device, for the case
where a box is walked before it is onboarded. Both answer the same body.

A silent device answers `504`, a refused request `400`, and any other failure
`502` — the same classification `/api/snmp/test` uses, so a client tells "fix
your request" from "that box is down" without reading prose. The `GET` form
answers `404` for an unregistered device id. `limit` above 20000 is clamped to
20000 rather than refused; the `POST` form also accepts `limit`, plus the
credential fields `/api/snmp/test` takes.

## Related

* [security-posture.md](security-posture.md) — the API is unauthenticated, and
  this endpoint makes the server emit SNMP traffic on request.
