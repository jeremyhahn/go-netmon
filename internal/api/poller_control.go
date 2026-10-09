// Package api exposes the poller's own HTTP control surface: device
// registration, poll status and health.
package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/poller"
)

// PollerControlHandler provides REST API endpoints for controlling the SNMP poller
type PollerControlHandler struct {
	poller        *poller.Poller
	statusTracker *StatusTracker
	config        *config.Config
}

// NewPollerControlHandler creates a new PollerControlHandler instance
func NewPollerControlHandler(p *poller.Poller, cfg *config.Config) *PollerControlHandler {
	return &PollerControlHandler{
		poller:        p,
		statusTracker: NewStatusTracker(),
		config:        cfg,
	}
}

// GetStatusTracker returns the status tracker instance
func (h *PollerControlHandler) GetStatusTracker() *StatusTracker {
	return h.statusTracker
}

// RegisterDeviceRequest represents the request body for registering a device
type RegisterDeviceRequest struct {
	Name         string            `json:"name"`
	Address      string            `json:"address"`
	SNMPVersion  string            `json:"snmp_version"`
	Community    string            `json:"community,omitempty"`
	Username     string            `json:"username,omitempty"`
	AuthProtocol string            `json:"auth_protocol,omitempty"`
	AuthPassword string            `json:"auth_password,omitempty"`
	PrivProtocol string            `json:"priv_protocol,omitempty"`
	PrivPassword string            `json:"priv_password,omitempty"`
	PollInterval int               `json:"poll_interval"`
	Enabled      bool              `json:"enabled"`
	Modules      []string          `json:"modules,omitempty"`
	Profile      string            `json:"profile"`
	Tags         map[string]string `json:"tags,omitempty"`

	// ModuleFilters narrows what is polled within a module, keyed by module
	// name. A module with no entry is polled in full, which is what every
	// module used to be: the web UI stored an operator's filters and pushed
	// only the module list, so the narrowing never reached the worker that
	// chooses the OIDs.
	ModuleFilters map[string]config.ModuleFilter `json:"module_filters,omitempty"`
}

// DeviceResponse represents a device in API responses
type DeviceResponse struct {
	Name         string            `json:"name"`
	Address      string            `json:"address"`
	SNMPVersion  string            `json:"snmp_version"`
	PollInterval int               `json:"poll_interval"`
	Enabled      bool              `json:"enabled"`
	Profile      string            `json:"profile"`
	Modules      []string          `json:"modules,omitempty"`
	Tags         map[string]string `json:"tags,omitempty"`
	Status       *DeviceStatus     `json:"status,omitempty"`
}

// ErrorResponse represents an error response
type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
}

// ListDevicesResponse represents the response for listing devices
type ListDevicesResponse struct {
	Devices []DeviceResponse `json:"devices"`
	Count   int              `json:"count"`
}

// HandleRegisterDevice handles POST /api/poller/devices - Register a new device
func (h *PollerControlHandler) HandleRegisterDevice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.respondError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}

	var req RegisterDeviceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}

	// Validate required fields
	if err := h.validateRegisterRequest(&req); err != nil {
		h.respondError(w, http.StatusBadRequest, "validation failed", err.Error())
		return
	}

	// Convert request to config.Device
	device := h.requestToDevice(&req)
	if err := device.Validate(); err != nil {
		h.respondError(w, http.StatusBadRequest, "validation failed", err.Error())
		return
	}

	// Add device to poller
	ctx := r.Context()
	if err := h.poller.AddDevice(ctx, device); err != nil {
		log.Printf("Failed to register device %s: %v", req.Name, err)
		h.respondError(w, http.StatusInternalServerError, "failed to register device", err.Error())
		return
	}

	// Initialize status tracker
	pollInterval := fmt.Sprintf("%ds", device.PollInterval)
	if device.PollInterval == 0 {
		pollInterval = fmt.Sprintf("%ds", h.config.Global.DefaultPollInterval)
	}
	h.statusTracker.InitializeDevice(device.Name, pollInterval, device.Tags)

	log.Printf("Successfully registered device: %s (address: %s, profile: %s)",
		device.Name, device.Address, device.Profile)

	// Return the created device
	resp := h.deviceToResponse(device, nil)
	h.respondJSON(w, http.StatusCreated, resp)
}

