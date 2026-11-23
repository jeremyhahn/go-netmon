package mib

import (
	"errors"
	"strings"
	"testing"
)

// A malformed DISPLAY-HINT is a defect in a MIB file, not in a device, so the
// message has to carry the hint text itself: that is the only thing that tells
// an operator which TEXTUAL-CONVENTION to go and look at.
func TestDisplayHintErrorMessage(t *testing.T) {
	err := &DisplayHintError{Hint: "1x:", Reason: "missing format character"}

	got := err.Error()
	if !strings.Contains(got, `"1x:"`) {
		t.Errorf("Error() = %q, want the hint quoted so trailing punctuation is visible", got)
	}
	if !strings.Contains(got, "missing format character") {
		t.Errorf("Error() = %q, want it to carry the reason", got)
	}
	if !strings.HasPrefix(got, "mib: ") {
		t.Errorf("Error() = %q, want it to identify the package it came from", got)
	}
}

// TestDisplayHintErrorQuotesEmptyHint covers the case the parser reports for an
// absent hint: the quotes are what stop the message reading as though a reason
// were missing too.
func TestDisplayHintErrorQuotesEmptyHint(t *testing.T) {
	got := (&DisplayHintError{Hint: "", Reason: "empty"}).Error()

	if !strings.Contains(got, `""`) {
		t.Errorf("Error() = %q, want an empty hint to render as a visible empty string", got)
	}
}

func TestDisplayHintErrorIsMatchable(t *testing.T) {
	_, err := ParseDisplayHint("", false)
	if err == nil {
		t.Fatal("parsing an empty hint reported success")
	}

	var target *DisplayHintError
	if !errors.As(err, &target) {
		t.Fatalf("got %T, want *DisplayHintError", err)
	}
	if target.Reason == "" {
		t.Error("the parser produced an error with no reason")
	}
}

func TestValueTypeErrorMessage(t *testing.T) {
	err := &ValueTypeError{
		OID:      "1.3.6.1.2.1.2.2.1.10.1",
		Expected: "Counter32",
		Got:      "string",
	}

	got := err.Error()
	for _, want := range []string{"1.3.6.1.2.1.2.2.1.10.1", "Counter32", "string"} {
		if !strings.Contains(got, want) {
			t.Errorf("Error() = %q, want it to contain %q", got, want)
		}
	}
}

func TestValueTypeErrorOnZeroValue(t *testing.T) {
	got := (&ValueTypeError{}).Error()

	if got == "" {
		t.Fatal("Error() on a zero-value ValueTypeError is empty")
	}
	if !strings.HasPrefix(got, "mib: ") {
		t.Errorf("Error() = %q, want it to identify the package it came from", got)
	}
}
