package mib

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
)

// hintCache memoizes parsed DISPLAY-HINT clauses. The same handful of hints
// ("255a", "1x:", the DateAndTime hint) recur across every OID in every module,
// and formatting sits on the per-poll display path.
var hintCache sync.Map // map[hintKey]*DisplayHint

type hintKey struct {
	hint      string
	isInteger bool
}

// cachedHint parses a DISPLAY-HINT, reusing a previous parse when possible.
// An unparseable hint is cached as nil so it is not re-parsed on every value.
func cachedHint(hint string, isInteger bool) *DisplayHint {
	key := hintKey{hint: hint, isInteger: isInteger}
	if cached, ok := hintCache.Load(key); ok {
		parsed, _ := cached.(*DisplayHint)
		return parsed
	}

	parsed, err := ParseDisplayHint(hint, isInteger)
	if err != nil {
		parsed = nil
	}
	hintCache.Store(key, parsed)
	return parsed
}

// FormatValue renders a raw SNMP value as text for display in the CLI, the web
// UI, or a metric label, using the OID's MIB metadata to decide how.
//
// The rules, in order:
//
//   - An enumerated value renders as its label with the number in parentheses,
//     e.g. "up(1)", matching the convention users know from snmpwalk.
//   - TimeTicks render as a duration, e.g. "12d 3h 14m 7s".
//   - A DISPLAY-HINT, when present, drives the rendering: MAC addresses become
//     "00:1b:21:3c:4d:5e", DateAndTime becomes an RFC 3339 timestamp.
//   - Octet strings with no hint render as text when their content is text,
//     and as colon-separated hex otherwise; see FormatOctets.
//   - Anything else renders as its natural Go representation.
//
// Units are not appended; call FormatValueWithUnits for that. A nil meta -- no
// loaded MIB describes the object -- falls back to the content rule for octet
// strings and the natural representation otherwise. A nil meta or an
// unrecognized value always yields something printable rather than an error,
// because this sits on a display path where a bad value must not break a whole
// table of good ones.
func FormatValue(meta *OIDMetadata, value any) string {
	if value == nil {
		return ""
	}
	if meta == nil {
		// No MIB describes this object, which is the case FormatOctets is for:
		// the content is the only evidence there is. Without this an octet
		// string would render as Go syntax, "[4 0]", or as raw bytes depending
		// only on which Go type gosnmp happened to yield.
		if octets, ok := toBytes(value); ok {
			return FormatOctets(octets)
		}
		return fmt.Sprintf("%v", value)
	}

	// Enumerations win over every other rendering: the label is the value's
	// meaning, and the number alone is opaque.
	if len(meta.EnumValues) > 0 {
		if n, ok := toInt64(value); ok {
			if name, found := meta.EnumValues[n]; found {
				return name + "(" + strconv.FormatInt(n, 10) + ")"
			}
			// A value outside the enumeration is a real condition worth
			// surfacing rather than hiding behind a bare number.
			return "unknown(" + strconv.FormatInt(n, 10) + ")"
		}
	}

	switch normalizeType(meta.Type) {
	case "timeticks":
		if n, ok := toInt64(value); ok {
			return FormatTimeTicks(n)
		}
	case "ipaddress":
		if octets, ok := toBytes(value); ok && len(octets) == 4 {
			return fmt.Sprintf("%d.%d.%d.%d", octets[0], octets[1], octets[2], octets[3])
		}
	case "dateandtime":
		if octets, ok := toBytes(value); ok {
			if formatted, err := FormatDateAndTime(octets); err == nil {
				return formatted
			}
		}
	}

	if octets, ok := toBytes(value); ok {
		return formatOctetString(meta, octets)
	}

	if n, ok := toInt64(value); ok {
		if meta.DisplayHint != "" {
			if hint := cachedHint(meta.DisplayHint, true); hint != nil {
				return hint.FormatInteger(n)
			}
		}
		return strconv.FormatInt(n, 10)
	}

	return fmt.Sprintf("%v", value)
}

