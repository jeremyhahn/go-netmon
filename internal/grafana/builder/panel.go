package builder

import (
	"fmt"
	"strings"

	"github.com/jeremyhahn/go-netmon/internal/grafana"
	"github.com/jeremyhahn/go-netmon/internal/mib"
)

// PanelType represents the type of Grafana panel
type PanelType string

// The panel types this builder emits.
const (
	PanelTypeTimeSeries PanelType = "timeseries"
	PanelTypeStat       PanelType = "stat"
	PanelTypeGauge      PanelType = "gauge"
	PanelTypeTable      PanelType = "table"
	PanelTypeHeatmap    PanelType = "heatmap"
)

// PanelGenerator generates Grafana panels from MIB metadata
type PanelGenerator struct {
	queryBuilder *QueryBuilder
	nextPanelID  int
}

// NewPanelGenerator creates a new panel generator
func NewPanelGenerator(metricPrefix string) *PanelGenerator {
	return &PanelGenerator{
		queryBuilder: NewQueryBuilder(metricPrefix),
		nextPanelID:  1,
	}
}

// GenerateScalarPanel creates a panel for scalar OIDs
func (pg *PanelGenerator) GenerateScalarPanel(oid *mib.OIDMetadata, gridPos grafana.GridPos) (*grafana.Panel, error) {
	if !oid.IsScalar {
		return nil, fmt.Errorf("OID %s is not a scalar", oid.OID)
	}

	panelType := pg.DeterminePanelType(oid)
	panel := &grafana.Panel{
		ID:      pg.nextPanelID,
		Type:    string(panelType),
		Title:   pg.formatTitle(oid),
		GridPos: gridPos,
	}
	pg.nextPanelID++

	// Build appropriate query based on type
	var target grafana.Target
	if oid.IsCounter() {
		target = pg.queryBuilder.BuildCounterQuery(oid, true, "5m")
	} else {
		target = pg.queryBuilder.BuildScalarQuery(oid, true)
	}
	panel.Targets = []grafana.Target{target}

	// Configure field settings based on panel type
	panel.FieldConfig = pg.buildFieldConfig(oid, panelType)

	// Add description if available
	if oid.Description != "" {
		panel.Description = oid.Description
	}

	// Add panel-specific options
	panel.Options = pg.buildPanelOptions(panelType, oid)

	return panel, nil
}

// GenerateCounterPanel creates a time series panel with rate()
func (pg *PanelGenerator) GenerateCounterPanel(oid *mib.OIDMetadata, gridPos grafana.GridPos) (*grafana.Panel, error) {
	if !oid.IsCounter() {
		return nil, fmt.Errorf("OID %s is not a counter", oid.OID)
	}

	panel := &grafana.Panel{
		ID:      pg.nextPanelID,
		Type:    string(PanelTypeTimeSeries),
		Title:   pg.formatTitle(oid),
		GridPos: gridPos,
	}
	pg.nextPanelID++

	// Build counter query with rate
	target := pg.queryBuilder.BuildCounterQuery(oid, true, "5m")
	panel.Targets = []grafana.Target{target}

	// Configure for time series
	panel.FieldConfig = pg.buildFieldConfig(oid, PanelTypeTimeSeries)

	if oid.Description != "" {
		panel.Description = oid.Description
	}

	panel.Options = pg.buildPanelOptions(PanelTypeTimeSeries, oid)

	return panel, nil
}

// GenerateGaugePanel creates a stat or gauge panel
func (pg *PanelGenerator) GenerateGaugePanel(oid *mib.OIDMetadata, gridPos grafana.GridPos) (*grafana.Panel, error) {
	if !oid.IsGauge() {
		return nil, fmt.Errorf("OID %s is not a gauge", oid.OID)
	}

	// Determine if it should be a gauge or stat panel
	panelType := PanelTypeStat
	if pg.isPercentage(oid) {
		panelType = PanelTypeGauge
	}

	panel := &grafana.Panel{
		ID:      pg.nextPanelID,
		Type:    string(panelType),
		Title:   pg.formatTitle(oid),
		GridPos: gridPos,
	}
	pg.nextPanelID++

	target := pg.queryBuilder.BuildGaugeQuery(oid, true)
	panel.Targets = []grafana.Target{target}

	panel.FieldConfig = pg.buildFieldConfig(oid, panelType)

	if oid.Description != "" {
		panel.Description = oid.Description
	}

	panel.Options = pg.buildPanelOptions(panelType, oid)

	return panel, nil
}

