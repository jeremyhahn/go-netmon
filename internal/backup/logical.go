package backup

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"
)

// The logical export is a reviewable, portable copy of what the web UI holds:
// devices, profiles, the MIB catalog, the poller configuration, and the
// assignments that connect them.
//
// It is not the restore path — restoring is a physical copy of the volumes, and
// nothing here is ever applied. It exists because a physical archive is opaque:
// when a schema change makes an old volume unreadable, or an operator needs to
// see what a deployment looked like without standing it back up, this is the
// only artifact that answers.
//
// It must run while the stack is UP, so it happens before anything is stopped,
// and a failure is never fatal: an unreachable web UI records
// logical.status=unavailable and the physical backup proceeds. Losing the
// reviewable copy is a much smaller loss than declining to take the backup.

// Logical export status values recorded in the manifest.
const (
	logicalOK          = "ok"
	logicalUnavailable = "unavailable"
	logicalSkipped     = "skipped"
)

// DefaultWebUIURL is the web UI the logical export reads when none is given.
const DefaultWebUIURL = "http://localhost:8082"

// logicalDir is the backup subdirectory the export is written to.
const logicalDir = "logical"

// logicalTimeout bounds the whole export. It is generous because the per-device
// assignment walk is many small requests, and strict because the stack is still
// serving traffic and a backup must not hang against a wedged web UI.
const logicalTimeout = 2 * time.Minute

// exportLogical writes the logical export into dst and reports what it found.
//
// It returns a status rather than an error: every failure mode here is one the
// backup continues through.
func (s *Service) exportLogical(ctx context.Context, dst *Root) LogicalInfo {
	skipped := LogicalInfo{Status: logicalSkipped, Counts: map[string]int{}}
	if s.cfg.WebUIURL == "" {
		return skipped
	}

	s.info("exporting logical records from %s", s.cfg.WebUIURL)

	ctx, cancel := context.WithTimeout(ctx, logicalTimeout)
	defer cancel()

	ex := &logicalExport{svc: s, dst: dst}
	info, err := ex.run(ctx)
	if err != nil {
		s.warn("logical export failed (%v); the physical backup continues", err)
		s.warn("the manifest records logical.status=%s", logicalUnavailable)
		return LogicalInfo{Status: logicalUnavailable, Counts: map[string]int{}}
	}
	return info
}

// logicalExport holds one export in progress.
type logicalExport struct {
	svc *Service
	dst *Root
}

// idList is the shape every collection endpoint shares: a named array of
// objects that each carry an id. Only the ids are needed to walk the
// relationships, so only they are decoded.
type idList struct {
	Devices  []idOnly `json:"devices"`
	Profiles []idOnly `json:"profiles"`
	MIBs     []idOnly `json:"mibs"`
}

type idOnly struct {
	ID int64 `json:"id"`
}

// deviceMIBs is the device-to-MIB response, decoded only for the ids needed to
// fetch each assignment record.
type deviceMIBs struct {
	DirectMIBs []idOnly `json:"direct_mibs"`
}

// profileMIBs is the profile-to-MIB response.
type profileMIBs struct {
	MIBs []idOnly `json:"mibs"`
}

// deviceExport is one device's relationships, as written to device-mibs.json.
type deviceExport struct {
	DeviceID          int64            `json:"device_id"`
	MIBs              json.RawMessage  `json:"mibs"`
	Profiles          json.RawMessage  `json:"profiles"`
	DirectAssignments []assignedRecord `json:"direct_assignments"`
}

// profileExport is one profile's relationships, as written to
// profile-mibs.json.
type profileExport struct {
	ProfileID   int64            `json:"profile_id"`
	MIBs        json.RawMessage  `json:"mibs"`
	Assignments []assignedRecord `json:"assignments"`
}

// assignedRecord is one MIB assignment: the per-assignment record carries the
// enablement and any OID filters, which is state held nowhere else.
type assignedRecord struct {
	MIBID      int64           `json:"mib_id"`
	Assignment json.RawMessage `json:"assignment"`
}

// run performs the export and returns the manifest record for it.
func (e *logicalExport) run(ctx context.Context) (LogicalInfo, error) {
	if err := e.dst.MkdirAll(logicalDir, 0o755); err != nil {
		return LogicalInfo{}, err
	}

	devices, err := e.save(ctx, "/api/devices", "devices.json")
	if err != nil {
		return LogicalInfo{}, err
	}
	profiles, err := e.save(ctx, "/api/profiles", "profiles.json")
	if err != nil {
		return LogicalInfo{}, err
	}
	mibs, err := e.save(ctx, "/api/mibs", "mibs.json")
	if err != nil {
		return LogicalInfo{}, err
	}
	if _, err := e.save(ctx, "/api/poller/config", "poller-config.json"); err != nil {
		return LogicalInfo{}, err
	}

	if err := e.exportDeviceMIBs(ctx, ids(devices).Devices); err != nil {
		return LogicalInfo{}, err
	}
	if err := e.exportProfileMIBs(ctx, ids(profiles).Profiles); err != nil {
		return LogicalInfo{}, err
	}

	counts := map[string]int{
		"devices":  len(ids(devices).Devices),
		"profiles": len(ids(profiles).Profiles),
		"mibs":     len(ids(mibs).MIBs),
	}
	e.svc.info("  logical export: %d devices, %d profiles, %d MIBs",
		counts["devices"], counts["profiles"], counts["mibs"])

	return LogicalInfo{Status: logicalOK, Counts: counts}, nil
}

