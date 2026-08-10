package builder

import (
	"strings"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/grafana"
	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/promname"
)

func TestNewVariableGenerator(t *testing.T) {
	tests := []struct {
		name         string
		metricPrefix string
		wantPrefix   string
	}{
		{
			name:         "with custom prefix",
			metricPrefix: "custom_",
			wantPrefix:   "custom_",
		},
		{
			name:         "with empty prefix",
			metricPrefix: "",
			wantPrefix:   promname.Prefix,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vg := NewVariableGenerator(tt.metricPrefix)
			if vg.metricPrefix != tt.wantPrefix {
				t.Errorf("NewVariableGenerator() metricPrefix = %v, want %v", vg.metricPrefix, tt.wantPrefix)
			}
		})
	}
}

func TestVariableGenerator_GenerateDeviceVariable(t *testing.T) {
	vg := NewVariableGenerator("snmp___")

	tests := []struct {
		name        string
		oidForQuery string
		wantName    string
		wantMulti   bool
		wantAll     bool
	}{
		{
			name:        "standard device variable",
			oidForQuery: "1.3.6.1.2.1.1.1.0",
			wantName:    "device",
			wantMulti:   true,
			wantAll:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := vg.GenerateDeviceVariable(tt.oidForQuery)

			if v.Name != tt.wantName {
				t.Errorf("GenerateDeviceVariable() Name = %v, want %v", v.Name, tt.wantName)
			}

			if v.Multi != tt.wantMulti {
				t.Errorf("GenerateDeviceVariable() Multi = %v, want %v", v.Multi, tt.wantMulti)
			}

			if v.IncludeAll != tt.wantAll {
				t.Errorf("GenerateDeviceVariable() IncludeAll = %v, want %v", v.IncludeAll, tt.wantAll)
			}

			if v.Type != "query" {
				t.Errorf("GenerateDeviceVariable() Type = %v, want query", v.Type)
			}

			// GenerateDeviceVariable emits the Grafana 12.x variable query
			// object (grafana.VariableQuery), not a bare string.
			query, ok := v.Query.(grafana.VariableQuery)
			if !ok {
				t.Error("GenerateDeviceVariable() Query is not a grafana.VariableQuery")
			} else if !strings.Contains(query.Query, "label_values") {
				t.Errorf("GenerateDeviceVariable() Query.Query = %v, want to contain label_values", query.Query)
			}

			if v.DataSource == nil {
				t.Error("GenerateDeviceVariable() DataSource is nil")
			} else if v.DataSource.Type != "prometheus" {
				t.Errorf("GenerateDeviceVariable() DataSource.Type = %v, want prometheus", v.DataSource.Type)
			}
		})
	}
}

func TestVariableGenerator_GenerateTableIndexVariable(t *testing.T) {
	vg := NewVariableGenerator("snmp___")

	tests := []struct {
		name        string
		column      *mib.OIDMetadata
		indexColumn string
		wantName    string
		wantQuery   string
	}{
		{
			name:        "interface index",
			column:      &mib.OIDMetadata{OID: ".1.3.6.1.2.1.2.2.1.10", Name: "ifInOctets", IsColumn: true},
			indexColumn: "ifIndex",
			wantName:    "ifindex",
			wantQuery:   "label_values(snmp___ifInOctets, ifindex)",
		},
		{
			name:        "composite index picks the named column",
			column:      &mib.OIDMetadata{OID: ".1.3.6.1.4.1.9.9.187.1.2.5.1.3", Name: "cbgpPeer2State", IsColumn: true},
			indexColumn: "cbgpPeer2RemoteAddr",
			wantName:    "cbgppeer2remoteaddr",
			wantQuery:   "label_values(snmp___cbgpPeer2State, cbgppeer2remoteaddr)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := vg.GenerateTableIndexVariable(tt.column, tt.indexColumn)

			if v.Name != tt.wantName {
				t.Errorf("GenerateTableIndexVariable() Name = %v, want %v", v.Name, tt.wantName)
			}

			if v.Type != "query" {
				t.Errorf("GenerateTableIndexVariable() Type = %v, want query", v.Type)
			}

			queryStr, ok := v.Query.(string)
			if !ok {
				t.Fatal("GenerateTableIndexVariable() Query is not a string")
			}
			if queryStr != tt.wantQuery {
				t.Errorf("GenerateTableIndexVariable() Query = %v, want %v", queryStr, tt.wantQuery)
			}
			if strings.Contains(queryStr, "__name__") {
				t.Errorf("GenerateTableIndexVariable() Query = %v, still selects on __name__", queryStr)
			}
		})
	}
}

