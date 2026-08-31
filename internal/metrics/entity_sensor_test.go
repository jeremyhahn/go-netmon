package metrics

import (
	"errors"
	"math"
	"testing"
)

// scaleTolerance is how close a scaled reading must be to the expected
// quantity. It is a floating-point epsilon, not a measurement tolerance: the
// arithmetic is deterministic, so anything looser would let a genuine scaling
// error through.
const scaleTolerance = 1e-9

// TestSensorScaleExponent pins the exponent of every declared scale, in both
// modules.
//
// The ladder is stated value by value rather than recomputed with the formula
// under test, so a change to that formula has to be argued with against
// written-down expectations instead of agreeing with itself.
func TestSensorScaleExponent(t *testing.T) {
	tests := []struct {
		name         string
		scale        SensorScale
		wantStandard int
		wantCisco    int
	}{
		{"yocto", SensorScaleYocto, -24, -24},
		{"zepto", SensorScaleZepto, -21, -21},
		{"atto", SensorScaleAtto, -18, -18},
		{"femto", SensorScaleFemto, -15, -15},
		{"pico", SensorScalePico, -12, -12},
		{"nano", SensorScaleNano, -9, -9},
		{"micro", SensorScaleMicro, -6, -6},
		{"milli", SensorScaleMilli, -3, -3},
		{"units", SensorScaleUnits, 0, 0},
		{"kilo", SensorScaleKilo, 3, 3},
		{"mega", SensorScaleMega, 6, 6},
		{"giga", SensorScaleGiga, 9, 9},
		{"tera", SensorScaleTera, 12, 12},

		// The two the modules define differently. RFC 3433 erratum 2008
		// (Verified) corrects the labels and leaves the exponents, so the
		// standard table keeps the arithmetic ladder: 14 is 10^15 and 15
		// is 10^18. Cisco kept the original label order and moved the
		// exponents instead, so its 14 is 10^18 and its 15 is 10^15.
		{"scale 14", SensorScale14, 15, 18},
		{"scale 15", SensorScale15, 18, 15},

		{"zetta", SensorScaleZetta, 21, 21},
		{"yotta", SensorScaleYotta, 24, 24},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotStandard, err := tt.scale.Exponent(SensorTableStandard)
			if err != nil {
				t.Fatalf("Exponent(standard) returned error: %v", err)
			}
			if gotStandard != tt.wantStandard {
				t.Errorf("Exponent(ENTITY-SENSOR-MIB) = %d, want %d", gotStandard, tt.wantStandard)
			}

			gotCisco, err := tt.scale.Exponent(SensorTableCisco)
			if err != nil {
				t.Fatalf("Exponent(cisco) returned error: %v", err)
			}
			if gotCisco != tt.wantCisco {
				t.Errorf("Exponent(CISCO-ENTITY-SENSOR-MIB) = %d, want %d", gotCisco, tt.wantCisco)
			}
		})
	}
}

// TestSensorScaleExponentDiffersOnlyAtFourteenAndFifteen pins the shape of the
// disagreement itself: exactly two values depend on the module, they are 14
// and 15, and they are swapped relative to each other.
//
// Stating it as a property rather than as more examples is what makes it a
// guard. A future change that collapses the two modules to one table -- which
// has been proposed here twice on a plausible-sounding SI argument -- makes
// the sets equal and fails this immediately.
func TestSensorScaleExponentDiffersOnlyAtFourteenAndFifteen(t *testing.T) {
	var differing []SensorScale

	for scale := SensorScaleYocto; scale <= SensorScaleYotta; scale++ {
		standard, err := scale.Exponent(SensorTableStandard)
		if err != nil {
			t.Fatalf("Exponent(standard) for %d returned error: %v", scale, err)
		}
		cisco, err := scale.Exponent(SensorTableCisco)
		if err != nil {
			t.Fatalf("Exponent(cisco) for %d returned error: %v", scale, err)
		}

		if standard != cisco {
			differing = append(differing, scale)
			continue
		}

		// Everywhere the modules agree, they agree on the ladder.
		if ladder := 3 * (int(scale) - int(SensorScaleUnits)); standard != ladder {
			t.Errorf("scale %d agrees across modules at %d, but the ladder says %d",
				scale, standard, ladder)
		}
	}

	if len(differing) != 2 || differing[0] != SensorScale14 || differing[1] != SensorScale15 {
		t.Fatalf("modules differ at scales %v, want exactly [14 15]", differing)
	}

	// And the disagreement is a swap, not an offset.
	standard14, _ := SensorScale14.Exponent(SensorTableStandard)
	standard15, _ := SensorScale15.Exponent(SensorTableStandard)
	cisco14, _ := SensorScale14.Exponent(SensorTableCisco)
	cisco15, _ := SensorScale15.Exponent(SensorTableCisco)

	if standard14 != cisco15 || standard15 != cisco14 {
		t.Errorf("standard(14,15) = (%d,%d) and cisco(14,15) = (%d,%d); want the pair swapped",
			standard14, standard15, cisco14, cisco15)
	}
}