// EnumLabel renders value using an enumeration's labels, falling back to the
// decimal number when the enumeration does not define it.
//
// Unlike FormatValue this returns the bare label with no "(n)" suffix, because
// it feeds Prometheus label values where the rendered text is part of the
// series identity — changing it would orphan existing series.
func EnumLabel(enumValues map[int64]string, value int64) string {
	if name, ok := enumValues[value]; ok {
		return name
	}
	return strconv.FormatInt(value, 10)
}

// FormatValueWithUnits renders a value and appends the OID's UNITS clause when
// one is declared, e.g. "1500 octets". Values that already carry their unit in
// the rendering, such as durations and enumerations, are left alone.
func FormatValueWithUnits(meta *OIDMetadata, value any) string {
	formatted := FormatValue(meta, value)
	if meta == nil || meta.Units == "" || formatted == "" {
		return formatted
	}
	if len(meta.EnumValues) > 0 || normalizeType(meta.Type) == "timeticks" {
		return formatted
	}
	return formatted + " " + meta.Units
}

// formatOctetString renders a byte-valued object, preferring the DISPLAY-HINT
// and falling back to text-or-hex based on the content.
func formatOctetString(meta *OIDMetadata, octets []byte) string {
	if len(octets) == 0 {
		return ""
	}

	if meta.DisplayHint != "" {
		if hint := cachedHint(meta.DisplayHint, false); hint != nil {
			return hint.FormatOctets(octets)
		}
	}

	// Some agents return a MAC-shaped value with no usable hint.
	if isPhysAddressType(meta.Type) {
		return formatHexColons(octets)
	}

	return FormatOctets(octets)
}

// FormatOctets renders an OCTET STRING that carries no DISPLAY-HINT, deciding
// between text and hex from the content alone. This is the only information
// available for the binary objects vendors routinely define without a hint:
// engine IDs, opaque cookies, packed address blobs, protocol error codes.
//
// The rule:
//
//   - Trailing NUL octets are padding, not content. Agents commonly pad a
//     fixed-width string field, and such a value is still text.
//   - What remains is text when every octet is printable ASCII or one of the
//     whitespace controls that legitimately appear in multi-line values such
//     as sysDescr. A NUL anywhere but the tail is content, and content that
//     cannot be shown makes the whole value binary.
//   - Anything else renders as colon-separated hex over the original octets,
//     padding included, matching how the rest of the codebase shows binary.
//
// A value that was non-empty on the wire never renders as empty. An empty
// rendering is indistinguishable from "the agent returned no value", which is
// how cbgpPeer2LastError -- a two-octet BGP error code and subcode -- showed
// nothing at all for the value {0x04, 0x00}, "Hold Timer Expired".
func FormatOctets(octets []byte) string {
	if len(octets) == 0 {
		return ""
	}

	if trimmed := trimNULPadding(octets); len(trimmed) > 0 && isText(trimmed) {
		return string(trimmed)
	}
	return formatHexColons(octets)
}

// FormatTimeTicks renders TimeTicks (hundredths of a second since some epoch)
// as a compact duration. SNMP uptimes are routinely months long, so days are
// spelled out rather than rolled into hours.
func FormatTimeTicks(ticks int64) string {
	if ticks < 0 {
		return strconv.FormatInt(ticks, 10)
	}

	d := time.Duration(ticks) * 10 * time.Millisecond

	days := int64(d / (24 * time.Hour))
	d -= time.Duration(days) * 24 * time.Hour
	hours := int64(d / time.Hour)
	d -= time.Duration(hours) * time.Hour
	minutes := int64(d / time.Minute)
	d -= time.Duration(minutes) * time.Minute
	seconds := int64(d / time.Second)

	var parts []string
	if days > 0 {
		parts = append(parts, strconv.FormatInt(days, 10)+"d")
	}
	if hours > 0 || len(parts) > 0 {
		parts = append(parts, strconv.FormatInt(hours, 10)+"h")
	}
	if minutes > 0 || len(parts) > 0 {
		parts = append(parts, strconv.FormatInt(minutes, 10)+"m")
	}
	parts = append(parts, strconv.FormatInt(seconds, 10)+"s")

	return strings.Join(parts, " ")
}

