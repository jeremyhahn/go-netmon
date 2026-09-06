//go:build fips

package snmp

import (
	"errors"
	"strings"
	"testing"

	"github.com/gosnmp/gosnmp"
)

// What a FIPS build does with the three protocols it does not have.
//
// The negative half. A FIPS build must not merely fail to select MD5, SHA-1 or
// DES -- it must say that it recognised them and does not provide them. "want
// SHA224, SHA256, ..." reads as a typo in the config file and sends an
// operator looking for a mistake that is not there; the real problem is that
// this binary was built with -tags fips, which is a different problem with a
// different fix.
func TestFIPSBuildRefusesTheLegacyProtocols(t *testing.T) {
	tests := []struct {
		name  string
		creds V3Credentials
		field string
	}{
		{"MD5", V3Credentials{Username: "u", AuthProtocol: "MD5", AuthPassword: "a"}, "auth_protocol"},
		{"SHA-1", V3Credentials{Username: "u", AuthProtocol: "SHA", AuthPassword: "a"}, "auth_protocol"},
		{
			"DES",
			V3Credentials{Username: "u", AuthProtocol: "SHA256", AuthPassword: "a", PrivProtocol: "DES", PrivPassword: "p"},
			"priv_protocol",
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
			// The distinguishing content. Asserting only that an error came
			// back would pass against the generic "unknown protocol" answer,
			// which is the message this test exists to rule out.
			if !strings.Contains(configErr.Reason, "absent from FIPS builds") {
				t.Errorf("reason does not say the protocol is absent from FIPS builds: %q", configErr.Reason)
			}
			if !strings.Contains(configErr.Reason, "FIPS 140-3") {
				t.Errorf("reason does not name the standard: %q", configErr.Reason)
			}
		})
	}
}

// The positive half, and the more important one: SNMPv3 itself is NOT absent
// from a FIPS build. Authentication with the SHA-2 family and privacy with AES
// work exactly as they do in an ordinary build. Only three protocol names are
// missing, and a deployment subject to FIPS 140-3 could not lawfully have used
// those anyway.
//
// Without this assertion the test above is satisfied by a build in which
// SNMPv3 does not work at all.
func TestFIPSBuildStillSpeaksSNMPv3(t *testing.T) {
	tests := []struct {
		name  string
		creds V3Credentials
		flags gosnmp.SnmpV3MsgFlags
		auth  gosnmp.SnmpV3AuthProtocol
		priv  gosnmp.SnmpV3PrivProtocol
	}{
		{
			name:  "SHA256 and AES",
			creds: V3Credentials{Username: "u", AuthProtocol: "SHA256", AuthPassword: "a", PrivProtocol: "AES", PrivPassword: "p"},
			flags: gosnmp.AuthPriv, auth: gosnmp.SHA256, priv: gosnmp.AES,
		},
		{
			name:  "SHA512 and AES256",
			creds: V3Credentials{Username: "u", AuthProtocol: "SHA512", AuthPassword: "a", PrivProtocol: "AES256", PrivPassword: "p"},
			flags: gosnmp.AuthPriv, auth: gosnmp.SHA512, priv: gosnmp.AES256,
		},
		{
			name:  "SHA224 authNoPriv",
			creds: V3Credentials{Username: "u", AuthProtocol: "SHA224", AuthPassword: "a"},
			flags: gosnmp.AuthNoPriv, auth: gosnmp.SHA224, priv: gosnmp.NoPriv,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := &gosnmp.GoSNMP{}
			if err := ConfigureV3(conn, tt.creds); err != nil {
				t.Fatalf("a FIPS build refused an approved SNMPv3 configuration: %v", err)
			}
			if conn.MsgFlags != tt.flags {
				t.Errorf("flags = %v, want %v", conn.MsgFlags, tt.flags)
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

// The accepted-protocol list in the "unknown protocol" message is derived from
// the registry, so it must not advertise a protocol this build does not have.
// A hardcoded list is exactly what would.
func TestFIPSErrorDoesNotAdvertiseAbsentProtocols(t *testing.T) {
	err := ConfigureV3(&gosnmp.GoSNMP{}, V3Credentials{Username: "u", AuthProtocol: "SHA3", AuthPassword: "a"})

	var configErr *V3ConfigError
	if !errors.As(err, &configErr) {
		t.Fatalf("error = %T (%v), want *V3ConfigError", err, err)
	}
	for _, absent := range []string{"MD5", "SHA384, MD5", "DES"} {
		if strings.Contains(configErr.Reason, absent) {
			t.Errorf("the accepted list names %q, which this build does not provide: %q", absent, configErr.Reason)
		}
	}
	if !strings.Contains(configErr.Reason, "SHA256") {
		t.Errorf("the accepted list omits SHA256, which this build does provide: %q", configErr.Reason)
	}
}
