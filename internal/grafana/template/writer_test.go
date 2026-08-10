package template

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/mib"
)

func TestNewWriter(t *testing.T) {
	tmpDir := t.TempDir()
	writer := NewWriter(tmpDir)

	if writer == nil {
		t.Fatal("expected writer to be created")
	}

	if writer.outputDir != tmpDir {
		t.Errorf("expected output dir %s, got %s", tmpDir, writer.outputDir)
	}

	if writer.engine == nil {
		t.Fatal("expected engine to be created")
	}
}

func TestWrite(t *testing.T) {
	tmpDir := t.TempDir()
	writer := NewWriter(tmpDir)

	dashboard := &Dashboard{
		UID:           "test",
		Title:         "Test Dashboard",
		SchemaVersion: 39,
		Templating:    &Templating{List: []Variable{}},
		Time:          &TimeRange{From: "now-6h", To: "now"},
		Panels:        []Panel{},
	}

	filename := "test-dashboard.json"
	if err := writer.Write(dashboard, filename); err != nil {
		t.Fatalf("failed to write: %v", err)
	}

	// Verify file exists
	fullPath := filepath.Join(tmpDir, filename)
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		t.Fatal("file was not created")
	}

	// Load and verify
	loaded, err := writer.LoadDashboard(filename)
	if err != nil {
		t.Fatalf("failed to load dashboard: %v", err)
	}

	if loaded.UID != dashboard.UID {
		t.Errorf("expected UID %s, got %s", dashboard.UID, loaded.UID)
	}
}

func TestWriteNilDashboard(t *testing.T) {
	tmpDir := t.TempDir()
	writer := NewWriter(tmpDir)

	err := writer.Write(nil, "test.json")
	if err == nil {
		t.Fatal("expected error for nil dashboard")
	}
}

func TestWriteEmptyFilename(t *testing.T) {
	tmpDir := t.TempDir()
	writer := NewWriter(tmpDir)

	dashboard := &Dashboard{
		UID:           "test",
		Title:         "Test",
		SchemaVersion: 39,
		Templating:    &Templating{List: []Variable{}},
		Time:          &TimeRange{From: "now-6h", To: "now"},
	}

	err := writer.Write(dashboard, "")
	if err == nil {
		t.Fatal("expected error for empty filename")
	}
}

func TestWriteSpec(t *testing.T) {
	tmpDir := t.TempDir()
	writer := NewWriter(tmpDir)

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

	filename := "test-mib.json"
	if err := writer.WriteSpec(spec, filename); err != nil {
		t.Fatalf("failed to write spec: %v", err)
	}

	// Verify file exists
	fullPath := filepath.Join(tmpDir, filename)
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		t.Fatal("file was not created")
	}

	// Load and verify
	loaded, err := writer.LoadDashboard(filename)
	if err != nil {
		t.Fatalf("failed to load dashboard: %v", err)
	}

	if loaded.UID != "test-mib" {
		t.Errorf("expected UID 'test-mib', got %s", loaded.UID)
	}
}

func TestWriteAll(t *testing.T) {
	tmpDir := t.TempDir()
	writer := NewWriter(tmpDir)

	dashboards := []*Dashboard{
		{
			UID:           "test1",
			Title:         "Test 1",
			SchemaVersion: 39,
			Templating:    &Templating{List: []Variable{}},
			Time:          &TimeRange{From: "now-6h", To: "now"},
			Panels:        []Panel{},
		},
		{
			UID:           "test2",
			Title:         "Test 2",
			SchemaVersion: 39,
			Templating:    &Templating{List: []Variable{}},
			Time:          &TimeRange{From: "now-6h", To: "now"},
			Panels:        []Panel{},
		},
	}

	if err := writer.WriteAll(dashboards, ""); err != nil {
		t.Fatalf("failed to write all: %v", err)
	}

	// Verify files exist
	for _, dashboard := range dashboards {
		filename := dashboard.UID + ".json"
		fullPath := filepath.Join(tmpDir, filename)
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			t.Errorf("file %s was not created", filename)
		}
	}
}

