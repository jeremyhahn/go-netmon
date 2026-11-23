package mib

import "testing"

// TestFormatOctets covers the text-or-hex decision made for an OCTET STRING
// that carries no DISPLAY-HINT, which is the only information available for
// the binary objects vendors define without one.
func TestFormatOctets(t *testing.T) {
	tests := []struct {
		name   string
		octets []byte
		want   string
	}{
		{
			name:   "plain ascii is text",
			octets: []byte("GigabitEthernet0/0/0"),
			want:   "GigabitEthernet0/0/0",
		},
		{
			name:   "trailing NUL padding is dropped",
			octets: []byte("eth0\x00\x00\x00"),
			want:   "eth0",
		},
		{
			name:   "embedded NUL makes the value binary",
			octets: []byte("ab\x00cd"),
			want:   "61:62:00:63:64",
		},
		{
			name:   "all NUL renders as hex, never as empty",
			octets: []byte{0x00, 0x00},
			want:   "00:00",
		},
		{
			name:   "cbgpPeer2LastError hold timer expired",
			octets: []byte{0x04, 0x00},
			want:   "04:00",
		},
		{
			name:   "cbgpPeer2LastError notification with subcode",
			octets: []byte{0x06, 0x02},
			want:   "06:02",
		},
		{
			name:   "high bit octets are binary",
			octets: []byte{0x80, 0x00, 0x2a, 0xff},
			want:   "80:00:2a:ff",
		},
		{
			name:   "whitespace controls stay text so multi line sysDescr survives",
			octets: []byte("Cisco IOS\r\n\tVersion 17.3"),
			want:   "Cisco IOS\r\n\tVersion 17.3",
		},
		{
			name:   "a lone NUL is one binary octet, not an empty string",
			octets: []byte{0x00},
			want:   "00",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatOctets(tt.octets); got != tt.want {
				t.Errorf("FormatOctets(%v) = %q, want %q", tt.octets, got, tt.want)
			}
		})
	}
}

// TestFormatOctetsOnNoOctets checks the degenerate inputs: a value that really
// is empty on the wire, and a nil slice. Both must render as empty, because
// there is nothing to show and inventing "00" would be a lie.
func TestFormatOctetsOnNoOctets(t *testing.T) {
	if got := FormatOctets(nil); got != "" {
		t.Errorf("FormatOctets(nil) = %q, want empty", got)
	}
	if got := FormatOctets([]byte{}); got != "" {
		t.Errorf("FormatOctets(empty) = %q, want empty", got)
	}
}

// TestFormatOctetsNeverEmptiesANonEmptyValue is the invariant behind the
// defect: an octet string that arrived with bytes in it must render as
// something, because an empty rendering is indistinguishable from "the agent
// returned no value" and hid a real BGP error code for as long as it stood.
func TestFormatOctetsNeverEmptiesANonEmptyValue(t *testing.T) {
	for length := 1; length <= 8; length++ {
		octets := make([]byte, length)
		if got := FormatOctets(octets); got == "" {
			t.Errorf("%d NUL octets rendered as empty", length)
		}
	}
}