// TestSensorScaleExponentStandardTableIsTheArithmeticLadder pins RFC 3433 as
// corrected by erratum 2008: the labels were wrong, the exponents were not, so
// every value of the standard table is 3*(scale-9).
func TestSensorScaleExponentStandardTableIsTheArithmeticLadder(t *testing.T) {
	for scale := SensorScaleYocto; scale <= SensorScaleYotta; scale++ {
		got, err := scale.Exponent(SensorTableStandard)
		if err != nil {
			t.Fatalf("Exponent() for scale %d returned error: %v", scale, err)
		}
		if want := 3 * (int(scale) - int(SensorScaleUnits)); got != want {
			t.Errorf("standard scale %d = %d, want the ladder value %d", scale, got, want)
		}
	}
}

// TestSensorScaleExponentCiscoTableIsNotTheArithmeticLadder is the same
// property from the other side, and the one that actually matters on this
// fleet: every Cisco device answers the Cisco table, and two of its values are
// deliberately not the ladder.
//
// The tell is monotonicity. A computed ladder is always monotonic, so a
// version of this code that passes the ordering check below has silently gone
// back to computing.
func TestSensorScaleExponentCiscoTableIsNotTheArithmeticLadder(t *testing.T) {
	for scale := SensorScaleYocto; scale <= SensorScaleYotta; scale++ {
		got, err := scale.Exponent(SensorTableCisco)
		if err != nil {
			t.Fatalf("Exponent() for scale %d returned error: %v", scale, err)
		}

		ladder := 3 * (int(scale) - int(SensorScaleUnits))
		switch scale {
		case SensorScale14, SensorScale15:
			if got == ladder {
				t.Errorf("cisco scale %d = %d, which is the arithmetic ladder; "+
					"CISCO-ENTITY-SENSOR-MIB says otherwise", scale, got)
			}
		default:
			if got != ladder {
				t.Errorf("cisco scale %d = %d, want the ladder value %d", scale, got, ladder)
			}
		}
	}

	tera, _ := SensorScaleTera.Exponent(SensorTableCisco)
	got14, _ := SensorScale14.Exponent(SensorTableCisco)
	got15, _ := SensorScale15.Exponent(SensorTableCisco)
	zetta, _ := SensorScaleZetta.Exponent(SensorTableCisco)

	if got14 <= got15 {
		t.Errorf("cisco 14 = %d and 15 = %d; the Cisco ladder is not monotonic here", got14, got15)
	}
	if tera >= got15 || got15 >= got14 || got14 >= zetta {
		t.Errorf("cisco tera=%d 15=%d 14=%d zetta=%d: the four are not in ascending "+
			"magnitude, so one is misassigned", tera, got15, got14, zetta)
	}
}

// TestSensorScaleExponentRejectsUndeclared checks that a scale outside the
// enumeration is refused rather than extrapolated. Extrapolating would invent
// a factor of a thousand per step past the end of a ladder the agent has
// already shown it does not follow.
func TestSensorScaleExponentRejectsUndeclared(t *testing.T) {
	tests := []struct {
		name  string
		scale SensorScale
	}{
		{"zero", 0},
		{"negative", -1},
		{"one past yotta", 18},
		{"far out of range", 1000},
		{"large negative", -9999},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A scale outside the enumeration is undefined in both
			// modules, so neither may invent an exponent for it.
			for _, table := range []SensorTable{SensorTableStandard, SensorTableCisco} {
				got, err := tt.scale.Exponent(table)
				if err == nil {
					t.Fatalf("Exponent(%s) = %d, want error for scale %d", table, got, tt.scale)
				}
				var scaleErr *SensorScaleError
				if !errors.As(err, &scaleErr) {
					t.Fatalf("Exponent(%s) error = %T (%v), want *SensorScaleError", table, err, err)
				}
				if scaleErr.Scale != int32(tt.scale) {
					t.Errorf("SensorScaleError.Scale = %d, want %d", scaleErr.Scale, tt.scale)
				}
				if got != 0 {
					t.Errorf("Exponent(%s) = %d on error, want 0", table, got)
				}
			}
		})
	}
}

