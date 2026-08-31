package metrics

import (
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/jeremyhahn/go-netmon/internal/snmp"
)

// The two sensor tables and the entity table, spelled with the leading dot a
// device's responses carry, so the tests exercise the same normalization the
// poll path does.
const (
	stdSensorEntry   = "." + entPhySensorEntryOID
	ciscoSensorEntry = "." + entSensorValueEntryOID
	physicalEntry    = "." + entPhysicalEntryOID
)

// sensorTestTime is the timestamp every synthetic result carries, so a
// sample's timestamp can be asserted exactly.
var sensorTestTime = time.Unix(1756600000, 0)

// integerResult builds the varbind a device returns for an integer column.
func integerResult(oid string, value int) *snmp.Result {
	return &snmp.Result{
		OID:       oid,
		Type:      gosnmp.Integer,
		Value:     value,
		Timestamp: sensorTestTime,
	}
}

// stringResult builds the varbind a device returns for an octet string column,
// as the raw bytes gosnmp delivers.
func stringResult(oid, value string) *snmp.Result {
	return &snmp.Result{
		OID:       oid,
		Type:      gosnmp.OctetString,
		Value:     []byte(value),
		Timestamp: sensorTestTime,
	}
}

// column returns the instance OID of one column of one row.
func column(entryOID, columnID, index string) string {
	return entryOID + "." + columnID + "." + index
}

// testPoll observes one poll and remembers it, so a test can read that poll's
// readings the way the conversion path does.
//
// It exists only to keep the tests reading like the code: observe a poll, then
// scale its readings with what it observed. There is no state behind it -- a
// second Update simply replaces the first, which is exactly what happens in
// production, where nothing about a sensor survives its poll.
type testPoll struct {
	poll *pollSensors
}

func newTestSensorCache() *testPoll {
	return &testPoll{}
}

func (c *testPoll) Update(_ string, results []*snmp.Result) *pollSensors {
	c.poll = observeEntitySensors(results)
	return c.poll
}

func (c *testPoll) sensorSample(
	device string, key sensorKey, reading *snmp.Result, extra map[string]string,
) (*Sample, error) {
	return sensorSample(device, key, reading, extra, c.poll)
}

// ciscoKey and stdKey name a sensor as each module describes it. Metadata is
// keyed by module and index together, so a test has to say which module's row
// it means -- which is the distinction the key exists to enforce.
func ciscoKey(index string) sensorKey {
	return sensorKey{table: SensorTableCisco, index: index}
}

func stdKey(index string) sensorKey {
	return sensorKey{table: SensorTableStandard, index: index}
}

// sensorRow returns the five varbinds a walk of one sensor row returns:
// type, scale, precision, value and status, in the order a walk delivers them.
func sensorRow(entryOID, index string, sensorType SensorType, scale SensorScale, precision SensorPrecision, raw int, status SensorStatus) []*snmp.Result {
	return []*snmp.Result{
		integerResult(column(entryOID, sensorColumnType, index), int(sensorType)),
		integerResult(column(entryOID, sensorColumnScale, index), int(scale)),
		integerResult(column(entryOID, sensorColumnPrecision, index), int(precision)),
		integerResult(column(entryOID, sensorColumnValue, index), raw),
		integerResult(column(entryOID, sensorColumnStatus, index), int(status)),
	}
}

// TestEntitySensorCacheLearnsMetadata checks that one walk of a sensor row
// teaches the cache everything needed to read that row's value, from either
// sensor MIB.
func TestEntitySensorCacheLearnsMetadata(t *testing.T) {
	tests := []struct {
		name     string
		entryOID string
		key      sensorKey
	}{
		{"ENTITY-SENSOR-MIB", stdSensorEntry, stdKey("3001")},
		{"CISCO-ENTITY-SENSOR-MIB", ciscoSensorEntry, ciscoKey("3001")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := newTestSensorCache()
			cache.Update("dist-01", sensorRow(tt.entryOID, "3001", SensorTypeDBm, SensorScaleUnits, 2, -115, SensorStatusOK))

			metadata, known := sensorMetadataFor(cache.poll, tt.key)
			if !known {
				t.Fatal("sensor 3001 not learned")
			}

			// ...and nothing was written under the other module's name.
			other := stdKey("3001")
			if tt.key == other {
				other = ciscoKey("3001")
			}
			if _, crossed := sensorMetadataFor(cache.poll, other); crossed {
				t.Errorf("walking %s also wrote metadata under %s", tt.name, other.table)
			}
			if !metadata.haveType || metadata.sensorType != SensorTypeDBm {
				t.Errorf("sensorType = %v (have=%v), want dBm", metadata.sensorType, metadata.haveType)
			}
			if !metadata.haveScale || metadata.scale != SensorScaleUnits {
				t.Errorf("scale = %v (have=%v), want units", metadata.scale, metadata.haveScale)
			}
			if !metadata.havePrecision || metadata.precision != 2 {
				t.Errorf("precision = %v (have=%v), want 2", metadata.precision, metadata.havePrecision)
			}
			if !metadata.haveStatus || metadata.status != SensorStatusOK {
				t.Errorf("status = %v (have=%v), want ok", metadata.status, metadata.haveStatus)
			}
		})
	}
}

// TestEntitySensorCacheLearnsEntityNames checks the ENTITY-MIB join, including
// the fallback: entPhysicalName is preferred, entPhysicalDescr is used when
// the device leaves the name empty, and a row with neither has no name.
func TestEntitySensorCacheLearnsEntityNames(t *testing.T) {
	cache := newTestSensorCache()
	cache.Update("dist-01", []*snmp.Result{
		stringResult(column(physicalEntry, physicalColumnName, "3001"), "Ethernet1/3 Transceiver Transmit Power Sensor"),
		stringResult(column(physicalEntry, physicalColumnDescr, "3001"), "Transceiver Tx Power Sensor"),
		stringResult(column(physicalEntry, physicalColumnName, "3002"), ""),
		stringResult(column(physicalEntry, physicalColumnDescr, "3002"), "Transceiver Rx Power Sensor"),
	})

	sensors := cache.poll

	tests := []struct {
		name  string
		index string
		want  string
	}{
		{"name preferred over descr", "3001", "Ethernet1/3 Transceiver Transmit Power Sensor"},
		{"descr used when name is empty", "3002", "Transceiver Rx Power Sensor"},
		{"neither known", "9999", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sensors.entityName(tt.index); got != tt.want {
				t.Errorf("entityName(%s) = %q, want %q", tt.index, got, tt.want)
			}
		})
	}
}

// TestEntitySensorCacheTrimsEntityNames checks that the padding a device puts
// around a name is removed, since an all-whitespace name would become a label
// that looks present and says nothing.
func TestEntitySensorCacheTrimsEntityNames(t *testing.T) {
	cache := newTestSensorCache()
	cache.Update("access-01", []*snmp.Result{
		stringResult(column(physicalEntry, physicalColumnName, "1"), "  Te1/1/1 Transceiver  \x00\x00"),
		stringResult(column(physicalEntry, physicalColumnName, "2"), "   \x00"),
	})

	sensors := cache.poll
	if got := sensors.entityName("1"); got != "Te1/1/1 Transceiver" {
		t.Errorf("entityName(1) = %q, want %q", got, "Te1/1/1 Transceiver")
	}
	if got := sensors.entityName("2"); got != "" {
		t.Errorf("entityName(2) = %q, want empty", got)
	}
}

// TestEntitySensorCacheForgetsMetadataBetweenPolls is the inverse of a test
// this file used to carry.
//
// It asserted that metadata learned by a full walk still interpreted the
// readings of a later poll that returned the value column alone -- presented
// as the reason the cache existed. It was also the mechanism by which a reused
// entPhysicalIndex inherited a previous sensor's scale, so the behavior is
// gone and this pins its absence.
//
// The cost is real and accepted: a device whose poll does not carry the
// metadata columns publishes nothing for those sensors that cycle, and says so
// through the drop counter. There is no correct reading to produce from
// metadata that was not read.
func TestEntitySensorCacheForgetsMetadataBetweenPolls(t *testing.T) {
	cache := newTestSensorCache()
	cache.Update("dist-01", sensorRow(ciscoSensorEntry, "3001", SensorTypeDBm, SensorScaleUnits, 2, -115, SensorStatusOK))

	// A later poll of the value column only carries no metadata at all.
	cache.Update("dist-01", []*snmp.Result{
		integerResult(column(ciscoSensorEntry, sensorColumnValue, "3001"), -120),
	})

	sample, err := cache.sensorSample("dist-01", ciscoKey("3001"),
		integerResult(column(ciscoSensorEntry, sensorColumnValue, "3001"), -120), nil)

	if err == nil {
		t.Fatalf("published %v from a poll that carried no metadata", sample.Value)
	}
	var metadataErr *SensorMetadataError
	if !errors.As(err, &metadataErr) {
		t.Fatalf("error = %T (%v), want *SensorMetadataError", err, err)
	}
}

