package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// gzipBundle is the two-byte gzip magic followed by nothing useful. The SDK only
// has to recognize it, never decode it: unpacking a bundle is the server's job.
var gzipBundle = []byte{0x1f, 0x8b, 0x08, 0x00, 0x00}

func TestExportConfigBundleReturnsTheServersBytesUntouched(t *testing.T) {
	archive := append(append([]byte(nil), gzipBundle...), []byte("payload")...)

	var gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(archive)
	})

	body, err := c.ExportConfigBundle(context.Background(), ConfigExportOptions{})
	if err != nil {
		t.Fatalf("ExportConfigBundle: %v", err)
	}
	if !bytes.Equal(body, archive) {
		t.Errorf("body = %q, want the server's bytes verbatim", body)
	}
	if gotPath != "/api/config/export" {
		t.Errorf("requested %q", gotPath)
	}
}

func TestExportConfigBundleNarrowsWhatItAsksFor(t *testing.T) {
	tests := []struct {
		name string
		opts ConfigExportOptions
		want string
	}{
		{"the whole bundle", ConfigExportOptions{}, "/api/config/export"},
		{"the document alone", ConfigExportOptions{DocumentOnly: true},
			"/api/config/export?format=json"},
		{"only the modules in use", ConfigExportOptions{ReferencedMIBsOnly: true},
			"/api/config/export?mibs=referenced"},
		{"both", ConfigExportOptions{DocumentOnly: true, ReferencedMIBsOnly: true},
			"/api/config/export?format=json&mibs=referenced"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath string
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.RequestURI()
				_, _ = w.Write([]byte("{}"))
			})

			if _, err := c.ExportConfigBundle(context.Background(), tt.opts); err != nil {
				t.Fatalf("ExportConfigBundle: %v", err)
			}
			if gotPath != tt.want {
				t.Errorf("requested %q, want %q", gotPath, tt.want)
			}
		})
	}
}

func TestExportConfigDecodesTheDocument(t *testing.T) {
	interval := 45
	want := Config{
		SchemaVersion: ConfigSchemaVersion,
		MIBs:          []ConfigMIB{{Name: "IF-MIB", File: "mibs/IF-MIB.txt", SHA256: "abc"}},
		Profiles: []ConfigProfile{{
			Name: "core",
			MIBs: []ConfigAssignment{{
				Module:        "IF-MIB",
				MIBAssignment: MIBAssignment{Enabled: true, IncludeNames: []string{"^ifIn"}},
			}},
		}},
		Devices: []ConfigDevice{{
			Name: "core-01", Address: "192.0.2.1", Port: 161, SNMPVersion: "2c",
			Community: "public", PollInterval: &interval, Profiles: []string{"core"},
		}},
	}

	var gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		_ = json.NewEncoder(w).Encode(want)
	})

	got, err := c.ExportConfig(context.Background(), ConfigExportOptions{})
	if err != nil {
		t.Fatalf("ExportConfig: %v", err)
	}
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("document = %+v, want %+v", *got, want)
	}
	// A decoded document cannot carry MIB source files, so it must ask for the
	// document rather than silently discard the archive around it.
	if gotPath != "/api/config/export?format=json" {
		t.Errorf("requested %q, want the JSON form", gotPath)
	}
}

func TestExportConfigReportsAServerThatFails(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})

	if _, err := c.ExportConfig(context.Background(), ConfigExportOptions{}); err == nil {
		t.Fatal("ExportConfig returned no error for a 500")
	}
}

func TestExportConfigReportsABodyThatIsNotADocument(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json"))
	})

	var decodeErr *DecodeError
	if _, err := c.ExportConfig(context.Background(), ConfigExportOptions{}); !errors.As(err, &decodeErr) {
		t.Fatalf("error = %v, want *DecodeError", err)
	}
}

