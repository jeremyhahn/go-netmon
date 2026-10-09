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

// ClosedError reports an exporter asked to accept samples after it has been
// closed. Its sender has stopped, so anything accepted now would sit in a queue
// nothing can drain -- silently, which is the one thing this package must never
// do with data it has said yes to.
type ClosedError struct {
	Samples int
}

func (e *ClosedError) Error() string {
	return fmt.Sprintf("metrics: exporter is closed; refused %d sample(s) rather than queue them where nothing can send them", e.Samples)
}

// DroppedError reports samples discarded before they were ever written. It is
// what a caller waiting on a batch is told when its batch never reached the
// endpoint, and what Close returns when shutdown gave up on one.
//
// Reason is why, and it is the same vocabulary as
// gonetmon_export_dropped_samples_total{reason}: a reader who finds one of
// these in a log and that counter on a dashboard is looking at the same event
// described the same way.
//
// It was not carried, and the message named "bound queue memory" whatever had
// actually happened -- so a poll superseded by a newer one, or a batch the
// endpoint refused outright, reported backpressure. That is the wrong fault,
// stated confidently, in a package whose whole claim is that nothing leaves
// without something observable saying so. Saying so incorrectly is worse than
// the silence it replaced, because it is acted on.
//
// Reason is empty where a discard genuinely has more than one -- a Flush
// waiting behind several, or a shutdown that gave up on a mixed queue -- and
// the message then says only what is known rather than picking one.
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

// The errors below all report one thing: an entity sensor reading that cannot
// be turned into the quantity it represents. Each carries the reason label it
// is counted under, so a dropped reading is visible in
// gonetmon_entity_sensor_dropped_total broken down by cause rather than as an
// unexplained gap in a graph.
//
// They are separate types rather than one type with a code because callers
// distinguish them: a bad scale is a broken agent, a missing metadata column
// is a MIB module that was never assigned to the device, and a non-ok status
// is a sensor correctly reporting that it has nothing to say.

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

// SensorMetadataError reports a sensor reading whose type, scale or precision
// the device has not reported, leaving nothing to scale the reading by.
//
// In practice this means the sensor table was read without its metadata
// columns -- a device assigned no entity sensor MIB module, or polled for the
// value column alone.
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

// SensorStatusError reports a sensor whose operational status is not ok(1).
// unavailable(2) means the agent cannot presently read the sensor and
// nonoperational(3) means it believes the sensor is broken; in both cases the
// value column still holds a number, and that number is not a measurement.
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
	// Only the statuses both MIBs declare get their own reason; everything
	// else collapses to one label.
	//
	// This value is a Prometheus label on a counter and would otherwise come
	// straight from an Integer32 the device chose, so an agent answering a
	// fresh status per poll could mint unbounded label values -- from
	// untrusted input, on the very metric that exists to report untrusted
	// input. It also read "status_status_9", because Status.String() already
	// prefixes an undeclared value. The exact number stays in Error(), which
	// is where someone diagnosing one sensor looks.
	switch e.Status {
	case SensorStatusUnavailable, SensorStatusNonoperational:
		return "status_" + e.Status.String()
	default:
		return "status_unknown"
	}
}
