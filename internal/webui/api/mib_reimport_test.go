package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/mib/loader"
	mibstorage "github.com/jeremyhahn/go-netmon/internal/mib/storage"
	"github.com/jeremyhahn/go-netmon/internal/testsupport"
	webuistorage "github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// sensorMIB is a self-contained module whose first object carries a UNITS
// clause. Units is the metadata the re-import exists to repair: a module parsed
// before the parser read UNITS is stored without it, and nothing in the UI or
// the CLI can append a unit until the module is parsed again.
const sensorMIB = `TEST-SENSOR-MIB DEFINITIONS ::= BEGIN

IMPORTS
    MODULE-IDENTITY, OBJECT-TYPE, Integer32, enterprises
        FROM SNMPv2-SMI;

testSensorMIB MODULE-IDENTITY
    LAST-UPDATED "202401010000Z"
    ORGANIZATION "go-netmon tests"
    CONTACT-INFO  "go-netmon"
    DESCRIPTION   "Sensor readings, re-parsed by the re-import tests."
    REVISION      "202401010000Z"
    DESCRIPTION   "Initial revision."
    ::= { enterprises 99999 }

testSensorValueUpdateRate OBJECT-TYPE
    SYNTAX      Integer32
    UNITS       "milliseconds"
    MAX-ACCESS  read-only
    STATUS      current
    DESCRIPTION "How often the sensor value is refreshed."
    ::= { testSensorMIB 1 }

testSensorValue OBJECT-TYPE
    SYNTAX      Integer32
    MAX-ACCESS  read-only
    STATUS      current
    DESCRIPTION "The sensor reading."
    ::= { testSensorMIB 2 }

END
`

const (
	sensorModule        = "TEST-SENSOR-MIB"
	sensorFile          = "TEST-SENSOR-MIB.my"
	sensorUpdateRateOID = "1.3.6.1.4.1.99999.1"
	sensorRemovedOID    = "1.3.6.1.4.1.99999.99"
)

// reimportFixture is a server wired the way the running one is: a real MIB
// parser, a real object store and a real web UI store. Nothing about a
// re-import is worth asserting against a stand-in parser, because the whole
// operation is "parse this file again and keep everything else".
type reimportFixture struct {
	server      *Server
	records     *webuistorage.Storage
	objects     *mibstorage.Storage
	uploadedDir string
}

func newReimportFixture(t *testing.T) *reimportFixture {
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

	objects, ok := mibLoader.GetStorage().(*mibstorage.Storage)
	if !ok {
		t.Fatal("loader did not expose its object store")
	}

	return &reimportFixture{
		server: &Server{
			router:         mux.NewRouter(),
			storage:        records,
			mibLoader:      mibLoader,
			mibStoragePath: root,
		},
		records:     records,
		objects:     objects,
		uploadedDir: uploaded,
	}
}

// upload posts a MIB file, with query appended to the import path verbatim.
func (f *reimportFixture) upload(t *testing.T, fileName string, content []byte, query string) *httptest.ResponseRecorder {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write upload: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/mibs/import"+query, &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	w := httptest.NewRecorder()
	f.server.handleImportMIB(w, req)
	return w
}

// importedMIBResponse is the import endpoint's envelope.
type importedMIBResponse struct {
	MIB      webuistorage.MIB `json:"mib"`
	Replaced bool             `json:"replaced"`
	Message  string           `json:"message"`
}

func decodeImportResponse(t *testing.T, w *httptest.ResponseRecorder) importedMIBResponse {
	t.Helper()

	var got importedMIBResponse
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return got
}

