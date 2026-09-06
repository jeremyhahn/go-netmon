package api

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

// mockMIBLoader is a mock implementation of MIBLoader for testing
type mockMIBLoader struct {
	getParserFunc func() interface{}
}

func (m *mockMIBLoader) LoadModule(moduleName string) error {
	return nil
}

// EnsureModule delegates to LoadModule: this stub has no store to consult,
// so "already loaded and current" is not a state it can be in.
func (m *mockMIBLoader) EnsureModuleCurrent(moduleName string) error { return m.LoadModule(moduleName) }

func (m *mockMIBLoader) GetParser() interface{} {
	if m.getParserFunc != nil {
		return m.getParserFunc()
	}
	return nil
}

func (m *mockMIBLoader) GetStorage() interface{} {
	return nil
}

// Helper function to create a multipart form file upload request
func createMIBUploadRequest(t *testing.T, fileName string, content []byte) *http.Request {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		t.Fatalf("Failed to create form file: %v", err)
	}

	if _, err := part.Write(content); err != nil {
		t.Fatalf("Failed to write file content: %v", err)
	}

	if err := writer.Close(); err != nil {
		t.Fatalf("Failed to close writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/mibs/import", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	return req
}

func TestHandleImportMIB_FileTooLarge(t *testing.T) {
	tmpDir := t.TempDir()

	server := &Server{
		mibStoragePath: tmpDir,
	}

	// Create a file larger than maxUploadSize (10 MB)
	largeContent := make([]byte, maxUploadSize+1)
	req := createMIBUploadRequest(t, "LARGE-MIB.my", largeContent)

	w := httptest.NewRecorder()
	server.handleImportMIB(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Status code = %v, want %v for file too large", w.Code, http.StatusBadRequest)
	}
}

func TestHandleImportMIB_NoFile(t *testing.T) {
	tmpDir := t.TempDir()

	server := &Server{
		mibStoragePath: tmpDir,
	}

	// Create request without file
	req := httptest.NewRequest(http.MethodPost, "/api/mibs/import", nil)
	req.Header.Set("Content-Type", "multipart/form-data")

	w := httptest.NewRecorder()
	server.handleImportMIB(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Status code = %v, want %v for missing file", w.Code, http.StatusBadRequest)
	}
}

func TestHandleImportMIB_LoaderNotAvailable(t *testing.T) {
	tmpDir := t.TempDir()

	server := &Server{
		mibLoader:      nil, // No loader configured
		mibStoragePath: tmpDir,
	}

	mibContent := []byte("TEST-MIB DEFINITIONS ::= BEGIN\nEND")
	req := createMIBUploadRequest(t, "TEST-MIB.my", mibContent)

	w := httptest.NewRecorder()
	server.handleImportMIB(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("Status code = %v, want %v for nil loader", w.Code, http.StatusInternalServerError)
	}

	if !bytes.Contains(w.Body.Bytes(), []byte("MIB loader not available")) {
		t.Errorf("Expected error message about loader not available, got: %s", w.Body.String())
	}
}

func TestHandleImportMIB_ParserNotAvailable(t *testing.T) {
	tmpDir := t.TempDir()

	mockLoader := &mockMIBLoader{
		getParserFunc: func() interface{} {
			return nil // No parser available
		},
	}

	server := &Server{
		mibLoader:      mockLoader,
		mibStoragePath: tmpDir,
	}

	mibContent := []byte("TEST-MIB DEFINITIONS ::= BEGIN\nEND")
	req := createMIBUploadRequest(t, "TEST-MIB.my", mibContent)

	w := httptest.NewRecorder()
	server.handleImportMIB(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("Status code = %v, want %v for nil parser", w.Code, http.StatusInternalServerError)
	}

	if !bytes.Contains(w.Body.Bytes(), []byte("MIB parser not available")) {
		t.Errorf("Expected error message about parser not available, got: %s", w.Body.String())
	}
}

func TestHandleImportMIB_ParserTypeAssertionFailed(t *testing.T) {
	tmpDir := t.TempDir()

	mockLoader := &mockMIBLoader{
		getParserFunc: func() interface{} {
			return "not a parser" // Wrong type
		},
	}

	server := &Server{
		mibLoader:      mockLoader,
		mibStoragePath: tmpDir,
	}

	mibContent := []byte("TEST-MIB DEFINITIONS ::= BEGIN\nEND")
	req := createMIBUploadRequest(t, "TEST-MIB.my", mibContent)

	w := httptest.NewRecorder()
	server.handleImportMIB(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("Status code = %v, want %v for type assertion failure", w.Code, http.StatusInternalServerError)
	}

	if !bytes.Contains(w.Body.Bytes(), []byte("MIB parser type assertion failed")) {
		t.Errorf("Expected error message about type assertion, got: %s", w.Body.String())
	}
}
