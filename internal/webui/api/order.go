package api

import "strconv"

// compareInstance orders instance identifiers: numerically when both are numeric, else as text.
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