// FormatDateAndTime renders the SNMPv2-TC DateAndTime octet string, which is 8
// octets (local time) or 11 (with a UTC offset), as RFC 3339.
func FormatDateAndTime(octets []byte) (string, error) {
	if len(octets) != 8 && len(octets) != 11 {
		return "", &DisplayHintError{
			Hint:   "DateAndTime",
			Reason: "expected 8 or 11 octets, got " + strconv.Itoa(len(octets)),
		}
	}

	year := int(octets[0])<<8 | int(octets[1])
	month := time.Month(octets[2])
	day := int(octets[3])
	hour := int(octets[4])
	minute := int(octets[5])
	second := int(octets[6])
	deciseconds := int(octets[7])

	if month < time.January || month > time.December || day < 1 || day > 31 {
		return "", &DisplayHintError{Hint: "DateAndTime", Reason: "field out of range"}
	}

	location := time.UTC
	if len(octets) == 11 {
		offsetHours := int(octets[9])
		offsetMinutes := int(octets[10])
		offset := offsetHours*3600 + offsetMinutes*60
		if octets[8] == '-' {
			offset = -offset
		}
		location = time.FixedZone("", offset)
	}

	t := time.Date(year, month, day, hour, minute, second, deciseconds*100*int(time.Millisecond), location)
	return t.Format(time.RFC3339), nil
}

// formatHexColons renders octets as colon-separated hex pairs.
func formatHexColons(octets []byte) string {
	var b strings.Builder
	b.Grow(len(octets)*3 - 1)
	for i, o := range octets {
		if i > 0 {
			b.WriteByte(':')
		}
		fmt.Fprintf(&b, "%02x", o)
	}
	return b.String()
}

// trimNULPadding drops the trailing NUL octets agents use to pad a fixed-width
// string field. Only the tail is padding: a NUL before any other octet is
// content, and content is what makes a value binary.
func trimNULPadding(octets []byte) []byte {
	end := len(octets)
	for end > 0 && octets[end-1] == 0x00 {
		end--
	}
	return octets[:end]
}

// isText reports whether every octet can be shown as text: printable ASCII, or
// one of the whitespace controls that legitimately appear in multi-line values.
// NUL is not text; trimNULPadding removes it where it is merely padding.
func isText(octets []byte) bool {
	for _, o := range octets {
		if o == '\t' || o == '\n' || o == '\r' {
			continue
		}
		if o < 0x20 || o > 0x7e {
			return false
		}
	}
	return true
}

// isPhysAddressType reports whether a type name denotes a hardware address.
func isPhysAddressType(typeName string) bool {
	switch normalizeType(typeName) {
	case "macaddress", "physaddress":
		return true
	default:
		return false
	}
}

// normalizeType lowercases a MIB type name for comparison.
func normalizeType(typeName string) string {
	return strings.ToLower(typeName)
}

// toInt64 converts the integer-like values gosnmp yields to int64.
func toInt64(value any) (int64, bool) {
	switch v := value.(type) {
	case int:
		return int64(v), true
	case int8:
		return int64(v), true
	case int16:
		return int64(v), true
	case int32:
		return int64(v), true
	case int64:
		return v, true
	case uint:
		// uint is 64 bits here; wrapping would report a negative counter.
		if v > math.MaxInt64 {
			return 0, false
		}
		return int64(v), true
	case uint8:
		return int64(v), true
	case uint16:
		return int64(v), true
	case uint32:
		return int64(v), true
	case uint64:
		if v > math.MaxInt64 {
			return 0, false
		}
		return int64(v), true
	case float64:
		return int64(v), true
	default:
		return 0, false
	}
}

// toBytes converts the octet-string values gosnmp yields to a byte slice.
func toBytes(value any) ([]byte, bool) {
	switch v := value.(type) {
	case []byte:
		return v, true
	case string:
		return []byte(v), true
	default:
		return nil, false
	}
}
