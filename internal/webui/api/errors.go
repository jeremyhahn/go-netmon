package api

import (
	"strconv"
	"time"
)

// MIBStorageError reports that the MIB metadata store could not be reached or
// used to answer a request.
type MIBStorageError struct {
	Reason string
}

func (e *MIBStorageError) Error() string {
	return "mib storage: " + e.Reason
}

// AssignmentValidationError reports a MIB assignment the poller could not act
// on. It names the offending field and value so a client can point at the
// input that has to change.
type AssignmentValidationError struct {
	Field  string
	Value  string
	Reason string
}

func (e *AssignmentValidationError) Error() string {
	return "assignment: invalid " + e.Field + " " + strconv.Quote(e.Value) + ": " + e.Reason
}

// SNMPUnreachableError reports a device whose agent did not answer, with the address and timeout used.
type SNMPUnreachableError struct {
	Device  string
	Address string
	Budget  time.Duration
	Err     error
}

func (e *SNMPUnreachableError) Error() string {
	subject := e.Address
	if e.Device != "" {
		subject = e.Device + " (" + e.Address + ")"
	}
	return "snmp: device " + subject + " did not answer within " + e.Budget.String() + ": " + e.Err.Error()
}

func (e *SNMPUnreachableError) Unwrap() error { return e.Err }

// SNMPProbeError reports a probe that could not be attempted or completed for a
// reason other than the device staying silent, such as an unusable address.
type SNMPProbeError struct {
	Device  string
	Address string
	Reason  string
	Err     error
}

func (e *SNMPProbeError) Error() string {
	subject := e.Address
	if e.Device != "" {
		subject = e.Device + " (" + e.Address + ")"
	}
	msg := "snmp: probing device " + subject + ": " + e.Reason
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *SNMPProbeError) Unwrap() error { return e.Err }

// SNMPRequestError reports a probe request the server refused before sending
// anything, such as an unsupported SNMP version or an out-of-range port.
type SNMPRequestError struct {
	Field  string
	Value  string
	Reason string
}

func (e *SNMPRequestError) Error() string {
	return "snmp: invalid " + e.Field + " " + strconv.Quote(e.Value) + ": " + e.Reason
}

// Errors classifying why a device's status could not be produced.

// DeviceNameRequiredError reports a status request that named no device.
type DeviceNameRequiredError struct{}

func (e *DeviceNameRequiredError) Error() string {
	return "poller: device name is required"
}

// PollerUnconfiguredError reports that this web UI has no poller to ask. It is
// a deployment fact rather than a fault of the device being asked about.
type PollerUnconfiguredError struct{}

func (e *PollerUnconfiguredError) Error() string {
	return "poller: no poller client is configured"
}

// DeviceNotPolledError reports a device the poller has no record of.
type DeviceNotPolledError struct {
	Device string
	Err    error
}

func (e *DeviceNotPolledError) Error() string {
	return "poller: device " + strconv.Quote(e.Device) + " is not registered with the poller"
}

func (e *DeviceNotPolledError) Unwrap() error { return e.Err }

// PollerUnavailableError reports that the poller's control API could not be
// reached or did not answer in time, so nothing at all is known about the
// device — as distinct from knowing that it is unhealthy.
type PollerUnavailableError struct {
	Device string
	Err    error
}

func (e *PollerUnavailableError) Error() string {
	// The fleet view asks about the whole fleet at once, so it has no one
	// device to name when the poller does not answer.
	msg := "poller: the poller is unavailable"
	if e.Device != "" {
		msg = "poller: status for device " + strconv.Quote(e.Device) + " is unavailable"
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *PollerUnavailableError) Unwrap() error { return e.Err }

// PollerStatusMissingError reports a device the poller listed without a status.
type PollerStatusMissingError struct {
	Device string
}

func (e *PollerStatusMissingError) Error() string {
	return "poller: device " + strconv.Quote(e.Device) + " is registered but the poller holds no status for it"
}

// QueryParameterError reports a malformed narrowing parameter, naming it and its value.
type QueryParameterError struct {
	Parameter string
	Value     string
	Reason    string
}

func (e *QueryParameterError) Error() string {
	return "query parameter " + e.Parameter + "=" + strconv.Quote(e.Value) + ": " + e.Reason
}

// PageRangeError reports a page past the end of the matching records.
type PageRangeError struct {
	Offset  int
	Limit   int
	Matched int
}

func (e *PageRangeError) Error() string {
	return "offset " + strconv.Itoa(e.Offset) + " is past the end of " +
		strconv.Itoa(e.Matched) + " matching record(s)"
}

// ListenError reports that the web UI could not bind its listen address.
type ListenError struct {
	Addr string
	Err  error
}

func (e *ListenError) Error() string {
	return "webui: listen on " + strconv.Quote(e.Addr) + ": " + e.Err.Error()
}

func (e *ListenError) Unwrap() error { return e.Err }

// PollerDetachError reports that the poller may still poll a device, so it was not deleted.
type PollerDetachError struct {
	Device string
	Err    error
}

func (e *PollerDetachError) Error() string {
	return "poller: device " + strconv.Quote(e.Device) +
		" was not deleted because the poller could not be told to stop polling it: " + e.Err.Error()
}

func (e *PollerDetachError) Unwrap() error { return e.Err }
