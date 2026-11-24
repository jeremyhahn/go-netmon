package poller

import (
	"strconv"
	"time"
)

// The failures a poll can report, as types rather than formatted strings: what
// a caller does about them differs, and the poll result they travel in is the
// only record an operator gets of a cycle that collected nothing.

// UnreachableError reports a poll the device answered nothing to, with what was attempted and skipped.
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

// DeviceExistsError reports adding a device name already polled; RegisterDevice treats it as success.
type DeviceExistsError struct {
	Device string
}

func (e *DeviceExistsError) Error() string {
	return "poller: device " + e.Device + " is already registered"
}
