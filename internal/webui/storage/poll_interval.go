package storage

import "strconv"

// MinPollInterval is the shortest per-device poll interval that may be stored,
// in seconds.
//
// One second is not a recommendation, it is the floor below which the value
// stops describing a schedule: an interval of zero asks for a poll with no wait
// between polls, and a negative one asks for a poll in the past. Neither is a
// slower or faster configuration of the same thing, so neither is stored.
const MinPollInterval = 1

// InvalidPollIntervalError reports a per-device poll interval that cannot be
// stored.
//
// It names the device and the offending value, and it says how to ask for the
// global default, because that is the request an operator who typed 0 was
// almost certainly making.
type InvalidPollIntervalError struct {
	Device   string
	Interval int
}

func (e *InvalidPollIntervalError) Error() string {
	subject := "poll interval"
	if e.Device != "" {
		subject = "poll interval for device " + strconv.Quote(e.Device)
	}
	return "storage: " + subject + " " + strconv.Itoa(e.Interval) +
		" is not a schedule; it must be at least " + strconv.Itoa(MinPollInterval) +
		" second, or absent to inherit the global default"
}

// ValidatePollInterval reports whether a device's poll interval may be stored.
//
// A nil interval is the device asking to inherit the global default and is
// always allowed; that is the whole reason the field is a pointer rather than a
// count with a magic zero. Any value that is present has to be a real schedule.
func ValidatePollInterval(device string, interval *int) error {
	if interval == nil {
		return nil
	}
	if *interval < MinPollInterval {
		return &InvalidPollIntervalError{Device: device, Interval: *interval}
	}
	return nil
}

// PollIntervalOf returns a device's stored interval in seconds, or 0 when the
// device inherits the global default.
//
// Zero is how "inherit" is spelled downstream: both the poller's control API
// and its configuration document treat poll_interval 0 as "use
// global.default_poll_interval", so this is the one place that translates the
// web UI's absent-means-inherit into the poller's zero-means-inherit.
func PollIntervalOf(d *Device) int {
	if d == nil || d.PollInterval == nil {
		return 0
	}
	return *d.PollInterval
}
