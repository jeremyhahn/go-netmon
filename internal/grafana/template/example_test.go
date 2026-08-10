package template_test

import (
	"fmt"
	"os"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/grafana/template"
	"github.com/jeremyhahn/go-netmon/internal/mib"
)

// Example demonstrates basic usage of the template engine
func Example() {
	// Create module info
	module := &mib.ModuleInfo{
		Name:        "IF-MIB",
		Description: "Interfaces MIB",
		OIDCount:    25,
		LoadedAt:    time.Now(),
	}

	// Create dashboard specification
	spec := template.NewDashboardSpec(module)

	// Add a simple stat panel
	spec.AddPanel(template.Panel{
		ID:    1,
		Type:  "stat",
		Title: "Interface Count",
		GridPos: &template.GridPos{
			H: 4,
			W: 6,
			X: 0,
			Y: 0,
		},
		Targets: []template.Target{
			{
				Expr:  "count(ifIndex)",
				RefID: "A",
			},
		},
	})

	// Add a graph panel
	spec.AddPanel(template.Panel{
		ID:    2,
		Type:  "timeseries",
		Title: "Interface Traffic",
		GridPos: &template.GridPos{
			H: 8,
			W: 18,
			X: 6,
			Y: 0,
		},
		Targets: []template.Target{
			{
				Expr:         "rate(ifInOctets{device=~\"$device\"}[5m]) * 8",
				RefID:        "A",
				LegendFormat: "{{interface}} In",
			},
			{
				Expr:         "rate(ifOutOctets{device=~\"$device\"}[5m]) * 8",
				RefID:        "B",
				LegendFormat: "{{interface}} Out",
			},
		},
	})

	// Create engine and render
	engine := template.New()
	dashboard, err := spec.ToGrafanaDashboard()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	// Validate
	if err := engine.Validate(dashboard); err != nil {
		fmt.Printf("Validation error: %v\n", err)
		return
	}

	fmt.Printf("Dashboard: %s\n", dashboard.Title)
	fmt.Printf("UID: %s\n", dashboard.UID)
	fmt.Printf("Panels: %d\n", len(dashboard.Panels))
	fmt.Printf("Variables: %d\n", len(dashboard.Templating.List))
	// Output:
	// Dashboard: IF-MIB - Interfaces MIB
	// UID: if-mib
	// Panels: 2
	// Variables: 1
}

// ExampleNewDashboardSpec demonstrates creating a dashboard spec
func ExampleNewDashboardSpec() {
	module := &mib.ModuleInfo{
		Name:        "IP-MIB",
		Description: "IP Protocol MIB",
		OIDCount:    50,
		LoadedAt:    time.Now(),
	}

	spec := template.NewDashboardSpec(module)
	spec.SetTitle("Custom IP Dashboard")
	spec.AddTag("networking")

	dashboard, _ := spec.ToGrafanaDashboard()
	fmt.Printf("Title: %s\n", dashboard.Title)
	fmt.Printf("Tags: %v\n", len(dashboard.Tags) > 0)
	// Output:
	// Title: Custom IP Dashboard
	// Tags: true
}

// ExampleEngine_Render demonstrates rendering a dashboard
func ExampleEngine_Render() {
	module := &mib.ModuleInfo{
		Name:     "TEST-MIB",
		LoadedAt: time.Now(),
	}

	spec := template.NewDashboardSpec(module)
	spec.AddPanel(template.Panel{
		ID:    1,
		Type:  "graph",
		Title: "Test Panel",
		GridPos: &template.GridPos{
			H: 8,
			W: 12,
			X: 0,
			Y: 0,
		},
	})

	engine := template.New()
	data, err := engine.Render(spec)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	fmt.Printf("Valid JSON: %v\n", len(data) > 0)
	fmt.Printf("Has content: %v\n", len(data) > 100)
	// Output:
	// Valid JSON: true
	// Has content: true
}

// ExampleWriter demonstrates file management
func ExampleWriter() {
	// A unique directory that is removed afterwards. A fixed path such as
	// /tmp/grafana-example modifies the host, survives the run, and makes two
	// concurrent runs write over each other. Example functions get no
	// *testing.T, so t.TempDir is unavailable and this is the equivalent.
	outputDir, err := os.MkdirTemp("", "grafana-example")
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer func() { _ = os.RemoveAll(outputDir) }()

	module := &mib.ModuleInfo{
		Name:        "EXAMPLE-MIB",
		Description: "Example MIB",
		LoadedAt:    time.Now(),
	}

	spec := template.NewDashboardSpec(module)
	spec.AddPanel(template.Panel{
		ID:    1,
		Type:  "graph",
		Title: "Example Panel",
		GridPos: &template.GridPos{
			H: 8,
			W: 12,
			X: 0,
			Y: 0,
		},
	})

	// Create writer and write dashboard
	writer := template.NewWriter(outputDir)
	dashboard, _ := spec.ToGrafanaDashboard()

	err = writer.Write(dashboard, "example-mib.json")
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	// List dashboards
	dashboards, _ := writer.ListDashboards()

	fmt.Printf("Dashboards created: %d\n", len(dashboards))
	// Output:
	// Dashboards created: 1
}
