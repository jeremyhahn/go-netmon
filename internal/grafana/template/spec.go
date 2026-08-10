package template

import (
	"fmt"
	"strings"

	"github.com/jeremyhahn/go-netmon/internal/mib"
)

// Spec defines the specification for creating a dashboard
type Spec struct {
	Module      *mib.ModuleInfo
	Panels      []Panel
	Variables   []Variable
	Title       string
	Description string
	Tags        []string
}

// ToGrafanaDashboard converts the spec to a Grafana dashboard
func (ds *Spec) ToGrafanaDashboard() (*Dashboard, error) {
	if ds.Module == nil {
		return nil, fmt.Errorf("module is required")
	}

	// Generate UID from module name
	uid := generateUID(ds.Module.Name)

	// Use spec title or generate from module
	title := ds.Title
	if title == "" {
		title = fmt.Sprintf("%s - %s", ds.Module.Name, ds.Module.Description)
		if len(title) > 100 {
			title = title[:97] + "..."
		}
	}

	// Build description
	description := ds.Description
	if description == "" {
		description = fmt.Sprintf("Auto-generated dashboard for %s module", ds.Module.Name)
		if ds.Module.Description != "" {
			description += "\n\n" + ds.Module.Description
		}
	}

	// Build tags
	tags := ds.Tags
	if tags == nil {
		tags = []string{"snmp", "mib", strings.ToLower(ds.Module.Name)}
	}

	// Build variables - always include device selector
	variables := ds.Variables
	if variables == nil {
		variables = []Variable{}
	}
	variables = ensureDeviceVariable(variables)

	// Create dashboard
	dashboard := &Dashboard{
		UID:           uid,
		Title:         title,
		Description:   description,
		Tags:          tags,
		Timezone:      "browser",
		Editable:      true,
		SchemaVersion: 39,
		Templating: &Templating{
			List: variables,
		},
		Panels:  ds.Panels,
		Refresh: "30s",
		Time: &TimeRange{
			From: "now-6h",
			To:   "now",
		},
	}

	return dashboard, nil
}

// generateUID creates a UID from module name
func generateUID(moduleName string) string {
	// Convert to lowercase and replace underscores with hyphens
	uid := strings.ToLower(moduleName)
	uid = strings.ReplaceAll(uid, "_", "-")
	uid = strings.ReplaceAll(uid, " ", "-")
	return uid
}

// ensureDeviceVariable ensures device variable exists in the list
func ensureDeviceVariable(variables []Variable) []Variable {
	// Check if device variable already exists
	for _, v := range variables {
		if v.Name == "device" {
			return variables
		}
	}

	// Add device variable at the beginning
	deviceVar := Variable{
		Name: "device",
		Type: "query",
		Datasource: &Datasource{
			Type: "prometheus",
			UID:  "prometheus",
		},
		Query:      "label_values(snmp_up, device)",
		Multi:      true,
		IncludeAll: true,
		Current: &VarCurrent{
			Selected: true,
			Text:     "All",
			Value:    "$__all",
		},
		Label:   "Device",
		Refresh: 1, // On dashboard load
	}

	return append([]Variable{deviceVar}, variables...)
}

// NewDashboardSpec creates a new dashboard spec from module info
func NewDashboardSpec(module *mib.ModuleInfo) *Spec {
	return &Spec{
		Module:    module,
		Panels:    []Panel{},
		Variables: []Variable{},
		Tags:      []string{"snmp", "mib", strings.ToLower(module.Name)},
	}
}

// AddPanel adds a panel to the spec
func (ds *Spec) AddPanel(panel Panel) {
	ds.Panels = append(ds.Panels, panel)
}

// AddVariable adds a variable to the spec
func (ds *Spec) AddVariable(variable Variable) {
	ds.Variables = append(ds.Variables, variable)
}

// SetTitle sets the dashboard title
func (ds *Spec) SetTitle(title string) {
	ds.Title = title
}

// SetDescription sets the dashboard description
func (ds *Spec) SetDescription(description string) {
	ds.Description = description
}

// AddTag adds a tag to the dashboard
func (ds *Spec) AddTag(tag string) {
	ds.Tags = append(ds.Tags, tag)
}

// Validate validates the dashboard spec
func (ds *Spec) Validate() error {
	if ds.Module == nil {
		return fmt.Errorf("module is required")
	}

	if ds.Module.Name == "" {
		return fmt.Errorf("module name is required")
	}

	// Validate panel IDs are unique
	panelIDs := make(map[int]bool)
	for _, panel := range ds.Panels {
		if panelIDs[panel.ID] {
			return fmt.Errorf("duplicate panel ID: %d", panel.ID)
		}
		panelIDs[panel.ID] = true
	}

	// Validate variable names are unique
	varNames := make(map[string]bool)
	for _, variable := range ds.Variables {
		if variable.Name == "" {
			return fmt.Errorf("variable name is required")
		}
		if varNames[variable.Name] {
			return fmt.Errorf("duplicate variable name: %s", variable.Name)
		}
		varNames[variable.Name] = true
	}

	return nil
}
