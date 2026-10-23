package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/mib/loader"
	"github.com/jeremyhahn/go-netmon/internal/testsupport"
	webuistorage "github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// powerMIB is a second self-contained module, so the configuration tests can
// exercise a device with a direct assignment and a profile with another without
// either of them standing in for the other.
const powerMIB = `TEST-POWER-MIB DEFINITIONS ::= BEGIN

IMPORTS
    MODULE-IDENTITY, OBJECT-TYPE, Integer32, enterprises
        FROM SNMPv2-SMI;

testPowerMIB MODULE-IDENTITY
    LAST-UPDATED "202401010000Z"
    ORGANIZATION "go-netmon tests"
    CONTACT-INFO  "go-netmon"
    DESCRIPTION   "Power readings, used by the configuration tests."
    REVISION      "202401010000Z"
    DESCRIPTION   "Initial revision."
    ::= { enterprises 99998 }

testPowerWatts OBJECT-TYPE
    SYNTAX      Integer32
    UNITS       "watts"
    MAX-ACCESS  read-only
    STATUS      current
    DESCRIPTION "Power drawn right now."
    ::= { testPowerMIB 1 }

END
`

const (
	powerModule = "TEST-POWER-MIB"
	powerFile   = "TEST-POWER-MIB.my"
)

// configFixture is a server wired the way a deployed one is: a real MIB parser,
// a real object store, a real record store and a poller that records what it was
// told. Nothing about export and import is worth asserting against a stand-in
// parser, because importing a bundle is exactly "parse these files and then
// write this configuration".
type configFixture struct {
	server    *Server
	records   *webuistorage.Storage
	poller    *recordingPoller
	backupDir string
}

func newConfigFixture(t *testing.T) *configFixture {
	t.Helper()

	root := t.TempDir()
	uploaded := filepath.Join(root, "uploaded")
	if err := os.MkdirAll(uploaded, 0o755); err != nil {
		t.Fatalf("create upload directory: %v", err)
	}

	records, err := webuistorage.New(filepath.Join(root, "webui.db"))
	if err != nil {
		t.Fatalf("create web UI storage: %v", err)
	}
	t.Cleanup(func() { _ = records.Close() })

	mibLoader, err := loader.New(&config.MIBConfig{
		StoragePath: filepath.Join(root, "mib.db"),
		Paths:       append(testsupport.MIBDirs("standard"), uploaded),
	})
	if err != nil {
		t.Fatalf("create MIB loader: %v", err)
	}
	// Closing the loader releases the parser's process-wide module cache, so
	// the next test starts from an empty one.
	t.Cleanup(func() { _ = mibLoader.Close() })

	backups := filepath.Join(root, "config-backups")
	poller := &recordingPoller{}
	server := &Server{
		router:         mux.NewRouter(),
		storage:        records,
		mibLoader:      mibLoader,
		mibStoragePath: root,
		dbPath:         filepath.Join(root, "webui.db"),
		configBackups:  backups,
		pollerClient:   poller,
	}
	server.routes()

	return &configFixture{server: server, records: records, poller: poller, backupDir: backups}
}

// seed builds a configuration that uses every shape the document carries.
func (f *configFixture) seed(t *testing.T) {
	t.Helper()
	ctx := t.Context()

	f.importModule(t, sensorFile, sensorMIB)
	f.importModule(t, powerFile, powerMIB)

	profile := &webuistorage.DeviceProfile{Name: "core", Description: "core routers"}
	if err := f.records.CreateDeviceProfile(ctx, profile); err != nil {
		t.Fatalf("CreateDeviceProfile: %v", err)
	}
	sensorID := f.moduleID(t, sensorModule)
	if err := f.records.AssignMIBToProfile(ctx, profile.ID, sensorID); err != nil {
		t.Fatalf("AssignMIBToProfile: %v", err)
	}
	if err := f.records.SetProfileMIBAssignment(ctx, profile.ID, sensorID,
		webuistorage.MIBAssignment{
			Enabled: true, IncludeNames: []string{"^testSensor"}, MaxOIDs: 25,
		}); err != nil {
		t.Fatalf("SetProfileMIBAssignment: %v", err)
	}

	interval := 45
	router := &webuistorage.Device{
		Name: "core-01", Address: "192.0.2.252", SNMPVersion: "2c",
		Community: "public", Port: 161, PollInterval: &interval,
	}
	if err := f.records.CreateDevice(ctx, router); err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}
	if err := f.records.AssignProfileToDevice(ctx, router.ID, profile.ID); err != nil {
		t.Fatalf("AssignProfileToDevice: %v", err)
	}
	if err := f.records.AssignMIBToDevice(ctx, router.ID, f.moduleID(t, powerModule)); err != nil {
		t.Fatalf("AssignMIBToDevice: %v", err)
	}

	bastion := &webuistorage.Device{Name: "bastion", Address: "192.0.2.14", SNMPVersion: "v2c"}
	if err := f.records.CreateDevice(ctx, bastion); err != nil {
		t.Fatalf("CreateDevice(bastion): %v", err)
	}
}

// importModule imports one MIB source through the same path an upload takes.
func (f *configFixture) importModule(t *testing.T, fileName, content string) {
	t.Helper()

	if _, err := f.server.importMIBSource(
		t.Context(), fileName, strings.NewReader(content), nil, true); err != nil {
		t.Fatalf("importMIBSource(%s): %v", fileName, err)
	}
}

func (f *configFixture) moduleID(t *testing.T, name string) int64 {
	t.Helper()

	record, err := f.records.GetMIBByName(t.Context(), name)
	if err != nil {
		t.Fatalf("GetMIBByName(%s): %v", name, err)
	}
	return record.ID
}

// exportBundle fetches an export through the router and unpacks it.
func (f *configFixture) exportBundle(t *testing.T, query string) (*ConfigBundle, []byte) {
	t.Helper()

	rec := do(t, f.server, http.MethodGet, "/api/config/export"+query, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("export status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}

	archive := rec.Body.Bytes()
	bundle, err := readConfigBundle(archive)
	if err != nil {
		t.Fatalf("readConfigBundle: %v", err)
	}
	return bundle, archive
}

// postBundle uploads an archive to the import endpoint.
func (f *configFixture) postBundle(t *testing.T, archive []byte, query string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/api/config/import"+query, bytes.NewReader(archive))
	req.Header.Set("Content-Type", "application/gzip")
	rec := httptest.NewRecorder()
	f.server.router.ServeHTTP(rec, req)
	return rec
}

// currentDocument exports the configuration directly from the store, which is
// what a test compares before and after.
//
// The export timestamp is cleared: it moves on every call by design, and it
// describes when the copy was taken rather than what is configured.
func (f *configFixture) currentDocument(t *testing.T) *webuistorage.Config {
	t.Helper()

	doc, err := f.records.ExportConfig(t.Context(), webuistorage.ConfigExportOptions{})
	if err != nil {
		t.Fatalf("ExportConfig: %v", err)
	}
	doc.ExportedAt = time.Time{}
	return doc
}

func decodeReport(t *testing.T, rec *httptest.ResponseRecorder) ImportReport {
	t.Helper()

	var report ImportReport
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode report from %q: %v", rec.Body.String(), err)
	}
	return report
}

func decodeConfigError(t *testing.T, rec *httptest.ResponseRecorder) ConfigErrorResponse {
	t.Helper()

	var body ConfigErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body from %q: %v", rec.Body.String(), err)
	}
	return body
}

