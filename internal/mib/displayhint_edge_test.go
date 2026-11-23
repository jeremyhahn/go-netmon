package mib

import (
	"errors"
	"testing"
)

// TestParseOctetHintRejectsTruncatedSpecifier covers the specifier that names a
// length but never says how to render it. Accepting it would leave Format at
// zero and silently render the octets raw.
func TestParseOctetHintRejectsTruncatedSpecifier(t *testing.T) {
	for _, hint := range []string{"1", "1x:2", "*3"} {
		t.Run(hint, func(t *testing.T) {
			_, err := ParseDisplayHint(hint, false)
			if err == nil {
				t.Fatalf("ParseDisplayHint(%q) succeeded, want an error", hint)
			}

			var hintErr *DisplayHintError
			if !errors.As(err, &hintErr) {
				t.Fatalf("got %T, want *DisplayHintError", err)
			}
			if hintErr.Hint != hint {
				t.Errorf("Hint = %q, want %q", hintErr.Hint, hint)
			}
		})
	}
}

// TestFormatIntegerWithUnsetFormat covers a hint that carries no integer
// format, which is what an octet-string hint looks like if it reaches the
// integer path. Rendering the plain decimal is the only safe answer.
func TestFormatIntegerWithUnsetFormat(t *testing.T) {
	if got := (&DisplayHint{}).FormatInteger(1234); got != "1234" {
		t.Errorf("FormatInteger = %q, want %q", got, "1234")
	}

	octetHint, err := ParseDisplayHint("1x:", false)
	if err != nil {
		t.Fatalf("ParseDisplayHint: %v", err)
	}
	if got := octetHint.FormatInteger(255); got != "255" {
		t.Errorf("FormatInteger on an octet hint = %q, want the plain decimal %q", got, "255")
	}
}

// TestFormatOctetsWithNoSpecifiers is the mirror case: an integer hint asked to
// render octets must hand back the bytes rather than an empty string.
func TestFormatOctetsWithNoSpecifiers(t *testing.T) {
	integerHint, err := ParseDisplayHint("d", true)
	if err != nil {
		t.Fatalf("ParseDisplayHint: %v", err)
	}

	if got := integerHint.FormatOctets([]byte("abc")); got != "abc" {
		t.Errorf("FormatOctets on an integer hint = %q, want %q", got, "abc")
	}
}

// A repeat count larger than the octets actually present renders a dangling
// separator — "*1x:" over {9, 0xaa} yields "aa:" rather than "aa" — because the
// separator is emitted from the declared count rather than from whether another
// run follows. That case is deliberately not asserted here: it is a defect
// reported to the maintainer, not behavior to pin.
//
// TestFormatOctetsWithRepeatCount covers the '*' form, where the first octet of
// the value says how many times the specifier repeats. It is how variable
// length addresses are rendered, and getting the cursor wrong shifts every
// subsequent field.
func TestFormatOctetsWithRepeatCount(t *testing.T) {
	tests := []struct {
		name  string
		hint  string
		value []byte
		want  string
	}{
		{
			name:  "three one-octet hex runs",
			hint:  "*1x:",
			value: []byte{3, 0xaa, 0xbb, 0xcc},
			want:  "aa:bb:cc",
		},
		{
			name:  "terminator after the final repeat",
			hint:  "*1x:.",
			value: []byte{2, 0xaa, 0xbb},
			want:  "aa:bb.",
		},
		{
			name:  "count of zero consumes nothing",
			hint:  "*1x:",
			value: []byte{0},
			want:  "",
		},
		{
			// A repeat count larger than the octets actually present used to
			// leave a dangling separator ("aa:"), because the separator was
			// written on the repeat counter alone. These values reach
			// Prometheus as label values, so the stray byte was visible.
			name:  "repeat count exceeds the octets present",
			hint:  "*1x:",
			value: []byte{9, 0xaa},
			want:  "aa",
		},
		{
			name:  "repeat count exceeds a multi-octet run",
			hint:  "*2x:",
			value: []byte{4, 0xaa, 0xbb, 0xcc},
			want:  "aabb:cc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hint, err := ParseDisplayHint(tt.hint, false)
			if err != nil {
				t.Fatalf("ParseDisplayHint(%q): %v", tt.hint, err)
			}
			if got := hint.FormatOctets(tt.value); got != tt.want {
				t.Errorf("FormatOctets(%v) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

// TestFormatOctetRunsInEveryBase covers the numeric renderings. Each reads a
// run of octets as one big-endian number, so a multi-octet run must not be
// rendered as its first octet alone.
func TestFormatOctetRunsInEveryBase(t *testing.T) {
	tests := []struct {
		name  string
		hint  string
		value []byte
		want  string
	}{
		{name: "octal", hint: "1o", value: []byte{8}, want: "10"},
		{name: "binary", hint: "1b", value: []byte{5}, want: "101"},
		{name: "decimal", hint: "1d", value: []byte{200}, want: "200"},
		{name: "two-octet decimal is big-endian", hint: "2d", value: []byte{0x01, 0x00}, want: "256"},
		{name: "two-octet octal is big-endian", hint: "2o", value: []byte{0x01, 0x00}, want: "400"},
		{name: "ascii", hint: "4a", value: []byte("eth0"), want: "eth0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hint, err := ParseDisplayHint(tt.hint, false)
			if err != nil {
				t.Fatalf("ParseDisplayHint(%q): %v", tt.hint, err)
			}
			if got := hint.FormatOctets(tt.value); got != tt.want {
				t.Errorf("FormatOctets(%v) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

// TestFormatOctetRunWithUnknownFormat covers a specifier the parser would never
// produce but that a caller constructing a DisplayHint by hand could. The run
// falls back to the raw bytes rather than rendering nothing.
func TestFormatOctetRunWithUnknownFormat(t *testing.T) {
	hint := &DisplayHint{Octets: []OctetSpec{{Length: 3, Format: 'z'}}}

	if got := hint.FormatOctets([]byte("abc")); got != "abc" {
		t.Errorf("FormatOctets with an unknown format = %q, want %q", got, "abc")
	}
}

// TestFormatOctetsSeparatorBetweenSpecifiers covers the multi-specifier form
// used by DateAndTime, where each field has its own width and the separators
// between them are what make the value readable.
func TestFormatOctetsSeparatorBetweenSpecifiers(t *testing.T) {
	hint, err := ParseDisplayHint("2d-1d-1d", false)
	if err != nil {
		t.Fatalf("ParseDisplayHint: %v", err)
	}

	// 2024-03-01
	got := hint.FormatOctets([]byte{0x07, 0xe8, 3, 1})
	if got != "2024-3-1" {
		t.Errorf("FormatOctets = %q, want %q", got, "2024-3-1")
	}
}

// TestFormatOctetsOnEmptyValue checks that a hint applied to no octets produces
// no output rather than a lone separator.
func TestFormatOctetsOnEmptyValue(t *testing.T) {
	hint, err := ParseDisplayHint("1x:", false)
	if err != nil {
		t.Fatalf("ParseDisplayHint: %v", err)
	}

	if got := hint.FormatOctets(nil); got != "" {
		t.Errorf("FormatOctets(nil) = %q, want empty", got)
	}
	if got := hint.FormatOctets([]byte{}); got != "" {
		t.Errorf("FormatOctets(empty) = %q, want empty", got)
	}
}