// exportDeviceMIBs walks every device's MIBs, profiles and per-assignment
// records.
func (e *logicalExport) exportDeviceMIBs(ctx context.Context, devices []idOnly) error {
	out := make([]deviceExport, 0, len(devices))
	for _, d := range devices {
		base := "/api/devices/" + strconv.FormatInt(d.ID, 10)

		mibs, err := e.fetch(ctx, base+"/mibs")
		if err != nil {
			return err
		}
		// A device with no profiles answers 404 rather than an empty list, which
		// is an absence and not a failure.
		profiles, err := e.fetchOptional(ctx, base+"/profiles")
		if err != nil {
			return err
		}

		var dm deviceMIBs
		if decodeErr := json.Unmarshal(mibs, &dm); decodeErr != nil {
			return &LogicalDecodeError{Path: base + "/mibs", Err: decodeErr}
		}
		assignments, err := e.assignments(ctx, base+"/mibs/", dm.DirectMIBs)
		if err != nil {
			return err
		}

		out = append(out, deviceExport{
			DeviceID:          d.ID,
			MIBs:              mibs,
			Profiles:          profiles,
			DirectAssignments: assignments,
		})
	}
	return e.write("device-mibs.json", out)
}

// exportProfileMIBs walks every profile's MIBs and per-assignment records.
func (e *logicalExport) exportProfileMIBs(ctx context.Context, profiles []idOnly) error {
	out := make([]profileExport, 0, len(profiles))
	for _, p := range profiles {
		base := "/api/profiles/" + strconv.FormatInt(p.ID, 10)

		mibs, err := e.fetch(ctx, base+"/mibs")
		if err != nil {
			return err
		}
		var pm profileMIBs
		if decodeErr := json.Unmarshal(mibs, &pm); decodeErr != nil {
			return &LogicalDecodeError{Path: base + "/mibs", Err: decodeErr}
		}
		assignments, err := e.assignments(ctx, base+"/mibs/", pm.MIBs)
		if err != nil {
			return err
		}

		out = append(out, profileExport{ProfileID: p.ID, MIBs: mibs, Assignments: assignments})
	}
	return e.write("profile-mibs.json", out)
}

// assignments fetches the assignment record for each MIB under a base path.
func (e *logicalExport) assignments(ctx context.Context, base string, mibs []idOnly) ([]assignedRecord, error) {
	out := make([]assignedRecord, 0, len(mibs))
	for _, m := range mibs {
		path := base + strconv.FormatInt(m.ID, 10) + "/assignment"
		record, err := e.fetchOptional(ctx, path)
		if err != nil {
			return nil, err
		}
		out = append(out, assignedRecord{MIBID: m.ID, Assignment: record})
	}
	return out, nil
}

// save fetches a path and writes it to the export, returning the raw body so
// the caller can read ids out of it without a second request.
func (e *logicalExport) save(ctx context.Context, path, name string) ([]byte, error) {
	body, err := e.fetch(ctx, path)
	if err != nil {
		return nil, err
	}
	return body, e.writeRaw(name, body)
}

// fetch performs one GET and returns the body, requiring 200.
func (e *logicalExport) fetch(ctx context.Context, path string) ([]byte, error) {
	body, status, err := e.get(ctx, path)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, &LogicalFetchError{Path: path, Status: status}
	}
	return body, nil
}

// fetchOptional is fetch for a resource whose absence is meaningful rather than
// an error. A 404 becomes a JSON null.
func (e *logicalExport) fetchOptional(ctx context.Context, path string) (json.RawMessage, error) {
	body, status, err := e.get(ctx, path)
	if err != nil {
		return nil, err
	}
	switch status {
	case http.StatusOK:
		return body, nil
	case http.StatusNotFound:
		return json.RawMessage("null"), nil
	default:
		return nil, &LogicalFetchError{Path: path, Status: status}
	}
}

// get performs one request and returns the body with its status.
func (e *logicalExport) get(ctx context.Context, path string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.svc.cfg.WebUIURL+path, http.NoBody)
	if err != nil {
		return nil, 0, &LogicalFetchError{Path: path, Err: err}
	}
	resp, err := e.svc.httpClient().Do(req)
	if err != nil {
		return nil, 0, &LogicalFetchError{Path: path, Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	// Bounded so an unexpected endpoint cannot make a backup consume the host's
	// memory. The largest response here is the MIB catalog.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxLogicalResponse))
	if err != nil {
		return nil, 0, &LogicalFetchError{Path: path, Err: err}
	}
	return body, resp.StatusCode, nil
}

// maxLogicalResponse bounds a single response body.
const maxLogicalResponse = 64 << 20

// write marshals a value into the export directory.
func (e *logicalExport) write(name string, v any) error {
	encoded, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	return e.writeRaw(name, encoded)
}

// writeRaw writes bytes into the export directory, through the root.
func (e *logicalExport) writeRaw(name string, data []byte) error {
	// Re-indent so the archive is reviewable by hand rather than one long line.
	var buf any
	if err := json.Unmarshal(data, &buf); err == nil {
		if pretty, err := json.MarshalIndent(buf, "", "  "); err == nil {
			data = append(pretty, '\n') //nolint:gocritic // a new buffer replaces data on purpose
		}
	}
	return e.dst.WriteFile(logicalDir+"/"+name, data, 0o644)
}

// ids decodes the id-bearing arrays out of a collection response. A body that
// does not parse yields no ids, which the caller records as a count of zero
// rather than failing the backup.
func ids(body []byte) idList {
	var list idList
	_ = json.Unmarshal(body, &list)
	return list
}