// GenerateTablePanels creates panels for SNMP table
func (pg *PanelGenerator) GenerateTablePanels(
	table *mib.TableInfo,
	columns []*mib.OIDMetadata,
	startY int,
) ([]grafana.Panel, error) {
	if len(columns) == 0 {
		return nil, fmt.Errorf("no columns provided for table %s", table.Name)
	}

	panels := []grafana.Panel{}
	currentY := startY

	// Group columns by type for better organization
	counterCols := []*mib.OIDMetadata{}
	gaugeCols := []*mib.OIDMetadata{}
	otherCols := []*mib.OIDMetadata{}

	for _, col := range columns {
		// Check if counter by Type OR by name pattern (workaround for parser issues)
		switch {
		case col.IsCounter() || pg.isLikelyCounter(col):
			counterCols = append(counterCols, col)
		case col.IsGauge():
			gaugeCols = append(gaugeCols, col)
		default:
			otherCols = append(otherCols, col)
		}
	}

	// Create panels for counter columns (time series)
	for i, col := range counterCols {
		gridPos := pg.CalculateGridPos(i, 0)
		gridPos.Y = currentY

		panel := grafana.Panel{
			ID:      pg.nextPanelID,
			Type:    string(PanelTypeTimeSeries),
			Title:   fmt.Sprintf("%s - %s", table.Name, pg.formatTitle(col)),
			GridPos: gridPos,
		}
		pg.nextPanelID++

		target := pg.queryBuilder.BuildTableCounterQuery(col, true, true, "5m")
		panel.Targets = []grafana.Target{target}
		panel.FieldConfig = pg.buildFieldConfig(col, PanelTypeTimeSeries)
		panel.Options = pg.buildPanelOptions(PanelTypeTimeSeries, col)

		if col.Description != "" {
			panel.Description = col.Description
		}

		panels = append(panels, panel)

		// Move to next row after every 2 panels
		if (i+1)%2 == 0 {
			currentY += 8
		}
	}

	// Adjust Y for next section
	if len(counterCols)%2 != 0 {
		currentY += 8
	}

	// Create panels for gauge columns (also use timeseries to show all interfaces)
	for i, col := range gaugeCols {
		gridPos := pg.CalculateGridPos(i, 0)
		gridPos.Y = currentY

		panel := grafana.Panel{
			ID:      pg.nextPanelID,
			Type:    string(PanelTypeTimeSeries),
			Title:   fmt.Sprintf("%s - %s", table.Name, pg.formatTitle(col)),
			GridPos: gridPos,
		}
		pg.nextPanelID++

		target := pg.queryBuilder.BuildColumnQuery(col, true, true)
		panel.Targets = []grafana.Target{target}
		panel.FieldConfig = pg.buildFieldConfig(col, PanelTypeTimeSeries)
		panel.Options = pg.buildPanelOptions(PanelTypeTimeSeries, col)

		if col.Description != "" {
			panel.Description = col.Description
		}

		panels = append(panels, panel)

		// Move to next row after every 2 panels
		if (i+1)%2 == 0 {
			currentY += 8
		}
	}

	// Adjust Y for next section
	if len(gaugeCols)%2 != 0 {
		currentY += 8
	}

	// Create table panel for string/other columns
	if len(otherCols) > 0 {
		gridPos := grafana.GridPos{
			H: 8,
			W: 24,
			X: 0,
			Y: currentY,
		}

		panel := grafana.Panel{
			ID:      pg.nextPanelID,
			Type:    string(PanelTypeTable),
			Title:   fmt.Sprintf("%s - Details", table.Name),
			GridPos: gridPos,
		}
		pg.nextPanelID++

		// Add targets for all other columns
		targets := []grafana.Target{}
		for i, col := range otherCols {
			target := pg.queryBuilder.BuildColumnQuery(col, true, true)
			target.RefID = string(rune('A' + i))
			target.Format = "table"
			targets = append(targets, target)
		}
		panel.Targets = targets

		panel.Options = map[string]interface{}{
			"showHeader": true,
			"sortBy": []map[string]interface{}{
				{
					"displayName": "device",
					"desc":        false,
				},
			},
		}

		if table.Description != "" {
			panel.Description = table.Description
		}

		panels = append(panels, panel)
	}

	return panels, nil
}

