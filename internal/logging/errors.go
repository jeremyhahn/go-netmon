package logging

import "strconv"

// LevelError reports a logging.level the package cannot map to a slog level.
//
// The accepted set is the one config.Config.Validate enforces, so a
// configuration that loads cleanly can never produce this error; it exists for
// callers that build a LoggingConfig themselves.
type LevelError struct {
	Level string
}

func (e *LevelError) Error() string {
	return "logging: unsupported level " + strconv.Quote(e.Level) + ": must be one of: debug, info, warn, error"
}

// FormatError reports a logging.format the package cannot map to a handler.
type FormatError struct {
	Format string
}

func (e *FormatError) Error() string {
	return "logging: unsupported format " + strconv.Quote(e.Format) + ": must be one of: json, text"
}

// OutputError reports a logging.output the package cannot map to a sink.
type OutputError struct {
	Output string
}

func (e *OutputError) Error() string {
	return "logging: unsupported output " + strconv.Quote(e.Output) + ": must be one of: stdout, file"
}

// FilePathError reports that logging.output is "file" but no path was given.
//
// There is no default: a log that silently lands somewhere the operator did not
// choose is worse than a startup failure that says which key to set.
type FilePathError struct{}

func (e *FilePathError) Error() string {
	return "logging: output \"file\" requires logging.file to name the log file"
}

// FileOpenError reports that the configured log file could not be opened.
type FileOpenError struct {
	Path string
	Err  error
}

func (e *FileOpenError) Error() string {
	return "logging: open log file " + strconv.Quote(e.Path) + ": " + e.Err.Error()
}

func (e *FileOpenError) Unwrap() error { return e.Err }
