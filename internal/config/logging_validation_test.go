package config

import (
	"errors"
	"strings"
	"testing"
)

// validLoggingBase is a configuration that differs from a valid one only in its
// logging section, so a failure names the key under test.
func validLoggingBase(logging LoggingConfig) *Config {
	return &Config{
		Global:     GlobalConfig{DefaultPollInterval: 30, MaxWorkers: 1, SNMPTimeout: 1, SNMPRetries: 0},
		Prometheus: PrometheusConfig{RemoteWriteURL: "http://prometheus:9090", BatchSize: 1, FlushInterval: 1},
		API:        APIConfig{ListenAddress: ":9080"},
		Metrics:    MetricsConfig{ListenAddress: ":9100"},
		Logging:    logging,
	}
}

func TestLoggingValidationAccepts(t *testing.T) {
	tests := []struct {
		name string
		in   LoggingConfig
	}{
		{"every level", LoggingConfig{Level: "debug", Format: "json", Output: "stdout"}},
		{"text format", LoggingConfig{Level: "warn", Format: "text", Output: "stdout"}},
		{"a file with a path", LoggingConfig{Level: "error", Format: "json", Output: "file", File: "/var/log/netmon.log"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validLoggingBase(tt.in).Validate(); err != nil {
				t.Errorf("Validate rejected a valid logging section: %v", err)
			}
		})
	}

	for _, level := range []string{"debug", "info", "warn", "error"} {
		if err := validLoggingBase(LoggingConfig{Level: level, Format: "json", Output: "stdout"}).Validate(); err != nil {
			t.Errorf("Validate rejected level %q: %v", level, err)
		}
	}
}

func TestLoggingValidationRejects(t *testing.T) {
	tests := []struct {
		name      string
		in        LoggingConfig
		wantField string
	}{
		{"an unknown level", LoggingConfig{Level: "trace", Format: "json", Output: "stdout"}, "logging.level"},
		{"no level", LoggingConfig{Format: "json", Output: "stdout"}, "logging.level"},
		{"an unknown format", LoggingConfig{Level: "info", Format: "xml", Output: "stdout"}, "logging.format"},
		{"an unknown output", LoggingConfig{Level: "info", Format: "json", Output: "syslog"}, "logging.output"},
		{"a file output with no path", LoggingConfig{Level: "info", Format: "json", Output: "file"}, "logging.file"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validLoggingBase(tt.in).Validate()
			if err == nil {
				t.Fatal("Validate accepted an invalid logging section")
			}

			var validationErr *ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("error = %v (%T), want *ValidationError", err, err)
			}
			if validationErr.Field != tt.wantField {
				t.Errorf("Field = %q, want %q", validationErr.Field, tt.wantField)
			}
			if !strings.Contains(err.Error(), tt.wantField) {
				t.Errorf("error = %q, want it to name the key", err)
			}
		})
	}
}

// TestLoggingFileIsOnlyRequiredForFileOutput keeps a stray path from being
// treated as a requirement when the sink is stdout.
func TestLoggingFileIsOnlyRequiredForFileOutput(t *testing.T) {
	cfg := validLoggingBase(LoggingConfig{Level: "info", Format: "json", Output: "stdout", File: "/ignored.log"})
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate rejected a stdout config carrying an unused file path: %v", err)
	}
}