// seedStaleModule reproduces the state a module is left in by an older parser:
// a record that exists and objects that are stored, but with the metadata the
// old parser never extracted missing, plus an object the module no longer
// defines.
func (f *reimportFixture) seedStaleModule(t *testing.T, module string) *webuistorage.MIB {
	t.Helper()

	ctx := t.Context()
	record := &webuistorage.MIB{
		Name:        module,
		FilePath:    filepath.Join(f.uploadedDir, sensorFile),
		Description: "stale description",
		ObjectCount: 1,
	}
	if err := f.records.CreateMIB(ctx, record); err != nil {
		t.Fatalf("seed MIB record: %v", err)
	}
	if err := f.records.UpdateMIBTags(ctx, record.ID, []string{"cisco", "sensors"}); err != nil {
		t.Fatalf("seed tags: %v", err)
	}

	stale := []*mib.OIDMetadata{
		{
			OID:      sensorUpdateRateOID,
			Name:     "testSensorValueUpdateRate",
			Module:   module,
			Type:     "Integer32",
			Pollable: true,
			// Units is empty: this is exactly the gap the re-import closes.
		},
		{
			OID:      sensorRemovedOID,
			Name:     "testSensorRetired",
			Module:   module,
			Type:     "Integer32",
			Pollable: true,
		},
	}
	for _, object := range stale {
		if err := f.objects.StoreOID(object); err != nil {
			t.Fatalf("seed object %s: %v", object.Name, err)
		}
	}
	// The loader writes a module record alongside the objects on every load, so
	// a module that is in the object store has one. It is what tells the next
	// load which objects to retire.
	if err := f.objects.StoreModule(&mib.ModuleInfo{
		Name:        module,
		Path:        filepath.Join(f.uploadedDir, sensorFile),
		Description: "stale description",
		OIDCount:    len(stale),
		LoadedAt:    time.Now(),
	}); err != nil {
		t.Fatalf("seed module record: %v", err)
	}

	return record
}

// TestReimportRefreshesObjectMetadata is the whole point of the feature: a
// module already in the database picks up metadata a newer parser extracts,
// without the record changing identity.
func TestReimportRefreshesObjectMetadata(t *testing.T) {
	f := newReimportFixture(t)
	seeded := f.seedStaleModule(t, sensorModule)

	w := f.upload(t, sensorFile, []byte(sensorMIB), "?replace=true")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200; body: %s", w.Code, w.Body.String())
	}

	got := decodeImportResponse(t, w)
	if !got.Replaced {
		t.Error("response does not report the module as replaced")
	}

	// The identity assignments are keyed by must survive.
	if got.MIB.ID != seeded.ID {
		t.Errorf("ID = %d, want the original %d", got.MIB.ID, seeded.ID)
	}
	if got.MIB.Name != sensorModule {
		t.Errorf("Name = %q, want %q", got.MIB.Name, sensorModule)
	}
	if len(got.MIB.Tags) != 2 {
		t.Errorf("Tags = %v, want the two the operator set", got.MIB.Tags)
	}

	// The parsed summary must have moved on.
	if got.MIB.ObjectCount <= 1 {
		t.Errorf("ObjectCount = %d, want the re-parsed count", got.MIB.ObjectCount)
	}
	if got.MIB.Description == "stale description" {
		t.Error("Description was not re-parsed")
	}
	if got.MIB.RefreshedAt == nil {
		t.Error("RefreshedAt is unset after a refresh")
	}

	// And the object metadata itself, which is what the GUI and CLI read.
	refreshed, err := f.objects.GetOID(sensorUpdateRateOID)
	if err != nil {
		t.Fatalf("read refreshed object: %v", err)
	}
	if refreshed.Units != "milliseconds" {
		t.Errorf("Units = %q, want %q from the UNITS clause", refreshed.Units, "milliseconds")
	}
	if refreshed.Type == "" {
		t.Error("Type was lost by the refresh")
	}
}

// TestReimportDropsObjectsTheModuleNoLongerDefines checks a retired OID does
// not linger. A stale object still looks pollable to the selector, so leaving
// it would keep an OID that no longer exists in the polling set.
func TestReimportDropsObjectsTheModuleNoLongerDefines(t *testing.T) {
	f := newReimportFixture(t)
	f.seedStaleModule(t, sensorModule)

	if _, err := f.objects.GetOID(sensorRemovedOID); err != nil {
		t.Fatalf("seeded object is not there to begin with: %v", err)
	}

	if w := f.upload(t, sensorFile, []byte(sensorMIB), "?replace=true"); w.Code != http.StatusOK {
		t.Fatalf("status %d, body: %s", w.Code, w.Body.String())
	}

	if _, err := f.objects.GetOID(sensorRemovedOID); err == nil {
		t.Error("an object the module no longer defines is still stored")
	}
}

