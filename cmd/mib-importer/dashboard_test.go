package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/mib/storage"
)

func TestDashboardCommand(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "no subcommand",
			args:    []string{},
			wantErr: true,
		},
		{
			name:    "help",
			args:    []string{"help"},
			wantErr: false,
		},
		{
			name:    "unknown subcommand",
			args:    []string{"unknown"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := dashboardCommand(tt.args)
			if (err != nil) != tt.wantErr {
				t.Errorf("dashboardCommand() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestDashboardGenerate(t *testing.T) {
	// Create temporary storage
	tmpDir := t.TempDir()
	storagePath := filepath.Join(tmpDir, "storage")
	outputDir := filepath.Join(tmpDir, "dashboards")

	// Setup test storage with sample data
	store, err := storage.New(storagePath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}

	// Add test module
	module := &mib.ModuleInfo{
		Name:        "TEST-MIB",
		Path:        "/test/TEST-MIB",
		Description: "Test MIB for dashboard generation",
		OIDCount:    3,
		LoadedAt:    time.Now(),
	}
	if err := store.StoreModule(module); err != nil {
		t.Fatalf("failed to store module: %v", err)
	}

	// Add pollable scalar OID
	oid1 := &mib.OIDMetadata{
		OID:         ".1.3.6.1.2.1.1.1.0",
		Name:        "sysDescr",
		Module:      "TEST-MIB",
		Type:        "OctetString",
		Access:      "read-only",
		Status:      "current",
		Description: "System description",
		IsScalar:    true,
		Pollable:    true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := store.StoreOID(oid1); err != nil {
		t.Fatalf("failed to store OID: %v", err)
	}

	// Add counter OID
	oid2 := &mib.OIDMetadata{
		OID:         ".1.3.6.1.2.1.2.1.0",
		Name:        "ifNumber",
		Module:      "TEST-MIB",
		Type:        "Counter32",
		Access:      "read-only",
		Status:      "current",
		Description: "Number of interfaces",
		IsScalar:    true,
		Pollable:    true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := store.StoreOID(oid2); err != nil {
		t.Fatalf("failed to store OID: %v", err)
	}

	// Add gauge OID
	oid3 := &mib.OIDMetadata{
		OID:         ".1.3.6.1.2.1.25.3.3.1.2",
		Name:        "hrProcessorLoad",
		Module:      "TEST-MIB",
		Type:        "Integer32",
		Access:      "read-only",
		Status:      "current",
		Description: "Processor load percentage",
		IsScalar:    true,
		Pollable:    true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := store.StoreOID(oid3); err != nil {
		t.Fatalf("failed to store OID: %v", err)
	}

	store.Close()

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name: "generate with all flags",
			args: []string{
				"--storage-path", storagePath,
				"--output-dir", outputDir,
				"--overwrite",
				"TEST-MIB",
			},
			wantErr: false,
		},
		{
			name: "generate without module name",
			args: []string{
				"--storage-path", storagePath,
				"--output-dir", outputDir,
			},
			wantErr: true,
		},
		{
			name: "generate with invalid storage",
			args: []string{
				"--storage-path", "/nonexistent/path",
				"--output-dir", outputDir,
				"TEST-MIB",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := dashboardGenerate(tt.args)
			if (err != nil) != tt.wantErr {
				t.Errorf("dashboardGenerate() error = %v, wantErr %v", err, tt.wantErr)
			}

			// Check if dashboard was generated (for successful cases)
			if !tt.wantErr {
				dashboardFile := filepath.Join(outputDir, "TEST-MIB.json")
				if _, err := os.Stat(dashboardFile); os.IsNotExist(err) {
					t.Errorf("expected dashboard file to exist: %s", dashboardFile)
				}
			}
		})
	}
}

func TestDashboardGenerateAll(t *testing.T) {
	// Create temporary storage
	tmpDir := t.TempDir()
	storagePath := filepath.Join(tmpDir, "storage")
	outputDir := filepath.Join(tmpDir, "dashboards")

	// Setup test storage with sample data
	store, err := storage.New(storagePath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}

	// Add two test modules
	modules := []string{"TEST-MIB-1", "TEST-MIB-2"}
	for _, moduleName := range modules {
		module := &mib.ModuleInfo{
			Name:        moduleName,
			Path:        "/test/" + moduleName,
			Description: "Test MIB " + moduleName,
			OIDCount:    1,
			LoadedAt:    time.Now(),
		}
		if err := store.StoreModule(module); err != nil {
			t.Fatalf("failed to store module: %v", err)
		}

		// Add pollable OID for each module
		oid := &mib.OIDMetadata{
			OID:         ".1.3.6.1.2.1.1.1." + moduleName,
			Name:        "test" + moduleName,
			Module:      moduleName,
			Type:        "Integer32",
			Access:      "read-only",
			Status:      "current",
			Description: "Test OID",
			IsScalar:    true,
			Pollable:    true,
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}
		if err := store.StoreOID(oid); err != nil {
			t.Fatalf("failed to store OID: %v", err)
		}
	}

	store.Close()

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name: "generate all with index",
			args: []string{
				"--storage-path", storagePath,
				"--output-dir", outputDir,
				"--index",
				"--overwrite",
			},
			wantErr: false,
		},
		{
			name: "generate all with invalid storage",
			args: []string{
				"--storage-path", "/nonexistent/path",
				"--output-dir", outputDir,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := dashboardGenerateAll(tt.args)
			if (err != nil) != tt.wantErr {
				t.Errorf("dashboardGenerateAll() error = %v, wantErr %v", err, tt.wantErr)
			}

			// Check if dashboards were generated (for successful cases)
			if !tt.wantErr {
				for _, moduleName := range modules {
					dashboardFile := filepath.Join(outputDir, moduleName+".json")
					if _, err := os.Stat(dashboardFile); os.IsNotExist(err) {
						t.Errorf("expected dashboard file to exist: %s", dashboardFile)
					}
				}

				// Check if index was generated
				indexFile := filepath.Join(outputDir, "index.json")
				if _, err := os.Stat(indexFile); os.IsNotExist(err) {
					t.Errorf("expected index file to exist: %s", indexFile)
				}
			}
		})
	}
}

func TestDashboardList(t *testing.T) {
	// Create temporary storage
	tmpDir := t.TempDir()
	storagePath := filepath.Join(tmpDir, "storage")

	// Setup test storage with sample data
	store, err := storage.New(storagePath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}

	// Add test module
	module := &mib.ModuleInfo{
		Name:        "TEST-MIB",
		Path:        "/test/TEST-MIB",
		Description: "Test MIB",
		OIDCount:    1,
		LoadedAt:    time.Now(),
	}
	if err := store.StoreModule(module); err != nil {
		t.Fatalf("failed to store module: %v", err)
	}

	// Add pollable OID
	oid := &mib.OIDMetadata{
		OID:         ".1.3.6.1.2.1.1.1.0",
		Name:        "sysDescr",
		Module:      "TEST-MIB",
		Type:        "OctetString",
		Access:      "read-only",
		Status:      "current",
		Description: "System description",
		IsScalar:    true,
		Pollable:    true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := store.StoreOID(oid); err != nil {
		t.Fatalf("failed to store OID: %v", err)
	}

	store.Close()

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name: "list basic",
			args: []string{
				"--storage-path", storagePath,
			},
			wantErr: false,
		},
		{
			name: "list with pollable",
			args: []string{
				"--storage-path", storagePath,
				"--pollable",
			},
			wantErr: false,
		},
		{
			name: "list verbose",
			args: []string{
				"--storage-path", storagePath,
				"--verbose",
				"--pollable",
			},
			wantErr: false,
		},
		{
			// A regular file used as a parent directory: the kernel refuses it
			// with ENOTDIR whoever asks. "/nonexistent/path" relied on the
			// process lacking permission to create it, which is true for a
			// developer and false for the root user CI runs as - where the
			// store was created, the case failed, and the run wrote to the
			// host filesystem.
			name: "list with a storage path that cannot be opened",
			args: []string{
				"--storage-path", filepath.Join(notADirectory(t), "db"),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := dashboardList(tt.args)
			if (err != nil) != tt.wantErr {
				t.Errorf("dashboardList() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestDashboardClean(t *testing.T) {
	// Create temporary directory with test dashboards
	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "dashboards")

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		t.Fatalf("failed to create output directory: %v", err)
	}

	// Create test dashboard files
	dashboardFiles := []string{"test1.json", "test2.json"}
	for _, file := range dashboardFiles {
		path := filepath.Join(outputDir, file)
		if err := os.WriteFile(path, []byte("{}"), 0644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}
	}

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name: "clean with force",
			args: []string{
				"--output-dir", outputDir,
				"--force",
			},
			wantErr: false,
		},
		{
			name: "clean nonexistent directory",
			args: []string{
				"--output-dir", "/nonexistent/path",
				"--force",
			},
			wantErr: false, // Should not error for nonexistent directory
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := dashboardClean(tt.args)
			if (err != nil) != tt.wantErr {
				t.Errorf("dashboardClean() error = %v, wantErr %v", err, tt.wantErr)
			}

			// Check if files were removed (for successful cases)
			if !tt.wantErr && tt.args[1] == outputDir {
				for _, file := range dashboardFiles {
					path := filepath.Join(outputDir, file)
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Errorf("expected file to be removed: %s", path)
					}
				}
			}
		})
	}
}

func TestPrintDashboardUsage(t *testing.T) {
	// Should not panic
	printDashboardUsage()
}

// notADirectory returns the path of a regular file, for use where a test needs
// a location no process can open a directory under.
func notADirectory(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("seed the blocking file: %v", err)
	}
	return path
}