// TestFormatValueBinaryOctetStringWithoutHint checks that the same rule
// reaches values arriving through FormatValue, which is what the CLI, the web
// UI and the exporter all call.
func TestFormatValueBinaryOctetStringWithoutHint(t *testing.T) {
	meta := &OIDMetadata{
		Name: "cbgpPeer2LastError",
		Type: "OCTET STRING",
	}

	tests := []struct {
		name  string
		value any
		want  string
	}{
		{name: "no error", value: []byte{0x00, 0x00}, want: "00:00"},
		{name: "hold timer expired", value: []byte{0x04, 0x00}, want: "04:00"},
		{name: "value arriving as a string", value: "\x04\x00", want: "04:00"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatValue(meta, tt.value); got != tt.want {
				t.Errorf("FormatValue = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestFormatValueDisplayHintStillWins guards the precedence: a declared
// DISPLAY-HINT describes the value better than any content heuristic can, so
// the content rule must not be consulted when one is present.
func TestFormatValueDisplayHintStillWins(t *testing.T) {
	tests := []struct {
		name  string
		meta  *OIDMetadata
		value []byte
		want  string
	}{
		{
			name:  "hex hint renders NULs as hex pairs, not as empty text",
			meta:  &OIDMetadata{Name: "snmpEngineID", Type: "OCTET STRING", DisplayHint: "1x:"},
			value: []byte{0x00, 0x00},
			want:  "00:00",
		},
		{
			// Printable octets the content rule would call text: only a
			// declared hint can say they are not, so this is what proves the
			// hint is consulted first rather than merely agreeing.
			name:  "hex hint overrides the content rule on printable octets",
			meta:  &OIDMetadata{Name: "vendorBlob", Type: "OCTET STRING", DisplayHint: "1x:"},
			value: []byte("eth0"),
			want:  "65:74:68:30",
		},
		{
			name:  "MAC hint wins over the content rule",
			meta:  &OIDMetadata{Name: "ifPhysAddress", Type: "OCTET STRING", DisplayHint: "1x:"},
			value: []byte{0x00, 0x1b, 0x21, 0x3c, 0x4d, 0x5e},
			want:  "00:1b:21:3c:4d:5e",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatValue(tt.meta, tt.value); got != tt.want {
				t.Errorf("FormatValue = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestFormatValuePhysAddressWithoutHint checks that a MAC-shaped type with no
// usable hint still renders as colon hex, including the all-zero address that
// the content rule alone would have to guess about.
func TestFormatValuePhysAddressWithoutHint(t *testing.T) {
	meta := &OIDMetadata{Name: "ifPhysAddress", Type: "PhysAddress"}

	if got := FormatValue(meta, []byte{0x00, 0x1b, 0x21, 0x3c, 0x4d, 0x5e}); got != "00:1b:21:3c:4d:5e" {
		t.Errorf("MAC address = %q, want %q", got, "00:1b:21:3c:4d:5e")
	}
	if got := FormatValue(meta, []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00}); got != "00:00:00:00:00:00" {
		t.Errorf("all zero MAC = %q, want %q", got, "00:00:00:00:00:00")
	}
}

// TestFormatValueEmptyOctetStringStaysEmpty pins the one case where empty is
// the honest answer: nothing arrived.
func TestFormatValueEmptyOctetStringStaysEmpty(t *testing.T) {
	meta := &OIDMetadata{Name: "ifAlias", Type: "DisplayString"}

	if got := FormatValue(meta, []byte{}); got != "" {
		t.Errorf("empty octet string = %q, want empty", got)
	}
	if got := FormatValue(meta, ""); got != "" {
		t.Errorf("empty string = %q, want empty", got)
	}
}

// TestTrimNULPadding covers the padding rule on its own: only the tail is
// padding, and a value that is nothing but padding trims away to nothing,
// which is what tells FormatOctets to fall through to hex.
func TestTrimNULPadding(t *testing.T) {
	tests := []struct {
		name   string
		octets []byte
		want   string
	}{
		{name: "no padding", octets: []byte("eth0"), want: "eth0"},
		{name: "one pad octet", octets: []byte("eth0\x00"), want: "eth0"},
		{name: "many pad octets", octets: []byte("eth0\x00\x00\x00"), want: "eth0"},
		{name: "leading NUL is content", octets: []byte("\x00eth0"), want: "\x00eth0"},
		{name: "all padding", octets: []byte{0x00, 0x00}, want: ""},
		{name: "empty", octets: nil, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(trimNULPadding(tt.octets)); got != tt.want {
				t.Errorf("trimNULPadding(%v) = %q, want %q", tt.octets, got, tt.want)
			}
		})
	}
}

// TestIsText covers the text predicate directly, including the boundaries of
// the printable ASCII range that decide every borderline value.
func TestIsText(t *testing.T) {
	tests := []struct {
		name   string
		octets []byte
		want   bool
	}{
		{name: "ascii", octets: []byte("abc XYZ 019 /-_."), want: true},
		{name: "space is the low boundary", octets: []byte{0x20}, want: true},
		{name: "tilde is the high boundary", octets: []byte{0x7e}, want: true},
		{name: "whitespace controls", octets: []byte("a\tb\nc\rd"), want: true},
		{name: "empty is vacuously text", octets: nil, want: true},
		{name: "NUL is not text", octets: []byte{0x00}, want: false},
		{name: "below space is not text", octets: []byte{0x1f}, want: false},
		{name: "DEL is not text", octets: []byte{0x7f}, want: false},
		{name: "high bit is not text", octets: []byte{0x80}, want: false},
		{name: "one bad octet condemns the value", octets: []byte("eth0\x01"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isText(tt.octets); got != tt.want {
				t.Errorf("isText(%v) = %v, want %v", tt.octets, got, tt.want)
			}
		})
	}
}

// TestFormatValueWithoutMetadataRendersOctets covers the object no loaded MIB
// describes. Its content is the only evidence available, so the same content
// rule applies -- and the rendering must not depend on whether gosnmp handed
// back a []byte or a string, which once decided between "[4 0]" and two raw
// control characters for one value.
func TestFormatValueWithoutMetadataRendersOctets(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{name: "binary bytes", value: []byte{0x04, 0x00}, want: "04:00"},
		{name: "binary string", value: "\x04\x00", want: "04:00"},
		{name: "text bytes", value: []byte("eth0"), want: "eth0"},
		{name: "text string", value: "eth0", want: "eth0"},
		{name: "padded text", value: []byte("eth0\x00"), want: "eth0"},
		{name: "empty", value: []byte{}, want: ""},
		{name: "not octets at all", value: 42, want: "42"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatValue(nil, tt.value); got != tt.want {
				t.Errorf("FormatValue(nil, %v) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}
