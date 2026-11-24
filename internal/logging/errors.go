package logging

import "strconv"

// LevelError reports a logging.level with no slog mapping, for callers that skip config validation.
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

// FilePathError reports logging.output "file" without a path; there is no default.
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