// TestEntitySensorCacheAccumulatesColumnsWithinOnePoll checks the other side:
// within a single poll the columns still combine, in whatever order the
// varbinds arrive. That is what makes a normal walk work, since its five
// columns are five separate varbinds.
func TestEntitySensorCacheAccumulatesColumnsWithinOnePoll(t *testing.T) {
	cache := newTestSensorCache()

	// The columns of one sensor, deliberately out of column order.
	cache.Update("sw", []*snmp.Result{
		integerResult(column(ciscoSensorEntry, sensorColumnStatus, "1"), int(SensorStatusOK)),
		integerResult(column(ciscoSensorEntry, sensorColumnPrecision, "1"), 2),
		integerResult(column(ciscoSensorEntry, sensorColumnType, "1"), int(SensorTypeDBm)),
		integerResult(column(ciscoSensorEntry, sensorColumnScale, "1"), int(SensorScaleUnits)),
	})

	sample, err := cache.sensorSample("sw", ciscoKey("1"),
		integerResult(column(ciscoSensorEntry, sensorColumnValue, "1"), -115), nil)
	if err != nil {
		t.Fatalf("sensorSample() returned error: %v", err)
	}
	if sample.Value != -1.15 {
		t.Errorf("Value = %v, want -1.15", sample.Value)
	}
}

// TestEntitySensorCacheKeepsDevicesApart checks that one device's sensors are
// never read with another's metadata. entPhysicalIndex 3001 means different
// things on different chassis, so a shared index would silently mis-scale.
func TestEntitySensorCacheKeepsDevicesApart(t *testing.T) {
	cache := newTestSensorCache()

	// Each device is polled and read in turn, which is what the conversion
	// path does: one poll, then that poll's readings.
	cache.Update("dist-01", sensorRow(ciscoSensorEntry, "3001", SensorTypeDBm, SensorScaleUnits, 2, -115, SensorStatusOK))
	nexus, err := cache.sensorSample("dist-01", ciscoKey("3001"),
		integerResult(column(ciscoSensorEntry, sensorColumnValue, "3001"), -115), nil)
	if err != nil {
		t.Fatalf("sensorSample(dist-01) returned error: %v", err)
	}

	cache.Update("access-01", sensorRow(stdSensorEntry, "3001", SensorTypeCelsius, SensorScaleUnits, 1, 425, SensorStatusOK))
	if nexus.Name != "snmp_entity_sensor_dbm" || nexus.Value != -1.15 {
		t.Errorf("dist-01 sample = %s %v, want snmp_entity_sensor_dbm -1.15", nexus.Name, nexus.Value)
	}

	catalyst, err := cache.sensorSample("access-01", stdKey("3001"),
		integerResult(column(stdSensorEntry, sensorColumnValue, "3001"), 425), nil)
	if err != nil {
		t.Fatalf("sensorSample(access-01) returned error: %v", err)
	}
	if catalyst.Name != "snmp_entity_sensor_celsius" || catalyst.Value != 42.5 {
		t.Errorf("access-01 sample = %s %v, want snmp_entity_sensor_celsius 42.5", catalyst.Name, catalyst.Value)
	}
}

// TestEntitySensorCacheIgnoresIrrelevantResults checks that a poll carrying
// nothing from the three tables publishes nothing, and that the OIDs which
// merely resemble a sensor row are not mistaken for one.
func TestEntitySensorCacheIgnoresIrrelevantResults(t *testing.T) {
	cache := newTestSensorCache()
	cache.Update("core-01", []*snmp.Result{
		integerResult(".1.3.6.1.2.1.2.2.1.10.1", 12345),
		stringResult(".1.3.6.1.2.1.1.5.0", "core-01"),
		// A sensor entry OID with no column or index after it.
		integerResult(stdSensorEntry, 1),
		// A value column with a two-part index: not a row of a table
		// indexed by one integer.
		integerResult(column(stdSensorEntry, sensorColumnValue, "1.2"), 500),
		// A column the decoder has no use for.
		integerResult(column(stdSensorEntry, "8", "1"), 30),
		nil,
	})

	if !cache.poll.empty() {
		t.Errorf("observed %+v for a poll with no sensors, want nothing", cache.poll)
	}
}

// TestEntitySensorCacheIgnoresUnansweredVarbinds checks that a NoSuchObject
// response does not teach the cache anything. An agent that does not implement
// the sensor tables answers every column that way.
func TestEntitySensorCacheIgnoresUnansweredVarbinds(t *testing.T) {
	cache := newTestSensorCache()
	cache.Update("bastion", []*snmp.Result{
		{OID: column(stdSensorEntry, sensorColumnType, "1"), Type: gosnmp.NoSuchObject, Timestamp: sensorTestTime},
		{OID: column(stdSensorEntry, sensorColumnScale, "1"), Type: gosnmp.NoSuchInstance, Timestamp: sensorTestTime},
		{OID: column(physicalEntry, physicalColumnName, "1"), Type: gosnmp.EndOfMibView, Timestamp: sensorTestTime},
	})

	if !cache.poll.empty() {
		t.Errorf("observed %+v from unanswered varbinds, want nothing", cache.poll)
	}
}

// TestEntitySensorCacheIgnoresWrongColumnTypes checks that a column answered
// with the wrong ASN.1 type is skipped rather than coerced. A scale delivered
// as a string is a broken agent, and inventing a scale for it would mis-scale
// every reading of that sensor.
func TestEntitySensorCacheIgnoresWrongColumnTypes(t *testing.T) {
	cache := newTestSensorCache()
	cache.Update("broken", []*snmp.Result{
		stringResult(column(stdSensorEntry, sensorColumnScale, "1"), "units"),
		integerResult(column(physicalEntry, physicalColumnName, "1"), 42),
		integerResult(column(stdSensorEntry, sensorColumnType, "1"), int(SensorTypeDBm)),
	})

	metadata, known := sensorMetadataFor(cache.poll, stdKey("1"))
	if !known {
		t.Fatal("sensor 1 not learned at all")
	}
	if metadata.haveScale {
		t.Errorf("scale learned from a string column: %v", metadata.scale)
	}
	if name := cache.poll.entityName("1"); name != "" {
		t.Errorf("entity name learned from an integer column: %q", name)
	}
}

// TestEntitySensorCacheScalesWithoutAnyCacheAtAll is the same property stated
// where it matters: a conversion wired up with no index cache still publishes
// correctly scaled readings, because the metadata it needs came in with them.
//
// Before metadata was made per-poll this dropped every sensor reading, which
// was defensible only because the cache was the only place metadata lived.
func TestEntitySensorCacheScalesWithoutAnyCacheAtAll(t *testing.T) {
	results := sensorRow(ciscoSensorEntry, "3001", SensorTypeDBm, SensorScaleUnits, 2, -115, SensorStatusOK)

	samples := ConvertSNMPResultsWithTableIndexes("no-cache-device", results, nil, nil, nil)

	var dbm *Sample
	for _, sample := range samples {
		if sample.Name == "snmp_entity_sensor_dbm" {
			dbm = sample
		}
	}
	if dbm == nil {
		t.Fatal("no dBm sample produced without an index cache")
	}
	if dbm.Value != -1.15 {
		t.Errorf("Value = %v, want -1.15", dbm.Value)
	}
}