func TestExportConfigBundleReportsAnUnreachableServer(t *testing.T) {
	c, err := New("http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var transport *TransportError
	if _, err := c.ExportConfigBundle(context.Background(), ConfigExportOptions{}); !errors.As(err, &transport) {
		t.Fatalf("error = %v, want *TransportError", err)
	}
}

func TestImportConfigBundleIsADryRunUnlessAsked(t *testing.T) {
	tests := []struct {
		name string
		opts ConfigImportOptions
		want string
	}{
		{"a dry run by default", ConfigImportOptions{}, "/api/config/import"},
		{"applied when asked", ConfigImportOptions{Apply: true}, "/api/config/import?apply=true"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath, gotType string
			var gotBody []byte
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.RequestURI()
				gotType = r.Header.Get("Content-Type")
				gotBody, _ = readAll(r)
				_ = json.NewEncoder(w).Encode(ImportReport{Applied: tt.opts.Apply})
			})

			report, err := c.ImportConfigBundle(context.Background(), gzipBundle, tt.opts)
			if err != nil {
				t.Fatalf("ImportConfigBundle: %v", err)
			}
			if report.Applied != tt.opts.Apply {
				t.Errorf("applied = %v, want %v", report.Applied, tt.opts.Apply)
			}
			if gotPath != tt.want {
				t.Errorf("requested %q, want %q", gotPath, tt.want)
			}
			if !bytes.Equal(gotBody, gzipBundle) {
				t.Errorf("body = %q, want the bytes given verbatim", gotBody)
			}
			if gotType != "application/gzip" {
				t.Errorf("Content-Type = %q, want application/gzip", gotType)
			}
		})
	}
}

func TestImportConfigSendsADocument(t *testing.T) {
	var gotBody []byte
	var gotType string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotType = r.Header.Get("Content-Type")
		gotBody, _ = readAll(r)
		_ = json.NewEncoder(w).Encode(ImportReport{})
	})

	doc := Config{Devices: []ConfigDevice{{Name: "bastion", Address: "192.0.2.14", SNMPVersion: "2c"}}}
	if _, err := c.ImportConfig(context.Background(), doc, ConfigImportOptions{}); err != nil {
		t.Fatalf("ImportConfig: %v", err)
	}
	if gotType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotType)
	}

	var sent Config
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("the SDK sent something that is not a document: %v", err)
	}
	// A caller that builds a document in memory should not have to remember the
	// schema version, but the server must still receive one.
	if sent.SchemaVersion != ConfigSchemaVersion {
		t.Errorf("schema_version = %d, want %d", sent.SchemaVersion, ConfigSchemaVersion)
	}
}

func TestImportConfigKeepsAnExplicitSchemaVersion(t *testing.T) {
	var gotBody []byte
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = readAll(r)
		_ = json.NewEncoder(w).Encode(ImportReport{})
	})

	if _, err := c.ImportConfig(context.Background(),
		Config{SchemaVersion: 99}, ConfigImportOptions{}); err != nil {
		t.Fatalf("ImportConfig: %v", err)
	}

	var sent Config
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if sent.SchemaVersion != 99 {
		t.Errorf("schema_version = %d, want the caller's 99 to reach the server", sent.SchemaVersion)
	}
}

