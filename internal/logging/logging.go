// Package logging builds the application's structured logger from the
// logging section of the configuration file.
//
// The level, format and output keys have always been validated by the config
// package; this package is what makes them take effect. Everything routed
// through the returned logger is gated on the configured level, so debug
// tracing costs nothing at the default level of "info".
package logging

import (
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/jeremyhahn/go-netmon/internal/config"
)

// logFileMode is the permission new log files are created with: readable by the
// owner and its group, written only by the owner.
const logFileMode os.FileMode = 0o640

// New builds a logger from cfg, opening the log file when the output is "file".
//
// The returned closer releases the log file; it is a no-op for stdout, so a
// caller can always defer it. An error leaves nothing open.
func New(cfg config.LoggingConfig) (*slog.Logger, io.Closer, error) {
	w, closer, err := openOutput(cfg)
	if err != nil {
		return nil, nil, err
	}

	logger, err := NewWithWriter(cfg, w)
	if err != nil {
		// The sink was opened for a logger that will not exist.
		_ = closer.Close()
		return nil, nil, err
	}

	return logger, closer, nil
}

// NewWithWriter builds a logger from cfg that writes to w, ignoring cfg.Output.
//
// It is the constructor to use when the caller owns the sink — a test capturing
// output in a buffer, or a process that already multiplexes its own writer.
func NewWithWriter(cfg config.LoggingConfig, w io.Writer) (*slog.Logger, error) {
	level, err := ParseLevel(cfg.Level)
	if err != nil {
		return nil, err
	}

	opts := &slog.HandlerOptions{Level: level}

	var handler slog.Handler
	switch strings.ToLower(cfg.Format) {
	case "json":
		handler = slog.NewJSONHandler(w, opts)
	case "text":
		handler = slog.NewTextHandler(w, opts)
	default:
		return nil, &FormatError{Format: cfg.Format}
	}

	return slog.New(handler), nil
}

// ParseLevel maps a configured level name to its slog level.
//
// The accepted names are exactly those config.Config.Validate accepts, so a
// configuration that passes validation always parses here.
func ParseLevel(name string) (slog.Level, error) {
	switch strings.ToLower(name) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, &LevelError{Level: name}
	}
}

// openOutput resolves cfg.Output to a writer and the closer that releases it.
func openOutput(cfg config.LoggingConfig) (io.Writer, io.Closer, error) {
	switch strings.ToLower(cfg.Output) {
	case "stdout":
		return os.Stdout, nopCloser{}, nil
	case "file":
		if cfg.File == "" {
			return nil, nil, &FilePathError{}
		}
		// The path is the log file the operator named; opening it is the point.
		f, err := os.OpenFile(cfg.File, os.O_CREATE|os.O_WRONLY|os.O_APPEND, logFileMode) // #nosec G304 -- operator-supplied log path
		if err != nil {
			return nil, nil, &FileOpenError{Path: cfg.File, Err: err}
		}
		return f, f, nil
	default:
		return nil, nil, &OutputError{Output: cfg.Output}
	}
}

// nopCloser releases a sink this package does not own, such as os.Stdout.
type nopCloser struct{}

func (nopCloser) Close() error { return nil }

// Discard returns a logger that drops every record.
//
// It is the zero value a component should fall back to when no logger was
// injected, so that a nil logger can never panic at a call site.
func Discard() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// OrDiscard returns logger, or a discarding logger when logger is nil.
func OrDiscard(logger *slog.Logger) *slog.Logger {
	if logger == nil {
		return Discard()
	}
	return logger
}