// TestSensorSampleLabels pins every label a scaled reading carries.
func TestSensorSampleLabels(t *testing.T) {
	valueOID := column(ciscoSensorEntry, sensorColumnValue, "3001")

	cache := newTestSensorCache()
	cache.Update("dist-01", append(
		sensorRow(ciscoSensorEntry, "3001", SensorTypeDBm, SensorScaleUnits, 2, -115, SensorStatusOK),
		stringResult(column(physicalEntry, physicalColumnName, "3001"), "Ethernet1/3 Transceiver Transmit Power Sensor")))

	sample, err := cache.sensorSample("dist-01", ciscoKey("3001"), integerResult(valueOID, -115),
		map[string]string{"site": "core", "role": "spine"})
	if err != nil {
		t.Fatalf("sensorSample() returned error: %v", err)
	}

	want := map[string]string{
		labelDevice:      "dist-01",
		labelOID:         valueOID,
		labelEntityIndex: "3001",
		labelEntityName:  "Ethernet1/3 Transceiver Transmit Power Sensor",
		labelSensorType:  "dBm",
		"site":           "core",
		"role":           "spine",
	}
	for name, value := range want {
		if sample.Labels[name] != value {
			t.Errorf("label %s = %q, want %q", name, sample.Labels[name], value)
		}
	}
	if len(sample.Labels) != len(want) {
		t.Errorf("labels = %v, want exactly %v", sample.Labels, want)
	}
	if !sample.Timestamp.Equal(sensorTestTime) {
		t.Errorf("Timestamp = %v, want %v", sample.Timestamp, sensorTestTime)
	}
}

// TestSensorSampleOmitsUnknownEntityName checks that a device polled without
// ENTITY-MIB still reports its readings, without an empty name label. An empty
// label and an absent one are the same series to Prometheus, so writing one
// would only be misleading.
func TestSensorSampleOmitsUnknownEntityName(t *testing.T) {
	cache := newTestSensorCache()
	cache.Update("dist-01", sensorRow(ciscoSensorEntry, "3001", SensorTypeDBm, SensorScaleUnits, 2, -115, SensorStatusOK))

	sample, err := cache.sensorSample("dist-01", ciscoKey("3001"),
		integerResult(column(ciscoSensorEntry, sensorColumnValue, "3001"), -115), nil)
	if err != nil {
		t.Fatalf("sensorSample() returned error: %v", err)
	}
	if _, present := sample.Labels[labelEntityName]; present {
		t.Errorf("entity name label present with no ENTITY-MIB data: %q", sample.Labels[labelEntityName])
	}
	if sample.Labels[labelEntityIndex] != "3001" {
		t.Errorf("entity index label = %q, want 3001", sample.Labels[labelEntityIndex])
	}
}

// TestSensorSampleTagsCannotOverwriteStructuralLabels checks that a device tag
// named after a structural label is dropped, not merged. A tag named
// entphysicalname would file one transceiver's readings under another's.
func TestSensorSampleTagsCannotOverwriteStructuralLabels(t *testing.T) {
	cache := newTestSensorCache()
	cache.Update("dist-01", append(
		sensorRow(ciscoSensorEntry, "3001", SensorTypeDBm, SensorScaleUnits, 2, -115, SensorStatusOK),
		stringResult(column(physicalEntry, physicalColumnName, "3001"), "Ethernet1/3 Tx Power")))

	hostile := map[string]string{
		labelDevice:      "someone-else",
		labelOID:         ".1.2.3",
		labelEntityIndex: "9999",
		labelEntityName:  "Ethernet1/4 Tx Power",
		labelSensorType:  "celsius",
	}

	sample, err := cache.sensorSample("dist-01", ciscoKey("3001"),
		integerResult(column(ciscoSensorEntry, sensorColumnValue, "3001"), -115), hostile)
	if err != nil {
		t.Fatalf("sensorSample() returned error: %v", err)
	}

	if sample.Labels[labelDevice] != "dist-01" {
		t.Errorf("device label = %q, want dist-01", sample.Labels[labelDevice])
	}
	if sample.Labels[labelEntityIndex] != "3001" {
		t.Errorf("entity index label = %q, want 3001", sample.Labels[labelEntityIndex])
	}
	if sample.Labels[labelEntityName] != "Ethernet1/3 Tx Power" {
		t.Errorf("entity name label = %q, want %q", sample.Labels[labelEntityName], "Ethernet1/3 Tx Power")
	}
	if sample.Labels[labelSensorType] != "dBm" {
		t.Errorf("sensor type label = %q, want dBm", sample.Labels[labelSensorType])
	}
}

// TestSensorSampleRefusesUninterpretableReadings covers every way a reading
// reaches the sample path with nothing to interpret it, and pins the reason
// each is counted under. None of them may produce a sample.
func TestSensorSampleRefusesUninterpretableReadings(t *testing.T) {
	valueOID := column(stdSensorEntry, sensorColumnValue, "1")

	tests := []struct {
		name       string
		metadata   []*snmp.Result
		reading    *snmp.Result
		wantReason string
	}{
		{
			name:       "sensor never seen",
			metadata:   nil,
			reading:    integerResult(valueOID, -115),
			wantReason: "missing_metadata",
		},
		{
			name: "no type",
			metadata: []*snmp.Result{
				integerResult(column(stdSensorEntry, sensorColumnScale, "1"), int(SensorScaleUnits)),
				integerResult(column(stdSensorEntry, sensorColumnPrecision, "1"), 2),
			},
			reading:    integerResult(valueOID, -115),
			wantReason: "missing_type",
		},
		{
			name: "no scale",
			metadata: []*snmp.Result{
				integerResult(column(stdSensorEntry, sensorColumnType, "1"), int(SensorTypeDBm)),
				integerResult(column(stdSensorEntry, sensorColumnPrecision, "1"), 2),
			},
			reading:    integerResult(valueOID, -115),
			wantReason: "missing_scale",
		},
		{
			name: "no precision",
			metadata: []*snmp.Result{
				integerResult(column(stdSensorEntry, sensorColumnType, "1"), int(SensorTypeDBm)),
				integerResult(column(stdSensorEntry, sensorColumnScale, "1"), int(SensorScaleUnits)),
			},
			reading:    integerResult(valueOID, -115),
			wantReason: "missing_precision",
		},
		{
			name:       "sensor unavailable",
			metadata:   sensorRow(stdSensorEntry, "1", SensorTypeDBm, SensorScaleUnits, 2, -115, SensorStatusUnavailable),
			reading:    integerResult(valueOID, -115),
			wantReason: "status_unavailable",
		},
		{
			name:       "sensor nonoperational",
			metadata:   sensorRow(stdSensorEntry, "1", SensorTypeDBm, SensorScaleUnits, 2, -115, SensorStatusNonoperational),
			reading:    integerResult(valueOID, -115),
			wantReason: "status_nonoperational",
		},
		{
			name:       "reading is not an integer",
			metadata:   sensorRow(stdSensorEntry, "1", SensorTypeDBm, SensorScaleUnits, 2, -115, SensorStatusOK),
			reading:    stringResult(valueOID, "-1.15 dBm"),
			wantReason: "missing_reading",
		},
		{
			name:       "reading overflows",
			metadata:   sensorRow(stdSensorEntry, "1", SensorTypeDBm, SensorScaleUnits, 2, -115, SensorStatusOK),
			reading:    integerResult(valueOID, int(SensorValueOverflow)),
			wantReason: "overflow",
		},
		{
			name:       "reading underflows",
			metadata:   sensorRow(stdSensorEntry, "1", SensorTypeDBm, SensorScaleUnits, 2, -115, SensorStatusOK),
			reading:    integerResult(valueOID, int(SensorValueUnderflow)),
			wantReason: "underflow",
		},
		{
			name:       "scale outside the enumeration",
			metadata:   sensorRow(stdSensorEntry, "1", SensorTypeDBm, 99, 2, -115, SensorStatusOK),
			reading:    integerResult(valueOID, -115),
			wantReason: "invalid_scale",
		},
		{
			name:       "precision outside the enumeration",
			metadata:   sensorRow(stdSensorEntry, "1", SensorTypeDBm, SensorScaleUnits, 42, -115, SensorStatusOK),
			reading:    integerResult(valueOID, -115),
			wantReason: "invalid_precision",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := newTestSensorCache()
			if tt.metadata != nil {
				cache.Update("dist-01", tt.metadata)
			}

			sample, err := cache.sensorSample("dist-01", stdKey("1"), tt.reading, nil)
			if err == nil {
				t.Fatalf("sensorSample() = %+v, want error", sample)
			}
			if sample != nil {
				t.Errorf("sensorSample() = %+v on error, want nil", sample)
			}

			var drop sensorDropError
			if !errors.As(err, &drop) {
				t.Fatalf("error = %T (%v), does not report a drop reason", err, err)
			}
			if drop.DropReason() != tt.wantReason {
				t.Errorf("DropReason() = %q, want %q", drop.DropReason(), tt.wantReason)
			}
		})
	}
}

