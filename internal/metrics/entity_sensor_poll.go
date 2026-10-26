package metrics

import (
	"errors"
	"math"
	"strings"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/jeremyhahn/go-netmon/internal/snmp"
)

// Everything an entity sensor reading needs comes from the poll it arrived in,
// and nothing here outlives that poll.
//
// Scaling a reading needs its type, scale, precision and status; labeling it
// needs its entPhysicalName. The only such data provably describing the same
// hardware as the reading is data that arrived in the same poll. Anything
// carried across polls is a bet that the hardware did not change in between,
// and when an entPhysicalIndex is reused nothing in the protocol says it did:
// a same-type sensor swap changes no column the collector can see, and for a
// transceiver entPhysicalName is the *port*, so swapping the optic leaves the
// name identical.
//
// This file used to hold a cache, and it held one twice. The first version
// carried metadata across polls, which mis-scaled a reused index by a factor
// of a thousand. The second carried only names, on the reasoning that a stale
// name mislabels rather than mis-scales and is therefore the lesser harm. It
// is lesser, and it is still a confident lie: an operator reading
// "Ethernet1/3 Rx Power" for a sensor that is now Ethernet1/5 acts on it
// exactly as readily as on a wrong number. By this package's own standard an
// obvious gap beats a plausible falsehood, so a reading published with no name
// -- and gonetmon_entity_sensor_unnamed_total incrementing -- is the better
// outcome.
//
// Nothing is lost by refusing to remember. A poll is atomic here:
// internal/poller resolves every assigned module into one OID set, walks them
// all in one cycle, accumulates the results into one slice and exports it in a
// single call. So ENTITY-MIB's names arrive in the same poll as the sensor
// columns whenever ENTITY-MIB is assigned, and when it is not there are no
// names to remember. The only case a cache ever covered was a cycle in which
// one module's walk failed while another's succeeded, and that cycle now
// exports unnamed readings and recovers on the next one.
//
// What is left is a pure function of one poll's results, so there is no shared
// state, no lock, no copy-on-write and nothing to bound.

// Column OIDs of the two sensor tables and of the entity table their rows are
// named from. Both sensor tables are INDEX { entPhysicalIndex }, so a row of
// either joins to entPhysicalTable on a single sub-identifier.
//
// Every OID here is normalized without its leading dot, which is how
// entitySensorOIDPart compares them.
const (
	// entPhySensorEntry, ENTITY-SENSOR-MIB (RFC 3433), mib-2 99. No Cisco
	// device in this fleet answers it -- see the Cisco table below -- but it
	// is what a standards-compliant agent implements, and decoding both
	// costs one extra prefix comparison per varbind.
	entPhySensorEntryOID = "1.3.6.1.2.1.99.1.1.1"

	// entSensorValueEntry, CISCO-ENTITY-SENSOR-MIB, ciscoMgmt 91. Cisco
	// shipped its own copy of this table before RFC 3433 existed and never
	// moved off it: walked on 2026-08-31, both a Nexus 9000 and a Catalyst
	// C9300 answer this table and return No Such Object for every column of
	// the standard one above. On Cisco hardware this is the table that has
	// the readings; the standard table is decoded for everyone else.
	entSensorValueEntryOID = "1.3.6.1.4.1.9.9.91.1.1.1.1"

	// entPhysicalEntry, ENTITY-MIB.
	entPhysicalEntryOID = "1.3.6.1.2.1.47.1.1.1.1"
)

// Column sub-identifiers within the sensor entries. The two tables number
// their first five columns identically -- type, scale, precision, value,
// status -- which is what lets one decoder read both.
const (
	sensorColumnType      = "1"
	sensorColumnScale     = "2"
	sensorColumnPrecision = "3"
	sensorColumnValue     = "4"
	sensorColumnStatus    = "5"
)

// Column sub-identifiers within entPhysicalEntry.
const (
	physicalColumnDescr = "2"
	physicalColumnName  = "7"
)

// Structural labels an entity sensor sample carries beyond the shared device
// and oid labels.
//
// entphysicalindex and entphysicalname follow the convention the rest of the
// package uses for MIB-derived labels: the object's own name, lowercased
// (promname.IndexLabel). sensor_type does not, because the object it comes
// from is called entPhySensorType in one MIB and entSensorType in the other,
// and a label named after one of them would be wrong on half the devices.
const (
	labelEntityIndex = "entphysicalindex"
	labelEntityName  = "entphysicalname"
	labelSensorType  = "sensor_type"
)

