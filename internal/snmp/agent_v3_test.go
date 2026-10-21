package snmp

import (
	"testing"

	"github.com/gosnmp/gosnmp"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/testsupport"
)

// The SNMPv3 half of the in-process test agent. The USM work -- discovery,
// authentication, privacy -- is testsupport's, shared with the poller tests.

const (
	testV3User     = "netmon"
	testV3AuthPass = "auth-passphrase-1"
	testV3PrivPass = "priv-passphrase-1"
)

// newV3TestAgent starts an SNMPv3 agent whose one user is testV3User with
// the given protocols and the test passphrases.
func newV3TestAgent(t *testing.T, auth gosnmp.SnmpV3AuthProtocol, priv gosnmp.SnmpV3PrivProtocol, pdus ...gosnmp.SnmpPDU) *testAgent {
	t.Helper()
	user := testsupport.SNMPv3User{
		Name:           testV3User,
		AuthProtocol:   auth,
		AuthPassphrase: testV3AuthPass,
		PrivProtocol:   priv,
	}
	if priv != gosnmp.NoPriv {
		user.PrivPassphrase = testV3PrivPass
	}
	engine, err := testsupport.NewUSMEngine(user)
	if err != nil {
		t.Fatalf("agent keys: %v", err)
	}
	return newConfiguredAgent(t, func(a *testAgent) { a.usm = engine }, pdus...)
}

// v3AgentDevice is a device for a, with the agent's user and protocols.
func v3AgentDevice(t *testing.T, a *testAgent, authProtocol, privProtocol string) *config.Device {
	t.Helper()
	device := agentDeviceVersion(t, a, "3")
	device.Community = ""
	device.Username = testV3User
	device.AuthProtocol = authProtocol
	device.AuthPassword = testV3AuthPass
	if privProtocol != "" {
		device.PrivProtocol = privProtocol
		device.PrivPassword = testV3PrivPass
	}
	return device
}

// serveV3 answers one v3 datagram, reporting false for one it drops.
func (a *testAgent) serveV3(datagram []byte) ([]byte, bool) {
	wire, err := a.usm.Answer(datagram, a.respond)
	if err != nil {
		a.rejected.Add(1)
		return nil, false
	}
	return wire, true
}
