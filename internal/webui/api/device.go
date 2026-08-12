package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/gorilla/mux"

	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// handleListDevices returns all devices
func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := s.storage.ListDevices(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"devices": newDeviceViews(devices, s.polling()),
	})
}

// handleCreateDevice creates a new device
func (s *Server) handleCreateDevice(w http.ResponseWriter, r *http.Request) {
	var device storage.Device
	if err := json.NewDecoder(r.Body).Decode(&device); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Validate required fields
	if device.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if device.Address == "" {
		http.Error(w, "address is required", http.StatusBadRequest)
		return
	}
	if device.SNMPVersion == "" {
		http.Error(w, "snmp_version is required", http.StatusBadRequest)
		return
	}
	if err := storage.ValidatePollInterval(device.Name, device.PollInterval); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Save device to storage
	if err := s.storage.CreateDevice(r.Context(), &device); err != nil {
		writeDeviceWriteError(w, err)
		return
	}

	// Register device with poller (graceful degradation - don't fail if poller is down)
	if s.pollerClient != nil {
		mibNames, err := s.getDeviceMIBNames(r.Context(), device.ID)
		if err != nil {
			log.Printf("Warning: failed to get device MIB names: %v", err)
			mibNames = nil
		}

		if err := s.pollerClient.RegisterDevice(r.Context(), &device, mibNames); err != nil {
			log.Printf("Warning: failed to register device with poller: %v", err)
			// Don't fail - device is saved in DB
		}
	}

	respondJSON(w, http.StatusCreated, newDeviceView(device, s.polling()))
}