// TestReimportKeepsDeviceAndProfileAssignments is the guarantee that makes this
// runnable against a live server. Deleting and re-importing would strip these.
func TestReimportKeepsDeviceAndProfileAssignments(t *testing.T) {
	ctx := t.Context()
	f := newReimportFixture(t)
	seeded := f.seedStaleModule(t, sensorModule)

	device := &webuistorage.Device{
		Name: "core-01", Address: "192.0.2.1", SNMPVersion: "v2c", Community: "public",
	}
	if err := f.records.CreateDevice(ctx, device); err != nil {
		t.Fatalf("create device: %v", err)
	}
	profile := &webuistorage.DeviceProfile{Name: "Core Routers"}
	if err := f.records.CreateDeviceProfile(ctx, profile); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	if err := f.records.AssignMIBToDevice(ctx, device.ID, seeded.ID); err != nil {
		t.Fatalf("assign to device: %v", err)
	}
	if err := f.records.AssignMIBToProfile(ctx, profile.ID, seeded.ID); err != nil {
		t.Fatalf("assign to profile: %v", err)
	}

	deviceAssignment := webuistorage.MIBAssignment{
		Enabled:      true,
		IncludeNames: []string{"testSensorValue"},
		ExcludeOIDs:  []string{"\\.99$"},
		MaxOIDs:      32,
	}
	if err := f.records.SetDeviceMIBAssignment(ctx, device.ID, seeded.ID, deviceAssignment); err != nil {
		t.Fatalf("set device assignment: %v", err)
	}
	profileAssignment := webuistorage.MIBAssignment{Enabled: false, MaxOIDs: 4}
	if err := f.records.SetProfileMIBAssignment(ctx, profile.ID, seeded.ID, profileAssignment); err != nil {
		t.Fatalf("set profile assignment: %v", err)
	}

	if w := f.upload(t, sensorFile, []byte(sensorMIB), "?replace=true"); w.Code != http.StatusOK {
		t.Fatalf("status %d, body: %s", w.Code, w.Body.String())
	}

	deviceMIBs, err := f.records.GetDeviceMIBs(ctx, device.ID)
	if err != nil {
		t.Fatalf("device MIBs: %v", err)
	}
	if len(deviceMIBs) != 1 || deviceMIBs[0].ID != seeded.ID {
		t.Fatalf("device MIBs = %+v, want the module still assigned", deviceMIBs)
	}

	profileMIBs, err := f.records.GetProfileMIBs(ctx, profile.ID)
	if err != nil {
		t.Fatalf("profile MIBs: %v", err)
	}
	if len(profileMIBs) != 1 || profileMIBs[0].ID != seeded.ID {
		t.Fatalf("profile MIBs = %+v, want the module still in the profile", profileMIBs)
	}

	gotDevice, err := f.records.DeviceMIBAssignment(ctx, device.ID, seeded.ID)
	if err != nil {
		t.Fatalf("device assignment: %v", err)
	}
	if !gotDevice.Enabled || gotDevice.MaxOIDs != 32 ||
		len(gotDevice.IncludeNames) != 1 || len(gotDevice.ExcludeOIDs) != 1 {
		t.Errorf("device assignment = %+v, want the filters that were set", gotDevice)
	}

	gotProfile, err := f.records.ProfileMIBAssignment(ctx, profile.ID, seeded.ID)
	if err != nil {
		t.Fatalf("profile assignment: %v", err)
	}
	if gotProfile.Enabled || gotProfile.MaxOIDs != 4 {
		t.Errorf("profile assignment = %+v, want it still disabled and capped", gotProfile)
	}
}

// TestImportExistingModuleConflicts checks the default path is a refusal that
// says what to do, not a 500 and not a silent overwrite.
func TestImportExistingModuleConflicts(t *testing.T) {
	f := newReimportFixture(t)
	seeded := f.seedStaleModule(t, sensorModule)

	w := f.upload(t, sensorFile, []byte(sensorMIB), "")
	if w.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409; body: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	for _, want := range []string{sensorModule, "replace=true", "netmon mib import --update"} {
		if !bytes.Contains([]byte(body), []byte(want)) {
			t.Errorf("conflict body is missing %q:\n%s", want, body)
		}
	}

	// A refused import changes nothing.
	stored, err := f.records.GetMIB(t.Context(), seeded.ID)
	if err != nil {
		t.Fatalf("get MIB: %v", err)
	}
	if stored.ObjectCount != 1 || stored.Description != "stale description" {
		t.Errorf("record = %+v, want it untouched by the refused import", stored)
	}
}

