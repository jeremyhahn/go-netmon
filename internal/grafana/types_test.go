package grafana

import (
	"encoding/json"
	"testing"
)

func TestNewDashboard(t *testing.T) {
	tests := []struct {
		name  string
		uid   string
		title string
		want  Dashboard
	}{
		{
			name:  "create basic dashboard",
			uid:   "test-dash",
			title: "Test Dashboard",
			want: Dashboard{
				UID:           "test-dash",
				Title:         "Test Dashboard",
				Tags:          []string{},
				Timezone:      "browser",
				Editable:      true,
				Refresh:       "30s",
				SchemaVersion: 36,
				Time: TimeRange{
					From: "now-6h",
					To:   "now",
				},
				Templating: Templating{
					List: []Variable{},
				},
				Panels: []Panel{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewDashboard(tt.uid, tt.title)
			if got.UID != tt.want.UID {
				t.Errorf("NewDashboard().UID = %v, want %v", got.UID, tt.want.UID)
			}
			if got.Title != tt.want.Title {
				t.Errorf("NewDashboard().Title = %v, want %v", got.Title, tt.want.Title)
			}
			if got.Timezone != tt.want.Timezone {
				t.Errorf("NewDashboard().Timezone = %v, want %v", got.Timezone, tt.want.Timezone)
			}
			if got.SchemaVersion != tt.want.SchemaVersion {
				t.Errorf("NewDashboard().SchemaVersion = %v, want %v", got.SchemaVersion, tt.want.SchemaVersion)
			}
		})
	}
}

func TestDashboard_AddPanel(t *testing.T) {
	d := NewDashboard("test", "Test")
	panel := Panel{
		ID:    1,
		Type:  PanelTypeTimeSeries,
		Title: "Test Panel",
	}

	d.AddPanel(panel)

	if len(d.Panels) != 1 {
		t.Errorf("AddPanel() panel count = %d, want 1", len(d.Panels))
	}
	if d.Panels[0].ID != 1 {
		t.Errorf("AddPanel() panel ID = %d, want 1", d.Panels[0].ID)
	}
}

func TestDashboard_AddVariable(t *testing.T) {
	d := NewDashboard("test", "Test")
	variable := Variable{
		Name:  "device",
		Type:  VariableTypeQuery,
		Label: "Device",
	}

	d.AddVariable(variable)

	if len(d.Templating.List) != 1 {
		t.Errorf("AddVariable() variable count = %d, want 1", len(d.Templating.List))
	}
	if d.Templating.List[0].Name != "device" {
		t.Errorf("AddVariable() variable name = %s, want device", d.Templating.List[0].Name)
	}
}

func TestNewTimeSeriesPanel(t *testing.T) {
	gridPos := GridPos{X: 0, Y: 0, W: 12, H: 8}
	panel := NewTimeSeriesPanel(1, "Test Panel", gridPos)

	if panel.ID != 1 {
		t.Errorf("NewTimeSeriesPanel().ID = %d, want 1", panel.ID)
	}
	if panel.Type != PanelTypeTimeSeries {
		t.Errorf("NewTimeSeriesPanel().Type = %s, want %s", panel.Type, PanelTypeTimeSeries)
	}
	if panel.Title != "Test Panel" {
		t.Errorf("NewTimeSeriesPanel().Title = %s, want Test Panel", panel.Title)
	}
	if panel.GridPos.W != 12 {
		t.Errorf("NewTimeSeriesPanel().GridPos.W = %d, want 12", panel.GridPos.W)
	}
}

func TestNewStatPanel(t *testing.T) {
	gridPos := GridPos{X: 0, Y: 0, W: 6, H: 4}
	panel := NewStatPanel(2, "Stat Panel", gridPos)

	if panel.ID != 2 {
		t.Errorf("NewStatPanel().ID = %d, want 2", panel.ID)
	}
	if panel.Type != PanelTypeStat {
		t.Errorf("NewStatPanel().Type = %s, want %s", panel.Type, PanelTypeStat)
	}
	if panel.FieldConfig.Defaults.Color == nil {
		t.Error("NewStatPanel().FieldConfig.Defaults.Color is nil")
	}
}

func TestNewGaugePanel(t *testing.T) {
	gridPos := GridPos{X: 0, Y: 0, W: 6, H: 4}
	panel := NewGaugePanel(3, "Gauge Panel", gridPos)

	if panel.ID != 3 {
		t.Errorf("NewGaugePanel().ID = %d, want 3", panel.ID)
	}
	if panel.Type != PanelTypeGauge {
		t.Errorf("NewGaugePanel().Type = %s, want %s", panel.Type, PanelTypeGauge)
	}
	if panel.FieldConfig.Defaults.Min == nil {
		t.Error("NewGaugePanel().FieldConfig.Defaults.Min is nil")
	}
	if panel.FieldConfig.Defaults.Max == nil {
		t.Error("NewGaugePanel().FieldConfig.Defaults.Max is nil")
	}
	if *panel.FieldConfig.Defaults.Min != 0.0 {
		t.Errorf("NewGaugePanel().FieldConfig.Defaults.Min = %f, want 0.0", *panel.FieldConfig.Defaults.Min)
	}
	if *panel.FieldConfig.Defaults.Max != 100.0 {
		t.Errorf("NewGaugePanel().FieldConfig.Defaults.Max = %f, want 100.0", *panel.FieldConfig.Defaults.Max)
	}
}

func TestNewTablePanel(t *testing.T) {
	gridPos := GridPos{X: 0, Y: 0, W: 24, H: 8}
	panel := NewTablePanel(4, "Table Panel", gridPos)

	if panel.ID != 4 {
		t.Errorf("NewTablePanel().ID = %d, want 4", panel.ID)
	}
	if panel.Type != PanelTypeTable {
		t.Errorf("NewTablePanel().Type = %s, want %s", panel.Type, PanelTypeTable)
	}
}

func TestNewRowPanel(t *testing.T) {
	gridPos := GridPos{X: 0, Y: 0, W: 24, H: 1}
	panel := NewRowPanel(5, "Row Panel", gridPos)

	if panel.ID != 5 {
		t.Errorf("NewRowPanel().ID = %d, want 5", panel.ID)
	}
	if panel.Type != PanelTypeRow {
		t.Errorf("NewRowPanel().Type = %s, want %s", panel.Type, PanelTypeRow)
	}
}

func TestNewQueryVariable(t *testing.T) {
	variable := NewQueryVariable("device", "Device", "label_values(device)")

	if variable.Name != "device" {
		t.Errorf("NewQueryVariable().Name = %s, want device", variable.Name)
	}
	if variable.Type != VariableTypeQuery {
		t.Errorf("NewQueryVariable().Type = %s, want %s", variable.Type, VariableTypeQuery)
	}
	if variable.Label != "Device" {
		t.Errorf("NewQueryVariable().Label = %s, want Device", variable.Label)
	}
	if !variable.Multi {
		t.Error("NewQueryVariable().Multi = false, want true")
	}
	if !variable.IncludeAll {
		t.Error("NewQueryVariable().IncludeAll = false, want true")
	}
}

func TestNewCustomVariable(t *testing.T) {
	options := []string{"option1", "option2", "option3"}
	variable := NewCustomVariable("custom", "Custom", options)

	if variable.Name != "custom" {
		t.Errorf("NewCustomVariable().Name = %s, want custom", variable.Name)
	}
	if variable.Type != VariableTypeCustom {
		t.Errorf("NewCustomVariable().Type = %s, want %s", variable.Type, VariableTypeCustom)
	}
	if len(variable.Options) != 3 {
		t.Errorf("NewCustomVariable() option count = %d, want 3", len(variable.Options))
	}
	if variable.Options[0].Value != "option1" {
		t.Errorf("NewCustomVariable().Options[0].Value = %s, want option1", variable.Options[0].Value)
	}
}

func TestPanel_AddTarget(t *testing.T) {
	panel := Panel{ID: 1, Type: PanelTypeTimeSeries}
	target := Target{
		RefID:        "A",
		Expr:         "up",
		LegendFormat: "{{instance}}",
	}

	panel.AddTarget(target)

	if len(panel.Targets) != 1 {
		t.Errorf("AddTarget() target count = %d, want 1", len(panel.Targets))
	}
	if panel.Targets[0].RefID != "A" {
		t.Errorf("AddTarget() target RefID = %s, want A", panel.Targets[0].RefID)
	}
}

func TestNewTarget(t *testing.T) {
	target := NewTarget("A", "up{job=\"test\"}", "{{instance}}")

	if target.RefID != "A" {
		t.Errorf("NewTarget().RefID = %s, want A", target.RefID)
	}
	if target.Expr != "up{job=\"test\"}" {
		t.Errorf("NewTarget().Expr = %s, want up{job=\"test\"}", target.Expr)
	}
	if target.LegendFormat != "{{instance}}" {
		t.Errorf("NewTarget().LegendFormat = %s, want {{instance}}", target.LegendFormat)
	}
	if !target.Range {
		t.Error("NewTarget().Range = false, want true")
	}
	if target.Instant {
		t.Error("NewTarget().Instant = true, want false")
	}
}

func TestNewInstantTarget(t *testing.T) {
	target := NewInstantTarget("B", "up", "test")

	if target.RefID != "B" {
		t.Errorf("NewInstantTarget().RefID = %s, want B", target.RefID)
	}
	if !target.Instant {
		t.Error("NewInstantTarget().Instant = false, want true")
	}
	if target.Range {
		t.Error("NewInstantTarget().Range = true, want false")
	}
}

func TestPanel_SetUnit(t *testing.T) {
	panel := NewTimeSeriesPanel(1, "Test", GridPos{})
	panel.SetUnit("bytes")

	if panel.FieldConfig.Defaults.Unit != "bytes" {
		t.Errorf("SetUnit() unit = %s, want bytes", panel.FieldConfig.Defaults.Unit)
	}
}

func TestPanel_SetThresholds(t *testing.T) {
	panel := NewTimeSeriesPanel(1, "Test", GridPos{})
	val1 := 0.0
	val2 := 80.0
	steps := []Threshold{
		{Value: &val1, Color: "green"},
		{Value: &val2, Color: "red"},
	}
	panel.SetThresholds(ThresholdModeAbsolute, steps)

	if panel.FieldConfig.Defaults.Thresholds == nil {
		t.Fatal("SetThresholds() thresholds is nil")
	}
	if panel.FieldConfig.Defaults.Thresholds.Mode != ThresholdModeAbsolute {
		t.Errorf("SetThresholds() mode = %s, want %s", panel.FieldConfig.Defaults.Thresholds.Mode, ThresholdModeAbsolute)
	}
	if len(panel.FieldConfig.Defaults.Thresholds.Steps) != 2 {
		t.Errorf("SetThresholds() steps count = %d, want 2", len(panel.FieldConfig.Defaults.Thresholds.Steps))
	}
}

func TestPanel_SetMinMax(t *testing.T) {
	panel := NewTimeSeriesPanel(1, "Test", GridPos{})
	panel.SetMinMax(0, 100)

	if panel.FieldConfig.Defaults.Min == nil {
		t.Fatal("SetMinMax() min is nil")
	}
	if panel.FieldConfig.Defaults.Max == nil {
		t.Fatal("SetMinMax() max is nil")
	}
	if *panel.FieldConfig.Defaults.Min != 0 {
		t.Errorf("SetMinMax() min = %f, want 0", *panel.FieldConfig.Defaults.Min)
	}
	if *panel.FieldConfig.Defaults.Max != 100 {
		t.Errorf("SetMinMax() max = %f, want 100", *panel.FieldConfig.Defaults.Max)
	}
}

func TestPanel_SetDecimals(t *testing.T) {
	panel := NewTimeSeriesPanel(1, "Test", GridPos{})
	panel.SetDecimals(2)

	if panel.FieldConfig.Defaults.Decimals == nil {
		t.Fatal("SetDecimals() decimals is nil")
	}
	if *panel.FieldConfig.Defaults.Decimals != 2 {
		t.Errorf("SetDecimals() decimals = %d, want 2", *panel.FieldConfig.Defaults.Decimals)
	}
}

func TestDashboardJSONSerialization(t *testing.T) {
	d := NewDashboard("test-uid", "Test Dashboard")
	d.Description = "Test description"

	panel := NewTimeSeriesPanel(1, "Test Panel", GridPos{X: 0, Y: 0, W: 12, H: 8})
	target := NewTarget("A", "up", "{{instance}}")
	panel.AddTarget(target)
	d.AddPanel(panel)

	variable := NewQueryVariable("device", "Device", "label_values(device)")
	d.AddVariable(variable)

	// Test serialization
	data, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	// Test deserialization
	var decoded Dashboard
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if decoded.UID != d.UID {
		t.Errorf("JSON round-trip UID = %s, want %s", decoded.UID, d.UID)
	}
	if decoded.Title != d.Title {
		t.Errorf("JSON round-trip Title = %s, want %s", decoded.Title, d.Title)
	}
	if len(decoded.Panels) != len(d.Panels) {
		t.Errorf("JSON round-trip panel count = %d, want %d", len(decoded.Panels), len(d.Panels))
	}
	if len(decoded.Templating.List) != len(d.Templating.List) {
		t.Errorf("JSON round-trip variable count = %d, want %d", len(decoded.Templating.List), len(d.Templating.List))
	}
}

func TestPanelTypes(t *testing.T) {
	tests := []struct {
		name      string
		panelType string
		expected  string
	}{
		{"timeseries", PanelTypeTimeSeries, "timeseries"},
		{"stat", PanelTypeStat, "stat"},
		{"gauge", PanelTypeGauge, "gauge"},
		{"table", PanelTypeTable, "table"},
		{"row", PanelTypeRow, "row"},
		{"text", PanelTypeText, "text"},
		{"heatmap", PanelTypeHeatmap, "heatmap"},
		{"graph", PanelTypeGraph, "graph"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.panelType != tt.expected {
				t.Errorf("PanelType %s = %s, want %s", tt.name, tt.panelType, tt.expected)
			}
		})
	}
}

