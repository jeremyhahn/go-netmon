package metrics

import (
	"math"
	"strconv"
)

// This file holds the arithmetic that turns an entity sensor reading into the
// quantity it represents, and nothing else. It touches no cache, no device and
// no SNMP result, so every rule below is checked directly.
//
// An entity sensor reports its reading as three separate objects: an integer
// value, a scale and a precision (RFC 3433 for ENTITY-SENSOR-MIB, and the same
// design in CISCO-ENTITY-SENSOR-MIB). None of them means anything alone. A
// transceiver reporting -115 with scale units(9) and precision 2 is at
// -1.15 dBm; published as the bare integer it reads as a link 114 dB hotter
// than it is, which is not obviously wrong to anyone looking at a graph. That
// is the failure this file exists to prevent, so it prefers refusing to
// produce a number over producing a plausible one.

// SensorType is the EntitySensorDataType of a reading: what physical quantity
// the sensor measures.
//
// The values through specialEnum(13) are what the vendored MIBs declare.
// dBm(14) and dB(15) are not in either vendored copy -- ENTITY-SENSOR-MIB is
// RFC 3433 from 2002 and the CISCO-ENTITY-SENSOR-MIB copy predates them too --
// but they are what a Cisco switch actually answers for a transceiver's
// optical power, which is the reading this whole path was built for. Naming
// them here rather than editing a vendored MIB keeps the vendored files
// byte-identical to what the vendor ships.
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

// sensorTypeNames is the MIB label of each type, as it appears in the
// sensor_type label. The MIB spelling is kept exactly -- voltsAC and dBm, not
// volts_ac and dbm -- because it is what an operator reads in the MIB and in
// the device's own CLI output.
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

// sensorTypeLabelUnrecognized is the sensor_type label every undeclared type
// shares.
//
// It is not "unknown", even though that reads better, because unknown(2) is a
// declared EntitySensorDataType with its own meaning -- a sensor reporting
// "arbitrary, relative numbers". Collapsing undeclared types onto that label
// would merge two different things into one series.
const sensorTypeLabelUnrecognized = "unrecognized"

// Label returns the value this type contributes to the sensor_type label, and
// to the sensor_type dimension of gonetmon_entity_sensor_samples_total.
//
// Declared types use their MIB spelling. Every undeclared value collapses onto
// one label instead of naming itself, because this string is a Prometheus
// label derived from an Integer32 the device chose: an agent answering a
// different type per poll would otherwise mint one time series per response,
// on the metrics that exist to report what devices are doing. The number
// itself stays in String, which is what diagnostics and error messages use.
func (t SensorType) Label() string {
	if name, known := sensorTypeNames[t]; known {
		return name
	}
	return sensorTypeLabelUnrecognized
}

// String returns the MIB label for the type, or "type_<n>" for a value no
// revision of either MIB declares.
//
// This is the diagnostic spelling and it is deliberately unbounded: it names
// the exact value so a log line or an error identifies the sensor. It must not
// reach a Prometheus label -- use Label for that.
func (t SensorType) String() string {
	if name, known := sensorTypeNames[t]; known {
		return name
	}
	return "type_" + strconv.FormatInt(int64(t), 10)
}

// sensorUnitSuffixes is the metric name suffix each type is published under.
//
// The unit is in the metric name rather than only in a label because a single
// metric name holding dBm, degrees and volts at once cannot be aggregated,
// graphed or alerted on without every query first filtering by type -- and a
// query that forgets to averages optical power with fan speed. voltsAC and
// voltsDC share the volts metric and stay apart by their sensor_type label,
// since both are volts and comparing them is meaningful.
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

// sensorUntypedSuffix is the suffix for readings whose type names no unit:
// other(1), unknown(2), truthvalue(12), specialEnum(13) and anything the MIBs
// do not declare. Their numbers are still readings and are still scaled; they
// are simply not in units of anything, so they share one dimensionless metric
// and are told apart by sensor_type.
const sensorUntypedSuffix = "value"

// entitySensorMetricPrefix is the family every scaled sensor reading is
// published under.
const entitySensorMetricPrefix = metricNamePrefix + "entity_sensor_"

// MetricName returns the Prometheus metric name that readings of this sensor
// type are published under.
//
// promname.EntitySensorMetricName computes the same string for the query side;
// internal/promname/contract_test.go pins the two together.
func (t SensorType) MetricName() string {
	suffix, known := sensorUnitSuffixes[t]
	if !known {
		suffix = sensorUntypedSuffix
	}
	return entitySensorMetricPrefix + suffix
}

// SensorTable identifies which of the two sensor tables a reading and its
// metadata came from.
//
// It exists for exactly two enum values. EntitySensorDataScale is otherwise
// identical in both MIBs, but scale 14 and scale 15 mean different powers of
// ten depending on which module declared the column, so a scale cannot be
// interpreted without knowing who said it. See SensorScale.Exponent.
type SensorTable int

