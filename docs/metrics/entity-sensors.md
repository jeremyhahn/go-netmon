# Entity Sensors and Optical DOM

> **Status: REFERENCE** · Scope: go-netmon · Last verified: 2026-10-09

How go-netmon collects physical sensor readings — optical transmit and receive
power, temperature, voltage, current, fan speed — and why the number it exports
is not the number the device put on the wire.

## The problem

An entity sensor reports its reading as three separate SNMP objects:

| Object | What it is |
| --- | --- |
| `entPhySensorValue` / `entSensorValue` | an integer |
| `entPhySensorScale` / `entSensorScale` | an SI prefix, as an enum (`yocto(1)` … `units(9)` … `yotta(17)`) |
| `entPhySensorPrecision` / `entSensorPrecision` | how many of the integer's low digits are fractional (`-8..9`) |

None of them means anything alone, and two switches can encode the same
quantity completely differently. Both of these are real, walked from the fleet
on 2026-08-31:

- A Nexus 9000 at **-7.328 dBm** puts **-7328** on the wire, with scale
  `milli(8)` and precision `0`.
- A Catalyst C9300 at **-7.5 dBm** puts **-75** on the wire, with scale
  `units(9)` and precision `1`.

Exported as read, those links would be published as **-7328 dBm** and
**-75 dBm** in a metric whose unit is dBm; read with each other's encoding they
would be out by a factor of a thousand and of ten. Every one of those is a
number that looks like a reading, graphs like a reading, and is wrong. That is
worse than collecting nothing, so a reading this stack cannot interpret is
dropped and counted rather than published.

## The arithmetic

`internal/metrics/entity_sensor.go`:

```
quantity = value × 10^(scale exponent for the source MIB) ÷ 10^(fractional digits)
```

**Scale** is the arithmetic ladder `3 × (scale − 9)` — a thousand per step,
`units(9)` at `10^0` — for fifteen of the seventeen values in both MIBs. Values
**14 and 15 depend on which module declared the column**, and reading one
module's value with the other's semantics is a thousand-fold error:

| | scale 14 | scale 15 |
| --- | --- | --- |
| `ENTITY-SENSOR-MIB` (as corrected) | `10^15` | `10^18` |
| `CISCO-ENTITY-SENSOR-MIB` | `10^18` | `10^15` |

RFC 3433 as published reads `exa(14), -- 10^15` and `peta(15), -- 10^18`, which
is backwards from SI — peta is `10^15` and exa is `10^18`. **RFC 3433 erratum
2008 (status: Verified, editorial, verified by Dan Romascanu, one of the RFC's
own authors) corrects the labels, not the annotations**: it replaces them with
`peta(14), -- 10^15` and `exa(15), -- 10^18`. Each enum value keeps the
exponent already printed beside it, and the misordered SI names are what gets
fixed — so the standard table is the plain arithmetic ladder throughout.

Cisco did the opposite when it wrote its own copy: it kept the original label
order and moved the exponents to match the names, so its value 14 is `10^18`.
The two modules therefore disagree on what the integer 14 means, and since the
integer is what crosses the wire, the reading's source table has to travel with
its scale. It does: `entitySensorOIDPart` reports which table matched,
`sensorMetadata` stores it paired with the scale, and the two are only ever set
together.

> **A warning for whoever revisits this.** There is a seductive argument that
> both modules mean the same thing: both TEXTUAL-CONVENTIONs describe
> themselves as SI prefix tables, RFC 3433's carries a `REFERENCE` to SI
> Spec. Publ. 330, an SMI `--` is a non-normative comment, and therefore the
> labels are authoritative and the annotations are the typo. It has been
> believed here twice and it is wrong — the IETF's own correction goes the
> other way. Collapsing this to one uniform table scales a standards-compliant
> agent's scale 14 as `10^18` instead of `10^15`, silently and a thousand-fold
> high. `TestSensorScaleExponentDiffersOnlyAtFourteenAndFifteen` fails
> immediately if anyone tries.

**Precision** only divides when it is positive.

- `1..9` — "the number of decimal places in the fractional part". Divide by
  `10^precision`.
- `0` — "not a fixed-point number". The value is the reading.
- `-8..-1` — "the number of *accurate* digits". This is an accuracy claim, not
  a scale factor. `CISCO-ENTITY-SENSOR-MIB` settles it with a worked example: a
  fan measuring 0–10,000 RPM in 100 RPM steps has precision `-2`, scale
  `units(9)`, and "a SensorValue ranging from 0 to 10000", with "the 10s and 1s
  digits … always 0". The value is already in RPM. Treating `-2` as a
  multiplier the way a positive precision is a divisor would publish that fan
  at 1,000,000 RPM.