func TestExportProducesASelfContainedBundle(t *testing.T) {
	f := newConfigFixture(t)
	f.seed(t)

	bundle, archive := f.exportBundle(t, "")

	if bundle.Config.SchemaVersion != webuistorage.ConfigSchemaVersion {
		t.Errorf("schema_version = %d", bundle.Config.SchemaVersion)
	}
	if len(bundle.Config.Devices) != 2 || len(bundle.Config.Profiles) != 1 {
		t.Fatalf("bundle carries %d devices and %d profiles, want 2 and 1",
			len(bundle.Config.Devices), len(bundle.Config.Profiles))
	}

	// Every module the document names must arrive with its source file and a
	// checksum that matches it. That is the whole point of a bundle: a restore
	// onto an empty instance must not need the MIB files found separately.
	if len(bundle.Config.MIBs) != 2 {
		t.Fatalf("bundle names %d modules, want 2", len(bundle.Config.MIBs))
	}
	for _, entry := range bundle.Config.MIBs {
		content, carried := bundle.MIBFiles[entry.File]
		if !carried {
			t.Fatalf("module %s names file %q, which the bundle does not carry", entry.Name, entry.File)
		}
		if got := checksum(content); got != entry.SHA256 {
			t.Errorf("module %s checksum = %s, want %s", entry.Name, got, entry.SHA256)
		}
		if !strings.HasPrefix(entry.File, bundleMIBDir+"/") {
			t.Errorf("module %s file %q is not under %s/", entry.Name, entry.File, bundleMIBDir)
		}
	}

	if _, err := verifyBundleSources(bundle); err != nil {
		t.Errorf("the bundle this server produced does not verify: %v", err)
	}
	if !isGzip(archive) {
		t.Error("the export is not gzipped")
	}
}

func TestExportHeadersMarkTheDownload(t *testing.T) {
	f := newConfigFixture(t)
	f.seed(t)

	rec := do(t, f.server, http.MethodGet, "/api/config/export", "")

	if got := rec.Header().Get("Content-Type"); got != "application/gzip" {
		t.Errorf("Content-Type = %q, want application/gzip", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, ".tar.gz") {
		t.Errorf("Content-Disposition = %q, want a .tar.gz attachment", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

func TestExportAsJSONReturnsTheDocumentAlone(t *testing.T) {
	f := newConfigFixture(t)
	f.seed(t)

	rec := do(t, f.server, http.MethodGet, "/api/config/export?format=json", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}

	var doc webuistorage.Config
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("the JSON export does not parse: %v", err)
	}
	if len(doc.Devices) != 2 {
		t.Errorf("devices = %d, want 2", len(doc.Devices))
	}
	if !bytes.HasSuffix(rec.Body.Bytes(), []byte("\n")) {
		t.Error("the JSON export has no trailing newline, so it does not diff as a text file")
	}
}

func TestExportReferencedMIBsOnly(t *testing.T) {
	f := newConfigFixture(t)
	f.seed(t)

	// A module nothing assigns is in the catalog but not in the configuration.
	f.importModule(t, "SPARE-MIB.my", strings.ReplaceAll(
		strings.ReplaceAll(powerMIB, powerModule, "SPARE-MIB"), "99998", "99997"))

	all, _ := f.exportBundle(t, "")
	referenced, _ := f.exportBundle(t, "?mibs=referenced")

	if len(all.Config.MIBs) != 3 {
		t.Errorf("the full export names %d modules, want 3", len(all.Config.MIBs))
	}
	if len(referenced.Config.MIBs) != 2 {
		t.Errorf("the narrowed export names %d modules, want 2", len(referenced.Config.MIBs))
	}
	if len(referenced.MIBFiles) != 2 {
		t.Errorf("the narrowed export carries %d files, want 2", len(referenced.MIBFiles))
	}
}

func TestExportRejectsUnknownParameters(t *testing.T) {
	f := newConfigFixture(t)

	tests := []struct{ name, query string }{
		{"an unknown format", "?format=yaml"},
		{"an unknown MIB selection", "?mibs=some"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, f.server, http.MethodGet, "/api/config/export"+tt.query, "")
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			if kind := decodeConfigError(t, rec).Kind; kind != ConfigErrorBundle {
				t.Errorf("kind = %q, want %q", kind, ConfigErrorBundle)
			}
		})
	}
}

func TestImportIsADryRunByDefaultAndChangesNothing(t *testing.T) {
	f := newConfigFixture(t)
	f.seed(t)

	bundle, _ := f.exportBundle(t, "")
	bundle.Config.Devices = nil
	archive := repack(t, bundle)

	before := f.currentDocument(t)
	rec := f.postBundle(t, archive, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}

	report := decodeReport(t, rec)
	if report.Applied {
		t.Error("an import with no ?apply=true reported itself as applied")
	}
	if report.Counts.DevicesDeleted != 2 {
		t.Errorf("the dry run planned %d deletions, want 2", report.Counts.DevicesDeleted)
	}
	if report.SafetyExport != "" {
		t.Errorf("a dry run named a safety export at %q", report.SafetyExport)
	}

	if after := f.currentDocument(t); !reflect.DeepEqual(after, before) {
		t.Error("a dry run changed the stored configuration")
	}
	if _, err := os.Stat(f.backupDir); !os.IsNotExist(err) {
		t.Error("a dry run created the safety export directory")
	}
	if calls := f.poller.calls(&f.poller.removed); len(calls) != 0 {
		t.Errorf("a dry run told the poller to remove %v", calls)
	}
}

func TestImportWithApplyReplacesTheConfiguration(t *testing.T) {
	f := newConfigFixture(t)
	f.seed(t)

	bundle, _ := f.exportBundle(t, "")
	bundle.Config.Devices = dropDevice(bundle.Config.Devices, "bastion")
	archive := repack(t, bundle)

	rec := f.postBundle(t, archive, "?apply=true")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}

	report := decodeReport(t, rec)
	if !report.Applied {
		t.Error("an applied import reported itself as a dry run")
	}
	if report.Counts.DevicesDeleted != 1 {
		t.Errorf("deleted %d devices, want 1", report.Counts.DevicesDeleted)
	}

	// Replace semantics: a device the file omits is gone.
	if _, err := f.records.GetDeviceByName(t.Context(), "bastion"); err == nil {
		t.Error("a device absent from the imported configuration survived")
	}

	if report.SafetyExport == "" {
		t.Fatal("an applied import did not say where the previous configuration was saved")
	}
	saved, err := os.ReadFile(report.SafetyExport)
	if err != nil {
		t.Fatalf("the safety export is not where the report says: %v", err)
	}
	previous, err := readConfigBundle(saved)
	if err != nil {
		t.Fatalf("the safety export is not a readable bundle: %v", err)
	}
	if len(previous.Config.Devices) != 2 {
		t.Errorf("the safety export holds %d devices, want the 2 that were replaced",
			len(previous.Config.Devices))
	}
}

