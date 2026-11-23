package backup

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The logical export is the only reviewable record of what a deployment held.
// These tests drive it against a real HTTP server rather than a stub, because
// the parts worth covering are the walk over relationships and what happens
// when the web UI answers badly — neither of which a stub would exercise.

// webUI serves the endpoints the export reads.
type webUI struct {
	devices  string
	profiles string
	mibs     string

	// fail, when set, is the path prefix that answers 500.
	fail string
	// missing, when set, is the path prefix that answers 404.
	missing

	requests []string
}

type missing = string

func (w *webUI) handler(t *testing.T) http.Handler {
	t.Helper()

	mux := http.NewServeMux()
	respond := func(body string) http.HandlerFunc {
		return func(rw http.ResponseWriter, r *http.Request) {
			w.requests = append(w.requests, r.URL.Path)
			if w.fail != "" && strings.HasPrefix(r.URL.Path, w.fail) {
				rw.WriteHeader(http.StatusInternalServerError)
				return
			}
			if w.missing != "" && strings.HasPrefix(r.URL.Path, w.missing) {
				rw.WriteHeader(http.StatusNotFound)
				return
			}
			rw.Header().Set("Content-Type", "application/json")
			_, _ = rw.Write([]byte(body))
		}
	}

	mux.Handle("/api/devices", respond(w.devices))
	mux.Handle("/api/profiles", respond(w.profiles))
	mux.Handle("/api/mibs", respond(w.mibs))
	mux.Handle("/api/poller/config", respond(`{"default_poll_interval":30}`))
	mux.Handle("/api/devices/1/mibs", respond(`{"direct_mibs":[{"id":7}]}`))
	mux.Handle("/api/devices/1/profiles", respond(`{"profiles":[{"id":3}]}`))
	mux.Handle("/api/devices/1/mibs/7/assignment", respond(`{"enabled":true,"oid_filters":["1.3.6.1.2.1.2"]}`))
	// A second device, for the tests that check the walk covers more than the
	// first one. It has no profiles, which 404s and is an absence rather than a
	// failure.
	mux.Handle("/api/devices/2/mibs", respond(`{"direct_mibs":[{"id":7}]}`))
	mux.Handle("/api/devices/2/mibs/7/assignment", respond(`{"enabled":true}`))
	mux.Handle("/api/profiles/3/mibs", respond(`{"mibs":[{"id":7}]}`))
	mux.Handle("/api/profiles/3/mibs/7/assignment", respond(`{"enabled":false}`))
	return mux
}

// fullWebUI is a deployment with one device, one profile and one MIB, wired to
// each other the way a real one is.
func fullWebUI() *webUI {
	return &webUI{
		devices:  `{"devices":[{"id":1,"name":"core-01","community":"public"}]}`,
		profiles: `{"profiles":[{"id":3,"name":"cisco-router"}]}`,
		mibs:     `{"mibs":[{"id":7,"name":"IF-MIB"}]}`,
	}
}

// exportInto runs a logical export against a server and returns the result with
// the root it wrote into.
func exportInto(t *testing.T, ui *webUI) (LogicalInfo, *Root) {
	t.Helper()

	server := httptest.NewServer(ui.handler(t))
	t.Cleanup(server.Close)

	dst, err := Ensure("backup destination", t.TempDir())
	if err != nil {
		t.Fatalf("open destination: %v", err)
	}
	t.Cleanup(func() { _ = dst.Close() })

	svc := &Service{cfg: Config{WebUIURL: server.URL, Out: testWriter{t}}}
	return svc.exportLogical(t.Context(), dst), dst
}