// TestReimportOfUnknownModuleImportsIt states the chosen behavior: refreshing
// a module the server has never seen creates it. A refresh run over a whole MIB
// tree must not fail on the one module that was never imported.
func TestReimportOfUnknownModuleImportsIt(t *testing.T) {
	f := newReimportFixture(t)

	w := f.upload(t, sensorFile, []byte(sensorMIB), "?replace=true")
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d, want 201; body: %s", w.Code, w.Body.String())
	}

	got := decodeImportResponse(t, w)
	if got.Replaced {
		t.Error("a module that did not exist was reported as replaced")
	}
	if got.MIB.ID == 0 || got.MIB.Name != sensorModule {
		t.Errorf("record = %+v, want a newly created %s", got.MIB, sensorModule)
	}
	if got.MIB.RefreshedAt != nil {
		t.Error("a first import must not claim to have refreshed anything")
	}
}

// TestReimportParseFailureLeavesStoredDataIntact is the safety property that
// matters most: a half-applied refresh on a live system is worse than a
// rejected one. The module here has never been parsed in this process, so the
// upload really does fail to parse.
func TestReimportParseFailureLeavesStoredDataIntact(t *testing.T) {
	ctx := t.Context()
	f := newReimportFixture(t)

	const (
		module   = "BROKEN-MIB"
		fileName = "BROKEN-MIB.my"
	)

	// The file the stored record points at, as it would be on a working server.
	goodFile := filepath.Join(f.uploadedDir, fileName)
	if err := os.WriteFile(goodFile, []byte(sensorMIB), 0o600); err != nil {
		t.Fatalf("seed uploaded file: %v", err)
	}

	record := &webuistorage.MIB{
		Name: module, FilePath: goodFile, Description: "good description", ObjectCount: 7,
	}
	if err := f.records.CreateMIB(ctx, record); err != nil {
		t.Fatalf("seed record: %v", err)
	}
	object := &mib.OIDMetadata{
		OID: "1.3.6.1.4.1.99998.1", Name: "brokenSensor", Module: module,
		Type: "Integer32", Units: "seconds", Pollable: true,
	}
	if err := f.objects.StoreOID(object); err != nil {
		t.Fatalf("seed object: %v", err)
	}

	w := f.upload(t, fileName, []byte("this is not a MIB module at all\n"), "?replace=true")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400; body: %s", w.Code, w.Body.String())
	}

	stored, err := f.records.GetMIB(ctx, record.ID)
	if err != nil {
		t.Fatalf("get MIB after a failed refresh: %v", err)
	}
	if stored.ObjectCount != 7 || stored.Description != "good description" {
		t.Errorf("record = %+v, want it unchanged by the failed refresh", stored)
	}
	if stored.RefreshedAt != nil {
		t.Error("a failed refresh must not stamp RefreshedAt")
	}

	survived, err := f.objects.GetOID(object.OID)
	if err != nil {
		t.Fatalf("the module's objects were destroyed by a failed refresh: %v", err)
	}
	if survived.Units != "seconds" {
		t.Errorf("Units = %q, want the stored %q", survived.Units, "seconds")
	}

	// The record still points at a file that parses.
	onDisk, err := os.ReadFile(goodFile)
	if err != nil {
		t.Fatalf("read the file the record points at: %v", err)
	}
	if string(onDisk) != sensorMIB {
		t.Error("the unparseable upload was left in place of the file that parsed")
	}
}

// TestImportParseFailureLeavesNoFileBehind covers the other arm of the
// rollback: an upload that displaced nothing is removed, so a rejected file
// cannot be picked up by a later search of the MIB directory.
func TestImportParseFailureLeavesNoFileBehind(t *testing.T) {
	f := newReimportFixture(t)

	w := f.upload(t, "NEVER-SEEN-MIB.my", []byte("not a MIB\n"), "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400; body: %s", w.Code, w.Body.String())
	}

	if _, err := os.Stat(filepath.Join(f.uploadedDir, "NEVER-SEEN-MIB.my")); !os.IsNotExist(err) {
		t.Errorf("rejected upload was left on disk (stat err = %v)", err)
	}
}