func TestVariableTypes(t *testing.T) {
	tests := []struct {
		name     string
		varType  string
		expected string
	}{
		{"query", VariableTypeQuery, "query"},
		{"custom", VariableTypeCustom, "custom"},
		{"constant", VariableTypeConstant, "constant"},
		{"interval", VariableTypeInterval, "interval"},
		{"textbox", VariableTypeTextbox, "textbox"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.varType != tt.expected {
				t.Errorf("VariableType %s = %s, want %s", tt.name, tt.varType, tt.expected)
			}
		})
	}
}

func TestColorModes(t *testing.T) {
	tests := []struct {
		name     string
		mode     string
		expected string
	}{
		{"palette", ColorModePalette, "palette-classic"},
		{"value", ColorModeValue, "value"},
		{"fixed", ColorModeFixed, "fixed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.mode != tt.expected {
				t.Errorf("ColorMode %s = %s, want %s", tt.name, tt.mode, tt.expected)
			}
		})
	}
}

func TestThresholdModes(t *testing.T) {
	tests := []struct {
		name     string
		mode     string
		expected string
	}{
		{"absolute", ThresholdModeAbsolute, "absolute"},
		{"percentage", ThresholdModePercentage, "percentage"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.mode != tt.expected {
				t.Errorf("ThresholdMode %s = %s, want %s", tt.name, tt.mode, tt.expected)
			}
		})
	}
}
