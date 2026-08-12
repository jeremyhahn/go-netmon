package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/gorilla/mux"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/webui/client"
	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// recordingPollerClient captures what the API tells the poller so tests can
// assert that a change was pushed rather than left for a restart.
type recordingPollerClient struct {
	updates []pollerUpdate
}

type pollerUpdate struct {
	device   string
	mibNames []string

	// filters is what narrows each module. The poller applies these when it
	// builds a worker, so a change that does not arrive here does not happen.
	filters map[string]config.ModuleFilter
}

func (c *recordingPollerClient) RegisterDevice(
	ctx context.Context, device *storage.Device,
	mibNames []string, filters map[string]config.ModuleFilter,
) error {
	return nil
}

func (c *recordingPollerClient) UpdateDevice(
	ctx context.Context, device *storage.Device,
	mibNames []string, filters map[string]config.ModuleFilter,
) error {
	c.updates = append(c.updates,
		pollerUpdate{device: device.Name, mibNames: mibNames, filters: filters})
	return nil
}

func (c *recordingPollerClient) RemoveDevice(ctx context.Context, deviceName string) error {
	return nil
}

func (c *recordingPollerClient) GetDeviceStatus(ctx context.Context, deviceName string) (*client.DeviceStatus, error) {
	return nil, nil
}

func (c *recordingPollerClient) ListDevices(ctx context.Context) ([]client.DeviceInfo, error) {
	return nil, nil
}

// newAssignmentServer builds a server backed by a temporary database with its
// real routes registered, so tests exercise the paths clients call.
func newAssignmentServer(t *testing.T) (*Server, *recordingPollerClient) {
	t.Helper()

	poller := &recordingPollerClient{}
	return newServerWithPoller(t, poller), poller
}

func mustCreateDevice(t *testing.T, store *storage.Storage, name string) int64 {
	t.Helper()

	device := &storage.Device{Name: name, Address: "192.0.2.1", SNMPVersion: "v2c", Community: "public", Port: 161}
	if err := store.CreateDevice(context.Background(), device); err != nil {
		t.Fatalf("failed to create device %s: %v", name, err)
	}
	return device.ID
}

func mustCreateProfile(t *testing.T, store *storage.Storage, name string) int64 {
	t.Helper()

	profile := &storage.DeviceProfile{Name: name}
	if err := store.CreateDeviceProfile(context.Background(), profile); err != nil {
		t.Fatalf("failed to create profile %s: %v", name, err)
	}
	return profile.ID
}

func mustCreateMIB(t *testing.T, store *storage.Storage, name string) int64 {
	t.Helper()

	m := &storage.MIB{Name: name, FilePath: "/mibs/" + name}
	if err := store.CreateMIB(context.Background(), m); err != nil {
		t.Fatalf("failed to create MIB %s: %v", name, err)
	}
	return m.ID
}

