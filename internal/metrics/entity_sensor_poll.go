package metrics

import (
	"errors"
	"math"
	"strings"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/jeremyhahn/go-netmon/internal/snmp"
)

// Entity sensor metadata and names come only from the poll the reading arrived in; nothing is cached across polls.

// Column OIDs (no leading dot) of the two sensor tables and the entity table; all are INDEX { entPhysicalIndex }.
const (
	// entPhySensorEntry, ENTITY-SENSOR-MIB (RFC 3433), mib-2 99.
	entPhySensorEntryOID = "1.3.6.1.2.1.99.1.1.1"

	// entSensorValueEntry, CISCO-ENTITY-SENSOR-MIB, ciscoMgmt 91; some Cisco devices answer only this table.
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

// Structural labels beyond device and oid; sensor_type is not named after either MIB's object.
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

// sensorMetadata is what a sensor row says about reading its value; presence flags distinguish absent from precision 0.
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

// sensorKey identifies one sensor's metadata by module and entPhysicalIndex, since the two MIBs' scale enums differ.
type sensorKey struct {
	table SensorTable
	index string
}

// pollSensors is the sensor metadata and entity names one poll carried; sensors missing a column publish nothing that cycle.
type pollSensors struct {
	metadata map[sensorKey]sensorMetadata
	names    map[string]string
	descrs   map[string]string
}

// empty reports whether the poll carries nothing worth publishing or caching.
func (p *pollSensors) empty() bool {
	return p == nil || (len(p.metadata) == 0 && len(p.names) == 0 && len(p.descrs) == 0)
}

// entityName returns this poll's entPhysicalName for a sensor, else entPhysicalDescr, else empty.
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

	// All four columns are Integer32; values out of range are rejected rather than narrowed.
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

// entitySensorColumn splits a sensor-table instance OID into its column and its (module, index) key.
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

// tableColumnAndIndex splits a single-integer-indexed instance OID into column and index, rejecting longer suffixes.
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

// entitySensorValueKey reports whether an OID is a sensor value column and returns its (module, index) key.
func entitySensorValueKey(oid string) (key sensorKey, ok bool) {
	column, key, ok := entitySensorColumn(oid)
	if !ok || column != sensorColumnValue {
		return sensorKey{}, false
	}
	return key, true
}

// sensorSample builds the base-unit sample for one sensor reading, or the typed error saying why there is none.
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

	// The status column is required: both MIBs make it mandatory.
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

// sensorMetadataFor returns what this poll observed about one sensor from one module.
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
