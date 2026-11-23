package logging

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/config"
)

// levelConfig is a valid configuration at the named level, writing JSON.
func levelConfig(level string) config.LoggingConfig {
	return config.LoggingConfig{Level: level, Format: "json", Output: "stdout"}
}

func TestParseLevel(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want slog.Level
	}{
		{"debug", "debug", slog.LevelDebug},
		{"info", "info", slog.LevelInfo},
		{"warn", "warn", slog.LevelWarn},
		{"error", "error", slog.LevelError},
		{"case is not significant", "DEBUG", slog.LevelDebug},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseLevel(tt.in)
			if err != nil {
				t.Fatalf("ParseLevel(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ParseLevel(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseLevelRejectsUnknown(t *testing.T) {
	for _, in := range []string{"", "trace", "verbose", "INFORMATIONAL"} {
		_, err := ParseLevel(in)
		var levelErr *LevelError
		if !errors.As(err, &levelErr) {
			t.Fatalf("ParseLevel(%q) error = %v, want *LevelError", in, err)
		}
		if levelErr.Level != in {
			t.Errorf("LevelError.Level = %q, want %q", levelErr.Level, in)
		}
	}
}

// TestLevelsAcceptedMatchConfigValidation pins the logger's accepted level set
// to the one config validation enforces, so the two can never drift.
func TestLevelsAcceptedMatchConfigValidation(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error"} {
		cfg := &config.Config{
			Global:     config.GlobalConfig{DefaultPollInterval: 30, MaxWorkers: 1, SNMPTimeout: 1, SNMPRetries: 0},
			Prometheus: config.PrometheusConfig{RemoteWriteURL: "http://x", BatchSize: 1, FlushInterval: 1},
			API:        config.APIConfig{ListenAddress: ":1"},
			Metrics:    config.MetricsConfig{ListenAddress: ":2"},
			Logging:    levelConfig(level),
		}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("config rejects level %q that logging accepts: %v", level, err)
		}
		if _, err := ParseLevel(level); err != nil {
			t.Fatalf("logging rejects level %q that config accepts: %v", level, err)
		}
	}

	// The reverse: a level config rejects must not build a logger.
	if _, err := NewWithWriter(levelConfig("trace"), &bytes.Buffer{}); err == nil {
		t.Fatal("NewWithWriter accepted level \"trace\" that config validation rejects")
	}
}

// TestLevelGatesRecords is the guarantee the whole change rests on: a debug
// statement is silent at the default level and speaks at "debug".
func TestLevelGatesRecords(t *testing.T) {
	tests := []struct {
		name      string
		level     string
		wantDebug bool
		wantInfo  bool
		wantWarn  bool
	}{
		{"debug emits everything", "debug", true, true, true},
		{"info drops debug", "info", false, true, true},
		{"warn drops debug and info", "warn", false, false, true},
		{"error drops all three", "error", false, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger, err := NewWithWriter(levelConfig(tt.level), &buf)
			if err != nil {
				t.Fatalf("NewWithWriter: %v", err)
			}

			logger.Debug("a-debug-record")
			logger.Info("an-info-record")
			logger.Warn("a-warn-record")

			out := buf.String()
			if got := strings.Contains(out, "a-debug-record"); got != tt.wantDebug {
				t.Errorf("debug present = %v, want %v (output %q)", got, tt.wantDebug, out)
			}
			if got := strings.Contains(out, "an-info-record"); got != tt.wantInfo {
				t.Errorf("info present = %v, want %v (output %q)", got, tt.wantInfo, out)
			}
			if got := strings.Contains(out, "a-warn-record"); got != tt.wantWarn {
				t.Errorf("warn present = %v, want %v (output %q)", got, tt.wantWarn, out)
			}
		})
	}
}

