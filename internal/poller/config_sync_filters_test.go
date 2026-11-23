package poller

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/config"
)

func TestDeviceConfigToConfigDevice(t *testing.T) {
	tests := []struct {
		name  string
		input DeviceConfig
		want  config.Device
	}{
		{
			name: "a device with no filters carries no filter map",
			input: DeviceConfig{
				Name:         "router1",
				Address:      "198.51.100.101:161",
				SNMPVersion:  "v2c",
				Community:    "public",
				PollInterval: 30,
				Enabled:      true,
				Profile:      "cisco",
				Modules:      []string{"IF-MIB"},
				Tags:         map[string]string{"role": "router"},
			},
			want: config.Device{
				Name:         "router1",
				Address:      "198.51.100.101:161",
				SNMPVersion:  "2c",
				Community:    "public",
				PollInterval: 30,
				Enabled:      true,
				Profile:      "cisco",
				Modules:      []string{"IF-MIB"},
				Tags:         map[string]string{"role": "router"},
			},
		},
		{
			name: "module filters are carried across for every module that has one",
			input: DeviceConfig{
				Name:        "router2",
				Address:     "198.51.100.102:161",
				SNMPVersion: "v3",
				Username:    "monitor",
				Modules:     []string{"IF-MIB", "UDP-MIB"},
				ModuleFilters: map[string]ModuleFilterConfig{
					"IF-MIB": {
						IncludeNames: []string{"^ifHC"},
						ExcludeOIDs:  []string{"^1\\.3\\.6\\.1\\.2\\.1\\.2\\.2\\.1\\.1"},
						MaxOIDs:      12,
					},
				},
			},
			want: config.Device{
				Name:        "router2",
				Address:     "198.51.100.102:161",
				SNMPVersion: "3",
				Username:    "monitor",
				Modules:     []string{"IF-MIB", "UDP-MIB"},
				ModuleFilters: map[string]config.ModuleFilter{
					"IF-MIB": {
						IncludeNames: []string{"^ifHC"},
						ExcludeOIDs:  []string{"^1\\.3\\.6\\.1\\.2\\.1\\.2\\.2\\.1\\.1"},
						MaxOIDs:      12,
					},
				},
			},
		},
		{
			name:  "an empty device converts without inventing values",
			input: DeviceConfig{},
			want:  config.Device{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.input.toConfigDevice()
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("toConfigDevice() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestToConfigModuleFilters(t *testing.T) {
	tests := []struct {
		name  string
		input map[string]ModuleFilterConfig
		want  map[string]config.ModuleFilter
	}{
		{
			name:  "no filters",
			input: nil,
			want:  nil,
		},
		{
			name:  "an empty map is treated as no filters",
			input: map[string]ModuleFilterConfig{},
			want:  nil,
		},
		{
			name: "each field is carried across",
			input: map[string]ModuleFilterConfig{
				"IF-MIB": {
					IncludeOIDs:  []string{"^1\\.3\\.6"},
					ExcludeOIDs:  []string{"^1\\.3\\.6\\.1\\.2\\.1\\.2\\.2\\.1\\.1"},
					IncludeNames: []string{"^ifIn"},
					ExcludeNames: []string{"Discards$"},
					MaxOIDs:      5,
				},
			},
			want: map[string]config.ModuleFilter{
				"IF-MIB": {
					IncludeOIDs:  []string{"^1\\.3\\.6"},
					ExcludeOIDs:  []string{"^1\\.3\\.6\\.1\\.2\\.1\\.2\\.2\\.1\\.1"},
					IncludeNames: []string{"^ifIn"},
					ExcludeNames: []string{"Discards$"},
					MaxOIDs:      5,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toConfigModuleFilters(tt.input)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("toConfigModuleFilters() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestPollerConfigResponseModuleFiltersRoundTrip checks that the filters WebUI
// serves survive decoding and reach the device configuration the poller polls
// from, since that is the whole path a narrowed module travels.
func TestPollerConfigResponseModuleFiltersRoundTrip(t *testing.T) {
	body := []byte(`{
		"devices": [{
			"name": "router1",
			"address": "198.51.100.101:161",
			"snmp_version": "v2c",
			"community": "public",
			"poll_interval": 30,
			"enabled": true,
			"modules": ["IF-MIB"],
			"module_filters": {
				"IF-MIB": {"include_names": ["^ifHC"], "max_oids": 4}
			}
		}],
		"mib_modules": [],
		"version": 7
	}`)

	var resp ConfigResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("failed to decode configuration: %v", err)
	}

	cfg := resp.ToConfig(&config.Config{})
	if len(cfg.Devices) != 1 {
		t.Fatalf("devices = %d, want 1", len(cfg.Devices))
	}

	filter, ok := cfg.Devices[0].ModuleFilters["IF-MIB"]
	if !ok {
		t.Fatalf("module filters = %+v, want an entry for IF-MIB", cfg.Devices[0].ModuleFilters)
	}
	if !reflect.DeepEqual(filter.IncludeNames, []string{"^ifHC"}) || filter.MaxOIDs != 4 {
		t.Errorf("filter = %+v, want the include pattern and cap from the response", filter)
	}
}

// TestPollerConfigResponseRejectsMalformedBody guards the decode path the sync
// client depends on.
func TestPollerConfigResponseRejectsMalformedBody(t *testing.T) {
	var resp ConfigResponse
	if err := json.Unmarshal([]byte(`{"devices": "not a list"}`), &resp); err == nil {
		t.Fatal("decoding a malformed configuration succeeded, want an error")
	}
}
