package template

import (
	"testing"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/mib"
)

func TestNewDashboardSpec(t *testing.T) {
	module := &mib.ModuleInfo{
		Name:        "IF-MIB",
		Description: "Interfaces MIB",
		OIDCount:    10,
		LoadedAt:    time.Now(),
	}

	spec := NewDashboardSpec(module)

	if spec == nil {
		t.Fatal("expected spec to be created")
	}

	if spec.Module != module {
		t.Error("expected module to be set")
	}

	if len(spec.Panels) != 0 {
		t.Errorf("expected 0 panels, got %d", len(spec.Panels))
	}

	if len(spec.Variables) != 0 {
		t.Errorf("expected 0 variables, got %d", len(spec.Variables))
	}

	if len(spec.Tags) != 3 {
		t.Errorf("expected 3 tags, got %d", len(spec.Tags))
	}
}

func TestToGrafanaDashboard(t *testing.T) {
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

	dashboard, err := spec.ToGrafanaDashboard()
	if err != nil {
		t.Fatalf("failed to convert spec: %v", err)
	}

	if dashboard.UID != "if-mib" {
		t.Errorf("expected UID 'if-mib', got %s", dashboard.UID)
	}

	if dashboard.SchemaVersion != 39 {
		t.Errorf("expected schema version 39, got %d", dashboard.SchemaVersion)
	}

	if dashboard.Timezone != "browser" {
		t.Errorf("expected timezone 'browser', got %s", dashboard.Timezone)
	}

	if !dashboard.Editable {
		t.Error("expected editable to be true")
	}

	if dashboard.Refresh != "30s" {
		t.Errorf("expected refresh '30s', got %s", dashboard.Refresh)
	}

	if len(dashboard.Panels) != 1 {
		t.Errorf("expected 1 panel, got %d", len(dashboard.Panels))
	}

	// Should have device variable
	if len(dashboard.Templating.List) == 0 {
		t.Fatal("expected at least one variable (device)")
	}

	deviceVarFound := false
	for _, v := range dashboard.Templating.List {
		if v.Name == "device" {
			deviceVarFound = true
			break
		}
	}

	if !deviceVarFound {
		t.Error("expected device variable to be present")
	}
}

func TestToGrafanaDashboardNilModule(t *testing.T) {
	spec := &Spec{}

	_, err := spec.ToGrafanaDashboard()
	if err == nil {
		t.Fatal("expected error for nil module")
	}
}

func TestToGrafanaDashboardCustomTitle(t *testing.T) {
	module := &mib.ModuleInfo{
		Name:        "IF-MIB",
		Description: "Interfaces MIB",
		OIDCount:    10,
		LoadedAt:    time.Now(),
	}

	spec := NewDashboardSpec(module)
	spec.SetTitle("Custom Title")

	dashboard, err := spec.ToGrafanaDashboard()
	if err != nil {
		t.Fatalf("failed to convert spec: %v", err)
	}

	if dashboard.Title != "Custom Title" {
		t.Errorf("expected title 'Custom Title', got %s", dashboard.Title)
	}
}

func TestToGrafanaDashboardLongTitle(t *testing.T) {
	module := &mib.ModuleInfo{
		Name:        "VERY-LONG-MODULE-NAME",
		Description: "This is a very long description that exceeds the maximum length allowed for dashboard titles and should be truncated",
		OIDCount:    10,
		LoadedAt:    time.Now(),
	}

	spec := NewDashboardSpec(module)

	dashboard, err := spec.ToGrafanaDashboard()
	if err != nil {
		t.Fatalf("failed to convert spec: %v", err)
	}

	if len(dashboard.Title) > 100 {
		t.Errorf("expected title to be truncated to 100 chars, got %d", len(dashboard.Title))
	}
}

