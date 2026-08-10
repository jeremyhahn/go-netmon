package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// configBundleBytes stands in for an export archive. The CLI never looks inside
// one — it moves bytes between the server and a file — so what matters in these
// tests is that the bytes arrive unaltered.
var configBundleBytes = []byte{0x1f, 0x8b, 0x08, 0x00, 'b', 'u', 'n', 'd', 'l', 'e'}

// configExportHandler serves an export and records what was asked for.
func configExportHandler(body []byte, gotURI *string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		*gotURI = r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(body)
	}
}

// configImportHandler answers an import with the given report and records the
// request.
func configImportHandler(t *testing.T, report client.ImportReport, gotURI *string, gotBody *[]byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		*gotURI = r.URL.RequestURI()
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(r.Body); err != nil {
			t.Errorf("read request body: %v", err)
		}
		*gotBody = buf.Bytes()
		respondJSON(t, w, report)
	}
}

func TestConfigExportWritesTheBundleToStdout(t *testing.T) {
	var gotURI string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/config/export", configExportHandler(configBundleBytes, &gotURI))

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newConfigCommand(env), "export"); err != nil {
		t.Fatalf("config export: %v", err)
	}

	if !bytes.Equal(out.Bytes(), configBundleBytes) {
		t.Errorf("stdout = %q, want the server's bytes verbatim", out.Bytes())
	}
	if gotURI != "/api/config/export" {
		t.Errorf("requested %q", gotURI)
	}
}

func TestConfigExportNarrowsWhatItAsksFor(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"the whole bundle", []string{"export"}, "/api/config/export"},
		{"the document alone", []string{"export", "--document"}, "/api/config/export?format=json"},
		{"only the modules in use", []string{"export", "--referenced-mibs"},
			"/api/config/export?mibs=referenced"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotURI string
			mux := http.NewServeMux()
			mux.HandleFunc("/api/config/export", configExportHandler([]byte("{}"), &gotURI))

			env, _ := newFakeEnv(t, mux)
			if err := runCommand(t, newConfigCommand(env), tt.args...); err != nil {
				t.Fatalf("config export: %v", err)
			}
			if gotURI != tt.want {
				t.Errorf("requested %q, want %q", gotURI, tt.want)
			}
		})
	}
}

func TestConfigExportWritesAFileAndConfirmsIt(t *testing.T) {
	var gotURI string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/config/export", configExportHandler(configBundleBytes, &gotURI))

	path := filepath.Join(t.TempDir(), "netmon-config.tar.gz")
	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newConfigCommand(env), "export", "--output", path); err != nil {
		t.Fatalf("config export: %v", err)
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the exported file: %v", err)
	}
	if !bytes.Equal(written, configBundleBytes) {
		t.Errorf("file = %q, want the server's bytes verbatim", written)
	}
	if !strings.Contains(out.String(), path) {
		t.Errorf("output does not name the file it wrote:\n%s", out)
	}

	// An export carries community strings, so it must not be world readable.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}
}

func TestConfigExportReportsTheFileAsJSON(t *testing.T) {
	var gotURI string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/config/export", configExportHandler(configBundleBytes, &gotURI))

	path := filepath.Join(t.TempDir(), "netmon-config.tar.gz")
	env, out := newFakeEnv(t, mux)
	env.Opts.JSON = true
	if err := runCommand(t, newConfigCommand(env), "export", "--output", path); err != nil {
		t.Fatalf("config export: %v", err)
	}

	var report changeReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("decode %q: %v", out.String(), err)
	}
	if report.Action != "exported" || report.Ref != path {
		t.Errorf("report = %+v", report)
	}
}

func TestConfigExportReportsAnUnwritableFile(t *testing.T) {
	var gotURI string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/config/export", configExportHandler(configBundleBytes, &gotURI))

	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newConfigCommand(env), "export",
		"--output", filepath.Join(t.TempDir(), "no-such-directory", "config.tar.gz"))

	var fileErr *ConfigFileError
	if !errors.As(err, &fileErr) {
		t.Fatalf("error = %v, want *ConfigFileError", err)
	}
	if fileErr.Op != "write" {
		t.Errorf("op = %q, want write", fileErr.Op)
	}
}

func TestConfigExportReportsAServerFailure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/config/export", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "storage unavailable", http.StatusInternalServerError)
	})

	env, _ := newFakeEnv(t, mux)
	if err := runCommand(t, newConfigCommand(env), "export"); err == nil {
		t.Fatal("config export returned no error for a 500")
	}
}