// handleGetDevice retrieves a device by ID
func (s *Server) handleGetDevice(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id, err := strconv.ParseInt(vars["id"], 10, 64)
	if err != nil {
		http.Error(w, "invalid device ID", http.StatusBadRequest)
		return
	}

	device, err := s.storage.GetDevice(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	respondJSON(w, http.StatusOK, newDeviceView(*device, s.polling()))
}

// handleUpdateDevice updates an existing device
func (s *Server) handleUpdateDevice(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id, err := strconv.ParseInt(vars["id"], 10, 64)
	if err != nil {
		http.Error(w, "invalid device ID", http.StatusBadRequest)
		return
	}

	var device storage.Device
	if err := json.NewDecoder(r.Body).Decode(&device); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	device.ID = id

	// Validate required fields
	if device.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if device.Address == "" {
		http.Error(w, "address is required", http.StatusBadRequest)
		return
	}
	if device.SNMPVersion == "" {
		http.Error(w, "snmp_version is required", http.StatusBadRequest)
		return
	}
	if err := storage.ValidatePollInterval(device.Name, device.PollInterval); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Update device in storage
	if err := s.storage.UpdateDevice(r.Context(), &device); err != nil {
		writeDeviceWriteError(w, err)
		return
	}

	// Update device in poller (graceful degradation - don't fail if poller is down)
	if s.pollerClient != nil {
		mibNames, err := s.getDeviceMIBNames(r.Context(), device.ID)
		if err != nil {
			log.Printf("Warning: failed to get device MIB names: %v", err)
			mibNames = nil
		}

		if err := s.pollerClient.UpdateDevice(r.Context(), &device, mibNames); err != nil {
			log.Printf("Warning: failed to update device in poller: %v", err)
			// Don't fail - device is updated in DB
		}
	}

	respondJSON(w, http.StatusOK, newDeviceView(device, s.polling()))
}

// writeDeviceWriteError reports a failed device write.
//
// A rejected poll interval is the caller's input, not the server's fault, so it
// answers 400 with the message that names the offending value. Anything else is
// the storage layer failing and stays a 500.
func writeDeviceWriteError(w http.ResponseWriter, err error) {
	var invalidInterval *storage.InvalidPollIntervalError
	if errors.As(err, &invalidInterval) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

// handleDeleteDevice deletes a device
func (s *Server) handleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id, err := strconv.ParseInt(vars["id"], 10, 64)
	if err != nil {
		http.Error(w, "invalid device ID", http.StatusBadRequest)
		return
	}

	// Get device name before deletion (needed for poller)
	device, err := s.storage.GetDevice(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Delete device from storage
	if err := s.storage.DeleteDevice(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Remove device from poller (graceful degradation - don't fail if poller is down)
	if s.pollerClient != nil {
		if err := s.pollerClient.RemoveDevice(r.Context(), device.Name); err != nil {
			log.Printf("Warning: failed to remove device from poller: %v", err)
			// Don't fail - device is deleted from DB
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleGetDeviceMIBs returns all MIBs associated with a device.
//
// Each MIB carries its assignment so a client can show what is polled without
// asking per MIB. The direct list reports the device's own assignment, which is
// what a write to that MIB's assignment edits; the combined list reports the
// effective one, which is what the poller acts on.
func (s *Server) handleGetDeviceMIBs(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id, err := strconv.ParseInt(vars["id"], 10, 64)
	if err != nil {
		http.Error(w, "invalid device ID", http.StatusBadRequest)
		return
	}

	directMIBs, allMIBs, err := s.storage.GetDeviceMIBsSeparated(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	assignments, err := s.loadDeviceAssignments(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"direct_mibs": mibAssignmentViews(directMIBs, assignments.directAssignment),
		"all_mibs":    mibAssignmentViews(allMIBs, assignments.effective),
	})
}

// handleAssignMIBToDevice assigns a MIB to a device
func (s *Server) handleAssignMIBToDevice(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	deviceID, err := strconv.ParseInt(vars["id"], 10, 64)
	if err != nil {
		http.Error(w, "invalid device ID", http.StatusBadRequest)
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

	if err := s.storage.AssignMIBToDevice(r.Context(), deviceID, req.MibID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.syncDeviceWithPoller(r.Context(), deviceID, "MIB assignment")

	respondJSON(w, http.StatusOK, s.describeAssignment(r.Context(), deviceID, req.MibID))
}

// MIBAssignmentResult is what an assignment will produce, reported back to whoever
// made it.
//
// The assignment used to answer 204, which is honest about the storage write
// and silent about the outcome. A module can be assigned successfully and yield
// nothing — ATEN-PRODUCTS-MIB is six OBJECT IDENTIFIER registrations, so the
// only symptom was an empty dashboard some minutes later, with no way to tell a
// module that produces nothing from one that has not been polled yet.
type MIBAssignmentResult struct {
	MIB    string `json:"mib"`
	Device string `json:"device"`

	// Objects, Pollable and Unresolved describe the module as stored.
	Objects    int `json:"objects"`
	Pollable   int `json:"pollable"`
	Unresolved int `json:"unresolved"`

	// Series is what the cost estimator expects the assignment to add.
	Series int `json:"series"`

	// Warning is empty when the module will produce data.
	Warning string `json:"warning,omitempty"`
}

// describeAssignment reports what was just assigned.
//
// It runs after the write, so nothing here can fail the assignment: the MIB is
// attached whether or not it can be described, and a description that cannot be
// produced leaves the counts at zero rather than reporting a bogus warning.
func (s *Server) describeAssignment(ctx context.Context, deviceID, mibID int64) MIBAssignmentResult {
	assignment := MIBAssignmentResult{}

	device, err := s.storage.GetDevice(ctx, deviceID)
	if err != nil || device == nil {
		return assignment
	}
	assignment.Device = device.Name

	stored, err := s.storage.GetMIB(ctx, mibID)
	if err != nil || stored == nil {
		return assignment
	}
	assignment.MIB = stored.Name

	// One read of the module serves both the pollability check and the cost
	// estimate; estimateObjectSeries exists for exactly this.
	objects, err := s.moduleOIDs(stored.Name)
	if err != nil {
		log.Printf("Cannot describe assignment of %s to %s: %v", stored.Name, device.Name, err)
		return assignment
	}

	pollability := assessPollability(stored.Name, objects)
	assignment.Objects = pollability.Objects
	assignment.Pollable = pollability.Pollable
	assignment.Unresolved = pollability.Unresolved
	assignment.Warning = pollability.Warning

	if estimate := s.estimateObjectSeries(ctx, device.Name, stored.Name, objects); estimate != nil {
		assignment.Series = estimate.Series
	}

	return assignment
}

// handleRemoveMIBFromDevice removes a MIB from a device
func (s *Server) handleRemoveMIBFromDevice(w http.ResponseWriter, r *http.Request) {
	s.editDeviceLink(w, r, "id", "mibId", "MIB", "MIB removal", s.storage.RemoveMIBFromDevice)
}

// handleAssignProfileToDevice assigns a profile to a device
func (s *Server) handleAssignProfileToDevice(w http.ResponseWriter, r *http.Request) {
	s.editDeviceLink(w, r, "deviceId", "profileId", "profile", "profile assignment",
		s.storage.AssignProfileToDevice)
}

// handleRemoveProfileFromDevice removes a profile from a device
func (s *Server) handleRemoveProfileFromDevice(w http.ResponseWriter, r *http.Request) {
	s.editDeviceLink(w, r, "deviceId", "profileId", "profile", "profile removal",
		s.storage.RemoveProfileFromDevice)
}

// handleGetDeviceProfiles retrieves all profiles assigned to a device
func (s *Server) handleGetDeviceProfiles(w http.ResponseWriter, r *http.Request) {
	listByPathID(w, r, "id", "device", "profiles", s.storage.GetDeviceProfiles)
}

// editDeviceLink applies op to the association between a device and one related
// record, both named by numeric path variables, and pushes the result to the
// poller. deviceVar and otherVar name the mux variables, otherLabel names the
// related record in a 400, and reason labels the poller sync.
func (s *Server) editDeviceLink(
	w http.ResponseWriter, r *http.Request,
	deviceVar, otherVar, otherLabel, reason string,
	op func(ctx context.Context, deviceID, otherID int64) error,
) {
	vars := mux.Vars(r)
	deviceID, err := strconv.ParseInt(vars[deviceVar], 10, 64)
	if err != nil {
		http.Error(w, "invalid device ID", http.StatusBadRequest)
		return
	}

	otherID, err := strconv.ParseInt(vars[otherVar], 10, 64)
	if err != nil {
		http.Error(w, "invalid "+otherLabel+" ID", http.StatusBadRequest)
		return
	}

	if err := op(r.Context(), deviceID, otherID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.syncDeviceWithPoller(r.Context(), deviceID, reason)

	w.WriteHeader(http.StatusNoContent)
}

// listByPathID answers a route carrying one numeric path variable with a
// single-key JSON envelope. idLabel names the record in a 400 and key names the
// envelope field.
func listByPathID[T any](
	w http.ResponseWriter, r *http.Request,
	idVar, idLabel, key string,
	load func(ctx context.Context, id int64) ([]T, error),
) {
	id, err := strconv.ParseInt(mux.Vars(r)[idVar], 10, 64)
	if err != nil {
		http.Error(w, "invalid "+idLabel+" ID", http.StatusBadRequest)
		return
	}

	items, err := load(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{key: items})
}

// syncDeviceWithPoller pushes a device's current module set to the poller so
// that a change takes effect without restarting it.
//
// The poller replaces a device's worker on update, and it is that replacement
// which recomputes the OIDs the device polls — a worker chooses its OIDs once,
// when it starts. A failure here is logged rather than reported: the change is
// already durable, and the poller picks it up on its next configuration sync.
//
// Because the outcome is never reported, this push must never be what makes a
// request time out. PollerSyncBudget caps it, retries included, well inside the
// handler's own budget: the caller waits for a durable write, not for a
// best-effort notification to a service that may be slow or gone.
func (s *Server) syncDeviceWithPoller(ctx context.Context, deviceID int64, reason string) {
	if s.pollerClient == nil {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, PollerSyncBudget)
	defer cancel()

	device, err := s.storage.GetDevice(ctx, deviceID)
	if err != nil {
		log.Printf("Warning: failed to load device %d after %s: %v", deviceID, reason, err)
		return
	}

	mibNames, err := s.getDeviceMIBNames(ctx, deviceID)
	if err != nil {
		log.Printf("Warning: failed to get MIB names for device %s after %s: %v", device.Name, reason, err)
		return
	}

	if err := s.pollerClient.UpdateDevice(ctx, device, mibNames); err != nil {
		log.Printf("Warning: failed to update device %s in poller after %s: %v", device.Name, reason, err)
	}
}

// getDeviceMIBNames returns the names of the MIB modules a device polls.
//
// A MIB whose assignment is disabled is left out, so telling the poller this
// list is what stops or resumes polling of a module.
func (s *Server) getDeviceMIBNames(ctx context.Context, deviceID int64) ([]string, error) {
	selection, err := s.resolveDeviceModules(ctx, deviceID)
	if err != nil {
		return nil, err
	}
	return selection.Modules, nil
}

// respondJSON writes a JSON response. The status line is already on the wire by
// the time the body is encoded, so an encoding failure can only be logged.
func respondJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("api: encoding JSON response: %v", err)
	}
}