func TestMergeProfileAssignments(t *testing.T) {
	tests := []struct {
		name  string
		input []storage.MIBAssignment
		want  storage.MIBAssignment
	}{
		{
			name:  "no profile mentions the MIB, so nothing narrows it",
			input: nil,
			want:  storage.MIBAssignment{Enabled: true},
		},
		{
			name: "one enabling profile is carried through unchanged",
			input: []storage.MIBAssignment{
				{Enabled: true, IncludeNames: []string{"^ifIn"}, MaxOIDs: 10},
			},
			want: storage.MIBAssignment{Enabled: true, IncludeNames: []string{"^ifIn"}, MaxOIDs: 10},
		},
		{
			name: "several enabling profiles union their filters and take the tightest cap",
			input: []storage.MIBAssignment{
				{Enabled: true, IncludeNames: []string{"^ifIn"}, ExcludeOIDs: []string{"^1\\.3\\.6\\.1\\.2\\.1\\.2\\.2\\.1\\.1"}, MaxOIDs: 10},
				{Enabled: true, IncludeNames: []string{"^ifOut"}, IncludeOIDs: []string{"^1\\.3\\.6"}, MaxOIDs: 5},
			},
			want: storage.MIBAssignment{
				Enabled:      true,
				IncludeOIDs:  []string{"^1\\.3\\.6"},
				ExcludeOIDs:  []string{"^1\\.3\\.6\\.1\\.2\\.1\\.2\\.2\\.1\\.1"},
				IncludeNames: []string{"^ifIn", "^ifOut"},
				MaxOIDs:      5,
			},
		},
		{
			name: "an uncapped profile leaves the cap of a capped one in place",
			input: []storage.MIBAssignment{
				{Enabled: true},
				{Enabled: true, MaxOIDs: 7},
			},
			want: storage.MIBAssignment{Enabled: true, MaxOIDs: 7},
		},
		{
			name: "a disabled profile contributes neither enablement nor filters",
			input: []storage.MIBAssignment{
				{Enabled: false, IncludeNames: []string{"^never"}, MaxOIDs: 1},
				{Enabled: true, IncludeNames: []string{"^ifIn"}},
			},
			want: storage.MIBAssignment{Enabled: true, IncludeNames: []string{"^ifIn"}},
		},
		{
			name: "every profile disabled leaves the MIB disabled",
			input: []storage.MIBAssignment{
				{Enabled: false, IncludeNames: []string{"^a"}},
				{Enabled: false, IncludeNames: []string{"^b"}},
			},
			want: storage.MIBAssignment{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeProfileAssignments(tt.input)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("mergeProfileAssignments() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDeviceAssignmentsEffective(t *testing.T) {
	const mibID int64 = 42

	tests := []struct {
		name        string
		assignments deviceAssignments
		want        storage.MIBAssignment
	}{
		{
			name: "a direct assignment beats an enabling profile",
			assignments: deviceAssignments{
				direct: map[int64]storage.MIBAssignment{
					mibID: {Enabled: true, IncludeNames: []string{"^direct"}},
				},
				profiles: map[int64][]storage.MIBAssignment{
					mibID: {{Enabled: true, IncludeNames: []string{"^profile"}, MaxOIDs: 3}},
				},
			},
			want: storage.MIBAssignment{Enabled: true, IncludeNames: []string{"^direct"}},
		},
		{
			name: "a direct assignment that disables wins over an enabling profile",
			assignments: deviceAssignments{
				direct: map[int64]storage.MIBAssignment{mibID: {Enabled: false}},
				profiles: map[int64][]storage.MIBAssignment{
					mibID: {{Enabled: true}},
				},
			},
			want: storage.MIBAssignment{},
		},
		{
			name: "with no direct assignment the profiles decide",
			assignments: deviceAssignments{
				direct: map[int64]storage.MIBAssignment{},
				profiles: map[int64][]storage.MIBAssignment{
					mibID: {{Enabled: false}, {Enabled: true, MaxOIDs: 4}},
				},
			},
			want: storage.MIBAssignment{Enabled: true, MaxOIDs: 4},
		},
		{
			name:        "a MIB nothing has an opinion on is polled in full",
			assignments: deviceAssignments{},
			want:        storage.MIBAssignment{Enabled: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.assignments.effective(mibID)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("effective(%d) = %+v, want %+v", mibID, got, tt.want)
			}
		})
	}
}

func TestDeviceAssignmentsDirectAssignment(t *testing.T) {
	assignments := deviceAssignments{
		direct: map[int64]storage.MIBAssignment{7: {Enabled: false, MaxOIDs: 2}},
	}

	if got := assignments.directAssignment(7); !reflect.DeepEqual(got, storage.MIBAssignment{Enabled: false, MaxOIDs: 2}) {
		t.Errorf("directAssignment(7) = %+v, want the stored assignment", got)
	}

	// A MIB the device does not carry directly has no assignment of its own.
	if got := assignments.directAssignment(99); !got.Enabled || got.HasFilters() {
		t.Errorf("directAssignment(99) = %+v, want the enabled default", got)
	}
}

func TestValidateAssignment(t *testing.T) {
	tests := []struct {
		name        string
		assignment  storage.MIBAssignment
		wantErr     bool
		wantField   string
		wantInError string
	}{
		{
			name: "valid patterns and cap are accepted",
			assignment: storage.MIBAssignment{
				Enabled:      true,
				IncludeOIDs:  []string{"^1\\.3\\.6\\.1\\.2\\.1"},
				ExcludeNames: []string{"Discards$"},
				MaxOIDs:      25,
			},
		},
		{
			name:       "an empty assignment is accepted",
			assignment: storage.MIBAssignment{},
		},
		{
			name:        "a malformed include OID pattern is named",
			assignment:  storage.MIBAssignment{IncludeOIDs: []string{"^1\\.3", "[unclosed"}},
			wantErr:     true,
			wantField:   "include_oids",
			wantInError: "[unclosed",
		},
		{
			name:        "a malformed exclude OID pattern is named",
			assignment:  storage.MIBAssignment{ExcludeOIDs: []string{"*bad"}},
			wantErr:     true,
			wantField:   "exclude_oids",
			wantInError: "*bad",
		},
		{
			name:        "a malformed include name pattern is named",
			assignment:  storage.MIBAssignment{IncludeNames: []string{"(unclosed"}},
			wantErr:     true,
			wantField:   "include_names",
			wantInError: "(unclosed",
		},
		{
			name:        "a malformed exclude name pattern is named",
			assignment:  storage.MIBAssignment{ExcludeNames: []string{"a{2,1}"}},
			wantErr:     true,
			wantField:   "exclude_names",
			wantInError: "a{2,1}",
		},
		{
			name:        "a negative cap is rejected",
			assignment:  storage.MIBAssignment{MaxOIDs: -1},
			wantErr:     true,
			wantField:   "max_oids",
			wantInError: "-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAssignment(tt.assignment)
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("validateAssignment() = %v, want no error", err)
				}
				return
			}

			var validationErr *AssignmentValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("validateAssignment() = %T (%v), want *AssignmentValidationError", err, err)
			}
			if validationErr.Field != tt.wantField {
				t.Errorf("field = %q, want %q", validationErr.Field, tt.wantField)
			}
			if !strings.Contains(err.Error(), tt.wantInError) {
				t.Errorf("error %q does not name the offending value %q", err.Error(), tt.wantInError)
			}
		})
	}
}