// DeterminePanelType selects appropriate panel type based on OID metadata
func (pg *PanelGenerator) DeterminePanelType(oid *mib.OIDMetadata) PanelType {
	// Counters always get time series
	if oid.IsCounter() {
		return PanelTypeTimeSeries
	}

	// Percentages get gauge panels
	if pg.isPercentage(oid) {
		return PanelTypeGauge
	}

	// Gauges and integers get stat panels
	if oid.IsGauge() {
		return PanelTypeStat
	}

	// Strings get table panels
	if oid.IsString() {
		return PanelTypeTable
	}

	// Default to stat panel
	return PanelTypeStat
}

// CalculateGridPos calculates grid position for a panel
func (pg *PanelGenerator) CalculateGridPos(panelIndex, rowIndex int) grafana.GridPos {
	// 2 panels per row, each 12 units wide
	col := panelIndex % 2
	row := rowIndex + (panelIndex / 2)

	return grafana.GridPos{
		H: 8, // Default height
		W: 12,
		X: col * 12,
		Y: row * 8,
	}
}

// buildFieldConfig creates field configuration based on OID and panel type
func (pg *PanelGenerator) buildFieldConfig(oid *mib.OIDMetadata, panelType PanelType) grafana.FieldConfig {
	config := grafana.FieldConfig{
		Defaults: grafana.FieldConfigDefaults{},
	}

	// Determine unit
	unit := pg.determineUnit(oid)
	if unit != "" {
		config.Defaults.Unit = unit
	}

	// Set min/max for percentages
	if pg.isPercentage(oid) {
		lower := 0.0
		upper := 100.0
		config.Defaults.Min = &lower
		config.Defaults.Max = &upper
	}

	// Add thresholds for gauge and stat panels
	if panelType == PanelTypeGauge || panelType == PanelTypeStat {
		config.Defaults.Thresholds = pg.buildThresholds(oid)
	}

	// Add custom settings for time series
	if panelType == PanelTypeTimeSeries {
		config.Defaults.Custom = map[string]interface{}{
			"lineWidth":     1,
			"fillOpacity":   10,
			"gradientMode":  "none",
			"axisPlacement": "auto",
			"axisLabel":     "",
			"scaleDistribution": map[string]interface{}{
				"type": "linear",
			},
		}
	}

	return config
}

// buildPanelOptions creates panel-specific options
func (pg *PanelGenerator) buildPanelOptions(panelType PanelType, _ *mib.OIDMetadata) map[string]interface{} {
	options := make(map[string]interface{})

	switch panelType {
	case PanelTypeTimeSeries:
		options["tooltip"] = map[string]interface{}{
			"mode": "multi",
			"sort": "none",
		}
		options["legend"] = map[string]interface{}{
			"displayMode": "list",
			"placement":   "bottom",
			"showLegend":  true,
		}

	case PanelTypeStat:
		options["reduceOptions"] = map[string]interface{}{
			"values": false,
			"calcs":  []string{"lastNotNull"},
		}
		options["orientation"] = "auto"
		options["textMode"] = "auto"
		options["colorMode"] = "value"
		options["graphMode"] = "area"
		options["justifyMode"] = "auto"

	case PanelTypeGauge:
		options["reduceOptions"] = map[string]interface{}{
			"values": false,
			"calcs":  []string{"lastNotNull"},
		}
		options["orientation"] = "auto"
		options["showThresholdLabels"] = false
		options["showThresholdMarkers"] = true

	case PanelTypeTable:
		options["showHeader"] = true
		options["sortBy"] = []map[string]interface{}{
			{
				"displayName": "Time",
				"desc":        true,
			},
		}
	}

	return options
}

