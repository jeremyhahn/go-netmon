package metrics

import (
	"math"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/snmp"
)

// This file is the verification the whole change is judged by: the readings
// the four production transceivers reported on 2026-08-31, measured from the
// transceivers' own DOM, put through the real conversion path and compared to
// what the switches themselves print.
//
// Every row below was captured by snmpwalk against the production switches on
// 2026-08-31, not derived from the dBm figures. That matters: an encoding
// invented to match a known answer cannot fail, and an earlier draft of this
// fixture guessed the Nexus encoding wrong while still "passing".
//
// The two families encode the same quantity differently, and it is SCALE that
// separates them, not precision:
//
//	Nexus 9000 (dist-01/02)   scale milli(8), precision 0 -- milli-dBm
//	                             "Ethernet1/3 Lane 1 Transceiver Receive Power Sensor"
//	                             raw -7328 -> -7.328 dBm
//	Catalyst C9300 (access-01) scale units(9), precision 1 -- tenths of a dBm
//	                             "Te1/1/1 Receive Power Sensor"
//	                             raw -75 -> -7.5 dBm
//
// Read with the wrong scale a reading lands a factor of a thousand out; with
// the wrong precision, a factor of ten. Either is a plausible-looking number on
// a link that is fine, in the same range as the 5.9 dB fault that started this.
//
// Both families answer the CISCO table. The standard ENTITY-SENSOR-MIB at
// .1.3.6.1.2.1.99.1.1.1 returns "No Such Object" on the Nexus AND the Catalyst,
// so the standard-table decoder is exercised by the unit tests rather than
// here. It is kept because other vendors do answer it, not because these do.

// domTolerance is how far a collected reading may sit from the dBm computed
// from its own varbinds. The scaling is exact, so this absorbs only the last
// binary digit; a real precision or scale error is orders of magnitude larger.
//
// It is deliberately NOT a tolerance against the switch CLI. Optical power
// drifts by a few tenths of a dB between samples -- the walk below and the CLI
// reading minutes earlier differ by up to 0.5 dB on a healthy link -- so
// pinning to the CLI would make this test flap. What is pinned is the
// arithmetic: these varbinds must produce these dBm.
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

// entityName is what ENTITY-MIB actually reports for the sensor. The two
// families word it differently -- the Nexus includes "Lane 1 Transceiver", the
// Catalyst says only "<port> <direction> Power Sensor" -- so it is captured per
// row rather than composed, which is what a walk shows.
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

// TestOpticalDOMWouldCatchTheAccessFault is the regression the issue is
// actually asking for: a link that is up, error-free, and 5.9 dB below where
// it should be has to show up as a number that differs from the good link's.
//
// It is the same assertion as the table above seen from the other side --
// nothing here is special-cased for a fault -- but it states the failure the
// collection exists to catch, so a future change that flattens every reading
// to a constant fails a test that says why that matters.
func TestOpticalDOMWouldCatchTheAccessFault(t *testing.T) {
	// The walked Te1/1/1 receive sensor: CISCO table, entity index 1109,
	// dBm at units scale in tenths.
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

// TestOpticalDOMSuppressesRawSensorValues checks that the integer the device
// put on the wire is never exported under any metric name.
//
// This is the half of the issue that a passing value assertion does not cover.
// If the generic conversion also emitted snmp_entsensorvalue = -115 alongside
// the scaled sample, every assertion above would still pass while Prometheus
// held a second series reporting the transceiver 114 dB hot.
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

// TestOpticalDOMScalesWithoutAnIndexCache checks the degenerate wiring: a
// conversion with no index cache still scales and names its readings
// correctly, because everything they need arrived with them.
//
// This assertion has been inverted twice as the design tightened. It first
// said nothing was exported at all, which was right while metadata lived only
// in a cache; then that the reading was scaled but unnamed, which was right
// while names alone were cached. Now neither is cached and the degenerate
// wiring costs nothing at all. What has not changed through any of it is the
// part that matters: the raw integer never reaches Prometheus under any name.
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
