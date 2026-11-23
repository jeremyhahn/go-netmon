package template

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/mib"
)

func TestNew(t *testing.T) {
	engine := New()
	if engine == nil {
		t.Fatal("expected engine to be created")
	}

	if engine.baseTemplate == nil {
		t.Fatal("expected base template to be created")
	}

	// Verify base template defaults
	if engine.baseTemplate.SchemaVersion != 39 {
		t.Errorf("expected schema version 39, got %d", engine.baseTemplate.SchemaVersion)
	}

	if engine.baseTemplate.Timezone != "browser" {
		t.Errorf("expected timezone 'browser', got %s", engine.baseTemplate.Timezone)
	}

	if !engine.baseTemplate.Editable {
		t.Error("expected editable to be true")
	}

	if engine.baseTemplate.Refresh != "30s" {
		t.Errorf("expected refresh '30s', got %s", engine.baseTemplate.Refresh)
	}
}

func TestRender(t *testing.T) {
	engine := New()

	module := &mib.ModuleInfo{
		Name:        "IF-MIB",
		Description: "Interfaces MIB",
		OIDCount:    10,
		LoadedAt:    time.Now(),
	}

	spec := NewDashboardSpec(module)
	spec.AddPanel(Panel{
		ID:    1,
		Type:  "graph",
		Title: "Test Panel",
		GridPos: &GridPos{
			H: 8,
			W: 12,
			X: 0,
			Y: 0,
		},
	})

	data, err := engine.Render(spec)
	if err != nil {
		t.Fatalf("failed to render: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("expected data to be rendered")
	}

	// Verify JSON is valid
	var dashboard Dashboard
	if err := json.Unmarshal(data, &dashboard); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if dashboard.UID != "if-mib" {
		t.Errorf("expected UID 'if-mib', got %s", dashboard.UID)
	}

	if len(dashboard.Panels) != 1 {
		t.Errorf("expected 1 panel, got %d", len(dashboard.Panels))
	}
}

func TestRenderNilSpec(t *testing.T) {
	engine := New()

	_, err := engine.Render(nil)
	if err == nil {
		t.Fatal("expected error for nil spec")
	}
}

func TestRenderInvalidSpec(t *testing.T) {
	engine := New()

	// Spec with no module
	spec := &Spec{}

	_, err := engine.Render(spec)
	if err == nil {
		t.Fatal("expected error for invalid spec")
	}
}

func TestRenderToFile(t *testing.T) {
	engine := New()

	// Create temp directory
	tmpDir := t.TempDir()
	filename := filepath.Join(tmpDir, "test-dashboard.json")

	module := &mib.ModuleInfo{
		Name:        "TEST-MIB",
		Description: "Test MIB",
		OIDCount:    5,
		LoadedAt:    time.Now(),
	}

	spec := NewDashboardSpec(module)
	spec.AddPanel(Panel{
		ID:    1,
		Type:  "graph",
		Title: "Test Panel",
		GridPos: &GridPos{
			H: 8,
			W: 12,
			X: 0,
			Y: 0,
		},
	})

	if err := engine.RenderToFile(spec, filename); err != nil {
		t.Fatalf("failed to render to file: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		t.Fatal("file was not created")
	}

	// Verify file content
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	var dashboard Dashboard
	if err := json.Unmarshal(data, &dashboard); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if dashboard.UID != "test-mib" {
		t.Errorf("expected UID 'test-mib', got %s", dashboard.UID)
	}
}

func TestRenderToFileEmptyFilename(t *testing.T) {
	engine := New()

	module := &mib.ModuleInfo{
		Name:     "TEST-MIB",
		LoadedAt: time.Now(),
	}

	spec := NewDashboardSpec(module)

	err := engine.RenderToFile(spec, "")
	if err == nil {
		t.Fatal("expected error for empty filename")
	}
}

func TestValidate(t *testing.T) {
	engine := New()

	tests := []struct {
		name      string
		dashboard *Dashboard
		wantErr   bool
	}{
		{
			name:      "nil dashboard",
			dashboard: nil,
			wantErr:   true,
		},
		{
			name: "missing UID",
			dashboard: &Dashboard{
				Title:         "Test",
				SchemaVersion: 39,
				Templating:    &Templating{List: []Variable{}},
				Time:          &TimeRange{From: "now-6h", To: "now"},
			},
			wantErr: true,
		},
		{
			name: "missing title",
			dashboard: &Dashboard{
				UID:           "test",
				SchemaVersion: 39,
				Templating:    &Templating{List: []Variable{}},
				Time:          &TimeRange{From: "now-6h", To: "now"},
			},
			wantErr: true,
		},
		{
			name: "missing schema version",
			dashboard: &Dashboard{
				UID:        "test",
				Title:      "Test",
				Templating: &Templating{List: []Variable{}},
				Time:       &TimeRange{From: "now-6h", To: "now"},
			},
			wantErr: true,
		},
		{
			name: "missing templating",
			dashboard: &Dashboard{
				UID:           "test",
				Title:         "Test",
				SchemaVersion: 39,
				Time:          &TimeRange{From: "now-6h", To: "now"},
			},
			wantErr: true,
		},
		{
			name: "missing time range",
			dashboard: &Dashboard{
				UID:           "test",
				Title:         "Test",
				SchemaVersion: 39,
				Templating:    &Templating{List: []Variable{}},
			},
			wantErr: true,
		},
		{
			name: "valid dashboard",
			dashboard: &Dashboard{
				UID:           "test",
				Title:         "Test",
				SchemaVersion: 39,
				Templating:    &Templating{List: []Variable{}},
				Time:          &TimeRange{From: "now-6h", To: "now"},
				Panels:        []Panel{},
			},
			wantErr: false,
		},
		{
			name: "valid dashboard with panels",
			dashboard: &Dashboard{
				UID:           "test",
				Title:         "Test",
				SchemaVersion: 39,
				Templating:    &Templating{List: []Variable{}},
				Time:          &TimeRange{From: "now-6h", To: "now"},
				Panels: []Panel{
					{
						ID:    1,
						Type:  "graph",
						Title: "Panel 1",
						GridPos: &GridPos{
							H: 8,
							W: 12,
							X: 0,
							Y: 0,
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "duplicate panel IDs",
			dashboard: &Dashboard{
				UID:           "test",
				Title:         "Test",
				SchemaVersion: 39,
				Templating:    &Templating{List: []Variable{}},
				Time:          &TimeRange{From: "now-6h", To: "now"},
				Panels: []Panel{
					{
						ID:    1,
						Type:  "graph",
						Title: "Panel 1",
						GridPos: &GridPos{
							H: 8,
							W: 12,
							X: 0,
							Y: 0,
						},
					},
					{
						ID:    1,
						Type:  "graph",
						Title: "Panel 2",
						GridPos: &GridPos{
							H: 8,
							W: 12,
							X: 12,
							Y: 0,
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "invalid panel ID",
			dashboard: &Dashboard{
				UID:           "test",
				Title:         "Test",
				SchemaVersion: 39,
				Templating:    &Templating{List: []Variable{}},
				Time:          &TimeRange{From: "now-6h", To: "now"},
				Panels: []Panel{
					{
						ID:    0,
						Type:  "graph",
						Title: "Panel 1",
						GridPos: &GridPos{
							H: 8,
							W: 12,
							X: 0,
							Y: 0,
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "missing panel type",
			dashboard: &Dashboard{
				UID:           "test",
				Title:         "Test",
				SchemaVersion: 39,
				Templating:    &Templating{List: []Variable{}},
				Time:          &TimeRange{From: "now-6h", To: "now"},
				Panels: []Panel{
					{
						ID:    1,
						Title: "Panel 1",
						GridPos: &GridPos{
							H: 8,
							W: 12,
							X: 0,
							Y: 0,
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "missing grid position",
			dashboard: &Dashboard{
				UID:           "test",
				Title:         "Test",
				SchemaVersion: 39,
				Templating:    &Templating{List: []Variable{}},
				Time:          &TimeRange{From: "now-6h", To: "now"},
				Panels: []Panel{
					{
						ID:    1,
						Type:  "graph",
						Title: "Panel 1",
					},
				},
			},
			wantErr: true,
		},
		{
			name: "duplicate variable names",
			dashboard: &Dashboard{
				UID:           "test",
				Title:         "Test",
				SchemaVersion: 39,
				Templating: &Templating{
					List: []Variable{
						{Name: "device", Type: "query"},
						{Name: "device", Type: "query"},
					},
				},
				Time:   &TimeRange{From: "now-6h", To: "now"},
				Panels: []Panel{},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := engine.Validate(tt.dashboard)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateGridPos(t *testing.T) {
	tests := []struct {
		name    string
		pos     *GridPos
		wantErr bool
	}{
		{
			name:    "valid position",
			pos:     &GridPos{H: 8, W: 12, X: 0, Y: 0},
			wantErr: false,
		},
		{
			name:    "width too small",
			pos:     &GridPos{H: 8, W: 0, X: 0, Y: 0},
			wantErr: true,
		},
		{
			name:    "width too large",
			pos:     &GridPos{H: 8, W: 25, X: 0, Y: 0},
			wantErr: true,
		},
		{
			name:    "height too small",
			pos:     &GridPos{H: 0, W: 12, X: 0, Y: 0},
			wantErr: true,
		},
		{
			name:    "negative x",
			pos:     &GridPos{H: 8, W: 12, X: -1, Y: 0},
			wantErr: true,
		},
		{
			name:    "x too large",
			pos:     &GridPos{H: 8, W: 12, X: 24, Y: 0},
			wantErr: true,
		},
		{
			name:    "negative y",
			pos:     &GridPos{H: 8, W: 12, X: 0, Y: -1},
			wantErr: true,
		},
		{
			name:    "panel extends beyond grid",
			pos:     &GridPos{H: 8, W: 12, X: 13, Y: 0},
			wantErr: true,
		},
		{
			name:    "full width panel",
			pos:     &GridPos{H: 8, W: 24, X: 0, Y: 0},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateGridPos(tt.pos)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateGridPos() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRenderMultiple(t *testing.T) {
	engine := New()

	modules := []*mib.ModuleInfo{
		{Name: "IF-MIB", Description: "Interfaces MIB", OIDCount: 10, LoadedAt: time.Now()},
		{Name: "IP-MIB", Description: "IP MIB", OIDCount: 15, LoadedAt: time.Now()},
	}

	specs := make([]*Spec, len(modules))
	for i, module := range modules {
		spec := NewDashboardSpec(module)
		spec.AddPanel(Panel{
			ID:    1,
			Type:  "graph",
			Title: "Test Panel",
			GridPos: &GridPos{
				H: 8,
				W: 12,
				X: 0,
				Y: 0,
			},
		})
		specs[i] = spec
	}

	results, err := engine.RenderMultiple(specs)
	if err != nil {
		t.Fatalf("failed to render multiple: %v", err)
	}

	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}

	if _, ok := results["IF-MIB"]; !ok {
		t.Error("expected IF-MIB in results")
	}

	if _, ok := results["IP-MIB"]; !ok {
		t.Error("expected IP-MIB in results")
	}
}

func TestValidateJSON(t *testing.T) {
	engine := New()

	module := &mib.ModuleInfo{
		Name:        "TEST-MIB",
		Description: "Test MIB",
		OIDCount:    5,
		LoadedAt:    time.Now(),
	}

	spec := NewDashboardSpec(module)
	spec.AddPanel(Panel{
		ID:    1,
		Type:  "graph",
		Title: "Test Panel",
		GridPos: &GridPos{
			H: 8,
			W: 12,
			X: 0,
			Y: 0,
		},
	})

	data, err := engine.Render(spec)
	if err != nil {
		t.Fatalf("failed to render: %v", err)
	}

	// Validate the rendered JSON
	if err := engine.ValidateJSON(data); err != nil {
		t.Errorf("ValidateJSON() error = %v", err)
	}

	// Test invalid JSON
	invalidJSON := []byte("invalid json")
	if err := engine.ValidateJSON(invalidJSON); err == nil {
		t.Error("expected error for invalid JSON")
	}
}