// TestVariableGenerator_GenerateTableIndexVariableEmptyIndex covers an index
// column the MIB did not name: the variable still has to be a legal Prometheus
// query rather than one with an empty label.
func TestVariableGenerator_GenerateTableIndexVariableEmptyIndex(t *testing.T) {
	vg := NewVariableGenerator("")
	v := vg.GenerateTableIndexVariable(&mib.OIDMetadata{Name: "ifInOctets", IsColumn: true}, "")

	queryStr, ok := v.Query.(string)
	if !ok {
		t.Fatal("GenerateTableIndexVariable() Query is not a string")
	}
	if want := "label_values(" + promname.MetricName("ifInOctets") + ", )"; queryStr != want {
		t.Errorf("GenerateTableIndexVariable() Query = %v, want %v", queryStr, want)
	}
}

func TestVariableGenerator_GenerateIntervalVariable(t *testing.T) {
	vg := NewVariableGenerator("snmp___")

	v := vg.GenerateIntervalVariable()

	if v.Name != "interval" {
		t.Errorf("GenerateIntervalVariable() Name = %v, want interval", v.Name)
	}

	if v.Type != "interval" {
		t.Errorf("GenerateIntervalVariable() Type = %v, want interval", v.Type)
	}

	if len(v.Options) == 0 {
		t.Error("GenerateIntervalVariable() has no options")
	}

	// Check that 5m is the default
	if v.Current == nil {
		t.Error("GenerateIntervalVariable() Current is nil")
	} else if v.Current.Value != "5m" {
		t.Errorf("GenerateIntervalVariable() Current.Value = %v, want 5m", v.Current.Value)
	}

	// Verify options contain expected intervals
	expectedIntervals := []string{"1m", "5m", "10m", "30m", "1h"}
	for _, expected := range expectedIntervals {
		found := false
		for _, opt := range v.Options {
			if opt.Value == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("GenerateIntervalVariable() missing option %v", expected)
		}
	}
}

func TestVariableGenerator_GenerateCustomVariable(t *testing.T) {
	vg := NewVariableGenerator("snmp___")

	tests := []struct {
		name       string
		varName    string
		label      string
		options    []grafana.VariableOption
		multi      bool
		includeAll bool
	}{
		{
			name:    "single select variable",
			varName: "status",
			label:   "Status",
			options: []grafana.VariableOption{
				{Text: "Active", Value: "1", Selected: true},
				{Text: "Inactive", Value: "0", Selected: false},
			},
			multi:      false,
			includeAll: false,
		},
		{
			name:    "multi select variable",
			varName: "types",
			label:   "Types",
			options: []grafana.VariableOption{
				{Text: "Type A", Value: "a", Selected: false},
				{Text: "Type B", Value: "b", Selected: false},
			},
			multi:      true,
			includeAll: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := vg.GenerateCustomVariable(tt.varName, tt.label, tt.options, tt.multi, tt.includeAll)

			if v.Name != tt.varName {
				t.Errorf("GenerateCustomVariable() Name = %v, want %v", v.Name, tt.varName)
			}

			if v.Label != tt.label {
				t.Errorf("GenerateCustomVariable() Label = %v, want %v", v.Label, tt.label)
			}

			if v.Type != "custom" {
				t.Errorf("GenerateCustomVariable() Type = %v, want custom", v.Type)
			}

			if v.Multi != tt.multi {
				t.Errorf("GenerateCustomVariable() Multi = %v, want %v", v.Multi, tt.multi)
			}

			if v.IncludeAll != tt.includeAll {
				t.Errorf("GenerateCustomVariable() IncludeAll = %v, want %v", v.IncludeAll, tt.includeAll)
			}

			if len(v.Options) != len(tt.options) {
				t.Errorf("GenerateCustomVariable() option count = %v, want %v", len(v.Options), len(tt.options))
			}
		})
	}
}

