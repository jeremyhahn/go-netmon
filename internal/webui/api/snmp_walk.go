package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/gosnmp/gosnmp"

	"github.com/jeremyhahn/go-netmon/internal/mib"
)

// Walking a device's OID tree.
//
// Answering "what does this box actually expose?" needed net-snmp on the host,
// or a throwaway program written against internal packages. Neither is portable
// and neither uses the credentials already stored for the device, which for
// SNMPv3 is most of the difficulty.
//
// A walk is the tool for two questions that come up constantly: whether a MIB is
// worth importing, since a module the device does not implement produces
// nothing; and why a polled object is empty, since the device answering
// NoSuchObject looks exactly like the poller not asking.
//
// Names are resolved from the imported MIBs where they are known and left as
// raw OIDs where they are not, so a walk is useful before any MIB is imported.

// WalkDefaultRoot is where a walk starts when the caller names no subtree. It
// is the whole internet subtree: everything standard and every vendor's own,
// which is what "show me what this device has" means.
const WalkDefaultRoot = "1.3.6.1"

// Walk bounds. A walk runs inside an HTTP handler against a device that may hold
// tens of thousands of objects, so it is capped in both dimensions: the caller
// gets a truncated answer with Truncated set rather than a closed connection.
const (
	WalkDefaultLimit = 500
	WalkMaxLimit     = 20000
	WalkBudget       = 60 * time.Second
)

// SNMPWalkVarbind is one object a device answered with.
type SNMPWalkVarbind struct {
	OID string `json:"oid"`

	// Name is the MIB object this OID belongs to, and Module the MIB that
	// defines it. Both are empty when no imported MIB describes the OID, which
	// is the normal case for a device whose vendor MIB is not loaded yet.
	Name   string `json:"name,omitempty"`
	Module string `json:"module,omitempty"`

	Type  string `json:"type"`
	Value string `json:"value"`
}

// SNMPWalkResponse is the result of one walk.
type SNMPWalkResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`

	Device  string `json:"device,omitempty"`
	Address string `json:"address,omitempty"`
	Root    string `json:"root"`

	Count     int               `json:"count"`
	Truncated bool              `json:"truncated,omitempty"`
	Duration  string            `json:"duration"`
	Varbinds  []SNMPWalkVarbind `json:"varbinds"`

	Error string `json:"error,omitempty"`

	// TimedOut and Rejected separate "that box is silent" from "your request
	// was wrong", exactly as they do for a connectivity test.
	TimedOut bool `json:"timed_out,omitempty"`
	Rejected bool `json:"rejected,omitempty"`
}

// handleWalkDevice handles GET /api/devices/{id}/walk, walking a stored device
// with the credentials already held for it.
func (s *Server) handleWalkDevice(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil {
		http.Error(w, "invalid device ID", http.StatusBadRequest)
		return
	}

	device, err := s.storage.GetDevice(r.Context(), id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, "device not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	root, limit, err := walkParams(r.URL.Query().Get("oid"), r.URL.Query().Get("limit"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	response := s.performSNMPWalk(r.Context(), probeForDevice(device), root, limit)
	respondJSON(w, statusForWalk(response), response)
}

// handleWalkDirect handles POST /api/snmp/walk, walking an address that is not
// a stored device. It mirrors /api/snmp/test, which exists for the same reason:
// a device is usually walked before it is registered.
func (s *Server) handleWalkDirect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SNMPTestRequest
		OID   string `json:"oid,omitempty"`
		Limit int    `json:"limit,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Address == "" {
		http.Error(w, "address is required", http.StatusBadRequest)
		return
	}

	limitText := ""
	if req.Limit != 0 {
		limitText = strconv.Itoa(req.Limit)
	}
	root, limit, err := walkParams(req.OID, limitText)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	probe := snmpProbe{
		Address:      req.Address,
		SNMPVersion:  req.SNMPVersion,
		Community:    req.Community,
		Port:         req.Port,
		Username:     req.Username,
		AuthProtocol: req.AuthProtocol,
		AuthPassword: req.AuthPassword,
		PrivProtocol: req.PrivProtocol,
		PrivPassword: req.PrivPassword,
	}
	response := s.performSNMPWalk(r.Context(), probe, root, limit)
	respondJSON(w, statusForWalk(response), response)
}