// TestImportThenReimportKeepsTheSameID walks the operator's actual sequence:
// import a module, then refresh it. The ID has to be stable across both.
func TestImportThenReimportKeepsTheSameID(t *testing.T) {
	f := newReimportFixture(t)

	first := f.upload(t, sensorFile, []byte(sensorMIB), "")
	if first.Code != http.StatusCreated {
		t.Fatalf("first import status %d, body: %s", first.Code, first.Body.String())
	}
	created := decodeImportResponse(t, first)

	second := f.upload(t, sensorFile, []byte(sensorMIB), "?replace=true")
	if second.Code != http.StatusOK {
		t.Fatalf("refresh status %d, body: %s", second.Code, second.Body.String())
	}
	refreshed := decodeImportResponse(t, second)

	if refreshed.MIB.ID != created.MIB.ID {
		t.Errorf("ID = %d after a refresh, want the original %d",
			refreshed.MIB.ID, created.MIB.ID)
	}
	if !refreshed.MIB.ImportedAt.Equal(created.MIB.ImportedAt) {
		t.Errorf("ImportedAt = %v, want the original %v",
			refreshed.MIB.ImportedAt, created.MIB.ImportedAt)
	}
	if refreshed.MIB.RefreshedAt == nil {
		t.Error("RefreshedAt is unset after a refresh")
	}
}

// failingLoader wraps a working loader and fails only the object reload, which
// is the one step of a refresh this package cannot otherwise provoke.
type failingLoader struct {
	MIBLoader
	err error
}

func (l *failingLoader) LoadModule(string) error          { return l.err }
func (l *failingLoader) EnsureModuleCurrent(string) error { return l.err }

// TestReimportLeavesRecordAloneWhenObjectReloadFails checks the ordering
// choice: the record is only rewritten once the objects it summarizes have
// actually been reloaded.
func TestReimportLeavesRecordAloneWhenObjectReloadFails(t *testing.T) {
	ctx := t.Context()
	f := newReimportFixture(t)
	seeded := f.seedStaleModule(t, sensorModule)

	f.server.mibLoader = &failingLoader{
		MIBLoader: f.server.mibLoader,
		err:       &MIBStorageError{Reason: "object store is read-only"},
	}

	w := f.upload(t, sensorFile, []byte(sensorMIB), "?replace=true")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500; body: %s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("left unchanged")) {
		t.Errorf("body does not say the module was left alone:\n%s", w.Body.String())
	}

	stored, err := f.records.GetMIB(ctx, seeded.ID)
	if err != nil {
		t.Fatalf("get MIB: %v", err)
	}
	if stored.ObjectCount != 1 || stored.RefreshedAt != nil {
		t.Errorf("record = %+v, want it untouched when the reload failed", stored)
	}
}

// TestImportRejectsAnUnparseableReplaceFlag covers the bad-input arm of the
// opt-in: a typo must be refused rather than quietly read as "do not replace",
// which would turn into a 409 the caller cannot explain.
func TestImportRejectsAnUnparseableReplaceFlag(t *testing.T) {
	f := newReimportFixture(t)

	w := f.upload(t, sensorFile, []byte(sensorMIB), "?replace=yes-please")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400; body: %s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("replace")) {
		t.Errorf("body does not name the offending field:\n%s", w.Body.String())
	}
}

