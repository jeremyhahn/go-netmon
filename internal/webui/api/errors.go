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

// SNMPUnreachableError reports that a device's SNMP agent did not answer.
//
// It names the device as the operator knows it, the address actually probed and
// how long the probe was allowed to take, because "no response" is only
// actionable once you know which box was asked and for how long.
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

// The errors below classify why a device's status could not be produced. They
// exist so that the per-device endpoint and the fleet endpoint can draw
// different conclusions from the same failure without either of them re-reading
// the poller's error strings: the per-device endpoint maps them to HTTP status
// codes, the fleet endpoint maps them to a per-device state.

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

// DeviceNotPolledError reports that the poller has no record of a device.
//
// It is a definite answer, not a failure to get one: the device exists in the
// web UI's own records but has never been picked up for polling, which is a
// state a fleet view has to show and an operator has to act on.
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
	msg := "poller: status for device " + strconv.Quote(e.Device) + " is unavailable"
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *PollerUnavailableError) Unwrap() error { return e.Err }

// QueryParameterError reports a narrowing parameter the server will not act on:
// a limit that is not a number, a negative offset, a flag that is not a
// boolean.
//
// It is produced before the handler reads anything, so a malformed request
// costs a parse rather than a walk of a ten-thousand-object module, and it
// names the parameter and the value so a client can point at what has to
// change.
type QueryParameterError struct {
	Parameter string
	Value     string
	Reason    string
}

func (e *QueryParameterError) Error() string {
	return "query parameter " + e.Parameter + "=" + strconv.Quote(e.Value) + ": " + e.Reason
}

// PageRangeError reports a page that lies past the end of the matching records.
//
// It cannot be detected before the records are counted, so unlike
// QueryParameterError it is raised after the list is assembled. Answering it
// with an empty list instead would show an operator a blank table and let them
// conclude the module is empty.
type PageRangeError struct {
	Offset  int
	Limit   int
	Matched int
}

func (e *PageRangeError) Error() string {
	return "offset " + strconv.Itoa(e.Offset) + " is past the end of " +
		strconv.Itoa(e.Matched) + " matching record(s)"
}