// testWriter routes the service's operator output into the test log.
type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Logf("%s", strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// TestLogicalExportWritesEveryRecord is the working case: all six files exist
// and the relationships are walked, not merely the top-level collections.
func TestLogicalExportWritesEveryRecord(t *testing.T) {
	t.Parallel()

	info, dst := exportInto(t, fullWebUI())

	if info.Status != logicalOK {
		t.Fatalf("status = %q, want %q", info.Status, logicalOK)
	}
	want := map[string]int{"devices": 1, "profiles": 1, "mibs": 1}
	for key, n := range want {
		if info.Counts[key] != n {
			t.Errorf("counts[%q] = %d, want %d", key, info.Counts[key], n)
		}
	}

	for _, name := range []string{
		"devices.json", "profiles.json", "mibs.json",
		"poller-config.json", "device-mibs.json", "profile-mibs.json",
	} {
		if ok, err := dst.Exists(logicalDir + "/" + name); err != nil || !ok {
			t.Errorf("logical/%s was not written (%v)", name, err)
		}
	}
}

// TestLogicalExportCapturesAssignmentRecords is the reason the export walks per
// device instead of just saving the collections: enablement and OID filters
// live only in the assignment record, and a copy without them cannot describe
// what the deployment was actually polling.
func TestLogicalExportCapturesAssignmentRecords(t *testing.T) {
	t.Parallel()

	_, dst := exportInto(t, fullWebUI())

	data, err := dst.ReadFile(logicalDir + "/device-mibs.json")
	if err != nil {
		t.Fatalf("read device-mibs.json: %v", err)
	}
	var devices []deviceExport
	if err := json.Unmarshal(data, &devices); err != nil {
		t.Fatalf("decode device-mibs.json: %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("device-mibs.json holds %d devices, want 1", len(devices))
	}
	if len(devices[0].DirectAssignments) != 1 {
		t.Fatalf("device 1 has %d assignments, want 1", len(devices[0].DirectAssignments))
	}

	assignment := string(devices[0].DirectAssignments[0].Assignment)
	if !strings.Contains(assignment, "oid_filters") {
		t.Errorf("the assignment record lost its OID filters: %s", assignment)
	}
	if devices[0].DirectAssignments[0].MIBID != 7 {
		t.Errorf("assignment mib_id = %d, want 7", devices[0].DirectAssignments[0].MIBID)
	}
	// Decoded rather than string-matched: the export re-indents what it stores,
	// so asserting on the spelling would test the formatter, not the content.
	var profiles struct {
		Profiles []idOnly `json:"profiles"`
	}
	if err := json.Unmarshal(devices[0].Profiles, &profiles); err != nil {
		t.Fatalf("decode the device's profiles: %v", err)
	}
	if len(profiles.Profiles) != 1 || profiles.Profiles[0].ID != 3 {
		t.Errorf("the device's profiles were not captured: %s", devices[0].Profiles)
	}
}

// TestLogicalExportSurvivesAnUnreachableWebUI is the bad-input case that matters
// most: the export is a convenience and the physical backup is the point, so a
// web UI that answers 500 must degrade to a recorded status, never fail a
// backup that was about to stop the stack.
func TestLogicalExportSurvivesAnUnreachableWebUI(t *testing.T) {
	t.Parallel()

	ui := fullWebUI()
	ui.fail = "/api/devices"

	info, dst := exportInto(t, ui)

	if info.Status != logicalUnavailable {
		t.Errorf("status = %q, want %q", info.Status, logicalUnavailable)
	}
	if len(info.Counts) != 0 {
		t.Errorf("counts = %v, want none: nothing was successfully read", info.Counts)
	}
	if ok, _ := dst.Exists(logicalDir + "/device-mibs.json"); ok {
		t.Error("a failed export left a device-mibs.json that describes nothing")
	}
}

// TestLogicalExportRecordsAnAbsentResource covers the difference between "this
// device has no profiles" and "the request failed". A 404 is an answer.
func TestLogicalExportRecordsAnAbsentResource(t *testing.T) {
	t.Parallel()

	ui := fullWebUI()
	ui.missing = "/api/devices/1/profiles"

	info, dst := exportInto(t, ui)
	if info.Status != logicalOK {
		t.Fatalf("status = %q, want %q: a 404 is an absence, not a failure", info.Status, logicalOK)
	}

	data, err := dst.ReadFile(logicalDir + "/device-mibs.json")
	if err != nil {
		t.Fatalf("read device-mibs.json: %v", err)
	}
	var devices []deviceExport
	if err := json.Unmarshal(data, &devices); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := strings.TrimSpace(string(devices[0].Profiles)); got != "null" {
		t.Errorf("profiles = %s, want null for a device that has none", got)
	}
}

// TestLogicalExportIsSkippedWithoutAWebUI pins that an unset URL is recorded as
// skipped rather than silently producing an empty export that looks complete.
func TestLogicalExportIsSkippedWithoutAWebUI(t *testing.T) {
	t.Parallel()

	dst, err := Ensure("backup destination", t.TempDir())
	if err != nil {
		t.Fatalf("open destination: %v", err)
	}
	t.Cleanup(func() { _ = dst.Close() })

	svc := &Service{cfg: Config{Out: testWriter{t}}}
	info := svc.exportLogical(t.Context(), dst)

	if info.Status != logicalSkipped {
		t.Errorf("status = %q, want %q", info.Status, logicalSkipped)
	}
	if ok, _ := dst.Exists(logicalDir); ok {
		t.Error("a skipped export created a logical/ directory")
	}
}

// TestLogicalExportReportsAFailedRelationshipWalk covers the per-device and
// per-profile walks.
//
// These are where the state that exists nowhere else lives: which MIBs a device
// is assigned, whether each is enabled, and the OID filters on it. A collection
// endpoint that answers while the relationship endpoints do not would otherwise
// produce an export that lists every device and knows nothing about any of
// them -- which reads as a fleet with no MIB assignments at all.
func TestLogicalExportReportsAFailedRelationshipWalk(t *testing.T) {
	tests := []struct {
		name string
		fail string
	}{
		{"a device's MIB list", "/api/devices/1/mibs"},
		{"a device's assignment record", "/api/devices/1/mibs/7/assignment"},
		{"a profile's MIB list", "/api/profiles/3/mibs"},
		{"a profile's assignment record", "/api/profiles/3/mibs/7/assignment"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ui := fullWebUI()
			ui.fail = tt.fail

			info, _ := exportInto(t, ui)

			if info.Status != logicalUnavailable {
				t.Errorf("status = %q, want %q: a walk that could not complete must not "+
					"be recorded as a good export", info.Status, logicalUnavailable)
			}
		})
	}
}

// TestLogicalExportReportsAMalformedCollection covers the decode. A body that
// is not the shape expected cannot be walked, and guessing at it would produce
// an export that silently covers nothing.
func TestLogicalExportReportsAMalformedCollection(t *testing.T) {
	ui := fullWebUI()
	ui.devices = `{"devices": "not-a-list"}`

	info, _ := exportInto(t, ui)

	if info.Status != logicalUnavailable {
		t.Errorf("status = %q, want %q for a collection that could not be decoded",
			info.Status, logicalUnavailable)
	}
}

// TestLogicalExportWalksEveryDevice guards the loop rather than the first
// iteration: an export that stops after device one is the failure this is most
// likely to have, and it looks like a success.
func TestLogicalExportWalksEveryDevice(t *testing.T) {
	ui := fullWebUI()
	ui.devices = `{"devices":[{"id":1,"name":"core-01"},{"id":2,"name":"core-02"}]}`

	info, dst := exportInto(t, ui)
	if info.Status != logicalOK {
		t.Fatalf("status = %q, want %q", info.Status, logicalOK)
	}
	if info.Counts["devices"] != 2 {
		t.Errorf("counted %d devices, want 2", info.Counts["devices"])
	}

	// Device 2 has no profiles, which answers 404 -- an absence the walk
	// tolerates. What it must not do is drop the device.
	body, err := dst.ReadFile(logicalDir + "/device-mibs.json")
	if err != nil {
		t.Fatalf("read device-mibs.json: %v", err)
	}
	var exported []deviceExport
	if unmarshalErr := json.Unmarshal(body, &exported); unmarshalErr != nil {
		t.Fatalf("decode device-mibs.json: %v", unmarshalErr)
	}
	if len(exported) != 2 {
		t.Errorf("device-mibs.json holds %d devices, want both: an export that stops "+
			"after the first device still reports success", len(exported))
	}
}
