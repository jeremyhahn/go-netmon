package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DefaultTimeout bounds a single API call. MIB imports parse thousands of
// objects server-side, so this is generous by CLI standards.
const DefaultTimeout = 60 * time.Second

// Client talks to the go-netmon management API.
//
// A Client is safe for concurrent use and must not be copied: it memoizes the
// name→ID lookups the resolvers perform, which is what keeps a command that
// names several resources from listing the catalog once per argument.
type Client struct {
	baseURL string
	http    *http.Client

	// Name lookups are memoized per resource kind; see nameCache.
	deviceNames  nameCache
	profileNames nameCache
	mibNames     nameCache
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient supplies a custom HTTP client, chiefly for tests and for
// callers that need proxy or TLS settings.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.http = hc }
}

// WithTimeout sets the per-request timeout.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.http.Timeout = d }
}

// New creates a Client for a server base URL such as
// "http://198.51.100.91:8081". A bare host:port is accepted and assumed HTTP.
func New(baseURL string, opts ...Option) (*Client, error) {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return nil, &ConfigError{Field: "server URL", Reason: "must not be empty"}
	}
	if !strings.Contains(trimmed, "://") {
		trimmed = "http://" + trimmed
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, &ConfigError{Field: "server URL", Reason: err.Error()}
	}
	if parsed.Host == "" {
		return nil, &ConfigError{Field: "server URL", Reason: "missing host"}
	}

	c := &Client{
		baseURL: strings.TrimRight(parsed.String(), "/"),
		http:    &http.Client{Timeout: DefaultTimeout},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// BaseURL returns the server address the client targets.
func (c *Client) BaseURL() string { return c.baseURL }

// do issues a request and decodes a JSON response into out, which may be nil
// when the caller does not need the body.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return &DecodeError{Path: path, Err: err}
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return &TransportError{Method: method, URL: c.baseURL + path, Err: err}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	return c.send(req, method, path, out)
}

// send executes a prepared request and decodes the response.
func (c *Client) send(req *http.Request, method, path string, out any) error {
	// The request URL is built from the --server address the operator supplied;
	// pointing this client at a chosen host is the purpose of that flag.
	resp, err := c.http.Do(req) // #nosec G704 -- destination is the operator-supplied API server
	if err != nil {
		return &TransportError{Method: method, URL: req.URL.String(), Err: err}
	}
	// Closing a response body only releases the connection; a failure there
	// cannot change what this call already returned to the caller.
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Bound the error body: a misrouted request can return a whole HTML page.
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return &APIError{
			StatusCode: resp.StatusCode,
			Method:     method,
			Path:       path,
			Body:       strings.TrimSpace(string(snippet)),
		}
	}

	if out == nil || resp.StatusCode == http.StatusNoContent {
		// Drain so the connection can be reused. A short read only costs us
		// that reuse, so there is nothing to report.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return &DecodeError{Path: path, Err: err}
	}
	return nil
}

// Health reports whether the server is reachable and responding.
func (c *Client) Health(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/api/devices", nil, &struct {
		Devices []Device `json:"devices"`
	}{})
}

// ---------------------------------------------------------------- devices