func TestImportPushesTheResultToThePoller(t *testing.T) {
	f := newConfigFixture(t)
	f.seed(t)

	bundle, _ := f.exportBundle(t, "")
	bundle.Config.Devices = dropDevice(bundle.Config.Devices, "bastion")
	bundle.Config.Devices = append(bundle.Config.Devices, webuistorage.ConfigDevice{
		Name: "newswitch", Address: "192.0.2.9", SNMPVersion: "2c", Port: 161,
		MIBs: []webuistorage.ConfigAssignment{{
			Module:        powerModule,
			MIBAssignment: webuistorage.MIBAssignment{Enabled: true},
		}},
	})

	if rec := f.postBundle(t, repack(t, bundle), "?apply=true"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body %q)", rec.Code, rec.Body.String())
	}

	if got := f.poller.calls(&f.poller.removed); !reflect.DeepEqual(got, []string{"bastion"}) {
		t.Errorf("poller removals = %v, want [bastion]", got)
	}
	if got := f.poller.calls(&f.poller.registered); !reflect.DeepEqual(got, []string{"newswitch"}) {
		t.Errorf("poller registrations = %v, want [newswitch]", got)
	}
	// Every surviving device is pushed, not just the ones whose own fields
	// changed: a profile edit changes what a device polls without changing the
	// device record at all.
	if got := f.poller.calls(&f.poller.updated); !reflect.DeepEqual(got, []string{"core-01"}) {
		t.Errorf("poller updates = %v, want [core-01]", got)
	}
}

func TestImportRestoresOntoAnInstanceThatHasLostEverything(t *testing.T) {
	f := newConfigFixture(t)
	f.seed(t)

	original := f.currentDocument(t)
	_, archive := f.exportBundle(t, "")

	// Wipe the instance the way a fresh deployment starts: no configuration and
	// no MIB catalog. The bundle has to put both back on its own.
	if _, err := f.records.ImportConfig(t.Context(),
		&webuistorage.Config{SchemaVersion: webuistorage.ConfigSchemaVersion}); err != nil {
		t.Fatalf("wipe configuration: %v", err)
	}
	for _, module := range []string{sensorModule, powerModule} {
		record, err := f.records.GetMIBByName(t.Context(), module)
		if err != nil {
			t.Fatalf("GetMIBByName(%s): %v", module, err)
		}
		if err := f.records.DeleteMIB(t.Context(), record.ID); err != nil {
			t.Fatalf("DeleteMIB(%s): %v", module, err)
		}
	}

	rec := f.postBundle(t, archive, "?apply=true")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}

	report := decodeReport(t, rec)
	if report.Counts.MIBsImported != 2 {
		t.Errorf("imported %d modules, want 2 (the bundle had to supply both)",
			report.Counts.MIBsImported)
	}
	for _, change := range report.MIBs {
		if change.Action != MIBActionImport {
			t.Errorf("module %s action = %q, want %q", change.Module, change.Action, MIBActionImport)
		}
	}

	restored := f.currentDocument(t)
	if !reflect.DeepEqual(restored, original) {
		t.Errorf("the restored configuration differs from the exported one\n got: %+v\nwant: %+v",
			restored, original)
	}
}

func TestImportOfItsOwnExportIsANoOp(t *testing.T) {
	f := newConfigFixture(t)
	f.seed(t)

	_, archive := f.exportBundle(t, "")
	before := f.currentDocument(t)

	rec := f.postBundle(t, archive, "?apply=true")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body %q)", rec.Code, rec.Body.String())
	}

	report := decodeReport(t, rec)
	if report.Changed {
		t.Errorf("re-importing an instance's own export reported changes: %+v", report)
	}
	if len(report.Devices) != 0 || len(report.Profiles) != 0 {
		t.Errorf("the plan is not empty: %d device and %d profile changes",
			len(report.Devices), len(report.Profiles))
	}
	// The modules are already here, so they are refreshed in place rather than
	// imported, and every assignment pointing at them survives.
	if report.Counts.MIBsRefreshed != 2 {
		t.Errorf("refreshed %d modules, want 2", report.Counts.MIBsRefreshed)
	}

	after := f.currentDocument(t)
	if !reflect.DeepEqual(after, before) {
		t.Error("a no-op import changed the configuration")
	}
}

func TestImportRejectsAModuleNothingCanSupply(t *testing.T) {
	f := newConfigFixture(t)
	f.seed(t)

	// A configuration that assigns a module the bundle does not carry and this
	// instance has never imported. Dropping the assignment instead is how a
	// device ends up quietly polling less than it was configured to.
	document := &webuistorage.Config{
		SchemaVersion: webuistorage.ConfigSchemaVersion,
		Devices: []webuistorage.ConfigDevice{{
			Name: "core-01", Address: "192.0.2.252", SNMPVersion: "2c", Port: 161,
			MIBs: []webuistorage.ConfigAssignment{
				{Module: "CISCO-MEMORY-POOL-MIB", MIBAssignment: webuistorage.MIBAssignment{Enabled: true}},
				{Module: "ENTITY-MIB", MIBAssignment: webuistorage.MIBAssignment{Enabled: true}},
			},
		}},
	}

	before := f.currentDocument(t)
	rec := f.postJSON(t, document, "?apply=true")

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %q)", rec.Code, rec.Body.String())
	}
	body := decodeConfigError(t, rec)
	if body.Kind != ConfigErrorMissingMIBs {
		t.Errorf("kind = %q, want %q", body.Kind, ConfigErrorMissingMIBs)
	}
	want := []string{"CISCO-MEMORY-POOL-MIB", "ENTITY-MIB"}
	if !reflect.DeepEqual(body.MissingModules, want) {
		t.Errorf("missing_modules = %v, want %v", body.MissingModules, want)
	}
	for _, module := range want {
		if !strings.Contains(body.Message, module) {
			t.Errorf("the message does not name %s: %s", module, body.Message)
		}
	}
	if after := f.currentDocument(t); !reflect.DeepEqual(after, before) {
		t.Error("a rejected import changed the configuration")
	}
}

