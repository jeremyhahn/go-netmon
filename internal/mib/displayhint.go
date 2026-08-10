package mib

import (
	"fmt"
	"strconv"
	"strings"
)

// DisplayHint is a parsed DISPLAY-HINT clause from a TEXTUAL-CONVENTION.
//
// RFC 2579 defines two grammars, chosen by the underlying base type:
//
//   - Integer types take a single specifier: "d" (decimal), "d-N" (decimal
//     with N implied fraction digits), "x" (hex), "o" (octal), "b" (binary).
//   - Octet-string types take a sequence of specifiers, each consuming a run
//     of octets and rendering them in one format, optionally followed by a
//     separator character.
//
// A parsed hint is immutable and safe to share between goroutines.
type DisplayHint struct {
	// Integer form.
	IntFormat   byte // 'd', 'x', 'o' or 'b'; zero when this is an octet hint.
	IntDecimals int  // Implied fraction digits for "d-N".

	// Octet-string form.
	Octets []OctetSpec
}

// OctetSpec is one specifier within an octet-string DISPLAY-HINT, such as the
// "1x:" in "1x:1x:1x:1x:1x:1x" or the "2d-" in DateAndTime's hint.
type OctetSpec struct {
	// Repeat means the specifier is preceded by '*': the next octet in the
	// value gives how many times this specifier repeats.
	Repeat bool

	// Length is the number of octets consumed per iteration.
	Length int

	// Format is 'a' (ASCII), 'b' (binary), 'd' (decimal), 'o' (octal) or
	// 'x' (hexadecimal).
	Format byte

	// Separator is emitted between iterations, zero if absent.
	Separator byte

	// Terminator is emitted after the final repeat of a repeating specifier,
	// zero if absent.
	Terminator byte
}

// IsOctetHint reports whether the hint describes an octet string.
func (h *DisplayHint) IsOctetHint() bool {
	return len(h.Octets) > 0
}

// ParseDisplayHint parses a DISPLAY-HINT string. isInteger selects which of the
// two grammars applies, since "d" is valid in both and means different things.
func ParseDisplayHint(hint string, isInteger bool) (*DisplayHint, error) {
	if hint == "" {
		return nil, &DisplayHintError{Hint: hint, Reason: "empty"}
	}
	if isInteger {
		return parseIntegerHint(hint)
	}
	return parseOctetHint(hint)
}

// parseIntegerHint handles the "d", "d-N", "x", "o", "b" forms.
func parseIntegerHint(hint string) (*DisplayHint, error) {
	format := hint[0]
	switch format {
	case 'd':
		decimals := 0
		if rest := hint[1:]; rest != "" {
			if !strings.HasPrefix(rest, "-") {
				return nil, &DisplayHintError{Hint: hint, Reason: "expected '-' after 'd'"}
			}
			n, err := strconv.Atoi(rest[1:])
			if err != nil || n < 0 {
				return nil, &DisplayHintError{Hint: hint, Reason: "invalid decimal count"}
			}
			decimals = n
		}
		return &DisplayHint{IntFormat: 'd', IntDecimals: decimals}, nil
	case 'x', 'o', 'b':
		if len(hint) != 1 {
			return nil, &DisplayHintError{Hint: hint, Reason: "trailing characters"}
		}
		return &DisplayHint{IntFormat: format}, nil
	default:
		return nil, &DisplayHintError{Hint: hint, Reason: "unknown integer format " + string(format)}
	}
}

// parseOctetHint handles the repeating octet-string grammar:
//
//	octet-hint     = *( [ "*" ] length format [ separator [ terminator ] ] )
//	length         = 1*DIGIT
//	format         = "a" / "b" / "d" / "o" / "x"
//	separator      = <any non-alphanumeric character>
//	terminator     = <any non-alphanumeric character>
func parseOctetHint(hint string) (*DisplayHint, error) {
	parsed := &DisplayHint{}

	for i := 0; i < len(hint); {
		spec := OctetSpec{}

		if hint[i] == '*' {
			spec.Repeat = true
			i++
			if i >= len(hint) {
				return nil, &DisplayHintError{Hint: hint, Reason: "'*' at end of hint"}
			}
		}

		start := i
		for i < len(hint) && hint[i] >= '0' && hint[i] <= '9' {
			i++
		}
		if start == i {
			return nil, &DisplayHintError{Hint: hint, Reason: "missing octet length"}
		}
		length, err := strconv.Atoi(hint[start:i])
		if err != nil || length <= 0 {
			return nil, &DisplayHintError{Hint: hint, Reason: "invalid octet length"}
		}
		spec.Length = length

		if i >= len(hint) {
			return nil, &DisplayHintError{Hint: hint, Reason: "missing format character"}
		}
		switch hint[i] {
		case 'a', 'b', 'd', 'o', 'x':
			spec.Format = hint[i]
			i++
		default:
			return nil, &DisplayHintError{Hint: hint, Reason: "unknown format character " + string(hint[i])}
		}

		// An optional separator, then an optional terminator, each a single
		// non-alphanumeric character. A digit or letter here starts the next
		// specifier instead.
		if i < len(hint) && !isAlphanumeric(hint[i]) {
			spec.Separator = hint[i]
			i++
			if spec.Repeat && i < len(hint) && !isAlphanumeric(hint[i]) {
				spec.Terminator = hint[i]
				i++
			}
		}

		parsed.Octets = append(parsed.Octets, spec)
	}

	if len(parsed.Octets) == 0 {
		return nil, &DisplayHintError{Hint: hint, Reason: "no specifiers"}
	}
	return parsed, nil
}

