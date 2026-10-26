// Package client talks to the poller's control API from the web UI.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/config"

	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// PollerClient provides HTTP client for calling the main poller service API
type PollerClient struct {
	baseURL    string
	httpClient *http.Client
	timeout    time.Duration
	maxRetries int
}

// PollerDeviceRequest represents the request body for poller device operations
type PollerDeviceRequest struct {
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
	Profile      string            `json:"profile"`
	Modules      []string          `json:"modules,omitempty"`
	Tags         map[string]string `json:"tags,omitempty"`

	// ModuleFilters narrows what is polled within a module, keyed by module
	// name. Without it the poller has only the module list and fetches every
	// pollable object of every module -- which is what happened to every filter
	// an operator set: stored, shown in the UI, counted by the cost estimator,
	// and then ignored by the thing actually doing the polling.
	ModuleFilters map[string]config.ModuleFilter `json:"module_filters,omitempty"`
}

// DeviceStatus represents the runtime status of a monitored device
type DeviceStatus struct {
	Name             string            `json:"name"`
	Connected        bool              `json:"connected"`
	LastPollTime     time.Time         `json:"last_poll_time"`
	LastPollSuccess  bool              `json:"last_poll_success"`
	LastError        string            `json:"last_error,omitempty"`
	MetricsCollected int               `json:"metrics_collected"`
	PollInterval     string            `json:"poll_interval"`
	Tags             map[string]string `json:"tags,omitempty"`

	// Unreachable is set while the poller is getting no SNMP answers at all
	// from the device and is retrying it on a widening interval. A device that
	// answers some objects and errors on others is not this: it is reachable
	// and still collecting.
	Unreachable bool `json:"unreachable"`

	// ConsecutiveFailures counts the polls in a row the device answered no part
	// of, and is what NextPollTime is backed off by.
	ConsecutiveFailures int `json:"consecutive_failures,omitempty"`

	// NextPollTime is when the poller will next try the device.
	//
	// ABSENT from the JSON means no retry is scheduled yet -- the state a device
	// is in for the whole of its first poll. Treat a missing next_poll_time as
	// "not yet known", not as an error. See MarshalJSON.
	NextPollTime time.Time `json:"next_poll_time,omitempty"`
}

// MarshalJSON omits next_poll_time while no retry has been scheduled.
//
// The `omitempty` tag cannot do this by itself: encoding/json treats only
// false, 0, nil pointers and interfaces, and empty arrays, slices, maps and
// strings as empty, and time.Time is a struct, so a zero value would marshal as
// "0001-01-01T00:00:00Z". This mirrors internal/api.DeviceStatus.MarshalJSON --
// the two shapes are one wire contract and must not drift.
//
// LastPollTime keeps its always-present zero deliberately: that is what
// distinguishes "never polled" from "polled and failed".
func (s DeviceStatus) MarshalJSON() ([]byte, error) {
	// The local type sheds the method set so this does not recurse; the
	// shadowing pointer field at depth 0 wins over the embedded one at depth 1.
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

// ListDevicesResponse represents the response from listing devices
type ListDevicesResponse struct {
	Devices []DeviceInfo `json:"devices"`
	Count   int          `json:"count"`
}

// DeviceInfo represents basic device information
type DeviceInfo struct {
	Name         string            `json:"name"`
	Address      string            `json:"address,omitempty"`
	SNMPVersion  string            `json:"snmp_version,omitempty"`
	PollInterval int               `json:"poll_interval"`
	Enabled      bool              `json:"enabled"`
	Profile      string            `json:"profile,omitempty"`
	Modules      []string          `json:"modules,omitempty"`
	Tags         map[string]string `json:"tags,omitempty"`
	Status       *DeviceStatus     `json:"status,omitempty"`
}

// NewPollerClient creates a new PollerClient instance
// If timeout is 0, defaults to 10 seconds
func NewPollerClient(baseURL string, timeout time.Duration) *PollerClient {
	if timeout == 0 {
		timeout = 10 * time.Second
	}

	return &PollerClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: timeout,
		},
		timeout:    timeout,
		maxRetries: 3,
	}
}