func TestImportRejectsBadDocuments(t *testing.T) {
	badInterval := 0
	tests := []struct {
		name       string
		mutate     func(doc *webuistorage.Config)
		wantStatus int
		wantKind   string
		wantField  string
	}{
		{
			name: "a filter that is not a regular expression",
			mutate: func(doc *webuistorage.Config) {
				doc.Profiles[0].MIBs[0].IncludeNames = []string{"^testSensor("}
			},
			wantStatus: http.StatusBadRequest,
			wantKind:   ConfigErrorValidation,
			wantField:  "mibs[" + sensorModule + "].include_names",
		},
		{
			name: "an SNMP version nothing can poll",
			mutate: func(doc *webuistorage.Config) {
				doc.Devices[0].SNMPVersion = "v4"
			},
			wantStatus: http.StatusBadRequest,
			wantKind:   ConfigErrorValidation,
			wantField:  "snmp_version",
		},
		{
			name: "a poll interval that is not a schedule",
			mutate: func(doc *webuistorage.Config) {
				doc.Devices[0].PollInterval = &badInterval
			},
			wantStatus: http.StatusBadRequest,
			wantKind:   ConfigErrorValidation,
			wantField:  "poll_interval",
		},
		{
			name: "a schema version this build cannot read",
			mutate: func(doc *webuistorage.Config) {
				doc.SchemaVersion = webuistorage.ConfigSchemaVersion + 1
			},
			wantStatus: http.StatusBadRequest,
			wantKind:   ConfigErrorSchema,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newConfigFixture(t)
			f.seed(t)

			bundle, _ := f.exportBundle(t, "")
			tt.mutate(bundle.Config)
			before := f.currentDocument(t)

			rec := f.postBundle(t, repack(t, bundle), "?apply=true")
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}

			body := decodeConfigError(t, rec)
			if body.Kind != tt.wantKind {
				t.Errorf("kind = %q, want %q", body.Kind, tt.wantKind)
			}
			if tt.wantField != "" && !hasReportedField(body.Problems, tt.wantField) {
				t.Errorf("problems %+v do not name field %q", body.Problems, tt.wantField)
			}
			if after := f.currentDocument(t); !reflect.DeepEqual(after, before) {
				t.Error("a rejected import changed the configuration")
			}
		})
	}
}

func TestImportRejectsUnreadableUploads(t *testing.T) {
	f := newConfigFixture(t)
	f.seed(t)

	bundle, _ := f.exportBundle(t, "")
	tampered := repackWith(t, bundle, func(files map[string][]byte) {
		for name := range files {
			files[name] = append(files[name], []byte("\n-- tampered\n")...)
		}
	})

	tests := []struct {
		name    string
		archive []byte
		query   string
	}{
		{"a body that is neither an archive nor a document", []byte("not a config"), ""},
		{"an empty body", nil, ""},
		{"a MIB source that does not match its checksum", tampered, "?apply=true"},
		{"an archive without config.json", archiveWithout(t, bundle), ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := f.currentDocument(t)
			rec := f.postBundle(t, tt.archive, tt.query)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %q)", rec.Code, rec.Body.String())
			}
			if kind := decodeConfigError(t, rec).Kind; kind != ConfigErrorBundle {
				t.Errorf("kind = %q, want %q", kind, ConfigErrorBundle)
			}
			if after := f.currentDocument(t); !reflect.DeepEqual(after, before) {
				t.Error("a rejected upload changed the configuration")
			}
		})
	}
}

func TestImportRejectsAnUnusableApplyFlag(t *testing.T) {
	f := newConfigFixture(t)
	f.seed(t)

	_, archive := f.exportBundle(t, "")
	rec := f.postBundle(t, archive, "?apply=perhaps")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if msg := decodeConfigError(t, rec).Message; !strings.Contains(msg, "apply") {
		t.Errorf("message %q does not name the parameter", msg)
	}
}

func TestImportAcceptsAMultipartUpload(t *testing.T) {
	f := newConfigFixture(t)
	f.seed(t)

	_, archive := f.exportBundle(t, "")

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "netmon-config.tar.gz")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(archive); err != nil {
		t.Fatalf("write upload: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/config/import", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	f.server.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if decodeReport(t, rec).Changed {
		t.Error("uploading the current configuration as a form reported changes")
	}
}

func TestImportRejectsAMultipartUploadWithNoFile(t *testing.T) {
	f := newConfigFixture(t)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("notafile", "x"); err != nil {
		t.Fatalf("WriteField: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/config/import", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	f.server.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %q)", rec.Code, rec.Body.String())
	}
}

func TestImportAcceptsABareDocumentWhenTheModulesAreAlreadyHere(t *testing.T) {
	f := newConfigFixture(t)
	f.seed(t)

	document := f.currentDocument(t)
	document.Devices = dropDevice(document.Devices, "bastion")

	rec := f.postJSON(t, document, "?apply=true")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}

	report := decodeReport(t, rec)
	if report.Counts.MIBsImported != 0 || report.Counts.MIBsRefreshed != 0 {
		t.Errorf("a bare document imported %d and refreshed %d modules, want none",
			report.Counts.MIBsImported, report.Counts.MIBsRefreshed)
	}
	if report.Counts.MIBsPresent != 2 {
		t.Errorf("reported %d modules already present, want 2", report.Counts.MIBsPresent)
	}
	if _, err := f.records.GetDeviceByName(t.Context(), "bastion"); err == nil {
		t.Error("a device absent from the imported document survived")
	}
}