func isAlphanumeric(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// FormatInteger renders an integer according to an integer DISPLAY-HINT.
func (h *DisplayHint) FormatInteger(value int64) string {
	switch h.IntFormat {
	case 'x':
		return formatSignedBase(value, 16)
	case 'o':
		return formatSignedBase(value, 8)
	case 'b':
		return formatSignedBase(value, 2)
	case 'd':
		if h.IntDecimals == 0 {
			return strconv.FormatInt(value, 10)
		}
		return formatScaledDecimal(value, h.IntDecimals)
	default:
		return strconv.FormatInt(value, 10)
	}
}

// formatSignedBase renders value in the given base, keeping a leading minus
// sign rather than emitting a two's-complement representation.
func formatSignedBase(value int64, base int) string {
	if value < 0 {
		return "-" + strconv.FormatInt(-value, base)
	}
	return strconv.FormatInt(value, base)
}

// formatScaledDecimal renders value with an implied decimal point, so that
// DISPLAY-HINT "d-2" turns 1234 into "12.34".
func formatScaledDecimal(value int64, decimals int) string {
	sign := ""
	if value < 0 {
		sign = "-"
		value = -value
	}

	digits := strconv.FormatInt(value, 10)
	if len(digits) <= decimals {
		digits = strings.Repeat("0", decimals-len(digits)+1) + digits
	}

	split := len(digits) - decimals
	return sign + digits[:split] + "." + digits[split:]
}

// FormatOctets renders a byte slice according to an octet-string DISPLAY-HINT.
// Octets beyond what the specifiers consume are rendered by repeating the last
// specifier, which is how RFC 2579 handles values longer than the hint.
func (h *DisplayHint) FormatOctets(value []byte) string {
	if len(h.Octets) == 0 {
		return string(value)
	}

	var out strings.Builder
	pos := 0
	specIndex := 0

	for pos < len(value) {
		spec := h.Octets[specIndex]

		repeats := 1
		if spec.Repeat {
			repeats = int(value[pos])
			pos++
		}

		for r := 0; r < repeats && pos < len(value); r++ {
			end := pos + spec.Length
			if end > len(value) {
				end = len(value)
			}
			out.WriteString(formatOctetRun(value[pos:end], spec.Format))
			pos = end

			// A separator joins two runs, so it is only correct when another
			// run will actually follow. A declared repeat count larger than
			// the octets present (e.g. hint "*1x:" over {9, 0xaa}) would
			// otherwise leave a dangling "aa:".
			if spec.Separator != 0 && r < repeats-1 && pos < len(value) {
				out.WriteByte(spec.Separator)
			}
		}

		if spec.Terminator != 0 {
			out.WriteByte(spec.Terminator)
		} else if spec.Separator != 0 && pos < len(value) {
			out.WriteByte(spec.Separator)
		}

		// Advance to the next specifier, staying on the last one once they are
		// exhausted: RFC 2579 reapplies the final specifier to any remaining
		// octets, which is what makes "1x:" render an address of any length.
		if specIndex < len(h.Octets)-1 {
			specIndex++
		}
	}

	return out.String()
}

// formatOctetRun renders one run of octets in the given format.
func formatOctetRun(octets []byte, format byte) string {
	switch format {
	case 'a':
		return string(octets)
	case 'x':
		var b strings.Builder
		for _, o := range octets {
			fmt.Fprintf(&b, "%02x", o)
		}
		return b.String()
	case 'd':
		return strconv.FormatUint(beUint(octets), 10)
	case 'o':
		return strconv.FormatUint(beUint(octets), 8)
	case 'b':
		return strconv.FormatUint(beUint(octets), 2)
	default:
		return string(octets)
	}
}

// beUint decodes octets as a big-endian unsigned integer.
func beUint(octets []byte) uint64 {
	var n uint64
	for _, o := range octets {
		n = n<<8 | uint64(o)
	}
	return n
}