// RegisterDevice registers a device with the poller service.
//
// A poller that answers 409 already has the device. That is not a failure: the
// outcome this call exists to reach is a poller polling the device, and a
// poller that already polls it has reached it. The web UI re-registers every
// stored device each time it starts, so this is the ordinary case on any
// instance whose poller has been up longer than its web UI -- and treating it
// as a failure is what produced roughly 1,300 HTTP 500s a day on monitor-01, each one
// a device the web UI then recorded as unregistered while it was polling
// normally.
//
// The poller is not the thing that was wrong. "Already exists", for a device
// that does exist, is the correct answer to the question asked; it stays an
// error there, and the poller still refuses to overwrite a live worker behind
// the caller's back. What changes is that the caller stops reading a truthful
// answer as a fault.
//
// Deliberately narrow. Only 409 -- the poller stating that it holds this device
// -- is a success. Every other failure, including a poller that did not answer
// at all, stays an error, because "the poller did not say" is not the same as
// "the poller has it" and only one of those means the device is being polled.
//
// A disabled device is not sent at all. The poller's control API starts a
// worker for every device it is given, whatever the request's enabled field
// says, so registering a disabled device would poll it. Leaving it out is the
// only way this call can respect the flag.
func (c *PollerClient) RegisterDevice(
	ctx context.Context, device *storage.Device,
	mibNames []string, filters map[string]config.ModuleFilter,
) error {
	if !storage.EnabledOf(device) {
		log.Printf("Device %s is disabled; not registering it with the poller", device.Name)
		return nil
	}

	req := c.deviceToRequest(device, mibNames, filters)

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/api/poller/devices", c.baseURL)
	if _, err = c.doRequestWithRetry(ctx, http.MethodPost, url, body); err != nil {
		var status *httpError
		if errors.As(err, &status) && status.statusCode == http.StatusConflict {
			log.Printf("Device %s is already registered with the poller", device.Name)
			return nil
		}
		return fmt.Errorf("failed to register device: %w", err)
	}

	log.Printf("Successfully registered device %s with poller", device.Name)
	return nil
}

// UpdateDevice updates an existing device in the poller service.
//
// The poller replaces the device it holds, or adds it if it holds none, so an
// update is also how a device that was disabled starts polling again. Updating
// a disabled device removes it instead, for the reason RegisterDevice gives.
func (c *PollerClient) UpdateDevice(
	ctx context.Context, device *storage.Device,
	mibNames []string, filters map[string]config.ModuleFilter,
) error {
	if !storage.EnabledOf(device) {
		return c.RemoveDevice(ctx, device.Name)
	}

	req := c.deviceToRequest(device, mibNames, filters)

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/api/poller/devices/%s", c.baseURL, device.Name)
	_, err = c.doRequestWithRetry(ctx, http.MethodPut, url, body)
	if err != nil {
		return fmt.Errorf("failed to update device: %w", err)
	}

	log.Printf("Successfully updated device %s in poller", device.Name)
	return nil
}

// RemoveDevice removes a device from the poller service.
//
// A poller that answers 404 does not have the device, which is precisely the
// state this call exists to reach, so it is a success. Reporting it as a
// failure would make every re-issued delete -- and every delete of a device the
// poller had already dropped, which is any device added while the poller was
// down -- look like the poller and storage disagreeing when they agree.
//
// This is deliberately narrow: 404 is the poller stating what it holds. Every
// other failure, including one that leaves the answer unknown, stays an error,
// because "the poller did not say" is not the same as "the poller does not have
// it" and only one of the two is safe to call done.
func (c *PollerClient) RemoveDevice(ctx context.Context, deviceName string) error {
	url := fmt.Sprintf("%s/api/poller/devices/%s", c.baseURL, deviceName)
	_, err := c.doRequestWithRetry(ctx, http.MethodDelete, url, nil)
	if err != nil {
		var status *httpError
		if errors.As(err, &status) && status.statusCode == http.StatusNotFound {
			log.Printf("Device %s was already absent from the poller", deviceName)
			return nil
		}
		return fmt.Errorf("failed to remove device: %w", err)
	}

	log.Printf("Successfully removed device %s from poller", deviceName)
	return nil
}