// TestSensorPrecisionFractionalDigits covers the whole declared range,
// including the negative half.
//
// The negative cases are the point of this test. A negative precision is an
// accuracy claim, not a scale factor: CISCO-ENTITY-SENSOR-MIB's own example is
// a fan reporting 0..10000 with precision -2 whose reading is 10,000 RPM, not
// 1,000,000. Every negative precision here must therefore divide by nothing.
func TestSensorPrecisionFractionalDigits(t *testing.T) {
	tests := []struct {
		name      string
		precision SensorPrecision
		want      int
	}{
		{"eight accurate digits", -8, 0},
		{"fan example", -2, 0},
		{"one accurate digit", -1, 0},
		{"not fixed point", 0, 0},
		{"tenths", 1, 1},
		{"hundredths", 2, 2},
		{"thousandths", 3, 3},
		{"maximum", 9, 9},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.precision.FractionalDigits()
			if err != nil {
				t.Fatalf("FractionalDigits() returned error: %v", err)
			}
			if got != tt.want {
				t.Errorf("FractionalDigits() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestSensorPrecisionFractionalDigitsRejectsUndeclared checks that a precision
// outside -8..9 is refused rather than clamped.
func TestSensorPrecisionFractionalDigitsRejectsUndeclared(t *testing.T) {
	tests := []struct {
		name      string
		precision SensorPrecision
	}{
		{"one below minimum", -9},
		{"one above maximum", 10},
		{"far below", -100},
		{"far above", 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.precision.FractionalDigits()
			if err == nil {
				t.Fatalf("FractionalDigits() = %d, want error for precision %d", got, tt.precision)
			}
			var precisionErr *SensorPrecisionError
			if !errors.As(err, &precisionErr) {
				t.Fatalf("FractionalDigits() error = %T (%v), want *SensorPrecisionError", err, err)
			}
			if precisionErr.Precision != int32(tt.precision) {
				t.Errorf("SensorPrecisionError.Precision = %d, want %d", precisionErr.Precision, tt.precision)
			}
		})
	}
}

// TestScaleSensorValue drives the combined arithmetic across the scales,
// precisions and sensor types the fleet's devices actually report, plus the
// extremes of both enumerations.
func TestScaleSensorValue(t *testing.T) {
	tests := []struct {
		name      string
		raw       int64
		scale     SensorScale
		precision SensorPrecision
		table     SensorTable
		want      float64
	}{
		// Optical power, the reading this path was built for. A Nexus
		// reports hundredths, a Catalyst reports tenths, and both are
		// dBm at units scale.
		{"dBm hundredths negative", -115, SensorScaleUnits, 2, SensorTableCisco, -1.15},
		{"dBm hundredths near zero", -2, SensorScaleUnits, 2, SensorTableCisco, -0.02},
		{"dBm tenths negative", -14, SensorScaleUnits, 1, SensorTableCisco, -1.4},
		{"dBm tenths receive", -74, SensorScaleUnits, 1, SensorTableCisco, -7.4},
		{"dBm positive", 25, SensorScaleUnits, 1, SensorTableCisco, 2.5},
		{"dBm zero", 0, SensorScaleUnits, 2, SensorTableCisco, 0},

		// The RFC 3433 worked example: a temperature sensor reading
		// 0..1000 in tenths of a degree.
		{"celsius tenths", 425, SensorScaleUnits, 1, SensorTableCisco, 42.5},
		{"celsius whole degrees", 42, SensorScaleUnits, 0, SensorTableCisco, 42},

		// Negative precision changes nothing but the accuracy claim.
		// CISCO-ENTITY-SENSOR-MIB's fan: 10000 with precision -2 is
		// ten thousand RPM.
		{"rpm accurate to hundreds", 10000, SensorScaleUnits, -2, SensorTableCisco, 10000},
		{"rpm accurate to tens", 9900, SensorScaleUnits, -1, SensorTableCisco, 9900},
		{"rpm minimum precision", 12345678, SensorScaleUnits, -8, SensorTableCisco, 12345678},

		// Scale without precision.
		{"millivolts", 3300, SensorScaleMilli, 0, SensorTableCisco, 3.3},
		{"microamperes", 250, SensorScaleMicro, 0, SensorTableCisco, 0.00025},
		{"kilohertz", 100, SensorScaleKilo, 0, SensorTableCisco, 100000},
		{"megahertz", 2400, SensorScaleMega, 0, SensorTableCisco, 2.4e9},

		// Scale and precision together, in both directions.
		{"millivolts to tenths", 33005, SensorScaleMilli, 1, SensorTableCisco, 3.3005},
		{"kilowatts to hundredths", 1250, SensorScaleKilo, 2, SensorTableCisco, 12500},
		{"nano with precision", 5000, SensorScaleNano, 3, SensorTableCisco, 5e-9},

		// The scales either side of the exa/peta discontinuity, which no
		// other case here reaches: an off-by-one on this pair used to be
		// invisible between the ordinary scales and yocto/yotta.
		{"tera", 5, SensorScaleTera, 0, SensorTableCisco, 5e12},
		{"zetta", 5, SensorScaleZetta, 0, SensorTableCisco, 5e21},

		// Scale 14 and 15 through the whole scaling path, in both
		// modules and in both directions. Reading either module with the
		// other's semantics lands these a thousand-fold out, which is
		// what a single uniform table would have done.
		{"scale 14, standard", 5, SensorScale14, 0, SensorTableStandard, 5e15},
		{"scale 14, cisco", 5, SensorScale14, 0, SensorTableCisco, 5e18},
		{"scale 15, standard", 5, SensorScale15, 0, SensorTableStandard, 5e18},
		{"scale 15, cisco", 5, SensorScale15, 0, SensorTableCisco, 5e15},
		{"scale 14, standard, with precision", 5, SensorScale14, 3, SensorTableStandard, 5e12},
		{"scale 14, cisco, with precision", 5, SensorScale14, 3, SensorTableCisco, 5e15},

		// Both ends of the scale ladder.
		{"yocto", 1, SensorScaleYocto, 0, SensorTableCisco, 1e-24},
		{"yotta", 1, SensorScaleYotta, 0, SensorTableCisco, 1e24},
		{"yotta with maximum precision", 1, SensorScaleYotta, 9, SensorTableCisco, 1e15},
		{"yocto with maximum precision", 1, SensorScaleYocto, 9, SensorTableCisco, 1e-33},

		// Sentinel-adjacent values are readings, not sentinels.
		{"just inside overflow", 999999999, SensorScaleUnits, 0, SensorTableCisco, 999999999},
		{"just inside underflow", -999999999, SensorScaleUnits, 0, SensorTableCisco, -999999999},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ScaleSensorValue(tt.raw, tt.scale, tt.precision, tt.table)
			if err != nil {
				t.Fatalf("ScaleSensorValue() returned error: %v", err)
			}
			if !closeEnough(got, tt.want) {
				t.Errorf("ScaleSensorValue(%d, scale %d, precision %d, %s) = %v, want %v",
					tt.raw, tt.scale, tt.precision, tt.table, got, tt.want)
			}
		})
	}
}

// TestScaleSensorValueRejectsUninterpretable covers every input that has no
// reading in it: the two reserved sentinels and metadata outside either
// enumeration. Each must return no value, because a caller that publishes a
// zero here reports a transceiver as dark and a temperature as freezing.
func TestScaleSensorValueRejectsUninterpretable(t *testing.T) {
	tests := []struct {
		name      string
		raw       int64
		scale     SensorScale
		precision SensorPrecision
		check     func(*testing.T, error)
	}{
		{"overflow sentinel", SensorValueOverflow, SensorScaleUnits, 1, wantRangeError(SensorValueOverflow)},
		{"underflow sentinel", SensorValueUnderflow, SensorScaleUnits, 1, wantRangeError(SensorValueUnderflow)},
		{"beyond overflow", 2000000000, SensorScaleUnits, 0, wantRangeError(2000000000)},
		{"beyond underflow", -2000000000, SensorScaleUnits, 0, wantRangeError(-2000000000)},
		{"scale zero", 100, 0, 1, wantScaleError(0)},
		{"scale past yotta", 100, 18, 1, wantScaleError(18)},
		{"precision past maximum", 100, SensorScaleUnits, 10, wantPrecisionError(10)},
		{"precision past minimum", 100, SensorScaleUnits, -9, wantPrecisionError(-9)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ScaleSensorValue(tt.raw, tt.scale, tt.precision, SensorTableCisco)
			if err == nil {
				t.Fatalf("ScaleSensorValue() = %v, want error", got)
			}
			if got != 0 {
				t.Errorf("ScaleSensorValue() = %v on error, want 0", got)
			}
			tt.check(t, err)
		})
	}
}

