// Package template renders dashboard specifications into Grafana dashboard JSON
// and writes them to a provisioning directory.
package template

import (
	"encoding/json"
	"fmt"
	"os"
)

// Engine is the template engine for rendering Grafana dashboards
type Engine struct {
	baseTemplate *Dashboard
}

// New creates a new template engine
func New() *Engine {
	return &Engine{
		baseTemplate: createBaseTemplate(),
	}
}

// createBaseTemplate creates a base dashboard template with sensible defaults
func createBaseTemplate() *Dashboard {
	return &Dashboard{
		Timezone:      "browser",
		Editable:      true,
		SchemaVersion: 39,
		Refresh:       "30s",
		Time: &TimeRange{
			From: "now-6h",
			To:   "now",
		},
		Templating: &Templating{
			List: []Variable{},
		},
		Panels: []Panel{},
	}
}

// Render generates final dashboard JSON
func (e *Engine) Render(spec *Spec) ([]byte, error) {
	if spec == nil {
		return nil, fmt.Errorf("spec is nil")
	}

	// Validate spec
	if err := spec.Validate(); err != nil {
		return nil, fmt.Errorf("invalid spec: %w", err)
	}

	// Convert spec to dashboard
	dashboard, err := spec.ToGrafanaDashboard()
	if err != nil {
		return nil, fmt.Errorf("failed to convert spec: %w", err)
	}

	// Validate dashboard
	if validateErr := e.Validate(dashboard); validateErr != nil {
		return nil, fmt.Errorf("invalid dashboard: %w", validateErr)
	}

	// Marshal to JSON with indentation for readability
	data, err := json.MarshalIndent(dashboard, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal dashboard: %w", err)
	}

	return data, nil
}

// RenderToFile writes dashboard to file
func (e *Engine) RenderToFile(spec *Spec, filename string) error {
	if filename == "" {
		return fmt.Errorf("filename is required")
	}

	// Render dashboard
	data, err := e.Render(spec)
	if err != nil {
		return fmt.Errorf("failed to render: %w", err)
	}

	// Write to file
	if err := os.WriteFile(filename, data, 0o644); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	return nil
}

// Validate checks dashboard JSON structure
func (e *Engine) Validate(dashboard *Dashboard) error {
	if dashboard == nil {
		return fmt.Errorf("dashboard is nil")
	}

	if dashboard.UID == "" {
		return fmt.Errorf("dashboard UID is required")
	}

	if dashboard.Title == "" {
		return fmt.Errorf("dashboard title is required")
	}

	if dashboard.SchemaVersion == 0 {
		return fmt.Errorf("dashboard schema version is required")
	}

	if dashboard.Templating == nil {
		return fmt.Errorf("dashboard templating is required")
	}

	if dashboard.Time == nil {
		return fmt.Errorf("dashboard time range is required")
	}

	// Validate panel IDs are sequential and start from 1
	panelIDs := make(map[int]bool)
	for _, panel := range dashboard.Panels {
		if panel.ID < 1 {
			return fmt.Errorf("panel ID must be >= 1, got: %d", panel.ID)
		}
		if panelIDs[panel.ID] {
			return fmt.Errorf("duplicate panel ID: %d", panel.ID)
		}
		panelIDs[panel.ID] = true

		// Validate panel type
		if panel.Type == "" {
			return fmt.Errorf("panel type is required for panel %d", panel.ID)
		}

		// Validate grid position
		if panel.GridPos == nil {
			return fmt.Errorf("panel grid position is required for panel %d", panel.ID)
		}
		if err := validateGridPos(panel.GridPos); err != nil {
			return fmt.Errorf("invalid grid position for panel %d: %w", panel.ID, err)
		}
	}

	// Validate variables
	varNames := make(map[string]bool)
	for _, variable := range dashboard.Templating.List {
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

// validateGridPos validates a grid position
func validateGridPos(pos *GridPos) error {
	if pos.W < 1 || pos.W > 24 {
		return fmt.Errorf("width must be 1-24, got: %d", pos.W)
	}
	if pos.H < 1 {
		return fmt.Errorf("height must be >= 1, got: %d", pos.H)
	}
	if pos.X < 0 || pos.X >= 24 {
		return fmt.Errorf("x position must be 0-23, got: %d", pos.X)
	}
	if pos.Y < 0 {
		return fmt.Errorf("y position must be >= 0, got: %d", pos.Y)
	}
	if pos.X+pos.W > 24 {
		return fmt.Errorf("panel extends beyond grid width: x=%d + w=%d > 24", pos.X, pos.W)
	}
	return nil
}

// RenderMultiple renders multiple dashboard specs
func (e *Engine) RenderMultiple(specs []*Spec) (map[string][]byte, error) {
	results := make(map[string][]byte)

	for _, spec := range specs {
		if spec.Module == nil {
			continue
		}

		data, err := e.Render(spec)
		if err != nil {
			return nil, fmt.Errorf("failed to render %s: %w", spec.Module.Name, err)
		}

		results[spec.Module.Name] = data
	}

	return results, nil
}

// ValidateJSON validates a dashboard JSON byte array
func (e *Engine) ValidateJSON(data []byte) error {
	var dashboard Dashboard
	if err := json.Unmarshal(data, &dashboard); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}

	return e.Validate(&dashboard)
}