// GetDeviceStatus retrieves the status of a device from the poller service
func (c *PollerClient) GetDeviceStatus(ctx context.Context, deviceName string) (*DeviceStatus, error) {
	url := fmt.Sprintf("%s/api/poller/devices/%s/status", c.baseURL, deviceName)
	respBody, err := c.doRequestWithRetry(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get device status: %w", err)
	}

	var status DeviceStatus
	if err := json.Unmarshal(respBody, &status); err != nil {
		return nil, fmt.Errorf("failed to unmarshal status response: %w", err)
	}

	return &status, nil
}

// ListDevices lists all devices from the poller service
func (c *PollerClient) ListDevices(ctx context.Context) ([]DeviceInfo, error) {
	url := fmt.Sprintf("%s/api/poller/devices", c.baseURL)
	respBody, err := c.doRequestWithRetry(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list devices: %w", err)
	}

	var response ListDevicesResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal list response: %w", err)
	}

	return response.Devices, nil
}

// deviceToRequest converts a storage.Device to a PollerDeviceRequest.
//
// The poll interval is the device's own, or 0 when the device has none. 0 is
// what the poller's control API reads as "use global.default_poll_interval", so
// a device that inherits arrives at the poller still inheriting: the default it
// gets is the poller's current one, not a copy of it taken when the device was
// last pushed.
func (c *PollerClient) deviceToRequest(
	device *storage.Device, mibNames []string, filters map[string]config.ModuleFilter,
) PollerDeviceRequest {
	// Create tags map
	tags := make(map[string]string)
	tags["source"] = "webui"

	// Normalize SNMP version: the web UI states it as "v1"/"v2c"/"v3" and the
	// poller as "1"/"2c"/"3". A version that arrives unnormalized is refused by
	// the poller's own validation, so all three are handled here.
	snmpVersion := device.SNMPVersion
	switch snmpVersion {
	case "v1":
		snmpVersion = "1"
	case "v2c":
		snmpVersion = "2c"
	case "v3":
		snmpVersion = "3"
	}

	return PollerDeviceRequest{
		Name:        device.Name,
		Address:     fmt.Sprintf("%s:%d", device.Address, device.Port),
		SNMPVersion: snmpVersion,
		Community:   device.Community,

		// Without these a v3 device reaches the agent with no username and is
		// refused with "unknown username" — which is what happened to every v3
		// device, because nothing carried them this far.
		Username:     device.Username,
		AuthProtocol: device.AuthProtocol,
		AuthPassword: device.AuthPassword,
		PrivProtocol: device.PrivProtocol,
		PrivPassword: device.PrivPassword,

		PollInterval:  storage.PollIntervalOf(device),
		Enabled:       storage.EnabledOf(device),
		Profile:       "default",
		Modules:       mibNames,
		ModuleFilters: filters,
		Tags:          tags,
	}
}

// doRequestWithRetry performs an HTTP request with exponential backoff retry
func (c *PollerClient) doRequestWithRetry(ctx context.Context, method, url string, body []byte) ([]byte, error) {
	var lastErr error

	for attempt := 0; attempt < c.maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 100ms, 200ms, 400ms, ...
			backoff := time.Duration(math.Pow(2, float64(attempt-1))) * 100 * time.Millisecond
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}

		respBody, err := c.doRequest(ctx, method, url, body)
		if err == nil {
			return respBody, nil
		}

		lastErr = err

		// Don't retry on client errors (4xx)
		if httpErr, ok := err.(*httpError); ok {
			if httpErr.statusCode >= 400 && httpErr.statusCode < 500 {
				return nil, err
			}
		}

		// Don't retry on context errors
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		log.Printf("Request attempt %d/%d failed: %v", attempt+1, c.maxRetries, err)
	}

	return nil, fmt.Errorf("request failed after %d attempts: %w", c.maxRetries, lastErr)
}

// doRequest performs a single HTTP request
func (c *PollerClient) doRequest(ctx context.Context, method, url string, body []byte) ([]byte, error) {
	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	// Closing the body only returns the connection to the pool; the call's
	// outcome is already determined by what was read above.
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &httpError{
			statusCode: resp.StatusCode,
			message:    string(respBody),
		}
	}

	return respBody, nil
}

// httpError represents an HTTP error response
type httpError struct {
	statusCode int
	message    string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.statusCode, e.message)
}
