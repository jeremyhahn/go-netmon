package mib

import (
	"strings"
	"testing"
)

func TestFormatValue_Enum(t *testing.T) {
	operStatus := &OIDMetadata{
		Name: "ifOperStatus", Type: "INTEGER",
		EnumValues: map[int64]string{
			1: "up", 2: "down", 3: "testing", 4: "unknown",
			5: "dormant", 6: "notPresent", 7: "lowerLayerDown",
		},
	}

	tests := []struct {
		name  string
		value any
		want  string
	}{
		{"up", 1, "up(1)"},
		{"down", 2, "down(2)"},
		{"lowerLayerDown", int64(7), "lowerLayerDown(7)"},
		{"unsigned input", uint32(1), "up(1)"},
		// A value the MIB does not define is a real condition; surface it
		// rather than printing a bare number that looks valid.
		{"out of range", 99, "unknown(99)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatValue(operStatus, tt.value); got != tt.want {
				t.Errorf("FormatValue = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatValue_EnumWithNonNumericValue(t *testing.T) {
	meta := &OIDMetadata{
		Name: "ifOperStatus", Type: "INTEGER",
		EnumValues: map[int64]string{1: "up"},
	}

	// A string where an enum is expected must not panic or claim "up".
	got := FormatValue(meta, "not-a-number")
	if strings.Contains(got, "up") {
		t.Errorf("FormatValue = %q, must not resolve a non-numeric value to an enum label", got)
	}
}

func TestFormatValue_TimeTicks(t *testing.T) {
	meta := &OIDMetadata{Name: "sysUpTime", Type: "TimeTicks"}

	tests := []struct {
		name  string
		ticks int64
		want  string
	}{
		{"zero", 0, "0s"},
		{"one second", 100, "1s"},
		{"one minute", 6000, "1m 0s"},
		{"one hour", 360000, "1h 0m 0s"},
		{"one day", 8640000, "1d 0h 0m 0s"},
		{"realistic uptime", 109427307, "12d 15h 57m 53s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatValue(meta, tt.ticks); got != tt.want {
				t.Errorf("FormatValue(%d) = %q, want %q", tt.ticks, got, tt.want)
			}
		})
	}
}

func TestFormatValue_TimeTicksNegative(t *testing.T) {
	meta := &OIDMetadata{Name: "sysUpTime", Type: "TimeTicks"}

	// Negative ticks are nonsensical; render the raw number rather than a
	// duration computed from a wrapped value.
	if got := FormatValue(meta, int64(-100)); got != "-100" {
		t.Errorf("FormatValue(-100) = %q, want %q", got, "-100")
	}
}

func TestFormatValue_MacAddressDisplayHint(t *testing.T) {
	meta := &OIDMetadata{Name: "ifPhysAddress", Type: "PhysAddress", DisplayHint: "1x:"}

	got := FormatValue(meta, []byte{0x00, 0x1b, 0x21, 0x3c, 0x4d, 0x5e})
	if want := "00:1b:21:3c:4d:5e"; got != want {
		t.Errorf("FormatValue = %q, want %q", got, want)
	}
}

func TestFormatValue_MacAddressWithoutDisplayHint(t *testing.T) {
	// Some agents return a MAC with no usable hint; the type name still tells
	// us not to print raw bytes as text.
	meta := &OIDMetadata{Name: "ifPhysAddress", Type: "MacAddress"}

	got := FormatValue(meta, []byte{0xde, 0xad, 0xbe, 0xef, 0x00, 0x01})
	if want := "de:ad:be:ef:00:01"; got != want {
		t.Errorf("FormatValue = %q, want %q", got, want)
	}
}

func TestFormatValue_DisplayString(t *testing.T) {
	meta := &OIDMetadata{Name: "ifDescr", Type: "DisplayString", DisplayHint: "255a"}

	got := FormatValue(meta, []byte("GigabitEthernet0/0/0"))
	if want := "GigabitEthernet0/0/0"; got != want {
		t.Errorf("FormatValue = %q, want %q", got, want)
	}
}

func TestFormatValue_BinaryOctetStringFallsBackToHex(t *testing.T) {
	// No hint, non-printable content: hex is the only honest rendering.
	meta := &OIDMetadata{Name: "someOpaque", Type: "OctetString"}

	got := FormatValue(meta, []byte{0x00, 0xff, 0x80, 0x01})
	if want := "00:ff:80:01"; got != want {
		t.Errorf("FormatValue = %q, want %q", got, want)
	}
}

func TestFormatValue_IPAddress(t *testing.T) {
	meta := &OIDMetadata{Name: "ipAdEntAddr", Type: "IpAddress"}

	got := FormatValue(meta, []byte{198, 51, 100, 252})
	if want := "198.51.100.252"; got != want {
		t.Errorf("FormatValue = %q, want %q", got, want)
	}
}

func TestFormatValue_IPAddressWrongLength(t *testing.T) {
	meta := &OIDMetadata{Name: "ipAdEntAddr", Type: "IpAddress"}

	// Three octets is not an IPv4 address; it must not be rendered as one.
	got := FormatValue(meta, []byte{198, 51, 100})
	if strings.Count(got, ".") == 3 {
		t.Errorf("FormatValue = %q, must not render a 3-octet value as IPv4", got)
	}
}

func TestFormatValue_DateAndTime(t *testing.T) {
	meta := &OIDMetadata{Name: "hrSystemDate", Type: "DateAndTime"}

	// 2026-08-08 14:30:45.0 UTC+0
	octets := []byte{0x07, 0xEA, 8, 8, 14, 30, 45, 0, '+', 0, 0}
	got := FormatValue(meta, octets)
	if !strings.HasPrefix(got, "2026-08-08T14:30:45") {
		t.Errorf("FormatValue = %q, want an RFC 3339 timestamp for 2026-08-08T14:30:45", got)
	}
}

func TestFormatDateAndTime_BadInput(t *testing.T) {
	tests := []struct {
		name   string
		octets []byte
	}{
		{"too short", []byte{0x07, 0xEA, 8}},
		{"wrong length", []byte{0x07, 0xEA, 8, 8, 14, 30, 45, 0, '+'}},
		{"month out of range", []byte{0x07, 0xEA, 13, 8, 14, 30, 45, 0}},
		{"day out of range", []byte{0x07, 0xEA, 8, 0, 14, 30, 45, 0}},
		{"empty", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := FormatDateAndTime(tt.octets); err == nil {
				t.Error("expected an error, got nil")
			}
		})
	}
}

