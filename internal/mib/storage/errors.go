package storage

import "fmt"

// ModuleRequiredError reports an operation that is scoped to a single module
// but was given no module name.
type ModuleRequiredError struct {
	Op string
}

func (e *ModuleRequiredError) Error() string {
	return fmt.Sprintf("mib/storage: %s requires a module name", e.Op)
}

// WriteError reports a failed write to the underlying key/value store.
type WriteError struct {
	Op    string
	Cause error
}

func (e *WriteError) Error() string {
	return fmt.Sprintf("mib/storage: %s failed: %v", e.Op, e.Cause)
}

func (e *WriteError) Unwrap() error {
	return e.Cause
}

// IndexError reports a failure to read or rebuild the module OID index.
type IndexError struct {
	Module string
	Op     string
	Cause  error
}

func (e *IndexError) Error() string {
	return fmt.Sprintf("mib/storage: module index %s for %s failed: %v", e.Op, e.Module, e.Cause)
}

func (e *IndexError) Unwrap() error {
	return e.Cause
}
