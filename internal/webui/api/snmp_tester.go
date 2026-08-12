package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/gosnmp/gosnmp"

	"github.com/jeremyhahn/go-netmon/internal/snmp"
	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// SNMPTestRequest represents the request body for SNMP test
type SNMPTestRequest struct {
	Address     string `json:"address"`
	SNMPVersion string `json:"snmp_version"`
	Community   string `json:"community,omitempty"`
	Port        int    `json:"port,omitempty"`
	Timeout     int    `json:"timeout,omitempty"` // seconds

	Username     string `json:"username,omitempty"`
	AuthProtocol string `json:"auth_protocol,omitempty"`
	AuthPassword string `json:"auth_password,omitempty"`
	PrivProtocol string `json:"priv_protocol,omitempty"`
	PrivPassword string `json:"priv_password,omitempty"`
}

// SNMPTestResult represents a single OID result
type SNMPTestResult struct {
	OID   string `json:"oid"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

// SNMPTestResponse represents the response from SNMP test
type SNMPTestResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`

	// Device and Address say which box the result is about. A failure that
	// does not name its subject is not actionable, and this response is what
	// the CLI renders when a probe fails.
	Device  string `json:"device,omitempty"`
	Address string `json:"address,omitempty"`

	Duration string           `json:"duration"`
	Results  []SNMPTestResult `json:"results,omitempty"`
	Error    string           `json:"error,omitempty"`

	// TimedOut says the device stayed silent for the whole budget, and
	// Rejected says the server refused the request before sending anything.
	// They are what separates "your input is wrong" from "that box is down".
	TimedOut bool `json:"timed_out,omitempty"`
	Rejected bool `json:"rejected,omitempty"`

	SysDescr    string `json:"sys_descr,omitempty"`
	SysName     string `json:"sys_name,omitempty"`
	SysUpTime   string `json:"sys_uptime,omitempty"`
	SysContact  string `json:"sys_contact,omitempty"`
	SysLocation string `json:"sys_location,omitempty"`
}

// Standard system OIDs
var systemOIDs = []string{
	"1.3.6.1.2.1.1.1.0", // sysDescr
	"1.3.6.1.2.1.1.3.0", // sysUpTime
	"1.3.6.1.2.1.1.4.0", // sysContact
	"1.3.6.1.2.1.1.5.0", // sysName
	"1.3.6.1.2.1.1.6.0", // sysLocation
}

var oidNames = map[string]string{
	"1.3.6.1.2.1.1.1.0": "sysDescr",
	"1.3.6.1.2.1.1.3.0": "sysUpTime",
	"1.3.6.1.2.1.1.4.0": "sysContact",
	"1.3.6.1.2.1.1.5.0": "sysName",
	"1.3.6.1.2.1.1.6.0": "sysLocation",
}

// handleTestSNMPConnection handles POST /api/devices/{id}/test-snmp
func (s *Server) handleTestSNMPConnection(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	idStr := vars["id"]

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid device ID", http.StatusBadRequest)
		return
	}

	// Get device from storage
	device, err := s.storage.GetDevice(r.Context(), id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, "device not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// probeForDevice honors the device's own port. This used to pass
	// defaultSNMPPort unconditionally, so a device configured on any other port
	// was tested against 161 and reported unreachable while polling fine.
	response := s.performSNMPTest(r.Context(), probeForDevice(device))
	respondJSON(w, statusForProbe(response), response)
}

// handleTestSNMPDirect handles POST /api/snmp/test - Test SNMP without saved device
func (s *Server) handleTestSNMPDirect(w http.ResponseWriter, r *http.Request) {
	var req SNMPTestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.Address == "" {
		http.Error(w, "address is required", http.StatusBadRequest)
		return
	}

	if req.SNMPVersion == "" {
		req.SNMPVersion = "v2c"
	}

	if req.Port == 0 {
		req.Port = defaultSNMPPort
	}

	response := s.performSNMPTest(r.Context(), snmpProbe{
		Address:      req.Address,
		SNMPVersion:  req.SNMPVersion,
		Community:    req.Community,
		Port:         req.Port,
		Username:     req.Username,
		AuthProtocol: req.AuthProtocol,
		AuthPassword: req.AuthPassword,
		PrivProtocol: req.PrivProtocol,
		PrivPassword: req.PrivPassword,
	})
	respondJSON(w, statusForProbe(response), response)
}