// TestSensorSampleRequiresTheStatusColumn checks that a sensor whose status
// the device never reported is not published.
//
// This inverts an earlier decision. The reading was published on the reasoning
// that RFC 3433 makes the column mandatory but refusing every reading for a
// missing status would drop a whole table over one absent column. Measured
// against the fleet, no device omits it: dist-01 and dist-02 return a
// status row for all 104 of their sensors and access-01 for all 13. So the
// leniency defended nobody and left a fail-open in the one path whose premise
// is that an unvalidated number is worse than no number.
func TestSensorSampleRequiresTheStatusColumn(t *testing.T) {
	cache := newTestSensorCache()
	cache.Update("dist-01", []*snmp.Result{
		integerResult(column(stdSensorEntry, sensorColumnType, "1"), int(SensorTypeDBm)),
		integerResult(column(stdSensorEntry, sensorColumnScale, "1"), int(SensorScaleUnits)),
		integerResult(column(stdSensorEntry, sensorColumnPrecision, "1"), 2),
	})

	sample, err := cache.sensorSample("dist-01", stdKey("1"),
		integerResult(column(stdSensorEntry, sensorColumnValue, "1"), -115), nil)

	if err == nil {
		t.Fatalf("published %v dBm for a sensor with no status column", sample.Value)
	}
	if sample != nil {
		t.Errorf("sensorSample() = %+v on error, want nil", sample)
	}

	var metadataErr *SensorMetadataError
	if !errors.As(err, &metadataErr) {
		t.Fatalf("error = %T (%v), want *SensorMetadataError", err, err)
	}
	if metadataErr.Missing != "status" {
		t.Errorf("Missing = %q, want %q", metadataErr.Missing, "status")
	}

	// It is counted the same way every other unreadable sensor is, so the
	// gap is visible rather than silent.
	var drop sensorDropError
	if !errors.As(err, &drop) {
		t.Fatalf("error = %T, does not report a drop reason", err)
	}
	if drop.DropReason() != "missing_status" {
		t.Errorf("DropReason() = %q, want %q", drop.DropReason(), "missing_status")
	}
}

// TestSensorSampleReadsAStatusOfOK checks the other side: a sensor that does
// report ok(1) is published normally, so requiring the column did not close
// the path for conforming agents.
func TestSensorSampleReadsAStatusOfOK(t *testing.T) {
	cache := newTestSensorCache()
	cache.Update("dist-01", sensorRow(stdSensorEntry, "1", SensorTypeDBm, SensorScaleUnits, 2, -115, SensorStatusOK))

	sample, err := cache.sensorSample("dist-01", stdKey("1"),
		integerResult(column(stdSensorEntry, sensorColumnValue, "1"), -115), nil)
	if err != nil {
		t.Fatalf("sensorSample() returned error: %v", err)
	}
	if sample.Value != -1.15 {
		t.Errorf("Value = %v, want -1.15", sample.Value)
	}
}

// TestTableColumnAndIndex checks the OID split for both sensor tables, the
// entity table, and every shape that is not a row of a table indexed by one
// integer.
func TestTableColumnAndIndex(t *testing.T) {
	tests := []struct {
		name       string
		oid        string
		entryOID   string
		wantColumn string
		wantIndex  string
		wantOK     bool
	}{
		{"leading dot", ".1.3.6.1.2.1.99.1.1.1.4.3001", entPhySensorEntryOID, "4", "3001", true},
		{"no leading dot", "1.3.6.1.2.1.99.1.1.1.4.3001", entPhySensorEntryOID, "4", "3001", true},
		{"cisco table", ".1.3.6.1.4.1.9.9.91.1.1.1.1.4.7", entSensorValueEntryOID, "4", "7", true},
		{"entity table", ".1.3.6.1.2.1.47.1.1.1.1.7.3001", entPhysicalEntryOID, "7", "3001", true},
		{"entry OID itself", ".1.3.6.1.2.1.99.1.1.1", entPhySensorEntryOID, "", "", false},
		{"column with no index", ".1.3.6.1.2.1.99.1.1.1.4", entPhySensorEntryOID, "", "", false},
		{"two-part index", ".1.3.6.1.2.1.99.1.1.1.4.1.2", entPhySensorEntryOID, "", "", false},
		{"different table", ".1.3.6.1.2.1.2.2.1.10.1", entPhySensorEntryOID, "", "", false},
		{"prefix of the entry OID", ".1.3.6.1.2.1.99.1.1", entPhySensorEntryOID, "", "", false},
		{"entry OID as a substring, not a prefix", ".1.3.6.1.2.1.99.1.1.11.4.1", entPhySensorEntryOID, "", "", false},
		{"empty", "", entPhySensorEntryOID, "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			column, index, ok := tableColumnAndIndex(tt.oid, tt.entryOID)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if column != tt.wantColumn || index != tt.wantIndex {
				t.Errorf("(column, index) = (%q, %q), want (%q, %q)", column, index, tt.wantColumn, tt.wantIndex)
			}
		})
	}
}

// TestEntitySensorValueKey checks that exactly the value column of the two
// sensor tables is recognized, no other column of them is, and that the key it
// returns names the module the reading actually came from.
//
// That last part is the whole point: the module in this key is what decides
// which metadata the reading resolves against, so returning the right index
// under the wrong module would reintroduce the cross-module scaling bug.
func TestEntitySensorValueKey(t *testing.T) {
	tests := []struct {
		name    string
		oid     string
		wantKey sensorKey
		wantOK  bool
	}{
		{"standard value column", column(stdSensorEntry, sensorColumnValue, "3001"), stdKey("3001"), true},
		{"cisco value column", column(ciscoSensorEntry, sensorColumnValue, "7"), ciscoKey("7"), true},
		{"same index, each module", column(stdSensorEntry, sensorColumnValue, "1"), stdKey("1"), true},
		{"same index, cisco", column(ciscoSensorEntry, sensorColumnValue, "1"), ciscoKey("1"), true},
		{"standard type column", column(stdSensorEntry, sensorColumnType, "3001"), sensorKey{}, false},
		{"standard scale column", column(stdSensorEntry, sensorColumnScale, "3001"), sensorKey{}, false},
		{"standard precision column", column(stdSensorEntry, sensorColumnPrecision, "3001"), sensorKey{}, false},
		{"standard status column", column(stdSensorEntry, sensorColumnStatus, "3001"), sensorKey{}, false},
		{"cisco threshold table", ".1.3.6.1.4.1.9.9.91.1.2.1.1.4.7.1", sensorKey{}, false},
		{"an interface counter", ".1.3.6.1.2.1.2.2.1.10.1", sensorKey{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, ok := entitySensorValueKey(tt.oid)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if key != tt.wantKey {
				t.Errorf("key = %+v, want %+v", key, tt.wantKey)
			}
		})
	}
}

// TestEntitySensorValueKeySeparatesTheModules pins that the same
// entPhysicalIndex read from the two tables produces two different keys. Both
// tables are INDEX { entPhysicalIndex }, so this is the only thing keeping a
// dual-implementing agent's two descriptions of one sensor apart.
func TestEntitySensorValueKeySeparatesTheModules(t *testing.T) {
	standard, ok := entitySensorValueKey(column(stdSensorEntry, sensorColumnValue, "1"))
	if !ok {
		t.Fatal("standard value column not recognized")
	}
	cisco, ok := entitySensorValueKey(column(ciscoSensorEntry, sensorColumnValue, "1"))
	if !ok {
		t.Fatal("cisco value column not recognized")
	}

	if standard == cisco {
		t.Fatalf("both modules produced key %+v for entPhysicalIndex 1", standard)
	}
	if standard.index != cisco.index {
		t.Errorf("indexes differ (%q vs %q); only the module should", standard.index, cisco.index)
	}
}

// TestCountSensorDropUnknownReason checks that an error carrying no drop
// reason is still counted, under a reason that says so. A dropped reading that
// increments nothing is the failure this counter exists to prevent.
func TestCountSensorDropUnknownReason(t *testing.T) {
	before := droppedCount("count-drop-device", "unknown")
	countSensorDrop("count-drop-device", errors.New("something else went wrong"))
	if after := droppedCount("count-drop-device", "unknown"); after != before+1 {
		t.Errorf("unknown drop count = %v, want %v", after, before+1)
	}
}

