package snmp

import (
	"errors"
	"strings"
	"testing"

	"github.com/gosnmp/gosnmp"
)

// The messages these types produce are the only evidence an operator gets when
// a poll returns nothing, so each has to say which request failed and why. The
// distinction between them matters too: tooBig is a device limit that splitting
// fixes, a GetError is a transport failure, and a ResponseError is an agent
// that answered with a refusal — three different things to do about it.

func TestNoOIDsErrorMessage(t *testing.T) {
	var err error = &NoOIDsError{}

	if !strings.Contains(err.Error(), "no OIDs") {
		t.Errorf("Error() = %q, want it to say no OIDs were provided", err)
	}

	var target *NoOIDsError
	if !errors.As(err, &target) {
		t.Error("a NoOIDsError is not matchable with errors.As")
	}
}

func TestGetErrorMessageAndUnwrap(t *testing.T) {
	cause := errors.New("request timeout")
	err := &GetError{Count: 42, Err: cause}

	message := err.Error()
	if !strings.Contains(message, "42") {
		t.Errorf("Error() = %q, want it to report how many OIDs were requested", message)
	}
	if !strings.Contains(message, cause.Error()) {
		t.Errorf("Error() = %q, want it to include the underlying failure", message)
	}
	if !errors.Is(err, cause) {
		t.Error("the underlying transport error is not reachable with errors.Is")
	}
}

func TestTooBigErrorMessage(t *testing.T) {
	err := &TooBigError{Count: 1, OID: "1.3.6.1.2.1.1.1.0"}

	message := err.Error()
	if !strings.Contains(message, "1.3.6.1.2.1.1.1.0") {
		t.Errorf("Error() = %q, want it to name the offending OID", message)
	}
	if !strings.Contains(message, "tooBig") {
		t.Errorf("Error() = %q, want it to identify the agent's status", message)
	}
}

func TestResponseErrorMessage(t *testing.T) {
	err := &ResponseError{Status: gosnmp.NoSuchName, Index: 3, Count: 7}

	message := err.Error()
	for _, want := range []string{gosnmp.NoSuchName.String(), "7", "3"} {
		if !strings.Contains(message, want) {
			t.Errorf("Error() = %q, want it to contain %q", message, want)
		}
	}
}

// TestResponseErrorZeroValueIsStillReadable covers the degenerate case: a
// status of NoError should never reach this type, but if it does the message
// must still be a complete sentence rather than a panic or a blank.
func TestResponseErrorZeroValueIsStillReadable(t *testing.T) {
	message := (&ResponseError{}).Error()

	if message == "" {
		t.Fatal("Error() on a zero-value ResponseError is empty")
	}
	if !strings.HasPrefix(message, "snmp: ") {
		t.Errorf("Error() = %q, want it to identify the package it came from", message)
	}
}

// TestErrorTypesAreDistinguishable is what lets a caller decide between
// retrying, splitting and giving up. Collapsing any two of these into one type
// would take that decision away.
func TestErrorTypesAreDistinguishable(t *testing.T) {
	var tooBig *TooBigError
	var getErr *GetError
	var respErr *ResponseError
	var noOIDs *NoOIDsError

	var err error = &TooBigError{OID: "1.3.6.1.2.1.1.1.0"}
	if !errors.As(err, &tooBig) {
		t.Error("a TooBigError is not matchable as itself")
	}
	if errors.As(err, &getErr) || errors.As(err, &respErr) || errors.As(err, &noOIDs) {
		t.Error("a TooBigError matches another error type")
	}

	err = &ResponseError{Status: gosnmp.GenErr}
	if !errors.As(err, &respErr) {
		t.Error("a ResponseError is not matchable as itself")
	}
	if errors.As(err, &tooBig) {
		t.Error("a ResponseError matches TooBigError")
	}
}
