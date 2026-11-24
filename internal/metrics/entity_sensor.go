package metrics

import (
	"math"
	"strconv"
)

// Arithmetic that turns an entity sensor value, scale and precision (RFC 3433) into the quantity it represents.

// SensorType is the EntitySensorDataType of a reading; dBm(14) and dB(15) are not in the vendored MIBs but Cisco devices report them.
type SensorType int32

// EntitySensorDataType values.
const (
	SensorTypeOther       SensorType = 1
	SensorTypeUnknown     SensorType = 2
	SensorTypeVoltsAC     SensorType = 3
	SensorTypeVoltsDC     SensorType = 4
	SensorTypeAmperes     SensorType = 5
	SensorTypeWatts       SensorType = 6
	SensorTypeHertz       SensorType = 7
	SensorTypeCelsius     SensorType = 8
	SensorTypePercentRH   SensorType = 9
	SensorTypeRPM         SensorType = 10
	SensorTypeCMM         SensorType = 11
	SensorTypeTruthValue  SensorType = 12
	SensorTypeSpecialEnum SensorType = 13
	SensorTypeDBm         SensorType = 14
	SensorTypeDB          SensorType = 15
)

// sensorTypeNames is each type's MIB spelling, used as the sensor_type label.
var sensorTypeNames = map[SensorType]string{
	SensorTypeOther:       "other",
	SensorTypeUnknown:     "unknown",
	SensorTypeVoltsAC:     "voltsAC",
	SensorTypeVoltsDC:     "voltsDC",
	SensorTypeAmperes:     "amperes",
	SensorTypeWatts:       "watts",
	SensorTypeHertz:       "hertz",
	SensorTypeCelsius:     "celsius",
	SensorTypePercentRH:   "percentRH",
	SensorTypeRPM:         "rpm",
	SensorTypeCMM:         "cmm",
	SensorTypeTruthValue:  "truthvalue",
	SensorTypeSpecialEnum: "specialEnum",
	SensorTypeDBm:         "dBm",
	SensorTypeDB:          "dB",
}

// sensorTypeLabelUnrecognized is the label every undeclared type shares; distinct from unknown(2).
const sensorTypeLabelUnrecognized = "unrecognized"

// Label returns the sensor_type label value; undeclared values share one label to bound cardinality.
func (t SensorType) Label() string {
	if name, known := sensorTypeNames[t]; known {
		return name
	}
	return sensorTypeLabelUnrecognized
}

// String returns the MIB label or "type_<n>"; for diagnostics only, never a Prometheus label.
func (t SensorType) String() string {
	if name, known := sensorTypeNames[t]; known {
		return name
	}
	return "type_" + strconv.FormatInt(int64(t), 10)
}

// sensorUnitSuffixes is the metric name suffix per type; voltsAC and voltsDC share volts.
var sensorUnitSuffixes = map[SensorType]string{
	SensorTypeVoltsAC:   "volts",
	SensorTypeVoltsDC:   "volts",
	SensorTypeAmperes:   "amperes",
	SensorTypeWatts:     "watts",
	SensorTypeHertz:     "hertz",
	SensorTypeCelsius:   "celsius",
	SensorTypePercentRH: "percent",
	SensorTypeRPM:       "rpm",
	SensorTypeCMM:       "cubic_meters_per_minute",
	SensorTypeDBm:       "dbm",
	SensorTypeDB:        "db",
}

// sensorUntypedSuffix is the dimensionless suffix for types that name no unit.
const sensorUntypedSuffix = "value"

// entitySensorMetricPrefix is the family every scaled sensor reading is
// published under.
const entitySensorMetricPrefix = metricNamePrefix + "entity_sensor_"

// MetricName returns the metric name for this type; promname.EntitySensorMetricName must match (contract_test.go).
func (t SensorType) MetricName() string {
	suffix, known := sensorUnitSuffixes[t]
	if !known {
		suffix = sensorUntypedSuffix
	}
	return entitySensorMetricPrefix + suffix
}

// SensorTable identifies the source table, needed because scales 14 and 15 differ between the MIBs.
type SensorTable int

const (
	// SensorTableStandard is ENTITY-SENSOR-MIB (RFC 3433) entPhySensorTable, .1.3.6.1.2.1.99.1.1.1.
	SensorTableStandard SensorTable = iota

	// SensorTableCisco is CISCO-ENTITY-SENSOR-MIB entSensorValueTable, .1.3.6.1.4.1.9.9.91.1.1.1.1.
	SensorTableCisco
)

// String names the module, for error messages and diagnostics.
func (t SensorTable) String() string {
	if t == SensorTableCisco {
		return "CISCO-ENTITY-SENSOR-MIB"
	}
	return "ENTITY-SENSOR-MIB"
}

// SensorScale is the EntitySensorDataScale of a reading: the SI prefix its
// integer value is expressed in.
type SensorScale int32