// droppedCount reads gonetmon_entity_sensor_dropped_total for one device and
// reason.
func droppedCount(deviceName, reason string) float64 {
	return testutil.ToFloat64(entitySensorDropped.WithLabelValues(deviceName, reason))
}

// BenchmarkEntitySensorCacheUpdate measures learning one poll's worth of
// sensor metadata: 48 transceivers, two sensors each, with entity names.
func BenchmarkEntitySensorCacheUpdate(b *testing.B) {
	var results []*snmp.Result
	for i := 0; i < 96; i++ {
		index := strconv.Itoa(3000 + i)
		results = append(results, sensorRow(ciscoSensorEntry, index, SensorTypeDBm, SensorScaleUnits, 2, -115, SensorStatusOK)...)
		results = append(results, stringResult(column(physicalEntry, physicalColumnName, index), "Ethernet1/"+index+" Transceiver Rx Power"))
	}

	cache := newTestSensorCache()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache.Update("bench-device", results)
	}
}

// BenchmarkSensorSample measures building one scaled sample, which happens
// once per sensor per poll.
func BenchmarkSensorSample(b *testing.B) {
	cache := newTestSensorCache()
	cache.Update("bench-device", sensorRow(ciscoSensorEntry, "3001", SensorTypeDBm, SensorScaleUnits, 2, -115, SensorStatusOK))
	reading := integerResult(column(ciscoSensorEntry, sensorColumnValue, "3001"), -115)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := cache.sensorSample("bench-device", ciscoKey("3001"), reading, nil); err != nil {
			b.Fatalf("sensorSample() returned error: %v", err)
		}
	}
}

// TestSensorErrorMessages checks what each refusal says.
//
// These strings are how an operator finds out why a transceiver has no graph,
// so the device, the sensor and the cause all have to be in them. The range
// error's wording is the one with logic behind it: the same type reports both
// ends of the range and has to name the right one.
func TestSensorErrorMessages(t *testing.T) {
	tests := []struct {
		name string
		err  sensorDropError
		want string
	}{
		{
			name: "scale",
			err:  &SensorScaleError{Scale: 42},
			want: "metrics: entity sensor scale 42 is outside EntitySensorDataScale (1..17)",
		},
		{
			name: "precision",
			err:  &SensorPrecisionError{Precision: -9},
			want: "metrics: entity sensor precision -9 is outside EntitySensorPrecision (-8..9)",
		},
		{
			name: "overflow",
			err:  &SensorRangeError{Value: SensorValueOverflow},
			want: "metrics: entity sensor value 1000000000 signals overflow",
		},
		{
			name: "underflow",
			err:  &SensorRangeError{Value: SensorValueUnderflow},
			want: "metrics: entity sensor value -1000000000 signals underflow",
		},
		{
			name: "metadata",
			err:  &SensorMetadataError{Device: "dist-01", Index: "3001", Missing: "scale"},
			want: "metrics: no scale for sensor 3001 on dist-01: reading cannot be scaled",
		},
		{
			name: "status",
			err:  &SensorStatusError{Device: "access-01", Index: "7", Status: SensorStatusNonoperational},
			want: "metrics: sensor 7 on access-01 reports status nonoperational, not ok",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSensorRangeErrorReasonMatchesMessage checks that the counter reason and
// the message agree about which end of the range was hit, for values on both
// sides and beyond the sentinels.
func TestSensorRangeErrorReasonMatchesMessage(t *testing.T) {
	tests := []struct {
		name  string
		value int64
		want  string
	}{
		{"overflow sentinel", SensorValueOverflow, "overflow"},
		{"beyond overflow", 2000000000, "overflow"},
		{"underflow sentinel", SensorValueUnderflow, "underflow"},
		{"beyond underflow", -2000000000, "underflow"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &SensorRangeError{Value: tt.value}
			if got := err.DropReason(); got != tt.want {
				t.Errorf("DropReason() = %q, want %q", got, tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Error() = %q, does not mention %q", err.Error(), tt.want)
			}
		})
	}
}

// TestDeviceSensorsEntityNameNilReceiver checks the degenerate lookup: a
// device with no learned entity data at all names nothing rather than
// panicking on the sample path.
func TestDeviceSensorsEntityNameNilReceiver(t *testing.T) {
	var poll *pollSensors
	if got := poll.entityName("3001"); got != "" {
		t.Errorf("entityName() = %q, want empty", got)
	}
	if !poll.empty() {
		t.Error("empty() = false for a nil pollSensors")
	}
}

// TestEntitySensorCacheRejectsOversizedColumnValues checks the narrowing
// guard on the four Integer32 metadata columns.
//
// Truncation wraps, so this is a case where accepting the value is more
// dangerous than rejecting it: 1<<32 + 9 narrows to units(9), which passes
// every check downstream and scales every reading of that sensor as if the
// agent had said something it did not. A value that does not fit Integer32
// must teach the cache nothing.
func TestEntitySensorCacheRejectsOversizedColumnValues(t *testing.T) {
	const wrapsToUnits = int64(1)<<32 + 9

	tests := []struct {
		name   string
		column string
		value  int64
	}{
		{"type wraps to a valid enum", sensorColumnType, int64(1)<<32 + 14},
		{"scale wraps to units", sensorColumnScale, wrapsToUnits},
		{"precision wraps to 2", sensorColumnPrecision, int64(1)<<32 + 2},
		{"status wraps to ok", sensorColumnStatus, int64(1)<<32 + 1},
		{"negative beyond Integer32", sensorColumnScale, math.MinInt32 - 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := newTestSensorCache()
			cache.Update("broken", []*snmp.Result{
				{
					OID:       column(stdSensorEntry, tt.column, "1"),
					Type:      gosnmp.Counter64,
					Value:     tt.value,
					Timestamp: sensorTestTime,
				},
			})

			if _, known := sensorMetadataFor(cache.poll, stdKey("1")); known {
				t.Error("learned metadata from a value outside Integer32")
			}
		})
	}
}

// TestInt32Value covers the narrowing guard directly, at both boundaries.
func TestInt32Value(t *testing.T) {
	tests := []struct {
		name     string
		number   int64
		want     int32
		wantFits bool
	}{
		{"zero", 0, 0, true},
		{"scale enum", 9, 9, true},
		{"negative precision", -8, -8, true},
		{"maximum Integer32", math.MaxInt32, math.MaxInt32, true},
		{"minimum Integer32", math.MinInt32, math.MinInt32, true},
		{"one past maximum", math.MaxInt32 + 1, 0, false},
		{"one past minimum", math.MinInt32 - 1, 0, false},
		{"wraps to units", int64(1)<<32 + 9, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, fits := int32Value(tt.number)
			if fits != tt.wantFits {
				t.Fatalf("fits = %v, want %v", fits, tt.wantFits)
			}
			if got != tt.want {
				t.Errorf("int32Value(%d) = %d, want %d", tt.number, got, tt.want)
			}
		})
	}
}

// documentedDropReasons is every value the reason label of
// gonetmon_entity_sensor_dropped_total is allowed to take, as
// docs/metrics/entity-sensors.md publishes it.
//
// The set has to be closed and this has to be the list, because the label is
// derived from data a device chose. A reason minted per response would grow
// one Prometheus time series per distinct value, on the counter whose whole
// job is to report devices behaving badly -- the metric would become the
// attack surface it exists to describe.
var documentedDropReasons = map[string]bool{
	"missing_metadata":      true,
	"missing_type":          true,
	"missing_scale":         true,
	"missing_precision":     true,
	"missing_status":        true,
	"missing_reading":       true,
	"invalid_scale":         true,
	"invalid_precision":     true,
	"overflow":              true,
	"underflow":             true,
	"status_unavailable":    true,
	"status_nonoperational": true,
	"status_unknown":        true,
	"unknown":               true,
}

// TestSensorStatusErrorReasonIsClosed checks that no status a device can put
// in an Integer32 produces a reason outside the documented set.
//
// It sweeps the boundaries rather than a handful of examples, because the
// property being checked is "no input reaches the label", and an example-based
// test only shows that the examples do not.
func TestSensorStatusErrorReasonIsClosed(t *testing.T) {
	statuses := []SensorStatus{
		SensorStatusOK, SensorStatusUnavailable, SensorStatusNonoperational,
		0, -1, 4, 9, 42, 255, 65536,
		math.MinInt32, math.MaxInt32,
	}
	for i := SensorStatus(-20); i <= 20; i++ {
		statuses = append(statuses, i)
	}

	for _, status := range statuses {
		t.Run(status.String(), func(t *testing.T) {
			err := &SensorStatusError{Device: "d", Index: "1", Status: status}
			reason := err.DropReason()

			if !documentedDropReasons[reason] {
				t.Errorf("status %d produces undocumented reason %q", status, reason)
			}
			if strings.Contains(reason, "status_status") {
				t.Errorf("status %d produces doubled prefix %q", status, reason)
			}
			// Whatever the label collapses to, the number itself must
			// still be recoverable by whoever is diagnosing the sensor.
			if !strings.Contains(err.Error(), status.String()) {
				t.Errorf("Error() = %q, does not carry the status %d", err.Error(), status)
			}
		})
	}
}

// TestEntitySensorDropReasonsStayBounded is the same property checked where it
// actually matters: on the Prometheus counter, through the real conversion
// path, for statuses a device made up.
//
// The invariant is not "few labels" but "the labels do not grow with the
// input", so it is checked by sweeping two devices with wildly different
// numbers of distinct statuses and requiring the same reason set from both. A
// fixed upper bound would pass just as well on an implementation that leaked
// one label per status for the first few statuses.
func TestEntitySensorDropReasonsStayBounded(t *testing.T) {
	few := sweepSensorStatuses(t, "bounded-reason-few", 6)
	many := sweepSensorStatuses(t, "bounded-reason-many", 400)

	if len(few) == 0 {
		t.Fatal("no drops recorded; the test did not exercise the counter")
	}
	for reason := range many {
		if !documentedDropReasons[reason] {
			t.Errorf("counter carries undocumented reason %q", reason)
		}
	}
	if !reflect.DeepEqual(few, many) {
		t.Errorf("6 distinct statuses produced %v but 400 produced %v; "+
			"the reason label grows with device input", few, many)
	}
}

// sweepSensorStatuses runs one sensor per status value through the real
// conversion path and returns the reason labels that reached the counter.
//
// A reading is expected only for ok(1); every other status is a sensor saying
// it has nothing to report, and a sample from one would be a stale number
// published as a measurement.
func sweepSensorStatuses(t *testing.T, device string, statuses int) map[string]bool {
	t.Helper()

	cache := NewTableIndexCache(nil)
	for status := 0; status < statuses; status++ {
		index := strconv.Itoa(status)
		results := sensorRow(ciscoSensorEntry, index, SensorTypeDBm, SensorScaleUnits, 2, -115, SensorStatus(status))

		for _, sample := range ConvertSNMPResultsWithTableIndexes(device, results, nil, cache, nil) {
			if sample.Labels[labelSensorType] == "" {
				continue // a metadata column, not a reading
			}
			if SensorStatus(status) != SensorStatusOK {
				t.Errorf("status %d produced a reading: %s = %v", status, sample.Name, sample.Value)
			}
		}
	}

	return observedDropReasons(t, device)
}

// observedDropReasons returns the reason label values
// gonetmon_entity_sensor_dropped_total carries for one device.
func observedDropReasons(t *testing.T, device string) map[string]bool {
	t.Helper()

	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}

	reasons := map[string]bool{}
	for _, family := range families {
		if family.GetName() != "gonetmon_entity_sensor_dropped_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			var forDevice bool
			var reason string
			for _, label := range metric.GetLabel() {
				switch label.GetName() {
				case "device":
					forDevice = label.GetValue() == device
				case "reason":
					reason = label.GetValue()
				}
			}
			if forDevice {
				reasons[reason] = true
			}
		}
	}
	return reasons
}

