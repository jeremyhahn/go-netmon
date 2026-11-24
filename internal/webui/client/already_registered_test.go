package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// aStoredDevice is a device as the web UI holds it.
func aStoredDevice(name string) *storage.Device {
	return &storage.Device{
		Name:        name,
		Address:     "192.0.2.14",
		SNMPVersion: "v2c",
		Community:   "public",
		Port:        161,
	}
}

// TestRegisteringADeviceThePollerAlreadyHasIsASuccess checks a second registration of the same device succeeds.
func TestRegisteringADeviceThePollerAlreadyHasIsASuccess(t *testing.T) {
	var registered atomic.Bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// A poller that holds the device refuses to replace it, and says so.
		if registered.Swap(true) {
			http.Error(w, `{"error":"failed to register device","message":"device bastion already exists"}`,
				http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	c := NewPollerClient(server.URL, time.Second)
	device := aStoredDevice("bastion")

	if err := c.RegisterDevice(context.Background(), device, nil, nil); err != nil {
		t.Fatalf("first RegisterDevice: %v", err)
	}
	if err := c.RegisterDevice(context.Background(), device, nil, nil); err != nil {
		t.Fatalf("second RegisterDevice = %v, want nil: the device is registered, "+
			"which is the outcome the call exists to reach", err)
	}
}

// TestAnAlreadyRegisteredDeviceIsNotRetried checks a 409 is not retried.
func TestAnAlreadyRegisteredDeviceIsNotRetried(t *testing.T) {
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		http.Error(w, `{"message":"device bastion already exists"}`, http.StatusConflict)
	}))
	defer server.Close()

	c := NewPollerClient(server.URL, time.Second)

	if err := c.RegisterDevice(context.Background(), aStoredDevice("bastion"), nil, nil); err != nil {
		t.Fatalf("RegisterDevice: %v", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Errorf("requests = %d, want 1; an already-registered device must not be retried", got)
	}
}

// TestRegistrationFailuresAreStillFailures checks only 409 counts as already registered.
func TestRegistrationFailuresAreStillFailures(t *testing.T) {
	tests := []struct {
		name   string
		status int
	}{
		{"the poller failed", http.StatusInternalServerError},
		{"the poller is unavailable", http.StatusServiceUnavailable},
		{"the request was refused as invalid", http.StatusBadRequest},
		{"the device could not be found to register", http.StatusNotFound},
		{"the poller is a gateway that could not reach it", http.StatusBadGateway},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, `{"error":"nope"}`, tt.status)
			}))
			defer server.Close()

			c := NewPollerClient(server.URL, time.Second)

			err := c.RegisterDevice(context.Background(), aStoredDevice("bastion"), nil, nil)
			if err == nil {
				t.Fatalf("RegisterDevice = nil for HTTP %d, want an error: "+
					"the device was not registered", tt.status)
			}
		})
	}
}

// TestARegistrationThatReachesNoPollerIsAFailure covers the case with no HTTP
// status at all, which is the one most easily mistaken for "already there":
// nothing is known about the device, so nothing can be assumed about it.
func TestARegistrationThatReachesNoPollerIsAFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close() // nothing is listening now

	c := NewPollerClient(url, 200*time.Millisecond)

	if err := c.RegisterDevice(context.Background(), aStoredDevice("bastion"), nil, nil); err == nil {
		t.Fatal("RegisterDevice = nil with no poller listening, want an error")
	}
}
