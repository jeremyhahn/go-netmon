//go:build !fips

package api

import (
	"log"
	"net/http"

	"github.com/gorilla/websocket"

	ws "github.com/jeremyhahn/go-netmon/internal/webui/websocket"
)

// The live-metric WebSocket surface.
//
// It lives in its own file because it cannot exist in a FIPS build. RFC 6455
// section 4.2.2 defines Sec-WebSocket-Accept as a SHA-1 digest of the client's
// key and a fixed GUID, so gorilla/websocket hashes with crypto/sha1 on both
// the Upgrade and the Dial side (util.go computeAcceptKey). Under
// GODEBUG=fips140=only the runtime refuses that operation and the handshake
// fails; there is no approved substitute, because the algorithm is named by
// the protocol rather than chosen by the implementation.
//
// Only the HANDSHAKE is excluded. The Hub, its clients, the delta encoding and
// the query path touch no non-approved primitive and are compiled into both
// builds unchanged -- the FIPS build simply never has a client to serve.
var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(_ *http.Request) bool {
		return true // Allow all origins in development
	},
}

// registerWebSocketRoute installs the upgrade handler.
func (s *Server) registerWebSocketRoute() {
	s.router.HandleFunc("/ws", s.handleWebSocket)
}

// handleWebSocket upgrades HTTP to WebSocket
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}

	wsClient := ws.NewClient(s.hub, conn)
	s.hub.RegisterClient(wsClient)

	go wsClient.WritePump()
	go wsClient.ReadPump()
}
