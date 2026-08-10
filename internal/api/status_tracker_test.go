package api

import (
	"errors"
	"testing"
	"time"
)

// The status tracker is the poller's record of what each device's last poll
// produced. It is read by HTTP handlers while the result loop writes it, and it
// is the only thing that distinguishes a device that has never been polled from
// one whose polls fail — so both the copying and the timestamps below are load
// bearing.

func TestUpdatePollSuccessRecordsAConnectedDevice(t *testing.T) {
	t.Parallel()

	tracker := NewStatusTracker()
	tracker.InitializeDevice("core-01", "30s", map[string]string{"site": "hq"})

	before := time.Now()
	tracker.UpdatePollSuccess("core-01", 6610)

	status := tracker.GetStatus("core-01")
	if status == nil {
		t.Fatal("the device has no status after a successful poll")
	}
	if !status.Connected || !status.LastPollSuccess {
		t.Errorf("status = %+v, want a connected device whose last poll succeeded", status)
	}
	if status.MetricsCollected != 6610 {
		t.Errorf("metrics_collected = %d, want 6610", status.MetricsCollected)
	}
	if status.LastPollTime.Before(before) {
		t.Errorf("last_poll_time = %s, want the time of the poll just recorded", status.LastPollTime)
	}
	if status.PollInterval != "30s" {
		t.Errorf("poll_interval = %q, want the interval the device was initialized with", status.PollInterval)
	}
}

// TestUpdatePollErrorRecordsAFailedPoll is the "never polled" distinction: a
// failed poll is still a poll, and has to leave a timestamp and a reason behind
// it.
func TestUpdatePollErrorRecordsAFailedPoll(t *testing.T) {
	t.Parallel()

	tracker := NewStatusTracker()
	cause := errors.New("poller: device bastion answered none of 4 SNMP operations")

	if status := tracker.GetStatus("bastion"); status != nil {
		t.Fatalf("an unknown device already has a status: %+v", status)
	}

	tracker.UpdatePollError("bastion", cause)

	status := tracker.GetStatus("bastion")
	if status == nil {
		t.Fatal("a device whose poll failed has no status")
	}
	if status.Connected || status.LastPollSuccess {
		t.Errorf("status = %+v, want a device whose last poll failed", status)
	}
	if status.LastPollTime.IsZero() {
		t.Error("a failed poll left no timestamp, so it reads as never having been polled")
	}
	if status.LastError != cause.Error() {
		t.Errorf("last_error = %q, want %q", status.LastError, cause)
	}
}

// TestUpdatePollErrorWithoutAnError covers the degenerate call: the poll still
// failed, and the record must say so rather than reporting a success with no
// explanation.
func TestUpdatePollErrorWithoutAnError(t *testing.T) {
	t.Parallel()

	tracker := NewStatusTracker()
	tracker.UpdatePollError("bastion", nil)

	status := tracker.GetStatus("bastion")
	if status == nil {
		t.Fatal("the device has no status")
	}
	if status.LastPollSuccess {
		t.Error("a failed poll with no error attached was recorded as a success")
	}
	if status.LastError != "" {
		t.Errorf("last_error = %q, want it empty when no error was given", status.LastError)
	}
}

// TestARecoveredDeviceClearsItsLastError keeps a stale reason from outliving the
// failure that produced it.
func TestARecoveredDeviceClearsItsLastError(t *testing.T) {
	t.Parallel()

	tracker := NewStatusTracker()
	tracker.UpdatePollError("bastion", errors.New("request timeout (after 3 retries)"))
	tracker.UpdatePollSuccess("bastion", 12)

	status := tracker.GetStatus("bastion")
	if status.LastError != "" {
		t.Errorf("last_error = %q after a successful poll, want it cleared", status.LastError)
	}
	if !status.Connected {
		t.Error("a device that answered is still recorded as disconnected")
	}
}

