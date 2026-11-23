package mib

import (
	"math"
	"strings"
	"testing"
)

// TestEnumLabel covers the rendering that feeds Prometheus label values. The
// bare label with no "(n)" suffix is part of the series identity, so a change
// here orphans every existing series for that metric.
func TestEnumLabel(t *testing.T) {
	ifOperStatus := map[int64]string{1: "up", 2: "down", 3: "testing"}

	if got := EnumLabel(ifOperStatus, 1); got != "up" {
		t.Errorf("EnumLabel(1) = %q, want %q", got, "up")
	}
	// A value the MIB does not enumerate must still produce a stable label
	// rather than an empty one, which would collapse distinct series.
	if got := EnumLabel(ifOperStatus, 9); got != "9" {
		t.Errorf("EnumLabel(9) = %q, want %q", got, "9")
	}
	if got := EnumLabel(nil, 4); got != "4" {
		t.Errorf("EnumLabel with no enumeration = %q, want %q", got, "4")
	}
	if got := EnumLabel(ifOperStatus, -1); got != "-1" {
		t.Errorf("EnumLabel(-1) = %q, want %q", got, "-1")
	}
}

// TestFormatValueAcceptsEveryIntegerWidth covers the conversion in front of
// every numeric rendering. gosnmp hands back a different Go type per ASN.1
// type — uint for Gauge32, uint64 for Counter64, int for Integer — and a width
// that falls through renders as Go's default formatting instead of a number.
func TestFormatValueAcceptsEveryIntegerWidth(t *testing.T) {
	meta := &OIDMetadata{Name: "ifSpeed", Type: "Gauge32"}

	values := map[string]any{
		"int":     int(42),
		"int8":    int8(42),
		"int16":   int16(42),
		"int32":   int32(42),
		"int64":   int64(42),
		"uint":    uint(42),
		"uint8":   uint8(42),
		"uint16":  uint16(42),
		"uint32":  uint32(42),
		"uint64":  uint64(42),
		"float64": float64(42),
	}

	for name, value := range values {
		if got := FormatValue(meta, value); got != "42" {
			t.Errorf("FormatValue(%s) = %q, want %q", name, got, "42")
		}
	}
}

// TestFormatValueOnCounterBeyondInt64 covers a Counter64 past 2^63. The
// conversion refuses it rather than wrapping to a negative, and the formatter
// falls back to Go's own rendering so the true magnitude still reaches the
// display instead of a plausible-looking negative reading.
func TestFormatValueOnCounterBeyondInt64(t *testing.T) {
	meta := &OIDMetadata{Name: "ifHCInOctets", Type: "Counter64"}

	got := FormatValue(meta, uint64(math.MaxInt64)+1)

	if strings.HasPrefix(got, "-") {
		t.Errorf("FormatValue = %q; a large counter wrapped to a negative value", got)
	}
	if got != "9223372036854775808" {
		t.Errorf("FormatValue = %q, want the value unchanged", got)
	}
}

// TestFormatValueOnEnumWithOversizedValue checks the same guard on the
// enumeration path: a value that will not fit an int64 must not be looked up as
// though it had wrapped into the enumeration's range.
func TestFormatValueOnEnumWithOversizedValue(t *testing.T) {
	meta := &OIDMetadata{
		Name:       "ifOperStatus",
		Type:       "Integer",
		EnumValues: map[int64]string{1: "up", 2: "down"},
	}

	got := FormatValue(meta, uint64(math.MaxInt64)+1)

	if strings.Contains(got, "up") || strings.Contains(got, "down") {
		t.Errorf("FormatValue = %q; an out-of-range value was matched against the enumeration", got)
	}
}

// TestFormatValueOnUnconvertibleValue covers a value of a type neither
// conversion recognizes. The contract is that this path always yields something
// printable, because it sits in front of a table where one bad cell must not
// take out the good ones.
func TestFormatValueOnUnconvertibleValue(t *testing.T) {
	meta := &OIDMetadata{Name: "weird", Type: "Opaque"}

	got := FormatValue(meta, struct{ A int }{A: 1})
	if got == "" {
		t.Error("an unrecognized value rendered as empty rather than as something printable")
	}
}

func TestFormatValueWithoutMetadata(t *testing.T) {
	if got := FormatValue(nil, 42); got != "42" {
		t.Errorf("FormatValue(nil meta) = %q, want %q", got, "42")
	}
	if got := FormatValue(nil, nil); got != "" {
		t.Errorf("FormatValue(nil, nil) = %q, want empty", got)
	}
}

// TestFormatValueDateAndTimeFallsBackWhenMalformed covers a device that
// declares DateAndTime but returns something that is not one. The value must
// still be shown — a technician needs to see what the device actually sent —
// rather than swallowed.
func TestFormatValueDateAndTimeFallsBackWhenMalformed(t *testing.T) {
	meta := &OIDMetadata{Name: "hrSystemDate", Type: "DateAndTime"}

	got := FormatValue(meta, []byte{0x07, 0xe8})
	if got == "" {
		t.Error("a malformed DateAndTime rendered as empty")
	}
	if strings.Contains(got, "T") && strings.Contains(got, "Z") {
		t.Errorf("FormatValue = %q; a malformed value was rendered as a valid timestamp", got)
	}
}

