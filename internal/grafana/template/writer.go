package template

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Writer handles writing dashboards to the filesystem
type Writer struct {
	outputDir string
	engine    *Engine
}

// NewWriter creates a new dashboard writer
func NewWriter(outputDir string) *Writer {
	return &Writer{
		outputDir: outputDir,
		engine:    New(),
	}
}

// Write saves dashboard to file
func (w *Writer) Write(dashboard *Dashboard, filename string) error {
	if dashboard == nil {
		return fmt.Errorf("dashboard is nil")
	}

	if filename == "" {
		return fmt.Errorf("filename is required")
	}

	// Validate dashboard
	if err := w.engine.Validate(dashboard); err != nil {
		return fmt.Errorf("invalid dashboard: %w", err)
	}

	// Ensure output directory exists
	if err := os.MkdirAll(w.outputDir, 0o755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Build full path
	fullPath := filepath.Join(w.outputDir, filename)

	// Marshal dashboard
	data, err := json.MarshalIndent(dashboard, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal dashboard: %w", err)
	}

	// Write to file
	if err := os.WriteFile(fullPath, data, 0o644); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	return nil
}

// WriteSpec saves a dashboard spec to file
func (w *Writer) WriteSpec(spec *Spec, filename string) error {
	if spec == nil {
		return fmt.Errorf("spec is nil")
	}

	// Convert spec to dashboard
	dashboard, err := spec.ToGrafanaDashboard()
	if err != nil {
		return fmt.Errorf("failed to convert spec: %w", err)
	}

	return w.Write(dashboard, filename)
}

// WriteAll saves multiple dashboards
func (w *Writer) WriteAll(dashboards []*Dashboard, dir string) error {
	if dir != "" {
		w.outputDir = dir
	}

	// Ensure output directory exists
	if err := os.MkdirAll(w.outputDir, 0o755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	for _, dashboard := range dashboards {
		if dashboard == nil {
			continue
		}

		// Generate filename from UID
		filename := fmt.Sprintf("%s.json", dashboard.UID)
		if err := w.Write(dashboard, filename); err != nil {
			return fmt.Errorf("failed to write %s: %w", filename, err)
		}
	}

	return nil
}

// WriteAllSpecs saves multiple dashboard specs
func (w *Writer) WriteAllSpecs(specs []*Spec) error {
	// Ensure output directory exists
	if err := os.MkdirAll(w.outputDir, 0o755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	for _, spec := range specs {
		if spec == nil || spec.Module == nil {
			continue
		}

		// Generate filename from module name
		filename := fmt.Sprintf("%s.json", strings.ToLower(strings.ReplaceAll(spec.Module.Name, "_", "-")))
		if err := w.WriteSpec(spec, filename); err != nil {
			return fmt.Errorf("failed to write %s: %w", filename, err)
		}
	}

	return nil
}

// Clean removes all generated dashboards
func (w *Writer) Clean(dir string) error {
	if dir != "" {
		w.outputDir = dir
	}

	// Check if directory exists
	if _, err := os.Stat(w.outputDir); os.IsNotExist(err) {
		return nil // Nothing to clean
	}

	// Read directory
	entries, err := os.ReadDir(w.outputDir)
	if err != nil {
		return fmt.Errorf("failed to read directory: %w", err)
	}

	// Remove all JSON files except index.json
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if strings.HasSuffix(name, ".json") && name != "index.json" {
			path := filepath.Join(w.outputDir, name)
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("failed to remove %s: %w", name, err)
			}
		}
	}

	return nil
}

// GenerateIndex creates catalog JSON
func (w *Writer) GenerateIndex(dashboards []*Dashboard) error {
	if len(dashboards) == 0 {
		return fmt.Errorf("no dashboards provided")
	}

	// Ensure output directory exists
	if err := os.MkdirAll(w.outputDir, 0o755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Build index entries
	entries := make([]IndexEntry, 0, len(dashboards))
	for _, dashboard := range dashboards {
		if dashboard == nil {
			continue
		}

		// Extract module name from tags
		module := ""
		for _, tag := range dashboard.Tags {
			if tag != "snmp" && tag != "mib" {
				module = tag
				break
			}
		}

		entry := IndexEntry{
			UID:       dashboard.UID,
			Title:     dashboard.Title,
			Module:    strings.ToUpper(module),
			Panels:    len(dashboard.Panels),
			Generated: time.Now(),
		}
		entries = append(entries, entry)
	}

	// Create index
	index := Index{
		Dashboards: entries,
	}

	// Marshal index
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal index: %w", err)
	}

	// Write index file
	indexPath := filepath.Join(w.outputDir, "index.json")
	if err := os.WriteFile(indexPath, data, 0o644); err != nil {
		return fmt.Errorf("failed to write index: %w", err)
	}

	return nil
}

// GenerateIndexFromSpecs creates catalog JSON from specs
func (w *Writer) GenerateIndexFromSpecs(specs []*Spec) error {
	dashboards := make([]*Dashboard, 0, len(specs))

	for _, spec := range specs {
		if spec == nil {
			continue
		}

		dashboard, err := spec.ToGrafanaDashboard()
		if err != nil {
			return fmt.Errorf("failed to convert spec: %w", err)
		}

		dashboards = append(dashboards, dashboard)
	}

	return w.GenerateIndex(dashboards)
}

// ListDashboards returns all dashboards in the output directory
func (w *Writer) ListDashboards() ([]string, error) {
	// Check if directory exists
	if _, err := os.Stat(w.outputDir); os.IsNotExist(err) {
		return []string{}, nil
	}

	// Read directory
	entries, err := os.ReadDir(w.outputDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	// Collect JSON files
	var dashboards []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if strings.HasSuffix(name, ".json") && name != "index.json" {
			dashboards = append(dashboards, name)
		}
	}

	return dashboards, nil
}

// LoadDashboard loads a dashboard from file
func (w *Writer) LoadDashboard(filename string) (*Dashboard, error) {
	if filename == "" {
		return nil, fmt.Errorf("filename is required")
	}

	// Build full path
	fullPath := filepath.Join(w.outputDir, filename)

	// Read file
	data, err := os.ReadFile(fullPath) // #nosec G304 -- confined to the writer's own output directory
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	// Unmarshal dashboard
	var dashboard Dashboard
	if err := json.Unmarshal(data, &dashboard); err != nil {
		return nil, fmt.Errorf("failed to unmarshal dashboard: %w", err)
	}

	return &dashboard, nil
}

// LoadIndex loads the dashboard index
func (w *Writer) LoadIndex() (*Index, error) {
	indexPath := filepath.Join(w.outputDir, "index.json")

	// Read file
	data, err := os.ReadFile(indexPath) // #nosec G304 -- fixed index.json in the writer's own output directory
	if err != nil {
		if os.IsNotExist(err) {
			// Return empty index if file doesn't exist
			return &Index{Dashboards: []IndexEntry{}}, nil
		}
		return nil, fmt.Errorf("failed to read index: %w", err)
	}

	// Unmarshal index
	var index Index
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, fmt.Errorf("failed to unmarshal index: %w", err)
	}

	return &index, nil
}