func TestWriterIsTerminalGuardsBinaryOutput(t *testing.T) {
	// A buffer is not a file, and a regular file is not a character device, so
	// piping and redirecting an export are both allowed.
	if writerIsTerminal(&bytes.Buffer{}) {
		t.Error("a buffer was treated as a terminal")
	}

	regular, err := os.CreateTemp(t.TempDir(), "export")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer func() { _ = regular.Close() }()
	if writerIsTerminal(regular) {
		t.Error("a regular file was treated as a terminal")
	}

	// A character device is what the guard is looking for.
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer func() { _ = devNull.Close() }()
	if !writerIsTerminal(devNull) {
		t.Errorf("%s is a character device and should trip the guard", os.DevNull)
	}

	if (&TerminalOutputError{}).Error() == "" {
		t.Error("the terminal guard's error has no message")
	}
}

func TestConfigImportIsADryRunByDefault(t *testing.T) {
	report := client.ImportReport{
		SchemaVersion: client.ConfigSchemaVersion,
		Changed:       true,
		Counts: client.ImportCounts{
			DevicesDeleted: 1, DevicesUpdated: 1, DevicesCreated: 1, MIBsImported: 1,
		},
		MIBs: []client.ConfigMIBChange{
			{Module: "IF-MIB", Action: client.MIBActionImport, File: "IF-MIB.txt"},
		},
		Devices: []client.ConfigChange{
			{Action: client.ConfigActionDelete, Name: "bastion", ID: 4, Detail: "192.0.2.14:161"},
			{
				Action: client.ConfigActionUpdate, Name: "core-01", ID: 1,
				Fields: []client.ConfigFieldChange{
					{Field: "address", From: "192.0.2.252", To: "192.0.2.253"},
					{Field: "mibs[IF-MIB].include_names", From: "[]", To: "[^ifIn]"},
				},
			},
			{Action: client.ConfigActionCreate, Name: "newswitch", Detail: "192.0.2.9:161"},
		},
	}

	var gotURI string
	var gotBody []byte
	mux := http.NewServeMux()
	mux.HandleFunc("/api/config/import", configImportHandler(t, report, &gotURI, &gotBody))

	path := writeTempFile(t, configBundleBytes)
	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newConfigCommand(env), "import", path); err != nil {
		t.Fatalf("config import: %v", err)
	}

	if gotURI != "/api/config/import" {
		t.Errorf("requested %q, want no apply parameter", gotURI)
	}
	if !bytes.Equal(gotBody, configBundleBytes) {
		t.Errorf("uploaded %q, want the file's bytes verbatim", gotBody)
	}

	got := out.String()
	for _, want := range []string{
		"DRY RUN", "nothing has been changed",
		"MIB modules", "import", "IF-MIB",
		"delete", "bastion", "(id 4)",
		"update", "core-01", "address: 192.0.2.252 -> 192.0.2.253",
		"mibs[IF-MIB].include_names: [] -> [^ifIn]",
		"create", "newswitch",
		"1 device deleted", "1 device updated", "1 device created", "1 MIB module imported",
		"--yes",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestConfigImportAppliesWhenAsked(t *testing.T) {
	report := client.ImportReport{
		Applied:      true,
		Changed:      true,
		Counts:       client.ImportCounts{DevicesDeleted: 1},
		Devices:      []client.ConfigChange{{Action: client.ConfigActionDelete, Name: "bastion", ID: 4}},
		SafetyExport: "/app/data/webui/config-backups/pre-import-20260810T145103Z.tar.gz",
	}

	var gotURI string
	var gotBody []byte
	mux := http.NewServeMux()
	mux.HandleFunc("/api/config/import", configImportHandler(t, report, &gotURI, &gotBody))

	path := writeTempFile(t, configBundleBytes)
	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newConfigCommand(env), "import", path, "--yes"); err != nil {
		t.Fatalf("config import: %v", err)
	}

	if gotURI != "/api/config/import?apply=true" {
		t.Errorf("requested %q, want the apply parameter", gotURI)
	}

	got := out.String()
	if !strings.Contains(got, "APPLIED") {
		t.Errorf("output does not say the import was applied:\n%s", got)
	}
	if !strings.Contains(got, report.SafetyExport) {
		t.Errorf("output does not name the safety export:\n%s", got)
	}
	if strings.Contains(got, "--yes") {
		t.Errorf("an applied import still suggests --yes:\n%s", got)
	}
}