func TestVariableGenerator_GenerateConstantVariable(t *testing.T) {
	vg := NewVariableGenerator("snmp___")

	tests := []struct {
		name     string
		varName  string
		value    string
		hide     bool
		wantHide int
	}{
		{
			name:     "visible constant",
			varName:  "prefix",
			value:    "snmp__",
			hide:     false,
			wantHide: 0,
		},
		{
			name:     "hidden constant",
			varName:  "secret",
			value:    "hidden_value",
			hide:     true,
			wantHide: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := vg.GenerateConstantVariable(tt.varName, tt.value, tt.hide)

			if v.Name != tt.varName {
				t.Errorf("GenerateConstantVariable() Name = %v, want %v", v.Name, tt.varName)
			}

			if v.Type != "constant" {
				t.Errorf("GenerateConstantVariable() Type = %v, want constant", v.Type)
			}

			queryStr, ok := v.Query.(string)
			if !ok {
				t.Error("GenerateConstantVariable() Query is not a string")
			} else if queryStr != tt.value {
				t.Errorf("GenerateConstantVariable() Query = %v, want %v", queryStr, tt.value)
			}

			if v.Hide != tt.wantHide {
				t.Errorf("GenerateConstantVariable() Hide = %v, want %v", v.Hide, tt.wantHide)
			}

			if v.Current == nil {
				t.Error("GenerateConstantVariable() Current is nil")
			}
		})
	}
}

func TestVariableGenerator_GenerateTextBoxVariable(t *testing.T) {
	vg := NewVariableGenerator("snmp___")

	tests := []struct {
		name         string
		varName      string
		label        string
		defaultValue string
	}{
		{
			name:         "text box variable",
			varName:      "threshold",
			label:        "Threshold",
			defaultValue: "80",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := vg.GenerateTextBoxVariable(tt.varName, tt.label, tt.defaultValue)

			if v.Name != tt.varName {
				t.Errorf("GenerateTextBoxVariable() Name = %v, want %v", v.Name, tt.varName)
			}

			if v.Label != tt.label {
				t.Errorf("GenerateTextBoxVariable() Label = %v, want %v", v.Label, tt.label)
			}

			if v.Type != "textbox" {
				t.Errorf("GenerateTextBoxVariable() Type = %v, want textbox", v.Type)
			}

			queryStr, ok := v.Query.(string)
			if !ok {
				t.Error("GenerateTextBoxVariable() Query is not a string")
			} else if queryStr != tt.defaultValue {
				t.Errorf("GenerateTextBoxVariable() Query = %v, want %v", queryStr, tt.defaultValue)
			}
		})
	}
}

func TestVariableGenerator_GenerateAdHocVariable(t *testing.T) {
	vg := NewVariableGenerator("snmp___")

	v := vg.GenerateAdHocVariable()

	if v.Name != "filters" {
		t.Errorf("GenerateAdHocVariable() Name = %v, want filters", v.Name)
	}

	if v.Type != "adhoc" {
		t.Errorf("GenerateAdHocVariable() Type = %v, want adhoc", v.Type)
	}

	if v.DataSource == nil {
		t.Error("GenerateAdHocVariable() DataSource is nil")
	} else if v.DataSource.Type != "prometheus" {
		t.Errorf("GenerateAdHocVariable() DataSource.Type = %v, want prometheus", v.DataSource.Type)
	}
}