func TestWriteAllSpecs(t *testing.T) {
	tmpDir := t.TempDir()
	writer := NewWriter(tmpDir)

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

	if err := writer.WriteAllSpecs(specs); err != nil {
		t.Fatalf("failed to write all specs: %v", err)
	}

	// Verify files exist
	expectedFiles := []string{"if-mib.json", "ip-mib.json"}
	for _, filename := range expectedFiles {
		fullPath := filepath.Join(tmpDir, filename)
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			t.Errorf("file %s was not created", filename)
		}
	}
}

func TestClean(t *testing.T) {
	tmpDir := t.TempDir()
	writer := NewWriter(tmpDir)

	// Create some test files
	files := []string{"test1.json", "test2.json", "index.json", "readme.txt"}
	for _, filename := range files {
		path := filepath.Join(tmpDir, filename)
		if err := os.WriteFile(path, []byte("test"), 0644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}
	}

	// Clean
	if err := writer.Clean(""); err != nil {
		t.Fatalf("failed to clean: %v", err)
	}

	// Verify JSON files are removed (except index.json)
	for _, filename := range []string{"test1.json", "test2.json"} {
		path := filepath.Join(tmpDir, filename)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("file %s should have been removed", filename)
		}
	}

	// Verify index.json is kept
	indexPath := filepath.Join(tmpDir, "index.json")
	if _, err := os.Stat(indexPath); os.IsNotExist(err) {
		t.Error("index.json should not have been removed")
	}

	// Verify non-JSON files are kept
	readmePath := filepath.Join(tmpDir, "readme.txt")
	if _, err := os.Stat(readmePath); os.IsNotExist(err) {
		t.Error("readme.txt should not have been removed")
	}
}