func TestExportNamesModulesWhoseSourceIsGone(t *testing.T) {
	f := newConfigFixture(t)
	f.seed(t)

	// A catalog entry whose file has been removed underneath it. The export has
	// to say so rather than produce a bundle that quietly cannot restore.
	record, err := f.records.GetMIBByName(t.Context(), powerModule)
	if err != nil {
		t.Fatalf("GetMIBByName: %v", err)
	}
	if removeErr := os.Remove(record.FilePath); removeErr != nil {
		t.Fatalf("remove source: %v", removeErr)
	}

	rec := do(t, f.server, http.MethodGet, "/api/config/export", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("X-Netmon-Missing-MIB-Sources"); got != powerModule {
		t.Errorf("X-Netmon-Missing-MIB-Sources = %q, want %q", got, powerModule)
	}

	bundle, err := readConfigBundle(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("readConfigBundle: %v", err)
	}
	for _, entry := range bundle.Config.MIBs {
		if entry.Name == powerModule && entry.File != "" {
			t.Errorf("module %s claims to carry %q, which is not there", entry.Name, entry.File)
		}
	}
}

func TestConfigBackupDirDefaultsBesideTheRecordStore(t *testing.T) {
	s := &Server{dbPath: "/app/data/webui/webui.db"}
	if got := s.configBackupDir(); got != "/app/data/webui/config-backups" {
		t.Errorf("configBackupDir() = %q", got)
	}

	s.configBackups = "/elsewhere"
	if got := s.configBackupDir(); got != "/elsewhere" {
		t.Errorf("an explicit directory was ignored: %q", got)
	}
}

func TestConfigErrorStatusMapsEveryFailure(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantKind   string
	}{
		{
			"missing modules", &webuistorage.ConfigMIBMissingError{Modules: []string{"IF-MIB"}},
			http.StatusConflict, ConfigErrorMissingMIBs,
		},
		{
			"an unreadable schema", &webuistorage.ConfigSchemaError{Found: 2, Supported: 1},
			http.StatusBadRequest, ConfigErrorSchema,
		},
		{
			"invalid fields", &webuistorage.ConfigValidationError{
				Problems: []webuistorage.ConfigProblem{{Scope: "device", Reason: "bad"}},
			},
			http.StatusBadRequest, ConfigErrorValidation,
		},
		{
			"a MIB source that will not parse", &ConfigMIBImportError{
				Module: "X", Err: &MIBParseError{Stage: "file", Err: os.ErrInvalid},
			},
			http.StatusBadRequest, ConfigErrorMIBImport,
		},
		{
			"a server with no MIB parser", &ConfigMIBImportError{
				Module: "X", Err: &MIBLoaderError{Reason: "MIB loader not available"},
			},
			http.StatusInternalServerError, ConfigErrorStorage,
		},
		{
			"a safety export that could not be written", &ConfigSafetyExportError{
				Reason: "writing", Err: os.ErrPermission,
			},
			http.StatusInternalServerError, ConfigErrorSafety,
		},
		{
			"an archive that is not a bundle", &BundleFormatError{Reason: "no"},
			http.StatusBadRequest, ConfigErrorBundle,
		},
		{
			"a bad member", &BundleEntryError{Entry: "mibs/x", Reason: "no"},
			http.StatusBadRequest, ConfigErrorBundle,
		},
		{
			"a checksum mismatch", &BundleChecksumError{Module: "X", Entry: "mibs/x"},
			http.StatusBadRequest, ConfigErrorBundle,
		},
		{
			"anything else", os.ErrClosed,
			http.StatusInternalServerError, ConfigErrorStorage,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, kind := configErrorStatus(tt.err)
			if status != tt.wantStatus || kind != tt.wantKind {
				t.Errorf("configErrorStatus() = (%d, %q), want (%d, %q)",
					status, kind, tt.wantStatus, tt.wantKind)
			}
			if tt.err.Error() == "" {
				t.Error("the error has no message")
			}
		})
	}
}

func TestConfigServiceErrorsExplainThemselves(t *testing.T) {
	safety := &ConfigSafetyExportError{Reason: "writing /tmp/x", Err: os.ErrPermission}
	if !strings.Contains(safety.Error(), "nothing was changed") {
		t.Errorf("a failed safety export does not say the import stopped: %s", safety)
	}
	if !strings.Contains(safety.Error(), "/tmp/x") {
		t.Errorf("a failed safety export does not name the path: %s", safety)
	}

	mibErr := &ConfigMIBImportError{
		Module: "IF-MIB", Imported: []string{"A", "B"}, Err: os.ErrInvalid,
	}
	for _, fragment := range []string{"IF-MIB", "2 module(s)", "configuration was not changed"} {
		if !strings.Contains(mibErr.Error(), fragment) {
			t.Errorf("message %q does not contain %q", mibErr, fragment)
		}
	}
}

func TestBundleReaderRefusesDangerousArchives(t *testing.T) {
	f := newConfigFixture(t)
	f.seed(t)
	bundle, _ := f.exportBundle(t, "")

	tests := []struct {
		name    string
		archive []byte
	}{
		{"a member outside the layout", archiveWithEntry(t, bundle, "../escape.txt")},
		{"an absolute member", archiveWithEntry(t, bundle, "/etc/passwd")},
		{"a member in an unknown directory", archiveWithEntry(t, bundle, "secrets/key")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := readConfigBundle(tt.archive); err == nil {
				t.Fatal("readConfigBundle accepted an archive it should refuse")
			}
		})
	}
}

func TestBundleSourcesMustBeCarriedWhenNamed(t *testing.T) {
	bundle := &ConfigBundle{
		Config: &webuistorage.Config{
			SchemaVersion: webuistorage.ConfigSchemaVersion,
			MIBs:          []webuistorage.ConfigMIB{{Name: "IF-MIB", File: "mibs/IF-MIB.txt"}},
		},
		MIBFiles: map[string][]byte{},
	}

	_, err := verifyBundleSources(bundle)
	if err == nil {
		t.Fatal("a document naming a file the bundle does not carry was accepted")
	}
	if !strings.Contains(err.Error(), "IF-MIB") {
		t.Errorf("message %q does not name the module", err)
	}
}

