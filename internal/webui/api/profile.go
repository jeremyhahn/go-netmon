package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/gorilla/mux"

	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// handleListProfiles returns all device profiles
func (s *Server) handleListProfiles(w http.ResponseWriter, r *http.Request) {
	profiles, err := s.storage.ListDeviceProfiles(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"profiles": profiles,
	})
}

// handleCreateProfile creates a new device profile
func (s *Server) handleCreateProfile(w http.ResponseWriter, r *http.Request) {
	var profile storage.DeviceProfile
	if err := json.NewDecoder(r.Body).Decode(&profile); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Validate required fields
	if profile.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	if err := s.storage.CreateDeviceProfile(r.Context(), &profile); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	respondJSON(w, http.StatusCreated, profile)
}

// handleGetProfile retrieves a device profile by ID
func (s *Server) handleGetProfile(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id, err := strconv.ParseInt(vars["id"], 10, 64)
	if err != nil {
		http.Error(w, "invalid profile ID", http.StatusBadRequest)
		return
	}

	profile, err := s.storage.GetDeviceProfile(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	respondJSON(w, http.StatusOK, profile)
}

// handleUpdateProfile updates an existing device profile
func (s *Server) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id, err := strconv.ParseInt(vars["id"], 10, 64)
	if err != nil {
		http.Error(w, "invalid profile ID", http.StatusBadRequest)
		return
	}

	var profile storage.DeviceProfile
	if err := json.NewDecoder(r.Body).Decode(&profile); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	profile.ID = id

	// Validate required fields
	if profile.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	if err := s.storage.UpdateDeviceProfile(r.Context(), &profile); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	respondJSON(w, http.StatusOK, profile)
}

// handleDeleteProfile deletes a device profile
func (s *Server) handleDeleteProfile(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id, err := strconv.ParseInt(vars["id"], 10, 64)
	if err != nil {
		http.Error(w, "invalid profile ID", http.StatusBadRequest)
		return
	}

	if err := s.storage.DeleteDeviceProfile(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleGetProfileMIBs returns all MIBs associated with a profile, each with
// the assignment that says how the profile polls it.
func (s *Server) handleGetProfileMIBs(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id, err := strconv.ParseInt(vars["id"], 10, 64)
	if err != nil {
		http.Error(w, "invalid profile ID", http.StatusBadRequest)
		return
	}

	mibs, err := s.storage.GetProfileMIBs(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	assignments, err := s.storage.ProfileMIBAssignments(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"mibs": mibAssignmentViews(mibs, func(mibID int64) storage.MIBAssignment {
			if a, ok := assignments[mibID]; ok {
				return a
			}
			return storage.DefaultMIBAssignment()
		}),
	})
}

// handleAssignMIBToProfile assigns a MIB to a profile
func (s *Server) handleAssignMIBToProfile(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	profileID, err := strconv.ParseInt(vars["id"], 10, 64)
	if err != nil {
		http.Error(w, "invalid profile ID", http.StatusBadRequest)
		return
	}

	var req struct {
		MibID int64 `json:"mib_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.MibID == 0 {
		http.Error(w, "mib_id is required", http.StatusBadRequest)
		return
	}

	if err := s.storage.AssignMIBToProfile(r.Context(), profileID, req.MibID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleRemoveMIBFromProfile removes a MIB from a profile
func (s *Server) handleRemoveMIBFromProfile(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	profileID, err := strconv.ParseInt(vars["id"], 10, 64)
	if err != nil {
		http.Error(w, "invalid profile ID", http.StatusBadRequest)
		return
	}

	mibID, err := strconv.ParseInt(vars["mibId"], 10, 64)
	if err != nil {
		http.Error(w, "invalid MIB ID", http.StatusBadRequest)
		return
	}

	if err := s.storage.RemoveMIBFromProfile(r.Context(), profileID, mibID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleGetProfileDevices returns all devices using a specific profile
func (s *Server) handleGetProfileDevices(w http.ResponseWriter, r *http.Request) {
	listByPathID(w, r, "id", "profile", "devices", s.storage.GetDevicesByProfile)
}
