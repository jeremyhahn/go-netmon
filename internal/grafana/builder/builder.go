package builder

import (
	"context"
	"fmt"
	"sort"

	"github.com/jeremyhahn/go-netmon/internal/grafana"
	"github.com/jeremyhahn/go-netmon/internal/grafana/template"
	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/mib/storage"
	"github.com/jeremyhahn/go-netmon/internal/promname"
)

// Config contains builder configuration
type Config struct {
	PrometheusDataSource string
	RefreshInterval      string
	DeviceLabel          string
	OIDLabel             string
	MetricPrefix         string
	PrometheusURL        string // URL for querying Prometheus to check metric existence
}

// Builder generates Grafana dashboards from MIB storage
type Builder struct {
	config     *Config
	storage    *storage.Storage
	panelGen   *PanelGenerator
	varGen     *VariableGenerator
	queryGen   *QueryBuilder
	promClient *PrometheusClient
}

// New creates a new dashboard builder
func New(config *Config, store *storage.Storage) *Builder {
	if config == nil {
		config = DefaultConfig()
	}

	if config.MetricPrefix == "" {
		config.MetricPrefix = promname.Prefix
	}

	// Initialize Prometheus client if URL is provided
	var promClient *PrometheusClient
	if config.PrometheusURL != "" {
		promClient = NewPrometheusClient(config.PrometheusURL)
	}

	return &Builder{
		config:     config,
		storage:    store,
		panelGen:   NewPanelGenerator(config.MetricPrefix),
		varGen:     NewVariableGenerator(config.MetricPrefix),
		queryGen:   NewQueryBuilder(config.MetricPrefix),
		promClient: promClient,
	}
}

// DefaultConfig returns default builder configuration
func DefaultConfig() *Config {
	return &Config{
		PrometheusDataSource: "Prometheus",
		RefreshInterval:      "30s",
		DeviceLabel:          promname.DeviceLabel,
		OIDLabel:             promname.OIDLabel,
		MetricPrefix:         promname.Prefix,
		PrometheusURL:        "http://prometheus:9090",
	}
}

// Generate creates a dashboard for a specific MIB module
func (b *Builder) Generate(moduleName string) (*template.Spec, error) {
	// Get module info
	module, err := b.storage.GetModule(moduleName)
	if err != nil {
		return nil, fmt.Errorf("failed to get module: %w", err)
	}

	// Get pollable OIDs for this module
	pollableOIDs, err := b.storage.GetPollableOIDs(moduleName)
	if err != nil {
		return nil, fmt.Errorf("failed to get pollable OIDs: %w", err)
	}

	if len(pollableOIDs) == 0 {
		return nil, fmt.Errorf("no pollable OIDs found for module %s", moduleName)
	}

	// Filter OIDs to only those that exist in Prometheus
	if b.promClient != nil {
		filtered, filterErr := b.filterOIDsByPrometheus(pollableOIDs)
		switch {
		case filterErr != nil:
			// Log warning but continue with all OIDs. filtered is nil on
			// error, so pollableOIDs must not be overwritten with it here.
			fmt.Printf("Warning: failed to filter OIDs via Prometheus: %v\n", filterErr)
		case len(filtered) == 0:
			return nil, fmt.Errorf("no metrics found in Prometheus for module %s", moduleName)
		default:
			pollableOIDs = filtered
		}
	}

	// Create dashboard spec
	spec := template.NewDashboardSpec(module)

	// Add device variable (using template types)
	sampleOID := pollableOIDs[0].OID
	deviceVar := b.varGen.GenerateDeviceVariable(sampleOID)

	templateVar := b.convertToTemplateVariable(deviceVar)
	spec.AddVariable(templateVar)

	// Get tables in this module
	tables, err := b.storage.ListTables(moduleName)
	if err != nil {
		return nil, fmt.Errorf("failed to list tables: %w", err)
	}

	// Separate scalar and table OIDs
	scalarOIDs := []*mib.OIDMetadata{}
	tableOIDMap := make(map[string][]*mib.OIDMetadata)

	for _, oid := range pollableOIDs {
		if oid.IsColumn {
			// Find parent table
			tableName := b.findTableForColumn(oid, tables)
			if tableName != "" {
				tableOIDMap[tableName] = append(tableOIDMap[tableName], oid)
			}
		} else if oid.IsScalar {
			scalarOIDs = append(scalarOIDs, oid)
		}
	}

	// Add interface variable if this dashboard has table columns
	if len(tableOIDMap) > 0 {
		// Use first table's first OID to query interface names
		for _, columns := range tableOIDMap {
			if len(columns) > 0 {
				interfaceVar := b.varGen.GenerateInterfaceVariable(columns[0].OID)
				interfaceTemplateVar := b.convertToTemplateVariable(interfaceVar)
				spec.AddVariable(interfaceTemplateVar)
				break
			}
		}
	}

	// Generate panels
	currentY := 0

	// Add scalar panels first
	if len(scalarOIDs) > 0 {
		panels := b.generateScalarPanels(scalarOIDs, currentY)
		for _, panel := range panels {
			spec.AddPanel(panel)
		}
		// Calculate next Y position
		rows := (len(scalarOIDs) + 1) / 2
		currentY += rows * 8
	}

	// Add table panels
	for tableName, columns := range tableOIDMap {
		table := b.findTable(tableName, tables)
		if table == nil {
			continue
		}

		panels, err := b.generateTablePanels(table, columns, currentY)
		if err != nil {
			continue // Skip problematic tables
		}

		for _, panel := range panels {
			spec.AddPanel(panel)
		}

		// Update Y position based on generated panels
		if len(panels) > 0 {
			maxY := 0
			for _, panel := range panels {
				if panel.GridPos.Y+panel.GridPos.H > maxY {
					maxY = panel.GridPos.Y + panel.GridPos.H
				}
			}
			currentY = maxY
		}
	}

	// Validate spec
	if err := spec.Validate(); err != nil {
		return nil, fmt.Errorf("invalid dashboard spec: %w", err)
	}

	return spec, nil
}

