package metrics

import (
	"math"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/snmp"
)

// Real transceiver walks through the conversion: Nexus 9000 uses scale milli(8) precision 0, Catalyst C9300 scale units(9) precision 1.

// domTolerance absorbs binary rounding only; it is not a tolerance against the switch CLI.
const domTolerance = 0.001

// opticalReading is one transceiver's measured Tx or Rx power and the varbinds
// the device returns for it.
type opticalReading struct {
	device      string
	port        string
	direction   string
	entityIndex string
	entryOID    string
	scale       SensorScale
	precision   SensorPrecision
	raw         int
	sensorName  string
	wantDBm     float64
}

// referenceReadings are the four links from the issue, both directions each.
//
// The raw column is the integer the device puts on the wire: the measured dBm
// times ten to the precision. It is written out rather than computed from
// wantDBm so that a wrong precision here cannot cancel itself out.
var referenceReadings = []opticalReading{
	// Nexus 9000: milli-dBm. Indices are the real 300xxxxxx-form entity
	// indices these switches use.
	{"dist-01", "Ethernet1/3", "Transmit", "300003434", ciscoSensorEntry, SensorScaleMilli, 0, -1163,
		"Ethernet1/3 Lane 1 Transceiver Transmit Power Sensor", -1.163},
	{"dist-01", "Ethernet1/3", "Receive", "300003433", ciscoSensorEntry, SensorScaleMilli, 0, -7328,
		"Ethernet1/3 Lane 1 Transceiver Receive Power Sensor", -7.328},
	{"dist-02", "Ethernet1/3", "Transmit", "300003534", ciscoSensorEntry, SensorScaleMilli, 0, -30,
		"Ethernet1/3 Lane 1 Transceiver Transmit Power Sensor", -0.03},
	{"dist-02", "Ethernet1/3", "Receive", "300003533", ciscoSensorEntry, SensorScaleMilli, 0, -6420,
		"Ethernet1/3 Lane 1 Transceiver Receive Power Sensor", -6.42},

	// Catalyst C9300: tenths of a dBm, and note the name has no "Transceiver"
	// in it. This is the switch whose uplink ran 5.9 dB out of spec for a
	// month without ever going down.
	{"access-01", "Te1/1/1", "Transmit", "1108", ciscoSensorEntry, SensorScaleUnits, 1, -14,
		"Te1/1/1 Transmit Power Sensor", -1.4},
	{"access-01", "Te1/1/1", "Receive", "1109", ciscoSensorEntry, SensorScaleUnits, 1, -75,
		"Te1/1/1 Receive Power Sensor", -7.5},
	{"access-01", "Te1/1/2", "Transmit", "1114", ciscoSensorEntry, SensorScaleUnits, 1, -2,
		"Te1/1/2 Transmit Power Sensor", -0.2},
	{"access-01", "Te1/1/2", "Receive", "1115", ciscoSensorEntry, SensorScaleUnits, 1, -69,
		"Te1/1/2 Receive Power Sensor", -6.9},
}

// entityName is the ENTITY-MIB sensor name as walked, per row.
func (r opticalReading) entityName() string {
	return r.sensorName
}

// walkResults returns the varbinds a poll of this sensor returns: the sensor
// row and the ENTITY-MIB row that names it.
func (r opticalReading) walkResults() []*snmp.Result {
	results := sensorRow(r.entryOID, r.entityIndex, SensorTypeDBm, r.scale, r.precision, r.raw, SensorStatusOK)
	return append(results,
		stringResult(column(physicalEntry, physicalColumnName, r.entityIndex), r.entityName()),
		stringResult(column(physicalEntry, physicalColumnDescr, r.entityIndex), "10Gbase-LR SFP+"),
	)
}

// TestOpticalDOMMatchesMeasuredValues is the issue's verification table, run
// through the conversion the exporter uses.
func TestOpticalDOMMatchesMeasuredValues(t *testing.T) {
	byDevice := map[string][]*snmp.Result{}
	for _, reading := range referenceReadings {
		byDevice[reading.device] = append(byDevice[reading.device], reading.walkResults()...)
	}

	cache := NewTableIndexCache(nil)
	collected := map[string]*Sample{}
	for device, results := range byDevice {
		for _, sample := range ConvertSNMPResultsWithTableIndexes(device, results, nil, cache, nil) {
			if sample.Name == "snmp_entity_sensor_dbm" {
				collected[device+" "+sample.Labels[labelEntityName]] = sample
			}
		}
	}

	for _, reading := range referenceReadings {
		t.Run(reading.device+" "+reading.port+" "+reading.direction, func(t *testing.T) {
			sample, found := collected[reading.device+" "+reading.entityName()]
			if !found {
				t.Fatalf("no dBm sample for %s %s; collected %d", reading.device, reading.entityName(), len(collected))
			}

			if diff := math.Abs(sample.Value - reading.wantDBm); diff > domTolerance {
				t.Errorf("collected %.4f dBm, switch reports %.2f dBm (off by %.4f)",
					sample.Value, reading.wantDBm, diff)
			}
			if sample.Labels[labelSensorType] != "dBm" {
				t.Errorf("sensor_type = %q, want dBm", sample.Labels[labelSensorType])
			}
			if sample.Labels[labelEntityIndex] != reading.entityIndex {
				t.Errorf("entphysicalindex = %q, want %q", sample.Labels[labelEntityIndex], reading.entityIndex)
			}
			if sample.Labels[labelDevice] != reading.device {
				t.Errorf("device = %q, want %q", sample.Labels[labelDevice], reading.device)
			}
		})
	}
}