func TestImportConfigSurfacesTheServersDetail(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       configErrorBody
		wantKind   string
		assertions func(t *testing.T, err *ConfigDocumentError)
	}{
		{
			name:   "modules that have to be imported first",
			status: http.StatusConflict,
			body: configErrorBody{
				Kind: "missing_mibs", Message: "2 modules missing",
				MissingModules: []string{"ENTITY-MIB", "IF-MIB"},
			},
			wantKind: "missing_mibs",
			assertions: func(t *testing.T, err *ConfigDocumentError) {
				if !err.MissingMIBs() {
					t.Error("MissingMIBs() is false for a missing-module refusal")
				}
				if !reflect.DeepEqual(err.MissingModules, []string{"ENTITY-MIB", "IF-MIB"}) {
					t.Errorf("modules = %v", err.MissingModules)
				}
			},
		},
		{
			name:   "fields that have to be corrected",
			status: http.StatusBadRequest,
			body: configErrorBody{
				Kind: "validation", Message: "1 problem",
				Problems: []ConfigProblem{{
					Scope: "device", Subject: "bastion", Field: "snmp_version",
					Value: "v4", Reason: "is not supported",
				}},
			},
			wantKind: "validation",
			assertions: func(t *testing.T, err *ConfigDocumentError) {
				if len(err.Problems) != 1 || err.Problems[0].Field != "snmp_version" {
					t.Errorf("problems = %+v", err.Problems)
				}
				if got := err.Problems[0].Error(); got == "" {
					t.Error("a problem has no message")
				}
			},
		},
		{
			name:   "a document from a newer build",
			status: http.StatusBadRequest,
			body: configErrorBody{
				Kind: "schema", Message: "unreadable",
				SchemaVersion: 7, SupportedSchemaVersion: 1,
			},
			wantKind: "schema",
			assertions: func(t *testing.T, err *ConfigDocumentError) {
				if err.SchemaVersion != 7 || err.SupportedSchemaVersion != 1 {
					t.Errorf("schema versions = %d/%d", err.SchemaVersion, err.SupportedSchemaVersion)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_ = json.NewEncoder(w).Encode(tt.body)
			})

			_, err := c.ImportConfigBundle(context.Background(), gzipBundle, ConfigImportOptions{})

			var configErr *ConfigDocumentError
			if !errors.As(err, &configErr) {
				t.Fatalf("error = %v, want *ConfigDocumentError", err)
			}
			if configErr.Kind != tt.wantKind {
				t.Errorf("kind = %q, want %q", configErr.Kind, tt.wantKind)
			}
			if configErr.StatusCode != tt.status {
				t.Errorf("status = %d, want %d", configErr.StatusCode, tt.status)
			}
			if configErr.Error() == "" {
				t.Error("the error has no message")
			}
			tt.assertions(t, configErr)
		})
	}
}

func TestImportConfigLeavesAnUnstructuredFailureAlone(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gateway exploded", http.StatusBadGateway)
	})

	_, err := c.ImportConfigBundle(context.Background(), gzipBundle, ConfigImportOptions{})

	var configErr *ConfigDocumentError
	if errors.As(err, &configErr) {
		t.Fatalf("a plain-text failure was decoded as a configuration error: %+v", configErr)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *APIError", err)
	}
}

func TestConfigDocumentErrorFallsBackToTheStatusLine(t *testing.T) {
	err := &ConfigDocumentError{StatusCode: http.StatusConflict}
	if got := err.Error(); got != "netmon config: 409 Conflict" {
		t.Errorf("Error() = %q", got)
	}
	if err.MissingMIBs() {
		t.Error("MissingMIBs() is true with no modules listed")
	}
}

func TestContentTypeOfLabelsTheUpload(t *testing.T) {
	tests := []struct {
		name string
		body []byte
		want string
	}{
		{"a gzipped bundle", gzipBundle, "application/gzip"},
		{"a bare document", []byte(`{"schema_version":1}`), "application/json"},
		{"an empty body", nil, "application/json"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := contentTypeOf(tt.body); got != tt.want {
				t.Errorf("contentTypeOf() = %q, want %q", got, tt.want)
			}
		})
	}
}

// readAll drains a request body for a test handler.
func readAll(r *http.Request) ([]byte, error) {
	var buf bytes.Buffer
	_, err := buf.ReadFrom(r.Body)
	return buf.Bytes(), err
}

// TestImportConfigBundleRejectsAnUnusableRequest covers the path where the
// request cannot even be built, which a caller sees as a transport failure
// rather than a server refusal.
func TestImportConfigBundleRejectsAnUnusableRequest(t *testing.T) {
	c, err := New("http://example.invalid")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.baseURL = "http://%zz"

	var transport *TransportError
	if _, err := c.ImportConfigBundle(context.Background(), gzipBundle, ConfigImportOptions{}); !errors.As(err, &transport) {
		t.Fatalf("error = %v, want *TransportError", err)
	}
}

