package api

import "strconv"

// compareInstance orders two instance identifiers — the values of the label a
// chart is broken down by, such as an ifIndex or an entPhysicalIndex.
//
// Identifiers that are entirely numeric compare numerically, because SNMP table
// indexes usually are and an operator reading a list of interfaces expects 2
// before 10. Anything else compares as text. The point is only that the order
// is the same on every request: the instance picker is built from this list and
// its first entry is what the page selects by default, so a list assembled in
// map order changes the selected interface on every reload.
func compareInstance(a, b string) int {
	numA, errA := strconv.ParseInt(a, 10, 64)
	numB, errB := strconv.ParseInt(b, 10, 64)

	switch {
	case errA == nil && errB == nil:
		switch {
		case numA < numB:
			return -1
		case numA > numB:
			return 1
		default:
			return 0
		}
	case errA == nil:
		// Numbered instances first: they are the table's own rows, and a
		// non-numeric identifier is usually a name that was substituted in.
		return -1
	case errB == nil:
		return 1
	default:
		switch {
		case a < b:
			return -1
		case a > b:
			return 1
		default:
			return 0
		}
	}
}