// Self-instrumentation. A reading this package refuses to publish is a
// deliberate silence, and a silence that is not counted is indistinguishable
// from a collector that was never wired up.
var (
	entitySensorSamplesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gonetmon_entity_sensor_samples_total",
			Help: "Total number of entity sensor readings scaled and exported, by device and sensor type.",
		},
		[]string{"device", "sensor_type"},
	)
	entitySensorDropped = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gonetmon_entity_sensor_dropped_total",
			Help: "Total number of entity sensor readings not exported because they could not be interpreted, by device and reason (missing metadata, out-of-range scale or precision, overflow, underflow, or a sensor status other than ok).",
		},
		[]string{"device", "reason"},
	)
	entitySensorUnnamed = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gonetmon_entity_sensor_unnamed_total",
			Help: "Total number of exported entity sensor readings with no entPhysicalName or entPhysicalDescr to identify them, which means ENTITY-MIB is not assigned to the device.",
		},
		[]string{"device"},
	)
)

func init() {
	prometheus.MustRegister(
		entitySensorSamplesTotal,
		entitySensorDropped,
		entitySensorUnnamed,
	)
}

// sensorMetadata is what a sensor row says about how to read its value.
//
// The presence flags are not redundant with zero values. Scale and type have
// no valid zero, but precision 0 is a legal value meaning "not a fixed-point
// number", so an absent precision and a precision of zero would otherwise be
// the same thing -- and reading a value with an assumed precision of 0 when
// the true precision is 2 publishes it a hundred times too large.
type sensorMetadata struct {
	sensorType    SensorType
	scale         SensorScale
	precision     SensorPrecision
	status        SensorStatus
	haveType      bool
	haveScale     bool
	havePrecision bool
	haveStatus    bool
}

// sensorKey identifies one sensor's metadata: the module that described it and
// the entPhysicalIndex it described.// sensorKey identifies one sensor's metadata: the module that described it and
// the entPhysicalIndex it described.
//
// The module is half the key rather than a field on the metadata, and that is
// the point. Both sensor tables are INDEX { entPhysicalIndex }, so a device
// implementing both reports the same physical sensor at the same index in each
// -- with each module's own enum for the quantity, since scale 14 is 10^18 in
// CISCO-ENTITY-SENSOR-MIB and 10^15 in ENTITY-SENSOR-MIB. Keyed by index
// alone, the two modules' rows collide: whichever module's poll wrote last
// decides how the other module's readings are scaled, and the answer is a
// thousand-fold out for one of them, chosen by poll ordering.
//
// Making the module part of the key turns that into two namespaces that cannot
// reach each other. A value read from one module resolves metadata written by
// that module and nothing else, which also means a value whose own module
// described nothing simply does not resolve. That is the correct outcome
// rather than a regression: scaling a reading with the other module's metadata
// is the bug.
//
// It also removes a field. The metadata used to carry the table its scale came
// from, kept in step with the scale by hand in three places; now the key says
// it, so there is nothing left to keep in step and nothing that can disagree.
type sensorKey struct {
	table SensorTable
	index string
}

// pollSensors is what one poll observed: the sensor metadata it carried, and
// the entity names it carried.
//
// The metadata half never outlives the poll it came from, and that is the
// whole design. Scaling a reading needs its type, scale, precision and status,
// and the only metadata provably describing the same hardware as the reading
// is metadata that arrived in the same poll. Anything carried across polls is
// a bet that the hardware did not change in between -- and when an
// entPhysicalIndex is reused by a sensor of the same type, nothing in the
// protocol says it did. That bet publishes a plausible wrong number, which is
// the one outcome this whole path exists to avoid.
//
// A poll is the right unit because a poll is atomic here: internal/poller
// accumulates every walk of a device into one slice and exports it in a single
// call, so all five columns of a sensor arrive together or the poll did not
// collect them. A cycle in which a column's walk failed, or which an operator
// filtered or truncated below the full column set, publishes nothing for the
// affected sensors and says so through gonetmon_entity_sensor_dropped_total.
// It recovers on the next cycle that does collect them. Refusing is right:
// there is no correct reading to produce from metadata that was never read.
type pollSensors struct {
	metadata map[sensorKey]sensorMetadata
	names    map[string]string
	descrs   map[string]string
}

// empty reports whether the poll carries nothing worth publishing or caching.
func (p *pollSensors) empty() bool {
	return p == nil || (len(p.metadata) == 0 && len(p.names) == 0 && len(p.descrs) == 0)
}

// entityName returns the friendly name this poll reported for a sensor's
// entPhysicalIndex, or empty if it reported none.
//
// entPhysicalName is preferred and entPhysicalDescr is the fallback, in that
// order, because entPhysicalName is what the device calls the port in its own
// CLI -- "Ethernet1/3 Transceiver Receive Power" -- while entPhysicalDescr is
// the vendor's description of the part. Either identifies the reading; only
// one of them is the name an operator would search for.
//
// A name from an earlier poll is not a fallback, because it is not reachable:
// see the file comment. An unnamed reading is counted, not guessed at.
func (p *pollSensors) entityName(index string) string {
	if p == nil {
		return ""
	}
	if name := p.names[index]; name != "" {
		return name
	}
	return p.descrs[index]
}