A scale outside `1..17`, a precision outside `-8..9`, and the reserved
`±1000000000` overflow/underflow sentinels each produce a typed error and no
sample. The sentinels apply to the fixed-point physical types; for `other(1)`,
`unknown(2)` and `specialEnum(13)` the endpoints are ordinary readings and only
a value beyond the `Integer32` range is refused (`malformed_value`).

## Metric names

The unit is in the metric name, because a single metric holding dBm, degrees
and volts at once cannot be aggregated without every query first filtering by
type — and a query that forgets averages optical power with fan speed.

| `entSensorType` | Metric |
| --- | --- |
| `dBm(14)` | `snmp_entity_sensor_dbm` |
| `dB(15)` | `snmp_entity_sensor_db` |
| `celsius(8)` | `snmp_entity_sensor_celsius` |
| `voltsAC(3)`, `voltsDC(4)` | `snmp_entity_sensor_volts` |
| `amperes(5)` | `snmp_entity_sensor_amperes` |
| `watts(6)` | `snmp_entity_sensor_watts` |
| `hertz(7)` | `snmp_entity_sensor_hertz` |
| `rpm(10)` | `snmp_entity_sensor_rpm` |
| `percentRH(9)` | `snmp_entity_sensor_percent` |
| `cmm(11)` | `snmp_entity_sensor_cubic_meters_per_minute` |
| `other(1)`, `unknown(2)`, `truthvalue(12)`, `specialEnum(13)`, anything undeclared | `snmp_entity_sensor_value` |

`voltsAC` and `voltsDC` share the volts metric and stay apart by their
`sensor_type` label, since both are volts.

`dBm(14)` and `dB(15)` are **not** in either vendored MIB —
`ENTITY-SENSOR-MIB` is RFC 3433 from 2002, and the vendored
`CISCO-ENTITY-SENSOR-MIB` predates them too — but they are what a Cisco switch
answers for a transceiver's optical power. They are named in Go
(`internal/metrics/entity_sensor.go`) rather than by editing a vendored MIB, so
the vendored files stay byte-identical to what the vendor ships.

Consumers derive these names through `promname.EntitySensorMetricName`, pinned
to the exporter's own output by `internal/promname/contract_test.go`. This is
the one metric family that `promname.MetricName` cannot reach, because its name
comes from the reading's unit rather than from the MIB object that produced it.

## Labels

| Label | Source |
| --- | --- |
| `device` | the polled device's name |
| `oid` | the instance OID of the value column |
| `entphysicalindex` | the `entPhysicalIndex` the reading was read at |
| `entphysicalname` | `entPhysicalName`, falling back to `entPhysicalDescr` |
| `sensor_type` | the `EntitySensorDataType` label, e.g. `dBm`; `unrecognized` for any value neither MIB declares |

`entphysicalname` is what makes a reading identifiable: `entPhysicalIndex 3001`
means nothing on its own, `Ethernet1/3 Transceiver Receive Power Sensor` is the
port. It is absent when the device was polled without **ENTITY-MIB**, since an
empty Prometheus label and a missing one are the same series; that case
increments `gonetmon_entity_sensor_unnamed_total`.

`sensor_type` does not follow the index-label convention of naming the MIB
object, because the object is `entPhySensorType` in one MIB and `entSensorType`
in the other and a device may answer either.

Device tags are merged in the usual way and can never overwrite these
([sample-labels.md](sample-labels.md)).

## The raw value column is not exported

`entPhySensorValue` and `entSensorValue` are the only readings whose raw sample
is **suppressed** rather than supplemented: the wrong number must not reach
Prometheus under any name. The other four columns — type, scale, precision and
status — are still exported as ordinary readings. They are enumerations rather
than measurements, they are correct as integers, and an operator diagnosing a
mis-scaled reading needs to see them.

## What is collected, and from where

| MIB | Table | Needed for |
| --- | --- | --- |
| `CISCO-ENTITY-SENSOR-MIB` | `entSensorValueTable` (`.1.3.6.1.4.1.9.9.91.1.1.1.1`) | **all readings on Cisco hardware** |
| `ENTITY-SENSOR-MIB` | `entPhySensorTable` (`.1.3.6.1.2.1.99.1.1.1`) | readings on standards-compliant non-Cisco agents |
| `ENTITY-MIB` | `entPhysicalTable` (`.1.3.6.1.2.1.47.1.1.1.1`) | the `entphysicalname` join |

