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

// ClosedError reports samples offered to a closed exporter.
type ClosedError struct {
	Samples int
}

func (e *ClosedError) Error() string {
	return fmt.Sprintf("metrics: exporter is closed; refused %d sample(s) rather than queue them where nothing can send them", e.Samples)
}

// DroppedError reports samples discarded before writing; Reason matches gonetmon_export_dropped_samples_total and is empty when mixed.
type DroppedError struct {
	Samples int
	Reason  string
}

func (e *DroppedError) Error() string {
	if phrase, named := dropPhrase[e.Reason]; named {
		return fmt.Sprintf("metrics: dropped %d sample(s) before they were written: %s (%s)",
			e.Samples, phrase, e.Reason)
	}
	return fmt.Sprintf("metrics: dropped %d sample(s) before they were written", e.Samples)
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

// Entity sensor conversion errors, one type per cause, each carrying its gonetmon_entity_sensor_dropped_total reason.

// sensorDropError is implemented by every error that ends a sensor reading, so
// the conversion path can label its counter without a type switch that has to
// be extended in step with the errors.
type sensorDropError interface {
	error
	// DropReason is the value of the "reason" label the reading is counted
	// under.
	DropReason() string
}

// SensorScaleError reports an entSensorScale outside the EntitySensorDataScale
// enumeration, whose factor of a thousand is therefore unknown.
type SensorScaleError struct {
	Scale int32
}

func (e *SensorScaleError) Error() string {
	return fmt.Sprintf("metrics: entity sensor scale %d is outside EntitySensorDataScale (1..17)", e.Scale)
}

// DropReason implements sensorDropError.
func (e *SensorScaleError) DropReason() string { return "invalid_scale" }

// SensorPrecisionError reports an entSensorPrecision outside the -8..9 range
// both sensor MIBs declare.
type SensorPrecisionError struct {
	Precision int32
}

func (e *SensorPrecisionError) Error() string {
	return fmt.Sprintf("metrics: entity sensor precision %d is outside EntitySensorPrecision (-8..9)", e.Precision)
}

// DropReason implements sensorDropError.
func (e *SensorPrecisionError) DropReason() string { return "invalid_precision" }

// SensorRangeError reports one of the reserved EntitySensorValue sentinels:
// the sensor's true reading is above or below what the object can carry, so
// there is no measurement to publish.
type SensorRangeError struct {
	Value int64
}

func (e *SensorRangeError) Error() string {
	bound := "overflow"
	if e.Value <= SensorValueUnderflow {
		bound = "underflow"
	}
	return fmt.Sprintf("metrics: entity sensor value %d signals %s", e.Value, bound)
}

// DropReason implements sensorDropError.
func (e *SensorRangeError) DropReason() string {
	if e.Value <= SensorValueUnderflow {
		return "underflow"
	}
	return "overflow"
}

// SensorMalformedValueError reports an EntitySensorValue outside the range its
// sensor type permits and not one of the reserved sentinels: the agent sent
// data its own MIB forbids, so there is no reading to publish.
type SensorMalformedValueError struct {
	Value int64
	Type  SensorType
}

func (e *SensorMalformedValueError) Error() string {
	return fmt.Sprintf("metrics: entity sensor value %d is outside the range of %s sensors", e.Value, e.Type.Label())
}

// DropReason implements sensorDropError.
func (e *SensorMalformedValueError) DropReason() string { return "malformed_value" }

// SensorMetadataError reports a reading whose type, scale or precision the device has not reported.
type SensorMetadataError struct {
	Device  string
	Index   string
	Missing string
}

func (e *SensorMetadataError) Error() string {
	return fmt.Sprintf("metrics: no %s for sensor %s on %s: reading cannot be scaled", e.Missing, e.Index, e.Device)
}

// DropReason implements sensorDropError.
func (e *SensorMetadataError) DropReason() string { return "missing_" + e.Missing }

// SensorStatusError reports a sensor whose status is not ok(1), so its value is not a measurement.
type SensorStatusError struct {
	Device string
	Index  string
	Status SensorStatus
}

func (e *SensorStatusError) Error() string {
	return fmt.Sprintf("metrics: sensor %s on %s reports status %s, not ok", e.Index, e.Device, e.Status)
}

// DropReason implements sensorDropError.
func (e *SensorStatusError) DropReason() string {
	// Only statuses both MIBs declare get their own reason; others share one label to bound cardinality.
	switch e.Status {
	case SensorStatusUnavailable, SensorStatusNonoperational:
		return "status_" + e.Status.String()
	default:
		return "status_unknown"
	}
}
