package api

import (
	"encoding/json"
	"sync"
	"time"
)

// DeviceStatus is a device's runtime status; a zero LastPollTime means never polled.
type DeviceStatus struct {
	Name             string            `json:"name"`
	Connected        bool              `json:"connected"`
	LastPollTime     time.Time         `json:"last_poll_time"`
	LastPollSuccess  bool              `json:"last_poll_success"`
	LastError        string            `json:"last_error,omitempty"`
	MetricsCollected int               `json:"metrics_collected"`
	PollInterval     string            `json:"poll_interval"`
	Tags             map[string]string `json:"tags,omitempty"`

	// Unreachable is set while the device answers no SNMP at all and is retried with backoff.
	Unreachable bool `json:"unreachable"`

	// ConsecutiveFailures counts the polls in a row the device answered no part
	// of, and is what NextPollTime is backed off by.
	ConsecutiveFailures int `json:"consecutive_failures,omitempty"`

	// NextPollTime is the next attempt; absent from JSON until the first poll returns (see MarshalJSON).
	NextPollTime time.Time `json:"next_poll_time,omitempty"`
}

// MarshalJSON omits next_poll_time while no retry is scheduled; omitempty does not work on time.Time.
func (s DeviceStatus) MarshalJSON() ([]byte, error) {
	// The local type sheds the method set, so json.Marshal below does not call
	// this method again. The shadowing pointer field sits at depth 0 and wins
	// over the embedded one at depth 1, which is what lets omitempty apply.
	type alias DeviceStatus
	out := struct {
		alias
		NextPollTime *time.Time `json:"next_poll_time,omitempty"`
	}{alias: alias(s)}

	if !s.NextPollTime.IsZero() {
		out.NextPollTime = &s.NextPollTime
	}

	return json.Marshal(out)
}

// StatusTracker maintains runtime status information for all monitored devices
// It provides thread-safe access to device status information
type StatusTracker struct {
	mu       sync.RWMutex
	statuses map[string]*DeviceStatus
}

// NewStatusTracker creates a new StatusTracker instance
func NewStatusTracker() *StatusTracker {
	return &StatusTracker{
		statuses: make(map[string]*DeviceStatus),
	}
}

// GetStatus retrieves the status for a specific device
// Returns nil if device is not found
func (st *StatusTracker) GetStatus(name string) *DeviceStatus {
	st.mu.RLock()
	defer st.mu.RUnlock()

	status, exists := st.statuses[name]
	if !exists {
		return nil
	}

	// Return a copy to prevent external modifications
	statusCopy := *status
	if status.Tags != nil {
		statusCopy.Tags = make(map[string]string, len(status.Tags))
		for k, v := range status.Tags {
			statusCopy.Tags[k] = v
		}
	}

	return &statusCopy
}

// GetAllStatuses retrieves status for all devices
// Returns a map of device name to status
func (st *StatusTracker) GetAllStatuses() map[string]*DeviceStatus {
	st.mu.RLock()
	defer st.mu.RUnlock()

	result := make(map[string]*DeviceStatus, len(st.statuses))
	for name, status := range st.statuses {
		statusCopy := *status
		if status.Tags != nil {
			statusCopy.Tags = make(map[string]string, len(status.Tags))
			for k, v := range status.Tags {
				statusCopy.Tags[k] = v
			}
		}
		result[name] = &statusCopy
	}

	return result
}

// UpdateStatus updates the status for a device
// If the device doesn't exist, it creates a new status entry
func (st *StatusTracker) UpdateStatus(name string, status DeviceStatus) {
	st.mu.Lock()
	defer st.mu.Unlock()

	// Copy the status to prevent external modifications
	statusCopy := status
	if status.Tags != nil {
		statusCopy.Tags = make(map[string]string, len(status.Tags))
		for k, v := range status.Tags {
			statusCopy.Tags[k] = v
		}
	}

	st.statuses[name] = &statusCopy
}

// UpdatePollSuccess updates the status after a successful poll
func (st *StatusTracker) UpdatePollSuccess(name string, metricsCollected int) {
	st.mu.Lock()
	defer st.mu.Unlock()

	status, exists := st.statuses[name]
	if !exists {
		status = &DeviceStatus{
			Name: name,
		}
		st.statuses[name] = status
	}

	status.Connected = true
	status.LastPollTime = time.Now()
	status.LastPollSuccess = true
	status.LastError = ""
	status.MetricsCollected = metricsCollected
}

// UpdatePollError updates the status after a failed poll
func (st *StatusTracker) UpdatePollError(name string, err error) {
	st.mu.Lock()
	defer st.mu.Unlock()

	status, exists := st.statuses[name]
	if !exists {
		status = &DeviceStatus{
			Name: name,
		}
		st.statuses[name] = status
	}

	status.Connected = false
	status.LastPollTime = time.Now()
	status.LastPollSuccess = false
	if err != nil {
		status.LastError = err.Error()
	}
}

// RemoveDevice removes a device from the status tracker
func (st *StatusTracker) RemoveDevice(name string) {
	st.mu.Lock()
	defer st.mu.Unlock()

	delete(st.statuses, name)
}

// InitializeDevice initializes status for a new device
func (st *StatusTracker) InitializeDevice(name, pollInterval string, tags map[string]string) {
	st.mu.Lock()
	defer st.mu.Unlock()

	tagsCopy := make(map[string]string, len(tags))
	for k, v := range tags {
		tagsCopy[k] = v
	}

	st.statuses[name] = &DeviceStatus{
		Name:            name,
		Connected:       false,
		LastPollSuccess: false,
		PollInterval:    pollInterval,
		Tags:            tagsCopy,
	}
}

// DeviceExists checks if a device exists in the tracker
func (st *StatusTracker) DeviceExists(name string) bool {
	st.mu.RLock()
	defer st.mu.RUnlock()

	_, exists := st.statuses[name]
	return exists
}

// GetDeviceCount returns the total number of tracked devices
func (st *StatusTracker) GetDeviceCount() int {
	st.mu.RLock()
	defer st.mu.RUnlock()

	return len(st.statuses)
}
