package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/webui/client"
	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// recordingPoller is a PollerClient that records what it was told and can be
// made to stall, which is how the tests below check that a slow poller cannot
// hold a request open past its budget.
type recordingPoller struct {
	mu         sync.Mutex
	registered []string
	updated    []string
	removed    []string
	modules    [][]string

	// filters is what each call carried alongside the module list. The poller
	// applies these when it builds a worker; the web UI used to push only the
	// names, so a filter never reached the thing choosing the OIDs.
	filters []map[string]config.ModuleFilter

	// stall blocks every call until the call's context is done, standing in
	// for a poller that is not answering.
	stall bool
	err   error

	// onRemove runs inside RemoveDevice, before it returns. It is how a test
	// observes what storage held at the moment the poller was asked, which is
	// the only way to check the ORDER of a delete rather than its outcome.
	// Set at construction and never written again, so it needs no lock.
	onRemove func()
}

func (p *recordingPoller) record(
	list *[]string, name string, mibNames []string, filters map[string]config.ModuleFilter,
) {
	p.mu.Lock()
	defer p.mu.Unlock()
	*list = append(*list, name)
	p.modules = append(p.modules, mibNames)
	p.filters = append(p.filters, filters)
}

func (p *recordingPoller) blockIfStalled(ctx context.Context) error {
	p.mu.Lock()
	stall := p.stall
	p.mu.Unlock()
	if !stall {
		return p.err
	}
	<-ctx.Done()
	return ctx.Err()
}

func (p *recordingPoller) RegisterDevice(
	ctx context.Context, d *storage.Device,
	mibNames []string, filters map[string]config.ModuleFilter,
) error {
	p.record(&p.registered, d.Name, mibNames, filters)
	return p.blockIfStalled(ctx)
}

func (p *recordingPoller) UpdateDevice(
	ctx context.Context, d *storage.Device,
	mibNames []string, filters map[string]config.ModuleFilter,
) error {
	p.record(&p.updated, d.Name, mibNames, filters)
	return p.blockIfStalled(ctx)
}

func (p *recordingPoller) RemoveDevice(ctx context.Context, name string) error {
	p.record(&p.removed, name, nil, nil)
	if p.onRemove != nil {
		p.onRemove()
	}
	return p.blockIfStalled(ctx)
}

func (p *recordingPoller) GetDeviceStatus(_ context.Context, name string) (*client.DeviceStatus, error) {
	if p.err != nil {
		return nil, p.err
	}
	return &client.DeviceStatus{Name: name, Connected: true, PollInterval: "30s"}, nil
}

func (p *recordingPoller) ListDevices(_ context.Context) ([]client.DeviceInfo, error) {
	return nil, p.err
}

func (p *recordingPoller) calls(list *[]string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), *list...)
}

// do runs one request through the router and returns the recorder.
func do(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	var reader interface {
		Read([]byte) (int, error)
	} = http.NoBody
	if body != "" {
		reader = strings.NewReader(body)
	}

	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest(method, path, reader))
	return rec
}

