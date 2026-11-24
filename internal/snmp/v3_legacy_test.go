//go:build !fips

package snmp

import (
	"testing"

	"github.com/gosnmp/gosnmp"
)

// The legacy protocols the default build offers; TestFIPSBuildRefusesTheLegacyProtocols covers the FIPS build.
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
