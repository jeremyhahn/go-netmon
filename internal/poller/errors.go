package poller

import (
	"strconv"
	"time"
)

// The failures a poll can report, as types rather than formatted strings: what
// a caller does about them differs, and the poll result they travel in is the
// only record an operator gets of a cycle that collected nothing.

// UnreachableError reports a poll in which the device answered nothing.
//
// It carries what was attempted and what was skipped because attempting it
// would have been pointless, so the poll it fails is still an honest record of
// what the poller did.
type UnreachableError struct {
	Device   string
	Attempts int   // operations tried, none of which the device answered
	Skipped  int   // operations abandoned rather than attempted
	Err      error // the last transport failure
}

func (e *UnreachableError) Error() string {
	message := "poller: device " + e.Device + " answered none of " +
		strconv.Itoa(e.Attempts) + " SNMP operations"
	if e.Skipped > 0 {
		message += "; skipped the remaining " + strconv.Itoa(e.Skipped)
	}
	if e.Err != nil {
		message += ": " + e.Err.Error()
	}
	return message
}

func (e *UnreachableError) Unwrap() error { return e.Err }

// ClientError reports a device whose SNMP client could not be built or
// connected, so the poll never got as far as asking it anything.
type ClientError struct {
	Device string
	Err    error
}

func (e *ClientError) Error() string {
	return "poller: device " + e.Device + ": SNMP client initialization failed: " + e.Err.Error()
}

func (e *ClientError) Unwrap() error { return e.Err }

// IntervalError reports a device whose poll interval is not a positive
// duration, which would run its worker in a loop instead of on a schedule.
type IntervalError struct {
	Device   string
	Interval time.Duration
}

func (e *IntervalError) Error() string {
	return "poller: device " + e.Device + " has a poll interval of " + e.Interval.String() +
		"; it must be positive (set poll_interval on the device or default_poll_interval globally)"
}

// DeviceExistsError reports an attempt to add a device under a name the poller
// already polls.
//
// It is a type rather than a formatted string because the answer depends
// entirely on who asked. AddDevice is a create, and a create that finds the
// name taken has failed. A registration push is a statement of what the poller
// should be polling, and one that finds the device already registered has
// nothing to do -- so RegisterDevice catches this and reports success. Neither
// caller can tell the two apart from a message.
type DeviceExistsError struct {
	Device string
}

func (e *DeviceExistsError) Error() string {
	return "poller: device " + e.Device + " is already registered"
}
