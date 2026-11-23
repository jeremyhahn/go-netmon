package sdnotify

// AddressError reports a NOTIFY_SOCKET value that is not a shape systemd uses.
type AddressError struct {
	Addr   string
	Reason string
}

func (e *AddressError) Error() string {
	return "sdnotify: notification socket address " + quote(e.Addr) + ": " + e.Reason
}

// DialError reports a failure to open the notification socket.
type DialError struct {
	Addr string
	Err  error
}

func (e *DialError) Error() string {
	return "sdnotify: dial notification socket " + quote(e.Addr) + ": " + e.Err.Error()
}

func (e *DialError) Unwrap() error { return e.Err }

// SendError reports a state message that could not be delivered.
type SendError struct {
	State string
	Err   error
}

func (e *SendError) Error() string {
	return "sdnotify: send " + quote(e.State) + ": " + e.Err.Error()
}

func (e *SendError) Unwrap() error { return e.Err }

// CloseError reports a failure to close the notification socket.
type CloseError struct {
	Err error
}

func (e *CloseError) Error() string {
	return "sdnotify: close notification socket: " + e.Err.Error()
}

func (e *CloseError) Unwrap() error { return e.Err }

// quote wraps a value in quotes so that an empty or whitespace-only address
// reads as a value in the message rather than as a gap in the sentence.
func quote(s string) string { return `"` + s + `"` }