// walkParams validates the subtree and cap a caller asked for.
func walkParams(oid, limit string) (string, int, error) {
	root := strings.TrimSpace(oid)
	if root == "" {
		root = WalkDefaultRoot
	}
	root = strings.TrimPrefix(root, ".")
	if !validOID(root) {
		return "", 0, &SNMPRequestError{
			Field: "oid", Value: oid, Reason: "want a numeric OID such as 1.3.6.1.2.1",
		}
	}

	count := WalkDefaultLimit
	if text := strings.TrimSpace(limit); text != "" {
		parsed, err := strconv.Atoi(text)
		if err != nil || parsed < 1 {
			return "", 0, &SNMPRequestError{
				Field: "limit", Value: limit, Reason: "want a positive whole number",
			}
		}
		count = parsed
	}
	if count > WalkMaxLimit {
		count = WalkMaxLimit
	}
	return root, count, nil
}

// validOID reports whether text is a numeric OID. A walk root reaches gosnmp,
// which is strict about this, but a rejection naming the field is more use than
// whatever the library says.
func validOID(text string) bool {
	if text == "" {
		return false
	}
	for _, part := range strings.Split(text, ".") {
		if part == "" {
			return false
		}
		for _, digit := range part {
			if digit < '0' || digit > '9' {
				return false
			}
		}
	}
	return true
}

// statusForWalk maps a walk to the status describing it, on the same reasoning
// as statusForProbe: a silent device is not a server error.
func statusForWalk(resp *SNMPWalkResponse) int {
	switch {
	case resp.Success:
		return http.StatusOK
	case resp.Rejected:
		return http.StatusBadRequest
	case resp.TimedOut:
		return http.StatusGatewayTimeout
	default:
		return http.StatusBadGateway
	}
}

// performSNMPWalk walks root and reports what the device answered.
//
// A device that answers nothing under root is a success with no varbinds, not a
// failure: "this box does not implement that MIB" is the answer, and the most
// common reason for walking in the first place.
func (s *Server) performSNMPWalk(
	ctx context.Context, probe snmpProbe, root string, limit int,
) *SNMPWalkResponse {
	started := time.Now()

	ctx, cancel := context.WithTimeout(ctx, WalkBudget)
	defer cancel()

	response := &SNMPWalkResponse{
		Device: probe.Device, Address: probe.Address, Root: root,
		Varbinds: []SNMPWalkVarbind{},
	}

	conn, err := probe.connection(ctx)
	if err != nil {
		response.Rejected = true
		response.Error = err.Error()
		response.Duration = time.Since(started).String()
		return response
	}
	if connectErr := conn.Connect(); connectErr != nil {
		response.Error = (&SNMPProbeError{
			Device: probe.Device, Address: probe.Address, Reason: "connect failed", Err: connectErr,
		}).Error()
		response.Duration = time.Since(started).String()
		return response
	}
	defer func() { _ = conn.Conn.Close() }()

	names := s.oidNamer()

	// There is no GETBULK in SNMPv1, and asking for one is refused outright
	// rather than answered slowly, so v1 walks one variable at a time.
	walk := conn.BulkWalk
	if conn.Version == gosnmp.Version1 {
		walk = conn.Walk
	}

	err = walk(root, func(pdu gosnmp.SnmpPDU) error {
		if len(response.Varbinds) >= limit {
			response.Truncated = true
			// Ending the walk is what the cap is for; the error is the signal
			// to gosnmp and is discarded below.
			return errWalkLimitReached
		}

		oid := strings.TrimPrefix(pdu.Name, ".")
		varbind := SNMPWalkVarbind{
			OID:   oid,
			Type:  getSNMPTypeName(pdu.Type),
			Value: formatSNMPValue(pdu),
		}
		if object := names(oid); object != nil {
			varbind.Name = object.Name
			varbind.Module = object.Module
		}
		response.Varbinds = append(response.Varbinds, varbind)
		return nil
	})

	response.Duration = time.Since(started).String()
	response.Count = len(response.Varbinds)

	switch {
	case err == nil || response.Truncated:
		response.Success = true
		response.Message = walkSummary(response)
	case isProbeTimeout(ctx, err):
		response.TimedOut = true
		response.Error = (&SNMPUnreachableError{
			Device: probe.Device, Address: probe.Address, Budget: WalkBudget, Err: err,
		}).Error()
	default:
		response.Error = (&SNMPProbeError{
			Device: probe.Device, Address: probe.Address, Reason: "walk failed", Err: err,
		}).Error()
	}
	return response
}

