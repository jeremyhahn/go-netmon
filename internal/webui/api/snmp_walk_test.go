package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/mib"
)

// Walking a device's OID tree.
//
// The parameter handling and the result classification are what a caller sees
// first, and both have to be right before a packet is worth sending: a walk
// answering nothing must read as "this device does not implement that", not as a
// failure, because that answer is the reason to walk in the first place.

// TestWalkDefaultsToTheWholeTree covers the no-argument case: a caller who does
// not know what to ask for wants everything.
func TestWalkDefaultsToTheWholeTree(t *testing.T) {
	root, limit, err := walkParams("", "")
	if err != nil {
		t.Fatalf("an empty request was refused: %v", err)
	}
	if root != WalkDefaultRoot {
		t.Errorf("root = %q, want %q", root, WalkDefaultRoot)
	}
	if limit != WalkDefaultLimit {
		t.Errorf("limit = %d, want %d", limit, WalkDefaultLimit)
	}
}

// TestWalkAcceptsALeadingDot is the ergonomic case: every tool that prints OIDs
// prints them dotted, so a pasted OID has to work.
func TestWalkAcceptsALeadingDot(t *testing.T) {
	root, _, err := walkParams(".1.3.6.1.4.1.21317", "")
	if err != nil {
		t.Fatalf("a dotted OID was refused: %v", err)
	}
	if root != "1.3.6.1.4.1.21317" {
		t.Errorf("root = %q, want the OID without its leading dot", root)
	}
}

// TestWalkRefusesWhatIsNotAnOID covers the bad-input case. The message has to
// name the field, since a walk takes two parameters and either could be wrong.
func TestWalkRefusesWhatIsNotAnOID(t *testing.T) {
	for _, bad := range []string{"sysDescr", "1.3.6.1.2.1.x", "1..3", "1.3.", "-1.3"} {
		_, _, err := walkParams(bad, "")
		if err == nil {
			t.Errorf("%q was accepted as an OID", bad)
			continue
		}
		var request *SNMPRequestError
		if !errors.As(err, &request) {
			t.Errorf("%q gave %T, want an *SNMPRequestError", bad, err)
			continue
		}
		if request.Field != "oid" {
			t.Errorf("%q blamed field %q, want \"oid\"", bad, request.Field)
		}
	}
}

// TestWalkRefusesAnUnusableLimit covers the other parameter.
func TestWalkRefusesAnUnusableLimit(t *testing.T) {
	for _, bad := range []string{"0", "-5", "many", "1.5"} {
		_, _, err := walkParams("1.3.6.1", bad)
		if err == nil {
			t.Errorf("limit %q was accepted", bad)
			continue
		}
		var request *SNMPRequestError
		if !errors.As(err, &request) || request.Field != "limit" {
			t.Errorf("limit %q gave %v, want an *SNMPRequestError naming limit", bad, err)
		}
	}
}

// TestWalkCapsAnEnormousLimit keeps one request from trying to hold a whole
// router in memory. The cap is silent by design -- the response reports what it
// actually walked and sets Truncated -- so this pins the clamp itself.
func TestWalkCapsAnEnormousLimit(t *testing.T) {
	_, limit, err := walkParams("1.3.6.1", "5000000")
	if err != nil {
		t.Fatalf("a large limit was refused rather than capped: %v", err)
	}
	if limit != WalkMaxLimit {
		t.Errorf("limit = %d, want it clamped to %d", limit, WalkMaxLimit)
	}

	// A limit under the cap is honored exactly.
	if _, exact, err := walkParams("1.3.6.1", "2000"); err != nil || exact != 2000 {
		t.Errorf("limit 2000 gave (%d, %v), want it kept", exact, err)
	}
}

// TestASilentSubtreeIsASuccessfulWalk is the semantic that matters most.
//
// Walking is how "does this device implement this MIB?" gets answered, and the
// answer "no" arrives as an empty walk. Reporting that as a failure would make
// the tool useless for its main purpose.
func TestASilentSubtreeIsASuccessfulWalk(t *testing.T) {
	empty := &SNMPWalkResponse{Success: true, Root: "1.3.6.1.4.1.9", Count: 0}

	if status := statusForWalk(empty); status != http.StatusOK {
		t.Errorf("an empty walk answered %d, want 200: the device implementing nothing is a result", status)
	}

	summary := walkSummary(empty)
	if summary == "" {
		t.Fatal("an empty walk explained nothing")
	}
	for _, want := range []string{"1.3.6.1.4.1.9", "does not implement"} {
		if !strings.Contains(summary, want) {
			t.Errorf("the summary does not mention %q: %s", want, summary)
		}
	}
}

