//go:build fips

package api

import (
	"net/http"
)

// The FIPS build's /ws route.
//
// The endpoint is still REGISTERED, and answers. A capability that silently
// disappears is worse than one that is declared: an unregistered route returns
// 404, which reads to the browser and to whoever is debugging it as a wrong
// URL or a routing bug, and sends them looking for a mistake that is not
// there. 501 with the reason spelled out is the honest answer -- the server
// understood the request and does not implement it in this build.
//
// See websocket.go for why: RFC 6455's Sec-WebSocket-Accept is a SHA-1 digest,
// which GODEBUG=fips140=only refuses, and the protocol names the algorithm so
// no approved substitute exists.
func (s *Server) registerWebSocketRoute() {
	s.router.HandleFunc("/ws", s.handleWebSocketUnavailable)
}

func (s *Server) handleWebSocketUnavailable(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotImplemented)
	_, _ = w.Write([]byte(`{"error":"live metric streaming is not available in FIPS builds",` +
		`"reason":"the RFC 6455 handshake requires a SHA-1 digest (Sec-WebSocket-Accept), which is not a FIPS 140-3 approved algorithm",` +
		`"alternative":"poll the REST metric endpoints"}` + "\n"))
}
