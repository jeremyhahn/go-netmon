package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jeremyhahn/go-netmon/internal/webui/client"
	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

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

	// StateDisabled is a device an operator switched off. It is deliberately
	// left off the poller, so its absence there is neither a device fault nor
	// a configuration fault, and it is counted apart from both.
	StateDisabled DeviceState = "disabled"

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

	// Enabled is the device record's flag, stated on every row.
	Enabled bool `json:"enabled"`

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
// iterate it without a null check. It carries one page when the caller asked
// for one and the whole fleet otherwise.
type FleetStatusResponse struct {
	Devices []FleetDeviceStatus `json:"devices"`

	// Total is how many devices match the search and state filter — what a
	// pager needs to know how far it can go.
	Total int `json:"total"`

	// Configured is how many devices exist at all, regardless of any filter.
	Configured int `json:"configured"`

	// Summary counts every configured device by state, so a fleet-wide tally
	// travels as a handful of numbers rather than as the rows behind them. It
	// ignores the filters deliberately: "12 unreachable" is a statement about
	// the fleet, and a chip that recounted itself against its own filter would
	// always read as the number of rows on screen.
	Summary map[DeviceState]int `json:"summary"`
}

// FleetQuery is the narrowing a caller asked for.
type FleetQuery struct {
	// Search matches a device's name, address or state, case-insensitively.
	Search string
	// State keeps only devices in one state, as the summary chips do.
	State string
	// Limit is the page size; zero means the whole fleet, which is what a CLI
	// or a script wants and what this endpoint has always returned.
	Limit int
	// Offset is how many matching devices to skip.
	Offset int
}

// parseFleetQuery reads the narrowing parameters, refusing values it cannot act
// on rather than guessing at them.
func parseFleetQuery(r *http.Request) (FleetQuery, error) {
	query := FleetQuery{
		Search: strings.ToLower(strings.TrimSpace(r.URL.Query().Get("search"))),
		State:  strings.TrimSpace(r.URL.Query().Get("state")),
	}

	for _, spec := range []struct {
		name  string
		field *int
	}{
		{"limit", &query.Limit},
		{"offset", &query.Offset},
	} {
		raw := strings.TrimSpace(r.URL.Query().Get(spec.name))
		if raw == "" {
			continue
		}
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			return FleetQuery{}, &QueryParameterError{Parameter: spec.name, Value: raw}
		}
		*spec.field = value
	}

	return query, nil
}

// matches says whether a row survives the caller's narrowing.
func (q FleetQuery) matches(entry FleetDeviceStatus) bool {
	if q.State != "" && string(entry.State) != q.State {
		return false
	}
	if q.Search == "" {
		return true
	}
	return strings.Contains(strings.ToLower(entry.Name), q.Search) ||
		strings.Contains(strings.ToLower(entry.Address), q.Search) ||
		strings.Contains(strings.ToLower(string(entry.State)), q.Search)
}

// page returns the slice of rows the caller asked for. An offset past the end
// is an empty page rather than an error: a fleet can shrink under a reader who
// has already paged into it.
func (q FleetQuery) page(entries []FleetDeviceStatus) []FleetDeviceStatus {
	if q.Limit == 0 {
		return entries
	}
	if q.Offset >= len(entries) {
		return []FleetDeviceStatus{}
	}
	end := min(q.Offset+q.Limit, len(entries))
	return entries[q.Offset:end]
}

// handleFleetStatus handles GET /api/devices/status: live status for every
// configured device in one request.
//
// It exists because a fleet overview built on the per-device endpoint issues one
// request per device on every page load, which is invisible at four devices and
// is not at a hundred.
func (s *Server) handleFleetStatus(w http.ResponseWriter, r *http.Request) {
	query, err := parseFleetQuery(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	devices, err := s.storage.ListDevices(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Every device is classified, because the summary counts all of them and
	// the poller answers for all of them in the one call either way. What the
	// narrowing saves is the serialization and the transfer, which is where the
	// cost actually was.
	all := s.fleetStatus(r.Context(), devices)

	summary := make(map[DeviceState]int, len(all))
	matching := make([]FleetDeviceStatus, 0, len(all))
	for _, entry := range all {
		summary[entry.State]++
		if query.matches(entry) {
			matching = append(matching, entry)
		}
	}

	respondJSON(w, http.StatusOK, FleetStatusResponse{
		Devices:    query.page(matching),
		Total:      len(matching),
		Configured: len(all),
		Summary:    summary,
	})
}

// fleetStatus collects every device's status in the order given.
//
// It asks the poller once. The poller builds its device listing from in-memory
// worker state, so one response already carries the status of everything it
// polls; asking per device instead cost a round trip per device, and since
// every open overview repeats that every 30 seconds the load on the poller was
// devices times sessions. At 10,000 devices and 25 operators that was 250,000
// requests a tick, which queued past the collection budget and left 85% of the
// rows blank — an overview reporting a fleet-wide outage that was not
// happening. One request per refresh is flat in both directions.
//
// Order comes from the caller's slice: the storage layer sorts devices by name
// and rows are filled in that order, so repeated requests do not reshuffle. A
// device the poller has no record of is reported as such rather than as an
// error, and if the poller cannot be reached at all every row says so.
func (s *Server) fleetStatus(ctx context.Context, devices []storage.Device) []FleetDeviceStatus {
	ctx, cancel := context.WithTimeout(ctx, FleetStatusBudget)
	defer cancel()

	polling := s.polling()
	entries := make([]FleetDeviceStatus, len(devices))

	known, err := s.pollerFleet(ctx)
	if err != nil {
		// Nothing could be learned about anything. Every device still gets a
		// row, carrying the reason.
		for i, device := range devices {
			entries[i] = newFleetDeviceStatus(device, nil, err, polling)
		}
		return entries
	}

	for i, device := range devices {
		status, found := known[device.Name]
		switch {
		case !found:
			entries[i] = newFleetDeviceStatus(device, nil,
				&DeviceNotPolledError{Device: device.Name}, polling)
		case status == nil:
			// Registered, but the poller carried nothing about it. That is not
			// the same as not being polled, and the row has to say which.
			entries[i] = newFleetDeviceStatus(device, nil,
				&PollerStatusMissingError{Device: device.Name}, polling)
		default:
			entries[i] = newFleetDeviceStatus(device, status, nil, polling)
		}
	}

	return entries
}

// pollerFleet returns the poller's view of every device it polls, by name.
func (s *Server) pollerFleet(ctx context.Context) (map[string]*client.DeviceStatus, error) {
	if s.pollerClient == nil {
		return nil, &PollerUnconfiguredError{}
	}

	infos, err := s.pollerClient.ListDevices(ctx)
	if err != nil {
		return nil, &PollerUnavailableError{Err: err}
	}

	known := make(map[string]*client.DeviceStatus, len(infos))
	for _, info := range infos {
		// The poller always fills a status, falling back to a never-polled one
		// for a worker that has not run yet. A nil here would read as unknown,
		// which is the honest answer for a listing that carried no status.
		known[info.Name] = info.Status
	}
	return known, nil
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
		Enabled:               storage.EnabledOf(&device),
		PollInterval:          device.PollInterval,
		EffectivePollInterval: polling.Effective(device.PollInterval),
		Status:                status,
	}

	// A disabled device the poller has nothing for is exactly as intended,
	// and that needs no answer from the poller to know. One the poller still
	// reports on is still being polled, and its real state says so.
	if !entry.Enabled && status == nil {
		entry.State = StateDisabled
		return entry
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