func observeEntitySensors(results []*snmp.Result) *pollSensors {
	observed := &pollSensors{
		metadata: map[sensorKey]sensorMetadata{},
		names:    map[string]string{},
		descrs:   map[string]string{},
	}

	for _, result := range results {
		if result == nil || !result.HasValue() {
			continue
		}

		if column, key, ok := entitySensorColumn(result.OID); ok {
			observeSensorColumn(observed, column, key, result)
			continue
		}

		if column, index, ok := entityPhysicalOIDPart(result.OID); ok {
			observeEntityColumn(observed, column, index, result)
		}
	}

	return observed
}

// observeSensorColumn records one metadata column of one sensor row. The value
// column is not metadata and is skipped here; it is read on the sample path,
// where the metadata to interpret it is already published.
func observeSensorColumn(observed *pollSensors, column string, key sensorKey, result *snmp.Result) {
	number, err := result.GetInt64Value()
	if err != nil {
		return
	}

	// All four columns are Integer32 in both MIBs, so a value that does not
	// fit one did not come from a conforming agent. Narrowing it anyway is
	// the dangerous option rather than the lenient one: truncation wraps, so
	// a scale column answered with 4294967305 would arrive as units(9) --
	// a value that passes every check downstream and scales every reading of
	// that sensor by the wrong factor of a thousand.
	narrowed, fits := int32Value(number)
	if !fits {
		return
	}

	metadata := observed.metadata[key]
	switch column {
	case sensorColumnType:
		metadata.sensorType, metadata.haveType = SensorType(narrowed), true
	case sensorColumnScale:
		metadata.scale, metadata.haveScale = SensorScale(narrowed), true
	case sensorColumnPrecision:
		metadata.precision, metadata.havePrecision = SensorPrecision(narrowed), true
	case sensorColumnStatus:
		metadata.status, metadata.haveStatus = SensorStatus(narrowed), true
	default:
		return
	}
	observed.metadata[key] = metadata
}

// int32Value narrows an SNMP integer to the Integer32 the MIBs declare, and
// reports whether it fit.
func int32Value(number int64) (int32, bool) {
	if number < math.MinInt32 || number > math.MaxInt32 {
		return 0, false
	}
	return int32(number), true
}

// observeEntityColumn records an entPhysicalName or entPhysicalDescr. Trailing
// NULs and surrounding whitespace are trimmed the same way a text scalar's are,
// since an empty label cannot be told from an absent one.
func observeEntityColumn(observed *pollSensors, column, index string, result *snmp.Result) {
	text, err := result.GetStringValue()
	if err != nil {
		return
	}
	text = strings.TrimSpace(strings.TrimRight(text, "\x00"))
	if text == "" {
		return
	}

	switch column {
	case physicalColumnName:
		observed.names[index] = text
	case physicalColumnDescr:
		observed.descrs[index] = text
	}
}

// entitySensorColumn splits an instance OID of either sensor table into the
// column it reads and the sensor it identifies, and reports whether it is one
// at all.
//
// The module in the returned key is not decoration. It is what
// SensorScale.Exponent needs to read a scale of 14 or 15, and it is what keeps
// two modules' descriptions of the same entPhysicalIndex apart. The OID prefix
// is the only place that information exists -- a varbind carries no other hint
// of which module defined the column -- and the two tables live under
// different arcs, so the match is unambiguous.
func entitySensorColumn(oid string) (column string, key sensorKey, ok bool) {
	if col, index, matched := tableColumnAndIndex(oid, entPhySensorEntryOID); matched {
		return col, sensorKey{table: SensorTableStandard, index: index}, true
	}
	col, index, matched := tableColumnAndIndex(oid, entSensorValueEntryOID)
	return col, sensorKey{table: SensorTableCisco, index: index}, matched
}

// entityPhysicalOIDPart splits an instance OID of entPhysicalTable into its
// column and its entPhysicalIndex, and reports whether it is one.
func entityPhysicalOIDPart(oid string) (column, index string, ok bool) {
	return tableColumnAndIndex(oid, entPhysicalEntryOID)
}

// tableColumnAndIndex splits an instance OID of a table indexed by a single
// integer into its column sub-identifier and its index.
//
// It rejects anything with more than one sub-identifier after the column,
// rather than taking the last one as the index the way the generic
// ExtractTableIndex fallback does. All three tables here are INDEX { one
// Integer32 }, so a longer suffix is not a row of this table, and guessing an
// index for it would attach a reading to a sensor that never reported it.
func tableColumnAndIndex(oid, entryOID string) (column, index string, ok bool) {
	suffix := strings.TrimPrefix(oid, ".")
	if !strings.HasPrefix(suffix, entryOID+".") {
		return "", "", false
	}

	suffix = suffix[len(entryOID)+1:]
	column, index, found := strings.Cut(suffix, ".")
	if !found || column == "" || index == "" || strings.Contains(index, ".") {
		return "", "", false
	}
	return column, index, true
}