// HandleUpdateDevice handles PUT /api/poller/devices/{name} - Update an existing device
func (h *PollerControlHandler) HandleUpdateDevice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		h.respondError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}

	// Extract device name from URL path
	deviceName := h.extractDeviceName(r.URL.Path)
	if deviceName == "" {
		h.respondError(w, http.StatusBadRequest, "device name is required", "")
		return
	}

	var req RegisterDeviceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}

	// Ensure name matches URL
	if req.Name != "" && req.Name != deviceName {
		h.respondError(w, http.StatusBadRequest, "device name mismatch",
			"name in body must match name in URL")
		return
	}
	req.Name = deviceName

	// Validate request
	if err := h.validateRegisterRequest(&req); err != nil {
		h.respondError(w, http.StatusBadRequest, "validation failed", err.Error())
		return
	}

	// Convert request to config.Device and validate it BEFORE the running
	// worker is touched: AddDevice validates too, but by then the old worker
	// is gone, so a refused update (for example one the FIPS profile rejects)
	// would leave a valid device unmonitored.
	device := h.requestToDevice(&req)
	if err := device.Validate(); err != nil {
		h.respondError(w, http.StatusBadRequest, "validation failed", err.Error())
		return
	}

	ctx := r.Context()

	// Remove old device
	if err := h.poller.RemoveDevice(deviceName); err != nil {
		log.Printf("Warning: failed to remove old device %s: %v", deviceName, err)
	}

	// Add updated device
	if err := h.poller.AddDevice(ctx, device); err != nil {
		log.Printf("Failed to update device %s: %v", deviceName, err)
		h.respondError(w, http.StatusInternalServerError, "failed to update device", err.Error())
		return
	}

	// Update status tracker
	pollInterval := fmt.Sprintf("%ds", device.PollInterval)
	if device.PollInterval == 0 {
		pollInterval = fmt.Sprintf("%ds", h.config.Global.DefaultPollInterval)
	}
	h.statusTracker.InitializeDevice(device.Name, pollInterval, device.Tags)

	log.Printf("Successfully updated device: %s", deviceName)

	// Return the updated device
	resp := h.deviceToResponse(device, nil)
	h.respondJSON(w, http.StatusOK, resp)
}

// HandleRemoveDevice handles DELETE /api/poller/devices/{name} - Remove a device
func (h *PollerControlHandler) HandleRemoveDevice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		h.respondError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}

	// Extract device name from URL path
	deviceName := h.extractDeviceName(r.URL.Path)
	if deviceName == "" {
		h.respondError(w, http.StatusBadRequest, "device name is required", "")
		return
	}

	// Remove device from poller
	if err := h.poller.RemoveDevice(deviceName); err != nil {
		log.Printf("Failed to remove device %s: %v", deviceName, err)
		h.respondError(w, http.StatusNotFound, "device not found", err.Error())
		return
	}

	// Remove from status tracker
	h.statusTracker.RemoveDevice(deviceName)

	log.Printf("Successfully removed device: %s", deviceName)

	// Return success response
	h.respondJSON(w, http.StatusOK, map[string]string{
		"message": "device removed successfully",
		"device":  deviceName,
	})
}

// HandleListDevices handles GET /api/poller/devices - List all devices
func (h *PollerControlHandler) HandleListDevices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.respondError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}

	// Get worker status from poller
	workerStatus := h.poller.GetWorkerStatus()

	// Build device list with status
	devices := make([]DeviceResponse, 0, len(workerStatus))
	for _, worker := range workerStatus {
		// Get status from tracker, with the poller's own view of whether the
		// device is answering merged in.
		status := h.statusTracker.GetStatus(worker.DeviceName)
		if status == nil {
			status = neverPolled(worker)
		}
		applyReachability(status, worker)

		// Create device response from worker's device config
		device := DeviceResponse{
			Name:         worker.Device.Name,
			Address:      worker.Device.Address,
			SNMPVersion:  worker.Device.SNMPVersion,
			PollInterval: worker.Device.PollInterval,
			Enabled:      worker.Running,
			Profile:      worker.Device.Profile,
			Modules:      worker.Device.Modules,
			Tags:         worker.Device.Tags,
			Status:       status,
		}

		devices = append(devices, device)
	}

	resp := ListDevicesResponse{
		Devices: devices,
		Count:   len(devices),
	}

	h.respondJSON(w, http.StatusOK, resp)
}

// HandleGetDeviceStatus handles GET /api/poller/devices/{name}/status - Get device status
func (h *PollerControlHandler) HandleGetDeviceStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.respondError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}

	// Extract device name from URL path
	deviceName := h.extractDeviceName(r.URL.Path)
	if deviceName == "" {
		h.respondError(w, http.StatusBadRequest, "device name is required", "")
		return
	}

	status := h.deviceStatus(deviceName)
	if status == nil {
		h.respondError(w, http.StatusNotFound, "device not found",
			fmt.Sprintf("no status available for device: %s", deviceName))
		return
	}

	h.respondJSON(w, http.StatusOK, status)
}

// deviceStatus is a device's full runtime status: what its last poll produced,
// according to the status tracker, and whether the device is answering at all,
// according to the poller.
//
// The two sources answer different questions and neither is sufficient alone. A
// failed poll is not the same as an unreachable device — a device that answers
// some objects and errors on others fails polls forever while being perfectly
// healthy — and a device the poller has registered but never managed to poll
// has no tracker entry at all, which is a fact about it rather than grounds for
// reporting it unknown.
//
// It returns nil only when neither source has heard of the device.
func (h *PollerControlHandler) deviceStatus(deviceName string) *DeviceStatus {
	status := h.statusTracker.GetStatus(deviceName)

	worker, polling := h.poller.WorkerStatus(deviceName)
	if !polling {
		return status
	}

	if status == nil {
		status = neverPolled(worker)
	}
	applyReachability(status, worker)

	return status
}

