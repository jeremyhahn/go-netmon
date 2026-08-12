package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// "netmon device walk" is the troubleshooting command: it answers what a device
// actually exposes, using the credentials the server already holds. What it
// prints has to be usable as evidence, which means the empty case and the
// failure case have to read differently from each other.

// walkServer answers the device lookup and the walk with a payload of our own.
func walkServer(t *testing.T, status int, body any) *http.ServeMux {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/devices", deviceListHandler(t, testDeviceFixture()))
	mux.HandleFunc("GET /api/devices/7/walk", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Fatalf("encode walk response: %v", err)
		}
	})
	return mux
}

// TestWalkPrintsWhatTheDeviceAnswered is the ordinary case, including that an
// OID with no imported MIB still appears -- walking before importing is the
// whole point.
func TestWalkPrintsWhatTheDeviceAnswered(t *testing.T) {
	env, out := newFakeEnv(t, walkServer(t, http.StatusOK, client.SNMPWalkResponse{
		Success: true, Device: "core-01", Root: "1.3.6.1.4.1.21317",
		Count: 2, Duration: "0.4s",
		Varbinds: []client.SNMPWalkVarbind{
			{OID: "1.3.6.1.4.1.21317.1.3.1.13.1.9.4.0", Name: "temperature",
				Module: "KNGeneric-MIB", Type: "OctetString", Value: "35 C"},
			{OID: "1.3.6.1.4.1.21317.1.3.1.13.1.9.9.0", Type: "Integer", Value: "1"},
		},
	}))

	if err := runCommand(t, newDeviceCommand(env), "walk", "core-01", "1.3.6.1.4.1.21317"); err != nil {
		t.Fatalf("walk: %v", err)
	}

	printed := out.String()
	for _, want := range []string{"temperature", "KNGeneric-MIB", "35 C", "1.3.6.1.4.1.21317.1.3.1.13.1.9.9.0"} {
		if !strings.Contains(printed, want) {
			t.Errorf("the walk does not show %q:\n%s", want, printed)
		}
	}
	// An object no MIB describes still has to be listed, with the empty columns
	// marked rather than left as a ragged gap.
	if !strings.Contains(printed, "-") {
		t.Errorf("an unnamed object left a hole instead of a dash:\n%s", printed)
	}
}

// TestWalkSaysTheSubtreeIsEmptyRatherThanFailing is the case the command exists
// for. "This device implements nothing there" decides whether a MIB is worth
// importing, and it must not look like an error.
func TestWalkSaysTheSubtreeIsEmptyRatherThanFailing(t *testing.T) {
	env, out := newFakeEnv(t, walkServer(t, http.StatusOK, client.SNMPWalkResponse{
		Success: true, Device: "core-01", Address: "198.51.100.252",
		Root: "1.3.6.1.4.1.21317", Count: 0, Duration: "0.2s",
		Varbinds: []client.SNMPWalkVarbind{},
	}))

	if err := runCommand(t, newDeviceCommand(env), "walk", "core-01", "1.3.6.1.4.1.21317"); err != nil {
		t.Fatalf("an empty subtree must not fail the command: %v", err)
	}

	printed := out.String()
	if !strings.Contains(printed, "answered nothing") {
		t.Errorf("the empty result is not stated plainly:\n%s", printed)
	}
	if !strings.Contains(printed, "no metrics") {
		t.Errorf("the consequence for polling is not spelled out:\n%s", printed)
	}
}

// TestWalkOfASilentDeviceFails is the other half: a device that did not answer
// at all is a failure, and the command must exit non-zero so a script notices.
func TestWalkOfASilentDeviceFails(t *testing.T) {
	env, out := newFakeEnv(t, walkServer(t, http.StatusGatewayTimeout, client.SNMPWalkResponse{
		Success: false, TimedOut: true, Device: "core-01", Address: "198.51.100.252",
		Root: "1.3.6.1", Error: "device 198.51.100.252 did not answer within 60s",
	}))

	err := runCommand(t, newDeviceCommand(env), "walk", "core-01")

	var walkErr *WalkError
	if !asWalkError(err, &walkErr) {
		t.Fatalf("error is %T (%v), want a *WalkError", err, err)
	}
	// The explanation is printed, not only returned, so the reader sees the
	// device and the reason rather than a bare exit code.
	printed := out.String()
	if !strings.Contains(printed, "did not answer") {
		t.Errorf("the timeout is not explained:\n%s", printed)
	}
	if !strings.Contains(printed, "198.51.100.252") {
		t.Errorf("the failure does not name the device:\n%s", printed)
	}
}