func TestCleanNonExistentDir(t *testing.T) {
	writer := NewWriter("/non/existent/dir")

	// Should not error on non-existent directory
	if err := writer.Clean(""); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestGenerateIndex(t *testing.T) {
	tmpDir := t.TempDir()
	writer := NewWriter(tmpDir)

	dashboards := []*Dashboard{
		{
			UID:           "if-mib",
			Title:         "IF-MIB - Interfaces",
			Tags:          []string{"snmp", "mib", "if-mib"},
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
		{
			UID:           "ip-mib",
			Title:         "IP-MIB - IP Protocol",
			Tags:          []string{"snmp", "mib", "ip-mib"},
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
					ID:    2,
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
	}

	if err := writer.GenerateIndex(dashboards); err != nil {
		t.Fatalf("failed to generate index: %v", err)
	}

	// Verify index file exists
	indexPath := filepath.Join(tmpDir, "index.json")
	if _, err := os.Stat(indexPath); os.IsNotExist(err) {
		t.Fatal("index.json was not created")
	}

	// Load and verify index
	index, err := writer.LoadIndex()
	if err != nil {
		t.Fatalf("failed to load index: %v", err)
	}

	if len(index.Dashboards) != 2 {
		t.Errorf("expected 2 dashboards in index, got %d", len(index.Dashboards))
	}

	// Verify first entry
	if index.Dashboards[0].UID != "if-mib" {
		t.Errorf("expected UID 'if-mib', got %s", index.Dashboards[0].UID)
	}

	if index.Dashboards[0].Panels != 1 {
		t.Errorf("expected 1 panel, got %d", index.Dashboards[0].Panels)
	}

	// Verify second entry
	if index.Dashboards[1].UID != "ip-mib" {
		t.Errorf("expected UID 'ip-mib', got %s", index.Dashboards[1].UID)
	}

	if index.Dashboards[1].Panels != 2 {
		t.Errorf("expected 2 panels, got %d", index.Dashboards[1].Panels)
	}
}

func TestGenerateIndexNoDashboards(t *testing.T) {
	tmpDir := t.TempDir()
	writer := NewWriter(tmpDir)

	err := writer.GenerateIndex([]*Dashboard{})
	if err == nil {
		t.Fatal("expected error for no dashboards")
	}
}

func TestGenerateIndexFromSpecs(t *testing.T) {
	tmpDir := t.TempDir()
	writer := NewWriter(tmpDir)

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

	if err := writer.GenerateIndexFromSpecs(specs); err != nil {
		t.Fatalf("failed to generate index from specs: %v", err)
	}

	// Verify index file exists
	indexPath := filepath.Join(tmpDir, "index.json")
	if _, err := os.Stat(indexPath); os.IsNotExist(err) {
		t.Fatal("index.json was not created")
	}

	// Load and verify index
	index, err := writer.LoadIndex()
	if err != nil {
		t.Fatalf("failed to load index: %v", err)
	}

	if len(index.Dashboards) != 2 {
		t.Errorf("expected 2 dashboards in index, got %d", len(index.Dashboards))
	}
}

func TestListDashboards(t *testing.T) {
	tmpDir := t.TempDir()
	writer := NewWriter(tmpDir)

	// Create some test files
	files := []string{"test1.json", "test2.json", "index.json", "readme.txt"}
	for _, filename := range files {
		path := filepath.Join(tmpDir, filename)
		if err := os.WriteFile(path, []byte("test"), 0644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}
	}

	dashboards, err := writer.ListDashboards()
	if err != nil {
		t.Fatalf("failed to list dashboards: %v", err)
	}

	// Should list JSON files except index.json
	expectedCount := 2
	if len(dashboards) != expectedCount {
		t.Errorf("expected %d dashboards, got %d", expectedCount, len(dashboards))
	}

	// Verify index.json is not in the list
	for _, dashboard := range dashboards {
		if dashboard == "index.json" {
			t.Error("index.json should not be in the list")
		}
	}
}

func TestListDashboardsNonExistentDir(t *testing.T) {
	writer := NewWriter("/non/existent/dir")

	dashboards, err := writer.ListDashboards()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(dashboards) != 0 {
		t.Errorf("expected 0 dashboards, got %d", len(dashboards))
	}
}

func TestLoadDashboard(t *testing.T) {
	tmpDir := t.TempDir()
	writer := NewWriter(tmpDir)

	// Write a dashboard
	dashboard := &Dashboard{
		UID:           "test",
		Title:         "Test Dashboard",
		SchemaVersion: 39,
		Templating:    &Templating{List: []Variable{}},
		Time:          &TimeRange{From: "now-6h", To: "now"},
		Panels:        []Panel{},
	}

	filename := "test.json"
	if err := writer.Write(dashboard, filename); err != nil {
		t.Fatalf("failed to write: %v", err)
	}

	// Load dashboard
	loaded, err := writer.LoadDashboard(filename)
	if err != nil {
		t.Fatalf("failed to load dashboard: %v", err)
	}

	if loaded.UID != dashboard.UID {
		t.Errorf("expected UID %s, got %s", dashboard.UID, loaded.UID)
	}

	if loaded.Title != dashboard.Title {
		t.Errorf("expected title %s, got %s", dashboard.Title, loaded.Title)
	}
}

func TestLoadDashboardNonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	writer := NewWriter(tmpDir)

	_, err := writer.LoadDashboard("non-existent.json")
	if err == nil {
		t.Fatal("expected error for non-existent file")
	}
}

func TestLoadDashboardEmptyFilename(t *testing.T) {
	tmpDir := t.TempDir()
	writer := NewWriter(tmpDir)

	_, err := writer.LoadDashboard("")
	if err == nil {
		t.Fatal("expected error for empty filename")
	}
}

func TestLoadIndex(t *testing.T) {
	tmpDir := t.TempDir()
	writer := NewWriter(tmpDir)

	// Generate index
	dashboards := []*Dashboard{
		{
			UID:           "test",
			Title:         "Test",
			Tags:          []string{"snmp", "mib", "test"},
			SchemaVersion: 39,
			Templating:    &Templating{List: []Variable{}},
			Time:          &TimeRange{From: "now-6h", To: "now"},
			Panels:        []Panel{},
		},
	}

	if err := writer.GenerateIndex(dashboards); err != nil {
		t.Fatalf("failed to generate index: %v", err)
	}

	// Load index
	index, err := writer.LoadIndex()
	if err != nil {
		t.Fatalf("failed to load index: %v", err)
	}

	if len(index.Dashboards) != 1 {
		t.Errorf("expected 1 dashboard in index, got %d", len(index.Dashboards))
	}
}

func TestLoadIndexNonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	writer := NewWriter(tmpDir)

	// Should return empty index if file doesn't exist
	index, err := writer.LoadIndex()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(index.Dashboards) != 0 {
		t.Errorf("expected empty index, got %d dashboards", len(index.Dashboards))
	}
}
