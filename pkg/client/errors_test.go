package client

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestErrorMessages checks the text users actually see. These strings are the
// CLI's entire diagnostic surface when something goes wrong, so they must name
// the operation and the cause.
func TestErrorMessages(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want []string
	}{
		{
			name: "APIError includes status and body",
			err:  &APIError{StatusCode: 404, Method: "GET", Path: "/api/devices/9", Body: "device not found"},
			want: []string{"GET", "/api/devices/9", "404", "Not Found", "device not found"},
		},
		{
			name: "APIError without a body",
			err:  &APIError{StatusCode: 500, Method: "POST", Path: "/api/devices"},
			want: []string{"POST", "/api/devices", "500", "Internal Server Error"},
		},
		{
			name: "APIError with an unmapped status",
			err:  &APIError{StatusCode: 418, Method: "GET", Path: "/api/x"},
			want: []string{"418"},
		},
		{
			name: "TransportError names the URL and cause",
			err:  &TransportError{Method: "GET", URL: "http://host:8081/api/devices", Err: errors.New("connection refused")},
			want: []string{"GET", "http://host:8081/api/devices", "connection refused"},
		},
		{
			name: "DecodeError names the path",
			err:  &DecodeError{Path: "/api/mibs", Err: errors.New("unexpected EOF")},
			want: []string{"/api/mibs", "unexpected EOF"},
		},
		{
			name: "ConfigError names the field",
			err:  &ConfigError{Field: "server URL", Reason: "missing host"},
			want: []string{"server URL", "missing host"},
		},
		{
			name: "NotFoundError quotes the name",
			err:  &NotFoundError{Kind: "device", Name: "core-99"},
			want: []string{"device", `"core-99"`},
		},
		{
			name: "AmbiguousNameError suggests using the ID",
			err:  &AmbiguousNameError{Kind: "device", Name: "core", Matches: 3},
			want: []string{"3", "device", `"core"`, "numeric ID"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := tt.err.Error()
			for _, want := range tt.want {
				if !strings.Contains(msg, want) {
					t.Errorf("message %q is missing %q", msg, want)
				}
			}
		})
	}
}

func TestErrorUnwrap(t *testing.T) {
	cause := errors.New("root cause")

	transport := &TransportError{Method: "GET", URL: "http://x", Err: cause}
	if !errors.Is(transport, cause) {
		t.Error("TransportError does not unwrap to its cause")
	}

	decode := &DecodeError{Path: "/api/x", Err: cause}
	if !errors.Is(decode, cause) {
		t.Error("DecodeError does not unwrap to its cause")
	}
}

func TestAPIErrorNotFound(t *testing.T) {
	tests := []struct {
		status int
		want   bool
	}{
		{http.StatusNotFound, true},
		{http.StatusInternalServerError, false},
		{http.StatusBadRequest, false},
	}

	for _, tt := range tests {
		err := &APIError{StatusCode: tt.status}
		if got := err.NotFound(); got != tt.want {
			t.Errorf("NotFound() for %d = %v, want %v", tt.status, got, tt.want)
		}
	}
}

func TestOptions(t *testing.T) {
	custom := &http.Client{Timeout: 5 * time.Second}

	c, err := New("http://localhost:8081", WithHTTPClient(custom))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.http != custom {
		t.Error("WithHTTPClient did not install the supplied client")
	}

	c, err = New("http://localhost:8081", WithTimeout(3*time.Second))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.http.Timeout != 3*time.Second {
		t.Errorf("Timeout = %v, want 3s", c.http.Timeout)
	}
}