// ListDevices returns every configured device.
func (c *Client) ListDevices(ctx context.Context) ([]Device, error) {
	var resp struct {
		Devices []Device `json:"devices"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/devices", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Devices, nil
}

// GetDevice returns one device by ID.
func (c *Client) GetDevice(ctx context.Context, id int64) (*Device, error) {
	var device Device
	if err := c.do(ctx, http.MethodGet, "/api/devices/"+itoa(id), nil, &device); err != nil {
		return nil, err
	}
	return &device, nil
}

// CreateDevice registers a new device and starts polling it.
func (c *Client) CreateDevice(ctx context.Context, device Device) (*Device, error) {
	if err := validatePollInterval(device); err != nil {
		return nil, err
	}

	var created Device
	if err := c.do(ctx, http.MethodPost, "/api/devices", device, &created); err != nil {
		return nil, err
	}
	return &created, nil
}

// UpdateDevice replaces a device's configuration.
func (c *Client) UpdateDevice(ctx context.Context, id int64, device Device) (*Device, error) {
	if err := validatePollInterval(device); err != nil {
		return nil, err
	}

	var updated Device
	if err := c.do(ctx, http.MethodPut, "/api/devices/"+itoa(id), device, &updated); err != nil {
		return nil, err
	}
	return &updated, nil
}

// validatePollInterval rejects an interval the poller could not act on, before
// the request is made. The server enforces the same rule; catching it here is
// what keeps a typo from costing a round trip and a half-applied change.
func validatePollInterval(device Device) error {
	if device.PollInterval == nil {
		return nil
	}
	if *device.PollInterval < 1 {
		return &PollIntervalError{Device: device.Name, Interval: *device.PollInterval}
	}
	return nil
}

// DeleteDevice removes a device and stops polling it.
func (c *Client) DeleteDevice(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodDelete, "/api/devices/"+itoa(id), nil, nil)
}

// DeviceStatus returns the poller's view of a device.
func (c *Client) DeviceStatus(ctx context.Context, id int64) (*DeviceStatus, error) {
	var status DeviceStatus
	if err := c.do(ctx, http.MethodGet, "/api/devices/"+itoa(id)+"/status", nil, &status); err != nil {
		return nil, err
	}
	return &status, nil
}

// TestDeviceSNMP probes a stored device's SNMP agent using its saved credentials.
//
// A device that does not answer is reported as the probe result it is, not as
// an API failure, so callers render one shape either way.
func (c *Client) TestDeviceSNMP(ctx context.Context, id int64) (*SNMPTestResponse, error) {
	var resp SNMPTestResponse
	if err := c.do(ctx, http.MethodPost, "/api/devices/"+itoa(id)+"/test-snmp", nil, &resp); err != nil {
		return probeResult(err)
	}
	return &resp, nil
}

// TestSNMP probes an arbitrary address without storing anything.
func (c *Client) TestSNMP(ctx context.Context, req SNMPTestRequest) (*SNMPTestResponse, error) {
	var resp SNMPTestResponse
	if err := c.do(ctx, http.MethodPost, "/api/snmp/test", req, &resp); err != nil {
		return probeResult(err)
	}
	return &resp, nil
}

// probeResult recovers the structured probe result carried by a failed probe.
//
// The server reports an unreachable device with a gateway status and the same
// body a successful probe returns, so the negative result is data the caller
// can render — naming the device and why it failed — rather than an opaque
// "504 Gateway Timeout". Anything that is not a probe result stays an error.
func probeResult(err error) (*SNMPTestResponse, error) {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return nil, err
	}

	var resp SNMPTestResponse
	if decodeErr := json.Unmarshal([]byte(apiErr.Body), &resp); decodeErr != nil {
		return nil, err
	}
	if resp.Success || resp.Message == "" {
		// Not a probe result: some other handler produced this body.
		return nil, err
	}

	return &resp, nil
}

// DeviceMIBs returns a device's directly assigned MIBs and its full effective set.
func (c *Client) DeviceMIBs(ctx context.Context, id int64) (*DeviceMIBs, error) {
	var mibs DeviceMIBs
	if err := c.do(ctx, http.MethodGet, "/api/devices/"+itoa(id)+"/mibs", nil, &mibs); err != nil {
		return nil, err
	}
	return &mibs, nil
}

// AssignMIBToDevice attaches a MIB directly to a device.
func (c *Client) AssignMIBToDevice(ctx context.Context, deviceID, mibID int64) error {
	body := map[string]int64{"mib_id": mibID}
	return c.do(ctx, http.MethodPost, "/api/devices/"+itoa(deviceID)+"/mibs", body, nil)
}

// RemoveMIBFromDevice detaches a directly assigned MIB.
func (c *Client) RemoveMIBFromDevice(ctx context.Context, deviceID, mibID int64) error {
	return c.do(ctx, http.MethodDelete,
		"/api/devices/"+itoa(deviceID)+"/mibs/"+itoa(mibID), nil, nil)
}

// DeviceProfiles returns the profiles assigned to a device.
func (c *Client) DeviceProfiles(ctx context.Context, id int64) ([]Profile, error) {
	var resp struct {
		Profiles []Profile `json:"profiles"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/devices/"+itoa(id)+"/profiles", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Profiles, nil
}

// AssignProfileToDevice attaches a profile to a device.
func (c *Client) AssignProfileToDevice(ctx context.Context, deviceID, profileID int64) error {
	return c.do(ctx, http.MethodPost,
		"/api/devices/"+itoa(deviceID)+"/profiles/"+itoa(profileID), nil, nil)
}

// RemoveProfileFromDevice detaches a profile from a device.
func (c *Client) RemoveProfileFromDevice(ctx context.Context, deviceID, profileID int64) error {
	return c.do(ctx, http.MethodDelete,
		"/api/devices/"+itoa(deviceID)+"/profiles/"+itoa(profileID), nil, nil)
}

// ---------------------------------------------------------------- profiles

// ListProfiles returns every device profile.
func (c *Client) ListProfiles(ctx context.Context) ([]Profile, error) {
	var resp struct {
		Profiles []Profile `json:"profiles"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/profiles", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Profiles, nil
}

// GetProfile returns one profile by ID.
func (c *Client) GetProfile(ctx context.Context, id int64) (*Profile, error) {
	var profile Profile
	if err := c.do(ctx, http.MethodGet, "/api/profiles/"+itoa(id), nil, &profile); err != nil {
		return nil, err
	}
	return &profile, nil
}

// CreateProfile creates a device profile.
func (c *Client) CreateProfile(ctx context.Context, profile Profile) (*Profile, error) {
	var created Profile
	if err := c.do(ctx, http.MethodPost, "/api/profiles", profile, &created); err != nil {
		return nil, err
	}
	return &created, nil
}

// UpdateProfile replaces a profile's fields.
func (c *Client) UpdateProfile(ctx context.Context, id int64, profile Profile) (*Profile, error) {
	var updated Profile
	if err := c.do(ctx, http.MethodPut, "/api/profiles/"+itoa(id), profile, &updated); err != nil {
		return nil, err
	}
	return &updated, nil
}

// DeleteProfile removes a profile.
func (c *Client) DeleteProfile(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodDelete, "/api/profiles/"+itoa(id), nil, nil)
}

// ProfileMIBs returns the MIBs in a profile, each with the assignment that says
// how the profile polls it.
func (c *Client) ProfileMIBs(ctx context.Context, id int64) ([]AssignedMIB, error) {
	var resp struct {
		MIBs []AssignedMIB `json:"mibs"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/profiles/"+itoa(id)+"/mibs", nil, &resp); err != nil {
		return nil, err
	}
	return resp.MIBs, nil
}

// AddMIBToProfile adds a MIB to a profile.
func (c *Client) AddMIBToProfile(ctx context.Context, profileID, mibID int64) error {
	body := map[string]int64{"mib_id": mibID}
	return c.do(ctx, http.MethodPost, "/api/profiles/"+itoa(profileID)+"/mibs", body, nil)
}

// RemoveMIBFromProfile removes a MIB from a profile.
func (c *Client) RemoveMIBFromProfile(ctx context.Context, profileID, mibID int64) error {
	return c.do(ctx, http.MethodDelete,
		"/api/profiles/"+itoa(profileID)+"/mibs/"+itoa(mibID), nil, nil)
}

// ProfileDevices returns the devices using a profile.
func (c *Client) ProfileDevices(ctx context.Context, id int64) ([]Device, error) {
	var resp struct {
		Devices []Device `json:"devices"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/profiles/"+itoa(id)+"/devices", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Devices, nil
}

// ---------------------------------------------------------------- MIBs

// ListMIBs returns imported MIBs. When availableOnly is set, the server
// restricts the list to modules that actually have data flowing.
//
// It is the whole catalog: use SearchMIBs to have the server narrow it.
func (c *Client) ListMIBs(ctx context.Context, availableOnly bool) ([]MIB, error) {
	mibs, _, err := c.SearchMIBs(ctx, MIBQuery{AvailableOnly: availableOnly})
	return mibs, err
}

// SearchMIBs returns the module catalog narrowed by q, together with the counts
// that say what was left out. The page is nil when q narrowed nothing, in which
// case the returned slice is the whole catalog.
//
// The catalog is ordered by module name, so a given offset means the same
// modules on every request.
func (c *Client) SearchMIBs(ctx context.Context, q MIBQuery) ([]MIB, *Page, error) {
	if err := q.validate(); err != nil {
		return nil, nil, err
	}

	var resp struct {
		MIBs []MIB `json:"mibs"`
		Page *Page `json:"page"`
	}
	if err := c.do(ctx, http.MethodGet, withQuery("/api/mibs", q.values()), nil, &resp); err != nil {
		return nil, nil, err
	}
	return resp.MIBs, resp.Page, nil
}

// GetMIB returns a MIB module together with every object it defines.
//
// A large module is large: CISCO-ENTITY-VENDORTYPE-OID-MIB is 1.6 MiB of JSON.
// Use SearchMIBObjects when only some of the objects are wanted.
func (c *Client) GetMIB(ctx context.Context, id int64) (*MIBDetail, error) {
	return c.SearchMIBObjects(ctx, id, ObjectQuery{})
}

// SearchMIBObjects returns a MIB module with its objects narrowed by q.
//
// The module's own fields are unaffected by the narrowing — ObjectCount still
// counts the module, not the page — and Page reports what the query selected.
// Page is nil when q narrowed nothing.
//
// Objects come back in the order the server's metadata store holds them, which
// is the same on every request, so paging through a module visits each object
// once.
func (c *Client) SearchMIBObjects(ctx context.Context, id int64, q ObjectQuery) (*MIBDetail, error) {
	if err := q.validate(); err != nil {
		return nil, err
	}

	var detail MIBDetail
	path := withQuery("/api/mibs/"+itoa(id), q.values())
	if err := c.do(ctx, http.MethodGet, path, nil, &detail); err != nil {
		return nil, err
	}
	return &detail, nil
}

// MIBTables returns the tables defined in a MIB module.
func (c *Client) MIBTables(ctx context.Context, id int64) ([]Table, error) {
	var resp struct {
		Tables []Table `json:"tables"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/mibs/"+itoa(id)+"/tables", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Tables, nil
}

// DeleteMIB removes an imported MIB.
func (c *Client) DeleteMIB(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodDelete, "/api/mibs/"+itoa(id), nil, nil)
}

// SetMIBTags replaces a MIB's tags.
func (c *Client) SetMIBTags(ctx context.Context, id int64, tags []string) error {
	body := map[string][]string{"tags": tags}
	return c.do(ctx, http.MethodPut, "/api/mibs/"+itoa(id)+"/tags", body, nil)
}

// ImportMIB uploads and parses a MIB file, storing it as a new module.
//
// A module that is already imported is refused; use ImportMIBFile with
// Replace set to refresh one in place.
func (c *Client) ImportMIB(ctx context.Context, path string, tags []string) (*MIB, error) {
	result, err := c.ImportMIBFile(ctx, path, MIBImportOptions{Tags: tags})
	if err != nil {
		return nil, err
	}
	return &result.MIB, nil
}

// ImportMIBFile uploads a MIB file and reports what the server did with it.
//
// With Replace set, a module that is already imported has its parsed metadata —
// its objects and their types, units and display hints, its object count and
// its description — refreshed in place. The module keeps its ID, its name and
// its tags, and every device and profile assignment, including each
// assignment's enable state and filters, stays attached. Without Replace, an
// upload of an already-imported module is refused with 409 Conflict.
func (c *Client) ImportMIBFile(
	ctx context.Context,
	path string,
	opts MIBImportOptions,
) (*MIBImportResult, error) {
	// path is the MIB file the operator asked to import.
	file, err := os.Open(path) // #nosec G304 -- path is the MIB file named on the command line
	if err != nil {
		return nil, &ConfigError{Field: "MIB file", Reason: err.Error()}
	}
	// Read-only handle: a close failure cannot affect the upload that already
	// consumed the file.
	defer func() { _ = file.Close() }()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		return nil, &ConfigError{Field: "MIB upload", Reason: err.Error()}
	}
	if _, copyErr := io.Copy(part, file); copyErr != nil {
		return nil, &ConfigError{Field: "MIB upload", Reason: copyErr.Error()}
	}
	if len(opts.Tags) > 0 {
		if tagErr := writer.WriteField("tags", strings.Join(opts.Tags, ",")); tagErr != nil {
			return nil, &ConfigError{Field: "MIB tags", Reason: tagErr.Error()}
		}
	}
	if closeErr := writer.Close(); closeErr != nil {
		return nil, &ConfigError{Field: "MIB upload", Reason: closeErr.Error()}
	}

	uploadPath := "/api/mibs/import"
	if opts.Replace {
		uploadPath += "?replace=true"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+uploadPath, &body)
	if err != nil {
		return nil, &TransportError{Method: http.MethodPost, URL: c.baseURL + uploadPath, Err: err}
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	// The server wraps the record in a {"mib": ..., "replaced": ...,
	// "message": ...} envelope.
	var resp MIBImportResult
	if sendErr := c.send(req, http.MethodPost, uploadPath, &resp); sendErr != nil {
		return nil, sendErr
	}
	return &resp, nil
}

// ------------------------------------------------------- MIB assignments

// DeviceMIBAssignment returns how a directly assigned MIB is polled for a device.
func (c *Client) DeviceMIBAssignment(ctx context.Context, deviceID, mibID int64) (*MIBAssignment, error) {
	return c.assignment(ctx, assignmentPath("devices", deviceID, mibID))
}

// SetDeviceMIBAssignment replaces how a directly assigned MIB is polled and
// returns the stored result.
func (c *Client) SetDeviceMIBAssignment(
	ctx context.Context, deviceID, mibID int64, a MIBAssignment,
) (*MIBAssignment, error) {
	return c.setAssignment(ctx, assignmentPath("devices", deviceID, mibID), a)
}

// ProfileMIBAssignment returns how a MIB in a profile is polled.
func (c *Client) ProfileMIBAssignment(ctx context.Context, profileID, mibID int64) (*MIBAssignment, error) {
	return c.assignment(ctx, assignmentPath("profiles", profileID, mibID))
}

// SetProfileMIBAssignment replaces how a MIB in a profile is polled, which the
// server pushes to every device using that profile.
func (c *Client) SetProfileMIBAssignment(
	ctx context.Context, profileID, mibID int64, a MIBAssignment,
) (*MIBAssignment, error) {
	return c.setAssignment(ctx, assignmentPath("profiles", profileID, mibID), a)
}

// EstimateMIBSeries reports how many Prometheus series enabling a MIB would add
// to a device. The MIB need not be assigned: asking before committing is the
// point of the call.
func (c *Client) EstimateMIBSeries(ctx context.Context, deviceID, mibID int64) (*MIBSeriesEstimate, error) {
	var estimate MIBSeriesEstimate
	path := "/api/devices/" + itoa(deviceID) + "/mibs/" + itoa(mibID) + "/estimate"
	if err := c.do(ctx, http.MethodGet, path, nil, &estimate); err != nil {
		return nil, err
	}
	return &estimate, nil
}

// DeviceMIBObjects reports which of a MIB's objects a device is polling, and
// the reason for each one it is not. The MIB has to be assigned to the device,
// directly or through a profile.
//
// It returns the whole plan; use SearchDeviceMIBObjects to have the server
// narrow it.
func (c *Client) DeviceMIBObjects(ctx context.Context, deviceID, mibID int64) (*MIBObjectPlan, error) {
	return c.SearchDeviceMIBObjects(ctx, deviceID, mibID, PlanQuery{})
}

// SearchDeviceMIBObjects returns a device's object plan with its objects
// narrowed by q.
//
// Total and Polled still describe the whole module, and Page reports what the
// query selected, so a paged view can say both "showing 51-100 of 293 matching"
// and "12 polled of 293". Page is nil when q narrowed nothing.
//
// The objects keep the order the poller sees them in, whatever the query: a
// max_oids cap is spent in that order, so a page is a slice of it and never a
// re-sort of it.
func (c *Client) SearchDeviceMIBObjects(
	ctx context.Context,
	deviceID, mibID int64,
	q PlanQuery,
) (*MIBObjectPlan, error) {
	if err := q.validate(); err != nil {
		return nil, err
	}

	var plan MIBObjectPlan
	path := withQuery(
		"/api/devices/"+itoa(deviceID)+"/mibs/"+itoa(mibID)+"/objects", q.values())
	if err := c.do(ctx, http.MethodGet, path, nil, &plan); err != nil {
		return nil, err
	}
	return &plan, nil
}

// assignment reads one assignment.
func (c *Client) assignment(ctx context.Context, path string) (*MIBAssignment, error) {
	var a MIBAssignment
	if err := c.do(ctx, http.MethodGet, path, nil, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// setAssignment writes one assignment, refusing a filter the poller could not
// act on so that a malformed pattern costs a round trip rather than reaching a
// worker.
func (c *Client) setAssignment(ctx context.Context, path string, a MIBAssignment) (*MIBAssignment, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}

	var stored MIBAssignment
	if err := c.do(ctx, http.MethodPut, path, a, &stored); err != nil {
		return nil, err
	}
	return &stored, nil
}

// assignmentPath builds the URL of one owner's MIB assignment. owner is the
// collection segment, "devices" or "profiles".
func assignmentPath(owner string, ownerID, mibID int64) string {
	return "/api/" + owner + "/" + itoa(ownerID) + "/mibs/" + itoa(mibID) + "/assignment"
}

// ---------------------------------------------------------------- metrics

// QueryRange runs a range query over stored metrics. timeRange is one of the
// server's accepted windows: 6h, 1d, 7d, 30d, 1y.
func (c *Client) QueryRange(ctx context.Context, metric, timeRange string) ([]TimeSeries, error) {
	body := map[string]string{"metric": metric, "time_range": timeRange}

	var wire []wireTimeSeries
	if err := c.do(ctx, http.MethodPost, "/api/metrics/query", body, &wire); err != nil {
		return nil, err
	}

	series := make([]TimeSeries, len(wire))
	for i, w := range wire {
		series[i] = w.toTimeSeries()
	}
	return series, nil
}

// QueryInstant runs a PromQL instant query.
func (c *Client) QueryInstant(ctx context.Context, query string) ([]InstantValue, error) {
	body := map[string]string{"query": query}

	var resp struct {
		Metrics []wireInstantValue `json:"metrics"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/metrics/instant", body, &resp); err != nil {
		return nil, err
	}

	values := make([]InstantValue, len(resp.Metrics))
	for i, w := range resp.Metrics {
		values[i] = w.toInstantValue()
	}
	return values, nil
}

// SystemInfo returns the cached system-group values for a device.
func (c *Client) SystemInfo(ctx context.Context, device string, fields []string) (*SystemInfo, error) {
	body := map[string]any{"device": device, "fields": fields}

	var info SystemInfo
	if err := c.do(ctx, http.MethodPost, "/api/metrics/system-info", body, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// ---------------------------------------------------------------- poller

// PollerConfig returns the configuration the poller consumes.
func (c *Client) PollerConfig(ctx context.Context) (*PollerConfig, error) {
	var cfg PollerConfig
	if err := c.do(ctx, http.MethodGet, "/api/poller/config", nil, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// LearnedTables returns the tables the poller has discovered at runtime.
func (c *Client) LearnedTables(ctx context.Context) (map[string]any, error) {
	var resp map[string]any
	if err := c.do(ctx, http.MethodGet, "/api/learned-tables", nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// itoa renders an ID for use in a URL path.
func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}
