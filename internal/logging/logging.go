// Package logging builds the structured logger from the configuration's logging section.
package logging

import (
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/jeremyhahn/go-netmon/internal/config"
)

// logFileMode is the permission for new log files: owner write, group read.
const logFileMode os.FileMode = 0o640

// New builds a logger from cfg and a closer that releases the log file (no-op for stdout).
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

// Discard returns a logger that drops every record, the fallback when none was injected.
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