// TestWalkStatusSeparatesSilenceFromBadInput keeps the three failure shapes
// distinguishable, so a client can tell "fix your request" from "that box is
// down" without parsing prose.
func TestWalkStatusSeparatesSilenceFromBadInput(t *testing.T) {
	cases := []struct {
		name string
		resp *SNMPWalkResponse
		want int
	}{
		{"a walk that worked", &SNMPWalkResponse{Success: true}, http.StatusOK},
		{"a request the server refused", &SNMPWalkResponse{Rejected: true}, http.StatusBadRequest},
		{"a device that stayed silent", &SNMPWalkResponse{TimedOut: true}, http.StatusGatewayTimeout},
		{"anything else", &SNMPWalkResponse{}, http.StatusBadGateway},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := statusForWalk(tt.resp); got != tt.want {
				t.Errorf("status = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestATruncatedWalkSaysHowToSeeMore covers the cap being hit: the count is real
// and the message has to say the subtree continues, or a reader takes a partial
// answer for the whole one.
func TestATruncatedWalkSaysHowToSeeMore(t *testing.T) {
	resp := &SNMPWalkResponse{
		Success: true, Truncated: true, Root: "1.3.6.1.2.1", Count: WalkDefaultLimit,
	}

	if status := statusForWalk(resp); status != http.StatusOK {
		t.Errorf("a truncated walk answered %d, want 200: the objects it did read are good", status)
	}
	summary := walkSummary(resp)
	if !strings.Contains(summary, "limit") {
		t.Errorf("the summary does not say the limit stopped it: %s", summary)
	}
	if !strings.Contains(summary, "1.3.6.1.2.1") {
		t.Errorf("the summary does not name the subtree: %s", summary)
	}
}

// TestWalkLimitErrorReads pins the sentinel's own message, since it can reach a
// log even though the walk that raised it succeeds.
func TestWalkLimitErrorReads(t *testing.T) {
	err := &WalkLimitError{Limit: 500}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("the error does not name the limit: %v", err)
	}
}

// TestARegistrationNodeIsNotAName is the bug a live walk found immediately.
//
// A MIB registers names for interior nodes as well as objects, so stripping
// sub-identifiers off an unrecognized OID eventually reaches 1.3.6.1.4.1 —
// "enterprises" — which exists. Every one of a device's 68 vendor objects came
// back labeled "enterprises / SNMPv2-SMI", which reads as though the vendor MIB
// were understood when none of it was.
func TestARegistrationNodeIsNotAName(t *testing.T) {
	registrations := []*mib.OIDMetadata{
		{OID: "1.3.6.1.4.1", Name: "enterprises", Module: "SNMPv2-SMI"},
		{OID: "1.3.6.1.2.1", Name: "mib-2", Module: "SNMPv2-SMI"},
		{OID: "1.3.6.1.4.1.21317.1", Name: "atenProducts", Module: "ATEN-SMI"},
		// The parser writes "unknown" rather than an empty access for a
		// registration, so a check that only asked whether access was set let
		// every one of these back in: a walk of a Supermicro BMC attributed
		// 1317 objects to ATEN-PRODUCTS-MIB, which defines six.
		{OID: "1.3.6.1.4.1.21317.1.3.1", Name: "kvmoverip", Module: "ATEN-PRODUCTS-MIB",
			Access: "unknown"},
		{OID: "1.3.6.1.4.1.21317.1.3", Name: "overip", Module: "ATEN-PRODUCTS-MIB",
			Access: "unknown", Status: "current"},
	}
	for _, node := range registrations {
		if describesAnObject(node) {
			t.Errorf("%s is a place in the tree, not an object, but was accepted as a name", node.Name)
		}
	}

	// Anything with syntax or access is a real object and must still resolve,
	// including the table nodes a column's name is looked up through.
	objects := []*mib.OIDMetadata{
		{OID: "1.3.6.1.2.1.1.1", Name: "sysDescr", Type: "OctetString", Access: "read-only"},
		{OID: "1.3.6.1.2.1.2.2.1.10", Name: "ifInOctets", Type: "Counter32", IsColumn: true},
		{OID: "1.3.6.1.2.1.2.2", Name: "ifTable", Access: "not-accessible", IsTable: true},
		{OID: "1.3.6.1.4.1.21317.1.3.1.13.1.9.4", Name: "temperature", Type: "DisplayString"},
	}
	for _, object := range objects {
		if !describesAnObject(object) {
			t.Errorf("%s is a real object but was rejected as a name", object.Name)
		}
	}

	if describesAnObject(nil) {
		t.Error("a missing object was accepted as a name")
	}
}