func TestDeviceCRUD(t *testing.T) {
	s := newTestServer(t)
	poller := &recordingPoller{}
	s.pollerClient = poller

	rec := do(t, s, http.MethodPost, "/api/devices",
		`{"name":"bastion","address":"192.0.2.14","snmp_version":"v2c","community":"public"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201 (body %q)", rec.Code, rec.Body.String())
	}

	var created storage.Device
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("create response: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("the created device has no ID")
	}
	if got := poller.calls(&poller.registered); len(got) != 1 || got[0] != "bastion" {
		t.Errorf("poller registrations = %v, want [bastion]", got)
	}

	rec = do(t, s, http.MethodGet, "/api/devices/"+itoa(created.ID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200", rec.Code)
	}

	rec = do(t, s, http.MethodGet, "/api/devices", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200", rec.Code)
	}
	var list struct {
		Devices []storage.Device `json:"devices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("list response: %v", err)
	}
	if len(list.Devices) != 1 {
		t.Errorf("list returned %d devices, want 1", len(list.Devices))
	}

	rec = do(t, s, http.MethodPut, "/api/devices/"+itoa(created.ID),
		`{"name":"bastion","address":"192.0.2.99","snmp_version":"v2c","community":"private"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if got := poller.calls(&poller.updated); len(got) != 1 || got[0] != "bastion" {
		t.Errorf("poller updates = %v, want [bastion]", got)
	}

	rec = do(t, s, http.MethodDelete, "/api/devices/"+itoa(created.ID), "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", rec.Code)
	}
	if got := poller.calls(&poller.removed); len(got) != 1 || got[0] != "bastion" {
		t.Errorf("poller removals = %v, want [bastion]", got)
	}

	if rec = do(t, s, http.MethodGet, "/api/devices/"+itoa(created.ID), ""); rec.Code != http.StatusNotFound {
		t.Errorf("get after delete = %d, want 404", rec.Code)
	}
}

func TestDeviceHandlersRejectBadInput(t *testing.T) {
	s := newTestServer(t)
	device := storeDevice(t, s, "bastion", "192.0.2.14")

	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
	}{
		{"create with a malformed body", http.MethodPost, "/api/devices", "{", http.StatusBadRequest},
		{"create with no name", http.MethodPost, "/api/devices", `{"address":"1.2.3.4","snmp_version":"v2c"}`, http.StatusBadRequest},
		{"create with no address", http.MethodPost, "/api/devices", `{"name":"x","snmp_version":"v2c"}`, http.StatusBadRequest},
		{"create with no SNMP version", http.MethodPost, "/api/devices", `{"name":"x","address":"1.2.3.4"}`, http.StatusBadRequest},
		{"create under a name in use", http.MethodPost, "/api/devices", `{"name":"bastion","address":"1.2.3.4","snmp_version":"v2c"}`, http.StatusInternalServerError},
		{"get an unknown ID", http.MethodGet, "/api/devices/4242", "", http.StatusNotFound},
		{"update with a malformed body", http.MethodPut, "/api/devices/" + itoa(device), "{", http.StatusBadRequest},
		{"update with no name", http.MethodPut, "/api/devices/" + itoa(device), `{"address":"1.2.3.4","snmp_version":"v2c"}`, http.StatusBadRequest},
		{"update with no address", http.MethodPut, "/api/devices/" + itoa(device), `{"name":"x","snmp_version":"v2c"}`, http.StatusBadRequest},
		{"update with no SNMP version", http.MethodPut, "/api/devices/" + itoa(device), `{"name":"x","address":"1.2.3.4"}`, http.StatusBadRequest},
		{"update an unknown ID", http.MethodPut, "/api/devices/4242", `{"name":"x","address":"1.2.3.4","snmp_version":"v2c"}`, http.StatusInternalServerError},
		{"delete an unknown ID", http.MethodDelete, "/api/devices/4242", "", http.StatusInternalServerError},
		{"assign a MIB with a malformed body", http.MethodPost, "/api/devices/" + itoa(device) + "/mibs", "{", http.StatusBadRequest},
		{"assign a MIB with no mib_id", http.MethodPost, "/api/devices/" + itoa(device) + "/mibs", `{}`, http.StatusBadRequest},
		{"assign an unknown MIB", http.MethodPost, "/api/devices/" + itoa(device) + "/mibs", `{"mib_id":4242}`, http.StatusInternalServerError},
		{"remove a MIB that is not assigned", http.MethodDelete, "/api/devices/" + itoa(device) + "/mibs/4242", "", http.StatusInternalServerError},
		{"assign an unknown profile", http.MethodPost, "/api/devices/" + itoa(device) + "/profiles/4242", "", http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, s, tt.method, tt.path, tt.body)
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}

// TestDeviceMIBAssignmentSyncsThePoller states the contract the whole MIB
// assignment path exists for: the poller is told the device's new module set,
// because a worker chooses its OIDs once, when it starts.
func TestDeviceMIBAssignmentSyncsThePoller(t *testing.T) {
	s := newTestServer(t)
	poller := &recordingPoller{}
	s.pollerClient = poller

	device := storeDevice(t, s, "bastion", "192.0.2.14")
	mib := &storage.MIB{Name: "IF-MIB", FilePath: "/mibs/IF-MIB.mib"}
	if err := s.storage.CreateMIB(context.Background(), mib); err != nil {
		t.Fatalf("CreateMIB: %v", err)
	}

	rec := do(t, s, http.MethodPost, "/api/devices/"+itoa(device)+"/mibs", `{"mib_id":`+itoa(mib.ID)+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("assign status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}

	// The assignment reports what it will produce, so a module that yields
	// nothing can say so instead of leaving an empty dashboard to imply it.
	var assigned MIBAssignmentResult
	if err := json.Unmarshal(rec.Body.Bytes(), &assigned); err != nil {
		t.Fatalf("the assignment response is not a result: %v (body %q)", err, rec.Body.String())
	}
	if assigned.MIB != "IF-MIB" || assigned.Device != "bastion" {
		t.Errorf("assignment = %+v, want the MIB and device it attached", assigned)
	}
	// This server has no MIB loader, so the module cannot be examined. That must
	// leave the counts at zero rather than fabricate "this MIB produces nothing":
	// a warning that appears whenever the loader is down would send an operator
	// looking for a fault in a working module.
	if assigned.Warning != "" {
		t.Errorf("a module that could not be read was warned about: %s", assigned.Warning)
	}

	if got := poller.calls(&poller.updated); len(got) != 1 || got[0] != "bastion" {
		t.Fatalf("poller updates = %v, want [bastion]", got)
	}

	poller.mu.Lock()
	modules := poller.modules[0]
	poller.mu.Unlock()
	if len(modules) != 1 || modules[0] != "IF-MIB" {
		t.Errorf("modules pushed = %v, want [IF-MIB]", modules)
	}

	rec = do(t, s, http.MethodGet, "/api/devices/"+itoa(device)+"/mibs", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("device MIBs status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "IF-MIB") {
		t.Errorf("device MIBs = %q, want IF-MIB", rec.Body.String())
	}

	rec = do(t, s, http.MethodDelete, "/api/devices/"+itoa(device)+"/mibs/"+itoa(mib.ID), "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("remove status = %d, want 204", rec.Code)
	}
	if got := poller.calls(&poller.updated); len(got) != 2 {
		t.Errorf("poller updates = %v, want a second push after the removal", got)
	}
}

// TestAStalledPollerCannotHoldARequestOpen is the regression for the MIB
// assignment endpoint blocking for tens of seconds. The push to the poller is
// best-effort and its outcome is never reported, so it must never be what makes
// a caller wait past the handler's budget.
func TestAStalledPollerCannotHoldARequestOpen(t *testing.T) {
	t.Parallel()

	s := newTestServer(t)
	s.pollerClient = &recordingPoller{stall: true}

	device := storeDevice(t, s, "bastion", "192.0.2.14")
	mib := &storage.MIB{Name: "IF-MIB", FilePath: "/mibs/IF-MIB.mib"}
	if err := s.storage.CreateMIB(context.Background(), mib); err != nil {
		t.Fatalf("CreateMIB: %v", err)
	}

	start := time.Now()
	rec := do(t, s, http.MethodPost, "/api/devices/"+itoa(device)+"/mibs", `{"mib_id":`+itoa(mib.ID)+`}`)
	elapsed := time.Since(start)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: a durable change is not undone by a silent poller", rec.Code)
	}
	if elapsed >= ServerWriteTimeout {
		t.Fatalf("the request took %v, past ServerWriteTimeout (%v): the connection would be closed unanswered",
			elapsed, ServerWriteTimeout)
	}
	if elapsed > PollerSyncBudget+time.Second {
		t.Errorf("the request took %v, which overruns PollerSyncBudget (%v)", elapsed, PollerSyncBudget)
	}
}

// TestPollerFailuresDoNotUndoADurableChange keeps a device CREATE or UPDATE
// reported as successful when the poller could not be told about it. Those two
// fail into something visible and self-correcting -- a device that exists and
// is under-polled -- so the durable change is worth keeping.
//
// The delete arm used to be here too, asserting 204, and it is gone
// deliberately: see TestADeviceIsNotDeletedWhileThePollerStillPollsIt. A delete
// the poller misses is not an under-polled device, it is a worker polling a
// device that no longer exists, with nothing left in the UI to act on. The
// justification the old assertion rested on -- that "the poller re-reads it on
// its next configuration sync" -- describes a sync that does not exist;
// ApplyConfiguration runs once, at poller startup.
func TestPollerFailuresDoNotUndoADurableChange(t *testing.T) {
	s := newTestServer(t)
	s.pollerClient = &recordingPoller{err: errors.New("poller is down")}

	rec := do(t, s, http.MethodPost, "/api/devices",
		`{"name":"bastion","address":"192.0.2.14","snmp_version":"v2c"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201 despite the poller being down", rec.Code)
	}

	var created storage.Device
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("create response: %v", err)
	}

	rec = do(t, s, http.MethodPut, "/api/devices/"+itoa(created.ID),
		`{"name":"bastion","address":"192.0.2.15","snmp_version":"v2c"}`)
	if rec.Code != http.StatusOK {
		t.Errorf("update status = %d, want 200 despite the poller being down", rec.Code)
	}
}

// TestDeviceWritesWithoutAPoller keeps the web UI usable on its own.
func TestDeviceWritesWithoutAPoller(t *testing.T) {
	s := newTestServer(t)
	s.pollerClient = nil

	rec := do(t, s, http.MethodPost, "/api/devices",
		`{"name":"bastion","address":"192.0.2.14","snmp_version":"v2c"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201 with no poller configured", rec.Code)
	}
}

func TestProfileCRUD(t *testing.T) {
	s := newTestServer(t)

	rec := do(t, s, http.MethodPost, "/api/profiles", `{"name":"switches","description":"access layer"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201 (body %q)", rec.Code, rec.Body.String())
	}

	var profile storage.DeviceProfile
	if err := json.Unmarshal(rec.Body.Bytes(), &profile); err != nil {
		t.Fatalf("create response: %v", err)
	}

	if rec = do(t, s, http.MethodGet, "/api/profiles/"+itoa(profile.ID), ""); rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200", rec.Code)
	}
	if rec = do(t, s, http.MethodGet, "/api/profiles", ""); rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200", rec.Code)
	}

	rec = do(t, s, http.MethodPut, "/api/profiles/"+itoa(profile.ID), `{"name":"access-switches"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}

	// A profile's MIBs and devices are reachable and start empty.
	if rec = do(t, s, http.MethodGet, "/api/profiles/"+itoa(profile.ID)+"/mibs", ""); rec.Code != http.StatusOK {
		t.Errorf("profile MIBs status = %d, want 200", rec.Code)
	}
	if rec = do(t, s, http.MethodGet, "/api/profiles/"+itoa(profile.ID)+"/devices", ""); rec.Code != http.StatusOK {
		t.Errorf("profile devices status = %d, want 200", rec.Code)
	}

	if rec = do(t, s, http.MethodDelete, "/api/profiles/"+itoa(profile.ID), ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", rec.Code)
	}
	if rec = do(t, s, http.MethodGet, "/api/profiles/"+itoa(profile.ID), ""); rec.Code != http.StatusNotFound {
		t.Errorf("get after delete = %d, want 404", rec.Code)
	}
}

func TestProfileHandlersRejectBadInput(t *testing.T) {
	s := newTestServer(t)

	rec := do(t, s, http.MethodPost, "/api/profiles", `{"name":"switches"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup create status = %d, want 201", rec.Code)
	}
	var profile storage.DeviceProfile
	if err := json.Unmarshal(rec.Body.Bytes(), &profile); err != nil {
		t.Fatalf("setup response: %v", err)
	}

	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
	}{
		{"create with a malformed body", http.MethodPost, "/api/profiles", "{", http.StatusBadRequest},
		{"create with no name", http.MethodPost, "/api/profiles", `{"description":"x"}`, http.StatusBadRequest},
		{"create under a name in use", http.MethodPost, "/api/profiles", `{"name":"switches"}`, http.StatusInternalServerError},
		{"get an unknown ID", http.MethodGet, "/api/profiles/4242", "", http.StatusNotFound},
		{"update with a malformed body", http.MethodPut, "/api/profiles/" + itoa(profile.ID), "{", http.StatusBadRequest},
		{"update with no name", http.MethodPut, "/api/profiles/" + itoa(profile.ID), `{"description":"x"}`, http.StatusBadRequest},
		{"update an unknown ID", http.MethodPut, "/api/profiles/4242", `{"name":"ghosts"}`, http.StatusInternalServerError},
		{"delete an unknown ID", http.MethodDelete, "/api/profiles/4242", "", http.StatusInternalServerError},
		{"assign a MIB with a malformed body", http.MethodPost, "/api/profiles/" + itoa(profile.ID) + "/mibs", "{", http.StatusBadRequest},
		{"assign a MIB with no mib_id", http.MethodPost, "/api/profiles/" + itoa(profile.ID) + "/mibs", `{}`, http.StatusBadRequest},
		{"assign an unknown MIB", http.MethodPost, "/api/profiles/" + itoa(profile.ID) + "/mibs", `{"mib_id":4242}`, http.StatusInternalServerError},
		{"remove a MIB that is not assigned", http.MethodDelete, "/api/profiles/" + itoa(profile.ID) + "/mibs/4242", "", http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, s, tt.method, tt.path, tt.body)
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestProfileMIBAssignmentAndDeviceMembership(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	rec := do(t, s, http.MethodPost, "/api/profiles", `{"name":"switches"}`)
	var profile storage.DeviceProfile
	if err := json.Unmarshal(rec.Body.Bytes(), &profile); err != nil {
		t.Fatalf("create profile: %v", err)
	}

	mib := &storage.MIB{Name: "IF-MIB", FilePath: "/mibs/IF-MIB.mib"}
	if err := s.storage.CreateMIB(ctx, mib); err != nil {
		t.Fatalf("CreateMIB: %v", err)
	}
	device := storeDevice(t, s, "bastion", "192.0.2.14")

	if rec = do(t, s, http.MethodPost, "/api/profiles/"+itoa(profile.ID)+"/mibs",
		`{"mib_id":`+itoa(mib.ID)+`}`); rec.Code != http.StatusNoContent {
		t.Fatalf("assign MIB status = %d, want 204", rec.Code)
	}
	if rec = do(t, s, http.MethodPost,
		"/api/devices/"+itoa(device)+"/profiles/"+itoa(profile.ID), ""); rec.Code != http.StatusNoContent {
		t.Fatalf("assign profile status = %d, want 204", rec.Code)
	}

	// The device now polls the profile's MIB even though nothing was assigned
	// to it directly.
	rec = do(t, s, http.MethodGet, "/api/devices/"+itoa(device)+"/mibs", "")
	var mibs struct {
		DirectMIBs []json.RawMessage `json:"direct_mibs"`
		AllMIBs    []json.RawMessage `json:"all_mibs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &mibs); err != nil {
		t.Fatalf("device MIBs response: %v", err)
	}
	if len(mibs.DirectMIBs) != 0 {
		t.Errorf("direct MIBs = %d, want none", len(mibs.DirectMIBs))
	}
	if len(mibs.AllMIBs) != 1 {
		t.Errorf("effective MIBs = %d, want the profile's one", len(mibs.AllMIBs))
	}

	rec = do(t, s, http.MethodGet, "/api/profiles/"+itoa(profile.ID)+"/devices", "")
	if !strings.Contains(rec.Body.String(), "bastion") {
		t.Errorf("profile devices = %q, want bastion", rec.Body.String())
	}

	rec = do(t, s, http.MethodGet, "/api/devices/"+itoa(device)+"/profiles", "")
	if !strings.Contains(rec.Body.String(), "switches") {
		t.Errorf("device profiles = %q, want switches", rec.Body.String())
	}

	if rec = do(t, s, http.MethodDelete,
		"/api/devices/"+itoa(device)+"/profiles/"+itoa(profile.ID), ""); rec.Code != http.StatusNoContent {
		t.Errorf("remove profile status = %d, want 204", rec.Code)
	}
	if rec = do(t, s, http.MethodDelete,
		"/api/profiles/"+itoa(profile.ID)+"/mibs/"+itoa(mib.ID), ""); rec.Code != http.StatusNoContent {
		t.Errorf("remove MIB status = %d, want 204", rec.Code)
	}
}

func TestDeviceStatusHandler(t *testing.T) {
	s := newTestServer(t)
	s.pollerClient = &recordingPoller{}
	device := storeDevice(t, s, "bastion", "192.0.2.14")

	rec := do(t, s, http.MethodGet, "/api/devices/"+itoa(device)+"/status", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "bastion") {
		t.Errorf("body = %q, want it to name the device", rec.Body.String())
	}
}

func TestDeviceStatusHandlerRejectsBadInput(t *testing.T) {
	s := newTestServer(t)
	device := storeDevice(t, s, "bastion", "192.0.2.14")

	t.Run("unknown device", func(t *testing.T) {
		s.pollerClient = &recordingPoller{}
		if rec := do(t, s, http.MethodGet, "/api/devices/4242/status", ""); rec.Code == http.StatusOK {
			t.Errorf("status = 200 for a device that was never stored")
		}
	})

	t.Run("the poller cannot answer", func(t *testing.T) {
		s.pollerClient = &recordingPoller{err: errors.New("poller is down")}
		rec := do(t, s, http.MethodGet, "/api/devices/"+itoa(device)+"/status", "")
		if rec.Code == http.StatusOK {
			t.Errorf("status = 200 while the poller was down (body %q)", rec.Body.String())
		}
	})
}