// TestEntitySensorCacheKeysMetadataByModule checks that a walk files its
// metadata under the module that produced it, and under no other.
func TestEntitySensorCacheKeysMetadataByModule(t *testing.T) {
	tests := []struct {
		name     string
		entryOID string
		want     sensorKey
		notWant  sensorKey
	}{
		{"ENTITY-SENSOR-MIB", stdSensorEntry, stdKey("1"), ciscoKey("1")},
		{"CISCO-ENTITY-SENSOR-MIB", ciscoSensorEntry, ciscoKey("1"), stdKey("1")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := newTestSensorCache()
			cache.Update("sw", sensorRow(tt.entryOID, "1", SensorTypeDBm, SensorScale14, 0, 5, SensorStatusOK))

			if _, known := sensorMetadataFor(cache.poll, tt.want); !known {
				t.Errorf("no metadata under %s", tt.want.table)
			}
			if _, known := sensorMetadataFor(cache.poll, tt.notWant); known {
				t.Errorf("metadata also written under %s", tt.notWant.table)
			}
		})
	}
}

// TestEntitySensorScaleIsReadWithItsOwnModule is why the module is half the
// key: scale 14 is 10^15 in ENTITY-SENSOR-MIB and 10^18 in
// CISCO-ENTITY-SENSOR-MIB, so the same varbind read through the wrong module
// is a thousand-fold out.
func TestEntitySensorScaleIsReadWithItsOwnModule(t *testing.T) {
	tests := []struct {
		name     string
		entryOID string
		key      sensorKey
		want     float64
	}{
		{"standard module scales 14 as 10^15", stdSensorEntry, stdKey("1"), 5e15},
		{"cisco module scales 14 as 10^18", ciscoSensorEntry, ciscoKey("1"), 5e18},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := newTestSensorCache()
			cache.Update("sw", sensorRow(tt.entryOID, "1", SensorTypeWatts, SensorScale14, 0, 5, SensorStatusOK))

			sample, err := cache.sensorSample("sw", tt.key,
				integerResult(column(tt.entryOID, sensorColumnValue, "1"), 5), nil)
			if err != nil {
				t.Fatalf("sensorSample() returned error: %v", err)
			}
			if sample.Value != tt.want {
				t.Errorf("Value = %v, want %v", sample.Value, tt.want)
			}
		})
	}
}

// TestEntitySensorValueDoesNotBorrowTheOtherModulesMetadata is the case the
// previous version of this file could not catch, because every reading was
// supplied from the same table as its metadata.
//
// One module describes entPhysicalIndex 1; a value for index 1 then arrives
// from the *other* module. Keyed by index alone, the reading would be scaled
// with metadata it has nothing to do with -- 5 at Cisco's scale 14 is 5e18,
// while the standard module's scale 14 is 5e15, and poll ordering would decide
// which one a reading got. It must not resolve at all.
func TestEntitySensorValueDoesNotBorrowTheOtherModulesMetadata(t *testing.T) {
	tests := []struct {
		name          string
		metadataEntry string
		valueEntry    string
		valueKey      sensorKey
	}{
		{"cisco metadata, standard reading", ciscoSensorEntry, stdSensorEntry, stdKey("1")},
		{"standard metadata, cisco reading", stdSensorEntry, ciscoSensorEntry, ciscoKey("1")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := newTestSensorCache()
			cache.Update("sw", sensorRow(tt.metadataEntry, "1", SensorTypeWatts, SensorScale14, 0, 5, SensorStatusOK))

			sample, err := cache.sensorSample("sw", tt.valueKey,
				integerResult(column(tt.valueEntry, sensorColumnValue, "1"), 5), nil)

			if err == nil {
				t.Fatalf("scaled a %s reading with %s metadata, producing %v",
					tt.valueKey.table, tt.metadataEntry, sample.Value)
			}
			if sample != nil {
				t.Errorf("sensorSample() = %+v on error, want nil", sample)
			}

			var metadataErr *SensorMetadataError
			if !errors.As(err, &metadataErr) {
				t.Fatalf("error = %T (%v), want *SensorMetadataError", err, err)
			}
		})
	}
}