// TestStatusesAreCopiedOnTheWayOut is what keeps a handler from mutating the
// tracker's state through the pointer it was handed.
func TestStatusesAreCopiedOnTheWayOut(t *testing.T) {
	t.Parallel()

	tags := map[string]string{"site": "hq"}
	tracker := NewStatusTracker()
	tracker.InitializeDevice("core-01", "30s", tags)

	// A caller that mutates what it was given, and a caller that mutates the
	// map it passed in.
	got := tracker.GetStatus("core-01")
	got.Name = "someone else"
	got.Tags["site"] = "elsewhere"
	tags["site"] = "elsewhere too"

	fresh := tracker.GetStatus("core-01")
	if fresh.Name != "core-01" {
		t.Errorf("name = %q, want the tracker's own value", fresh.Name)
	}
	if fresh.Tags["site"] != "hq" {
		t.Errorf("tags[site] = %q, want the tracker's own value", fresh.Tags["site"])
	}
}

func TestUpdateStatusReplacesTheWholeRecord(t *testing.T) {
	t.Parallel()

	tracker := NewStatusTracker()
	tracker.UpdatePollError("bastion", errors.New("request timeout"))

	replacement := DeviceStatus{
		Name:         "bastion",
		Connected:    true,
		PollInterval: "60s",
		Tags:         map[string]string{"rack": "4"},
	}
	tracker.UpdateStatus("bastion", replacement)

	status := tracker.GetStatus("bastion")
	if status.LastError != "" {
		t.Errorf("last_error = %q, want the replaced record's empty value", status.LastError)
	}
	if status.Tags["rack"] != "4" {
		t.Errorf("tags = %v, want the replacement's", status.Tags)
	}

	// The caller's map must not be the tracker's.
	replacement.Tags["rack"] = "9"
	if tracker.GetStatus("bastion").Tags["rack"] != "4" {
		t.Error("mutating the caller's map changed the tracked status")
	}
}

func TestGetAllStatusesReportsEveryDevice(t *testing.T) {
	t.Parallel()

	tracker := NewStatusTracker()
	tracker.InitializeDevice("core-01", "30s", map[string]string{"site": "hq"})
	tracker.InitializeDevice("bastion", "60s", nil)

	all := tracker.GetAllStatuses()
	if len(all) != 2 {
		t.Fatalf("got %d statuses, want 2", len(all))
	}
	all["core-01"].Tags["site"] = "elsewhere"
	if tracker.GetStatus("core-01").Tags["site"] != "hq" {
		t.Error("the listing handed out the tracker's own map")
	}
}

// TestTrackerForgetsARemovedDevice is the decommissioning case: a device nobody
// monitors any more must not keep appearing in the fleet view.
func TestTrackerForgetsARemovedDevice(t *testing.T) {
	t.Parallel()

	tracker := NewStatusTracker()
	tracker.InitializeDevice("bastion", "60s", nil)

	if !tracker.DeviceExists("bastion") || tracker.GetDeviceCount() != 1 {
		t.Fatalf("the device was not tracked: exists=%v count=%d",
			tracker.DeviceExists("bastion"), tracker.GetDeviceCount())
	}

	tracker.RemoveDevice("bastion")

	if tracker.DeviceExists("bastion") {
		t.Error("a removed device is still tracked")
	}
	if tracker.GetDeviceCount() != 0 {
		t.Errorf("device count = %d after removal, want 0", tracker.GetDeviceCount())
	}
	if status := tracker.GetStatus("bastion"); status != nil {
		t.Errorf("a removed device still has a status: %+v", status)
	}
}

// TestTrackerHandlesUnknownDevices is the bad-input half: every lookup for a
// name the tracker has never seen has to say so rather than invent a record.
func TestTrackerHandlesUnknownDevices(t *testing.T) {
	t.Parallel()

	tracker := NewStatusTracker()

	if status := tracker.GetStatus("no-such-device"); status != nil {
		t.Errorf("GetStatus for an unknown device = %+v, want nil", status)
	}
	if tracker.DeviceExists("no-such-device") {
		t.Error("DeviceExists reported an unknown device")
	}
	if got := len(tracker.GetAllStatuses()); got != 0 {
		t.Errorf("GetAllStatuses returned %d statuses for an empty tracker", got)
	}

	// Removing a device that was never tracked is not an error.
	tracker.RemoveDevice("no-such-device")
	if tracker.GetDeviceCount() != 0 {
		t.Errorf("device count = %d, want 0", tracker.GetDeviceCount())
	}
}
