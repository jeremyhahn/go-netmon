package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestClient starts a fake API server with the given handler and returns a
// client pointed at it.
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c, err := New(srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestNew(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"full url", "http://198.51.100.91:8081", "http://198.51.100.91:8081"},
		{"trailing slash trimmed", "http://localhost:8081/", "http://localhost:8081"},
		{"bare host gets scheme", "198.51.100.91:8081", "http://198.51.100.91:8081"},
		{"https preserved", "https://netmon.example.com", "https://netmon.example.com"},
		{"whitespace trimmed", "  http://localhost:8081  ", "http://localhost:8081"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := New(tt.input)
			if err != nil {
				t.Fatalf("New(%q): %v", tt.input, err)
			}
			if c.BaseURL() != tt.want {
				t.Errorf("BaseURL = %q, want %q", c.BaseURL(), tt.want)
			}
		})
	}
}

func TestNew_Invalid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		{"scheme with no host", "http://"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.input)
			if err == nil {
				t.Fatalf("New(%q) succeeded, want an error", tt.input)
			}
			var cfgErr *ConfigError
			if !errors.As(err, &cfgErr) {
				t.Errorf("error is %T, want *ConfigError", err)
			}
		})
	}
}

func TestListDevices(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/devices" {
			t.Errorf("path = %q, want /api/devices", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"devices": []Device{
				{ID: 1, Name: "core-01", Address: "198.51.100.252", SNMPVersion: "2c", Port: 161},
				{ID: 2, Name: "core-02", Address: "198.51.100.253", SNMPVersion: "2c", Port: 161},
			},
		})
	})

	devices, err := c.ListDevices(context.Background())
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if len(devices) != 2 {
		t.Fatalf("got %d devices, want 2", len(devices))
	}
	if devices[0].Name != "core-01" {
		t.Errorf("devices[0].Name = %q, want core-01", devices[0].Name)
	}
}

func TestListDevices_ServerError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "database unavailable", http.StatusInternalServerError)
	})

	_, err := c.ListDevices(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error is %T, want *APIError", err)
	}
	if apiErr.StatusCode != http.StatusInternalServerError {
		t.Errorf("StatusCode = %d, want 500", apiErr.StatusCode)
	}
	if !strings.Contains(apiErr.Error(), "database unavailable") {
		t.Errorf("error %q should include the server's message", apiErr.Error())
	}
}

func TestGetDevice_NotFound(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "device not found", http.StatusNotFound)
	})

	_, err := c.GetDevice(context.Background(), 99)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error is %T, want *APIError", err)
	}
	if !apiErr.NotFound() {
		t.Error("NotFound() = false, want true for a 404")
	}
}

func TestCreateDevice(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		var sent Device
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if sent.Name != "core-01" || sent.Community != "public" {
			t.Errorf("server received %+v, want name core-01 with community public", sent)
		}
		sent.ID = 7
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(sent)
	})

	created, err := c.CreateDevice(context.Background(), Device{
		Name: "core-01", Address: "198.51.100.252",
		SNMPVersion: "2c", Community: "public", Port: 161,
	})
	if err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}
	if created.ID != 7 {
		t.Errorf("ID = %d, want 7", created.ID)
	}
}

func TestDeleteDevice_NoContent(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	if err := c.DeleteDevice(context.Background(), 1); err != nil {
		t.Fatalf("DeleteDevice: %v", err)
	}
}

func TestTestSNMP(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(SNMPTestResponse{
			Success: true, Duration: "12ms",
			SysName: "core-01", SysDescr: "Cisco IOS Software",
		})
	})

	resp, err := c.TestSNMP(context.Background(), SNMPTestRequest{
		Address: "198.51.100.252", SNMPVersion: "2c", Community: "public",
	})
	if err != nil {
		t.Fatalf("TestSNMP: %v", err)
	}
	if !resp.Success || resp.SysName != "core-01" {
		t.Errorf("got %+v, want a successful probe of core-01", resp)
	}
}

func TestListMIBs_AvailableFilter(t *testing.T) {
	var gotQuery string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		json.NewEncoder(w).Encode(map[string]any{"mibs": []MIB{{ID: 1, Name: "IF-MIB"}}})
	})

	if _, err := c.ListMIBs(context.Background(), true); err != nil {
		t.Fatalf("ListMIBs: %v", err)
	}
	if gotQuery != "filter=available" {
		t.Errorf("query = %q, want filter=available", gotQuery)
	}

	if _, err := c.ListMIBs(context.Background(), false); err != nil {
		t.Fatalf("ListMIBs: %v", err)
	}
	if gotQuery != "" {
		t.Errorf("query = %q, want empty when the filter is off", gotQuery)
	}
}

func TestImportMIB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "TEST-MIB.txt")
	if err := os.WriteFile(path, []byte("TEST-MIB DEFINITIONS ::= BEGIN\nEND\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("form file: %v", err)
		}
		defer file.Close()
		if header.Filename != "TEST-MIB.txt" {
			t.Errorf("filename = %q, want TEST-MIB.txt", header.Filename)
		}
		if tags := r.FormValue("tags"); tags != "cisco,asr1000" {
			t.Errorf("tags = %q, want cisco,asr1000", tags)
		}
		// The server wraps the record in an envelope.
		json.NewEncoder(w).Encode(map[string]any{
			"mib":     MIB{ID: 3, Name: "TEST-MIB", ObjectCount: 0},
			"message": "Successfully imported MIB TEST-MIB with 0 objects",
		})
	})

	mib, err := c.ImportMIB(context.Background(), path, []string{"cisco", "asr1000"})
	if err != nil {
		t.Fatalf("ImportMIB: %v", err)
	}
	if mib.Name != "TEST-MIB" {
		t.Errorf("Name = %q, want TEST-MIB", mib.Name)
	}
}

func TestImportMIB_MissingFile(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("server must not be called when the file cannot be opened")
	})

	_, err := c.ImportMIB(context.Background(), "/nonexistent/NO-SUCH.mib", nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Errorf("error is %T, want *ConfigError", err)
	}
}

func TestTransportError(t *testing.T) {
	// Point at a port nothing is listening on.
	c, err := New("http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.http.Timeout = 500 * time.Millisecond

	_, err = c.ListDevices(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	var transportErr *TransportError
	if !errors.As(err, &transportErr) {
		t.Errorf("error is %T, want *TransportError", err)
	}
}

func TestDecodeError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("this is not json"))
	})

	_, err := c.ListDevices(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	var decodeErr *DecodeError
	if !errors.As(err, &decodeErr) {
		t.Errorf("error is %T, want *DecodeError", err)
	}
}

func TestContextCancellation(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := c.ListDevices(ctx); err == nil {
		t.Fatal("expected an error for a canceled context")
	}
}