// wantRangeError asserts the error is a *SensorRangeError naming value.
func wantRangeError(value int64) func(*testing.T, error) {
	return func(t *testing.T, err error) {
		t.Helper()
		var rangeErr *SensorRangeError
		if !errors.As(err, &rangeErr) {
			t.Fatalf("error = %T (%v), want *SensorRangeError", err, err)
		}
		if rangeErr.Value != value {
			t.Errorf("SensorRangeError.Value = %d, want %d", rangeErr.Value, value)
		}
	}
}

// wantScaleError asserts the error is a *SensorScaleError naming scale.
func wantScaleError(scale int32) func(*testing.T, error) {
	return func(t *testing.T, err error) {
		t.Helper()
		var scaleErr *SensorScaleError
		if !errors.As(err, &scaleErr) {
			t.Fatalf("error = %T (%v), want *SensorScaleError", err, err)
		}
		if scaleErr.Scale != scale {
			t.Errorf("SensorScaleError.Scale = %d, want %d", scaleErr.Scale, scale)
		}
	}
}

// wantPrecisionError asserts the error is a *SensorPrecisionError naming
// precision.
func wantPrecisionError(precision int32) func(*testing.T, error) {
	return func(t *testing.T, err error) {
		t.Helper()
		var precisionErr *SensorPrecisionError
		if !errors.As(err, &precisionErr) {
			t.Fatalf("error = %T (%v), want *SensorPrecisionError", err, err)
		}
		if precisionErr.Precision != precision {
			t.Errorf("SensorPrecisionError.Precision = %d, want %d", precisionErr.Precision, precision)
		}
	}
}