// neverPolled describes a device the poller has a worker for but which has not
// yet produced a poll result. Its zero LastPollTime is the point: it is what
// distinguishes a device that has never been polled from one whose polls fail.
func neverPolled(worker poller.WorkerStatus) *DeviceStatus {
	status := &DeviceStatus{
		Name:         worker.DeviceName,
		PollInterval: worker.Interval.String(),
	}
	if worker.Device != nil {
		status.Tags = worker.Device.Tags
	}
	return status
}

// applyReachability copies the poller's view of a device onto its status.
//
// The interval is copied here rather than only in neverPolled because the
// status tracker builds its entry from a poll result, which carries no
// interval — so a device that had polled reported an empty one while a device
// that never had reported it correctly, which is exactly backwards. The worker
// is the authority: it is what the interval is actually being applied by.
func applyReachability(status *DeviceStatus, worker poller.WorkerStatus) {
	status.Unreachable = worker.Unreachable
	status.ConsecutiveFailures = worker.ConsecutiveFailures
	status.NextPollTime = worker.NextPoll
	status.PollInterval = worker.Interval.String()
}

// Helper methods

// validateRegisterRequest validates the register device request
func (h *PollerControlHandler) validateRegisterRequest(req *RegisterDeviceRequest) error {
	if req.Name == "" {
		return fmt.Errorf("name is required")
	}
	if req.Address == "" {
		return fmt.Errorf("address is required")
	}
	if req.SNMPVersion == "" {
		return fmt.Errorf("snmp_version is required")
	}
	if req.SNMPVersion != "1" && req.SNMPVersion != "2c" && req.SNMPVersion != "3" {
		return fmt.Errorf("snmp_version must be '1', '2c' or '3'")
	}
	if (req.SNMPVersion == "1" || req.SNMPVersion == "2c") && req.Community == "" {
		return fmt.Errorf("community is required for SNMPv1 and SNMPv2c")
	}
	if req.SNMPVersion == "3" && req.Username == "" {
		return fmt.Errorf("username is required for SNMPv3")
	}
	if req.PollInterval < 0 {
		return fmt.Errorf("poll_interval must be non-negative")
	}

	return nil
}

// requestToDevice converts a RegisterDeviceRequest to a config.Device
func (h *PollerControlHandler) requestToDevice(req *RegisterDeviceRequest) *config.Device {
	device := &config.Device{
		Name:          req.Name,
		Address:       req.Address,
		SNMPVersion:   req.SNMPVersion,
		Community:     req.Community,
		Username:      req.Username,
		AuthProtocol:  req.AuthProtocol,
		AuthPassword:  req.AuthPassword,
		PrivProtocol:  req.PrivProtocol,
		PrivPassword:  req.PrivPassword,
		PollInterval:  req.PollInterval,
		Enabled:       req.Enabled,
		Modules:       req.Modules,
		ModuleFilters: req.ModuleFilters,
		Profile:       req.Profile,
		Tags:          req.Tags,
	}

	// Set defaults
	if device.Profile == "" {
		device.Profile = "default"
	}
	if device.PollInterval == 0 {
		device.PollInterval = h.config.Global.DefaultPollInterval
	}

	return device
}

// deviceToResponse converts a config.Device to a DeviceResponse
func (h *PollerControlHandler) deviceToResponse(device *config.Device, status *DeviceStatus) DeviceResponse {
	return DeviceResponse{
		Name:         device.Name,
		Address:      device.Address,
		SNMPVersion:  device.SNMPVersion,
		PollInterval: device.PollInterval,
		Enabled:      device.Enabled,
		Profile:      device.Profile,
		Modules:      device.Modules,
		Tags:         device.Tags,
		Status:       status,
	}
}

// extractDeviceName extracts the device name from URL path
// Expects paths like: /api/poller/devices/{name} or /api/poller/devices/{name}/status
func (h *PollerControlHandler) extractDeviceName(path string) string {
	// Remove trailing slash if present
	path = strings.TrimSuffix(path, "/")

	// Split path and extract device name
	parts := strings.Split(path, "/")

	// Expected format: ["", "api", "poller", "devices", "{name}"] or
	//                  ["", "api", "poller", "devices", "{name}", "status"]
	if len(parts) >= 5 {
		return parts[4]
	}

	return ""
}

// respondJSON sends a JSON response
func (h *PollerControlHandler) respondJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("Error encoding JSON response: %v", err)
	}
}

// respondError sends an error response
func (h *PollerControlHandler) respondError(w http.ResponseWriter, status int, reason, message string) {
	resp := ErrorResponse{
		Error:   reason,
		Message: message,
	}
	h.respondJSON(w, status, resp)
}
