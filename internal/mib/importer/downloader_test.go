package importer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDownloaderDownloadFile(t *testing.T) {
	// Create a test HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/CISCO-SYSTEM-MIB.my":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("-- CISCO-SYSTEM-MIB content\n"))
		case "/NOT-FOUND.my":
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte("404 not found"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	tests := []struct {
		name        string
		url         string
		wantErr     bool
		errContains string
	}{
		{
			name:    "successful download",
			url:     server.URL + "/CISCO-SYSTEM-MIB.my",
			wantErr: false,
		},
		{
			name:        "not found",
			url:         server.URL + "/NOT-FOUND.my",
			wantErr:     true,
			errContains: "404",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp directory
			tmpDir := t.TempDir()
			outputPath := filepath.Join(tmpDir, "test.my")

			d := NewDownloader(server.URL, 5*time.Second)
			ctx := context.Background()

			err := d.downloadFile(ctx, tt.url, outputPath)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got none")
					return
				}
				if tt.errContains != "" && !contains(err.Error(), tt.errContains) {
					t.Errorf("error = %v, want error containing %q", err, tt.errContains)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Verify file was created
			if _, statErr := os.Stat(outputPath); os.IsNotExist(statErr) {
				t.Error("file was not created")
			}

			// Verify content
			content, err := os.ReadFile(outputPath)
			if err != nil {
				t.Fatalf("failed to read file: %v", err)
			}

			if len(content) == 0 {
				t.Error("file is empty")
			}
		})
	}
}

func TestDownloaderDownloadMIB(t *testing.T) {
	// Create a test HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/CISCO-SYSTEM-MIB.my":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("-- CISCO-SYSTEM-MIB content\n"))
		case "/IF-MIB.my":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("-- IF-MIB content\n"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	tests := []struct {
		name        string
		mibName     string
		wantErr     bool
		errContains string
	}{
		{
			name:    "download existing MIB",
			mibName: "CISCO-SYSTEM-MIB",
			wantErr: false,
		},
		{
			name:    "download IF-MIB",
			mibName: "IF-MIB",
			wantErr: false,
		},
		{
			name:        "download non-existent MIB",
			mibName:     "NONEXISTENT-MIB",
			wantErr:     true,
			errContains: "404",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			outputPath := filepath.Join(tmpDir, tt.mibName+".my")

			d := NewDownloader(server.URL, 5*time.Second)
			ctx := context.Background()

			err := d.downloadMIB(ctx, tt.mibName, outputPath)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got none")
					return
				}
				if tt.errContains != "" && !contains(err.Error(), tt.errContains) {
					t.Errorf("error = %v, want error containing %q", err, tt.errContains)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Verify file exists
			if _, err := os.Stat(outputPath); os.IsNotExist(err) {
				t.Error("MIB file was not created")
			}
		})
	}
}

func TestDownloaderDownloadAll(t *testing.T) {
	// Create a test HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/CISCO-SYSTEM-MIB.my":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("-- CISCO-SYSTEM-MIB content\n"))
		case "/IF-MIB.my":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("-- IF-MIB content\n"))
		case "/TCP-MIB.my":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("-- TCP-MIB content\n"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	tests := []struct {
		name             string
		mibs             []string
		wantDownloaded   int
		wantNotFound     int
		wantAlreadyExist int
	}{
		{
			name:           "download multiple MIBs",
			mibs:           []string{"CISCO-SYSTEM-MIB", "IF-MIB", "TCP-MIB"},
			wantDownloaded: 3,
			wantNotFound:   0,
		},
		{
			name:           "download with some not found",
			mibs:           []string{"CISCO-SYSTEM-MIB", "NONEXISTENT-MIB", "IF-MIB"},
			wantDownloaded: 2,
			wantNotFound:   1,
		},
		{
			name:           "empty MIB list",
			mibs:           []string{},
			wantDownloaded: 0,
			wantNotFound:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()

			d := NewDownloader(server.URL, 5*time.Second)
			ctx := context.Background()

			opts := &DownloadOptions{
				OutputDir:       tmpDir,
				BaseURL:         server.URL,
				Overwrite:       false,
				MaxConcurrency:  3,
				Timeout:         5 * time.Second,
				ProgressUpdates: false,
			}

			stats, err := d.DownloadAll(ctx, tt.mibs, opts)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if stats.Downloaded != tt.wantDownloaded {
				t.Errorf("Downloaded = %d, want %d", stats.Downloaded, tt.wantDownloaded)
			}

			if stats.NotFound != tt.wantNotFound {
				t.Errorf("NotFound = %d, want %d", stats.NotFound, tt.wantNotFound)
			}

			if stats.TotalMIBs != len(tt.mibs) {
				t.Errorf("TotalMIBs = %d, want %d", stats.TotalMIBs, len(tt.mibs))
			}

			// Verify success rate
			if len(tt.mibs) > 0 {
				successRate := stats.SuccessRate()
				expectedRate := float64(tt.wantDownloaded+tt.wantAlreadyExist) / float64(len(tt.mibs)) * 100
				if successRate != expectedRate {
					t.Errorf("SuccessRate = %.2f%%, want %.2f%%", successRate, expectedRate)
				}
			}
		})
	}
}