**On Cisco hardware the standard table is not the one with the readings.**
Walked on 2026-08-31, both a Nexus 9000 and a Catalyst C9300 answer the Cisco
table and return `No Such Object` for every column of
`.1.3.6.1.2.1.99.1.1.1` — NX-OS and IOS-XE alike. Cisco shipped its own copy of
this table before RFC 3433 existed and never moved off it. Assign
`CISCO-ENTITY-SENSOR-MIB` to any Cisco device; the standard module is for
everyone else.

Both sensor tables are `INDEX { entPhysicalIndex }` and number their first five
columns identically, so one decoder reads both, and assigning both is harmless:
a device that does not implement one answers `NoSuchObject` for every column of
it and teaches the cache nothing.

An agent that implements **both** is handled properly rather than merely
tolerated. Because the two tables share the `entPhysicalIndex` namespace, such
an agent describes the same physical sensor at the same index in each — using
each module's own enum for the quantity, since `10^18` is scale 14 in Cisco and
scale 15 in the standard module. Sensor metadata is therefore cached by
**(module, `entPhysicalIndex`)**, not by index alone: a reading resolves the
metadata written by the module it was read from, and nothing else. A reading
whose own module described nothing does not resolve at all and is dropped as
`missing_metadata` — the correct outcome, since scaling it with the other
module's metadata is exactly the thousand-fold error this design prevents.

Entity *names* are still keyed by index alone. `entPhysicalName` comes from
ENTITY-MIB's single `entPhysicalTable`, which both sensor modules index into,
so the name of physical entity 3001 is one fact regardless of which module
described its sensor.

**These modules have to be assigned to the device.** Collection is driven by the
per-device MIB assignments in `internal/webui/storage`, not by a profile
default: nothing here changes what an existing device polls. A switch with no
entity sensor module assigned reports no sensors, exactly as before.

## Nothing outlives its poll

There is no cache. `internal/metrics/entity_sensor_poll.go` is a pure function
of one poll's results, and everything a reading needs — its type, scale,
precision, status **and** its `entPhysicalName` — comes from the poll the
reading arrived in.

The reason is the same for the numbers and for the label. The only data
provably describing the same hardware as a reading is data that arrived with
it. Anything carried across polls is a bet that the hardware did not change in
between, and when an `entPhysicalIndex` is reused nothing in the protocol says
it did: a same-type sensor swap changes no column the collector can see, and
for a transceiver `entPhysicalName` is the **port**, so swapping the optic
leaves the name identical.

This was arrived at in two steps, and the second is worth recording because the
first looked sufficient. Metadata was made per-poll to stop a reused index
being scaled by a previous sensor's precision. Names were kept, on the
reasoning that a stale name *mislabels* rather than *mis-scales* and is
therefore the lesser harm. It is lesser, and it is still a confident lie: an
operator reading `Ethernet1/3 Rx Power` for a sensor that is now `Ethernet1/5`
acts on it exactly as readily as on a wrong number. By this document's own
standard an obvious gap beats a plausible falsehood, so an **unnamed** reading
with `gonetmon_entity_sensor_unnamed_total` incrementing is the better outcome.

**Nothing is lost by refusing to remember.** A poll is atomic here:
`internal/poller` resolves every assigned module into one OID set, walks them
all in one cycle, accumulates the results into one slice and exports it in a
single call. So ENTITY-MIB's names arrive in the same poll as the sensor
columns whenever ENTITY-MIB is assigned, and when it is not there are no names
to remember. The only case a cache ever covered was a cycle in which one
module's walk failed while another's succeeded — and that cycle now exports
readings that are unnamed, or no readings at all if it was a metadata column
that failed, and recovers on the next cycle.

What this buys beyond correctness: no shared state, so no lock, no
copy-on-write, no eviction policy, and no bound to choose. A poll's maps are
bounded by the poll.

## Self-instrumentation

A refusal to publish is a deliberate silence, and an uncounted silence cannot
be told from a collector that was never wired up.

| Metric | Meaning |
| --- | --- |
| `gonetmon_entity_sensor_samples_total{device,sensor_type}` | readings scaled and exported |
| `gonetmon_entity_sensor_dropped_total{device,reason}` | readings not exported, by cause |
| `gonetmon_entity_sensor_unnamed_total{device}` | exported readings with no entity name — ENTITY-MIB is not assigned |

Both the `reason` label above and the `sensor_type` label on readings are
**closed sets**. `sensor_type` is derived from an `EntitySensorDataType` the
device chose, so every value neither MIB declares collapses to the single label
`unrecognized` — not `unknown`, which is a declared type of its own meaning
"arbitrary, relative numbers". The exact integer stays in diagnostics.
`TestEntitySensorTypeLabelsStayBounded` pins it by giving one device 4
undeclared types and another 400 and requiring the same label set.

