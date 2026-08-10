package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gorilla/mux"

	"github.com/jeremyhahn/go-netmon/internal/webui/client"
)

// deviceStatus is the single implementation of "what is this device's status".
//
// Both per-device endpoints and the fleet endpoint go through it, so there is
// one place that decides what the poller was asked, and one place that decides
// what its refusal meant. The distinctions it draws — no poller configured, a
// poller that has never heard of the device, a poller that could not be reached
// — are the same distinctions a fleet view needs per row and a single-device
// response needs as an HTTP status code, which is why they are returned as
// typed errors rather than baked into either representation.
func (s *Server) deviceStatus(ctx context.Context, deviceName string) (*client.DeviceStatus, error) {
	if deviceName == "" {
		return nil, &DeviceNameRequiredError{}
	}
	if s.pollerClient == nil {
		return nil, &PollerUnconfiguredError{}
	}

	status, err := s.pollerClient.GetDeviceStatus(ctx, deviceName)
	if err != nil {
		if isUnknownToPoller(err) {
			return nil, &DeviceNotPolledError{Device: deviceName, Err: err}
		}
		return nil, &PollerUnavailableError{Device: deviceName, Err: err}
	}

	return status, nil
}

// isUnknownToPoller says whether the poller answered that it has no such
// device, as opposed to failing to answer.
//
// The poller signals it with a 404, but the web UI's poller client flattens the
// response into a message, so this reads the message. It is the predicate the
// per-device endpoint has always used; it lives here so the fleet endpoint
// cannot come to a different conclusion about the same error.
func isUnknownToPoller(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "not found") || strings.Contains(msg, "404")
}

// writeDeviceStatusError maps a status failure onto the HTTP status codes the
// per-device endpoints have always returned.
func writeDeviceStatusError(w http.ResponseWriter, err error) {
	var nameRequired *DeviceNameRequiredError
	if errors.As(err, &nameRequired) {
		http.Error(w, "device name is required", http.StatusBadRequest)
		return
	}

	var unconfigured *PollerUnconfiguredError
	if errors.As(err, &unconfigured) {
		http.Error(w, "poller client not configured", http.StatusServiceUnavailable)
		return
	}

	log.Printf("Warning: failed to get device status from poller: %v", err)

	var notPolled *DeviceNotPolledError
	if errors.As(err, &notPolled) {
		http.Error(w, "device not found in poller", http.StatusNotFound)
		return
	}

	http.Error(w, "poller service unavailable", http.StatusServiceUnavailable)
}

// handleGetDeviceStatus handles GET /api/devices/{id}/status
// Fetches device status from the main poller service
func (s *Server) handleGetDeviceStatus(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	idStr := vars["id"]

	// Parse device ID
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid device ID", http.StatusBadRequest)
		return
	}

	// Get device from storage to retrieve the name
	device, err := s.storage.GetDevice(r.Context(), id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, "device not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	status, err := s.deviceStatus(r.Context(), device.Name)
	if err != nil {
		writeDeviceStatusError(w, err)
		return
	}

	respondJSON(w, http.StatusOK, status)
}

// handleGetDeviceStatusByName handles GET /api/devices/status/{name}
// Alternative endpoint to fetch status by device name
func (s *Server) handleGetDeviceStatusByName(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	status, err := s.deviceStatus(r.Context(), vars["name"])
	if err != nil {
		writeDeviceStatusError(w, err)
		return
	}

	respondJSON(w, http.StatusOK, status)
}