// GenerateAll creates dashboards for all modules in storage
func (b *Builder) GenerateAll() ([]*template.Spec, error) {
	modules, err := b.storage.ListModules()
	if err != nil {
		return nil, fmt.Errorf("failed to list modules: %w", err)
	}

	if len(modules) == 0 {
		return nil, fmt.Errorf("no modules found in storage")
	}

	specs := make([]*template.Spec, 0, len(modules))
	errors := make([]error, 0)

	for _, module := range modules {
		spec, err := b.Generate(module.Name)
		if err != nil {
			errors = append(errors, fmt.Errorf("%s: %w", module.Name, err))
			continue
		}
		specs = append(specs, spec)
	}

	if len(specs) == 0 && len(errors) > 0 {
		return nil, fmt.Errorf("failed to generate any dashboards: %v", errors[0])
	}

	return specs, nil
}

// generateScalarPanels creates panels for scalar OIDs
func (b *Builder) generateScalarPanels(oids []*mib.OIDMetadata, startY int) []template.Panel {
	panels := make([]template.Panel, 0, len(oids))

	for i, oid := range oids {
		gridPos := b.panelGen.CalculateGridPos(i, startY/8)
		gridPos.Y = startY + (i/2)*8

		var gPanel *grafana.Panel
		var err error

		switch {
		case oid.IsCounter():
			gPanel, err = b.panelGen.GenerateCounterPanel(oid, gridPos)
		case oid.IsGauge():
			gPanel, err = b.panelGen.GenerateGaugePanel(oid, gridPos)
		default:
			gPanel, err = b.panelGen.GenerateScalarPanel(oid, gridPos)
		}

		if err == nil && gPanel != nil {
			panel := b.convertGrafanaPanelToTemplate(gPanel)
			panels = append(panels, panel)
		}
	}

	return panels
}

// generateTablePanels creates panels for table columns
func (b *Builder) generateTablePanels(table *mib.TableInfo, columns []*mib.OIDMetadata, startY int) ([]template.Panel, error) {
	gPanels, err := b.panelGen.GenerateTablePanels(table, columns, startY)
	if err != nil {
		return nil, err
	}

	panels := make([]template.Panel, 0, len(gPanels))
	for _, gPanel := range gPanels {
		panel := b.convertGrafanaPanelToTemplate(&gPanel)
		panels = append(panels, panel)
	}

	return panels, nil
}

// findTableForColumn finds the table that contains a column OID
func (b *Builder) findTableForColumn(oid *mib.OIDMetadata, tables []*mib.TableInfo) string {
	for _, table := range tables {
		for _, colOID := range table.Columns {
			if colOID == oid.OID {
				return table.Name
			}
		}
	}
	return ""
}

// findTable finds a table by name
func (b *Builder) findTable(name string, tables []*mib.TableInfo) *mib.TableInfo {
	for _, table := range tables {
		if table.Name == name {
			return table
		}
	}
	return nil
}