// TestScaleSensorValueSentinelChecksPrecedeMetadata pins that a sentinel is
// reported as a sentinel even when the metadata is also unusable. The sentinel
// is the more specific diagnosis -- the sensor is out of range, which is a
// fact about the link -- and it is what an operator needs to see.
func TestScaleSensorValueSentinelChecksPrecedeMetadata(t *testing.T) {
	_, err := ScaleSensorValue(SensorValueOverflow, 0, 99, SensorTableCisco)

	var rangeErr *SensorRangeError
	if !errors.As(err, &rangeErr) {
		t.Fatalf("error = %T (%v), want *SensorRangeError", err, err)
	}
}

// TestScaleSensorValueRoundTripsDecimals checks the one thing a reciprocal
// multiplication would get wrong: a scaled reading has to render back as the
// decimal the device's own CLI prints, not as that decimal plus floating-point
// dust in the fifteenth place.
func TestScaleSensorValueRoundTripsDecimals(t *testing.T) {
	tests := []struct {
		name      string
		raw       int64
		precision SensorPrecision
		want      float64
	}{
		{"-1.15", -115, 2, -1.15},
		{"-7.87", -787, 2, -7.87},
		{"-6.38", -638, 2, -6.38},
		{"-6.9", -69, 1, -6.9},
		{"-1.4", -14, 1, -1.4},
		{"3.07", 307, 2, 3.07},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ScaleSensorValue(tt.raw, SensorScaleUnits, tt.precision, SensorTableCisco)
			if err != nil {
				t.Fatalf("ScaleSensorValue() returned error: %v", err)
			}
			if got != tt.want {
				t.Errorf("ScaleSensorValue(%d, units, %d) = %.20g, want exactly %.20g",
					tt.raw, tt.precision, got, tt.want)
			}
		})
	}
}

