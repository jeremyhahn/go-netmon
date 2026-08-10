package metrics

import "fmt"

// ConfigError reports invalid configuration passed to a metrics exporter
// constructor.
type ConfigError struct {
	Field  string
	Reason string
}

func (e *ConfigError) Error() string {
	return fmt.Sprintf("metrics: invalid %s: %s", e.Field, e.Reason)
}

// StorageUnavailableError reports a metrics component that needs MIB
// metadata but was constructed without a MIB store.
type StorageUnavailableError struct {
	Operation string
}

func (e *StorageUnavailableError) Error() string {
	return fmt.Sprintf("metrics: cannot %s: no MIB storage configured", e.Operation)
}

// OIDListError reports a failure to enumerate the OIDs held in MIB storage.
type OIDListError struct {
	Cause error
}

func (e *OIDListError) Error() string {
	return fmt.Sprintf("metrics: failed to list OIDs from MIB storage: %v", e.Cause)
}

func (e *OIDListError) Unwrap() error {
	return e.Cause
}

// MarshalError reports a failure to serialize a Prometheus remote-write
// WriteRequest.
type MarshalError struct {
	Cause error
}

func (e *MarshalError) Error() string {
	return fmt.Sprintf("metrics: failed to marshal WriteRequest: %v", e.Cause)
}

func (e *MarshalError) Unwrap() error {
	return e.Cause
}

// RequestError reports a failure to construct the outbound HTTP request for
// a Prometheus remote-write flush.
type RequestError struct {
	URL   string
	Cause error
}

func (e *RequestError) Error() string {
	return fmt.Sprintf("metrics: failed to create remote write request to %s: %v", e.URL, e.Cause)
}

func (e *RequestError) Unwrap() error {
	return e.Cause
}

// SendError reports a failure to deliver a Prometheus remote-write request,
// e.g. a network error or timeout.
type SendError struct {
	URL   string
	Cause error
}

func (e *SendError) Error() string {
	return fmt.Sprintf("metrics: failed to send remote write request to %s: %v", e.URL, e.Cause)
}

func (e *SendError) Unwrap() error {
	return e.Cause
}

// RemoteWriteError reports a Prometheus remote-write endpoint that
// responded with a non-2xx status.
type RemoteWriteError struct {
	URL        string
	StatusCode int
	Body       string
}

func (e *RemoteWriteError) Error() string {
	return fmt.Sprintf("metrics: remote write to %s failed with status %d: %s", e.URL, e.StatusCode, e.Body)
}