// EntitySensorDataScale values.
const (
	SensorScaleYocto SensorScale = 1
	SensorScaleZepto SensorScale = 2
	SensorScaleAtto  SensorScale = 3
	SensorScaleFemto SensorScale = 4
	SensorScalePico  SensorScale = 5
	SensorScaleNano  SensorScale = 6
	SensorScaleMicro SensorScale = 7
	SensorScaleMilli SensorScale = 8
	SensorScaleUnits SensorScale = 9
	SensorScaleKilo  SensorScale = 10
	SensorScaleMega  SensorScale = 11
	SensorScaleGiga  SensorScale = 12
	SensorScaleTera  SensorScale = 13
	SensorScaleZetta SensorScale = 16
	SensorScaleYotta SensorScale = 17
)

// Scales 14 and 15 are named by value: their SI names differ between the two MIBs.
const (
	SensorScale14 SensorScale = 14
	SensorScale15 SensorScale = 15
)

// Exponent returns the power of ten for this scale in table: 3*(scale-9), except 14/15 are 10^15/10^18 in ENTITY-SENSOR-MIB (RFC 3433 erratum 2008) and 10^18/10^15 in Cisco's.
func (s SensorScale) Exponent(table SensorTable) (int, error) {
	if s < SensorScaleYocto || s > SensorScaleYotta {
		return 0, &SensorScaleError{Scale: int32(s)}
	}

	// The only two values for which table is consulted at all. Everything
	// else, in either module, is the ladder below.
	if table == SensorTableCisco {
		switch s {
		case SensorScale14:
			return 18, nil
		case SensorScale15:
			return 15, nil
		}
	}

	return 3 * (int(s) - int(SensorScaleUnits)), nil
}

// SensorPrecision is the EntitySensorPrecision of a reading.
type SensorPrecision int32

// The range EntitySensorPrecision is declared over, in both MIBs.
const (
	SensorPrecisionMin SensorPrecision = -8
	SensorPrecisionMax SensorPrecision = 9
)

// FractionalDigits returns the fractional digits: precision 1..9 divides, 0 and -8..-1 (accuracy only) do not.
func (p SensorPrecision) FractionalDigits() (int, error) {
	if p < SensorPrecisionMin || p > SensorPrecisionMax {
		return 0, &SensorPrecisionError{Precision: int32(p)}
	}
	if p <= 0 {
		return 0, nil
	}
	return int(p), nil
}

// The sentinel values EntitySensorValue reserves. They are not readings: they
// say the sensor's true value is outside the range the object can carry.
const (
	SensorValueUnderflow int64 = -1000000000
	SensorValueOverflow  int64 = 1000000000
)

// carriesRangeSentinels reports whether the type reserves the underflow and overflow sentinel values.
func (t SensorType) carriesRangeSentinels() bool {
	switch t {
	case SensorTypeOther, SensorTypeUnknown, SensorTypeSpecialEnum:
		return false
	}
	return true
}

// checkSensorValueRange classifies raw for the type. Sentinel types match the
// two sentinels by exact equality; a value beyond them is malformed, not a
// sentinel it happens to sit near. Other types accept any Integer32.
func checkSensorValueRange(raw int64, sensorType SensorType) error {
	if sensorType.carriesRangeSentinels() {
		switch {
		case raw == SensorValueUnderflow || raw == SensorValueOverflow:
			return &SensorRangeError{Value: raw}
		case raw < SensorValueUnderflow || raw > SensorValueOverflow:
			return &SensorMalformedValueError{Value: raw, Type: sensorType}
		}
		return nil
	}
	if raw < math.MinInt32 || raw > math.MaxInt32 {
		return &SensorMalformedValueError{Value: raw, Type: sensorType}
	}
	return nil
}

// ScaleSensorValue returns the reading in its type's base unit, or a typed error that means do not publish.
func ScaleSensorValue(raw int64, sensorType SensorType, scale SensorScale, precision SensorPrecision, table SensorTable) (float64, error) {
	if err := checkSensorValueRange(raw, sensorType); err != nil {
		return 0, err
	}

	scaleExponent, err := scale.Exponent(table)
	if err != nil {
		return 0, err
	}

	fractionalDigits, err := precision.FractionalDigits()
	if err != nil {
		return 0, err
	}

	return applyDecimalExponent(float64(raw), scaleExponent-fractionalDigits), nil
}

// applyDecimalExponent returns value * 10**exponent, dividing for negative exponents to round once.
func applyDecimalExponent(value float64, exponent int) float64 {
	switch {
	case exponent == 0:
		return value
	case exponent > 0:
		return value * math.Pow10(exponent)
	default:
		return value / math.Pow10(-exponent)
	}
}

// SensorStatus is the EntitySensorStatus of a sensor: whether the agent can
// presently read it.
type SensorStatus int32

// EntitySensorStatus values.
const (
	SensorStatusOK             SensorStatus = 1
	SensorStatusUnavailable    SensorStatus = 2
	SensorStatusNonoperational SensorStatus = 3
)

// String returns the MIB label for the status, or "status_<n>" for a value
// neither MIB declares.
func (s SensorStatus) String() string {
	switch s {
	case SensorStatusOK:
		return "ok"
	case SensorStatusUnavailable:
		return "unavailable"
	case SensorStatusNonoperational:
		return "nonoperational"
	default:
		return "status_" + strconv.FormatInt(int64(s), 10)
	}
}

// Readable reports whether the sensor state is ok(1), the only state with a reading to publish.
func (s SensorStatus) Readable() bool {
	return s == SensorStatusOK
}