const (
	// SensorTableStandard is ENTITY-SENSOR-MIB (RFC 3433),
	// entPhySensorTable at .1.3.6.1.2.1.99.1.1.1.
	SensorTableStandard SensorTable = iota

	// SensorTableCisco is CISCO-ENTITY-SENSOR-MIB, entSensorValueTable at
	// .1.3.6.1.4.1.9.9.91.1.1.1.1. Every Cisco device in this fleet answers
	// this one and not the other.
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

// Scale 14 and scale 15 are named by their value and not by an SI prefix,
// because no prefix name is correct for both MIBs.
//
// RFC 3433 shipped the labels exa(14) and peta(15) and its published erratum
// swaps them to peta(14) and exa(15); CISCO-ENTITY-SENSOR-MIB kept the
// original label order and changed the exponents instead. So the label "peta"
// is value 14 in one module and value 15 in the other, and a constant called
// SensorScalePeta would be right in one table and a thousand-fold wrong in the
// other. What is on the wire is the integer, so the integer is what these are
// named for. Exponent is where the meaning is resolved.
const (
	SensorScale14 SensorScale = 14
	SensorScale15 SensorScale = 15
)

// Exponent returns the power of ten the scale stands for in table, so that a
// reading expressed in this scale is that reading times 10**Exponent base
// units.
//
// Fifteen of the seventeen values are the arithmetic ladder 3*(scale-9) in
// both MIBs -- a thousand per step, units(9) at 10^0. Values 14 and 15 are
// not, and they are the reason this takes a table at all:
//
//	                        scale 14   scale 15
//	ENTITY-SENSOR-MIB        10^15      10^18     (= the arithmetic ladder)
//	CISCO-ENTITY-SENSOR-MIB  10^18      10^15
//
// The two modules really do disagree, and the resolution is settled rather
// than inferred. **RFC 3433 erratum 2008 (status: Verified, editorial,
// verified by Dan Romascanu, one of the RFC's own authors) corrects the
// LABELS, not the annotations**: it replaces "exa(14), -- 10^15  peta(15), --
// 10^18" with "peta(14), -- 10^15  exa(15), -- 10^18". Each enum value keeps
// the exponent already printed beside it and the misordered SI names are what
// gets fixed. Cisco did the opposite when it wrote its own copy: it kept the
// original label order and moved the exponents to match, which is why its
// value 14 is 10^18.
//
// This is worth spelling out because the wrong reading is seductive and has
// been believed here twice. Both TEXTUAL-CONVENTIONs describe themselves as SI
// prefix tables and RFC 3433's carries a REFERENCE to SI Spec. Publ. 330, so
// it is natural to conclude that the labels are normative, the annotations are
// the typo, and both modules therefore mean the same thing. They do not. The
// IETF's own correction keeps the annotations and rewrites the labels, and the
// integer on the wire is the exponent's key -- not the name. Collapsing this
// to one uniform table scales a standards-compliant agent's scale 14 as 10^18
// instead of 10^15: a thousand-fold high, silently.
//
// A value outside the enumeration is a SensorScaleError, not a guess: an
// unknown scale is an unknown factor of a thousand.
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

// FractionalDigits returns how many of the reading's low-order decimal digits
// are fractional, so that the reading divided by 10**FractionalDigits is the
// fixed-point number the sensor meant.
//
// Only a positive precision scales the value. This is the one place where the
// MIBs' wording invites a wrong reading, and getting it wrong is off by orders
// of magnitude rather than by a rounding error, so the reasoning is worth
// writing down:
//
//   - 1..9 is "the number of decimal places in the fractional part of a
//     SensorValue fixed-point number". A temperature sensor reading 0..100 C
//     in tenths reports 0..1000 with precision 1: divide by 10.
//
//   - 0 is "not a fixed-point number": the value is already the reading.
//
//   - -8..-1 is "the number of accurate digits", which is an accuracy claim
//     and not a scale factor. CISCO-ENTITY-SENSOR-MIB settles it with a worked
//     example: a fan measuring 0..10,000 RPM in 100 RPM steps has precision -2,
//     scale units(9), and "a SensorValue ranging from 0 to 10000", with "the
//     10s and 1s digits ... always 0". The value is already in RPM. Treating
//     -2 as a multiplier the way a positive precision is a divisor would
//     publish that fan at 1,000,000 RPM.
//
// A precision outside -8..9 is a SensorPrecisionError. Clamping it would mean
// choosing a divisor on behalf of an agent that has already contradicted its
// own MIB.
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

// carriesRangeSentinels reports whether the type's value range reserves
// SensorValueUnderflow and SensorValueOverflow. The fixed-point physical types
// range -999,999,999..+999,999,999; other, unknown and specialEnum permit the
// full Integer32 subtype, where the endpoints are ordinary readings.
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

// ScaleSensorValue returns the physical quantity a sensor reading represents,
// in the base unit of its type -- dBm, degrees Celsius, volts, amperes.
//
// It is the composition of the two adjustments above: the value is a
// fixed-point number with precision fractional digits, expressed in units of
// scale. Both are applied as one power of ten so a reading is scaled once.
//
// An out-of-range scale, an out-of-range precision, and the overflow and
// underflow sentinels each return a typed error and no value. A caller that
// cannot scale a reading must not publish it.
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

// applyDecimalExponent returns value * 10**exponent.
//
// A negative exponent divides rather than multiplying by a reciprocal. The
// reciprocal of a power of ten is not exactly representable in binary
// floating point, so multiplying by it rounds twice where dividing rounds
// once, and the extra rounding is what puts a 15th decimal place of noise on
// a number an operator expects to read back as -1.15.
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

// Readable reports whether a sensor in this state has a reading to publish.
//
// Only ok(1) does. unavailable(2) and nonoperational(3) both leave whatever
// the value column last held in place, and publishing that as a measurement is
// how a dead sensor keeps reporting the last good reading forever.
func (s SensorStatus) Readable() bool {
	return s == SensorStatusOK
}
