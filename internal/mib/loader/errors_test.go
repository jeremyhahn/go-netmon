package loader

import (
	"errors"
	"strings"
	"testing"
)

// These messages are what an operator sees when MIBs will not load, which is
// the moment a device silently stops being polled. Each one therefore has to
// name the thing that failed — the store path, the module, the step — because
// "failed to load" on its own sends the reader to the wrong file.

// errSentinel stands in for the underlying failure being wrapped.
var errSentinel = errors.New("disk on fire")

func TestStoreOpenErrorNamesThePathAndCause(t *testing.T) {
	err := &StoreOpenError{Path: "/app/data/mib.db", Err: errSentinel}

	got := err.Error()
	for _, want := range []string{"loader:", "/app/data/mib.db", "disk on fire"} {
		if !strings.Contains(got, want) {
			t.Errorf("Error() = %q, want it to contain %q", got, want)
		}
	}
	if !errors.Is(err, errSentinel) {
		t.Error("the underlying failure is not reachable through errors.Is")
	}
}

func TestStoreErrorNamesTheOperation(t *testing.T) {
	err := &StoreError{Op: "count stored MIB metadata", Err: errSentinel}

	if got := err.Error(); !strings.Contains(got, "count stored MIB metadata") {
		t.Errorf("Error() = %q, want it to name the operation", got)
	}
	if !errors.Is(err, errSentinel) {
		t.Error("the underlying failure is not reachable through errors.Is")
	}
}

func TestParserInitErrorCarriesItsCause(t *testing.T) {
	err := &ParserInitError{Err: errSentinel}

	if got := err.Error(); !strings.Contains(got, "disk on fire") {
		t.Errorf("Error() = %q, want it to carry the cause", got)
	}
	if !errors.Is(err, errSentinel) {
		t.Error("the underlying failure is not reachable through errors.Is")
	}
}

func TestProfileLoadErrorCarriesItsCause(t *testing.T) {
	err := &ProfileLoadError{Err: errSentinel}

	if got := err.Error(); !strings.Contains(got, "built-in profiles") {
		t.Errorf("Error() = %q, want it to say which profiles failed", got)
	}
	if !errors.Is(err, errSentinel) {
		t.Error("the underlying failure is not reachable through errors.Is")
	}
}

// TestModuleLoadErrorDistinguishesTheStep matters because the three steps fail
// for unrelated reasons: a missing file, a malformed one, and a store that
// would not accept the result.
func TestModuleLoadErrorDistinguishesTheStep(t *testing.T) {
	read := (&ModuleLoadError{Module: "IF-MIB", Op: "read", Err: errSentinel}).Error()
	parse := (&ModuleLoadError{Module: "IF-MIB", Op: "parse", Err: errSentinel}).Error()

	if read == parse {
		t.Fatalf("a failed read and a failed parse render identically: %q", read)
	}
	for _, got := range []string{read, parse} {
		if !strings.Contains(got, "IF-MIB") {
			t.Errorf("Error() = %q, want it to name the module", got)
		}
	}

	var target *ModuleLoadError
	err := error(&ModuleLoadError{Module: "IF-MIB", Op: "parse", Err: errSentinel})
	if !errors.As(err, &target) || target.Module != "IF-MIB" {
		t.Error("the error does not survive errors.As with its module intact")
	}
	if !errors.Is(err, errSentinel) {
		t.Error("the underlying failure is not reachable through errors.Is")
	}
}

func TestModuleNotFoundErrorNamesTheModule(t *testing.T) {
	got := (&ModuleNotFoundError{Module: "CISCO-BGP4-MIB"}).Error()

	if !strings.Contains(got, "CISCO-BGP4-MIB") {
		t.Errorf("Error() = %q, want it to name the module", got)
	}
}

// TestCloseErrorKeepsEveryReason: the parser and the store fail for unrelated
// reasons, so reporting only the first hides a resource that was not released.
func TestCloseErrorKeepsEveryReason(t *testing.T) {
	second := errors.New("store still locked")
	err := &CloseError{Errs: []error{errSentinel, second}}

	got := err.Error()
	if !strings.Contains(got, "disk on fire") || !strings.Contains(got, "store still locked") {
		t.Errorf("Error() = %q, want both reasons", got)
	}
	if !errors.Is(err, second) {
		t.Error("a collected failure is not reachable through errors.Is")
	}
}

// TestCloseErrorWithNothingCollected covers the degenerate value: a caller that
// builds one from an empty slice must still get a readable message rather than
// a panic.
func TestCloseErrorWithNothingCollected(t *testing.T) {
	if got := (&CloseError{}).Error(); got == "" {
		t.Error("Error() is empty")
	}
}