// postJSON uploads a bare configuration document.
func (f *configFixture) postJSON(t *testing.T, doc *webuistorage.Config, query string) *httptest.ResponseRecorder {
	t.Helper()

	body, err := marshalConfig(doc)
	if err != nil {
		t.Fatalf("marshalConfig: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/config/import"+query, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.server.router.ServeHTTP(rec, req)
	return rec
}

// repack writes a bundle back out as an archive.
func repack(t *testing.T, bundle *ConfigBundle) []byte {
	t.Helper()
	return repackWith(t, bundle, nil)
}

// repackWith rewrites a bundle's files before packing it, which is how a test
// produces a damaged archive without hand-building one.
func repackWith(t *testing.T, bundle *ConfigBundle, edit func(files map[string][]byte)) []byte {
	t.Helper()

	files := make(map[string][]byte, len(bundle.MIBFiles))
	for name, content := range bundle.MIBFiles {
		files[name] = append([]byte(nil), content...)
	}
	if edit != nil {
		edit(files)
	}

	var archive bytes.Buffer
	if err := writeConfigBundle(&archive, &ConfigBundle{Config: bundle.Config, MIBFiles: files}); err != nil {
		t.Fatalf("writeConfigBundle: %v", err)
	}
	return archive.Bytes()
}

// archiveWithout packs a bundle's MIB files with no configuration document.
func archiveWithout(t *testing.T, bundle *ConfigBundle) []byte {
	t.Helper()

	var archive bytes.Buffer
	if err := writeConfigBundle(&archive, &ConfigBundle{
		Config: &webuistorage.Config{}, MIBFiles: bundle.MIBFiles,
	}); err != nil {
		t.Fatalf("writeConfigBundle: %v", err)
	}

	// Drop config.json by rewriting the archive with the document renamed to a
	// path the reader refuses.
	unpacked, err := readConfigBundle(archive.Bytes())
	if err != nil {
		t.Fatalf("readConfigBundle: %v", err)
	}
	return packRaw(t, unpacked.MIBFiles)
}

// archiveWithEntry packs a bundle with one extra member under a chosen name.
func archiveWithEntry(t *testing.T, bundle *ConfigBundle, name string) []byte {
	t.Helper()

	files := map[string][]byte{name: []byte("x")}
	for path, content := range bundle.MIBFiles {
		files[path] = content
	}
	return packRaw(t, files)
}

// packRaw writes an archive from exactly the members given, bypassing the
// bundle writer's own layout so a test can produce an archive the reader must
// refuse.
func packRaw(t *testing.T, files map[string][]byte) []byte {
	t.Helper()

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	var archive bytes.Buffer
	if err := writeRawArchive(&archive, names, files); err != nil {
		t.Fatalf("writeRawArchive: %v", err)
	}
	return archive.Bytes()
}

// dropDevice removes one device from a document.
func dropDevice(devices []webuistorage.ConfigDevice, name string) []webuistorage.ConfigDevice {
	kept := make([]webuistorage.ConfigDevice, 0, len(devices))
	for _, d := range devices {
		if d.Name != name {
			kept = append(kept, d)
		}
	}
	return kept
}

// hasReportedField reports whether any problem names the field.
func hasReportedField(problems []webuistorage.ConfigProblem, field string) bool {
	for _, p := range problems {
		if p.Field == field {
			return true
		}
	}
	return false
}

// writeRawArchive writes members verbatim, so a test can build an archive the
// bundle writer would never produce.
func writeRawArchive(w io.Writer, names []string, files map[string][]byte) error {
	gz := gzip.NewWriter(w)
	archive := tar.NewWriter(gz)

	for _, name := range names {
		header := &tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(files[name])),
			Typeflag: tar.TypeReg,
		}
		if err := archive.WriteHeader(header); err != nil {
			return err
		}
		if _, err := archive.Write(files[name]); err != nil {
			return err
		}
	}

	if err := archive.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func TestBundleReaderEnforcesItsLimits(t *testing.T) {
	document := []byte(`{"schema_version":1}`)

	tests := []struct {
		name    string
		build   func() []byte
		wantErr string
	}{
		{
			name: "a directory member is ignored rather than refused",
			build: func() []byte {
				return archiveWithTypes(t, []rawEntry{
					{name: bundleConfigEntry, content: document, kind: tar.TypeReg},
					{name: bundleMIBDir, kind: tar.TypeDir},
				})
			},
		},
		{
			name: "a symlink is refused",
			build: func() []byte {
				return archiveWithTypes(t, []rawEntry{
					{name: bundleConfigEntry, content: document, kind: tar.TypeReg},
					{name: "mibs/link", kind: tar.TypeSymlink},
				})
			},
			wantErr: "is not a regular file",
		},
		{
			name: "a file larger than the per-file cap is refused",
			build: func() []byte {
				return archiveWithTypes(t, []rawEntry{
					{name: bundleConfigEntry, content: document, kind: tar.TypeReg},
					{
						name:    "mibs/huge.my",
						content: bytes.Repeat([]byte("x"), maxBundleFileBytes+1),
						kind:    tar.TypeReg,
					},
				})
			},
			wantErr: "is larger than",
		},
		{
			name: "an archive with no document is refused",
			build: func() []byte {
				return archiveWithTypes(t, []rawEntry{
					{name: "mibs/IF-MIB.my", content: []byte("x"), kind: tar.TypeReg},
				})
			},
			wantErr: "does not contain " + bundleConfigEntry,
		},
		{
			name: "a document that is not JSON is refused",
			build: func() []byte {
				return archiveWithTypes(t, []rawEntry{
					{name: bundleConfigEntry, content: []byte("{nope"), kind: tar.TypeReg},
				})
			},
			wantErr: "not a JSON configuration document",
		},
		{
			name:    "something that is not gzip at all is refused",
			build:   func() []byte { return []byte{0x1f, 0x8b, 0x00, 0x00} },
			wantErr: "not a readable gzip archive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bundle, err := readConfigBundle(tt.build())
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("readConfigBundle: %v", err)
				}
				if len(bundle.MIBFiles) != 0 {
					t.Errorf("a directory entry became a file: %v", bundle.MIBFiles)
				}
				return
			}
			if err == nil {
				t.Fatal("readConfigBundle accepted an archive it should refuse")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestBundleReaderRefusesTooManyFiles(t *testing.T) {
	entries := []rawEntry{{
		name: bundleConfigEntry, content: []byte(`{"schema_version":1}`), kind: tar.TypeReg,
	}}
	for i := 0; i <= maxBundleEntries; i++ {
		entries = append(entries, rawEntry{
			name: "mibs/m" + strconv.Itoa(i) + ".my", content: []byte("x"), kind: tar.TypeReg,
		})
	}

	_, err := readConfigBundle(archiveWithTypes(t, entries))
	if err == nil {
		t.Fatal("an archive with more files than the limit was accepted")
	}
	if !strings.Contains(err.Error(), "more than") {
		t.Errorf("error %q does not explain the limit", err)
	}
}

func TestParseConfigUploadRejectsAnEmptyBody(t *testing.T) {
	if _, err := parseConfigUpload(nil); err == nil {
		t.Fatal("an empty upload was accepted")
	}
}

func TestReadMIBSourceResolvesTheModuleInTheDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "IF-MIB.txt"), []byte("IF-MIB DEFINITIONS"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}

	// The stored path names a container-era directory that is gone; only its
	// base name, and then the module name, decide which file is read.
	name, content, err := readMIBSource(dir, "IF-MIB", "/app/data/mibs/uploaded/IF-MIB.txt")
	if err != nil {
		t.Fatalf("readMIBSource: %v", err)
	}
	if name != "IF-MIB.txt" || string(content) != "IF-MIB DEFINITIONS" {
		t.Errorf("readMIBSource() = %q, %q, want the file in the directory", name, content)
	}

	// No stored path at all: the module name with a known suffix is found.
	if name, _, err = readMIBSource(dir, "IF-MIB", ""); err != nil || name != "IF-MIB.txt" {
		t.Errorf("readMIBSource() without a stored path = %q, %v, want IF-MIB.txt", name, err)
	}
}

func TestReadMIBSourceReportsAnAbsentFile(t *testing.T) {
	_, _, err := readMIBSource(t.TempDir(), "IF-MIB", "/app/data/mibs/uploaded/IF-MIB.txt")
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("readMIBSource() error = %v, want os.ErrNotExist", err)
	}
	var uploadErr *MIBUploadError
	if !errors.As(err, &uploadErr) {
		t.Errorf("readMIBSource() error = %T, want *MIBUploadError", err)
	}
}