`reason` is one of `missing_metadata`, `missing_type`, `missing_scale`,
`missing_precision`, `missing_status`, `missing_reading`, `invalid_scale`,
`invalid_precision`, `overflow`, `underflow`, `malformed_value`,
`status_unavailable`, `status_nonoperational`, `status_unknown`.

**That set is closed, and deliberately so.** The reason for a status drop is
derived from an `Integer32` the device chose, so every status outside the two
the MIBs declare collapses to the single `status_unknown` rather than becoming
its own label value. A device answering a fresh status each poll would
otherwise mint one Prometheus time series per response, on the very counter
that exists to report devices behaving badly. The exact status stays in the
error message, which is where someone diagnosing one sensor looks.
`TestEntitySensorDropReasonsStayBounded` pins it by sweeping 6 and then 400
distinct statuses and requiring the same label set from both.

A reading is published only when the sensor reports `ok(1)`.
`unavailable(2)` means the agent cannot presently read the sensor and
`nonoperational(3)` means it believes the sensor is broken; in both cases the
value column still holds a number that is not a measurement. The status column
itself is still exported, so a broken sensor is visible as data rather than
only as a gap.

**A sensor that reports no status at all is also not published**, and is
counted as `missing_status`. The column is mandatory in both MIBs, so an agent
omitting it is non-conforming and its readings are unvalidated. An earlier
version published them, on the reasoning that refusing would drop a whole table
over one absent column; measured against the fleet on 2026-08-31 no device
omits it — dist-01 and dist-02 return a status row for all 104 of their
sensors and access-01 for all 13 — so the leniency defended nobody and left a
fail-open in the one path whose premise is that an unvalidated number is worse
than no number.

## Querying

Current optical power per port:

    snmp_entity_sensor_dbm

Receive power on one link, over time:

    snmp_entity_sensor_dbm{device="access-01", entphysicalname=~".*Te1/1/1.*Receive.*"}

Drift from a week ago — the signal that would have caught the access-01
fusion splice, which ran 5.9 dB out of spec for over a month while carrying
production traffic with zero CRC errors and never going down:

    snmp_entity_sensor_dbm - snmp_entity_sensor_dbm offset 7d

Alerting and thresholds are out of scope here.

## Verification

`internal/metrics/entity_sensor_dom_test.go` runs the varbinds the production
switches actually put on the wire through the real conversion path. Every row
was captured by `snmpwalk` on 2026-08-31 rather than derived from the dBm
figures, which matters: an encoding invented to match a known answer cannot
fail, and an earlier draft of this fixture guessed the Nexus encoding wrong
while still passing.

The two families differ by **scale**, not by precision, and between them they
exercise both halves of the arithmetic against real encodings — the scale
ladder on one and the fixed-point divisor on the other.

| Device | Port | Direction | Scale | Precision | Raw | dBm |
| --- | --- | --- | --- | --- | --- | --- |
| dist-01 | Eth1/3 | Tx | `milli(8)` | 0 | -1163 | -1.163 |
| dist-01 | Eth1/3 | Rx | `milli(8)` | 0 | -7328 | -7.328 |
| dist-02 | Eth1/3 | Tx | `milli(8)` | 0 | -30 | -0.03 |
| dist-02 | Eth1/3 | Rx | `milli(8)` | 0 | -6420 | -6.42 |
| access-01 | Te1/1/1 | Tx | `units(9)` | 1 | -14 | -1.4 |
| access-01 | Te1/1/1 | Rx | `units(9)` | 1 | -75 | -7.5 |
| access-01 | Te1/1/2 | Tx | `units(9)` | 1 | -2 | -0.2 |
| access-01 | Te1/1/2 | Rx | `units(9)` | 1 | -69 | -6.9 |

The test pins the dBm column — what these varbinds must decode to — and not the
switch CLI. Optical power drifts by a few tenths of a dB between samples, so a
CLI-pinned assertion would flap on healthy hardware. The issue's CLI figures
(-1.15 / -7.87 on dist-01, -7.4 on access-01 Te1/1/1 Rx) sit within
0.5 dB of these, which is drift; a scale or precision error would land a factor
of ten or a thousand out.

Note also that the two families word `entPhysicalName` differently — the Nexus
says `Ethernet1/3 Lane 1 Transceiver Receive Power Sensor`, the Catalyst says
`Te1/1/1 Receive Power Sensor` with no "Transceiver" in it — so the fixture
quotes each name as walked rather than composing them.
