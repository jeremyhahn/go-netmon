package storage

import (
	"errors"
	"testing"
)

// TestStorageErrors_Messages verifies each error type names what failed, so a
// log line identifies the operation without a stack trace.
func TestStorageErrors_Messages(t *testing.T) {
	cause := errors.New("disk full")

	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "module required",
			err:  &ModuleRequiredError{Op: "ListModuleOIDs"},
			want: "mib/storage: ListModuleOIDs requires a module name",
		},
		{
			name: "write",
			err:  &WriteError{Op: "store OID 1.3.6.1", Cause: cause},
			want: "mib/storage: store OID 1.3.6.1 failed: disk full",
		},
		{
			name: "index",
			err:  &IndexError{Module: "IF-MIB", Op: "backfill", Cause: cause},
			want: "mib/storage: module index backfill for IF-MIB failed: disk full",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestStorageErrors_Unwrap verifies the wrapping errors expose their cause, so
// callers can test for the underlying failure rather than matching strings.
func TestStorageErrors_Unwrap(t *testing.T) {
	cause := errors.New("closed")

	for _, err := range []error{
		&WriteError{Op: "store OID", Cause: cause},
		&IndexError{Module: "IF-MIB", Op: "write", Cause: cause},
	} {
		if !errors.Is(err, cause) {
			t.Errorf("%T does not unwrap to its cause", err)
		}
	}

	// A missing module name has no underlying cause to unwrap.
	if errors.Unwrap(&ModuleRequiredError{Op: "ListModuleOIDs"}) != nil {
		t.Error("ModuleRequiredError should not wrap anything")
	}
}
