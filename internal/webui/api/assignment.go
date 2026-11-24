package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sort"
	"strconv"

	"github.com/gorilla/mux"

	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// MIBAssignmentView is a MIB together with how it is polled.
//
// The MIB's own fields are flattened into the object, so a client that only
// knows about the MIB keeps working while a newer one can read the assignment
// and show whether the module is enabled without a request per MIB.
type MIBAssignmentView struct {
	storage.MIB
	Assignment storage.MIBAssignment `json:"assignment"`
}

// moduleSelection is the set of MIB modules a device polls, with the filters
// that narrow each one. Modules is sorted so the poller sees a stable order.
type moduleSelection struct {
	Modules []string
	Filters map[string]PollerModuleFilter
}

// deviceAssignments holds one device's MIB assignments keyed by MIB ID. The
// direct assignments are kept apart from the ones contributed by profiles so
// that the two can be resolved against each other.
type deviceAssignments struct {
	direct   map[int64]storage.MIBAssignment
	profiles map[int64][]storage.MIBAssignment
}

// effective resolves how a MIB is polled for the device: a direct assignment wins, else the profiles merge.
func (d deviceAssignments) effective(mibID int64) storage.MIBAssignment {
	if direct, ok := d.direct[mibID]; ok {
		return direct
	}
	return mergeProfileAssignments(d.profiles[mibID])
}

// directAssignment returns the device's own assignment for a MIB, or the
// default for a MIB the device does not carry directly.
func (d deviceAssignments) directAssignment(mibID int64) storage.MIBAssignment {
	if direct, ok := d.direct[mibID]; ok {
		return direct
	}
	return storage.DefaultMIBAssignment()
}

// mergeProfileAssignments merges a MIB's profile assignments: polled if any enables it, filters unioned, tightest cap wins; empty means poll it all.
func mergeProfileAssignments(assignments []storage.MIBAssignment) storage.MIBAssignment {
	if len(assignments) == 0 {
		return storage.DefaultMIBAssignment()
	}

	merged := storage.MIBAssignment{}
	for _, a := range assignments {
		if !a.Enabled {
			continue
		}

		merged.Enabled = true
		merged.IncludeOIDs = append(merged.IncludeOIDs, a.IncludeOIDs...)
		merged.ExcludeOIDs = append(merged.ExcludeOIDs, a.ExcludeOIDs...)
		merged.IncludeNames = append(merged.IncludeNames, a.IncludeNames...)
		merged.ExcludeNames = append(merged.ExcludeNames, a.ExcludeNames...)

		if a.MaxOIDs > 0 && (merged.MaxOIDs == 0 || a.MaxOIDs < merged.MaxOIDs) {
			merged.MaxOIDs = a.MaxOIDs
		}
	}

	if !merged.Enabled {
		return storage.MIBAssignment{}
	}
	return merged
}

// loadDeviceAssignments reads every assignment that bears on a device: its own,
// and those of each profile it uses.
func (s *Server) loadDeviceAssignments(ctx context.Context, deviceID int64) (deviceAssignments, error) {
	direct, err := s.storage.DeviceMIBAssignments(ctx, deviceID)
	if err != nil {
		return deviceAssignments{}, err
	}

	profiles, err := s.storage.GetDeviceProfiles(ctx, deviceID)
	if err != nil {
		return deviceAssignments{}, err
	}

	byMIB := make(map[int64][]storage.MIBAssignment)
	for _, profile := range profiles {
		profileAssignments, err := s.storage.ProfileMIBAssignments(ctx, profile.ID)
		if err != nil {
			return deviceAssignments{}, err
		}
		for mibID, a := range profileAssignments {
			byMIB[mibID] = append(byMIB[mibID], a)
		}
	}

	return deviceAssignments{direct: direct, profiles: byMIB}, nil
}

// resolveDeviceModules returns the modules a device polls, with their filters; disabled MIBs are left out.
func (s *Server) resolveDeviceModules(ctx context.Context, deviceID int64) (moduleSelection, error) {
	mibs, err := s.storage.GetDeviceMIBs(ctx, deviceID)
	if err != nil {
		return moduleSelection{}, err
	}

	assignments, err := s.loadDeviceAssignments(ctx, deviceID)
	if err != nil {
		return moduleSelection{}, err
	}

	selection := moduleSelection{Modules: make([]string, 0, len(mibs))}
	for _, m := range mibs {
		a := assignments.effective(m.ID)
		if !a.Enabled {
			continue
		}

		selection.Modules = append(selection.Modules, m.Name)
		if a.HasFilters() {
			if selection.Filters == nil {
				selection.Filters = make(map[string]PollerModuleFilter)
			}
			selection.Filters[m.Name] = newPollerModuleFilter(a)
		}
	}

	sort.Strings(selection.Modules)
	return selection, nil
}

// source names where a MIB's effective assignment came from.
func (d deviceAssignments) source(mibID int64) string {
	if _, ok := d.direct[mibID]; ok {
		return "device"
	}
	if len(d.profiles[mibID]) > 0 {
		return "profile"
	}
	return "default"
}

