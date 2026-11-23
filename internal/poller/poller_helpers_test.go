package poller

import (
	"net"
	"testing"
)

// unreachableSNMPAddress returns a loopback address with no listener bound
// to it. Dialing it over UDP yields an immediate "connection refused" from
// the kernel (via ICMP port-unreachable) instead of the silent packet loss
// a real, unreachable device would produce, which the SNMP client can only
// detect by waiting out its configured timeout and retries.
//
// Tests use this instead of placeholder addresses such as 203.0.113.1 so
// that exercising the poller's real SNMP client path stays hermetic: no
// packets leave the host, and failure is deterministic and effectively
// instantaneous, keeping the unit test suite fast per project conventions.
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