// buildThresholds creates threshold configuration
func (pg *PanelGenerator) buildThresholds(oid *mib.OIDMetadata) *grafana.ThresholdsConfig {
	config := &grafana.ThresholdsConfig{
		Mode: "absolute",
		Steps: []grafana.Threshold{
			{Color: "green", Value: nil},
		},
	}

	// Add thresholds for percentages
	if pg.isPercentage(oid) {
		warning := 80.0
		critical := 90.0
		config.Steps = append(config.Steps,
			grafana.Threshold{Color: "yellow", Value: &warning},
			grafana.Threshold{Color: "red", Value: &critical},
		)
	}

	return config
}

// determineUnit determines the appropriate unit for an OID
func (pg *PanelGenerator) determineUnit(oid *mib.OIDMetadata) string {
	// Check explicit units first
	if oid.Units != "" {
		return pg.mapUnit(oid.Units)
	}

	// Infer from name
	name := strings.ToLower(oid.Name)

	if strings.Contains(name, "percent") || strings.Contains(name, "utilization") {
		return "percent"
	}

	if strings.Contains(name, "octet") || strings.Contains(name, "byte") {
		if oid.IsCounter() {
			return "bps" // Bits per second (after rate and * 8)
		}
		return "bytes"
	}

	if strings.Contains(name, "packet") || strings.Contains(name, "pkt") {
		if oid.IsCounter() {
			return "pps" // Packets per second
		}
		return "short"
	}

	if strings.Contains(name, "error") || strings.Contains(name, "discard") {
		if oid.IsCounter() {
			return "cps" // Count per second
		}
		return "short"
	}

	if strings.Contains(name, "time") || strings.Contains(name, "uptime") {
		return "s" // Seconds
	}

	if strings.Contains(name, "temperature") || strings.Contains(name, "temp") {
		return "celsius"
	}

	// Default to no unit
	return ""
}

// mapUnit maps MIB units to Grafana units
func (pg *PanelGenerator) mapUnit(unit string) string {
	unitMap := map[string]string{
		"seconds":      "s",
		"milliseconds": "ms",
		"bytes":        "bytes",
		"bits":         "bits",
		"percent":      "percent",
		"celsius":      "celsius",
		"fahrenheit":   "fahrenheit",
	}

	if mapped, ok := unitMap[strings.ToLower(unit)]; ok {
		return mapped
	}

	return unit
}

// formatTitle creates a human-readable title from OID metadata
func (pg *PanelGenerator) formatTitle(oid *mib.OIDMetadata) string {
	if oid.Name == "" {
		return oid.OID
	}

	// Convert camelCase/PascalCase to Title Case with spaces
	var result strings.Builder
	for i, r := range oid.Name {
		if i > 0 && r >= 'A' && r <= 'Z' {
			result.WriteRune(' ')
		}
		result.WriteRune(r)
	}

	return result.String()
}

// isPercentage checks if an OID represents a percentage value
func (pg *PanelGenerator) isPercentage(oid *mib.OIDMetadata) bool {
	if strings.Contains(strings.ToLower(oid.Units), "percent") {
		return true
	}

	name := strings.ToLower(oid.Name)
	return strings.Contains(name, "percent") ||
		strings.Contains(name, "utilization") ||
		strings.Contains(name, "usage")
}

// isLikelyCounter checks if an OID is likely a counter based on naming patterns
// This is a workaround for MIB parser issues where Type field isn't set correctly
func (pg *PanelGenerator) isLikelyCounter(oid *mib.OIDMetadata) bool {
	name := strings.ToLower(oid.Name)
	// Common counter patterns in SNMP MIBs
	return strings.Contains(name, "octet") || // ifInOctets, ifOutOctets, etc.
		strings.Contains(name, "pkt") || // ifInPkts, ifOutPkts, etc.
		strings.Contains(name, "packet") ||
		strings.Contains(name, "error") || // ifInErrors, ifOutErrors, etc.
		strings.Contains(name, "discard") || // ifInDiscards, ifOutDiscards, etc.
		strings.Contains(name, "drop") ||
		strings.Contains(name, "collision") ||
		strings.Contains(name, "frame")
}

// ResetPanelID resets the panel ID counter (useful for testing)
func (pg *PanelGenerator) ResetPanelID() {
	pg.nextPanelID = 1
}

// GetNextPanelID returns the next panel ID without incrementing
func (pg *PanelGenerator) GetNextPanelID() int {
	return pg.nextPanelID
}