func TestResolveDeviceModules(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name        string
		setup       func(t *testing.T, store *storage.Storage, deviceID int64)
		wantModules []string
		wantFilters map[string]PollerModuleFilter
	}{
		{
			name: "a legacy assignment polls the whole module",
			setup: func(t *testing.T, store *storage.Storage, deviceID int64) {
				mibID := mustCreateMIB(t, store, "IF-MIB")
				if err := store.AssignMIBToDevice(ctx, deviceID, mibID); err != nil {
					t.Fatalf("failed to assign MIB: %v", err)
				}
			},
			wantModules: []string{"IF-MIB"},
			wantFilters: nil,
		},
		{
			name: "a disabled MIB is left out of the module list",
			setup: func(t *testing.T, store *storage.Storage, deviceID int64) {
				keptID := mustCreateMIB(t, store, "IF-MIB")
				pausedID := mustCreateMIB(t, store, "UDP-MIB")
				if err := store.AssignMIBToDevice(ctx, deviceID, keptID); err != nil {
					t.Fatalf("failed to assign MIB: %v", err)
				}
				if err := store.AssignMIBToDevice(ctx, deviceID, pausedID); err != nil {
					t.Fatalf("failed to assign MIB: %v", err)
				}
				if err := store.SetDeviceMIBAssignment(ctx, deviceID, pausedID, storage.MIBAssignment{Enabled: false}); err != nil {
					t.Fatalf("failed to disable MIB: %v", err)
				}
			},
			wantModules: []string{"IF-MIB"},
		},
		{
			name: "filters travel with the module they narrow",
			setup: func(t *testing.T, store *storage.Storage, deviceID int64) {
				mibID := mustCreateMIB(t, store, "IF-MIB")
				if err := store.AssignMIBToDevice(ctx, deviceID, mibID); err != nil {
					t.Fatalf("failed to assign MIB: %v", err)
				}
				assignment := storage.MIBAssignment{Enabled: true, IncludeNames: []string{"^ifIn"}, MaxOIDs: 8}
				if err := store.SetDeviceMIBAssignment(ctx, deviceID, mibID, assignment); err != nil {
					t.Fatalf("failed to set assignment: %v", err)
				}
			},
			wantModules: []string{"IF-MIB"},
			wantFilters: map[string]PollerModuleFilter{
				"IF-MIB": {IncludeNames: []string{"^ifIn"}, MaxOIDs: 8},
			},
		},
		{
			name: "a direct assignment overrides the profile the MIB also comes from",
			setup: func(t *testing.T, store *storage.Storage, deviceID int64) {
				mibID := mustCreateMIB(t, store, "IF-MIB")
				profileID := mustCreateProfile(t, store, "core")

				if err := store.AssignMIBToProfile(ctx, profileID, mibID); err != nil {
					t.Fatalf("failed to assign MIB to profile: %v", err)
				}
				if err := store.AssignProfileToDevice(ctx, deviceID, profileID); err != nil {
					t.Fatalf("failed to assign profile: %v", err)
				}
				if err := store.SetProfileMIBAssignment(ctx, profileID, mibID, storage.MIBAssignment{
					Enabled:      true,
					IncludeNames: []string{"^fromProfile"},
				}); err != nil {
					t.Fatalf("failed to set profile assignment: %v", err)
				}

				if err := store.AssignMIBToDevice(ctx, deviceID, mibID); err != nil {
					t.Fatalf("failed to assign MIB to device: %v", err)
				}
				if err := store.SetDeviceMIBAssignment(ctx, deviceID, mibID, storage.MIBAssignment{
					Enabled:      true,
					IncludeNames: []string{"^fromDevice"},
				}); err != nil {
					t.Fatalf("failed to set device assignment: %v", err)
				}
			},
			wantModules: []string{"IF-MIB"},
			wantFilters: map[string]PollerModuleFilter{
				"IF-MIB": {IncludeNames: []string{"^fromDevice"}},
			},
		},
		{
			name: "a direct assignment that disables beats an enabling profile",
			setup: func(t *testing.T, store *storage.Storage, deviceID int64) {
				mibID := mustCreateMIB(t, store, "IF-MIB")
				profileID := mustCreateProfile(t, store, "core")

				if err := store.AssignMIBToProfile(ctx, profileID, mibID); err != nil {
					t.Fatalf("failed to assign MIB to profile: %v", err)
				}
				if err := store.AssignProfileToDevice(ctx, deviceID, profileID); err != nil {
					t.Fatalf("failed to assign profile: %v", err)
				}
				if err := store.AssignMIBToDevice(ctx, deviceID, mibID); err != nil {
					t.Fatalf("failed to assign MIB to device: %v", err)
				}
				if err := store.SetDeviceMIBAssignment(ctx, deviceID, mibID, storage.MIBAssignment{Enabled: false}); err != nil {
					t.Fatalf("failed to disable MIB: %v", err)
				}
			},
			wantModules: []string{},
		},
		{
			name: "two profiles carrying the same MIB union their filters",
			setup: func(t *testing.T, store *storage.Storage, deviceID int64) {
				mibID := mustCreateMIB(t, store, "IF-MIB")
				first := mustCreateProfile(t, store, "core")
				second := mustCreateProfile(t, store, "edge")

				for _, profileID := range []int64{first, second} {
					if err := store.AssignMIBToProfile(ctx, profileID, mibID); err != nil {
						t.Fatalf("failed to assign MIB to profile: %v", err)
					}
					if err := store.AssignProfileToDevice(ctx, deviceID, profileID); err != nil {
						t.Fatalf("failed to assign profile: %v", err)
					}
				}

				if err := store.SetProfileMIBAssignment(ctx, first, mibID, storage.MIBAssignment{
					Enabled:      true,
					IncludeNames: []string{"^ifIn"},
					MaxOIDs:      20,
				}); err != nil {
					t.Fatalf("failed to set profile assignment: %v", err)
				}
				if err := store.SetProfileMIBAssignment(ctx, second, mibID, storage.MIBAssignment{
					Enabled:      true,
					IncludeNames: []string{"^ifOut"},
					MaxOIDs:      6,
				}); err != nil {
					t.Fatalf("failed to set profile assignment: %v", err)
				}
			},
			wantModules: []string{"IF-MIB"},
			wantFilters: map[string]PollerModuleFilter{
				"IF-MIB": {IncludeNames: []string{"^ifIn", "^ifOut"}, MaxOIDs: 6},
			},
		},
		{
			name: "a MIB every profile disables is not polled",
			setup: func(t *testing.T, store *storage.Storage, deviceID int64) {
				mibID := mustCreateMIB(t, store, "IF-MIB")
				profileID := mustCreateProfile(t, store, "core")

				if err := store.AssignMIBToProfile(ctx, profileID, mibID); err != nil {
					t.Fatalf("failed to assign MIB to profile: %v", err)
				}
				if err := store.AssignProfileToDevice(ctx, deviceID, profileID); err != nil {
					t.Fatalf("failed to assign profile: %v", err)
				}
				if err := store.SetProfileMIBAssignment(ctx, profileID, mibID, storage.MIBAssignment{Enabled: false}); err != nil {
					t.Fatalf("failed to disable MIB: %v", err)
				}
			},
			wantModules: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, _ := newAssignmentServer(t)
			deviceID := mustCreateDevice(t, server.storage, "router-01")
			tt.setup(t, server.storage, deviceID)

			selection, err := server.resolveDeviceModules(ctx, deviceID)
			if err != nil {
				t.Fatalf("resolveDeviceModules() error: %v", err)
			}

			// The union of includes is order independent.
			for _, filter := range selection.Filters {
				sort.Strings(filter.IncludeNames)
				sort.Strings(filter.IncludeOIDs)
			}

			if !reflect.DeepEqual(selection.Modules, tt.wantModules) {
				t.Errorf("modules = %v, want %v", selection.Modules, tt.wantModules)
			}
			if !reflect.DeepEqual(selection.Filters, tt.wantFilters) {
				t.Errorf("filters = %+v, want %+v", selection.Filters, tt.wantFilters)
			}
		})
	}
}