// TestOpticalDOMWouldCatchTheAccessFault checks a 5.9 dB low link reads differently from a good one.
func TestOpticalDOMWouldCatchTheAccessFault(t *testing.T) {
	// The Te1/1/1 receive sensor: Cisco table, entity index 1109, tenths of a dBm.
	const (
		sensorIndex = "1109"
		goodRaw     = -75  // -7.5 dBm, the re-terminated link, as walked
		badRaw      = -134 // -13.4 dBm, 5.9 dB down, still up and error-free
	)

	cache := newTestSensorCache()
	cache.Update("access-01", sensorRow(ciscoSensorEntry, sensorIndex, SensorTypeDBm, SensorScaleUnits, 1, goodRaw, SensorStatusOK))

	valueOID := column(ciscoSensorEntry, sensorColumnValue, sensorIndex)

	good, err := cache.sensorSample("access-01", ciscoKey(sensorIndex), integerResult(valueOID, goodRaw), nil)
	if err != nil {
		t.Fatalf("sensorSample(good) returned error: %v", err)
	}
	bad, err := cache.sensorSample("access-01", ciscoKey(sensorIndex), integerResult(valueOID, badRaw), nil)
	if err != nil {
		t.Fatalf("sensorSample(bad) returned error: %v", err)
	}

	if diff := math.Abs(good.Value - bad.Value); math.Abs(diff-5.9) > domTolerance {
		t.Errorf("collected difference = %.4f dB, want 5.9 dB (good=%.2f bad=%.2f)",
			diff, good.Value, bad.Value)
	}
}

// TestOpticalDOMSuppressesRawSensorValues checks the raw integer is never exported.
func TestOpticalDOMSuppressesRawSensorValues(t *testing.T) {
	reading := referenceReadings[0]

	cache := NewTableIndexCache(nil)
	samples := ConvertSNMPResultsWithTableIndexes(reading.device, reading.walkResults(), nil, cache, nil)

	valueOID := column(reading.entryOID, sensorColumnValue, reading.entityIndex)
	for _, sample := range samples {
		if sample.Labels[labelOID] != valueOID {
			continue
		}
		if sample.Name != "snmp_entity_sensor_dbm" {
			t.Errorf("value column also exported as %s = %v", sample.Name, sample.Value)
		}
		if sample.Value == float64(reading.raw) {
			t.Errorf("%s exports the raw integer %v", sample.Name, sample.Value)
		}
	}
}

// TestOpticalDOMKeepsSensorMetadataColumns checks that suppressing the value
// column suppressed only that column. The type, scale, precision and status
// columns are correct as integers -- they are enumerations, not measurements
// -- and an operator diagnosing a mis-scaled reading needs to see them.
func TestOpticalDOMKeepsSensorMetadataColumns(t *testing.T) {
	reading := referenceReadings[0]

	cache := NewTableIndexCache(nil)
	samples := ConvertSNMPResultsWithTableIndexes(reading.device, reading.walkResults(), nil, cache, nil)

	byOID := map[string]float64{}
	for _, sample := range samples {
		byOID[sample.Labels[labelOID]] = sample.Value
	}

	want := map[string]float64{
		column(reading.entryOID, sensorColumnType, reading.entityIndex):      float64(SensorTypeDBm),
		column(reading.entryOID, sensorColumnScale, reading.entityIndex):     float64(reading.scale),
		column(reading.entryOID, sensorColumnPrecision, reading.entityIndex): float64(reading.precision),
		column(reading.entryOID, sensorColumnStatus, reading.entityIndex):    float64(SensorStatusOK),
	}
	for oid, value := range want {
		got, present := byOID[oid]
		if !present {
			t.Errorf("no sample for %s", oid)
			continue
		}
		if got != value {
			t.Errorf("%s = %v, want %v", oid, got, value)
		}
	}
}

