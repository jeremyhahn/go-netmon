# Alarms

> **Status: REFERENCE** · Scope: go-netmon · Last verified: 2026-10-09

The bell in the top-right of the web UI shows what is wrong right now.

Before it existed, everything the UI said about a fault was a toast: shown once,
for a few seconds, and then gone. A reader who was not watching the screen at
that moment had no way to find out, and there was nowhere on the page that
answered the question at all.

## Where the rules live

In the `alarms:` section of the configuration file the web UI reads —
`/etc/go-netmon/config.yaml` on an appliance, named by `CONFIG_PATH`. It is the
same file the poller reads, which ignores this section.

```yaml
alarms:
  - name: DeviceUnreachable
    expr: gonetmon_device_unreachable == 1
    severity: critical
    for: 2m
    summary: A device has stopped answering SNMP
    description: >-
      The poller has not had an answer from this device. Everything on its
      dashboard is the last thing it said, however recent the timestamps look.
```

| Field | |
|---|---|
| `name` | Identifies the rule. Must be unique. |
| `expr` | PromQL. The alarm fires for **every series the expression returns**. |
| `severity` | `critical`, `warning` or `info`. Orders the list; the bell takes the colour of the worst thing in it. |
| `summary` | One line saying what is wrong. This is what the reader sees first. |
| `description` | The longer text, shown under the summary. |
| `for` | How long the expression must keep returning a series before the alarm is raised. Omit it to raise on the first evaluation. |

The rules are evaluated every 15 seconds, and the browser re-reads the bell on
the same cadence.

## A rule fires once per series

This is the thing to understand before writing one. `snmp_ifOperStatus == 2`
does not produce one alarm called "an interface is down" — it produces one per
interface, because "Gi0/0/2 is down" and "Gi0/0/5 is down" are two things to
fix. Each firing instance carries the series' labels, so the bell can say which
one it is, and each keeps its own `since`.

That is also the trap. An expression that matches broadly fills the bell:
`snmp_ifOperStatus == 2 and on(device,ifindex) snmp_ifAdminStatus == 1` matches
every unused port on an access switch. Narrow the expression to the devices or
the interfaces you care about rather than raising a threshold. The rule that
ships adds `and on(device,ifindex) snmp_ifalias_info{value!=""}` — an interface
somebody bothered to label is an interface somebody cares about. Even so, the
comment on that rule in `deploy/config.yaml` records 36 matches on the fleet it
was written against.

Because a rule is ordinary PromQL, it can be tried in Prometheus's own console
before it is written down.

## Reloading without a restart

```
systemctl reload go-netmon-webui      # or: kill -HUP <pid>
```

Only the alarm rules are re-applied. Everything else in the file is bound at
startup, and pretending otherwise would be worse than saying so — but a
threshold is what an operator actually edits between restarts, and restarting
the web UI costs minutes of MIB parsing.

**A file that will not parse or will not validate leaves the running rules
exactly as they were**, and says so in the journal:

```
Reload refused, keeping the running rules: config validation error:
alarms[4].severity: must be one of: critical, warning, info
```

A reload that emptied the bell because of a typo would be a monitoring outage
caused by the act of configuring monitoring.

A reload does not restart the `for` window of an alarm that is still being
sighted: the evaluator remembers when each alarm (rule name plus series labels)
was first seen, and a reload keeps that. A renamed or newly added rule starts
its `for` window from the reload, so an alarm with `for: 10m` is not shown for
ten minutes after that rule first appears.

## What the bell says when it does not know

Three states look identical if you only count alarms, so the bell distinguishes
them:

| The panel says | What it means |
|---|---|
| `6 rules watching`, nothing firing | Nothing is wrong. |
| `No alarm rules are configured` | Nothing is being **watched**. Not the same thing. |
| `2 of 6 rules could not be evaluated` | Prometheus did not answer. **This is not an all-clear.** |

A rule that cannot be evaluated keeps whatever it had already raised rather than
having it cleared — otherwise a Prometheus outage would empty the board and
manufacture good news at the one moment an operator most needs otherwise. The
bell's border goes dashed while any rule is in that state.

## Relationship to Prometheus alerting rules

`deploy/rules/` holds Prometheus's own alerting rules, which are a different
mechanism: Prometheus evaluates them and an Alertmanager would page on them.
None is configured (`deploy/prometheus.yml` ships with an empty
`alertmanagers` target list), so those rules evaluate and appear as `ALERTS`
series without reaching anyone.

The bell is not a replacement for that and does not page. It answers "is
anything wrong right now" for a person who is looking at the page.

## API

```
GET /api/alarms
```

```json
{
  "alarms": [
    {
      "id": "...",
      "rule": "InterfaceDown",
      "severity": "warning",
      "summary": "A labeled interface is administratively up and operationally down",
      "description": "...",
      "device": "core-01",
      "labels": {"ifdescr": "GigabitEthernet0/0/5", "ifindex": "8", "...": "..."},
      "value": 2,
      "since": "2026-09-01T22:02:31.400406374-04:00"
    }
  ],
  "count": 1,
  "configured": 6
}
```

`unevaluated` is the number of rules that could not be asked on the last round;
it is omitted from the response when it is zero.

`since` is when the alarm was first raised, not when it was last seen: an alarm
that stays up keeps it, and one that cleared and recurred gets a new one, so a
flapping link does not read as a dead one.
