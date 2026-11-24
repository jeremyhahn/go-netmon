package storage

import "strconv"

// MinPollInterval is the shortest per-device poll interval that may be stored, in seconds.
const MinPollInterval = 1

// InvalidPollIntervalError reports a poll interval that cannot be stored and how to inherit the default.
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

// ValidatePollInterval reports whether an interval may be stored; nil inherits the default.
func ValidatePollInterval(device string, interval *int) error {
	if interval == nil {
		return nil
	}
	if *interval < MinPollInterval {
		return &InvalidPollIntervalError{Device: device, Interval: *interval}
	}
	return nil
}

// PollIntervalOf returns a device's interval in seconds, or 0 to inherit global.default_poll_interval.
func PollIntervalOf(d *Device) int {
	if d == nil || d.PollInterval == nil {
		return 0
	}
	return *d.PollInterval
}
