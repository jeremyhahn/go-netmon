//go:build fips

package api

import (
	"net/http"
)

// The FIPS build's /ws route answers 501: RFC 6455's Sec-WebSocket-Accept needs SHA-1, which fips140=only refuses.
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
