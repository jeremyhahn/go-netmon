// Package builder provides functionality for generating Grafana dashboards
// from SNMP MIB metadata.
//
// The builder package contains components for:
//   - Query generation: Building Prometheus PromQL queries for SNMP metrics
//   - Panel generation: Creating Grafana panels with appropriate configurations
//   - Variable generation: Generating dashboard template variables
//
// # Query Builder
//
// The QueryBuilder constructs Prometheus queries for different OID types. Every
// query names the MIB object, never the OID of one of its instances, and picks
// rows out with label matchers -- see internal/promname:
//
//	qb := builder.NewQueryBuilder(promname.Prefix)
//
//	// For scalar OIDs
//	target := qb.BuildScalarQuery(oid, true)
//
//	// For counters with rate()
//	target := qb.BuildCounterQuery(oid, true, "5m")
//
//	// For table columns
//	target := qb.BuildColumnQuery(oid, true, true)
//
// # Panel Generator
//
// The PanelGenerator creates Grafana panels based on OID metadata:
//
//	pg := builder.NewPanelGenerator(promname.Prefix)
//
//	// Generate panel for scalar OID (auto-detects type)
//	panel, err := pg.GenerateScalarPanel(oid, gridPos)
//
//	// Generate counter panel (time series with rate())
//	panel, err := pg.GenerateCounterPanel(oid, gridPos)
//
//	// Generate gauge/stat panel
//	panel, err := pg.GenerateGaugePanel(oid, gridPos)
//
//	// Generate panels for SNMP table
//	panels, err := pg.GenerateTablePanels(tableInfo, columns, startY)
//
// Panel types are automatically determined based on OID characteristics:
//   - Counter32/Counter64 → TimeSeries with rate()
//   - Gauge32 with percentage → Gauge panel
//   - Gauge32/Integer → Stat panel
//   - String types → Table panel
//
// # Variable Generator
//
// The VariableGenerator creates dashboard template variables:
//
//	vg := builder.NewVariableGenerator(promname.Prefix)
//
//	// Device selector variable
//	deviceVar := vg.GenerateDeviceVariable("1.3.6.1.2.1.1.1.0")
//
//	// Table index variable
//	indexVar := vg.GenerateTableIndexVariable(ifInOctets, "ifIndex")
//
//	// Time interval variable
//	intervalVar := vg.GenerateIntervalVariable()
//
//	// Generate standard variables for SNMP dashboards
//	vars := vg.GenerateDefaultVariables(sampleOID)
//
// # Units and Formatting
//
// The package automatically handles unit conversion and formatting:
//   - Byte counters (ifInOctets) → bits per second (bps)
//   - Packet counters → packets per second (pps)
//   - Percentages → 0-100 range with color thresholds
//   - Temperatures → celsius/fahrenheit
//
// # Example: Complete Dashboard Generation
//
//	// Initialize generators
//	pg := builder.NewPanelGenerator(promname.Prefix)
//	vg := builder.NewVariableGenerator(promname.Prefix)
//
//	// Create dashboard
//	dashboard := grafana.NewDashboard("snmp-interfaces", "Network Interfaces")
//
//	// Add variables
//	dashboard.AddVariable(vg.GenerateDeviceVariable("1.3.6.1.2.1.1.1.0"))
//	dashboard.AddVariable(vg.GenerateTableIndexVariable(ifInOctets, "ifIndex"))
//	dashboard.AddVariable(vg.GenerateIntervalVariable())
//
//	// Add panels for ifTable
//	panels, err := pg.GenerateTablePanels(ifTable, ifColumns, 0)
//	if err != nil {
//		return err
//	}
//	for _, panel := range panels {
//		dashboard.AddPanel(panel)
//	}
//
// # Grid Layout
//
// Panels are automatically positioned in a grid layout:
//   - 24 units wide (full dashboard width)
//   - 2 panels per row (12 units each)
//   - 8 units tall for time series panels
//   - 6 units tall for stat/gauge panels
//
// Use CalculateGridPos() for automatic positioning:
//
//	gridPos := pg.CalculateGridPos(panelIndex, rowIndex)
package builder
