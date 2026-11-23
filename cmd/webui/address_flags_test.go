package main

import (
	"flag"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-netmon/internal/config"
)

// A flag the operator set overrides the environment and the file; one they
// left alone does not.
func TestAddressOverrides_FlagWinsOnlyWhenSet(t *testing.T) {
	saved := flag.CommandLine
	t.Cleanup(func() { flag.CommandLine = saved })

	fs := flag.NewFlagSet("webui", flag.ContinueOnError)
	fs.String("listen", "", "")
	fs.String("prometheus", "", "")
	fs.String("poller", "", "")
	require.NoError(t, fs.Parse([]string{"-listen", ":7777"}))
	flag.CommandLine = fs

	t.Setenv("NETMON_WEBUI_LISTEN_ADDRESS", ":1")
	t.Setenv("NETMON_WEBUI_POLLER_URL", "http://env-poller:1")

	cfg := &config.Config{}
	config.ApplyDefaults(cfg)
	require.NoError(t, config.ResolveAddresses(cfg, "", addressOverrides()))

	assert.Equal(t, ":7777", cfg.WebUI.ListenAddress, "flag beats env")
	assert.Equal(t, "http://env-poller:1", cfg.WebUI.PollerURL, "unset flag leaves env")
	assert.Equal(t, config.DefaultWebUIPrometheus, cfg.WebUI.PrometheusURL, "default")
}
