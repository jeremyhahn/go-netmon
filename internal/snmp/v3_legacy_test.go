//go:build !fips

package snmp

import (
	"testing"

	"github.com/gosnmp/gosnmp"
)

// The protocols an ordinary build still offers and a FIPS build does not.
//
// This exists so that moving MD5, SHA-1 and DES out of the shared table did
// not quietly move them out of the suite. They are a supported capability of
// the default build -- the product exists to monitor legacy gear, a great deal
// of which speaks only these -- and the ruling that compiles them out of the
// FIPS build says in terms that "compiled out" never means deleted. A
// capability with no test is on its way to being deleted by accident.
//
// Its FIPS counterpart is TestFIPSBuildRefusesTheLegacyProtocols in
// v3_fips_test.go, which asserts the same three names are refused there, with
// a message that says why.
func TestConfigureV3AcceptsTheLegacyProtocols(t *testing.T) {
	tests := []struct {
		name  string
		creds V3Credentials
		auth  gosnmp.SnmpV3AuthProtocol
		priv  gosnmp.SnmpV3PrivProtocol
	}{
		{
			name:  "MD5 and DES",
			creds: V3Credentials{Username: "u", AuthProtocol: "MD5", AuthPassword: "a", PrivProtocol: "DES", PrivPassword: "p"},
			auth:  gosnmp.MD5, priv: gosnmp.DES,
		},
		{
			// "SHA" in SNMPv3 is HMAC-SHA-96, i.e. SHA-1 (RFC 3414 section 6).
			// It is still what most deployed device firmware offers.
			name:  "SHA-1 and AES",
			creds: V3Credentials{Username: "u", AuthProtocol: "SHA", AuthPassword: "a", PrivProtocol: "AES", PrivPassword: "p"},
			auth:  gosnmp.SHA, priv: gosnmp.AES,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := &gosnmp.GoSNMP{}
			if err := ConfigureV3(conn, tt.creds); err != nil {
				t.Fatalf("ConfigureV3: %v", err)
			}
			if conn.MsgFlags != gosnmp.AuthPriv {
				t.Errorf("flags = %v, want AuthPriv", conn.MsgFlags)
			}
			params := conn.SecurityParameters.(*gosnmp.UsmSecurityParameters)
			if params.AuthenticationProtocol != tt.auth {
				t.Errorf("auth = %v, want %v", params.AuthenticationProtocol, tt.auth)
			}
			if params.PrivacyProtocol != tt.priv {
				t.Errorf("priv = %v, want %v", params.PrivacyProtocol, tt.priv)
			}
		})
	}
}

// Lowercase and padded, the way an operator types them. Split from
// TestProtocolNamesAreCaseInsensitive when that test's fixture moved to SHA-2,
// so the normalisation is still covered for the legacy names too.
func TestLegacyProtocolNamesAreCaseInsensitive(t *testing.T) {
	conn := &gosnmp.GoSNMP{}
	err := ConfigureV3(conn, V3Credentials{
		Username: "u", AuthProtocol: " sha ", AuthPassword: "a",
		PrivProtocol: "des", PrivPassword: "p",
	})
	if err != nil {
		t.Fatalf("ConfigureV3 with lowercase, padded legacy names: %v", err)
	}

	params := conn.SecurityParameters.(*gosnmp.UsmSecurityParameters)
	if params.AuthenticationProtocol != gosnmp.SHA || params.PrivacyProtocol != gosnmp.DES {
		t.Errorf("names were not recognized: auth=%v priv=%v",
			params.AuthenticationProtocol, params.PrivacyProtocol)
	}
}