func TestGenerateUID(t *testing.T) {
	tests := []struct {
		name       string
		moduleName string
		want       string
	}{
		{
			name:       "simple name",
			moduleName: "IF-MIB",
			want:       "if-mib",
		},
		{
			name:       "with underscore",
			moduleName: "IF_MIB",
			want:       "if-mib",
		},
		{
			name:       "with space",
			moduleName: "IF MIB",
			want:       "if-mib",
		},
		{
			name:       "mixed case",
			moduleName: "IfMib",
			want:       "ifmib",
		},
		{
			name:       "complex name",
			moduleName: "CISCO_STACK_MIB",
			want:       "cisco-stack-mib",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := generateUID(tt.moduleName)
			if got != tt.want {
				t.Errorf("generateUID() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnsureDeviceVariable(t *testing.T) {
	tests := []struct {
		name            string
		variables       []Variable
		wantLen         int
		checkProperties bool
	}{
		{
			name:            "empty variables",
			variables:       []Variable{},
			wantLen:         1,
			checkProperties: true,
		},
		{
			name: "existing device variable",
			variables: []Variable{
				{Name: "device", Type: "query"},
			},
			wantLen:         1,
			checkProperties: false, // Don't check - existing variable is kept as-is
		},
		{
			name: "other variables",
			variables: []Variable{
				{Name: "interface", Type: "query"},
			},
			wantLen:         2,
			checkProperties: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ensureDeviceVariable(tt.variables)
			if len(result) != tt.wantLen {
				t.Errorf("expected %d variables, got %d", tt.wantLen, len(result))
			}

			// First variable should always be device
			if result[0].Name != "device" {
				t.Errorf("expected first variable to be 'device', got %s", result[0].Name)
			}

			// Only check properties when we create a new device variable
			if tt.checkProperties {
				deviceVar := result[0]
				if deviceVar.Type != "query" {
					t.Errorf("expected type 'query', got %s", deviceVar.Type)
				}

				if deviceVar.Datasource == nil {
					t.Error("expected datasource to be set")
				} else if deviceVar.Datasource.Type != "prometheus" || deviceVar.Datasource.UID != "prometheus" {
					t.Errorf("expected prometheus datasource, got %+v", *deviceVar.Datasource)
				}

				if !deviceVar.Multi {
					t.Error("expected multi to be true")
				}

				if !deviceVar.IncludeAll {
					t.Error("expected includeAll to be true")
				}
			}
		})
	}
}

func TestAddPanel(t *testing.T) {
	module := &mib.ModuleInfo{
		Name:     "IF-MIB",
		LoadedAt: time.Now(),
	}

	spec := NewDashboardSpec(module)

	panel1 := Panel{
		ID:    1,
		Type:  "graph",
		Title: "Panel 1",
		GridPos: &GridPos{
			H: 8,
			W: 12,
			X: 0,
			Y: 0,
		},
	}

	panel2 := Panel{
		ID:    2,
		Type:  "stat",
		Title: "Panel 2",
		GridPos: &GridPos{
			H: 8,
			W: 12,
			X: 12,
			Y: 0,
		},
	}

	spec.AddPanel(panel1)
	spec.AddPanel(panel2)

	if len(spec.Panels) != 2 {
		t.Errorf("expected 2 panels, got %d", len(spec.Panels))
	}

	if spec.Panels[0].ID != 1 {
		t.Errorf("expected panel 0 to have ID 1, got %d", spec.Panels[0].ID)
	}

	if spec.Panels[1].ID != 2 {
		t.Errorf("expected panel 1 to have ID 2, got %d", spec.Panels[1].ID)
	}
}

func TestAddVariable(t *testing.T) {
	module := &mib.ModuleInfo{
		Name:     "IF-MIB",
		LoadedAt: time.Now(),
	}

	spec := NewDashboardSpec(module)

	variable := Variable{
		Name: "interface",
		Type: "query",
		Datasource: &Datasource{
			Type: "prometheus",
			UID:  "prometheus",
		},
		Query: "label_values(ifIndex)",
	}

	spec.AddVariable(variable)

	if len(spec.Variables) != 1 {
		t.Errorf("expected 1 variable, got %d", len(spec.Variables))
	}

	if spec.Variables[0].Name != "interface" {
		t.Errorf("expected variable name 'interface', got %s", spec.Variables[0].Name)
	}
}

func TestSetTitle(t *testing.T) {
	module := &mib.ModuleInfo{
		Name:     "IF-MIB",
		LoadedAt: time.Now(),
	}

	spec := NewDashboardSpec(module)
	spec.SetTitle("Custom Title")

	if spec.Title != "Custom Title" {
		t.Errorf("expected title 'Custom Title', got %s", spec.Title)
	}
}

func TestSetDescription(t *testing.T) {
	module := &mib.ModuleInfo{
		Name:     "IF-MIB",
		LoadedAt: time.Now(),
	}

	spec := NewDashboardSpec(module)
	spec.SetDescription("Custom Description")

	if spec.Description != "Custom Description" {
		t.Errorf("expected description 'Custom Description', got %s", spec.Description)
	}
}

func TestAddTag(t *testing.T) {
	module := &mib.ModuleInfo{
		Name:     "IF-MIB",
		LoadedAt: time.Now(),
	}

	spec := NewDashboardSpec(module)
	initialLen := len(spec.Tags)

	spec.AddTag("custom-tag")

	if len(spec.Tags) != initialLen+1 {
		t.Errorf("expected %d tags, got %d", initialLen+1, len(spec.Tags))
	}

	found := false
	for _, tag := range spec.Tags {
		if tag == "custom-tag" {
			found = true
			break
		}
	}

	if !found {
		t.Error("custom-tag not found in tags")
	}
}

func TestSpecValidate(t *testing.T) {
	tests := []struct {
		name    string
		spec    *Spec
		wantErr bool
	}{
		{
			name: "valid spec",
			spec: &Spec{
				Module: &mib.ModuleInfo{
					Name:     "IF-MIB",
					LoadedAt: time.Now(),
				},
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
				Variables: []Variable{
					{Name: "device", Type: "query"},
				},
			},
			wantErr: false,
		},
		{
			name: "nil module",
			spec: &Spec{
				Panels:    []Panel{},
				Variables: []Variable{},
			},
			wantErr: true,
		},
		{
			name: "empty module name",
			spec: &Spec{
				Module: &mib.ModuleInfo{
					LoadedAt: time.Now(),
				},
				Panels:    []Panel{},
				Variables: []Variable{},
			},
			wantErr: true,
		},
		{
			name: "duplicate panel IDs",
			spec: &Spec{
				Module: &mib.ModuleInfo{
					Name:     "IF-MIB",
					LoadedAt: time.Now(),
				},
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
						Type:  "stat",
						Title: "Panel 2",
						GridPos: &GridPos{
							H: 8,
							W: 12,
							X: 12,
							Y: 0,
						},
					},
				},
				Variables: []Variable{},
			},
			wantErr: true,
		},
		{
			name: "empty variable name",
			spec: &Spec{
				Module: &mib.ModuleInfo{
					Name:     "IF-MIB",
					LoadedAt: time.Now(),
				},
				Panels: []Panel{},
				Variables: []Variable{
					{Type: "query"},
				},
			},
			wantErr: true,
		},
		{
			name: "duplicate variable names",
			spec: &Spec{
				Module: &mib.ModuleInfo{
					Name:     "IF-MIB",
					LoadedAt: time.Now(),
				},
				Panels: []Panel{},
				Variables: []Variable{
					{Name: "device", Type: "query"},
					{Name: "device", Type: "query"},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.spec.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
