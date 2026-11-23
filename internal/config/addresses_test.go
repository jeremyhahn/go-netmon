package config

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const addressTestBase = `
global: {default_poll_interval: 60, max_workers: 1, snmp_timeout: 5, snmp_retries: 1}
prometheus: {remote_write_url: "http://p/api/v1/write", batch_size: 1, flush_interval: 1}
logging: {level: info, format: json, output: stdout}
`

// addressCases lists every bind and target address with how to read it back.
var addressCases = []struct {
	name, key, env, def string
	get                 func(*Config) string
}{
	{"api", KeyAPIListen, "NETMON_API_LISTEN_ADDRESS", DefaultAPIListen, func(c *Config) string { return c.API.ListenAddress }},
	{"metrics", KeyMetricsListen, "NETMON_METRICS_LISTEN_ADDRESS", DefaultMetricsListen, func(c *Config) string { return c.Metrics.ListenAddress }},
	{"webui listen", KeyWebUIListen, "NETMON_WEBUI_LISTEN_ADDRESS", DefaultWebUIListen, func(c *Config) string { return c.WebUI.ListenAddress }},
	{"webui prometheus", KeyWebUIPrometheus, "NETMON_WEBUI_PROMETHEUS_URL", DefaultWebUIPrometheus, func(c *Config) string { return c.WebUI.PrometheusURL }},
	{"webui poller", KeyWebUIPoller, "NETMON_WEBUI_POLLER_URL", DefaultWebUIPoller, func(c *Config) string { return c.WebUI.PollerURL }},
	{"dashboard prometheus", KeyDashboardPromURL, "NETMON_DASHBOARD_PROMETHEUS_URL", DefaultDashboardPromURL, func(c *Config) string { return c.Dashboard.PrometheusURL }},
}

// fileWith writes the base config plus one nested key/value and returns its path.
func fileWith(t *testing.T, key, value string) string {
	t.Helper()
	body := addressTestBase
	if key != "" {
		section, field := splitKey(key)
		body += section + ":\n  " + field + ": \"" + value + "\"\n"
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func splitKey(key string) (string, string) {
	for i := range key {
		if key[i] == '.' {
			return key[:i], key[i+1:]
		}
	}
	return "", key
}

func TestAddresses_Precedence(t *testing.T) {
	for _, tc := range addressCases {
		t.Run(tc.name, func(t *testing.T) {
			// defaults layer
			cfg, err := LoadFromFile(fileWith(t, "", ""))
			require.NoError(t, err)
			assert.Equal(t, tc.def, tc.get(cfg))

			// file beats default
			path := fileWith(t, tc.key, "file:1")
			cfg, err = LoadFromFile(path)
			require.NoError(t, err)
			assert.Equal(t, "file:1", tc.get(cfg))

			// env beats file
			t.Setenv(tc.env, "env:2")
			cfg, err = LoadFromFile(path)
			require.NoError(t, err)
			assert.Equal(t, "env:2", tc.get(cfg))

			// flag beats env
			cfg, err = LoadFromFileWithOverrides(path, Overrides{tc.key: "flag:3"})
			require.NoError(t, err)
			assert.Equal(t, "flag:3", tc.get(cfg))
		})
	}
}

func TestAddresses_EnvWithoutFile(t *testing.T) {
	for _, tc := range addressCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.env, "env:9")
			cfg := &Config{}
			ApplyDefaults(cfg)
			require.NoError(t, ResolveAddresses(cfg, filepath.Join(t.TempDir(), "absent.yaml"), nil))
			assert.Equal(t, "env:9", tc.get(cfg))
		})
	}
}

func TestFlagOverrides_OnlyChangedFlags(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.String("listen", "", "")
	fs.String("poller", "", "")
	require.NoError(t, fs.Parse([]string{"-listen", ":1"}))

	o := FlagOverrides(fs, map[string]string{"listen": KeyWebUIListen, "poller": KeyWebUIPoller})
	assert.Equal(t, Overrides{KeyWebUIListen: ":1"}, o)
}
