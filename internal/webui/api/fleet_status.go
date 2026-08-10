package api

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/jeremyhahn/go-netmon/internal/webui/client"
	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// FleetStatusConcurrency bounds the poller requests one fleet status request
// has in flight at once.
//
// The status of a device costs the poller a map lookup, so the limit is not
// about load; it is about sockets. An unbounded fan-out over a large fleet
// would open one connection per device at once, and Go's default transport
// pools only a couple per host, so the rest would be built and torn down on
// every page load. Devices still queued behind the limit when the budget
// expires are reported as unknown rather than dropped.
const FleetStatusConcurrency = 16

// DeviceState summarizes a device's health for a fleet overview.
//
// It is a summary and not a new source of truth: every state is derived from
// the status the per-device endpoint returns, which travels alongside it. Its
// job is to make the distinctions a fleet card has to draw explicit, in
// particular the two that are easy to conflate — a device that has never been
// polled and a device whose polls fail — rather than leaving each client to
// rediscover that a zero last_poll_time means the former.
type DeviceState string

// The states a device can be in, from a fleet view's point of view.
const (
	// StateOK is a device whose most recent poll succeeded.
	StateOK DeviceState = "ok"

	// StateFailing is a device that answers SNMP but whose most recent poll did
	// not fully succeed. It is still collecting.
	StateFailing DeviceState = "failing"

	// StateUnreachable is a device the poller is getting no SNMP answers from
	// at all and is retrying on a widening interval.
	StateUnreachable DeviceState = "unreachable"

	// StateNeverPolled is a device the poller has registered but has not yet
	// produced a poll result for. Its status carries the zero last_poll_time
	// that says so.
	StateNeverPolled DeviceState = "never_polled"

	// StateNotRegistered is a device in the web UI's records that the poller
	// has no record of. Nothing is wrong with the device; something is wrong
	// with the configuration, and an operator has to act on it.
	StateNotRegistered DeviceState = "not_registered"

	// StateUnknown is a device whose status could not be obtained — the poller
	// was unreachable, too slow, or not configured. The row's error says which.
	StateUnknown DeviceState = "unknown"
)

// FleetDeviceStatus is one device's row in the fleet overview.
//
// Status is verbatim what GET /api/devices/{id}/status returns for the same
// device, nested rather than flattened, so the two endpoints cannot drift into
// describing a device differently. Around it sit only the fields a card needs
// to label and link the row, all of which come from the device record the fleet
// listing already read.
type FleetDeviceStatus struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Address string `json:"address"`

	State DeviceState `json:"state"`

	// PollInterval is the device's own interval in seconds, absent when it
	// inherits the global default, and EffectivePollInterval is the interval it
	// is polled at either way. They come from the device record this listing
	// already read, so a fleet view can label every row's schedule without a
	// request per device — and it can do so for a device the poller has never
	// heard of, whose Status carries nothing at all.
	PollInterval          *int `json:"poll_interval,omitempty"`
	EffectivePollInterval int  `json:"effective_poll_interval"`

	// Status is absent when none could be obtained; State and Error say why.
	Status *client.DeviceStatus `json:"status,omitempty"`

	// Error is set only when State is unknown — that is, only when the failure
	// was a failure to determine anything, rather than a determination that the
	// poller does not know the device.
	Error string `json:"error,omitempty"`
}

// FleetStatusResponse is the body of GET /api/devices/status.
//
// Devices is always an array, empty when nothing is configured, so a client can
// iterate it without a null check.
type FleetStatusResponse struct {
	Devices []FleetDeviceStatus `json:"devices"`
}

// handleFleetStatus handles GET /api/devices/status: live status for every
// configured device in one request.
//
// It exists because a fleet overview built on the per-device endpoint issues one
// request per device on every page load, which is invisible at four devices and
// is not at a hundred.
func (s *Server) handleFleetStatus(w http.ResponseWriter, r *http.Request) {
	devices, err := s.storage.ListDevices(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	respondJSON(w, http.StatusOK, FleetStatusResponse{Devices: s.fleetStatus(r.Context(), devices)})
}

// fleetStatus collects each device's status concurrently, in the order given.
//
// Three properties matter here and each is bought deliberately. Order comes
// from the caller's slice — the storage layer already sorts devices by name,
// and results are written back into the slot a device came from rather than
// appended as they arrive, so repeated requests return the same order and the
// UI does not reshuffle. Isolation comes from every device having its own
// deadline and its own error: a device the poller cannot answer for costs that
// row its status and nothing else. And boundedness comes from the whole
// collection sharing one budget, so the response arrives whether or not the
// poller does.
func (s *Server) fleetStatus(ctx context.Context, devices []storage.Device) []FleetDeviceStatus {
	ctx, cancel := context.WithTimeout(ctx, FleetStatusBudget)
	defer cancel()

	entries := make([]FleetDeviceStatus, len(devices))
	inFlight := make(chan struct{}, FleetStatusConcurrency)
	polling := s.polling()

	var wg sync.WaitGroup
	for i, device := range devices {
		wg.Add(1)
		go func() {
			defer wg.Done()

			select {
			case inFlight <- struct{}{}:
				defer func() { <-inFlight }()
			case <-ctx.Done():
				// The budget ran out while this device was still queued. It is
				// still a device, so it gets a row saying nothing is known.
				entries[i] = newFleetDeviceStatus(device, nil,
					&PollerUnavailableError{Device: device.Name, Err: ctx.Err()}, polling)
				return
			}

			deviceCtx, cancelDevice := context.WithTimeout(ctx, FleetStatusDeviceBudget)
			defer cancelDevice()

			status, err := s.deviceStatus(deviceCtx, device.Name)
			entries[i] = newFleetDeviceStatus(device, status, err, polling)
		}()
	}
	wg.Wait()

	return entries
}

// newFleetDeviceStatus builds one row from a device record and what the poller
// had to say about it.
func newFleetDeviceStatus(
	device storage.Device,
	status *client.DeviceStatus,
	err error,
	polling PollingConfig,
) FleetDeviceStatus {
	entry := FleetDeviceStatus{
		ID:                    device.ID,
		Name:                  device.Name,
		Address:               device.Address,
		State:                 deviceState(status, err),
		PollInterval:          device.PollInterval,
		EffectivePollInterval: polling.Effective(device.PollInterval),
		Status:                status,
	}

	// A reason is only worth carrying when the state does not already give one.
	if entry.State == StateUnknown && err != nil {
		entry.Error = err.Error()
	}

	return entry
}

// deviceState names the state a fleet card shows for a device.
//
// Unreachable outranks a failed poll because it is the stronger and more
// actionable statement: a device answering nothing at all is a different
// problem from one whose polls partly fail, and the poller distinguishes them.
// The zero last_poll_time is checked after it, so a device that has never
// answered since being registered reads as unreachable rather than as merely
// pending — the raw last_poll_time is still in the row for anyone who needs the
// finer fact.
func deviceState(status *client.DeviceStatus, err error) DeviceState {
	if err != nil {
		var notPolled *DeviceNotPolledError
		if errors.As(err, &notPolled) {
			return StateNotRegistered
		}
		return StateUnknown
	}

	if status == nil {
		return StateUnknown
	}

	switch {
	case status.Unreachable:
		return StateUnreachable
	case status.LastPollTime.IsZero():
		return StateNeverPolled
	case !status.LastPollSuccess:
		return StateFailing
	default:
		return StateOK
	}
}
