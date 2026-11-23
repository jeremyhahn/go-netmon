package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestRemovingADeviceThePollerDoesNotHaveIsASuccess covers the answer that
// makes a failed delete retryable.
//
// A device is not deleted until nothing is polling it, so the question a remove
// asks is "are you polling this?". A poller that answers 404 is answering no,
// which is the condition the caller needs. Treating that as a failure would
// leave a device that had already been detached permanently undeletable: every
// retry would ask again, get 404 again, and refuse again.
func TestRemovingADeviceThePollerDoesNotHaveIsASuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"device not found","message":"device bastion not found"}`, http.StatusNotFound)
	}))
	defer server.Close()

	c := NewPollerClient(server.URL, time.Second)

	if err := c.RemoveDevice(context.Background(), "bastion"); err != nil {
		t.Fatalf("RemoveDevice = %v, want nil: a poller that does not have the device is not polling it", err)
	}
}

// TestRemovingADeviceThePollerHasIsASuccess is the ordinary case.
func TestRemovingADeviceThePollerHasIsASuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c := NewPollerClient(server.URL, time.Second)

	if err := c.RemoveDevice(context.Background(), "bastion"); err != nil {
		t.Fatalf("RemoveDevice: %v", err)
	}
}

// TestRemovalFailuresAreStillFailures guards the inversion.
//
// Only 404 -- the poller stating what it holds -- means the device is not being
// polled. Every other outcome leaves that unknown, and the delete depends on
// knowing, so an unknown must not read as a yes.
func TestRemovalFailuresAreStillFailures(t *testing.T) {
	tests := []struct {
		name   string
		status int
	}{
		{"the poller failed", http.StatusInternalServerError},
		{"the poller is unavailable", http.StatusServiceUnavailable},
		{"the request was refused", http.StatusBadRequest},
		{"the poller is a gateway that could not reach it", http.StatusBadGateway},
		{"the poller refused to say", http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, `{"error":"nope"}`, tt.status)
			}))
			defer server.Close()

			c := NewPollerClient(server.URL, time.Second)

			if err := c.RemoveDevice(context.Background(), "bastion"); err == nil {
				t.Fatalf("RemoveDevice = nil for HTTP %d, want an error: "+
					"the device may still be polled", tt.status)
			}
		})
	}
}

// TestARemovalThatReachesNoPollerIsAFailure is the case with no status at all,
// and the one most easily mistaken for "not there": nothing answered, so
// nothing is known, and the device may well still be polled.
func TestARemovalThatReachesNoPollerIsAFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close() // nothing is listening now

	c := NewPollerClient(url, 200*time.Millisecond)

	if err := c.RemoveDevice(context.Background(), "bastion"); err == nil {
		t.Fatal("RemoveDevice = nil with no poller listening, want an error")
	}
}