// mibAssignmentViews pairs each MIB with the assignment the caller resolves
// for it.
func mibAssignmentViews(mibs []storage.MIB, assignmentOf func(mibID int64) storage.MIBAssignment) []MIBAssignmentView {
	views := make([]MIBAssignmentView, 0, len(mibs))
	for _, m := range mibs {
		views = append(views, MIBAssignmentView{MIB: m, Assignment: assignmentOf(m.ID)})
	}
	return views
}

// validateAssignment reports the first problem storage.ValidateMIBAssignment finds.
func validateAssignment(a storage.MIBAssignment) error {
	err := storage.ValidateMIBAssignment(a)
	if err == nil {
		return nil
	}

	var fieldErr *storage.AssignmentFieldError
	if errors.As(err, &fieldErr) {
		return &AssignmentValidationError{
			Field:  fieldErr.Field,
			Value:  fieldErr.Value,
			Reason: fieldErr.Reason,
		}
	}
	return err
}

// assignmentIDs parses the owner and MIB IDs from the path, answering the client when either is malformed.
func assignmentIDs(w http.ResponseWriter, r *http.Request, ownerField string) (ownerID, mibID int64, ok bool) {
	vars := mux.Vars(r)

	ownerID, err := strconv.ParseInt(vars["id"], 10, 64)
	if err != nil {
		http.Error(w, "invalid "+ownerField+" ID", http.StatusBadRequest)
		return 0, 0, false
	}

	mibID, err = strconv.ParseInt(vars["mibId"], 10, 64)
	if err != nil {
		http.Error(w, "invalid MIB ID", http.StatusBadRequest)
		return 0, 0, false
	}

	return ownerID, mibID, true
}

// decodeAssignment reads and validates an assignment request body.
func decodeAssignment(w http.ResponseWriter, r *http.Request) (storage.MIBAssignment, bool) {
	var a storage.MIBAssignment
	if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return storage.MIBAssignment{}, false
	}

	if err := validateAssignment(a); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return storage.MIBAssignment{}, false
	}

	return a, true
}

// respondAssignmentError maps a storage failure to a status. A MIB that is not
// assigned is a missing resource, not a server fault.
func respondAssignmentError(w http.ResponseWriter, err error) {
	var notFound *storage.AssignmentNotFoundError
	if errors.As(err, &notFound) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

// handleGetDeviceMIBAssignment returns how a directly assigned MIB is polled.
func (s *Server) handleGetDeviceMIBAssignment(w http.ResponseWriter, r *http.Request) {
	deviceID, mibID, ok := assignmentIDs(w, r, "device")
	if !ok {
		return
	}

	assignment, err := s.storage.DeviceMIBAssignment(r.Context(), deviceID, mibID)
	if err != nil {
		respondAssignmentError(w, err)
		return
	}

	respondJSON(w, http.StatusOK, assignment)
}

// handleSetDeviceMIBAssignment replaces how a directly assigned MIB is polled
// and pushes the device's new module set to the poller.
func (s *Server) handleSetDeviceMIBAssignment(w http.ResponseWriter, r *http.Request) {
	deviceID, mibID, ok := assignmentIDs(w, r, "device")
	if !ok {
		return
	}

	assignment, ok := decodeAssignment(w, r)
	if !ok {
		return
	}

	if err := s.storage.SetDeviceMIBAssignment(r.Context(), deviceID, mibID, assignment); err != nil {
		respondAssignmentError(w, err)
		return
	}

	s.syncDeviceWithPoller(r.Context(), deviceID, "MIB assignment change")

	respondJSON(w, http.StatusOK, assignment)
}

// handleGetProfileMIBAssignment returns how a MIB in a profile is polled.
func (s *Server) handleGetProfileMIBAssignment(w http.ResponseWriter, r *http.Request) {
	profileID, mibID, ok := assignmentIDs(w, r, "profile")
	if !ok {
		return
	}

	assignment, err := s.storage.ProfileMIBAssignment(r.Context(), profileID, mibID)
	if err != nil {
		respondAssignmentError(w, err)
		return
	}

	respondJSON(w, http.StatusOK, assignment)
}

// handleSetProfileMIBAssignment replaces how a MIB in a profile is polled and
// pushes the new module set to every device using that profile.
func (s *Server) handleSetProfileMIBAssignment(w http.ResponseWriter, r *http.Request) {
	profileID, mibID, ok := assignmentIDs(w, r, "profile")
	if !ok {
		return
	}

	assignment, ok := decodeAssignment(w, r)
	if !ok {
		return
	}

	if err := s.storage.SetProfileMIBAssignment(r.Context(), profileID, mibID, assignment); err != nil {
		respondAssignmentError(w, err)
		return
	}

	s.syncProfileDevicesWithPoller(r.Context(), profileID)

	respondJSON(w, http.StatusOK, assignment)
}

// syncProfileDevicesWithPoller pushes the new module set of every device that
// uses a profile, since a profile-level change reaches the poller only through
// the devices that inherit it.
func (s *Server) syncProfileDevicesWithPoller(ctx context.Context, profileID int64) {
	if s.pollerClient == nil {
		return
	}

	devices, err := s.storage.GetDevicesByProfile(ctx, profileID)
	if err != nil {
		log.Printf("Warning: failed to list devices for profile %d: %v", profileID, err)
		return
	}

	for _, device := range devices {
		s.syncDeviceWithPoller(ctx, device.ID, "profile MIB assignment change")
	}
}