// TestEntitySensorDualImplementingAgentScalesBothModules is the payoff, and
// the reason the key is (module, index) rather than a mismatch check that
// drops one side.
//
// Both sensor tables are INDEX { entPhysicalIndex }, so an agent implementing
// both describes the same physical sensor at the same index in each, using
// each module's own enum for the quantity: 10^18 is scale 14 in Cisco and
// scale 15 in the standard module. Both readings are valid and both must
// arrive at the same physical answer.
func TestEntitySensorDualImplementingAgentScalesBothModules(t *testing.T) {
	const physicalAnswer = 5e18

	cache := newTestSensorCache()
	// The same sensor, at the same index, described by both modules in one
	// poll -- which is what a device implementing both actually returns.
	// The descriptions differ in precision as well as in scale enum, which
	// is what makes a collision between them detectable: if the two rows
	// share a map entry, one module's raw value is read with the other's
	// decimal places and no combination lands on the right answer.
	cache.Update("dual", append(
		sensorRow(ciscoSensorEntry, "1", SensorTypeWatts, SensorScale14, 0, 5, SensorStatusOK),
		sensorRow(stdSensorEntry, "1", SensorTypeWatts, SensorScale15, 3, 5000, SensorStatusOK)...))

	tests := []struct {
		name     string
		entryOID string
		key      sensorKey
		raw      int
	}{
		// 5 at Cisco's scale 14 (10^18), no fractional digits.
		{"cisco reading", ciscoSensorEntry, ciscoKey("1"), 5},
		// 5000 at the standard module's scale 15 (10^18), three
		// fractional digits: the same physical quantity, stated the way
		// that module states it.
		{"standard reading", stdSensorEntry, stdKey("1"), 5000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sample, err := cache.sensorSample("dual", tt.key,
				integerResult(column(tt.entryOID, sensorColumnValue, "1"), tt.raw), nil)
			if err != nil {
				t.Fatalf("sensorSample() returned error: %v", err)
			}
			if sample.Value != physicalAnswer {
				t.Errorf("Value = %v, want %v; the two modules' descriptions of one sensor collided",
					sample.Value, physicalAnswer)
			}
		})
	}
}

// TestEntitySensorModulesDoNotCollideWithinOnePoll checks that one module's
// description of an index cannot disturb the other's when both arrive
// together, which is how a dual-implementing agent answers.
//
// This replaces a test that asserted the same separation *across* polls.
// Metadata no longer crosses polls at all, so the cross-poll form was
// asserting something that can no longer happen; the within-poll form is where
// the collision is still possible and where the key earns its keep.
func TestEntitySensorModulesDoNotCollideWithinOnePoll(t *testing.T) {
	cache := newTestSensorCache()
	cache.Update("sw", append(
		sensorRow(stdSensorEntry, "1", SensorTypeWatts, SensorScale14, 0, 5, SensorStatusOK),
		sensorRow(ciscoSensorEntry, "1", SensorTypeCelsius, SensorScale15, 1, 5, SensorStatusOK)...))

	standard, known := sensorMetadataFor(cache.poll, stdKey("1"))
	if !known {
		t.Fatal("standard metadata missing")
	}
	if standard.scale != SensorScale14 || standard.sensorType != SensorTypeWatts || standard.precision != 0 {
		t.Errorf("standard metadata = %+v, overwritten by the Cisco row", standard)
	}

	sample, err := cache.sensorSample("sw", stdKey("1"),
		integerResult(column(stdSensorEntry, sensorColumnValue, "1"), 5), nil)
	if err != nil {
		t.Fatalf("sensorSample() returned error: %v", err)
	}
	if sample.Value != 5e15 {
		t.Errorf("Value = %v, want 5e15; the reading took the other module's scale", sample.Value)
	}
}

// TestSensorTypeLabelIsClosed checks that no EntitySensorDataType a device can
// put in an Integer32 produces a sensor_type label outside the declared set.
//
// Same hazard as the drop reason: the label lands on every exported sample and
// on gonetmon_entity_sensor_samples_total, so an agent answering a fresh type
// per poll would mint one time series per response.
func TestSensorTypeLabelIsClosed(t *testing.T) {
	declared := map[string]bool{}
	for _, name := range sensorTypeNames {
		declared[name] = true
	}
	declared[sensorTypeLabelUnrecognized] = true

	types := []SensorType{0, -1, 16, 42, 255, 65536, math.MinInt32, math.MaxInt32}
	for i := SensorType(-20); i <= 40; i++ {
		types = append(types, i)
	}

	for _, sensorType := range types {
		label := sensorType.Label()
		if !declared[label] {
			t.Errorf("type %d produces undeclared label %q", sensorType, label)
		}
		// "unknown" is a declared type of its own; an unrecognized value
		// must not be filed under it.
		if _, isDeclared := sensorTypeNames[sensorType]; !isDeclared && label != sensorTypeLabelUnrecognized {
			t.Errorf("type %d produces label %q, want %q", sensorType, label, sensorTypeLabelUnrecognized)
		}
		// The exact value stays available for diagnosis.
		if !strings.Contains(sensorType.String(), strconv.FormatInt(int64(sensorType), 10)) &&
			sensorTypeNames[sensorType] == "" {
			t.Errorf("String() = %q for type %d, does not carry the value", sensorType.String(), sensorType)
		}
	}
}

// TestEntitySensorTypeLabelsStayBounded checks the same property on the
// exported samples, through the real conversion path.
//
// Both sweeps cover every declared type, so the declared half of the label set
// is identical by construction and only the undeclared half varies: one device
// answers 4 values no MIB declares, the other answers 400. The label sets must
// still match. Varying the whole range instead would fail for a legitimate
// reason -- a wider sweep reaches more declared types -- which is a mistake
// this test made on its first draft.
func TestEntitySensorTypeLabelsStayBounded(t *testing.T) {
	few := sweepSensorTypes(t, "bounded-type-few", 4)
	many := sweepSensorTypes(t, "bounded-type-many", 400)

	if len(few) == 0 {
		t.Fatal("no samples produced; the test did not exercise the label")
	}
	if !few[sensorTypeLabelUnrecognized] {
		t.Errorf("undeclared types produced no %q label: %v", sensorTypeLabelUnrecognized, few)
	}
	if !reflect.DeepEqual(few, many) {
		t.Errorf("4 undeclared sensor types produced %v but 400 produced %v; "+
			"the sensor_type label grows with device input", few, many)
	}
}

// sweepSensorTypes runs every declared EntitySensorDataType plus undeclared
// of the undeclared ones through the real conversion path, and returns the
// sensor_type labels that reached the exported samples.
func sweepSensorTypes(t *testing.T, device string, undeclared int) map[string]bool {
	t.Helper()

	sensorTypes := make([]SensorType, 0, len(sensorTypeNames)+undeclared)
	for sensorType := range sensorTypeNames {
		sensorTypes = append(sensorTypes, sensorType)
	}
	// Undeclared values start past the highest declared one so the two
	// groups cannot overlap.
	for i := 0; i < undeclared; i++ {
		sensorTypes = append(sensorTypes, SensorType(100+i))
	}

	cache := NewTableIndexCache(nil)
	labels := map[string]bool{}

	for i, sensorType := range sensorTypes {
		index := strconv.Itoa(i)
		results := sensorRow(ciscoSensorEntry, index, sensorType, SensorScaleUnits, 0, 5, SensorStatusOK)

		for _, sample := range ConvertSNMPResultsWithTableIndexes(device, results, nil, cache, nil) {
			if label := sample.Labels[labelSensorType]; label != "" {
				labels[label] = true
			}
		}
	}

	return labels
}

// TestEntitySensorMetadataNeverOutlivesItsPoll is the invariant gate 5 asked
// for: a value is never published with metadata that could belong to different
// hardware.
//
// The dangerous case is same-type reuse, which no signal in the protocol
// distinguishes from the sensor simply still being there. An entPhysicalIndex
// described as Celsius at precision 2, reused by a different Celsius sensor at
// precision 0, would publish 42 C as 0.42 C if the second poll's missing
// precision were filled in from the first. It cannot be, because metadata from
// an earlier poll is not reachable from the sample path at all.
func TestEntitySensorMetadataNeverOutlivesItsPoll(t *testing.T) {
	cache := newTestSensorCache()

	// Poll 1 fully describes a Celsius sensor reading hundredths.
	cache.Update("sw", sensorRow(ciscoSensorEntry, "1", SensorTypeCelsius, SensorScaleUnits, 2, 4200, SensorStatusOK))

	// Poll 2: the index has been reused by another Celsius sensor, and this
	// poll does not carry the precision column.
	cache.Update("sw", []*snmp.Result{
		integerResult(column(ciscoSensorEntry, sensorColumnType, "1"), int(SensorTypeCelsius)),
		integerResult(column(ciscoSensorEntry, sensorColumnScale, "1"), int(SensorScaleUnits)),
		integerResult(column(ciscoSensorEntry, sensorColumnStatus, "1"), int(SensorStatusOK)),
	})

	sample, err := cache.sensorSample("sw", ciscoKey("1"),
		integerResult(column(ciscoSensorEntry, sensorColumnValue, "1"), 42), nil)

	if err == nil {
		t.Fatalf("published %v for a sensor whose precision this poll never reported", sample.Value)
	}

	var metadataErr *SensorMetadataError
	if !errors.As(err, &metadataErr) {
		t.Fatalf("error = %T (%v), want *SensorMetadataError", err, err)
	}
	if metadataErr.Missing != "precision" {
		t.Errorf("Missing = %q, want %q", metadataErr.Missing, "precision")
	}
}