// errWalkLimitReached stops a walk that has filled its cap.
var errWalkLimitReached = &WalkLimitError{}

// WalkLimitError reports that a walk stopped at its cap rather than at the end
// of the subtree.
type WalkLimitError struct{ Limit int }

func (e *WalkLimitError) Error() string {
	return "walk stopped at its limit of " + strconv.Itoa(e.Limit) + " objects"
}

// walkSummary is the one line describing what came back.
func walkSummary(resp *SNMPWalkResponse) string {
	switch {
	case resp.Truncated:
		return "stopped at " + strconv.Itoa(resp.Count) +
			" objects; raise the limit to see the rest of " + resp.Root
	case resp.Count == 0:
		return "the device answered nothing under " + resp.Root +
			", so it does not implement anything there"
	default:
		return "the device answered " + strconv.Itoa(resp.Count) +
			" objects under " + resp.Root
	}
}

// oidNamer returns a lookup from an instance OID to the MIB object it belongs
// to, or a function reporting nothing when no MIB metadata is available.
//
// A walked OID carries an instance suffix the MIB does not define — ".0" for a
// scalar, the row index for a table column — so the lookup drops trailing
// sub-identifiers until it finds a definition. Bounded, because an OID under no
// known module would otherwise be stripped all the way to the root and match
// something meaninglessly general.
func (s *Server) oidNamer() func(string) *mib.OIDMetadata {
	store, err := s.mibStore()
	if err != nil {
		return func(string) *mib.OIDMetadata { return nil }
	}

	// One walk asks about many OIDs sharing a prefix, and most of a table's
	// rows resolve to the same object, so results are memoized per walk.
	seen := make(map[string]*mib.OIDMetadata)

	return func(oid string) *mib.OIDMetadata {
		if found, ok := seen[oid]; ok {
			return found
		}

		var found *mib.OIDMetadata
		candidate := oid
		for drops := 0; drops <= maxInstanceSubIDs; drops++ {
			if object, err := store.GetOID(candidate); err == nil && describesAnObject(object) {
				found = object
				break
			}
			cut := strings.LastIndex(candidate, ".")
			if cut <= 0 {
				break
			}
			candidate = candidate[:cut]
		}

		seen[oid] = found
		return found
	}
}

// maxInstanceSubIDs is how many trailing sub-identifiers a lookup will drop
// looking for the defining object. A composite table index is several, and an
// index containing a string is one sub-identifier per character, so this is
// generous; what it rules out is stripping an unknown OID back to the root.
const maxInstanceSubIDs = 24

// describesAnObject reports whether metadata is a real object rather than a
// place in the tree.
//
// A MIB registers names for interior nodes as well as objects — "enterprises",
// "atenProducts", "mib-2" — and those are stored with the same shape as an
// object but no syntax and no access. Without this check every unrecognized
// vendor OID resolved to "enterprises", because stripping sub-identifiers
// eventually reaches 1.3.6.1.4.1 and that node exists. Labeling 68 distinct
// ATEN objects "enterprises" is worse than labeling none of them: it reads as
// though the MIB were understood.
func describesAnObject(object *mib.OIDMetadata) bool {
	if object == nil {
		return false
	}
	// A syntax, or a structural role in a table. Access is deliberately not
	// part of this: the parser records "unknown" for a registration node rather
	// than leaving it empty, so accepting a non-empty access let every
	// registration back in — a walk of a Supermicro BMC came back with 1317
	// objects attributed to ATEN-PRODUCTS-MIB, which defines six.
	return object.Type != "" ||
		object.IsScalar || object.IsColumn || object.IsTable || object.IsTableEntry
}