func TestFormatValue_NilAndEmpty(t *testing.T) {
	meta := &OIDMetadata{Name: "ifDescr", Type: "DisplayString"}

	if got := FormatValue(meta, nil); got != "" {
		t.Errorf("FormatValue(nil) = %q, want empty", got)
	}
	if got := FormatValue(nil, 42); got != "42" {
		t.Errorf("FormatValue with nil metadata = %q, want %q", got, "42")
	}
	if got := FormatValue(nil, nil); got != "" {
		t.Errorf("FormatValue(nil, nil) = %q, want empty", got)
	}
}

func TestFormatValue_PlainCounter(t *testing.T) {
	meta := &OIDMetadata{Name: "ifInOctets", Type: "Counter32"}

	if got := FormatValue(meta, uint32(4294967295)); got != "4294967295" {
		t.Errorf("FormatValue = %q, want %q", got, "4294967295")
	}
}

func TestFormatValueWithUnits(t *testing.T) {
	tests := []struct {
		name string
		meta *OIDMetadata
		val  any
		want string
	}{
		{
			"units appended",
			&OIDMetadata{Name: "tcpRtoMin", Type: "Integer32", Units: "milliseconds"},
			200, "200 milliseconds",
		},
		{
			"no units declared",
			&OIDMetadata{Name: "ifInOctets", Type: "Counter32"},
			1500, "1500",
		},
		{
			// A duration already reads as time; "12d 15h 57m 53s centi-seconds"
			// would be worse than useless.
			"timeticks keep their own unit",
			&OIDMetadata{Name: "sysUpTime", Type: "TimeTicks", Units: "centi-seconds"},
			int64(109427307), "12d 15h 57m 53s",
		},
		{
			"enums keep their label",
			&OIDMetadata{
				Name: "ifOperStatus", Type: "INTEGER", Units: "state",
				EnumValues: map[int64]string{1: "up"},
			},
			1, "up(1)",
		},
		{
			"nil metadata",
			nil, 42, "42",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatValueWithUnits(tt.meta, tt.val); got != tt.want {
				t.Errorf("FormatValueWithUnits = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatValue_UnparseableDisplayHintFallsBack(t *testing.T) {
	// A malformed hint must degrade to a sensible rendering, not an error or
	// an empty cell.
	meta := &OIDMetadata{Name: "weird", Type: "OctetString", DisplayHint: "!!!"}

	got := FormatValue(meta, []byte("hello"))
	if got != "hello" {
		t.Errorf("FormatValue = %q, want %q", got, "hello")
	}
}
