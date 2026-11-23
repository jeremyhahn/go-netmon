package client

import "strconv"

// APIError reports a non-2xx response from the management API.
type APIError struct {
	StatusCode int
	Method     string
	Path       string
	Body       string
}

func (e *APIError) Error() string {
	msg := "netmon api: " + e.Method + " " + e.Path + ": " +
		strconv.Itoa(e.StatusCode) + " " + statusText(e.StatusCode)
	if e.Body != "" {
		msg += ": " + e.Body
	}
	return msg
}

// NotFound reports whether the server said the resource does not exist, which
// callers distinguish from a transport failure.
func (e *APIError) NotFound() bool {
	return e.StatusCode == 404
}

// TransportError reports a failure to reach the management API at all.
//
// This is the error an operator hits most often and the one that says least on
// its own: "connection refused" from a URL the user may not have chosen — it
// can come from a default — is not a diagnosis. The message therefore names the
// two things worth checking before anything else, while the cause stays
// wrapped so callers can still inspect it with errors.As.
type TransportError struct {
	Method string
	URL    string
	Err    error
}

func (e *TransportError) Error() string {
	return "netmon api: cannot reach the server: " + e.Method + " " + e.URL + ": " +
		e.Err.Error() + "; check that go-netmon is running and that --server " +
		"(or NETMON_SERVER) points at its management API"
}

func (e *TransportError) Unwrap() error { return e.Err }

// DecodeError reports a response body the client could not parse.
type DecodeError struct {
	Path string
	Err  error
}

func (e *DecodeError) Error() string {
	return "netmon api: decode response from " + e.Path + ": " + e.Err.Error()
}

func (e *DecodeError) Unwrap() error { return e.Err }

// ConfigError reports an unusable client configuration.
type ConfigError struct {
	Field  string
	Reason string
}

func (e *ConfigError) Error() string {
	return "netmon api: invalid " + e.Field + ": " + e.Reason
}

// NotFoundError reports that a named resource could not be resolved to an ID.
type NotFoundError struct {
	Kind string
	Name string
}

func (e *NotFoundError) Error() string {
	return "netmon: no " + e.Kind + " named " + strconv.Quote(e.Name)
}

// PatternError reports a MIB filter pattern that is not a valid Go regular
// expression. It is raised before the request is sent, so the pattern named
// here never reached the server.
type PatternError struct {
	Field   string
	Pattern string
	Err     error
}

func (e *PatternError) Error() string {
	return "netmon: " + e.Field + " pattern " + strconv.Quote(e.Pattern) +
		" is not a valid regular expression: " + e.Err.Error()
}

func (e *PatternError) Unwrap() error { return e.Err }

// PollIntervalError reports a per-device poll interval that is not a schedule.
//
// It is returned before the request is sent, so the value named here never
// reached the server and never reached the poller. It says how to ask for the
// global default, because that is what an operator who wrote 0 or a negative
// number was almost certainly after.
type PollIntervalError struct {
	Device   string
	Interval int
}

func (e *PollIntervalError) Error() string {
	subject := "poll interval"
	if e.Device != "" {
		subject = "poll interval for device " + strconv.Quote(e.Device)
	}
	return "netmon: " + subject + " " + strconv.Itoa(e.Interval) +
		" is not a schedule; it must be at least 1 second, or unset to inherit " +
		"the server's global default"
}

// AmbiguousNameError reports a name matching more than one resource.
type AmbiguousNameError struct {
	Kind    string
	Name    string
	Matches int
}

func (e *AmbiguousNameError) Error() string {
	return "netmon: " + strconv.Itoa(e.Matches) + " " + e.Kind + "s match " +
		strconv.Quote(e.Name) + "; use the numeric ID"
}

// statusText maps the handful of status codes this API returns to text,
// avoiding a dependency on net/http from error formatting.
func statusText(code int) string {
	switch code {
	case 400:
		return "Bad Request"
	case 401:
		return "Unauthorized"
	case 403:
		return "Forbidden"
	case 404:
		return "Not Found"
	case 409:
		return "Conflict"
	case 500:
		return "Internal Server Error"
	case 502:
		return "Bad Gateway"
	case 503:
		return "Service Unavailable"
	default:
		return ""
	}
}
