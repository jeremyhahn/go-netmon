package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Prometheus target the generator queries resolves flag > env > file >
// default.
func TestBuilderConfig_PrometheusURLPrecedence(t *testing.T) {
	newFS := func(args ...string) *flag.FlagSet {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		addPrometheusFlag(fs)
		require.NoError(t, fs.Parse(args))
		return fs
	}

	missing := filepath.Join(t.TempDir(), "absent.yaml")
	t.Setenv("CONFIG_PATH", missing)

	cfg, err := builderConfig(newFS())
	require.NoError(t, err)
	assert.Equal(t, "http://prometheus:9090", cfg.PrometheusURL, "defaults layer")

	file := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(file, []byte("dashboard:\n  prometheus_url: http://file:1\n"), 0o600))
	t.Setenv("CONFIG_PATH", file)
	cfg, err = builderConfig(newFS())
	require.NoError(t, err)
	assert.Equal(t, "http://file:1", cfg.PrometheusURL, "file beats default")

	t.Setenv("NETMON_DASHBOARD_PROMETHEUS_URL", "http://env:2")
	cfg, err = builderConfig(newFS())
	require.NoError(t, err)
	assert.Equal(t, "http://env:2", cfg.PrometheusURL, "env beats file")

	cfg, err = builderConfig(newFS("-prometheus-url", "http://flag:3"))
	require.NoError(t, err)
	assert.Equal(t, "http://flag:3", cfg.PrometheusURL, "flag beats env")
}
