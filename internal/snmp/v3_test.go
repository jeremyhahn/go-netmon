package snmp

import (
	"errors"
	"testing"

	"github.com/gosnmp/gosnmp"
)

// The security level is not a setting an operator picks; it follows from which
// protocols are configured, and the message flags have to agree with the
// message's contents or the agent rejects it before looking at the credentials.
//
// This was implemented twice and the two disagreed. The poller's client set
// authPriv unconditionally, so a device configured for authentication without
// encryption was refused by gosnmp before a packet left the host:
// "securityParameters.PrivacyProtocol is required". The ATEN KVM is exactly
// that device, and it read as unreachable.

func TestConfigureV3DerivesTheSecurityLevel(t *testing.T) {
	tests := []struct {
		name  string
		creds V3Credentials
		flags gosnmp.SnmpV3MsgFlags
		auth  gosnmp.SnmpV3AuthProtocol
		priv  gosnmp.SnmpV3PrivProtocol
	}{
		{
			name:  "authPriv",
			creds: V3Credentials{Username: "u", AuthProtocol: "SHA", AuthPassword: "a", PrivProtocol: "AES", PrivPassword: "p"},
			flags: gosnmp.AuthPriv, auth: gosnmp.SHA, priv: gosnmp.AES,
		},
		{
			// The case the poller could not express at all.
			name:  "authNoPriv",
			creds: V3Credentials{Username: "u", AuthProtocol: "SHA", AuthPassword: "a"},
			flags: gosnmp.AuthNoPriv, auth: gosnmp.SHA, priv: gosnmp.NoPriv,
		},
		{
			name:  "noAuthNoPriv",
			creds: V3Credentials{Username: "u"},
			flags: gosnmp.NoAuthNoPriv, auth: gosnmp.NoAuth, priv: gosnmp.NoPriv,
		},
		{
			name:  "MD5 and DES",
			creds: V3Credentials{Username: "u", AuthProtocol: "MD5", AuthPassword: "a", PrivProtocol: "DES", PrivPassword: "p"},
			flags: gosnmp.AuthPriv, auth: gosnmp.MD5, priv: gosnmp.DES,
		},
		{
			name:  "AES256",
			creds: V3Credentials{Username: "u", AuthProtocol: "SHA256", AuthPassword: "a", PrivProtocol: "AES256", PrivPassword: "p"},
			flags: gosnmp.AuthPriv, auth: gosnmp.SHA256, priv: gosnmp.AES256,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := &gosnmp.GoSNMP{}
			if err := ConfigureV3(conn, tt.creds); err != nil {
				t.Fatalf("ConfigureV3: %v", err)
			}

			if conn.Version != gosnmp.Version3 {
				t.Errorf("version = %v, want v3", conn.Version)
			}
			if conn.MsgFlags != tt.flags {
				t.Errorf("msg flags = %v, want %v", conn.MsgFlags, tt.flags)
			}
			params, ok := conn.SecurityParameters.(*gosnmp.UsmSecurityParameters)
			if !ok {
				t.Fatalf("security parameters = %T, want *gosnmp.UsmSecurityParameters", conn.SecurityParameters)
			}
			if params.AuthenticationProtocol != tt.auth {
				t.Errorf("auth = %v, want %v", params.AuthenticationProtocol, tt.auth)
			}
			if params.PrivacyProtocol != tt.priv {
				t.Errorf("priv = %v, want %v", params.PrivacyProtocol, tt.priv)
			}
		})
	}
}

// TestConfigureV3RejectsBadInput is the bad-input half. Each refusal names the
// field, because "invalid configuration" across five settings is not a
// diagnosis.
func TestConfigureV3RejectsBadInput(t *testing.T) {
	tests := []struct {
		name  string
		creds V3Credentials
		field string
	}{
		{"no user", V3Credentials{}, "username"},
		{"unknown auth protocol", V3Credentials{Username: "u", AuthProtocol: "SHA3"}, "auth_protocol"},
		{"unknown priv protocol", V3Credentials{Username: "u", PrivProtocol: "RC4"}, "priv_protocol"},
		{
			// A protocol with no passphrase cannot authenticate anything.
			// Accepting it would quietly downgrade the level that was asked for.
			"auth protocol without a passphrase",
			V3Credentials{Username: "u", AuthProtocol: "SHA"},
			"auth_password",
		},
		{
			"priv protocol without a passphrase",
			V3Credentials{Username: "u", AuthProtocol: "SHA", AuthPassword: "a", PrivProtocol: "AES"},
			"priv_password",
		},
		{
			// SNMPv3 has no noAuthPriv level.
			"privacy without authentication",
			V3Credentials{Username: "u", PrivProtocol: "AES", PrivPassword: "p"},
			"auth_protocol",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ConfigureV3(&gosnmp.GoSNMP{}, tt.creds)

			var configErr *V3ConfigError
			if !errors.As(err, &configErr) {
				t.Fatalf("error = %T (%v), want *V3ConfigError", err, err)
			}
			if configErr.Field != tt.field {
				t.Errorf("field = %q, want %q", configErr.Field, tt.field)
			}
		})
	}
}

// TestProtocolNamesAreCaseInsensitive covers what an operator types. The device
// form offers "SHA" and "AES"; a person at a terminal types "sha".
func TestProtocolNamesAreCaseInsensitive(t *testing.T) {
	conn := &gosnmp.GoSNMP{}
	err := ConfigureV3(conn, V3Credentials{
		Username: "u", AuthProtocol: " sha ", AuthPassword: "a",
		PrivProtocol: "aes128", PrivPassword: "p",
	})
	if err != nil {
		t.Fatalf("ConfigureV3 with lowercase, padded names: %v", err)
	}

	params := conn.SecurityParameters.(*gosnmp.UsmSecurityParameters)
	if params.AuthenticationProtocol != gosnmp.SHA || params.PrivacyProtocol != gosnmp.AES {
		t.Errorf("names were not recognized: auth=%v priv=%v",
			params.AuthenticationProtocol, params.PrivacyProtocol)
	}
}

// TestAuthNoPrivBuildsAUsableConnection is the regression this file exists for:
// the combination the poller refused now produces a connection gosnmp accepts.
func TestAuthNoPrivBuildsAUsableConnection(t *testing.T) {
	agent := newTestAgent(t,
		gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.1.1.0", Type: gosnmp.OctetString, Value: []byte("x")},
	)

	device := agentDeviceVersion(t, agent, "3")
	device.Username = "netmon"
	device.AuthProtocol = "SHA"
	device.AuthPassword = "netmon007"
	// No privacy: this is the combination the poller refused.

	client, err := NewClient(device, agentTimeout, 0)
	if err != nil {
		t.Fatalf("a v3 authNoPriv device was refused: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	// Connect is where gosnmp validates the security parameters, and where
	// "securityParameters.PrivacyProtocol is required" came from.
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect with authNoPriv: %v", err)
	}
}
