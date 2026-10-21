package api

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// storedDeviceNames lists the devices the web UI currently holds.
func storedDeviceNames(t *testing.T, s *Server) []string {
	t.Helper()

	devices, err := s.storage.ListDevices(context.Background())
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}

	names := make([]string, 0, len(devices))
	for _, d := range devices {
		names = append(names, d.Name)
	}
	return names
}

// TestADeviceIsNotDeletedWhileThePollerStillPollsIt is the regression test for
// the defect.
//
// Deleting used to remove the record first and ask the poller second, and to
// discard the poller's failure: the response was 204, the device vanished from
// the device list and the fleet view, and the poller went on polling it
// indefinitely with nothing left in the UI to act on. The invariant now is that
// a device is not deleted until nothing is polling it, so a poller that cannot
// be told to stop leaves the device exactly as it was.
func TestADeviceIsNotDeletedWhileThePollerStillPollsIt(t *testing.T) {
	s := newTestServer(t)
	poller := &recordingPoller{err: errors.New("poller is down")}
	s.pollerClient = poller

	id := storeDevice(t, s, "bastion", "192.0.2.14")

	rec := do(t, s, http.MethodDelete, "/api/devices/"+itoa(id), "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("delete status = %d, want %d (body %q)",
			rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}

	// The failure is in the response, not only in the log.
	if body := rec.Body.String(); body == "" {
		t.Error("the response carries no reason")
	}

	// Nothing was half-done: the device is still there, and still deletable.
	if names := storedDeviceNames(t, s); len(names) != 1 || names[0] != "bastion" {
		t.Errorf("devices = %v, want [bastion]: a failed detach must delete nothing", names)
	}
	if rec := do(t, s, http.MethodGet, "/api/devices/"+itoa(id), ""); rec.Code != http.StatusOK {
		t.Errorf("get after the failed delete = %d, want 200: the device must stay actionable", rec.Code)
	}

	// And the poller was asked, so the operator's retry is not a no-op.
	if got := poller.calls(&poller.removed); len(got) != 1 || got[0] != "bastion" {
		t.Errorf("poller removals = %v, want [bastion]", got)
	}
}

// TestDeletingADeviceStopsThePollerFirst pins the order, which is the whole
// fix. Asserting only the 204 would pass under either order.
func TestDeletingADeviceStopsThePollerFirst(t *testing.T) {
	s := newTestServer(t)

	// A poller that reports what storage held at the moment it was asked. If
	// the record were deleted first, this would see the device already gone.
	seen := make(chan []string, 1)
	s.pollerClient = &recordingPoller{onRemove: func() {
		seen <- storedDeviceNames(t, s)
	}}

	id := storeDevice(t, s, "bastion", "192.0.2.14")

	rec := do(t, s, http.MethodDelete, "/api/devices/"+itoa(id), "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204 (body %q)", rec.Code, rec.Body.String())
	}

	present := <-seen
	if len(present) != 1 || present[0] != "bastion" {
		t.Errorf("storage held %v when the poller was asked, want [bastion]: "+
			"the record must still exist while the detach is in flight", present)
	}

	// And afterwards it is gone.
	if names := storedDeviceNames(t, s); len(names) != 0 {
		t.Errorf("devices after delete = %v, want none", names)
	}
}

// TestDeletingADeviceWithNoPollerConfigured covers the trivial case: with no
// poller there is nothing polling the device, so the precondition holds and the
// delete proceeds. The web UI stays usable on its own.
func TestDeletingADeviceWithNoPollerConfigured(t *testing.T) {
	s := newTestServer(t)
	s.pollerClient = nil

	id := storeDevice(t, s, "bastion", "192.0.2.14")

	rec := do(t, s, http.MethodDelete, "/api/devices/"+itoa(id), "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204 (body %q)", rec.Code, rec.Body.String())
	}
	if names := storedDeviceNames(t, s); len(names) != 0 {
		t.Errorf("devices after delete = %v, want none", names)
	}
}

// TestAStalledPollerCannotHoldADeleteOpen keeps the detach inside the same
// budget every other push to the poller runs under.
//
// Reporting the poller's outcome only helps if the report arrives. A poller
// that has stopped answering must not hold the delete open until the server's
// write timeout closes the connection with nothing written.
func TestAStalledPollerCannotHoldADeleteOpen(t *testing.T) {
	s := newTestServer(t)
	s.pollerClient = &recordingPoller{stall: true}

	id := storeDevice(t, s, "bastion", "192.0.2.14")

	start := time.Now()
	rec := do(t, s, http.MethodDelete, "/api/devices/"+itoa(id), "")
	elapsed := time.Since(start)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("delete status = %d, want %d: a poller that never answers has not stopped polling",
			rec.Code, http.StatusServiceUnavailable)
	}
	if elapsed >= ServerWriteTimeout {
		t.Fatalf("the request took %v, past ServerWriteTimeout (%v): the connection would be closed unanswered",
			elapsed, ServerWriteTimeout)
	}
	if elapsed > PollerSyncBudget+time.Second {
		t.Errorf("the request took %v, which overruns PollerSyncBudget (%v)", elapsed, PollerSyncBudget)
	}

	// A delete that timed out deleted nothing.
	if names := storedDeviceNames(t, s); len(names) != 1 {
		t.Errorf("devices = %v, want [bastion]", names)
	}
}

// TestDeletingAnUnknownDeviceAsksNoPoller covers the bad-input path: an id
// storage does not hold never reaches the poller, so a delete of nothing cannot
// disturb a device that is being polled.
func TestDeletingAnUnknownDeviceAsksNoPoller(t *testing.T) {
	s := newTestServer(t)
	poller := &recordingPoller{}
	s.pollerClient = poller

	if rec := do(t, s, http.MethodDelete, "/api/devices/4242", ""); rec.Code == http.StatusNoContent {
		t.Fatal("deleting an unknown device reported success")
	}
	if got := poller.calls(&poller.removed); len(got) != 0 {
		t.Errorf("poller removals = %v, want none", got)
	}
}

// TestADeleteThatFailedCanBeRetried is what makes the invariant liveable: the
// operator's recourse is to try again, so a retry against a poller that has
// since come back must complete.
func TestADeleteThatFailedCanBeRetried(t *testing.T) {
	s := newTestServer(t)
	failing := &recordingPoller{err: errors.New("poller is down")}
	s.pollerClient = failing

	id := storeDevice(t, s, "bastion", "192.0.2.14")

	if rec := do(t, s, http.MethodDelete, "/api/devices/"+itoa(id), ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("first delete status = %d, want 503", rec.Code)
	}

	// The poller is back.
	s.pollerClient = &recordingPoller{}

	if rec := do(t, s, http.MethodDelete, "/api/devices/"+itoa(id), ""); rec.Code != http.StatusNoContent {
		t.Fatalf("retried delete status = %d, want 204", rec.Code)
	}
	if names := storedDeviceNames(t, s); len(names) != 0 {
		t.Errorf("devices after the retry = %v, want none", names)
	}
}
