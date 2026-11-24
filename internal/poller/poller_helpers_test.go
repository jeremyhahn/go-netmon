package poller

import (
	"net"
	"testing"
)

// unreachableSNMPAddress returns a loopback address with no listener, so a dial fails at once and nothing leaves the host.
func unreachableSNMPAddress(t testing.TB) string {
	t.Helper()

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("failed to reserve a udp port: %v", err)
	}
	addr := conn.LocalAddr().String()
	if err := conn.Close(); err != nil {
		t.Fatalf("failed to release reserved udp port: %v", err)
	}

	return addr
}