// TestEnabledReportsGating lets call sites skip building expensive arguments.
func TestEnabledReportsGating(t *testing.T) {
	info, err := NewWithWriter(levelConfig("info"), &bytes.Buffer{})
	if err != nil {
		t.Fatalf("NewWithWriter: %v", err)
	}
	if info.Enabled(t.Context(), slog.LevelDebug) {
		t.Error("debug reported enabled at level info")
	}

	debug, err := NewWithWriter(levelConfig("debug"), &bytes.Buffer{})
	if err != nil {
		t.Fatalf("NewWithWriter: %v", err)
	}
	if !debug.Enabled(t.Context(), slog.LevelDebug) {
		t.Error("debug reported disabled at level debug")
	}
}

func TestFormatJSONIsMachineReadable(t *testing.T) {
	var buf bytes.Buffer
	logger, err := NewWithWriter(config.LoggingConfig{Level: "info", Format: "json", Output: "stdout"}, &buf)
	if err != nil {
		t.Fatalf("NewWithWriter: %v", err)
	}

	logger.Info("device polled", "device", "bastion", "oids", 12)

	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &record); err != nil {
		t.Fatalf("output is not JSON (%q): %v", buf.String(), err)
	}
	if record["msg"] != "device polled" || record["device"] != "bastion" {
		t.Errorf("record = %v, want msg and device attributes", record)
	}
}

func TestFormatTextIsKeyValue(t *testing.T) {
	var buf bytes.Buffer
	logger, err := NewWithWriter(config.LoggingConfig{Level: "info", Format: "text", Output: "stdout"}, &buf)
	if err != nil {
		t.Fatalf("NewWithWriter: %v", err)
	}

	logger.Info("device polled", "device", "bastion")

	out := buf.String()
	if !strings.Contains(out, "device=bastion") {
		t.Errorf("text output = %q, want a device=bastion pair", out)
	}
	if json.Valid(bytes.TrimSpace(buf.Bytes())) {
		t.Errorf("text output %q parsed as JSON", out)
	}
}

func TestFormatRejectsUnknown(t *testing.T) {
	for _, format := range []string{"", "logfmt", "xml"} {
		_, err := NewWithWriter(config.LoggingConfig{Level: "info", Format: format, Output: "stdout"}, &bytes.Buffer{})
		var formatErr *FormatError
		if !errors.As(err, &formatErr) {
			t.Fatalf("format %q error = %v, want *FormatError", format, err)
		}
		if formatErr.Format != format {
			t.Errorf("FormatError.Format = %q, want %q", formatErr.Format, format)
		}
	}
}

func TestNewWritesToConfiguredFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "netmon.log")

	logger, closer, err := New(config.LoggingConfig{
		Level: "info", Format: "json", Output: "file", File: path,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	logger.Info("written to file", "device", "bastion")
	if closeErr := closer.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(content), "written to file") {
		t.Errorf("log file = %q, want the record", content)
	}
}

// TestNewFileAppends keeps a restart from truncating the operator's history.
func TestNewFileAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "netmon.log")
	cfg := config.LoggingConfig{Level: "info", Format: "json", Output: "file", File: path}

	for _, msg := range []string{"first-run", "second-run"} {
		logger, closer, newErr := New(cfg)
		if newErr != nil {
			t.Fatalf("New: %v", newErr)
		}
		logger.Info(msg)
		if closeErr := closer.Close(); closeErr != nil {
			t.Fatalf("Close: %v", closeErr)
		}
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(content), "first-run") || !strings.Contains(string(content), "second-run") {
		t.Errorf("log file = %q, want both runs", content)
	}
}