// TestWalkRejectsTheOIDGivenTwice covers the conflicting-input case: the OID can
// come from an argument or a flag, and two different values is a mistake worth
// naming rather than silently resolving.
func TestWalkRejectsTheOIDGivenTwice(t *testing.T) {
	env, _ := newFakeEnv(t, walkServer(t, http.StatusOK, client.SNMPWalkResponse{Success: true}))

	err := runCommand(t, newDeviceCommand(env),
		"walk", "core-01", "1.3.6.1.2.1", "--oid", "1.3.6.1.4.1.9")
	if err == nil {
		t.Fatal("two different OIDs were accepted")
	}
	if !strings.Contains(err.Error(), "twice") {
		t.Errorf("the refusal does not explain the conflict: %v", err)
	}

	// The same OID in both places is not a conflict, so it must be allowed.
	env2, _ := newFakeEnv(t, walkServer(t, http.StatusOK, client.SNMPWalkResponse{
		Success: true, Root: "1.3.6.1.2.1", Varbinds: []client.SNMPWalkVarbind{},
	}))
	if err := runCommand(t, newDeviceCommand(env2),
		"walk", "core-01", "1.3.6.1.2.1", "--oid", "1.3.6.1.2.1"); err != nil {
		t.Errorf("the same OID in both places was refused: %v", err)
	}
}

// TestWalkNamedKeepsTheDeviceCounts checks the --named view.
//
// Filtering to what the MIBs describe must not restate how much the device
// exposes: a device answering 600 objects of which 2 are named still answered
// 600, and reporting 2 would misrepresent the box.
func TestWalkNamedKeepsTheDeviceCounts(t *testing.T) {
	full := &client.SNMPWalkResponse{
		Success: true, Root: "1.3.6.1", Count: 4, Duration: "1s",
		Varbinds: []client.SNMPWalkVarbind{
			{OID: "1.3.6.1.2.1.1.1.0", Name: "sysDescr", Module: "SNMPv2-MIB", Value: "a"},
			{OID: "1.3.6.1.4.1.99.1.0", Value: "b"},
			{OID: "1.3.6.1.2.1.1.5.0", Name: "sysName", Module: "SNMPv2-MIB", Value: "c"},
			{OID: "1.3.6.1.4.1.99.2.0", Value: "d"},
		},
	}

	filtered := onlyNamed(full)

	if len(filtered.Varbinds) != 2 {
		t.Fatalf("kept %d varbinds, want the 2 with names", len(filtered.Varbinds))
	}
	if filtered.Count != 4 {
		t.Errorf("Count = %d, want the 4 the device answered", filtered.Count)
	}
	// The original must be untouched: it is the caller's, and --json prints it.
	if len(full.Varbinds) != 4 {
		t.Errorf("filtering mutated the original walk: %d varbinds left", len(full.Varbinds))
	}
}

// TestWalkValueStaysOnOneLine covers a multi-line sysDescr, which is common on
// Cisco and would otherwise break every column after it.
func TestWalkValueStaysOnOneLine(t *testing.T) {
	collapsed := oneLine("Cisco IOS Software\r\nVersion 15.2\nCopyright (c) 1986")

	if strings.ContainsAny(collapsed, "\r\n") {
		t.Errorf("the value still spans lines: %q", collapsed)
	}
	for _, want := range []string{"Cisco IOS Software", "Version 15.2"} {
		if !strings.Contains(collapsed, want) {
			t.Errorf("collapsing lost %q: %q", want, collapsed)
		}
	}
	// A single-line value must come through untouched, spacing included.
	if got := oneLine("KN4116VA,MFG#:A1H21350020"); got != "KN4116VA,MFG#:A1H21350020" {
		t.Errorf("a single-line value was altered: %q", got)
	}
}

// asWalkError is errors.As for *WalkError.
func asWalkError(err error, target **WalkError) bool {
	walkErr, ok := err.(*WalkError)
	if ok {
		*target = walkErr
	}
	return ok
}
