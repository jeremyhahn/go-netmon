package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"

	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/mib/storage"
	"github.com/jeremyhahn/go-netmon/internal/webui/dashboard"
	webuiStorage "github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// mockMIBLoaderForDashboard implements MIBLoader for dashboard tests
type mockMIBLoaderForDashboard struct {
	storage interface{}
}

func (m *mockMIBLoaderForDashboard) LoadModule(moduleName string) error {
	return nil
}

func (m *mockMIBLoaderForDashboard) GetParser() interface{} {
	return nil
}

func (m *mockMIBLoaderForDashboard) GetStorage() interface{} {
	return m.storage
}

func TestHandleGetMIBDashboard_Success(t *testing.T) {
	// Setup temporary directories
	dbPath := t.TempDir()
	mibStoragePath := t.TempDir()

	// Initialize storages
	webuiStore, err := webuiStorage.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create webui storage: %v", err)
	}
	defer webuiStore.Close()

	mibStore, err := storage.New(mibStoragePath)
	if err != nil {
		t.Fatalf("Failed to create MIB storage: %v", err)
	}
	defer mibStore.Close()

	// Create test MIB in webui database
	ctx := context.Background()
	testMIB := &webuiStorage.MIB{
		Name:        "TEST-MIB",
		FilePath:    "/test/TEST-MIB.my",
		Description: "Test MIB for dashboard",
		ObjectCount: 5,
	}
	if err := webuiStore.CreateMIB(ctx, testMIB); err != nil {
		t.Fatalf("Failed to create MIB: %v", err)
	}

	// Create test module in MIB storage
	moduleInfo := &mib.ModuleInfo{
		Name:        "TEST-MIB",
		Path:        "/test/TEST-MIB.my",
		Description: "Test MIB Module",
	}
	if err := mibStore.StoreModule(moduleInfo); err != nil {
		t.Fatalf("Failed to store module: %v", err)
	}

	// Create test OIDs - scalar and table structure
	testOIDs := []*mib.OIDMetadata{
		// Scalar object
		{
			OID:           "1.3.6.1.2.1.1.1.0",
			Name:          "sysDescr",
			Module:        "TEST-MIB",
			Type:          "DisplayString",
			Description:   "System description",
			ObjectClass:   mib.ObjectClassScalar,
			Pollable:      true,
			IsDisplayable: true,
			IsChartable:   false,
		},
		// Table structure
		{
			OID:         "1.3.6.1.2.1.2.2",
			Name:        "ifTable",
			Module:      "TEST-MIB",
			Type:        "SEQUENCE OF",
			Description: "Interface table",
			ObjectClass: mib.ObjectClassTableNode,
			IsTable:     true,
		},
		{
			OID:          "1.3.6.1.2.1.2.2.1",
			Name:         "ifEntry",
			Module:       "TEST-MIB",
			Type:         "SEQUENCE",
			Description:  "Interface entry",
			ObjectClass:  mib.ObjectClassEntryNode,
			IsTableEntry: true,
			Index:        []string{"ifIndex"},
		},
		// Index column
		{
			OID:         "1.3.6.1.2.1.2.2.1.1",
			Name:        "ifIndex",
			Module:      "TEST-MIB",
			Type:        "Integer32",
			Description: "Interface index",
			ObjectClass: mib.ObjectClassIndexColumn,
			TableOID:    "1.3.6.1.2.1.2.2",
			EntryOID:    "1.3.6.1.2.1.2.2.1",
			Pollable:    true,
			IsColumn:    true,
		},
		// Status column
		{
			OID:         "1.3.6.1.2.1.2.2.1.8",
			Name:        "ifOperStatus",
			Module:      "TEST-MIB",
			Type:        "INTEGER",
			Description: "Interface operational status",
			ObjectClass: mib.ObjectClassStatusColumn,
			TableOID:    "1.3.6.1.2.1.2.2",
			EntryOID:    "1.3.6.1.2.1.2.2.1",
			Pollable:    true,
			IsColumn:    true,
			EnumValues: map[int64]string{
				1: "up",
				2: "down",
			},
		},
		// Metric column
		{
			OID:         "1.3.6.1.2.1.2.2.1.10",
			Name:        "ifInOctets",
			Module:      "TEST-MIB",
			Type:        "Counter32",
			Description: "Inbound octets",
			Units:       "octets",
			ObjectClass: mib.ObjectClassMetricColumn,
			TableOID:    "1.3.6.1.2.1.2.2",
			EntryOID:    "1.3.6.1.2.1.2.2.1",
			Pollable:    true,
			IsColumn:    true,
			IsChartable: true,
			ChartType:   "line",
		},
	}

	// Store test OIDs
	for _, oid := range testOIDs {
		if err := mibStore.StoreOID(oid); err != nil {
			t.Fatalf("Failed to store OID %s: %v", oid.Name, err)
		}
	}

	// Setup server with mock loader
	mockLoader := &mockMIBLoaderForDashboard{
		storage: mibStore,
	}

	server := &Server{
		router:    mux.NewRouter(),
		storage:   webuiStore,
		mibLoader: mockLoader,
	}

	// Create request
	req := httptest.NewRequest(http.MethodGet, "/api/mibs/1/dashboard", nil)
	req = mux.SetURLVars(req, map[string]string{"id": "1"})

	w := httptest.NewRecorder()
	server.handleGetMIBDashboard(w, req)

	// Verify response
	if w.Code != http.StatusOK {
		t.Errorf("Status code = %v, want %v; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	// Parse response
	var spec dashboard.Spec
	if err := json.NewDecoder(w.Body).Decode(&spec); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Validate dashboard spec
	if spec.MIBName != "TEST-MIB" {
		t.Errorf("MIBName = %v, want TEST-MIB", spec.MIBName)
	}

	// Check for scalar widgets
	if len(spec.ScalarWidgets) != 1 {
		t.Errorf("ScalarWidgets count = %v, want 1", len(spec.ScalarWidgets))
	}

	if len(spec.ScalarWidgets) > 0 {
		scalar := spec.ScalarWidgets[0]
		if scalar.Name != "sysDescr" {
			t.Errorf("ScalarWidget name = %v, want sysDescr", scalar.Name)
		}
	}

	// Check for table widgets
	if len(spec.TableWidgets) != 1 {
		t.Errorf("TableWidgets count = %v, want 1", len(spec.TableWidgets))
	}

	if len(spec.TableWidgets) > 0 {
		table := spec.TableWidgets[0]
		if table.TableName != "ifTable" {
			t.Errorf("TableWidget name = %v, want ifTable", table.TableName)
		}

		// Check instance selector
		if table.InstanceSelector.IndexColumn != "ifIndex" {
			t.Errorf("InstanceSelector.IndexColumn = %v, want ifIndex", table.InstanceSelector.IndexColumn)
		}

		// Check status fields
		if len(table.StatusFields) != 1 {
			t.Errorf("StatusFields count = %v, want 1", len(table.StatusFields))
		}

		// Check metric charts
		if len(table.MetricCharts) != 1 {
			t.Errorf("MetricCharts count = %v, want 1", len(table.MetricCharts))
		}
	}
}

func TestHandleGetMIBDashboard_InvalidID(t *testing.T) {
	dbPath := t.TempDir()

	webuiStore, err := webuiStorage.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer webuiStore.Close()

	server := &Server{
		router:  mux.NewRouter(),
		storage: webuiStore,
	}

	req := httptest.NewRequest(http.MethodGet, "/api/mibs/invalid/dashboard", nil)
	req = mux.SetURLVars(req, map[string]string{"id": "invalid"})

	w := httptest.NewRecorder()
	server.handleGetMIBDashboard(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Status code = %v, want %v", w.Code, http.StatusBadRequest)
	}
}

func TestHandleGetMIBDashboard_MIBNotFound(t *testing.T) {
	dbPath := t.TempDir()

	webuiStore, err := webuiStorage.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer webuiStore.Close()

	server := &Server{
		router:  mux.NewRouter(),
		storage: webuiStore,
	}

	req := httptest.NewRequest(http.MethodGet, "/api/mibs/999/dashboard", nil)
	req = mux.SetURLVars(req, map[string]string{"id": "999"})

	w := httptest.NewRecorder()
	server.handleGetMIBDashboard(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("Status code = %v, want %v", w.Code, http.StatusNotFound)
	}
}

func TestHandleGetMIBDashboard_LoaderNotAvailable(t *testing.T) {
	dbPath := t.TempDir()

	webuiStore, err := webuiStorage.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer webuiStore.Close()

	// Create test MIB
	ctx := context.Background()
	if err := webuiStore.CreateMIB(ctx, &webuiStorage.MIB{
		Name:        "TEST-MIB",
		FilePath:    "/test/TEST-MIB.my",
		Description: "Test MIB",
		ObjectCount: 1,
	}); err != nil {
		t.Fatalf("Failed to create MIB: %v", err)
	}

	server := &Server{
		router:    mux.NewRouter(),
		storage:   webuiStore,
		mibLoader: nil, // No loader
	}

	req := httptest.NewRequest(http.MethodGet, "/api/mibs/1/dashboard", nil)
	req = mux.SetURLVars(req, map[string]string{"id": "1"})

	w := httptest.NewRecorder()
	server.handleGetMIBDashboard(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("Status code = %v, want %v", w.Code, http.StatusInternalServerError)
	}
}

func TestHandleGetMIBDashboard_StorageNotAvailable(t *testing.T) {
	dbPath := t.TempDir()

	webuiStore, err := webuiStorage.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer webuiStore.Close()

	// Create test MIB
	ctx := context.Background()
	if err := webuiStore.CreateMIB(ctx, &webuiStorage.MIB{
		Name:        "TEST-MIB",
		FilePath:    "/test/TEST-MIB.my",
		Description: "Test MIB",
		ObjectCount: 1,
	}); err != nil {
		t.Fatalf("Failed to create MIB: %v", err)
	}

	mockLoader := &mockMIBLoaderForDashboard{
		storage: nil, // No storage
	}

	server := &Server{
		router:    mux.NewRouter(),
		storage:   webuiStore,
		mibLoader: mockLoader,
	}

	req := httptest.NewRequest(http.MethodGet, "/api/mibs/1/dashboard", nil)
	req = mux.SetURLVars(req, map[string]string{"id": "1"})

	w := httptest.NewRecorder()
	server.handleGetMIBDashboard(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("Status code = %v, want %v", w.Code, http.StatusInternalServerError)
	}
}

func TestHandleGetMIBDashboard_InvalidStorageType(t *testing.T) {
	dbPath := t.TempDir()

	webuiStore, err := webuiStorage.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer webuiStore.Close()

	// Create test MIB
	ctx := context.Background()
	if err := webuiStore.CreateMIB(ctx, &webuiStorage.MIB{
		Name:        "TEST-MIB",
		FilePath:    "/test/TEST-MIB.my",
		Description: "Test MIB",
		ObjectCount: 1,
	}); err != nil {
		t.Fatalf("Failed to create MIB: %v", err)
	}

	mockLoader := &mockMIBLoaderForDashboard{
		storage: "not a storage", // Wrong type
	}

	server := &Server{
		router:    mux.NewRouter(),
		storage:   webuiStore,
		mibLoader: mockLoader,
	}

	req := httptest.NewRequest(http.MethodGet, "/api/mibs/1/dashboard", nil)
	req = mux.SetURLVars(req, map[string]string{"id": "1"})

	w := httptest.NewRecorder()
	server.handleGetMIBDashboard(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("Status code = %v, want %v", w.Code, http.StatusInternalServerError)
	}
}

func TestHandleGetMIBDashboard_JSONFormat(t *testing.T) {
	// Setup temporary directories
	dbPath := t.TempDir()
	mibStoragePath := t.TempDir()

	// Initialize storages
	webuiStore, err := webuiStorage.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create webui storage: %v", err)
	}
	defer webuiStore.Close()

	mibStore, err := storage.New(mibStoragePath)
	if err != nil {
		t.Fatalf("Failed to create MIB storage: %v", err)
	}
	defer mibStore.Close()

	// Create test MIB
	ctx := context.Background()
	if err := webuiStore.CreateMIB(ctx, &webuiStorage.MIB{
		Name:        "JSON-TEST-MIB",
		FilePath:    "/test/JSON-TEST-MIB.my",
		Description: "Test MIB for JSON format",
		ObjectCount: 1,
	}); err != nil {
		t.Fatalf("Failed to create MIB: %v", err)
	}

	// Create test module
	moduleInfo := &mib.ModuleInfo{
		Name:        "JSON-TEST-MIB",
		Path:        "/test/JSON-TEST-MIB.my",
		Description: "Test MIB Module",
	}
	if err := mibStore.StoreModule(moduleInfo); err != nil {
		t.Fatalf("Failed to store module: %v", err)
	}

	// Create simple scalar for testing JSON format
	testOID := &mib.OIDMetadata{
		OID:           "1.3.6.1.2.1.1.1.0",
		Name:          "sysDescr",
		Module:        "JSON-TEST-MIB",
		Type:          "DisplayString",
		Description:   "System description",
		ObjectClass:   mib.ObjectClassScalar,
		Pollable:      true,
		IsDisplayable: true,
	}

	if err := mibStore.StoreOID(testOID); err != nil {
		t.Fatalf("Failed to store OID: %v", err)
	}

	// Setup server
	mockLoader := &mockMIBLoaderForDashboard{
		storage: mibStore,
	}

	server := &Server{
		router:    mux.NewRouter(),
		storage:   webuiStore,
		mibLoader: mockLoader,
	}

	// Create request
	req := httptest.NewRequest(http.MethodGet, "/api/mibs/1/dashboard", nil)
	req = mux.SetURLVars(req, map[string]string{"id": "1"})

	w := httptest.NewRecorder()
	server.handleGetMIBDashboard(w, req)

	// Verify response
	if w.Code != http.StatusOK {
		t.Errorf("Status code = %v, want %v", w.Code, http.StatusOK)
	}

	// Verify content type
	contentType := w.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("Content-Type = %v, want application/json", contentType)
	}

	// Verify JSON can be decoded
	var spec dashboard.Spec
	if err := json.NewDecoder(w.Body).Decode(&spec); err != nil {
		t.Fatalf("Failed to decode JSON response: %v", err)
	}

	// Verify basic structure
	if spec.MIBName != "JSON-TEST-MIB" {
		t.Errorf("MIBName = %v, want JSON-TEST-MIB", spec.MIBName)
	}
}
