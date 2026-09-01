package config

import "testing"

// TestCatalogEmissionDefaultsToOff checks that designation must be a decision
// somebody made.
//
// Defaulting to on is fail-open: every shard whose configuration predates the
// field becomes an emitter the moment it is upgraded, and because the catalog
// shares a buffer with device telemetry a rejected catalog write can delay or
// reject device samples too. The cost of failing closed is a fresh install
// without a catalog until somebody says so, and that is loud -- no catalog
// series exist and MetricCatalogNotBeingPublished fires.
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
