# Metric naming

Every SNMP sample go-netmon writes to Prometheus is named after **the MIB object
it instantiates**, and the row it came from is carried in **labels**.

```
snmp_ifNumber{device="core-01"}
snmp_ifInOctets{device="core-01",ifindex="10",ifname="GigabitEthernet0/0"}
snmp_cpmProcessMemoryCore{device="core-01",cpmcputotalindex="7",cpmprocesspid="5420"}
```

The name never contains the row index. Two routers produce roughly 155 distinct
metric names, not one per table row.

## The rule

| Thing | Rule | Example |
| --- | --- | --- |
| Metric name | `snmp_` + the object's name, sanitized | `ifInOctets` → `snmp_ifInOctets` |
| Index label | the index column's name, lowercased | `cbgpPeer2RemoteAddr` → `cbgppeer2remoteaddr` |
| Info metric | `snmp_` + the column's name lowercased + `_info` | `ifDescr` → `snmp_ifdescr_info` |
| Unresolved OID | `snmp_` + the instance OID, sanitized | `.1.3.6.1.4.1.9.9.999.1.1` → `snmp__1_3_6_1_4_1_9_9_999_1_1` |

Case is preserved in metric names and dropped in label names, because that is
what the exporter does. A consumer that lowercases a metric name queries a
series that does not exist.

**Sanitizing** replaces every character outside `[a-zA-Z0-9_:]` with `_`, per
Prometheus' metric name grammar. MIB object names rarely need it.

## The single derivation: `internal/promname`

Nothing may rebuild these strings by hand. `internal/promname` is the one place
they are derived, and every consumer goes through it:

```go
promname.MetricName("ifInOctets")     // "snmp_ifInOctets"
promname.IndexLabel("ifIndex")        // "ifindex"
promname.InfoMetricName("ifDescr")    // "snmp_ifdescr_info"
promname.RawOIDMetricName(oid)        // the unresolved-OID fallback
```

`promname` composes `metrics.SanitizeMetricName`, so the character rules cannot
drift from the exporter's.

### Consumers

| Package | What it names |
| --- | --- |
| `internal/webui/api` (dashboard panels) | `metric` and `query` on every panel, `series_labels` for the label that names each line, and `index_labels` for the row behind it |
| `internal/webui/api` | which objects have data in Prometheus; the charts it generates |
| `internal/grafana/builder` | every PromQL expression in a generated Grafana dashboard |
| `internal/cli` | resolving a series back to `ifInOctets[10]` |

## Selecting one row

A table's rows share one metric name per column, so a row is selected with
label matchers:

```promql
snmp_cbgpPeer2FsmEstablishedTransitions{device="core-01",cbgppeer2remoteaddr="203.0.113.218"}
```

Not with a `__name__` regex. A `__name__=~"snmp__1_3_6_..._12(_.*)?"` selector
cannot use Prometheus' name index and has to scan every series in the label
set; it is also impossible to write by hand.

The index labels of a table come from its `INDEX` clause, so they differ
between tables that look alike. In CISCO-BGP4-MIB, `cbgpPeerAddrFamilyPrefixTable`
is indexed by `bgpPeerRemoteAddr` (from BGP4-MIB) and carries
`bgppeerremoteaddr`, while `cbgpPeer2Table` is indexed by `cbgpPeer2RemoteAddr`
and carries `cbgppeer2remoteaddr`. Take the label from the MIB, never from
another table that happens to describe the same peer.

## Identifying a series exactly

Every SNMP sample carries an `oid` label holding the full instance OID:

```
snmp_ifInOctets{device="core-01",oid=".1.3.6.1.2.1.2.2.1.10.10",ifindex="10",ifname="Gi0/0"}
```

That label is the only part of a series that identifies its instance exactly.
The metric name does not, and the other labels cannot be told apart by
inspection: `ifindex` is a table index, `ifname` and `ifoperstatus` are
enrichment the exporter adds, and nothing in the label set says which is which.
Resolve a series to a MIB object by longest-prefix matching the `oid` label —
that is what `netmon monitor` does to print `ifInOctets[10]`.

## Objects with no MIB

An instance OID that no loaded MIB describes keeps a raw-OID name, so no data
is lost. `gonetmon_metric_name_unresolved_samples_total{device}` counts them.
This is the only case in which a metric name contains an OID, and the only case
in which one metric name means one table row.

## The drift gate

`internal/promname/contract_test.go` runs the real exporter over synthetic SNMP
results and asserts that what `promname` — and each consumer built on it —
produces equals what the exporter emitted. It fails if either side changes
alone.

This test exists because the previous arrangement could not catch this class of
bug: each consumer asserted against its own local `snmp__` helper, so all of
them stayed green while the exporter stopped emitting those names and every
dashboard panel silently matched nothing.

Add a case to it whenever a new consumer starts naming metrics.