func TestVariableGenerator_metricName(t *testing.T) {
	vg := NewVariableGenerator("snmp__")

	tests := []struct {
		name   string
		object *mib.OIDMetadata
		want   string
	}{
		{
			name:   "scalar",
			object: &mib.OIDMetadata{OID: ".1.3.6.1.2.1.1.1.0", Name: "sysDescr"},
			want:   "snmp__sysDescr",
		},
		{
			name:   "column names the object, not the row",
			object: &mib.OIDMetadata{OID: ".1.3.6.1.2.1.2.2.1.10", Name: "ifInOctets", IsColumn: true},
			want:   "snmp__ifInOctets",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := vg.metricName(tt.object)
			if got != tt.want {
				t.Errorf("metricName() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestVariableGenerator_GenerateDefaultVariables(t *testing.T) {
	vg := NewVariableGenerator("snmp___")

	sampleOID := "1.3.6.1.2.1.1.1.0"
	vars := vg.GenerateDefaultVariables(sampleOID)

	if len(vars) != 2 {
		t.Errorf("GenerateDefaultVariables() returned %d variables, want 2", len(vars))
	}

	// Check for device variable
	foundDevice := false
	foundInterval := false
	for _, v := range vars {
		if v.Name == "device" {
			foundDevice = true
		}
		if v.Name == "interval" {
			foundInterval = true
		}
	}

	if !foundDevice {
		t.Error("GenerateDefaultVariables() missing device variable")
	}

	if !foundInterval {
		t.Error("GenerateDefaultVariables() missing interval variable")
	}
}

func TestVariableGenerator_GenerateTableVariables(t *testing.T) {
	vg := NewVariableGenerator("snmp___")

	sampleOID := "1.3.6.1.2.1.1.1.0"
	tableOID := "1.3.6.1.2.1.2.2"
	vars := vg.GenerateTableVariables(sampleOID, tableOID)

	if len(vars) != 3 {
		t.Errorf("GenerateTableVariables() returned %d variables, want 3", len(vars))
	}

	// Check for all required variables. GenerateTableVariables filters table
	// rows by interface name (the "interface" variable), not by raw row
	// index, consistent with BuildColumnQuery's ifname/$interface filter.
	foundDevice := false
	foundInterface := false
	foundInterval := false
	for _, v := range vars {
		if v.Name == "device" {
			foundDevice = true
		}
		if v.Name == "interface" {
			foundInterface = true
		}
		if v.Name == "interval" {
			foundInterval = true
		}
	}

	if !foundDevice {
		t.Error("GenerateTableVariables() missing device variable")
	}

	if !foundInterface {
		t.Error("GenerateTableVariables() missing interface variable")
	}

	if !foundInterval {
		t.Error("GenerateTableVariables() missing interval variable")
	}
}

func TestVariableGenerator_CustomVariableWithEmptyOptions(t *testing.T) {
	vg := NewVariableGenerator("snmp___")

	v := vg.GenerateCustomVariable("test", "Test", []grafana.VariableOption{}, false, false)

	if v.Current != nil {
		t.Error("GenerateCustomVariable() with empty options should have nil Current")
	}
}

func TestVariableGenerator_CustomVariableWithSelectedOption(t *testing.T) {
	vg := NewVariableGenerator("snmp___")

	options := []grafana.VariableOption{
		{Text: "Option 1", Value: "1", Selected: false},
		{Text: "Option 2", Value: "2", Selected: true},
		{Text: "Option 3", Value: "3", Selected: false},
	}

	v := vg.GenerateCustomVariable("test", "Test", options, false, false)

	if v.Current == nil {
		t.Fatal("GenerateCustomVariable() Current is nil")
	}

	if v.Current.Value != "2" {
		t.Errorf("GenerateCustomVariable() Current.Value = %v, want 2", v.Current.Value)
	}
}

func TestVariableGenerator_CustomVariableWithNoSelected(t *testing.T) {
	vg := NewVariableGenerator("snmp___")

	options := []grafana.VariableOption{
		{Text: "Option 1", Value: "1", Selected: false},
		{Text: "Option 2", Value: "2", Selected: false},
	}

	v := vg.GenerateCustomVariable("test", "Test", options, false, false)

	if v.Current == nil {
		t.Fatal("GenerateCustomVariable() Current is nil")
	}

	// Should default to first option
	if v.Current.Value != "1" {
		t.Errorf("GenerateCustomVariable() Current.Value = %v, want 1 (first option)", v.Current.Value)
	}
}

// Benchmark tests
func BenchmarkVariableGenerator_GenerateDeviceVariable(b *testing.B) {
	vg := NewVariableGenerator("snmp___")
	oid := "1.3.6.1.2.1.1.1.0"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = vg.GenerateDeviceVariable(oid)
	}
}

func BenchmarkVariableGenerator_GenerateIntervalVariable(b *testing.B) {
	vg := NewVariableGenerator("snmp___")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = vg.GenerateIntervalVariable()
	}
}

func BenchmarkVariableGenerator_GenerateDefaultVariables(b *testing.B) {
	vg := NewVariableGenerator("snmp___")
	oid := "1.3.6.1.2.1.1.1.0"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = vg.GenerateDefaultVariables(oid)
	}
}

func BenchmarkVariableGenerator_metricName(b *testing.B) {
	vg := NewVariableGenerator("snmp___")
	oid := &mib.OIDMetadata{OID: ".1.3.6.1.2.1.2.2.1.10", Name: "ifInOctets", IsColumn: true}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = vg.metricName(oid)
	}
}