func TestConfigImportReportsANoOp(t *testing.T) {
	var gotURI string
	var gotBody []byte
	mux := http.NewServeMux()
	mux.HandleFunc("/api/config/import",
		configImportHandler(t, client.ImportReport{Changed: false}, &gotURI, &gotBody))

	path := writeTempFile(t, configBundleBytes)
	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newConfigCommand(env), "import", path); err != nil {
		t.Fatalf("config import: %v", err)
	}

	if !strings.Contains(out.String(), "already matches the file") {
		t.Errorf("output does not report a no-op:\n%s", out)
	}
}

func TestConfigImportEmitsTheReportAsJSON(t *testing.T) {
	want := client.ImportReport{
		Applied: true, Changed: true,
		Counts:       client.ImportCounts{DevicesDeleted: 2},
		SafetyExport: "/tmp/pre-import.tar.gz",
	}

	var gotURI string
	var gotBody []byte
	mux := http.NewServeMux()
	mux.HandleFunc("/api/config/import", configImportHandler(t, want, &gotURI, &gotBody))

	path := writeTempFile(t, configBundleBytes)
	env, out := newFakeEnv(t, mux)
	env.Opts.JSON = true
	if err := runCommand(t, newConfigCommand(env), "import", path, "--yes"); err != nil {
		t.Fatalf("config import: %v", err)
	}

	var got client.ImportReport
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", out.String(), err)
	}
	if got.SafetyExport != want.SafetyExport || got.Counts.DevicesDeleted != 2 {
		t.Errorf("report = %+v, want %+v", got, want)
	}
}

func TestConfigImportReadsStdin(t *testing.T) {
	var gotURI string
	var gotBody []byte
	mux := http.NewServeMux()
	mux.HandleFunc("/api/config/import",
		configImportHandler(t, client.ImportReport{}, &gotURI, &gotBody))

	env, _ := newFakeEnv(t, mux)
	env.In = bytes.NewReader(configBundleBytes)
	if err := runCommand(t, newConfigCommand(env), "import", "-"); err != nil {
		t.Fatalf("config import: %v", err)
	}

	if !bytes.Equal(gotBody, configBundleBytes) {
		t.Errorf("uploaded %q, want what was on stdin", gotBody)
	}
}

func TestConfigImportReportsAMissingFile(t *testing.T) {
	mux := http.NewServeMux()
	env, _ := newFakeEnv(t, mux)

	err := runCommand(t, newConfigCommand(env), "import",
		filepath.Join(t.TempDir(), "not-there.tar.gz"))

	var fileErr *ConfigFileError
	if !errors.As(err, &fileErr) {
		t.Fatalf("error = %v, want *ConfigFileError", err)
	}
	if fileErr.Op != "read" {
		t.Errorf("op = %q, want read", fileErr.Op)
	}
	if fileErr.Unwrap() == nil {
		t.Error("the file error does not carry its cause")
	}
}

func TestConfigImportSurfacesModulesThatMustBeImportedFirst(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/config/import", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"kind":            "missing_mibs",
			"message":         "2 MIB module(s) are not imported: ENTITY-MIB, IF-MIB",
			"missing_modules": []string{"ENTITY-MIB", "IF-MIB"},
		})
	})

	path := writeTempFile(t, configBundleBytes)
	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newConfigCommand(env), "import", path, "--yes")

	var configErr *client.ConfigDocumentError
	if !errors.As(err, &configErr) {
		t.Fatalf("error = %v, want *client.ConfigDocumentError", err)
	}
	if !configErr.MissingMIBs() {
		t.Error("the error does not report missing modules")
	}
	for _, module := range []string{"ENTITY-MIB", "IF-MIB"} {
		if !strings.Contains(err.Error(), module) {
			t.Errorf("message %q does not name %s", err, module)
		}
	}
}