func TestReadMIBSourceCannotLeaveTheDirectory(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "uploaded")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create directory: %v", err)
	}
	outside := filepath.Join(parent, "SECRET-MIB.my")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	// A module name that climbs out of the directory is never a candidate.
	if _, content, err := readMIBSource(dir, "../SECRET-MIB", ""); err == nil || content != nil {
		t.Errorf("readMIBSource(../SECRET-MIB) read %q, want an error", content)
	}
	if _, content, err := readMIBSource(dir, "SECRET-MIB", "../SECRET-MIB.my"); err == nil || content != nil {
		t.Errorf("readMIBSource() through the stored path read %q, want an error", content)
	}

	// A link planted in the directory is not followed out of it.
	if err := os.Symlink(outside, filepath.Join(dir, "LINK-MIB.my")); err != nil {
		t.Fatalf("plant link: %v", err)
	}
	if _, content, err := readMIBSource(dir, "LINK-MIB", ""); err == nil || content != nil {
		t.Errorf("readMIBSource() through a link read %q, want an error", content)
	}
}

func TestExportCarriesASourceWhoseStoredPathIsStale(t *testing.T) {
	f := newConfigFixture(t)
	f.seed(t)

	// Production records hold paths from the container era. The file is
	// still in the configured upload directory, so the export must carry it.
	record, err := f.records.GetMIBByName(t.Context(), powerModule)
	if err != nil {
		t.Fatalf("GetMIBByName: %v", err)
	}
	fileName := filepath.Base(record.FilePath)
	if _, err = f.records.ReplaceMIBContent(t.Context(), powerModule, webuistorage.MIBContent{
		FilePath:    filepath.Join("/app/data/mibs/uploaded", fileName),
		Description: record.Description,
		ObjectCount: record.ObjectCount,
	}); err != nil {
		t.Fatalf("ReplaceMIBContent: %v", err)
	}

	rec := do(t, f.server, http.MethodGet, "/api/config/export", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("X-Netmon-Missing-MIB-Sources"); got != "" {
		t.Errorf("X-Netmon-Missing-MIB-Sources = %q, want none", got)
	}
	bundle, err := readConfigBundle(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("readConfigBundle: %v", err)
	}
	if _, ok := bundle.MIBFiles[bundleMIBDir+"/"+fileName]; !ok {
		t.Errorf("bundle does not carry %s", fileName)
	}

	// The detail view shows the file export reads, not the stale record.
	detail := do(t, f.server, http.MethodGet, fmt.Sprintf("/api/mibs/%d", record.ID), "")
	if want := filepath.Join(f.server.uploadedMIBDir(), fileName); !strings.Contains(detail.Body.String(), want) {
		t.Errorf("MIB detail = %s, want file_path %s", detail.Body.String(), want)
	}
}

func TestMIBListShowsTheResolvedSourcePath(t *testing.T) {
	f := newConfigFixture(t)
	f.seed(t)

	// One record with a container-era path whose file is still in the upload
	// directory, and one whose file is nowhere.
	record, err := f.records.GetMIBByName(t.Context(), powerModule)
	if err != nil {
		t.Fatalf("GetMIBByName: %v", err)
	}
	fileName := filepath.Base(record.FilePath)
	if _, err = f.records.ReplaceMIBContent(t.Context(), powerModule, webuistorage.MIBContent{
		FilePath:    filepath.Join("/app/data/mibs/uploaded", fileName),
		Description: record.Description,
		ObjectCount: record.ObjectCount,
	}); err != nil {
		t.Fatalf("ReplaceMIBContent: %v", err)
	}
	const goneModule = "TEST-GONE-MIB"
	if err = f.records.CreateMIB(t.Context(), &webuistorage.MIB{
		Name:     goneModule,
		FilePath: "/app/data/mibs/uploaded/TEST-GONE-MIB.my",
	}); err != nil {
		t.Fatalf("CreateMIB: %v", err)
	}

	rec := do(t, f.server, http.MethodGet, "/api/mibs", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		MIBs []struct {
			Name     string `json:"name"`
			FilePath string `json:"file_path"`
		} `json:"mibs"`
	}
	if err = json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := make(map[string]string, len(body.MIBs))
	for _, m := range body.MIBs {
		got[m.Name] = m.FilePath
	}
	if want := filepath.Join(f.server.uploadedMIBDir(), fileName); got[powerModule] != want {
		t.Errorf("file_path of %s = %q, want %q", powerModule, got[powerModule], want)
	}
	if path, ok := got[goneModule]; !ok || path != "" {
		t.Errorf("file_path of %s = %q (listed %v), want listed with no path", goneModule, path, ok)
	}
}

func TestPlanModulesDemandsAModuleTheDocumentAssignsButDoesNotList(t *testing.T) {
	doc := &webuistorage.Config{
		SchemaVersion: webuistorage.ConfigSchemaVersion,
		Devices: []webuistorage.ConfigDevice{{
			Name: "d", Address: "192.0.2.1", SNMPVersion: "2c",
			MIBs: []webuistorage.ConfigAssignment{
				{Module: "IF-MIB", MIBAssignment: webuistorage.MIBAssignment{Enabled: true}},
				{Module: "ENTITY-MIB", MIBAssignment: webuistorage.MIBAssignment{Enabled: true}},
			},
		}},
	}

	changes, missing := planModules(doc, nil, map[string]struct{}{"IF-MIB": {}})

	if !reflect.DeepEqual(missing, []string{"ENTITY-MIB"}) {
		t.Errorf("missing = %v, want [ENTITY-MIB]", missing)
	}
	want := []MIBChange{
		{Module: "ENTITY-MIB", Action: MIBActionMissing},
		{Module: "IF-MIB", Action: MIBActionPresent},
	}
	if !reflect.DeepEqual(changes, want) {
		t.Errorf("changes = %+v, want %+v", changes, want)
	}
}

func TestPlanModulesIgnoresAnUnreferencedModuleNobodyHas(t *testing.T) {
	// A document exported from an instance with a large catalog lists modules
	// nothing assigns. Demanding those would make every restore onto a smaller
	// instance fail for no reason.
	doc := &webuistorage.Config{
		SchemaVersion: webuistorage.ConfigSchemaVersion,
		MIBs:          []webuistorage.ConfigMIB{{Name: "SPARE-MIB"}},
	}

	changes, missing := planModules(doc, nil, map[string]struct{}{})

	if len(missing) != 0 {
		t.Errorf("missing = %v, want none", missing)
	}
	if len(changes) != 0 {
		t.Errorf("changes = %+v, want none", changes)
	}
}

func TestApplyStopsWhenTheSafetyExportCannotBeWritten(t *testing.T) {
	f := newConfigFixture(t)
	f.seed(t)

	// A backup directory that cannot be created, because a file already sits
	// where it would go. Nothing may be changed when the previous state cannot
	// be saved first.
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	f.server.configBackups = filepath.Join(blocked, "config-backups")

	bundle, _ := f.exportBundle(t, "")
	bundle.Config.Devices = dropDevice(bundle.Config.Devices, "bastion")
	before := f.currentDocument(t)

	rec := f.postBundle(t, repack(t, bundle), "?apply=true")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body %q)", rec.Code, rec.Body.String())
	}
	if kind := decodeConfigError(t, rec).Kind; kind != ConfigErrorSafety {
		t.Errorf("kind = %q, want %q", kind, ConfigErrorSafety)
	}
	if after := f.currentDocument(t); !reflect.DeepEqual(after, before) {
		t.Error("an import whose safety export failed still changed the configuration")
	}
}

