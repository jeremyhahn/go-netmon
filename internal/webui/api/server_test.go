package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// mockMIBLoaderWithTracking tracks LoadModule calls
type mockMIBLoaderWithTracking struct {
	loadedModules []string
	loadErrors    map[string]error
}

func (m *mockMIBLoaderWithTracking) LoadModule(moduleName string) error {
	m.loadedModules = append(m.loadedModules, moduleName)
	if err, exists := m.loadErrors[moduleName]; exists {
		return err
	}
	return nil
}

func (m *mockMIBLoaderWithTracking) GetParser() interface{} {
	return nil
}

func (m *mockMIBLoaderWithTracking) GetStorage() interface{} {
	return nil
}

func TestLoadStoredMIBs(t *testing.T) {
	tests := []struct {
		name           string
		setupMIBs      []storage.MIB
		expectedLoaded int
		expectError    bool
	}{
		{
			name:           "no MIBs in database",
			setupMIBs:      []storage.MIB{},
			expectedLoaded: 0,
			expectError:    false,
		},
		{
			name: "single MIB loaded successfully",
			setupMIBs: []storage.MIB{
				{Name: "IP-MIB", FilePath: "/app/data/mibs/uploaded/IP-MIB.my"},
			},
			expectedLoaded: 1,
			expectError:    false,
		},
		{
			name: "multiple MIBs loaded successfully",
			setupMIBs: []storage.MIB{
				{Name: "IP-MIB", FilePath: "/app/data/mibs/uploaded/IP-MIB.my"},
				{Name: "TCP-MIB", FilePath: "/app/data/mibs/uploaded/TCP-MIB.my"},
				{Name: "UDP-MIB", FilePath: "/app/data/mibs/uploaded/UDP-MIB.my"},
			},
			expectedLoaded: 3,
			expectError:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temporary database
			tmpDir := t.TempDir()
			dbPath := filepath.Join(tmpDir, "test.db")

			// Initialize storage
			store, err := storage.New(dbPath)
			if err != nil {
				t.Fatalf("Failed to create storage: %v", err)
			}
			defer store.Close()

			// Setup MIBs in database
			ctx := context.Background()
			for i := range tt.setupMIBs {
				if createErr := store.CreateMIB(ctx, &tt.setupMIBs[i]); createErr != nil {
					t.Fatalf("Failed to create MIB in storage: %v", createErr)
				}
			}

			// Create mock MIB loader
			mockLoader := &mockMIBLoaderWithTracking{
				loadedModules: []string{},
				loadErrors:    make(map[string]error),
			}

			// Create server with mock loader
			server := &Server{
				storage:   store,
				mibLoader: mockLoader,
			}

			// Test LoadStoredMIBs
			err = server.LoadStoredMIBs(ctx)

			// Verify results
			if tt.expectError && err == nil {
				t.Error("Expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}

			if len(mockLoader.loadedModules) != tt.expectedLoaded {
				t.Errorf("Expected %d modules loaded, got %d. Loaded: %v",
					tt.expectedLoaded, len(mockLoader.loadedModules), mockLoader.loadedModules)
			}

			// Verify the correct MIBs were loaded
			for _, mib := range tt.setupMIBs {
				found := false
				for _, loaded := range mockLoader.loadedModules {
					if loaded == mib.Name {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Expected MIB %s to be loaded, but it wasn't", mib.Name)
				}
			}
		})
	}
}

func TestLoadStoredMIBsWithErrors(t *testing.T) {
	// Create temporary database
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	// Initialize storage
	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer store.Close()

	// Setup MIBs in database
	ctx := context.Background()
	mibs := []storage.MIB{
		{Name: "IP-MIB", FilePath: "/app/data/mibs/uploaded/IP-MIB.my"},
		{Name: "BROKEN-MIB", FilePath: "/app/data/mibs/uploaded/BROKEN-MIB.my"},
		{Name: "TCP-MIB", FilePath: "/app/data/mibs/uploaded/TCP-MIB.my"},
	}

	for i := range mibs {
		if createErr := store.CreateMIB(ctx, &mibs[i]); createErr != nil {
			t.Fatalf("Failed to create MIB in storage: %v", createErr)
		}
	}

	// Create mock MIB loader that fails on BROKEN-MIB
	mockLoader := &mockMIBLoaderWithTracking{
		loadedModules: []string{},
		loadErrors: map[string]error{
			"BROKEN-MIB": os.ErrNotExist,
		},
	}

	// Create server with mock loader
	server := &Server{
		storage:   store,
		mibLoader: mockLoader,
	}

	// Test LoadStoredMIBs - should not fail even if one MIB fails
	err = server.LoadStoredMIBs(ctx)
	if err != nil {
		t.Errorf("LoadStoredMIBs should not fail even if individual MIBs fail to load: %v", err)
	}

	// Should have attempted to load all 3 MIBs
	if len(mockLoader.loadedModules) != 3 {
		t.Errorf("Expected 3 load attempts, got %d", len(mockLoader.loadedModules))
	}

	// Verify IP-MIB and TCP-MIB are in the loaded list (order may vary)
	expectedLoaded := map[string]bool{
		"IP-MIB":     false,
		"BROKEN-MIB": false,
		"TCP-MIB":    false,
	}

	for _, loaded := range mockLoader.loadedModules {
		if _, exists := expectedLoaded[loaded]; exists {
			expectedLoaded[loaded] = true
		}
	}

	for mib, wasLoaded := range expectedLoaded {
		if !wasLoaded {
			t.Errorf("Expected MIB %s to be attempted for loading, but it wasn't", mib)
		}
	}
}

func TestLoadStoredMIBsNilLoader(t *testing.T) {
	// Create temporary database
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	// Initialize storage
	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer store.Close()

	// Setup MIBs in database
	ctx := context.Background()
	mib := storage.MIB{Name: "IP-MIB", FilePath: "/app/data/mibs/uploaded/IP-MIB.my"}
	if createErr := store.CreateMIB(ctx, &mib); createErr != nil {
		t.Fatalf("Failed to create MIB in storage: %v", createErr)
	}

	// Create server with nil loader
	server := &Server{
		storage:   store,
		mibLoader: nil,
	}

	// Test LoadStoredMIBs - should not crash with nil loader
	err = server.LoadStoredMIBs(ctx)
	if err != nil {
		t.Errorf("LoadStoredMIBs should handle nil loader gracefully: %v", err)
	}
}
