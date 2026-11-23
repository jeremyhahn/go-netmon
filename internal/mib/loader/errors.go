package loader

import "strings"

// StoreOpenError reports a failure to open the MIB metadata store.
type StoreOpenError struct {
	Path string
	Err  error
}

func (e *StoreOpenError) Error() string {
	return "loader: open MIB store at " + e.Path + ": " + e.Err.Error()
}

func (e *StoreOpenError) Unwrap() error { return e.Err }

// StoreError reports a failure of a MIB store operation that is not tied to one
// module, such as counting what the store holds.
type StoreError struct {
	Op  string
	Err error
}

func (e *StoreError) Error() string {
	return "loader: " + e.Op + ": " + e.Err.Error()
}

func (e *StoreError) Unwrap() error { return e.Err }

// ParserInitError reports a failure to initialize the MIB parser.
type ParserInitError struct {
	Err error
}

func (e *ParserInitError) Error() string {
	return "loader: initialize MIB parser: " + e.Err.Error()
}

func (e *ParserInitError) Unwrap() error { return e.Err }

// ProfileLoadError reports a failure to load the built-in polling profiles.
type ProfileLoadError struct {
	Err error
}

func (e *ProfileLoadError) Error() string {
	return "loader: load built-in profiles: " + e.Err.Error()
}

func (e *ProfileLoadError) Unwrap() error { return e.Err }

// ModuleLoadError reports a failure to bring one MIB module into the store. Op
// names the step that failed, which is what distinguishes a module whose file
// could not be found or parsed from one the store refused to accept.
type ModuleLoadError struct {
	Module string
	Op     string
	Err    error
}

func (e *ModuleLoadError) Error() string {
	return "loader: " + e.Op + " module " + e.Module + ": " + e.Err.Error()
}

func (e *ModuleLoadError) Unwrap() error { return e.Err }

// ModuleNotFoundError reports a module the store does not hold.
type ModuleNotFoundError struct {
	Module string
}

func (e *ModuleNotFoundError) Error() string {
	return "loader: module " + e.Module + " not found"
}

// CloseError reports the failures collected while shutting the loader down. It
// keeps every one of them because closing the parser and closing the store fail
// for unrelated reasons, and reporting only the first hides the other.
type CloseError struct {
	Errs []error
}

func (e *CloseError) Error() string {
	reasons := make([]string, 0, len(e.Errs))
	for _, err := range e.Errs {
		reasons = append(reasons, err.Error())
	}
	return "loader: close: " + strings.Join(reasons, "; ")
}

// Unwrap exposes the collected failures to errors.Is and errors.As.
func (e *CloseError) Unwrap() []error { return e.Errs }
