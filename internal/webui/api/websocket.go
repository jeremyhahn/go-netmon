//go:build !fips

package api

import (
	"log"
	"net/http"

	"github.com/gorilla/websocket"

	ws "github.com/jeremyhahn/go-netmon/internal/webui/websocket"
)

// The live-metric WebSocket handshake, excluded from FIPS builds: RFC 6455 section 4.2.2 requires SHA-1.
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