func TestSyncImportedDevicesWithoutAPollerDoesNothing(t *testing.T) {
	f := newConfigFixture(t)
	f.seed(t)
	f.server.pollerClient = nil

	// A deployment with no poller configured must still be able to import.
	bundle, _ := f.exportBundle(t, "")
	bundle.Config.Devices = dropDevice(bundle.Config.Devices, "bastion")

	rec := f.postBundle(t, repack(t, bundle), "?apply=true")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if _, err := f.records.GetDeviceByName(t.Context(), "bastion"); err == nil {
		t.Error("the import did not take effect")
	}
}

func TestImportSurvivesAPollerThatFails(t *testing.T) {
	f := newConfigFixture(t)
	f.seed(t)
	f.poller.err = errors.New("poller is down")

	bundle, _ := f.exportBundle(t, "")
	bundle.Config.Devices = dropDevice(bundle.Config.Devices, "bastion")

	// The configuration is already durable when the poller is told about it, so
	// a poller that is not answering must not turn a successful import into a
	// failed request.
	rec := f.postBundle(t, repack(t, bundle), "?apply=true")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if _, err := f.records.GetDeviceByName(t.Context(), "bastion"); err == nil {
		t.Error("the import did not take effect")
	}
}

func TestImportRejectsABundleCarryingAFileThatIsNotAMIB(t *testing.T) {
	f := newConfigFixture(t)
	f.seed(t)

	// A module this instance has never seen, carried as something that is not a
	// MIB at all. It has to fail the import outright: importing the rest and
	// leaving one device short of the modules it was configured with is the
	// silent under-collection this whole feature exists to prevent.
	garbage := []byte("this is not a MIB module\n")
	document := &webuistorage.Config{
		SchemaVersion: webuistorage.ConfigSchemaVersion,
		MIBs: []webuistorage.ConfigMIB{{
			Name: "TEST-BROKEN-MIB", File: "mibs/TEST-BROKEN-MIB.my", SHA256: checksum(garbage),
		}},
		Devices: []webuistorage.ConfigDevice{{
			Name: "core-01", Address: "192.0.2.252", SNMPVersion: "2c", Port: 161,
			MIBs: []webuistorage.ConfigAssignment{{
				Module:        "TEST-BROKEN-MIB",
				MIBAssignment: webuistorage.MIBAssignment{Enabled: true},
			}},
		}},
	}
	bundle := &ConfigBundle{
		Config:   document,
		MIBFiles: map[string][]byte{"mibs/TEST-BROKEN-MIB.my": garbage},
	}
	before := f.currentDocument(t)

	rec := f.postBundle(t, repack(t, bundle), "?apply=true")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %q)", rec.Code, rec.Body.String())
	}

	body := decodeConfigError(t, rec)
	if body.Kind != ConfigErrorMIBImport {
		t.Errorf("kind = %q, want %q", body.Kind, ConfigErrorMIBImport)
	}
	if !strings.Contains(body.Message, "TEST-BROKEN-MIB") {
		t.Errorf("message %q does not name the module", body.Message)
	}
	if !strings.Contains(body.Message, "configuration was not changed") {
		t.Errorf("message %q does not say the configuration was left alone", body.Message)
	}
	if after := f.currentDocument(t); !reflect.DeepEqual(after, before) {
		t.Error("a bundle with an unparseable MIB still changed the configuration")
	}
}

// rawEntry is one archive member a test builds by hand.
type rawEntry struct {
	name    string
	content []byte
	kind    byte
}

// archiveWithTypes writes members of chosen types, which the bundle writer never
// produces and the reader must therefore be tested against.
func archiveWithTypes(t *testing.T, entries []rawEntry) []byte {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	archive := tar.NewWriter(gz)

	for _, entry := range entries {
		header := &tar.Header{
			Name:     entry.name,
			Mode:     0o644,
			Size:     int64(len(entry.content)),
			Typeflag: entry.kind,
		}
		if entry.kind == tar.TypeSymlink {
			header.Size, header.Linkname = 0, "target"
		}
		if entry.kind == tar.TypeDir {
			header.Size = 0
		}
		if err := archive.WriteHeader(header); err != nil {
			t.Fatalf("WriteHeader(%s): %v", entry.name, err)
		}
		if header.Size > 0 {
			if _, err := archive.Write(entry.content); err != nil {
				t.Fatalf("Write(%s): %v", entry.name, err)
			}
		}
	}

	if err := archive.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}