// TestExportConfigBundleRejectsAnUnusableRequest is the same check for the
// export path.
func TestExportConfigBundleRejectsAnUnusableRequest(t *testing.T) {
	c, err := New("http://example.invalid")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.baseURL = "http://%zz"

	var transport *TransportError
	if _, err := c.ExportConfigBundle(context.Background(), ConfigExportOptions{}); !errors.As(err, &transport) {
		t.Fatalf("error = %v, want *TransportError", err)
	}
}

// TestExportConfigBundleReportsAServerErrorBody keeps the structured detail on
// the export path too, so a client that asks for an export from a server that
// refuses the parameters gets the same shape it gets from an import.
func TestExportConfigBundleReportsAServerErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(configErrorBody{Kind: "bundle", Message: "want bundle or json"})
	}))
	t.Cleanup(srv.Close)

	c, err := New(srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = c.ExportConfigBundle(context.Background(), ConfigExportOptions{})

	var configErr *ConfigDocumentError
	if !errors.As(err, &configErr) {
		t.Fatalf("error = %v, want *ConfigDocumentError", err)
	}
	if configErr.Message != "want bundle or json" {
		t.Errorf("message = %q", configErr.Message)
	}
}

func TestExportConfigBundleBoundsALongErrorBody(t *testing.T) {
	// A misrouted request can return a whole HTML page. The error must carry
	// enough of it to diagnose and not the whole thing.
	long := strings.Repeat("x", 8192)
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, long, http.StatusNotFound)
	})

	_, err := c.ExportConfigBundle(context.Background(), ConfigExportOptions{})

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *APIError", err)
	}
	if len(apiErr.Body) > 2048 {
		t.Errorf("error body is %d bytes, want it bounded", len(apiErr.Body))
	}
	if !apiErr.NotFound() {
		t.Error("a 404 is not reported as not found")
	}
}

func TestConfigImportErrorLeavesANonAPIErrorAlone(t *testing.T) {
	inner := errors.New("something else")
	if got := configImportError(inner); !errors.Is(got, inner) {
		t.Errorf("configImportError() = %v, want the error it was given", got)
	}
}

func TestConfigImportErrorIgnoresABodyWithNoKind(t *testing.T) {
	// A JSON body from some other handler is not a configuration refusal, and
	// decoding it as one would invent a Kind the server never set.
	apiErr := &APIError{StatusCode: 400, Body: `{"message":"something"}`}
	if _, isConfig := configImportError(apiErr).(*ConfigDocumentError); isConfig {
		t.Error("a body with no kind was decoded as a configuration refusal")
	}
}

func TestConfigProblemRendersWithAndWithoutAField(t *testing.T) {
	withField := &ConfigProblem{
		Scope: "device", Subject: "bastion", Field: "port", Value: "70000",
		Reason: "must be between 1 and 65535",
	}
	if got := withField.Error(); !strings.Contains(got, `device "bastion"`) ||
		!strings.Contains(got, `port "70000"`) {
		t.Errorf("Error() = %q", got)
	}

	// A problem with the document as a whole names no field, and must not print
	// an empty one.
	whole := &ConfigProblem{Scope: "document", Reason: "no configuration was supplied"}
	if got := whole.Error(); got != "document: no configuration was supplied" {
		t.Errorf("Error() = %q", got)
	}
}

func TestExportConfigBundleReportsATruncatedResponse(t *testing.T) {
	// A response that promises more than it delivers: the client must report a
	// failure rather than hand back a half-read archive as if it were whole.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "4096")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		panic(http.ErrAbortHandler)
	}))
	t.Cleanup(srv.Close)

	c, err := New(srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var decodeErr *DecodeError
	if _, err := c.ExportConfigBundle(context.Background(), ConfigExportOptions{}); !errors.As(err, &decodeErr) {
		t.Fatalf("error = %v, want *DecodeError", err)
	}
}
