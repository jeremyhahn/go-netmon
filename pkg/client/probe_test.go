package client

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// A failed SNMP probe is not an API failure. The server answers an unreachable
// device with a gateway status and the same body a successful probe returns, so
// the negative result is data a caller can render — "no response from
// 198.51.100.101: timeout" — instead of an opaque "504 Gateway Timeout" that says
// nothing about which device or why. Anything that is not a probe result has to
// stay an error, or a genuine server fault would be displayed as a tidy
// negative probe.

// probeServer answers every request with the given status and body.
func probeServer(t *testing.T, status int, body string) *Client {
	t.Helper()
	return newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
}

func TestTestSNMPRecoversProbeResultFromGatewayError(t *testing.T) {
	const body = `{"success":false,"message":"no response from 198.51.100.101: request timeout"}`
	c := probeServer(t, http.StatusGatewayTimeout, body)

	resp, err := c.TestSNMP(context.Background(), SNMPTestRequest{Address: "198.51.100.101"})
	if err != nil {
		t.Fatalf("a failed probe was reported as an API error: %v", err)
	}
	if resp == nil {
		t.Fatal("TestSNMP returned no result and no error")
	}
	if resp.Success {
		t.Error("Success = true for a probe the device did not answer")
	}
	if !strings.Contains(resp.Message, "198.51.100.101") {
		t.Errorf("Message = %q, want it to name the device that did not answer", resp.Message)
	}
}

func TestTestDeviceSNMPRecoversProbeResultFromGatewayError(t *testing.T) {
	const body = `{"success":false,"message":"no response from core-01"}`
	c := probeServer(t, http.StatusBadGateway, body)

	resp, err := c.TestDeviceSNMP(context.Background(), 1)
	if err != nil {
		t.Fatalf("a failed probe was reported as an API error: %v", err)
	}
	if resp.Success {
		t.Error("Success = true for a probe the device did not answer")
	}
}

// TestProbeResultKeepsGenuineFailuresAsErrors is the other half of the
// contract. A body that is not a probe result must not be laundered into one,
// or a routing mistake or an authentication failure would show up in the UI as
// a device that merely did not answer.
func TestProbeResultKeepsGenuineFailuresAsErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{
			name:   "not JSON at all",
			status: http.StatusBadGateway,
			body:   "<html><body>502 Bad Gateway</body></html>",
		},
		{
			name:   "JSON without a probe message",
			status: http.StatusInternalServerError,
			body:   `{"error":"database unavailable"}`,
		},
		{
			name:   "a successful probe body on an error status",
			status: http.StatusInternalServerError,
			body:   `{"success":true,"message":"reachable"}`,
		},
		{
			name:   "empty body",
			status: http.StatusNotFound,
			body:   ``,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := probeServer(t, tt.status, tt.body)

			resp, err := c.TestSNMP(context.Background(), SNMPTestRequest{Address: "198.51.100.101"})
			if err == nil {
				t.Fatalf("a %d response was converted into probe result %+v", tt.status, resp)
			}
			if resp != nil {
				t.Error("a result was returned alongside an error")
			}

			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("got %T, want *APIError", err)
			}
			if apiErr.StatusCode != tt.status {
				t.Errorf("StatusCode = %d, want %d", apiErr.StatusCode, tt.status)
			}
		})
	}
}

// TestProbeResultLeavesTransportErrorsAlone checks the path where the request
// never reached the server: there is no body to recover a probe result from, so
// it must surface as the transport failure it is.
func TestProbeResultLeavesTransportErrorsAlone(t *testing.T) {
	c, err := New("http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// A canceled context fails before anything leaves the process.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	resp, probeErr := c.TestSNMP(ctx, SNMPTestRequest{Address: "198.51.100.101"})
	if probeErr == nil {
		t.Fatalf("a transport failure was converted into probe result %+v", resp)
	}

	var apiErr *APIError
	if errors.As(probeErr, &apiErr) {
		t.Error("a transport failure was reported as an API error")
	}
}
