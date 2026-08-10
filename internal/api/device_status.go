package api

import (
	"sync"
	"time"
)

// DeviceStatus represents the runtime status of a monitored device.
//
// LastPollTime is the zero time until the device has been polled at all, which
// is what tells "never polled" from "polled and failed": a failed poll is still
// a poll and still stamps this.
type DeviceStatus struct {
	Name             string            `json:"name"`
	Connected        bool              `json:"connected"`
	LastPollTime     time.Time         `json:"last_poll_time"`
	LastPollSuccess  bool              `json:"last_poll_success"`
	LastError        string            `json:"last_error,omitempty"`
	MetricsCollected int               `json:"metrics_collected"`
	PollInterval     string            `json:"poll_interval"`
	Tags             map[string]string `json:"tags,omitempty"`

	// Unreachable is set while the device answers no SNMP at all and is being
	// retried on a widening interval. It is deliberately narrower than
	// !LastPollSuccess: a device that answers some objects and errors on
	// others has a failed poll but is reachable, and is still collecting.
	Unreachable bool `json:"unreachable"`

	// ConsecutiveFailures counts the polls in a row the device answered no part
	// of, and is what NextPollTime is backed off by.
	ConsecutiveFailures int `json:"consecutive_failures,omitempty"`

	// NextPollTime is when the poller will next try the device.
	NextPollTime time.Time `json:"next_poll_time,omitempty"`
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
