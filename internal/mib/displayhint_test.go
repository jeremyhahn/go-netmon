package mib

import "testing"

func TestParseDisplayHint_Integer(t *testing.T) {
	tests := []struct {
		hint         string
		wantFormat   byte
		wantDecimals int
	}{
		{"d", 'd', 0},
		{"d-2", 'd', 2},
		{"d-9", 'd', 9},
		{"x", 'x', 0},
		{"o", 'o', 0},
		{"b", 'b', 0},
	}

	for _, tt := range tests {
		t.Run(tt.hint, func(t *testing.T) {
			got, err := ParseDisplayHint(tt.hint, true)
			if err != nil {
				t.Fatalf("ParseDisplayHint(%q): %v", tt.hint, err)
			}
			if got.IntFormat != tt.wantFormat {
				t.Errorf("IntFormat = %q, want %q", got.IntFormat, tt.wantFormat)
			}
			if got.IntDecimals != tt.wantDecimals {
				t.Errorf("IntDecimals = %d, want %d", got.IntDecimals, tt.wantDecimals)
			}
		})
	}
}

func TestParseDisplayHint_IntegerInvalid(t *testing.T) {
	tests := []string{"", "z", "d+2", "d-", "d-abc", "xx", "1x"}

	for _, hint := range tests {
		t.Run(hint, func(t *testing.T) {
			if _, err := ParseDisplayHint(hint, true); err == nil {
				t.Errorf("ParseDisplayHint(%q) succeeded, want an error", hint)
			}
		})
	}
}

func TestParseDisplayHint_Octet(t *testing.T) {
	tests := []struct {
		name      string
		hint      string
		wantSpecs []OctetSpec
	}{
		{
			"display string",
			"255a",
			[]OctetSpec{{Length: 255, Format: 'a'}},
		},
		{
			"mac address",
			"1x:",
			[]OctetSpec{{Length: 1, Format: 'x', Separator: ':'}},
		},
		{
			"ipv4 dotted decimal",
			"1d.",
			[]OctetSpec{{Length: 1, Format: 'd', Separator: '.'}},
		},
		{
			"date and time",
			"2d-1d-1d,1d:1d:1d.1d,1a1d:1d",
			[]OctetSpec{
				{Length: 2, Format: 'd', Separator: '-'},
				{Length: 1, Format: 'd', Separator: '-'},
				{Length: 1, Format: 'd', Separator: ','},
				{Length: 1, Format: 'd', Separator: ':'},
				{Length: 1, Format: 'd', Separator: ':'},
				{Length: 1, Format: 'd', Separator: '.'},
				{Length: 1, Format: 'd', Separator: ','},
				{Length: 1, Format: 'a'},
				{Length: 1, Format: 'd', Separator: ':'},
				{Length: 1, Format: 'd'},
			},
		},
		{
			"repeat indicator",
			"*1x:",
			[]OctetSpec{{Repeat: true, Length: 1, Format: 'x', Separator: ':'}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseDisplayHint(tt.hint, false)
			if err != nil {
				t.Fatalf("ParseDisplayHint(%q): %v", tt.hint, err)
			}
			if len(got.Octets) != len(tt.wantSpecs) {
				t.Fatalf("got %d specifiers, want %d: %+v", len(got.Octets), len(tt.wantSpecs), got.Octets)
			}
			for i, want := range tt.wantSpecs {
				if got.Octets[i] != want {
					t.Errorf("specifier %d = %+v, want %+v", i, got.Octets[i], want)
				}
			}
		})
	}
}

func TestParseDisplayHint_OctetInvalid(t *testing.T) {
	tests := []string{"", "a", "0x", "1z", "255", "*"}

	for _, hint := range tests {
		t.Run(hint, func(t *testing.T) {
			if _, err := ParseDisplayHint(hint, false); err == nil {
				t.Errorf("ParseDisplayHint(%q) succeeded, want an error", hint)
			}
		})
	}
}

func TestDisplayHint_FormatInteger(t *testing.T) {
	tests := []struct {
		name  string
		hint  string
		value int64
		want  string
	}{
		{"decimal", "d", 1234, "1234"},
		{"two implied decimals", "d-2", 1234, "12.34"},
		{"three implied decimals", "d-3", 5, "0.005"},
		{"implied decimals at boundary", "d-2", 7, "0.07"},
		{"negative scaled", "d-2", -1234, "-12.34"},
		{"hex", "x", 255, "ff"},
		{"octal", "o", 8, "10"},
		{"binary", "b", 5, "101"},
		{"negative hex keeps sign", "x", -255, "-ff"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hint, err := ParseDisplayHint(tt.hint, true)
			if err != nil {
				t.Fatalf("ParseDisplayHint: %v", err)
			}
			if got := hint.FormatInteger(tt.value); got != tt.want {
				t.Errorf("FormatInteger(%d) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestDisplayHint_FormatOctets(t *testing.T) {
	tests := []struct {
		name  string
		hint  string
		value []byte
		want  string
	}{
		{"mac address", "1x:", []byte{0x00, 0x1b, 0x21, 0x3c, 0x4d, 0x5e}, "00:1b:21:3c:4d:5e"},
		{"single octet mac", "1x:", []byte{0xde}, "de"},
		{"display string", "255a", []byte("Gi0/0/0"), "Gi0/0/0"},
		{"dotted decimal", "1d.", []byte{198, 51, 100, 252}, "198.51.100.252"},
		{"empty value", "1x:", nil, ""},
		{"two-octet decimal", "2d", []byte{0x07, 0xEA}, "2026"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hint, err := ParseDisplayHint(tt.hint, false)
			if err != nil {
				t.Fatalf("ParseDisplayHint: %v", err)
			}
			if got := hint.FormatOctets(tt.value); got != tt.want {
				t.Errorf("FormatOctets = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestDisplayHint_FormatOctetsOversizedValue verifies that a value longer than
// the specifiers describe does not panic or truncate silently.
func TestDisplayHint_FormatOctetsOversizedValue(t *testing.T) {
	hint, err := ParseDisplayHint("2d-1d-1d", false)
	if err != nil {
		t.Fatalf("ParseDisplayHint: %v", err)
	}

	// Eight octets against four specifiers' worth of length.
	got := hint.FormatOctets([]byte{0x07, 0xEA, 8, 8, 14, 30, 45, 0})
	if got == "" {
		t.Error("FormatOctets returned empty for an oversized value")
	}
}

func TestDisplayHint_IsOctetHint(t *testing.T) {
	octet, err := ParseDisplayHint("1x:", false)
	if err != nil {
		t.Fatalf("ParseDisplayHint: %v", err)
	}
	if !octet.IsOctetHint() {
		t.Error("IsOctetHint = false for an octet hint")
	}

	integer, err := ParseDisplayHint("d", true)
	if err != nil {
		t.Fatalf("ParseDisplayHint: %v", err)
	}
	if integer.IsOctetHint() {
		t.Error("IsOctetHint = true for an integer hint")
	}
}