// TestEntitySensorMetadataNeverOutlivesItsPollForAnyColumn is the same
// invariant across every column scaling depends on, so no single one is left
// reachable from a previous poll.
func TestEntitySensorMetadataNeverOutlivesItsPollForAnyColumn(t *testing.T) {
	tests := []struct {
		name        string
		omit        string
		wantMissing string
	}{
		{"type", sensorColumnType, "type"},
		{"scale", sensorColumnScale, "scale"},
		{"precision", sensorColumnPrecision, "precision"},
		{"status", sensorColumnStatus, "status"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := newTestSensorCache()
			cache.Update("sw", sensorRow(ciscoSensorEntry, "1", SensorTypeCelsius, SensorScaleUnits, 2, 4200, SensorStatusOK))

			// A second poll carrying every metadata column but one.
			var second []*snmp.Result
			for _, col := range []struct {
				id    string
				value int
			}{
				{sensorColumnType, int(SensorTypeCelsius)},
				{sensorColumnScale, int(SensorScaleUnits)},
				{sensorColumnPrecision, 0},
				{sensorColumnStatus, int(SensorStatusOK)},
			} {
				if col.id == tt.omit {
					continue
				}
				second = append(second, integerResult(column(ciscoSensorEntry, col.id, "1"), col.value))
			}
			cache.Update("sw", second)

			_, err := cache.sensorSample("sw", ciscoKey("1"),
				integerResult(column(ciscoSensorEntry, sensorColumnValue, "1"), 42), nil)

			var metadataErr *SensorMetadataError
			if !errors.As(err, &metadataErr) {
				t.Fatalf("error = %T (%v), want *SensorMetadataError; the omitted %s "+
					"column was filled in from an earlier poll", err, err, tt.name)
			}
			if metadataErr.Missing != tt.wantMissing {
				t.Errorf("Missing = %q, want %q", metadataErr.Missing, tt.wantMissing)
			}
		})
	}
}

// TestEntitySensorReadsASensorFullyDescribedByItsOwnPoll checks that the rule
// costs nothing when a poll does its job: every column present, reading
// published, with this poll's values and not a previous poll's.
func TestEntitySensorReadsASensorFullyDescribedByItsOwnPoll(t *testing.T) {
	cache := newTestSensorCache()

	// A transceiver at hundredths...
	cache.Update("sw", sensorRow(ciscoSensorEntry, "1", SensorTypeDBm, SensorScaleUnits, 2, -115, SensorStatusOK))
	// ...then the index is reused by a temperature sensor at tenths, fully
	// described by its own poll.
	cache.Update("sw", sensorRow(ciscoSensorEntry, "1", SensorTypeCelsius, SensorScaleUnits, 1, 425, SensorStatusOK))

	sample, err := cache.sensorSample("sw", ciscoKey("1"),
		integerResult(column(ciscoSensorEntry, sensorColumnValue, "1"), 425), nil)
	if err != nil {
		t.Fatalf("sensorSample() returned error: %v", err)
	}
	if sample.Value != 42.5 {
		t.Errorf("Value = %v, want 42.5; the previous poll's precision of 2 was used", sample.Value)
	}
	if sample.Name != "snmp_entity_sensor_celsius" {
		t.Errorf("metric = %s, want snmp_entity_sensor_celsius", sample.Name)
	}
}

// TestEntitySensorNameNeverOutlivesItsPoll is the label-side half of the
// invariant, and the last lifetime hole this design closed.
//
// Names were cached across polls on the reasoning that a stale name mislabels
// rather than mis-scales, and is therefore the lesser harm. It is lesser, and
// it is still a confident lie: an operator reading "Ethernet1/3 Rx Power" for
// a sensor that is now Ethernet1/5 acts on it exactly as readily as on a wrong
// number. An unnamed reading, with gonetmon_entity_sensor_unnamed_total
// incrementing, is the honest outcome.
func TestEntitySensorNameNeverOutlivesItsPoll(t *testing.T) {
	cache := newTestSensorCache()

	// Poll 1 names entPhysicalIndex 1.
	cache.Update("sw", append(
		sensorRow(ciscoSensorEntry, "1", SensorTypeDBm, SensorScaleUnits, 2, -115, SensorStatusOK),
		stringResult(column(physicalEntry, physicalColumnName, "1"), "Ethernet1/3 Rx Power")))

	// Poll 2 describes the same index but reports no name -- ENTITY-MIB was
	// not walked, or the index has been reused and is not yet named.
	cache.Update("sw", sensorRow(ciscoSensorEntry, "1", SensorTypeDBm, SensorScaleUnits, 2, -120, SensorStatusOK))

	sample, err := cache.sensorSample("sw", ciscoKey("1"),
		integerResult(column(ciscoSensorEntry, sensorColumnValue, "1"), -120), nil)
	if err != nil {
		t.Fatalf("sensorSample() returned error: %v", err)
	}

	// The reading still publishes -- the name is a label, not an input to
	// the arithmetic.
	if sample.Value != -1.20 {
		t.Errorf("Value = %v, want -1.2", sample.Value)
	}
	if got, named := sample.Labels[labelEntityName]; named {
		t.Errorf("entity name %q carried over from an earlier poll", got)
	}
}

// TestEntitySensorNameCountsTheGapItLeaves checks that an unnamed reading is
// visible rather than merely quiet, which is what makes refusing to guess an
// acceptable trade.
func TestEntitySensorNameCountsTheGapItLeaves(t *testing.T) {
	const device = "unnamed-count-device"

	before := testutil.ToFloat64(entitySensorUnnamed.WithLabelValues(device))

	cache := newTestSensorCache()
	cache.Update(device, sensorRow(ciscoSensorEntry, "1", SensorTypeDBm, SensorScaleUnits, 2, -115, SensorStatusOK))
	if _, err := cache.sensorSample(device, ciscoKey("1"),
		integerResult(column(ciscoSensorEntry, sensorColumnValue, "1"), -115), nil); err != nil {
		t.Fatalf("sensorSample() returned error: %v", err)
	}

	if after := testutil.ToFloat64(entitySensorUnnamed.WithLabelValues(device)); after != before+1 {
		t.Errorf("unnamed count = %v, want %v", after, before+1)
	}
}

// TestEntitySensorNamesComeFromTheSamePollAsTheReading is the other side: when
// ENTITY-MIB is walked alongside the sensor modules -- which is what the
// poller does, resolving every assigned module into one OID set and exporting
// one slice per cycle -- the name is there and is used.
func TestEntitySensorNamesComeFromTheSamePollAsTheReading(t *testing.T) {
	cache := newTestSensorCache()
	cache.Update("sw", append(
		sensorRow(ciscoSensorEntry, "1", SensorTypeDBm, SensorScaleUnits, 2, -115, SensorStatusOK),
		stringResult(column(physicalEntry, physicalColumnName, "1"), "Ethernet1/3 Rx Power")))

	sample, err := cache.sensorSample("sw", ciscoKey("1"),
		integerResult(column(ciscoSensorEntry, sensorColumnValue, "1"), -115), nil)
	if err != nil {
		t.Fatalf("sensorSample() returned error: %v", err)
	}
	if got := sample.Labels[labelEntityName]; got != "Ethernet1/3 Rx Power" {
		t.Errorf("entity name = %q, want %q", got, "Ethernet1/3 Rx Power")
	}
}
