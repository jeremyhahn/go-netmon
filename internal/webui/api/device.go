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
		modules, err := s.getDeviceModules(r.Context(), device.ID)
		if err != nil {
			log.Printf("Warning: failed to get device modules: %v", err)
			modules = moduleSelection{}
		}

		if err := s.pollerClient.RegisterDevice(r.Context(), &device, modules.Modules, modules.Filters); err != nil {
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

	// An update that omits enabled keeps the stored value.
	if device.Enabled == nil {
		if stored, err := s.storage.GetDevice(r.Context(), id); err == nil {
			device.Enabled = stored.Enabled
		}
	}

	// Update device in storage
	if err := s.storage.UpdateDevice(r.Context(), &device); err != nil {
		writeDeviceWriteError(w, err)
		return
	}

	// Update device in poller (graceful degradation - don't fail if poller is down)
	if s.pollerClient != nil {
		modules, err := s.getDeviceModules(r.Context(), device.ID)
		if err != nil {
			log.Printf("Warning: failed to get device modules: %v", err)
			modules = moduleSelection{}
		}

		if err := s.pollerClient.UpdateDevice(r.Context(), &device, modules.Modules, modules.Filters); err != nil {
			log.Printf("Warning: failed to update device in poller: %v", err)
			// Don't fail - device is updated in DB
		}
	}

	respondJSON(w, http.StatusOK, newDeviceView(device, s.polling()))
}

// writeDeviceWriteError answers 400 for a rejected poll interval, 500 otherwise.
func writeDeviceWriteError(w http.ResponseWriter, err error) {
	var invalidInterval *storage.InvalidPollIntervalError
	if errors.As(err, &invalidInterval) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

// handleDeleteDevice detaches the device from the poller first and deletes the record only once the poller confirms; a delete fails while the poller is unreachable.
func (s *Server) handleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id, err := strconv.ParseInt(vars["id"], 10, 64)
	if err != nil {
		http.Error(w, "invalid device ID", http.StatusBadRequest)
		return
	}

	// The name is read first because the poller is addressed by name, and
	// storage is the only thing that knows it.
	device, err := s.storage.GetDevice(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Stop the polling before touching the record. A failure here deletes
	// nothing, which is the point: the device stays whole rather than half.
	if err := s.detachFromPoller(r.Context(), device.Name); err != nil {
		s.log().WarnContext(r.Context(), "a device was not deleted because the poller is still polling it",
			"device", device.Name, "error", err)
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	if err := s.storage.DeleteDevice(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// detachFromPoller stops the poller polling a device within PollerSyncBudget; no poller, or a 404 from it, counts as detached.
func (s *Server) detachFromPoller(ctx context.Context, name string) error {
	if s.pollerClient == nil {
		return nil
	}

	callCtx, cancel := context.WithTimeout(ctx, PollerSyncBudget)
	defer cancel()

	if err := s.pollerClient.RemoveDevice(callCtx, name); err != nil {
		return &PollerDetachError{Device: name, Err: err}
	}
	return nil
}

// handleGetDeviceMIBs returns a device's MIBs with their direct and effective assignments.
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

// describeAssignment reports what was just assigned; it cannot fail the assignment.
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

// editDeviceLink applies op to a device's link with one related record and pushes the result to the poller.
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

// syncDeviceWithPoller pushes a device's module set to the poller, best effort, within PollerSyncBudget.
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

	modules, err := s.getDeviceModules(ctx, deviceID)
	if err != nil {
		log.Printf("Warning: failed to get modules for device %s after %s: %v", device.Name, reason, err)
		return
	}

	if err := s.pollerClient.UpdateDevice(ctx, device, modules.Modules, modules.Filters); err != nil {
		log.Printf("Warning: failed to update device %s in poller after %s: %v", device.Name, reason, err)
	}
}

// getDeviceModules returns the modules a device polls with their filters; disabled MIBs are left out.
func (s *Server) getDeviceModules(ctx context.Context, deviceID int64) (moduleSelection, error) {
	return s.resolveDeviceModules(ctx, deviceID)
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