// TestSensorTypeString covers the declared labels, the two Cisco added after
// the vendored MIBs were written, and an undeclared value.
func TestSensorTypeString(t *testing.T) {
	tests := []struct {
		sensorType SensorType
		want       string
	}{
		{SensorTypeOther, "other"},
		{SensorTypeUnknown, "unknown"},
		{SensorTypeVoltsAC, "voltsAC"},
		{SensorTypeVoltsDC, "voltsDC"},
		{SensorTypeAmperes, "amperes"},
		{SensorTypeWatts, "watts"},
		{SensorTypeHertz, "hertz"},
		{SensorTypeCelsius, "celsius"},
		{SensorTypePercentRH, "percentRH"},
		{SensorTypeRPM, "rpm"},
		{SensorTypeCMM, "cmm"},
		{SensorTypeTruthValue, "truthvalue"},
		{SensorTypeSpecialEnum, "specialEnum"},
		{SensorTypeDBm, "dBm"},
		{SensorTypeDB, "dB"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.sensorType.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSensorTypeStringUndeclared checks that a type no MIB revision declares
// still identifies itself, rather than being folded into unknown(2), which is
// a declared value with its own meaning.
func TestSensorTypeStringUndeclared(t *testing.T) {
	tests := []struct {
		name       string
		sensorType SensorType
		want       string
	}{
		{"zero", 0, "type_0"},
		{"negative", -1, "type_-1"},
		{"one past dB", 16, "type_16"},
		{"far out of range", 9999, "type_9999"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.sensorType.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSensorTypeMetricName pins each type to the metric it is published under,
// and in particular that dBm never shares a metric name with celsius or volts.
func TestSensorTypeMetricName(t *testing.T) {
	tests := []struct {
		name       string
		sensorType SensorType
		want       string
	}{
		{"dBm", SensorTypeDBm, "snmp_entity_sensor_dbm"},
		{"dB", SensorTypeDB, "snmp_entity_sensor_db"},
		{"celsius", SensorTypeCelsius, "snmp_entity_sensor_celsius"},
		{"voltsAC", SensorTypeVoltsAC, "snmp_entity_sensor_volts"},
		{"voltsDC", SensorTypeVoltsDC, "snmp_entity_sensor_volts"},
		{"amperes", SensorTypeAmperes, "snmp_entity_sensor_amperes"},
		{"watts", SensorTypeWatts, "snmp_entity_sensor_watts"},
		{"hertz", SensorTypeHertz, "snmp_entity_sensor_hertz"},
		{"rpm", SensorTypeRPM, "snmp_entity_sensor_rpm"},
		{"percentRH", SensorTypePercentRH, "snmp_entity_sensor_percent"},
		{"cmm", SensorTypeCMM, "snmp_entity_sensor_cubic_meters_per_minute"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.sensorType.MetricName(); got != tt.want {
				t.Errorf("MetricName() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSensorTypeMetricNameDimensionless checks that types naming no unit share
// one dimensionless metric rather than each inventing a name, and that an
// undeclared type lands there too instead of producing a metric name with a
// number in it.
func TestSensorTypeMetricNameDimensionless(t *testing.T) {
	dimensionless := []SensorType{
		SensorTypeOther,
		SensorTypeUnknown,
		SensorTypeTruthValue,
		SensorTypeSpecialEnum,
		0,
		42,
	}

	for _, sensorType := range dimensionless {
		t.Run(sensorType.String(), func(t *testing.T) {
			if got := sensorType.MetricName(); got != "snmp_entity_sensor_value" {
				t.Errorf("MetricName() = %q, want %q", got, "snmp_entity_sensor_value")
			}
		})
	}
}

// TestSensorTypeMetricNamesAreValid checks that every name this package can
// emit is a legal Prometheus metric name, which SanitizeMetricName is the
// authority on. A name that needed sanitizing would reach Prometheus in one
// form and be queried back in another.
func TestSensorTypeMetricNamesAreValid(t *testing.T) {
	for sensorType := SensorType(-2); sensorType <= 20; sensorType++ {
		name := sensorType.MetricName()
		if sanitized := SanitizeMetricName(name); sanitized != name {
			t.Errorf("MetricName() for type %d = %q, which sanitizes to %q", sensorType, name, sanitized)
		}
	}
}

// TestSensorStatusString covers the three declared statuses and an undeclared
// one.
func TestSensorStatusString(t *testing.T) {
	tests := []struct {
		status SensorStatus
		want   string
	}{
		{SensorStatusOK, "ok"},
		{SensorStatusUnavailable, "unavailable"},
		{SensorStatusNonoperational, "nonoperational"},
		{0, "status_0"},
		{7, "status_7"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.status.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSensorStatusReadable checks that only ok(1) yields a reading. A sensor
// the agent cannot read and one it believes is broken both leave a stale
// number in the value column.
func TestSensorStatusReadable(t *testing.T) {
	tests := []struct {
		name   string
		status SensorStatus
		want   bool
	}{
		{"ok", SensorStatusOK, true},
		{"unavailable", SensorStatusUnavailable, false},
		{"nonoperational", SensorStatusNonoperational, false},
		{"undeclared", 9, false},
		{"zero", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.status.Readable(); got != tt.want {
				t.Errorf("Readable() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestApplyDecimalExponent exercises the exponent helper directly, including
// the zero-exponent shortcut that leaves a value untouched.
func TestApplyDecimalExponent(t *testing.T) {
	tests := []struct {
		name     string
		value    float64
		exponent int
		want     float64
	}{
		{"identity", 1.5, 0, 1.5},
		{"multiply", 1.5, 3, 1500},
		{"divide", 1500, -3, 1.5},
		{"divide to hundredths", -115, -2, -1.15},
		{"negative value multiplied", -2, 6, -2e6},
		{"zero value", 0, -9, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := applyDecimalExponent(tt.value, tt.exponent); !closeEnough(got, tt.want) {
				t.Errorf("applyDecimalExponent(%v, %d) = %v, want %v", tt.value, tt.exponent, got, tt.want)
			}
		})
	}
}

// BenchmarkScaleSensorValue measures the arithmetic on the poll path. It runs
// once per sensor per poll, so it has to allocate nothing.
func BenchmarkScaleSensorValue(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := ScaleSensorValue(-115, SensorScaleUnits, 2, SensorTableCisco); err != nil {
			b.Fatalf("ScaleSensorValue() returned error: %v", err)
		}
	}
}

// closeEnough compares two scaled readings, relatively for large magnitudes
// and absolutely near zero, so one epsilon covers a ladder spanning 10^-33 to
// 10^24.
func closeEnough(got, want float64) bool {
	if got == want {
		return true
	}
	scale := math.Max(math.Abs(got), math.Abs(want))
	if scale == 0 {
		return true
	}
	return math.Abs(got-want)/scale <= scaleTolerance
}

// TestSensorTableString checks that a table names the module it stands for.
//
// This string appears in test failures and diagnostics about a mis-scaled
// reading, which is the one situation where knowing which of the two modules
// answered is the whole diagnosis.
func TestSensorTableString(t *testing.T) {
	tests := []struct {
		name  string
		table SensorTable
		want  string
	}{
		{"standard", SensorTableStandard, "ENTITY-SENSOR-MIB"},
		{"cisco", SensorTableCisco, "CISCO-ENTITY-SENSOR-MIB"},
		// The zero value is the standard table, which is what an
		// unpopulated sensorMetadata reads as.
		{"zero value", SensorTable(0), "ENTITY-SENSOR-MIB"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.table.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSensorTableStringUndeclared checks that a table value outside the two
// declared ones does not claim to be Cisco. Only SensorTableCisco selects the
// Cisco exponents, so anything else has to read as the standard module for the
// string and the arithmetic to agree.
func TestSensorTableStringUndeclared(t *testing.T) {
	for _, table := range []SensorTable{-1, 2, 99} {
		if got := table.String(); got != "ENTITY-SENSOR-MIB" {
			t.Errorf("SensorTable(%d).String() = %q, want the standard module", table, got)
		}

		// ...and it must scale like the standard module too.
		got, err := SensorScale14.Exponent(table)
		if err != nil {
			t.Fatalf("Exponent() returned error: %v", err)
		}
		if got != 15 {
			t.Errorf("SensorTable(%d) scales 14 as 10^%d, want 10^15", table, got)
		}
	}
}