// statusForProbe maps a probe result to the status that describes it.
//
// A silent device is a gateway timeout, not a server error: nothing here is
// broken, the box downstream did not answer. A request the server refused
// before sending anything is the caller's to fix. Either way the body is the
// same structured result, so a client renders one shape whatever happened.
func statusForProbe(resp *SNMPTestResponse) int {
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

// defaultSNMPPort is the port a probe uses when the caller names none.
const defaultSNMPPort = 161

// snmpProbe describes one connectivity test.
type snmpProbe struct {
	Device      string // name as the operator knows it; empty for an ad-hoc address
	Address     string // host, optionally with a port
	SNMPVersion string
	Community   string
	Port        int

	// SNMPv3. Testing a v3 device is the case that most needs a test: a
	// community string is either right or wrong, but v3 has a user, two
	// protocols and two passphrases, and the failure for every wrong
	// combination looks the same from the poller.
	Username     string
	AuthProtocol string
	AuthPassword string
	PrivProtocol string
	PrivPassword string
}

// connection turns a probe into a configured, unconnected gosnmp session.
//
// Every server-side SNMP operation builds its session here. That is deliberate:
// the poller and the connectivity tester once had separate copies of this and
// disagreed about SNMPv3, so a device could pass its test and still be
// unpollable. A second copy for walking would have reintroduced exactly that.
//
// The returned session carries ctx, so its deadline bounds the whole retry
// sequence rather than one request.
func (p snmpProbe) connection(ctx context.Context) (*gosnmp.GoSNMP, error) {
	// A port in the address wins over the field, which is how a caller names a
	// device listening somewhere other than 161.
	host := p.Address
	port := p.Port
	if h, parsed, err := net.SplitHostPort(p.Address); err == nil {
		host = h
		if parsedPort, err := strconv.Atoi(parsed); err == nil {
			port = parsedPort
		}
	}
	if port == 0 {
		port = defaultSNMPPort
	}

	// Both the request field and the parsed address are caller-supplied. Without
	// this check the conversion below would silently wrap and reach a different
	// port than the one asked for.
	if port < 1 || port > math.MaxUint16 {
		return nil, &SNMPRequestError{
			Field:  "port",
			Value:  strconv.Itoa(port),
			Reason: "must be between 1 and " + strconv.Itoa(math.MaxUint16),
		}
	}

	conn := &gosnmp.GoSNMP{
		Target:    host,
		Port:      uint16(port), // #nosec G115 -- range checked above
		Transport: "udp",
		Timeout:   SNMPProbeTimeout,
		Retries:   SNMPProbeRetries,
		Context:   ctx,
	}

	switch strings.ToLower(p.SNMPVersion) {
	case "v2c", "2c":
		conn.Version = gosnmp.Version2c
		conn.Community = p.Community
	case "v1", "1":
		conn.Version = gosnmp.Version1
		conn.Community = p.Community
	case "v3", "3":
		if err := snmp.ConfigureV3(conn, snmp.V3Credentials{
			Username:     p.Username,
			AuthProtocol: p.AuthProtocol,
			AuthPassword: p.AuthPassword,
			PrivProtocol: p.PrivProtocol,
			PrivPassword: p.PrivPassword,
		}); err != nil {
			return nil, err
		}
	default:
		return nil, &SNMPRequestError{
			Field:  "snmp_version",
			Value:  p.SNMPVersion,
			Reason: "want v1, v2c or v3",
		}
	}

	return conn, nil
}

// probeForDevice describes a stored device, so every endpoint that reaches a
// registered device does it with the same credentials in the same order.
func probeForDevice(device *storage.Device) snmpProbe {
	port := device.Port
	if port == 0 {
		port = defaultSNMPPort
	}
	return snmpProbe{
		Device:       device.Name,
		Address:      device.Address,
		SNMPVersion:  device.SNMPVersion,
		Community:    device.Community,
		Port:         port,
		Username:     device.Username,
		AuthProtocol: device.AuthProtocol,
		AuthPassword: device.AuthPassword,
		PrivProtocol: device.PrivProtocol,
		PrivPassword: device.PrivPassword,
	}
}

// performSNMPTest probes a device's system group and reports what it found.
//
// The whole probe is bounded by SNMPProbeBudget. That bound is the point: this
// runs inside an HTTP handler, and an unbounded probe against a device that
// never answers outlives the connection's write deadline, which makes the
// server close the connection with no response at all. The caller then sees a
// bare EOF instead of being told its device is unreachable.
//
// gosnmp honors the deadline of the context it is given on every attempt, so
// the budget caps the retry sequence rather than just one request.
func (s *Server) performSNMPTest(ctx context.Context, probe snmpProbe) *SNMPTestResponse {
	startTime := time.Now()

	ctx, cancel := context.WithTimeout(ctx, SNMPProbeBudget)
	defer cancel()

	snmpConn, err := probe.connection(ctx)
	if err != nil {
		return probe.reject(startTime, err)
	}

	// Connect
	if connectErr := snmpConn.Connect(); connectErr != nil {
		return probe.fail(startTime, &SNMPProbeError{
			Device: probe.Device, Address: probe.Address, Reason: "connect failed", Err: connectErr,
		})
	}
	// The probe is finished by the time this runs; a close error says nothing
	// about the result being reported.
	defer func() { _ = snmpConn.Conn.Close() }()

	// Perform SNMP GET for system OIDs
	result, err := snmpConn.Get(systemOIDs)
	if err != nil {
		if isProbeTimeout(ctx, err) {
			return probe.timeout(startTime, &SNMPUnreachableError{
				Device: probe.Device, Address: probe.Address, Budget: SNMPProbeBudget, Err: err,
			})
		}
		return probe.fail(startTime, &SNMPProbeError{
			Device: probe.Device, Address: probe.Address, Reason: "SNMP GET failed", Err: err,
		})
	}

	// Parse results
	response := &SNMPTestResponse{
		Success:  true,
		Message:  "SNMP connection successful",
		Device:   probe.Device,
		Address:  probe.Address,
		Duration: time.Since(startTime).String(),
		Results:  make([]SNMPTestResult, 0),
	}

	for _, variable := range result.Variables {
		oidStr := strings.TrimPrefix(variable.Name, ".")

		name := oidNames[oidStr]
		if name == "" {
			name = oidStr
		}

		value := formatSNMPValue(variable)
		typeStr := getSNMPTypeName(variable.Type)

		response.Results = append(response.Results, SNMPTestResult{
			OID:   oidStr,
			Name:  name,
			Type:  typeStr,
			Value: value,
		})

		// Extract specific system values
		switch oidStr {
		case "1.3.6.1.2.1.1.1.0":
			response.SysDescr = value
		case "1.3.6.1.2.1.1.3.0":
			response.SysUpTime = value
		case "1.3.6.1.2.1.1.4.0":
			response.SysContact = value
		case "1.3.6.1.2.1.1.5.0":
			response.SysName = value
		case "1.3.6.1.2.1.1.6.0":
			response.SysLocation = value
		}
	}

	s.log().DebugContext(ctx, "SNMP probe succeeded",
		"device", probe.Device, "host", snmpConn.Target, "port", snmpConn.Port,
		"sys_name", response.SysName)

	return response
}

// isProbeTimeout reports whether err means the device stayed silent, rather
// than the probe failing for a reason the device had no part in.
//
// What counts as "it never answered" is decided in one place, internal/snmp,
// so that a probe, the poller and a health check cannot form three different
// opinions about it. The context is the part that belongs here: a deadline the
// budget imposed is visible there rather than on the error.
func isProbeTimeout(ctx context.Context, err error) bool {
	return ctx.Err() != nil || snmp.TimedOut(err)
}

// reject reports a probe the server refused before sending anything.
func (p snmpProbe) reject(start time.Time, err error) *SNMPTestResponse {
	resp := p.failure(start, err)
	resp.Rejected = true
	return resp
}

// timeout reports a device that stayed silent for the whole budget.
func (p snmpProbe) timeout(start time.Time, err error) *SNMPTestResponse {
	resp := p.failure(start, err)
	resp.TimedOut = true
	return resp
}

// fail reports a probe that failed for a reason other than silence.
func (p snmpProbe) fail(start time.Time, err error) *SNMPTestResponse {
	return p.failure(start, err)
}

// failure builds the structured negative result shared by every failure path.
func (p snmpProbe) failure(start time.Time, err error) *SNMPTestResponse {
	return &SNMPTestResponse{
		Success:  false,
		Message:  "SNMP connection test failed",
		Device:   p.Device,
		Address:  p.Address,
		Duration: time.Since(start).String(),
		Error:    err.Error(),
	}
}

// formatSNMPValue converts SNMP value to string
func formatSNMPValue(pdu gosnmp.SnmpPDU) string {
	switch pdu.Type {
	case gosnmp.OctetString:
		return string(pdu.Value.([]byte))
	case gosnmp.Integer:
		return fmt.Sprintf("%d", pdu.Value.(int))
	case gosnmp.Counter32:
		return fmt.Sprintf("%d", pdu.Value.(uint))
	case gosnmp.Counter64:
		return fmt.Sprintf("%d", pdu.Value.(uint64))
	case gosnmp.Gauge32:
		return fmt.Sprintf("%d", pdu.Value.(uint))
	case gosnmp.TimeTicks:
		ticks := pdu.Value.(uint32)
		// Convert ticks to human-readable duration
		seconds := ticks / 100
		days := seconds / 86400
		hours := (seconds % 86400) / 3600
		minutes := (seconds % 3600) / 60
		secs := seconds % 60
		return fmt.Sprintf("%d days, %02d:%02d:%02d", days, hours, minutes, secs)
	case gosnmp.ObjectIdentifier:
		return pdu.Value.(string)
	case gosnmp.IPAddress:
		return pdu.Value.(string)
	case gosnmp.NoSuchObject, gosnmp.NoSuchInstance:
		return "No such object/instance"
	case gosnmp.Null:
		return "null"
	default:
		return fmt.Sprintf("%v", pdu.Value)
	}
}

// getSNMPTypeName returns human-readable type name
func getSNMPTypeName(t gosnmp.Asn1BER) string {
	switch t {
	case gosnmp.OctetString:
		return "OctetString"
	case gosnmp.Integer:
		return "Integer"
	case gosnmp.Counter32:
		return "Counter32"
	case gosnmp.Counter64:
		return "Counter64"
	case gosnmp.Gauge32:
		return "Gauge32"
	case gosnmp.TimeTicks:
		return "TimeTicks"
	case gosnmp.ObjectIdentifier:
		return "OID"
	case gosnmp.IPAddress:
		return "IPAddress"
	case gosnmp.NoSuchObject:
		return "NoSuchObject"
	case gosnmp.NoSuchInstance:
		return "NoSuchInstance"
	case gosnmp.Null:
		return "Null"
	default:
		return fmt.Sprintf("Type(%d)", t)
	}
}
