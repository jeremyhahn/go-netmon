package snmp

import (
	"strconv"

	"github.com/gosnmp/gosnmp"
)

// NoOIDsError reports a request that named no OIDs.
type NoOIDsError struct{}

func (e *NoOIDsError) Error() string {
	return "snmp: no OIDs provided"
}

// GetError reports a transport-level failure performing an SNMP GET.
type GetError struct {
	Count int
	Err   error
}

func (e *GetError) Error() string {
	return "snmp: GET of " + strconv.Itoa(e.Count) + " OIDs failed: " + e.Err.Error()
}

func (e *GetError) Unwrap() error { return e.Err }

// TooBigError reports an agent that cannot fit even a single varbind into a
// response, which no amount of splitting will resolve.
type TooBigError struct {
	Count int
	OID   string
}

func (e *TooBigError) Error() string {
	return "snmp: agent reports tooBig for a single OID " + e.OID +
		"; its maximum response size cannot hold one varbind"
}

// ResponseError reports an SNMP response carrying a non-zero error status.
//
// gosnmp returns these as successful calls, so a caller that only checks the
// Go error treats them as an empty but valid result.
type ResponseError struct {
	Status gosnmp.SNMPError
	Index  uint8
	Count  int
}

func (e *ResponseError) Error() string {
	return "snmp: agent returned " + e.Status.String() +
		" for a GET of " + strconv.Itoa(e.Count) + " OIDs" +
		" (error index " + strconv.Itoa(int(e.Index)) + ")"
}

// WalkError reports a walk that did not finish.
//
// Received is how many varbinds arrived before it failed, and is the field that
// makes the difference between a device which is not there and one which is
// merely awkward: an agent that streamed part of a table before falling silent
// answered, and a caller deciding whether the device is reachable must not
// count that as silence. gosnmp discards the partial rows, so this count is the
// only surviving evidence that they existed.
type WalkError struct {
	Op       string
	OID      string
	Received int
	Err      error
}

func (e *WalkError) Error() string {
	return "SNMP " + e.Op + " failed for " + e.OID + ": " + e.Err.Error()
}

func (e *WalkError) Unwrap() error { return e.Err }