// TestFormatValueDateAndTimeOutOfRangeFields covers the range check inside the
// conversion: an agent sending month 13 must not produce a timestamp that Go's
// time package silently normalizes into the next year.
func TestFormatValueDateAndTimeOutOfRangeFields(t *testing.T) {
	for _, octets := range [][]byte{
		{0x07, 0xe8, 13, 1, 0, 0, 0, 0}, // month 13
		{0x07, 0xe8, 0, 1, 0, 0, 0, 0},  // month 0
		{0x07, 0xe8, 1, 0, 0, 0, 0, 0},  // day 0
		{0x07, 0xe8, 1, 32, 0, 0, 0, 0}, // day 32
	} {
		if _, err := FormatDateAndTime(octets); err == nil {
			t.Errorf("FormatDateAndTime(%v) accepted an out-of-range field", octets)
		}
	}
}

// TestFormatDateAndTimeWithNegativeOffset covers the 11-octet form, whose sign
// octet decides whether the rendered timestamp is ahead of or behind UTC.
func TestFormatDateAndTimeWithNegativeOffset(t *testing.T) {
	west := []byte{0x07, 0xe8, 3, 1, 9, 30, 0, 0, '-', 5, 0}
	east := []byte{0x07, 0xe8, 3, 1, 9, 30, 0, 0, '+', 5, 30}

	westFormatted, err := FormatDateAndTime(west)
	if err != nil {
		t.Fatalf("FormatDateAndTime(west): %v", err)
	}
	if !strings.HasSuffix(westFormatted, "-05:00") {
		t.Errorf("got %q, want a -05:00 offset", westFormatted)
	}

	eastFormatted, err := FormatDateAndTime(east)
	if err != nil {
		t.Fatalf("FormatDateAndTime(east): %v", err)
	}
	if !strings.HasSuffix(eastFormatted, "+05:30") {
		t.Errorf("got %q, want a +05:30 offset", eastFormatted)
	}
}

// TestFormatValueOnEmptyOctetString covers the common case of a device that
// implements a string object but has nothing configured in it.
func TestFormatValueOnEmptyOctetString(t *testing.T) {
	meta := &OIDMetadata{Name: "sysLocation", Type: "DisplayString"}

	if got := FormatValue(meta, []byte{}); got != "" {
		t.Errorf("FormatValue on an empty octet string = %q, want empty", got)
	}
	if got := FormatValue(meta, ""); got != "" {
		t.Errorf("FormatValue on an empty Go string = %q, want empty", got)
	}
}

// TestFormatValueUsesIntegerDisplayHint covers the integer arm of the hint
// lookup: a "d-2" hint turns a raw hundredths reading into the decimal value a
// user expects to read.
func TestFormatValueUsesIntegerDisplayHint(t *testing.T) {
	meta := &OIDMetadata{Name: "temperature", Type: "Integer32", DisplayHint: "d-2"}

	if got := FormatValue(meta, 2350); got != "23.50" {
		t.Errorf("FormatValue = %q, want %q", got, "23.50")
	}
}

// TestCachedHintReusesAParse checks the memoization on the per-poll display
// path, including the negative case: an unparseable hint must be remembered as
// unparseable rather than re-parsed for every value of every row.
func TestCachedHintReusesAParse(t *testing.T) {
	const good = "1x:"
	first := cachedHint(good, false)
	second := cachedHint(good, false)

	if first == nil {
		t.Fatal("a valid hint did not parse")
	}
	if first != second {
		t.Error("a repeated hint was parsed again instead of reused")
	}

	const bad = "not-a-hint"
	if cachedHint(bad, false) != nil {
		t.Error("an invalid hint parsed successfully")
	}
	if cachedHint(bad, false) != nil {
		t.Error("an invalid hint parsed successfully on the second lookup")
	}

	// The two grammars are keyed separately: "d" means decimal-with-no-scaling
	// for an integer and a one-octet decimal run for an octet string.
	if cachedHint("d", true) == nil {
		t.Error("the integer grammar rejected \"d\"")
	}
}

func TestFormatValueWithUnitsOnUnitlessObject(t *testing.T) {
	meta := &OIDMetadata{Name: "ifIndex", Type: "Integer32"}

	if got := FormatValueWithUnits(meta, 3); got != "3" {
		t.Errorf("FormatValueWithUnits = %q, want %q", got, "3")
	}
	if got := FormatValueWithUnits(nil, 3); got != "3" {
		t.Errorf("FormatValueWithUnits(nil meta) = %q, want %q", got, "3")
	}
	// An empty rendering must not gain a dangling unit.
	withUnits := &OIDMetadata{Name: "sysLocation", Type: "DisplayString", Units: "octets"}
	if got := FormatValueWithUnits(withUnits, []byte{}); got != "" {
		t.Errorf("FormatValueWithUnits on an empty value = %q, want empty", got)
	}
}