// TestOpticalDOMDropsUnscalableReadingsFromConversion checks the conversion
// path's refusal, not just the cache's: a value column with no metadata
// produces no sample at all and increments the drop counter.
func TestOpticalDOMDropsUnscalableReadingsFromConversion(t *testing.T) {
	const device = "dom-no-metadata"

	before := droppedCount(device, "missing_metadata")

	cache := NewTableIndexCache(nil)
	samples := ConvertSNMPResultsWithTableIndexes(device, []*snmp.Result{
		integerResult(column(ciscoSensorEntry, sensorColumnValue, "3001"), -115),
	}, nil, cache, nil)

	for _, sample := range samples {
		t.Errorf("exported %s = %v for a reading with no metadata", sample.Name, sample.Value)
	}
	if after := droppedCount(device, "missing_metadata"); after != before+1 {
		t.Errorf("missing_metadata drop count = %v, want %v", after, before+1)
	}
}

// TestOpticalDOMScalesWithoutAnIndexCache checks readings scale and are named without an index cache.
func TestOpticalDOMScalesWithoutAnIndexCache(t *testing.T) {
	reading := referenceReadings[0]

	samples := ConvertSNMPResultsWithTableIndexes(reading.device, reading.walkResults(), nil, nil, nil)

	valueOID := column(ciscoSensorEntry, sensorColumnValue, reading.entityIndex)
	var exported *Sample
	for _, sample := range samples {
		if sample.Labels[labelOID] == valueOID {
			exported = sample
		}
	}

	if exported == nil {
		t.Fatal("no sample for the value column with no index cache")
	}
	if exported.Name != "snmp_entity_sensor_dbm" {
		t.Errorf("value column exported as %s, want snmp_entity_sensor_dbm", exported.Name)
	}
	if exported.Value == float64(reading.raw) {
		t.Errorf("%s exports the raw integer %v", exported.Name, exported.Value)
	}
	if diff := math.Abs(exported.Value - reading.wantDBm); diff > domTolerance {
		t.Errorf("collected %.4f dBm, want %.4f", exported.Value, reading.wantDBm)
	}

	// Nothing is lost without a cache, including the name: everything a
	// reading needs came in with it.
	if got := exported.Labels[labelEntityName]; got != reading.entityName() {
		t.Errorf("entity name = %q, want %q", got, reading.entityName())
	}
}

// TestOpticalDOMMixedSensorTypesStayApart checks the issue's last requirement:
// a chassis reporting optical power, temperature, voltage and current in one
// table must not pile them into one metric, where an average across the metric
// would mix dBm with degrees.
func TestOpticalDOMMixedSensorTypesStayApart(t *testing.T) {
	sensors := []struct {
		index      string
		sensorType SensorType
		scale      SensorScale
		precision  SensorPrecision
		raw        int
		wantMetric string
		wantValue  float64
	}{
		{"3001", SensorTypeDBm, SensorScaleUnits, 2, -115, "snmp_entity_sensor_dbm", -1.15},
		{"3002", SensorTypeCelsius, SensorScaleUnits, 1, 425, "snmp_entity_sensor_celsius", 42.5},
		{"3003", SensorTypeVoltsDC, SensorScaleMilli, 0, 3300, "snmp_entity_sensor_volts", 3.3},
		{"3004", SensorTypeAmperes, SensorScaleMilli, 1, 2505, "snmp_entity_sensor_amperes", 0.2505},
		{"3005", SensorTypeRPM, SensorScaleUnits, -2, 10000, "snmp_entity_sensor_rpm", 10000},
	}
	results := make([]*snmp.Result, 0, len(sensors)*5)
	for _, sensor := range sensors {
		results = append(results, sensorRow(ciscoSensorEntry, sensor.index, sensor.sensorType, sensor.scale, sensor.precision, sensor.raw, SensorStatusOK)...)
	}

	cache := NewTableIndexCache(nil)
	samples := ConvertSNMPResultsWithTableIndexes("dist-01", results, nil, cache, nil)

	byIndex := map[string]*Sample{}
	for _, sample := range samples {
		if index := sample.Labels[labelEntityIndex]; index != "" && sample.Labels[labelSensorType] != "" {
			byIndex[index] = sample
		}
	}

	for _, sensor := range sensors {
		t.Run(sensor.sensorType.String(), func(t *testing.T) {
			sample, found := byIndex[sensor.index]
			if !found {
				t.Fatalf("no sample for sensor %s", sensor.index)
			}
			if sample.Name != sensor.wantMetric {
				t.Errorf("metric = %s, want %s", sample.Name, sensor.wantMetric)
			}
			if diff := math.Abs(sample.Value - sensor.wantValue); diff > domTolerance {
				t.Errorf("value = %v, want %v", sample.Value, sensor.wantValue)
			}
			if sample.Labels[labelSensorType] != sensor.sensorType.String() {
				t.Errorf("sensor_type = %q, want %q", sample.Labels[labelSensorType], sensor.sensorType.String())
			}
		})
	}
}
