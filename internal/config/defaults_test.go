package config

import "testing"

// A Config built as a struct literal has zero values in every section the
// literal does not mention. The web UI does exactly that when no config file is
// present, and the resulting logging section was not usable: output "" is not a
// valid output, so the service failed to start instead of falling back. Every
// section ApplyDefaults fills must therefore survive Validate.
func TestApplyDefaultsMakesAPartialConfigValid(t *testing.T) {
	cfg := &Config{
		MIB: MIBConfig{
			Paths:       []string{"/app/mibs/standard"},
			StoragePath: "data/mib.db",
		},
	}

	ApplyDefaults(cfg)

	checks := []struct {
		name string
		got  string
	}{
		{"logging.level", cfg.Logging.Level},
		{"logging.format", cfg.Logging.Format},
		{"logging.output", cfg.Logging.Output},
		{"metrics.listen_address", cfg.Metrics.ListenAddress},
	}
	for _, c := range checks {
		if c.got == "" {
			t.Errorf("%s was left empty", c.name)
		}
	}

	// The caller's own values must survive.
	if cfg.MIB.StoragePath != "data/mib.db" {
		t.Errorf("MIB.StoragePath = %q, want the caller's value", cfg.MIB.StoragePath)
	}
	if len(cfg.MIB.Paths) != 1 {
		t.Errorf("MIB.Paths = %v, want the caller's single path", cfg.MIB.Paths)
	}
}

// Defaults must not overwrite what the caller set.
func TestApplyDefaultsKeepsExplicitValues(t *testing.T) {
	cfg := &Config{
		Logging: LoggingConfig{Level: "debug", Format: "text", Output: "stdout"},
		Metrics: MetricsConfig{ListenAddress: "127.0.0.1:9999"},
	}

	ApplyDefaults(cfg)

	if cfg.Logging.Level != "debug" {
		t.Errorf("Logging.Level = %q, want debug", cfg.Logging.Level)
	}
	if cfg.Logging.Format != "text" {
		t.Errorf("Logging.Format = %q, want text", cfg.Logging.Format)
	}
	if cfg.Metrics.ListenAddress != "127.0.0.1:9999" {
		t.Errorf("Metrics.ListenAddress = %q, want the caller's value", cfg.Metrics.ListenAddress)
	}
}

// The defaulted logging section must pass the same validation the config file
// path applies, since that is the check the web UI's fallback failed. Note
// prometheus.remote_write_url is deliberately not defaulted — a poller with
// nowhere to write is a misconfiguration, not something to guess at — so this
// asserts the logging rules directly rather than whole-config validity.
func TestApplyDefaultsProducesAValidLoggingSection(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)

	validLevels := map[string]bool{"debug": true, "info": true, "warn": true, "error": true}
	if !validLevels[cfg.Logging.Level] {
		t.Errorf("Logging.Level = %q, which is not an accepted level", cfg.Logging.Level)
	}
	validFormats := map[string]bool{"json": true, "text": true}
	if !validFormats[cfg.Logging.Format] {
		t.Errorf("Logging.Format = %q, which is not an accepted format", cfg.Logging.Format)
	}
	validOutputs := map[string]bool{"stdout": true, "file": true}
	if !validOutputs[cfg.Logging.Output] {
		t.Errorf("Logging.Output = %q, which is not an accepted output", cfg.Logging.Output)
	}
	if cfg.Logging.Output == "file" && cfg.Logging.File == "" {
		t.Error("defaulting to file output without a path would fail at startup")
	}
}
