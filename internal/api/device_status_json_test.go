package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The assertions here are on the SERIALIZED JSON rather than on the Go value,
// because that is the only place the defect they cover is visible: the struct
// field is a time.Time either way, and `omitempty` on a struct is silently a
// no-op. A test that round-tripped through the Go type would have passed
// against the broken version.

func TestDeviceStatusOmitsNextPollTimeUntilARetryIsScheduled(t *testing.T) {
	t.Parallel()

	// A device registered but still inside its first poll: the worker records
	// the next attempt only after that poll returns.
	status := DeviceStatus{Name: "bastion", PollInterval: "60s"}

	data, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if strings.Contains(string(data), "next_poll_time") {
		t.Errorf("next_poll_time is present for a device with no retry scheduled: %s", data)
	}

	// Deliberately scoped to next_poll_time rather than banning "0001-01-01"
	// from the payload outright: last_poll_time emits the zero time on purpose,
	// which TestDeviceStatusAlwaysCarriesLastPollTime pins. A blanket check
	// would fail on the field it is supposed to protect.
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if v, present := decoded["next_poll_time"]; present {
		t.Errorf("next_poll_time decoded as %v, want the key to be absent", v)
	}
}

func TestDeviceStatusCarriesNextPollTimeOnceScheduled(t *testing.T) {
	t.Parallel()

	want := time.Date(2026, 8, 21, 17, 30, 0, 0, time.UTC)
	data, err := json.Marshal(DeviceStatus{Name: "bastion", NextPollTime: want})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got struct {
		NextPollTime *time.Time `json:"next_poll_time"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.NextPollTime == nil {
		t.Fatalf("next_poll_time is absent for a scheduled retry: %s", data)
	}
	if !got.NextPollTime.Equal(want) {
		t.Errorf("next_poll_time = %s, want %s", got.NextPollTime, want)
	}
}

// LastPollTime is the deliberate counterpart: it carries no omitempty and its
// zero value is the documented signal for "never polled", so it must keep being
// emitted. Pinning that stops a later "tidy up the zero times" change from
// removing a field consumers rely on being present.
func TestDeviceStatusAlwaysCarriesLastPollTime(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal(DeviceStatus{Name: "bastion"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if !strings.Contains(string(data), `"last_poll_time"`) {
		t.Errorf("last_poll_time is missing for a never-polled device: %s", data)
	}
}

// The custom marshaller must not drop the rest of the struct, which is the
// failure mode of the embedded-alias idiom when the shadowing is written wrong.
func TestDeviceStatusMarshalKeepsEveryOtherField(t *testing.T) {
	t.Parallel()

	status := DeviceStatus{
		Name:                "bastion",
		Connected:           true,
		LastPollTime:        time.Date(2026, 8, 21, 17, 0, 0, 0, time.UTC),
		LastPollSuccess:     true,
		LastError:           "",
		MetricsCollected:    42,
		PollInterval:        "60s",
		Tags:                map[string]string{"site": "hq"},
		Unreachable:         true,
		ConsecutiveFailures: 3,
		NextPollTime:        time.Date(2026, 8, 21, 17, 30, 0, 0, time.UTC),
	}

	data, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got DeviceStatus
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.Name != status.Name || got.MetricsCollected != status.MetricsCollected ||
		got.PollInterval != status.PollInterval || got.Tags["site"] != "hq" ||
		!got.Connected || !got.LastPollSuccess || !got.Unreachable ||
		got.ConsecutiveFailures != status.ConsecutiveFailures {
		t.Errorf("fields were lost in the round trip:\n got %+v\nwant %+v", got, status)
	}
	if !got.LastPollTime.Equal(status.LastPollTime) {
		t.Errorf("LastPollTime = %s, want %s", got.LastPollTime, status.LastPollTime)
	}
	if !got.NextPollTime.Equal(status.NextPollTime) {
		t.Errorf("NextPollTime = %s, want %s", got.NextPollTime, status.NextPollTime)
	}
}

// An absent next_poll_time must decode back to the zero time, so the round trip
// through the API preserves "not yet scheduled" rather than turning it into an
// error or a bogus instant.
func TestDeviceStatusDecodesAnAbsentNextPollTimeAsZero(t *testing.T) {
	t.Parallel()

	var got DeviceStatus
	if err := json.Unmarshal([]byte(`{"name":"bastion","poll_interval":"60s"}`), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if !got.NextPollTime.IsZero() {
		t.Errorf("NextPollTime = %s, want the zero time for an absent field", got.NextPollTime)
	}
}