func TestResolveDeviceModulesUnknownDevice(t *testing.T) {
	server, _ := newAssignmentServer(t)

	selection, err := server.resolveDeviceModules(context.Background(), 4242)
	if err != nil {
		t.Fatalf("resolveDeviceModules() error: %v", err)
	}
	if len(selection.Modules) != 0 || len(selection.Filters) != 0 {
		t.Errorf("got %+v, want an empty selection for a device that does not exist", selection)
	}
}

func TestHandleDeviceMIBAssignment(t *testing.T) {
	ctx := context.Background()

	t.Run("reading an assignment", func(t *testing.T) {
		server, _ := newAssignmentServer(t)
		deviceID := mustCreateDevice(t, server.storage, "router-01")
		mibID := mustCreateMIB(t, server.storage, "IF-MIB")
		if err := server.storage.AssignMIBToDevice(ctx, deviceID, mibID); err != nil {
			t.Fatalf("failed to assign MIB: %v", err)
		}

		rec := doRequest(t, server, http.MethodGet, assignmentPath("devices", deviceID, mibID), nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
		}

		var got storage.MIBAssignment
		if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if !got.Enabled {
			t.Errorf("assignment = %+v, want a legacy row to read back as enabled", got)
		}
	})

	t.Run("reading an assignment for a MIB the device does not have", func(t *testing.T) {
		server, _ := newAssignmentServer(t)
		deviceID := mustCreateDevice(t, server.storage, "router-01")
		mibID := mustCreateMIB(t, server.storage, "IF-MIB")

		rec := doRequest(t, server, http.MethodGet, assignmentPath("devices", deviceID, mibID), nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("writing an assignment stops the MIB being polled and tells the poller", func(t *testing.T) {
		server, poller := newAssignmentServer(t)
		deviceID := mustCreateDevice(t, server.storage, "router-01")
		pausedID := mustCreateMIB(t, server.storage, "UDP-MIB")
		keptID := mustCreateMIB(t, server.storage, "IF-MIB")
		for _, mibID := range []int64{pausedID, keptID} {
			if err := server.storage.AssignMIBToDevice(ctx, deviceID, mibID); err != nil {
				t.Fatalf("failed to assign MIB: %v", err)
			}
		}

		body := storage.MIBAssignment{Enabled: false}
		rec := doRequest(t, server, http.MethodPut, assignmentPath("devices", deviceID, pausedID), body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
		}

		stored, err := server.storage.DeviceMIBAssignment(ctx, deviceID, pausedID)
		if err != nil {
			t.Fatalf("failed to read back assignment: %v", err)
		}
		if stored.Enabled {
			t.Error("assignment was not persisted as disabled")
		}

		if len(poller.updates) != 1 {
			t.Fatalf("poller updates = %d, want 1", len(poller.updates))
		}
		if got := poller.updates[0].mibNames; !reflect.DeepEqual(got, []string{"IF-MIB"}) {
			t.Errorf("poller was told modules %v, want only the still enabled one", got)
		}
	})

	t.Run("writing an assignment with filters keeps the MIB polled", func(t *testing.T) {
		server, poller := newAssignmentServer(t)
		deviceID := mustCreateDevice(t, server.storage, "router-01")
		mibID := mustCreateMIB(t, server.storage, "IF-MIB")
		if err := server.storage.AssignMIBToDevice(ctx, deviceID, mibID); err != nil {
			t.Fatalf("failed to assign MIB: %v", err)
		}

		body := storage.MIBAssignment{Enabled: true, IncludeNames: []string{"^ifHC"}, MaxOIDs: 12}
		rec := doRequest(t, server, http.MethodPut, assignmentPath("devices", deviceID, mibID), body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
		}

		stored, err := server.storage.DeviceMIBAssignment(ctx, deviceID, mibID)
		if err != nil {
			t.Fatalf("failed to read back assignment: %v", err)
		}
		if !reflect.DeepEqual(stored, body) {
			t.Errorf("stored = %+v, want %+v", stored, body)
		}
		if len(poller.updates) != 1 || !reflect.DeepEqual(poller.updates[0].mibNames, []string{"IF-MIB"}) {
			t.Errorf("poller updates = %+v, want the module to stay assigned", poller.updates)
		}
	})

	t.Run("a malformed pattern is refused and never stored", func(t *testing.T) {
		server, poller := newAssignmentServer(t)
		deviceID := mustCreateDevice(t, server.storage, "router-01")
		mibID := mustCreateMIB(t, server.storage, "IF-MIB")
		if err := server.storage.AssignMIBToDevice(ctx, deviceID, mibID); err != nil {
			t.Fatalf("failed to assign MIB: %v", err)
		}

		body := storage.MIBAssignment{Enabled: true, IncludeOIDs: []string{"[unclosed"}}
		rec := doRequest(t, server, http.MethodPut, assignmentPath("devices", deviceID, mibID), body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "[unclosed") {
			t.Errorf("response %q does not name the offending pattern", rec.Body.String())
		}

		stored, err := server.storage.DeviceMIBAssignment(ctx, deviceID, mibID)
		if err != nil {
			t.Fatalf("failed to read back assignment: %v", err)
		}
		if stored.HasFilters() {
			t.Errorf("stored = %+v, want the rejected filter not to have been written", stored)
		}
		if len(poller.updates) != 0 {
			t.Errorf("poller updates = %+v, want none for a rejected request", poller.updates)
		}
	})

	t.Run("a body that is not an assignment is refused", func(t *testing.T) {
		server, _ := newAssignmentServer(t)
		deviceID := mustCreateDevice(t, server.storage, "router-01")
		mibID := mustCreateMIB(t, server.storage, "IF-MIB")

		req := httptest.NewRequest(http.MethodPut, assignmentPath("devices", deviceID, mibID), strings.NewReader("not json"))
		rec := httptest.NewRecorder()
		server.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("writing to a MIB the device does not have", func(t *testing.T) {
		server, poller := newAssignmentServer(t)
		deviceID := mustCreateDevice(t, server.storage, "router-01")
		mibID := mustCreateMIB(t, server.storage, "IF-MIB")

		rec := doRequest(t, server, http.MethodPut, assignmentPath("devices", deviceID, mibID), storage.MIBAssignment{Enabled: true})
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (%s)", rec.Code, rec.Body.String())
		}
		if len(poller.updates) != 0 {
			t.Errorf("poller updates = %+v, want none when nothing changed", poller.updates)
		}
	})

	t.Run("a malformed MIB ID is refused", func(t *testing.T) {
		server, _ := newAssignmentServer(t)

		req := httptest.NewRequest(http.MethodGet, "/api/devices/1/mibs/abc/assignment", nil)
		req = mux.SetURLVars(req, map[string]string{"id": "1", "mibId": "abc"})
		rec := httptest.NewRecorder()
		server.handleGetDeviceMIBAssignment(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
		}
	})
}

func TestHandleProfileMIBAssignment(t *testing.T) {
	ctx := context.Background()

	t.Run("reading an assignment", func(t *testing.T) {
		server, _ := newAssignmentServer(t)
		profileID := mustCreateProfile(t, server.storage, "core")
		mibID := mustCreateMIB(t, server.storage, "IF-MIB")
		if err := server.storage.AssignMIBToProfile(ctx, profileID, mibID); err != nil {
			t.Fatalf("failed to assign MIB: %v", err)
		}

		rec := doRequest(t, server, http.MethodGet, assignmentPath("profiles", profileID, mibID), nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
		}

		var got storage.MIBAssignment
		if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if !got.Enabled {
			t.Errorf("assignment = %+v, want enabled", got)
		}
	})

	t.Run("reading an assignment for a MIB the profile does not have", func(t *testing.T) {
		server, _ := newAssignmentServer(t)
		profileID := mustCreateProfile(t, server.storage, "core")
		mibID := mustCreateMIB(t, server.storage, "IF-MIB")

		rec := doRequest(t, server, http.MethodGet, assignmentPath("profiles", profileID, mibID), nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("writing an assignment updates every device using the profile", func(t *testing.T) {
		server, poller := newAssignmentServer(t)
		profileID := mustCreateProfile(t, server.storage, "core")
		pausedID := mustCreateMIB(t, server.storage, "UDP-MIB")
		keptID := mustCreateMIB(t, server.storage, "IF-MIB")
		deviceID := mustCreateDevice(t, server.storage, "router-01")

		for _, mibID := range []int64{pausedID, keptID} {
			if err := server.storage.AssignMIBToProfile(ctx, profileID, mibID); err != nil {
				t.Fatalf("failed to assign MIB: %v", err)
			}
		}
		if err := server.storage.AssignProfileToDevice(ctx, deviceID, profileID); err != nil {
			t.Fatalf("failed to assign profile: %v", err)
		}

		rec := doRequest(t, server, http.MethodPut, assignmentPath("profiles", profileID, pausedID), storage.MIBAssignment{Enabled: false})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
		}

		if len(poller.updates) != 1 {
			t.Fatalf("poller updates = %d, want one per device using the profile", len(poller.updates))
		}
		if poller.updates[0].device != "router-01" {
			t.Errorf("updated device = %q, want router-01", poller.updates[0].device)
		}
		if got := poller.updates[0].mibNames; !reflect.DeepEqual(got, []string{"IF-MIB"}) {
			t.Errorf("poller was told modules %v, want only the still enabled one", got)
		}
	})

	t.Run("a malformed pattern is refused", func(t *testing.T) {
		server, poller := newAssignmentServer(t)
		profileID := mustCreateProfile(t, server.storage, "core")
		mibID := mustCreateMIB(t, server.storage, "IF-MIB")
		if err := server.storage.AssignMIBToProfile(ctx, profileID, mibID); err != nil {
			t.Fatalf("failed to assign MIB: %v", err)
		}

		body := storage.MIBAssignment{Enabled: true, ExcludeNames: []string{"a{2,1}"}}
		rec := doRequest(t, server, http.MethodPut, assignmentPath("profiles", profileID, mibID), body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "a{2,1}") {
			t.Errorf("response %q does not name the offending pattern", rec.Body.String())
		}
		if len(poller.updates) != 0 {
			t.Errorf("poller updates = %+v, want none for a rejected request", poller.updates)
		}
	})

	t.Run("writing to a MIB the profile does not have", func(t *testing.T) {
		server, _ := newAssignmentServer(t)
		profileID := mustCreateProfile(t, server.storage, "core")
		mibID := mustCreateMIB(t, server.storage, "IF-MIB")

		rec := doRequest(t, server, http.MethodPut, assignmentPath("profiles", profileID, mibID), storage.MIBAssignment{Enabled: true})
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("a malformed profile ID is refused", func(t *testing.T) {
		server, _ := newAssignmentServer(t)

		req := httptest.NewRequest(http.MethodGet, "/api/profiles/x/mibs/1/assignment", nil)
		req = mux.SetURLVars(req, map[string]string{"id": "x", "mibId": "1"})
		rec := httptest.NewRecorder()
		server.handleGetProfileMIBAssignment(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
		}
	})
}

func TestHandleGetDeviceMIBsCarriesAssignments(t *testing.T) {
	ctx := context.Background()
	server, _ := newAssignmentServer(t)

	deviceID := mustCreateDevice(t, server.storage, "router-01")
	directID := mustCreateMIB(t, server.storage, "IF-MIB")
	profileMIBID := mustCreateMIB(t, server.storage, "UDP-MIB")
	profileID := mustCreateProfile(t, server.storage, "core")

	if err := server.storage.AssignMIBToDevice(ctx, deviceID, directID); err != nil {
		t.Fatalf("failed to assign MIB: %v", err)
	}
	if err := server.storage.SetDeviceMIBAssignment(ctx, deviceID, directID, storage.MIBAssignment{Enabled: false}); err != nil {
		t.Fatalf("failed to disable MIB: %v", err)
	}
	if err := server.storage.AssignMIBToProfile(ctx, profileID, profileMIBID); err != nil {
		t.Fatalf("failed to assign MIB to profile: %v", err)
	}
	if err := server.storage.AssignProfileToDevice(ctx, deviceID, profileID); err != nil {
		t.Fatalf("failed to assign profile: %v", err)
	}

	rec := doRequest(t, server, http.MethodGet, "/api/devices/"+strconv.FormatInt(deviceID, 10)+"/mibs", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	var resp struct {
		DirectMIBs []MIBAssignmentView `json:"direct_mibs"`
		AllMIBs    []MIBAssignmentView `json:"all_mibs"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(resp.DirectMIBs) != 1 {
		t.Fatalf("direct_mibs = %d, want 1", len(resp.DirectMIBs))
	}
	if resp.DirectMIBs[0].Name != "IF-MIB" || resp.DirectMIBs[0].Assignment.Enabled {
		t.Errorf("direct_mibs[0] = %+v, want IF-MIB reported as disabled", resp.DirectMIBs[0])
	}

	byName := make(map[string]storage.MIBAssignment, len(resp.AllMIBs))
	for _, view := range resp.AllMIBs {
		byName[view.Name] = view.Assignment
	}
	if len(byName) != 2 {
		t.Fatalf("all_mibs = %+v, want both MIBs", resp.AllMIBs)
	}
	if byName["IF-MIB"].Enabled {
		t.Error("all_mibs reports IF-MIB as enabled, want the effective assignment")
	}
	if !byName["UDP-MIB"].Enabled {
		t.Error("all_mibs reports UDP-MIB as disabled, want the profile's enabled assignment")
	}
}

func TestHandleGetProfileMIBsCarriesAssignments(t *testing.T) {
	ctx := context.Background()
	server, _ := newAssignmentServer(t)

	profileID := mustCreateProfile(t, server.storage, "core")
	filteredID := mustCreateMIB(t, server.storage, "IF-MIB")
	legacyID := mustCreateMIB(t, server.storage, "UDP-MIB")

	for _, mibID := range []int64{filteredID, legacyID} {
		if err := server.storage.AssignMIBToProfile(ctx, profileID, mibID); err != nil {
			t.Fatalf("failed to assign MIB: %v", err)
		}
	}
	if err := server.storage.SetProfileMIBAssignment(ctx, profileID, filteredID, storage.MIBAssignment{
		Enabled:     true,
		ExcludeOIDs: []string{"^1\\.3\\.6\\.1\\.2\\.1\\.2\\.2\\.1\\.1"},
	}); err != nil {
		t.Fatalf("failed to set assignment: %v", err)
	}

	rec := doRequest(t, server, http.MethodGet, "/api/profiles/"+strconv.FormatInt(profileID, 10)+"/mibs", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	var resp struct {
		MIBs []MIBAssignmentView `json:"mibs"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(resp.MIBs) != 2 {
		t.Fatalf("mibs = %d, want 2", len(resp.MIBs))
	}

	for _, view := range resp.MIBs {
		if !view.Assignment.Enabled {
			t.Errorf("%s reported as disabled, want enabled", view.Name)
		}
		switch view.Name {
		case "IF-MIB":
			if len(view.Assignment.ExcludeOIDs) != 1 {
				t.Errorf("IF-MIB assignment = %+v, want its exclude pattern", view.Assignment)
			}
		case "UDP-MIB":
			if view.Assignment.HasFilters() {
				t.Errorf("UDP-MIB assignment = %+v, want a legacy row to carry no filters", view.Assignment)
			}
		default:
			t.Errorf("unexpected MIB %q in response", view.Name)
		}
	}
}

// assignmentPath builds the assignment URL for a device or profile.
func assignmentPath(owner string, ownerID, mibID int64) string {
	return "/api/" + owner + "/" + strconv.FormatInt(ownerID, 10) +
		"/mibs/" + strconv.FormatInt(mibID, 10) + "/assignment"
}

// doRequest sends a request through the server's registered routes.
func doRequest(t *testing.T, server *Server, method, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()

	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("failed to encode body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}

	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	return rec
}

func TestGetDeviceMIBNames(t *testing.T) {
	ctx := context.Background()

	t.Run("only enabled MIBs are reported", func(t *testing.T) {
		server, _ := newAssignmentServer(t)
		deviceID := mustCreateDevice(t, server.storage, "router-01")
		keptID := mustCreateMIB(t, server.storage, "IF-MIB")
		pausedID := mustCreateMIB(t, server.storage, "UDP-MIB")

		for _, mibID := range []int64{keptID, pausedID} {
			if err := server.storage.AssignMIBToDevice(ctx, deviceID, mibID); err != nil {
				t.Fatalf("failed to assign MIB: %v", err)
			}
		}
		if err := server.storage.SetDeviceMIBAssignment(ctx, deviceID, pausedID, storage.MIBAssignment{Enabled: false}); err != nil {
			t.Fatalf("failed to disable MIB: %v", err)
		}

		selection, err := server.getDeviceModules(ctx, deviceID)
		if err != nil {
			t.Fatalf("getDeviceModules() error: %v", err)
		}
		names := selection.Modules
		if !reflect.DeepEqual(names, []string{"IF-MIB"}) {
			t.Errorf("names = %v, want only the enabled MIB", names)
		}
	})

	t.Run("a device that does not exist has no MIBs", func(t *testing.T) {
		server, _ := newAssignmentServer(t)

		selection, err := server.getDeviceModules(ctx, 1234)
		if err != nil {
			t.Fatalf("getDeviceModules() error: %v", err)
		}
		names := selection.Modules
		if len(names) != 0 {
			t.Errorf("names = %v, want none", names)
		}
	})
}

func TestSyncDeviceWithPoller(t *testing.T) {
	ctx := context.Background()

	t.Run("the device current module set is pushed", func(t *testing.T) {
		server, poller := newAssignmentServer(t)
		deviceID := mustCreateDevice(t, server.storage, "router-01")
		mibID := mustCreateMIB(t, server.storage, "IF-MIB")
		if err := server.storage.AssignMIBToDevice(ctx, deviceID, mibID); err != nil {
			t.Fatalf("failed to assign MIB: %v", err)
		}

		server.syncDeviceWithPoller(ctx, deviceID, "test")

		if len(poller.updates) != 1 {
			t.Fatalf("poller updates = %d, want 1", len(poller.updates))
		}
		if poller.updates[0].device != "router-01" {
			t.Errorf("updated device = %q, want router-01", poller.updates[0].device)
		}
		if !reflect.DeepEqual(poller.updates[0].mibNames, []string{"IF-MIB"}) {
			t.Errorf("modules = %v, want [IF-MIB]", poller.updates[0].mibNames)
		}
	})

	t.Run("a device that does not exist is not pushed", func(t *testing.T) {
		server, poller := newAssignmentServer(t)

		server.syncDeviceWithPoller(ctx, 4242, "test")

		if len(poller.updates) != 0 {
			t.Errorf("poller updates = %+v, want none", poller.updates)
		}
	})

	t.Run("without a poller the call is a no-op", func(t *testing.T) {
		server, _ := newAssignmentServer(t)
		server.pollerClient = nil
		deviceID := mustCreateDevice(t, server.storage, "router-01")

		server.syncDeviceWithPoller(ctx, deviceID, "test")
	})
}

func TestSyncProfileDevicesWithPoller(t *testing.T) {
	ctx := context.Background()

	t.Run("every device using the profile is pushed", func(t *testing.T) {
		server, poller := newAssignmentServer(t)
		profileID := mustCreateProfile(t, server.storage, "core")
		mibID := mustCreateMIB(t, server.storage, "IF-MIB")
		if err := server.storage.AssignMIBToProfile(ctx, profileID, mibID); err != nil {
			t.Fatalf("failed to assign MIB: %v", err)
		}

		for _, name := range []string{"router-01", "router-02"} {
			deviceID := mustCreateDevice(t, server.storage, name)
			if err := server.storage.AssignProfileToDevice(ctx, deviceID, profileID); err != nil {
				t.Fatalf("failed to assign profile: %v", err)
			}
		}

		server.syncProfileDevicesWithPoller(ctx, profileID)

		if len(poller.updates) != 2 {
			t.Fatalf("poller updates = %d, want one per device", len(poller.updates))
		}
		for _, update := range poller.updates {
			if !reflect.DeepEqual(update.mibNames, []string{"IF-MIB"}) {
				t.Errorf("device %s got modules %v, want [IF-MIB]", update.device, update.mibNames)
			}
		}
	})

	t.Run("a profile no device uses pushes nothing", func(t *testing.T) {
		server, poller := newAssignmentServer(t)
		profileID := mustCreateProfile(t, server.storage, "unused")

		server.syncProfileDevicesWithPoller(ctx, profileID)

		if len(poller.updates) != 0 {
			t.Errorf("poller updates = %+v, want none", poller.updates)
		}
	})
}