func TestSummarizeCounts(t *testing.T) {
	tests := []struct {
		name   string
		counts client.ImportCounts
		want   string
	}{
		{"nothing at all", client.ImportCounts{}, "No changes."},
		{
			"one of each", client.ImportCounts{DevicesDeleted: 1, ProfilesCreated: 1},
			"1 device deleted, 1 profile created.",
		},
		{
			"several", client.ImportCounts{DevicesUpdated: 3, MIBsRefreshed: 17},
			"3 devices updated, 17 MIB modules refreshed.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := summarizeCounts(tt.counts); got != tt.want {
				t.Errorf("summarizeCounts() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBlankAndIDSuffixRenderEmptyValues(t *testing.T) {
	if got := blank(""); got != "(none)" {
		t.Errorf("blank(\"\") = %q", got)
	}
	if got := blank("x"); got != "x" {
		t.Errorf("blank(%q) = %q", "x", got)
	}
	if got := idSuffix(0); got != "" {
		t.Errorf("idSuffix(0) = %q, want nothing for a record that has no ID yet", got)
	}
	if got := idSuffix(7); got != " (id 7)" {
		t.Errorf("idSuffix(7) = %q", got)
	}
}

func TestConfigCommandsAreReachableFromTheRoot(t *testing.T) {
	root := NewRootCommand("test", "abc", "today")

	config, _, err := root.Find([]string{"config"})
	if err != nil {
		t.Fatalf("config is not registered: %v", err)
	}
	for _, name := range []string{"export", "import"} {
		if _, _, findErr := root.Find([]string{"config", name}); findErr != nil {
			t.Errorf("config %s is not registered: %v", name, findErr)
		}
	}
	if config.Short == "" {
		t.Error("the config command has no summary")
	}
}

// writeTempFile puts content in a file for a command to read.
func writeTempFile(t *testing.T, content []byte) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.tar.gz")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestConfigTimeoutHonoursAnExplicitTimeout(t *testing.T) {
	root := NewRootCommand("test", "abc", "today")
	opts := &Options{Timeout: 3 * time.Second}

	config, _, err := root.Find([]string{"config"})
	if err != nil {
		t.Fatalf("find config: %v", err)
	}

	// Untouched, a configuration command gets the long default: an import
	// re-parses every MIB module a bundle carries.
	if got := configTimeout(config, opts); got != ConfigCommandTimeout {
		t.Errorf("configTimeout() = %s, want %s", got, ConfigCommandTimeout)
	}

	if err := root.PersistentFlags().Set("timeout", "3s"); err != nil {
		t.Fatalf("set --timeout: %v", err)
	}
	if got := configTimeout(config, opts); got != 3*time.Second {
		t.Errorf("configTimeout() = %s, want the operator's 3s", got)
	}
}

func TestConfigTimeoutWithoutARootTimeoutFlag(t *testing.T) {
	// A command tree built without the root's persistent flags — which is how
	// the subcommand tests run it — still gets a usable deadline.
	env, _ := newFakeEnv(t, http.NewServeMux())
	cmd := newConfigCommand(env)
	if got := configTimeout(cmd, env.Opts); got != ConfigCommandTimeout {
		t.Errorf("configTimeout() = %s, want %s", got, ConfigCommandTimeout)
	}
}

func TestConfigImportReportsAStdinFailure(t *testing.T) {
	env, _ := newFakeEnv(t, http.NewServeMux())
	env.In = failingReader{}

	err := runCommand(t, newConfigCommand(env), "import", "-")

	var fileErr *ConfigFileError
	if !errors.As(err, &fileErr) {
		t.Fatalf("error = %v, want *ConfigFileError", err)
	}
	if fileErr.Path != "stdin" {
		t.Errorf("path = %q, want stdin", fileErr.Path)
	}
}

func TestRenderImportReportOmitsSectionsWithNothingInThem(t *testing.T) {
	var out bytes.Buffer
	renderImportReport(&out, &client.ImportReport{
		Changed: true,
		Counts:  client.ImportCounts{ProfilesCreated: 1},
		Profiles: []client.ConfigChange{
			{Action: client.ConfigActionCreate, Name: "core", Detail: "2 MIB(s)"},
		},
	})

	got := out.String()
	if strings.Contains(got, "MIB modules") {
		t.Errorf("a report with no MIB changes printed a MIB section:\n%s", got)
	}
	if strings.Contains(got, "Devices") {
		t.Errorf("a report with no device changes printed a device section:\n%s", got)
	}
	if !strings.Contains(got, "core") || !strings.Contains(got, "2 MIB(s)") {
		t.Errorf("the profile change is missing:\n%s", got)
	}
}

// failingReader stands in for a stdin that cannot be read.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("stdin is closed")
}
