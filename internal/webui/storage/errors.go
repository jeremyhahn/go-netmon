package storage

import "strconv"

// AssignmentNotFoundError reports that a MIB is not assigned to the device or
// profile whose assignment was requested.
type AssignmentNotFoundError struct {
	Kind    string // "device" or "profile"
	OwnerID int64
	MIBID   int64
}

func (e *AssignmentNotFoundError) Error() string {
	return "storage: MIB " + strconv.FormatInt(e.MIBID, 10) +
		" is not assigned to " + e.Kind + " " + strconv.FormatInt(e.OwnerID, 10)
}

// AssignmentKeyError reports a join row key that could not be parsed.
type AssignmentKeyError struct {
	Key string
}

func (e *AssignmentKeyError) Error() string {
	return "storage: malformed assignment key " + strconv.Quote(e.Key)
}
