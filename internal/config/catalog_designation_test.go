package config

import "testing"

// TestCatalogEmissionDefaultsToOff checks that catalog emission is off unless configured.
func TestCatalogEmissionDefaultsToOff(t *testing.T) {
	var cfg PrometheusConfig
	if cfg.CatalogEmissionEnabled() {
		t.Error("a configuration that says nothing publishes the catalog anyway; " +
			"an upgraded shard would silently become a second emitter")
	}
}

// TestCatalogEmissionEnabledRespectsAnExplicitChoice checks that a sharded
// fleet can turn it off everywhere but one collector.
func TestCatalogEmissionEnabledRespectsAnExplicitChoice(t *testing.T) {
	tests := []struct {
		name string
		set  bool
		want bool
	}{
		{"designated emitter", true, true},
		{"not the emitter", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value := tt.set
			cfg := PrometheusConfig{EmitMetricCatalog: &value}
			if got := cfg.CatalogEmissionEnabled(); got != tt.want {
				t.Errorf("CatalogEmissionEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}
