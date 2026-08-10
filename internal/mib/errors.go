package mib

// DisplayHintError reports a DISPLAY-HINT clause that does not conform to the
// grammar in RFC 2579.
type DisplayHintError struct {
	Hint   string
	Reason string
}

func (e *DisplayHintError) Error() string {
	return "mib: invalid DISPLAY-HINT " + quote(e.Hint) + ": " + e.Reason
}

// ValueTypeError reports a raw SNMP value whose Go type does not match the
// type declared for the OID in the MIB.
type ValueTypeError struct {
	OID      string
	Expected string
	Got      string
}

func (e *ValueTypeError) Error() string {
	return "mib: OID " + e.OID + ": expected " + e.Expected + " value, got " + e.Got
}

// quote wraps s in double quotes without pulling in fmt.
func quote(s string) string {
	return `"` + s + `"`
}