// TestImportRejectsAnUploadWithoutAName covers a multipart part whose filename
// carries no usable base, which would otherwise be written as the upload
// directory itself.
func TestImportRejectsAnUploadWithoutAName(t *testing.T) {
	f := newReimportFixture(t)

	if w := f.upload(t, "", []byte(sensorMIB), ""); w.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestReplaceRequested(t *testing.T) {
	tests := []struct {
		name    string
		target  string
		want    bool
		wantErr bool
	}{
		{name: "absent", target: "/api/mibs/import"},
		{name: "true", target: "/api/mibs/import?replace=true", want: true},
		{name: "1", target: "/api/mibs/import?replace=1", want: true},
		{name: "false", target: "/api/mibs/import?replace=false"},
		{name: "not a bool", target: "/api/mibs/import?replace=maybe", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := replaceRequested(httptest.NewRequest(http.MethodPost, tt.target, nil))
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("replaceRequested = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseTagList(t *testing.T) {
	tests := []struct {
		name  string
		field string
		want  []string
	}{
		{name: "empty", field: ""},
		{name: "one", field: "cisco", want: []string{"cisco"}},
		{name: "trimmed", field: " cisco , switch ", want: []string{"cisco", "switch"}},
		{name: "blanks dropped", field: ",,cisco,, ,", want: []string{"cisco"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseTagList(tt.field)
			if len(got) != len(tt.want) {
				t.Fatalf("parseTagList(%q) = %v, want %v", tt.field, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("tag %d = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestImportSucceedsWhenTheObjectReloadFailsForANewModule states the other half
// of the ordering choice. A first import is not a refresh: the record is worth
// keeping even if the loader could not take the objects, because the loader
// retries at startup and on demand. Failing the import instead would leave the
// operator with a file on disk and nothing in the catalog.
func TestImportSucceedsWhenTheObjectReloadFailsForANewModule(t *testing.T) {
	f := newReimportFixture(t)
	f.server.mibLoader = &failingLoader{
		MIBLoader: f.server.mibLoader,
		err:       &MIBStorageError{Reason: "object store is read-only"},
	}

	w := f.upload(t, sensorFile, []byte(sensorMIB), "")
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d, want 201; body: %s", w.Code, w.Body.String())
	}

	stored, err := f.records.GetMIBByName(t.Context(), sensorModule)
	if err != nil {
		t.Fatalf("the module was not cataloged: %v", err)
	}
	if stored.Name != sensorModule {
		t.Errorf("stored %+v, want %s", stored, sensorModule)
	}
}

// TestImportReportsAnUnusableMIBDirectory covers a misconfigured storage path —
// here the upload directory's place is taken by a file — which must be a server
// error naming nothing about the filesystem to the client.
func TestImportReportsAnUnusableMIBDirectory(t *testing.T) {
	f := newReimportFixture(t)

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "uploaded"), []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("occupy the upload path: %v", err)
	}
	f.server.mibStoragePath = root

	w := f.upload(t, sensorFile, []byte(sensorMIB), "")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500; body: %s", w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte(root)) {
		t.Errorf("the response leaks a server path:\n%s", w.Body.String())
	}
}

// TestWriteImportError checks each failure is reported with the status a client
// can act on: a conflict is the caller's to resolve, everything else is ours.
func TestWriteImportError(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{
			name:       "already imported",
			err:        &webuistorage.MIBExistsError{Name: "IF-MIB", ID: 3},
			wantStatus: http.StatusConflict,
			wantBody:   "netmon mib import --update",
		},
		{
			name:       "object reload failed",
			err:        &MIBRefreshError{Module: "IF-MIB", Err: errors.New("store closed")},
			wantStatus: http.StatusInternalServerError,
			wantBody:   "left unchanged",
		},
		{
			name:       "anything else",
			err:        errors.New("disk gone"),
			wantStatus: http.StatusInternalServerError,
			wantBody:   "disk gone",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeImportError(w, tt.err)

			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			if !bytes.Contains(w.Body.Bytes(), []byte(tt.wantBody)) {
				t.Errorf("body %q is missing %q", w.Body.String(), tt.wantBody)
			}
		})
	}
}

// TestRefreshModuleObjectsWithoutALoader covers the precondition: a server with
// no MIB loader reports it rather than dereferencing one.
func TestRefreshModuleObjectsWithoutALoader(t *testing.T) {
	server := &Server{}

	err := server.refreshModuleObjects("IF-MIB")

	var storageErr *MIBStorageError
	if !errors.As(err, &storageErr) {
		t.Fatalf("error = %v, want a *MIBStorageError", err)
	}
}

// TestMIBImportErrorMessages checks the import errors name what went wrong and
// keep their cause reachable; these strings are what an operator sees.
func TestMIBImportErrorMessages(t *testing.T) {
	cause := errors.New("permission denied")

	upload := &MIBUploadError{Op: "create", Path: "/mibs/uploaded/IF-MIB.my", Err: cause}
	for _, want := range []string{"create", "IF-MIB.my", "permission denied"} {
		if !strings.Contains(upload.Error(), want) {
			t.Errorf("upload message %q is missing %q", upload, want)
		}
	}
	if !errors.Is(upload, cause) {
		t.Error("MIBUploadError does not unwrap to its cause")
	}

	refresh := &MIBRefreshError{Module: "IF-MIB", Err: cause}
	if !strings.Contains(refresh.Error(), "IF-MIB") || !errors.Is(refresh, cause) {
		t.Errorf("refresh message %q does not name the module and its cause", refresh)
	}

	request := &MIBImportRequestError{Field: "replace", Value: "maybe"}
	for _, want := range []string{"replace", "maybe", "true or false"} {
		if !strings.Contains(request.Error(), want) {
			t.Errorf("request message %q is missing %q", request, want)
		}
	}
}

// TestUploadRollbackRestoresWhatItDisplaced exercises the rollback directly,
// including the arm where there was nothing to put back.
func TestUploadRollbackRestoresWhatItDisplaced(t *testing.T) {
	dir := t.TempDir()

	t.Run("restores previous content", func(t *testing.T) {
		path := filepath.Join(dir, "existing.my")
		if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
			t.Fatalf("seed: %v", err)
		}
		upload := &mibUpload{
			root: uploadRoot(t, dir), name: "existing.my", path: path,
			previous: []byte("original"), existed: true,
		}

		if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
			t.Fatalf("overwrite: %v", err)
		}
		if err := upload.restore(); err != nil {
			t.Fatalf("restore: %v", err)
		}

		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if string(got) != "original" {
			t.Errorf("content = %q, want the displaced original", got)
		}
	})

	t.Run("removes a file that displaced nothing", func(t *testing.T) {
		path := filepath.Join(dir, "fresh.my")
		if err := os.WriteFile(path, []byte("rejected"), 0o600); err != nil {
			t.Fatalf("seed: %v", err)
		}
		upload := &mibUpload{root: uploadRoot(t, dir), name: "fresh.my", path: path}

		if err := upload.restore(); err != nil {
			t.Fatalf("restore: %v", err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("file still present (stat err = %v)", err)
		}

		// Rolling back twice must not turn "already gone" into a failure.
		if err := upload.restore(); err != nil {
			t.Errorf("second restore: %v", err)
		}
	})

	t.Run("reports a restore it cannot perform", func(t *testing.T) {
		// A name under a directory that does not exist inside the root. The
		// kernel refuses it, which is the same class of refusal an escaping
		// name gets, so the caller still sees a typed error rather than a
		// silent no-op.
		upload := &mibUpload{
			root:     uploadRoot(t, dir),
			name:     "no-such-directory/x.my",
			path:     filepath.Join(dir, "no-such-directory", "x.my"),
			previous: []byte("original"),
			existed:  true,
		}

		var uploadErr *MIBUploadError
		if err := upload.restore(); !errors.As(err, &uploadErr) {
			t.Fatalf("error = %v, want a *MIBUploadError", err)
		}
	})
}

// uploadRoot opens a directory the way saveUploadedMIB does, so a rollback in a
// test is resolved by the kernel against that directory exactly as it is in the
// server.
func uploadRoot(t *testing.T, dir string) *os.Root {
	t.Helper()

	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("open the upload directory: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

// TestUploadCannotWriteThroughASymlink is the reason the upload directory is
// held open as an os.Root rather than addressed by path.
//
// filepath.Base stops "../" and was the whole of the previous defense, with
// #nosec annotations asserting it was enough. It is not: a base name is still
// followed through a symbolic link, so a link planted in the upload directory
// would have had os.Create write through it to wherever it pointed. Resolving
// the name against the open directory makes the kernel refuse it.
//
// The test asserts on the target being untouched as well as on the error. An
// implementation that wrote the file and then complained would satisfy an
// error-only check.
func TestUploadCannotWriteThroughASymlink(t *testing.T) {
	uploadDir := t.TempDir()
	outsideDir := t.TempDir()

	target := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(target, []byte("do not touch"), 0o600); err != nil {
		t.Fatalf("seed the target: %v", err)
	}

	// os.Symlink, not the root's: creating an escaping link is what the root
	// refuses, and building the hostile condition has to go around it.
	link := filepath.Join(uploadDir, "planted.my")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("plant the symlink: %v", err)
	}

	root := uploadRoot(t, uploadDir)

	if _, err := root.Create("planted.my"); err == nil {
		t.Error("creating a file through an escaping symlink was allowed")
	}
	if err := root.WriteFile("planted.my", []byte("overwritten"), 0o644); err == nil {
		t.Error("writing through an escaping symlink was allowed")
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("the file outside the upload directory is gone: %v", err)
	}
	if string(got) != "do not touch" {
		t.Errorf("the file outside the upload directory was modified: %q", got)
	}
}

// TestUploadRefusesATraversingName covers the case filepath.Base already
// handled, so removing that reduction cannot pass unnoticed.
func TestUploadRefusesATraversingName(t *testing.T) {
	uploadDir := t.TempDir()
	root := uploadRoot(t, uploadDir)

	for _, name := range []string{"../escape.my", "sub/../../escape.my", "/etc/passwd"} {
		if _, err := root.Create(name); err == nil {
			t.Errorf("Create(%q) was allowed", name)
		}
	}
}