// entitySensorValueKey reports whether an instance OID reads the value column
// of either sensor table, and returns the sensor it reads.
//
// The module in the key is the module the reading itself came from, which is
// the one whose metadata may interpret it. Discarding it here was the defect
// this key exists to close: it let a value from one module be scaled with the
// other module's scale.
func entitySensorValueKey(oid string) (key sensorKey, ok bool) {
	column, key, ok := entitySensorColumn(oid)
	if !ok || column != sensorColumnValue {
		return sensorKey{}, false
	}
	return key, true
}

// sensorSample builds the scaled sample for one reading of a sensor value
// column, or returns the typed error saying why there is none.
//
// The reading is published in the base unit of its type, under a metric name
// that names that unit, labeled with the entity it was read from. Everything
// this needs beyond the reading itself comes from the published snapshot, so
// building a sample takes no lock.
func sensorSample(
	deviceName string,
	key sensorKey,
	result *snmp.Result,
	extraLabels map[string]string,
	poll *pollSensors,
) (*Sample, error) {
	entityIndex := key.index

	// Everything below comes from this poll and only this poll.
	metadata, known := sensorMetadataFor(poll, key)
	if !known {
		return nil, &SensorMetadataError{Device: deviceName, Index: entityIndex, Missing: "metadata"}
	}
	if !metadata.haveType {
		return nil, &SensorMetadataError{Device: deviceName, Index: entityIndex, Missing: "type"}
	}
	if !metadata.haveScale {
		return nil, &SensorMetadataError{Device: deviceName, Index: entityIndex, Missing: "scale"}
	}
	if !metadata.havePrecision {
		return nil, &SensorMetadataError{Device: deviceName, Index: entityIndex, Missing: "precision"}
	}

	// The status column is required, not optional.
	//
	// This used to publish a reading whose status the device never sent, on
	// the reasoning that refusing them would drop the whole table for an
	// agent that omits one column. Measured against the fleet on
	// 2026-08-31, no such agent exists here: dist-01 and dist-02
	// return a status row for all 104 of their sensors and access-01 for
	// all 13, every one of them ok(1). The column is mandatory in both
	// MIBs, so an agent omitting it is non-conforming and its readings are
	// unvalidated -- and publishing an unvalidated reading is the one thing
	// this whole path exists not to do. The leniency cost a real fail-open
	// and bought nothing.
	if !metadata.haveStatus {
		return nil, &SensorMetadataError{Device: deviceName, Index: entityIndex, Missing: "status"}
	}
	if !metadata.status.Readable() {
		return nil, &SensorStatusError{Device: deviceName, Index: entityIndex, Status: metadata.status}
	}

	raw, err := result.GetInt64Value()
	if err != nil {
		return nil, &SensorMetadataError{Device: deviceName, Index: entityIndex, Missing: "reading"}
	}

	// The module is the key's, so the scale is read with the semantics of
	// the module that both described this sensor and produced this reading.
	value, err := ScaleSensorValue(raw, metadata.sensorType, metadata.scale, metadata.precision, key.table)
	if err != nil {
		return nil, err
	}

	labels := newInstanceLabels(deviceName, result.OID)
	labels[labelEntityIndex] = entityIndex
	labels[labelSensorType] = metadata.sensorType.Label()
	if name := poll.entityName(entityIndex); name != "" {
		labels[labelEntityName] = name
	} else {
		entitySensorUnnamed.WithLabelValues(deviceName).Inc()
	}
	mergeExtraLabels(deviceName, labels, extraLabels)

	entitySensorSamplesTotal.WithLabelValues(deviceName, metadata.sensorType.Label()).Inc()

	return &Sample{
		Name:      metadata.sensorType.MetricName(),
		Labels:    labels,
		Value:     value,
		Timestamp: result.Timestamp,
	}, nil
}

// sensorMetadataFor returns what one poll observed about one sensor, as
// described by one module.
//
// Metadata written by the other module is not a fallback: it describes a
// different row of a different table that happens to share an index. Neither
// is metadata from an earlier poll, which is not available here at all.
func sensorMetadataFor(poll *pollSensors, key sensorKey) (sensorMetadata, bool) {
	if poll == nil {
		return sensorMetadata{}, false
	}
	metadata, known := poll.metadata[key]
	return metadata, known
}

// countSensorDrop records a reading that was not published, under the reason
// its error names.
func countSensorDrop(deviceName string, err error) {
	reason := "unknown"
	var drop sensorDropError
	if errors.As(err, &drop) {
		reason = drop.DropReason()
	}
	entitySensorDropped.WithLabelValues(deviceName, reason).Inc()
}
