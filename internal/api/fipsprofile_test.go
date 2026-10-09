package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/fipsprofile"
)

// fipsV3Body is a registration the FIPS profile accepts: SNMPv3 authPriv with
// SHA-2 authentication and AES privacy.
func fipsV3Body(t *testing.T, name string) RegisterDeviceRequest {
	t.Helper()

	body := registerBody(t, name)
	body.SNMPVersion = "3"
	body.Community = ""
	body.Username = "monitor"
	body.AuthProtocol = "SHA256"
	body.AuthPassword = "authpassword"
	body.PrivProtocol = "AES"
	body.PrivPassword = "privpassword"
	return body
}

// TestHandleUpdateDeviceRefusedByTheFIPSProfileKeepsTheWorker pins the order of
// the update: a replacement the profile refuses must be answered 400 before the
// running worker is removed, not after, or a valid device is left unmonitored.
//
// It does not run in parallel: the profile override is process-wide, and the
// parallel tests resume only once every serial test has restored it.
func TestHandleUpdateDeviceRefusedByTheFIPSProfileKeepsTheWorker(t *testing.T) {
	defer fipsprofile.Force(true)()

	handler := newTestHandler(t)

	w := httptest.NewRecorder()
	handler.HandleRegisterDevice(w, request(t, http.MethodPost, "/api/poller/devices", fipsV3Body(t, "core")))
	if w.Code != http.StatusCreated {
		t.Fatalf("register: status = %d, want 201. Body: %s", w.Code, w.Body.String())
	}

	tests := []struct {
		name   string
		mutate func(*RegisterDeviceRequest)
	}{
		{"SNMPv2c", func(b *RegisterDeviceRequest) {
			b.SNMPVersion = "2c"
			b.Community = "public"
		}},
		{"MD5 authentication", func(b *RegisterDeviceRequest) { b.AuthProtocol = "MD5" }},
		{"DES privacy", func(b *RegisterDeviceRequest) { b.PrivProtocol = "DES" }},
		{"no privacy", func(b *RegisterDeviceRequest) { b.PrivProtocol = "" }},
		{"an unknown authentication protocol", func(b *RegisterDeviceRequest) { b.AuthProtocol = "SHA3" }},
		{"an unknown privacy protocol", func(b *RegisterDeviceRequest) { b.PrivProtocol = "3DES" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := fipsV3Body(t, "core")
			body.PollInterval = 120
			tt.mutate(&body)

			w := httptest.NewRecorder()
			handler.HandleUpdateDevice(w, request(t, http.MethodPut, "/api/poller/devices/core", body))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("update: status = %d, want 400. Body: %s", w.Code, w.Body.String())
			}

			status, polling := handler.poller.WorkerStatus("core")
			if !polling {
				t.Fatal("the refused update removed the running worker")
			}
			if status.Interval.Seconds() != 60 {
				t.Errorf("the worker polls every %s, want the original 60s", status.Interval)
			}
		})
	}
}

// TestHandleRegisterDeviceRefusedByTheFIPSProfileIs400 pins that a refusal is a
// client error on the registration path too, not a 500.
func TestHandleRegisterDeviceRefusedByTheFIPSProfileIs400(t *testing.T) {
	defer fipsprofile.Force(true)()

	handler := newTestHandler(t)
	body := registerBody(t, "legacy") // SNMPv2c

	w := httptest.NewRecorder()
	handler.HandleRegisterDevice(w, request(t, http.MethodPost, "/api/poller/devices", body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("register: status = %d, want 400. Body: %s", w.Code, w.Body.String())
	}
	if _, polling := handler.poller.WorkerStatus("legacy"); polling {
		t.Error("a refused registration started a worker")
	}
}
