# Sample Labels

How the metrics exporter decides which labels a sample carries, and what
happens when a device's configured tags collide with the labels that identify
the series.

## The two kinds of label

**Structural labels** come from the poll itself and establish series identity:

| Label | Source |
| --- | --- |
| `device` | the polled device's name |
| `oid` | the instance OID that was read (ordinary readings only) |
| index labels (`ifindex`, `cbgppeer2remoteaddr`, …) | the row index, decoded against the table's MIB `INDEX` clause |
| `value` | the status value an `snmp_*_info` metric describes |
| `sys_name`, `sys_descr`, `sys_location`, … | the system group values `snmp_system_info` reports |

**Extra labels** are the device's configured tags (`devices[].tags` in
`config.yaml`, or the tags the Web UI attaches to a device). They are attached
to every sample of that device so it can be filtered, federated and joined on.

## Every sample of a poll gets the same tags

Three converters in `internal/metrics` build samples — the MIB-aware
`ConvertSNMPResultsWithTableIndexes`, and the MIB-less
`ConvertSNMPResultsToSamples` and `ConvertSNMPResultsWithInterfaceNames` — and
each of them produces three kinds of sample: ordinary readings, status info
metrics (`snmp_*_info`), and `snmp_system_info`. All of them go through the
same label construction in `sample_labels.go`, so a device's tags appear on all
of its series or none of them.

This matters most for info metrics. An info metric is what a dashboard or
recording rule joins *against*:

    snmp_ifInOctets{environment="prod"}
      * on (device, ifindex) group_left(value) snmp_ifdescr_info

If the info metric does not carry `environment`, that join cannot be filtered
on the same label as the readings, and a federation or Prometheus-level filter
that selects `{environment="prod"}` silently returns the readings without the
names that describe them.

## Precedence: structural labels win

A tag whose name collides with a structural label is **dropped**, not merged.

Letting configuration through would let it rewrite series identity: a tag named
`device` would file one router's readings under another router's name, a tag
named `ifindex` would move a sample onto a different interface's series, and a
tag named `value` would make an info metric describe something it never
measured. Dropping is also the only outcome independent of Go's randomized map
iteration order, so the same configuration produces the same series on every
poll and in every process.

The drop is deliberate but not silent. Each one increments:

    gonetmon_sample_label_conflicts_total{device="...", label="..."}

A non-zero value means a configured tag is not reaching Prometheus, and the
`label` dimension names it. Rename the tag to clear it.

## Series identity

Adding a tag to a device changes the identity of every series that device
produces, info metrics included. The series carrying the old label set stops
being written and goes stale; a new one starts. This is normal Prometheus
behaviour for any label change, but it applies to `snmp_*_info` series from the
first poll after tags are configured — dashboards pinned to the old label set
need updating at the same time.