func TestDownloaderDownloadAll_AlreadyExists(t *testing.T) {
	// Create a test HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("-- MIB content\n"))
	}))
	defer server.Close()

	tmpDir := t.TempDir()

	// Pre-create a MIB file
	existingMIB := filepath.Join(tmpDir, "CISCO-SYSTEM-MIB.my")
	if err := os.WriteFile(existingMIB, []byte("existing"), 0644); err != nil {
		t.Fatalf("failed to create existing file: %v", err)
	}

	d := NewDownloader(server.URL, 5*time.Second)
	ctx := context.Background()

	opts := &DownloadOptions{
		OutputDir:       tmpDir,
		BaseURL:         server.URL,
		Overwrite:       false,
		MaxConcurrency:  3,
		Timeout:         5 * time.Second,
		ProgressUpdates: false,
	}

	stats, err := d.DownloadAll(ctx, []string{"CISCO-SYSTEM-MIB", "IF-MIB"}, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if stats.AlreadyExists != 1 {
		t.Errorf("AlreadyExists = %d, want 1", stats.AlreadyExists)
	}

	if stats.Downloaded != 1 {
		t.Errorf("Downloaded = %d, want 1", stats.Downloaded)
	}

	// Verify existing file wasn't overwritten
	content, err := os.ReadFile(existingMIB)
	if err != nil {
		t.Fatalf("failed to read existing file: %v", err)
	}

	if string(content) != "existing" {
		t.Error("existing file was overwritten")
	}
}

func TestDownloaderDownloadAll_Overwrite(t *testing.T) {
	// Create a test HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("new content"))
	}))
	defer server.Close()

	tmpDir := t.TempDir()

	// Pre-create a MIB file
	existingMIB := filepath.Join(tmpDir, "CISCO-SYSTEM-MIB.my")
	if err := os.WriteFile(existingMIB, []byte("old content"), 0644); err != nil {
		t.Fatalf("failed to create existing file: %v", err)
	}

	d := NewDownloader(server.URL, 5*time.Second)
	ctx := context.Background()

	opts := &DownloadOptions{
		OutputDir:       tmpDir,
		BaseURL:         server.URL,
		Overwrite:       true,
		MaxConcurrency:  3,
		Timeout:         5 * time.Second,
		ProgressUpdates: false,
	}

	stats, err := d.DownloadAll(ctx, []string{"CISCO-SYSTEM-MIB"}, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if stats.Downloaded != 1 {
		t.Errorf("Downloaded = %d, want 1", stats.Downloaded)
	}

	// Verify file was overwritten
	content, err := os.ReadFile(existingMIB)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	if string(content) != "new content" {
		t.Error("file was not overwritten")
	}
}

func TestStatisticsString(t *testing.T) {
	stats := &Statistics{
		TotalMIBs:     100,
		Downloaded:    80,
		AlreadyExists: 10,
		NotFound:      5,
		Failed:        5,
		Duration:      2 * time.Second,
		FailedMIBs:    []string{"MIB1: error1"},
		NotFoundMIBs:  []string{"MIB2", "MIB3"},
	}

	str := stats.String()
	if str == "" {
		t.Error("String() returned empty string")
	}

	// Verify it contains key information
	if !contains(str, "100") || !contains(str, "80") {
		t.Errorf("String() missing expected values: %s", str)
	}
}

func TestStatisticsSuccessRate(t *testing.T) {
	tests := []struct {
		name  string
		stats Statistics
		want  float64
	}{
		{
			name: "100% success",
			stats: Statistics{
				TotalMIBs:  100,
				Downloaded: 100,
			},
			want: 100.0,
		},
		{
			name: "50% success",
			stats: Statistics{
				TotalMIBs:  100,
				Downloaded: 50,
			},
			want: 50.0,
		},
		{
			name: "with already exists",
			stats: Statistics{
				TotalMIBs:     100,
				Downloaded:    50,
				AlreadyExists: 30,
			},
			want: 80.0,
		},
		{
			name: "zero total",
			stats: Statistics{
				TotalMIBs: 0,
			},
			want: 0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.stats.SuccessRate()
			if got != tt.want {
				t.Errorf("SuccessRate() = %.2f, want %.2f", got, tt.want)
			}
		})
	}
}

func TestDownloadMIB(t *testing.T) {
	// Create a test HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/CISCO-SYSTEM-MIB.my" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("-- MIB content\n"))
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	ctx := context.Background()

	err := DownloadMIB(ctx, "CISCO-SYSTEM-MIB", server.URL, tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify file exists
	outputPath := filepath.Join(tmpDir, "CISCO-SYSTEM-MIB.my")
	if _, err := os.Stat(outputPath); os.IsNotExist(err) {
		t.Error("MIB file was not created")
	}
}

// Helper function to check if a string contains a substring
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && indexOf(s, substr) >= 0))
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