// convertGrafanaPanelToTemplate converts grafana.Panel to template.Panel
func (b *Builder) convertGrafanaPanelToTemplate(gPanel *grafana.Panel) template.Panel {
	// Convert targets
	targets := make([]template.Target, 0, len(gPanel.Targets))
	for _, t := range gPanel.Targets {
		target := template.Target{
			Expr:           t.Expr,
			RefID:          t.RefID,
			LegendFormat:   t.LegendFormat,
			Interval:       t.Interval,
			Format:         t.Format,
			Instant:        t.Instant,
			Range:          t.Range,
			IntervalFactor: t.IntervalFactor,
		}

		// Add datasource if not set (use Prometheus)
		if t.DataSource == nil {
			target.Datasource = &template.Datasource{
				Type: "prometheus",
				UID:  "prometheus",
			}
		}

		targets = append(targets, target)
	}

	// Convert field config
	var fieldConfig *template.FieldConfig
	if gPanel.FieldConfig.Defaults.Unit != "" || gPanel.FieldConfig.Defaults.Thresholds != nil {
		fieldConfig = &template.FieldConfig{
			Defaults: &template.FieldDefaults{
				Unit:     gPanel.FieldConfig.Defaults.Unit,
				Min:      gPanel.FieldConfig.Defaults.Min,
				Max:      gPanel.FieldConfig.Defaults.Max,
				Decimals: gPanel.FieldConfig.Defaults.Decimals,
				Custom:   gPanel.FieldConfig.Defaults.Custom,
			},
		}

		// Convert thresholds
		if gPanel.FieldConfig.Defaults.Thresholds != nil {
			steps := make([]template.Threshold, 0, len(gPanel.FieldConfig.Defaults.Thresholds.Steps))
			for _, s := range gPanel.FieldConfig.Defaults.Thresholds.Steps {
				steps = append(steps, template.Threshold{
					Color: s.Color,
					Value: s.Value,
				})
			}
			fieldConfig.Defaults.Thresholds = &template.Thresholds{
				Mode:  gPanel.FieldConfig.Defaults.Thresholds.Mode,
				Steps: steps,
			}
		}
	}

	return template.Panel{
		ID:          gPanel.ID,
		Type:        gPanel.Type,
		Title:       gPanel.Title,
		Description: gPanel.Description,
		GridPos: &template.GridPos{
			H: gPanel.GridPos.H,
			W: gPanel.GridPos.W,
			X: gPanel.GridPos.X,
			Y: gPanel.GridPos.Y,
		},
		Targets:     targets,
		Options:     gPanel.Options,
		FieldConfig: fieldConfig,
		Datasource: &template.Datasource{
			Type: "prometheus",
			UID:  "prometheus",
		},
	}
}

// convertToTemplateVariable converts grafana.Variable to template.Variable
func (b *Builder) convertToTemplateVariable(gVar grafana.Variable) template.Variable {
	var currentVal *template.VarCurrent
	if gVar.Current != nil {
		currentVal = &template.VarCurrent{
			Selected: gVar.Current.Selected,
			Text:     gVar.Current.Text,
			Value:    gVar.Current.Value,
		}
	}

	// Extract query string
	queryStr := ""
	switch q := gVar.Query.(type) {
	case string:
		queryStr = q
	case map[string]interface{}:
		if qs, ok := q["query"].(string); ok {
			queryStr = qs
		}
	}

	var datasource *template.Datasource
	if gVar.DataSource != nil {
		datasource = &template.Datasource{
			Type: gVar.DataSource.Type,
			UID:  gVar.DataSource.UID,
		}
	}

	return template.Variable{
		Name:       gVar.Name,
		Type:       gVar.Type,
		Datasource: datasource,
		Query:      queryStr,
		Multi:      gVar.Multi,
		IncludeAll: gVar.IncludeAll,
		Current:    currentVal,
		Label:      gVar.Label,
		Hide:       gVar.Hide,
		AllValue:   gVar.AllValue,
		Refresh:    gVar.Refresh,
		Regex:      gVar.Regex,
	}
}

// ListModules returns all modules available for dashboard generation
func (b *Builder) ListModules() ([]*mib.ModuleInfo, error) {
	modules, err := b.storage.ListModules()
	if err != nil {
		return nil, fmt.Errorf("failed to list modules: %w", err)
	}

	// Sort by name
	sort.Slice(modules, func(i, j int) bool {
		return modules[i].Name < modules[j].Name
	})

	return modules, nil
}

// GetModulePollableCount returns the count of pollable OIDs for a module
func (b *Builder) GetModulePollableCount(moduleName string) (int, error) {
	pollableOIDs, err := b.storage.GetPollableOIDs(moduleName)
	if err != nil {
		return 0, err
	}
	return len(pollableOIDs), nil
}

// ValidateModule checks if a module can generate a dashboard
func (b *Builder) ValidateModule(moduleName string) error {
	module, err := b.storage.GetModule(moduleName)
	if err != nil {
		return fmt.Errorf("module not found: %w", err)
	}

	pollableOIDs, err := b.storage.GetPollableOIDs(moduleName)
	if err != nil {
		return fmt.Errorf("failed to get pollable OIDs: %w", err)
	}

	if len(pollableOIDs) == 0 {
		return fmt.Errorf("module %s has no pollable OIDs", module.Name)
	}

	return nil
}

// filterOIDsByPrometheus filters OIDs to only those that have metrics in Prometheus
func (b *Builder) filterOIDsByPrometheus(oids []*mib.OIDMetadata) ([]*mib.OIDMetadata, error) {
	ctx := context.Background()
	filtered := make([]*mib.OIDMetadata, 0)

	for _, oid := range oids {
		// Every row of a column shares the column's metric name, so asking
		// whether the object has any data is an exact-name lookup rather than
		// the prefix scan the old one-name-per-row scheme forced.
		exists, err := b.promClient.MetricExists(ctx, ObjectMetricName(oid.Name, b.config.MetricPrefix))
		if err != nil {
			return nil, fmt.Errorf("failed to query prometheus for OID %s: %w", oid.OID, err)
		}

		if exists {
			filtered = append(filtered, oid)
		}
	}

	return filtered, nil
}
