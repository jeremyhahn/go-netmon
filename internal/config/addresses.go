package config

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"strings"

	"github.com/spf13/viper"
)

// EnvPrefix prefixes the environment variable of every configuration key:
// api.listen_address is NETMON_API_LISTEN_ADDRESS.
const EnvPrefix = "NETMON"

// Keys of the bind and target addresses. Each is settable by the config file,
// by the environment variable derived from it and by a command-line flag.
const (
	KeyAPIListen        = "api.listen_address"
	KeyMetricsListen    = "metrics.listen_address"
	KeyWebUIListen      = "webui.listen_address"
	KeyWebUIPrometheus  = "webui.prometheus_url"
	KeyWebUIPoller      = "webui.poller_url"
	KeyDashboardPromURL = "dashboard.prometheus_url"
)

// The defaults layer. These are the only place an address literal lives; every
// listener and dialer takes the resolved value from the configuration.
//
// 9100 is what prometheus.yml scrapes, what docker-compose publishes and what
// the shipped configs set, so a deployment that omits the key listens where
// something is looking.
const (
	DefaultAPIListen         = "0.0.0.0:8080"
	DefaultMetricsListen     = "0.0.0.0:9100"
	DefaultWebUIListen       = ":8080"
	DefaultWebUIPrometheus   = "http://localhost:9091"
	DefaultWebUIPoller       = "http://localhost:9080"
	DefaultDashboardPromURL  = "http://prometheus:9090"
	addressKeyEnvReplacement = "_"
)

// Overrides carries the values of command-line flags the operator actually
// set, keyed by configuration key. It is the flag layer, which wins over the
// environment, the file and the defaults.
type Overrides map[string]string

// FlagOverrides builds Overrides from a parsed flag set. bindings maps a flag
// name to a configuration key. Only flags that were set on the command line are
// included, so an unset flag never masks the environment or the file.
func FlagOverrides(flags *flag.FlagSet, bindings map[string]string) Overrides {
	o := Overrides{}
	flags.Visit(func(f *flag.Flag) {
		if key, ok := bindings[f.Name]; ok {
			o[key] = f.Value.String()
		}
	})
	return o
}

// Settings resolves the address keys with the precedence flags > environment >
// file > defaults. A file that does not exist is not an error here (the keys
// then resolve from the lower layers); one that exists but cannot be parsed is.
func Settings(path string, o Overrides) (*viper.Viper, error) {
	v := viper.New()
	v.SetEnvPrefix(EnvPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", addressKeyEnvReplacement))
	v.AutomaticEnv()

	v.SetDefault(KeyAPIListen, DefaultAPIListen)
	v.SetDefault(KeyMetricsListen, DefaultMetricsListen)
	v.SetDefault(KeyWebUIListen, DefaultWebUIListen)
	v.SetDefault(KeyWebUIPrometheus, DefaultWebUIPrometheus)
	v.SetDefault(KeyWebUIPoller, DefaultWebUIPoller)
	v.SetDefault(KeyDashboardPromURL, DefaultDashboardPromURL)

	if path != "" {
		v.SetConfigFile(path)
		v.SetConfigType("yaml")
		if err := v.ReadInConfig(); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
	}

	for key, value := range o {
		// An empty flag value is as good as unset: it would blank an address.
		if value != "" {
			v.Set(key, value)
		}
	}
	return v, nil
}

// ResolveAddresses overlays the resolved address keys onto cfg.
func ResolveAddresses(cfg *Config, path string, o Overrides) error {
	v, err := Settings(path, o)
	if err != nil {
		return err
	}
	// An address the file sets to the empty string falls back to the default
	// already in cfg rather than blanking it.
	set := func(dst *string, key string) {
		if s := v.GetString(key); s != "" {
			*dst = s
		}
	}
	set(&cfg.API.ListenAddress, KeyAPIListen)
	set(&cfg.Metrics.ListenAddress, KeyMetricsListen)
	set(&cfg.WebUI.ListenAddress, KeyWebUIListen)
	set(&cfg.WebUI.PrometheusURL, KeyWebUIPrometheus)
	set(&cfg.WebUI.PollerURL, KeyWebUIPoller)
	set(&cfg.Dashboard.PrometheusURL, KeyDashboardPromURL)
	return nil
}

// Path returns the configuration file path in effect (CONFIG_PATH or the
// default).
func Path() string {
	return getConfigPath()
}
