//go:build fips

package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// A FIPS build must DECLARE the missing capability, not just lack it.
//
// The route stays registered and answers 501 with the reason. An unregistered
// route would 404, which reads to a browser and to whoever is debugging it as
// a wrong URL or a routing bug and sends them looking for a mistake that is
// not there. This asserts the status AND the explanation, because a bare 501
// is the answer that produces that same wasted afternoon.
func TestWebSocketRouteDeclaresItselfUnavailable(t *testing.T) {
	s := newTestServer(t)

	rec := do(t, s, http.MethodGet, "/ws", "")

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotImplemented)
	}

	var body struct {
		Error       string `json:"error"`
		Reason      string `json:"reason"`
		Alternative string `json:"alternative"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON (%v): %s", err, rec.Body.String())
	}
	if !strings.Contains(body.Error, "FIPS") {
		t.Errorf("error does not say this is a FIPS build: %q", body.Error)
	}
	if !strings.Contains(body.Reason, "SHA-1") {
		t.Errorf("reason does not name the primitive: %q", body.Reason)
	}
	if body.Alternative == "" {
		t.Error("no alternative offered; a declared absence should say what to do instead")
	}
}