func TestNewStdoutClosesCleanly(t *testing.T) {
	logger, closer, err := New(levelConfig("info"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if logger == nil {
		t.Fatal("New returned a nil logger")
	}
	// Closing must not close os.Stdout out from under the process.
	if err := closer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stdout.Stat(); err != nil {
		t.Fatalf("os.Stdout was closed: %v", err)
	}
}

func TestNewRejectsBadOutput(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.LoggingConfig
		want any
	}{
		{
			name: "unknown sink",
			cfg:  config.LoggingConfig{Level: "info", Format: "json", Output: "syslog"},
			want: new(*OutputError),
		},
		{
			name: "empty sink",
			cfg:  config.LoggingConfig{Level: "info", Format: "json", Output: ""},
			want: new(*OutputError),
		},
		{
			name: "file without a path",
			cfg:  config.LoggingConfig{Level: "info", Format: "json", Output: "file"},
			want: new(*FilePathError),
		},
		{
			name: "unopenable path",
			cfg:  config.LoggingConfig{Level: "info", Format: "json", Output: "file", File: filepath.Join("no", "such", "dir", "x.log")},
			want: new(*FileOpenError),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, closer, err := New(tt.cfg)
			if err == nil {
				t.Fatal("New accepted an invalid output")
			}
			if logger != nil || closer != nil {
				t.Error("New returned a logger or closer alongside an error")
			}
			switch want := tt.want.(type) {
			case **OutputError:
				if !errors.As(err, want) {
					t.Fatalf("error = %v (%T), want *OutputError", err, err)
				}
			case **FilePathError:
				if !errors.As(err, want) {
					t.Fatalf("error = %v (%T), want *FilePathError", err, err)
				}
			case **FileOpenError:
				if !errors.As(err, want) {
					t.Fatalf("error = %v (%T), want *FileOpenError", err, err)
				}
				if (*want).Err == nil {
					t.Error("FileOpenError does not wrap the cause")
				}
			}
		})
	}
}

// TestNewClosesFileWhenTheHandlerIsInvalid keeps a rejected configuration from
// leaking the descriptor it opened first.
func TestNewClosesFileWhenTheHandlerIsInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "netmon.log")

	_, _, err := New(config.LoggingConfig{Level: "info", Format: "xml", Output: "file", File: path})
	var formatErr *FormatError
	if !errors.As(err, &formatErr) {
		t.Fatalf("error = %v, want *FormatError", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("log file was not created before the failure: %v", statErr)
	}
}

func TestDiscardDropsEverything(t *testing.T) {
	logger := Discard()
	if logger.Enabled(t.Context(), slog.LevelError) {
		t.Error("the discarding logger reported error enabled")
	}
	// A record on a discarding logger must not panic.
	logger.Error("dropped", "device", "bastion")
}

func TestOrDiscard(t *testing.T) {
	if got := OrDiscard(nil); got == nil {
		t.Fatal("OrDiscard(nil) returned nil")
	}
	if OrDiscard(nil).Enabled(t.Context(), slog.LevelError) {
		t.Error("OrDiscard(nil) returned a logger that emits")
	}

	var buf bytes.Buffer
	logger, err := NewWithWriter(levelConfig("info"), &buf)
	if err != nil {
		t.Fatalf("NewWithWriter: %v", err)
	}
	if got := OrDiscard(logger); got != logger {
		t.Error("OrDiscard replaced a non-nil logger")
	}
}

func TestErrorMessages(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{&LevelError{Level: "trace"}, `logging: unsupported level "trace": must be one of: debug, info, warn, error`},
		{&FormatError{Format: "xml"}, `logging: unsupported format "xml": must be one of: json, text`},
		{&OutputError{Output: "syslog"}, `logging: unsupported output "syslog": must be one of: stdout, file`},
		{&FilePathError{}, `logging: output "file" requires logging.file to name the log file`},
		{&FileOpenError{Path: "/x.log", Err: os.ErrPermission}, `logging: open log file "/x.log": permission denied`},
	}

	for _, tt := range tests {
		if got := tt.err.Error(); got != tt.want {
			t.Errorf("Error() = %q, want %q", got, tt.want)
		}
	}

	if !errors.Is(&FileOpenError{Err: os.ErrPermission}, os.ErrPermission) {
		t.Error("FileOpenError does not unwrap to its cause")
	}
}
