package snmp

import (
	"strconv"
	"strings"

	"github.com/gosnmp/gosnmp"
)

// SNMPv3 user-based security, in one place.
//
// There were two implementations of this: the poller's client and the web UI's
// connectivity tester. They disagreed, which is the usual outcome. The client
// set MsgFlags to authPriv unconditionally, so a device configured for
// authentication without encryption — a real and common setting — was refused
// by gosnmp before a packet left the host, with
// "securityParameters.PrivacyProtocol is required". The tester, fixed later,
// derived the flags correctly. A device could therefore pass its connectivity
// test and still be unpollable.
//
// The security level is not a separate setting an operator chooses; it follows
// from which protocols are configured, and the flags have to agree with the
// message's actual contents or the agent rejects it before looking at the
// credentials.

// V3Credentials is what user-based security needs. An empty protocol means that
// layer is off, which is what makes authNoPriv and noAuthNoPriv expressible.
type V3Credentials struct {
	Username     string
	AuthProtocol string
	AuthPassword string
	PrivProtocol string
	PrivPassword string
}

// ConfigureV3 sets up user-based security on a connection.
func ConfigureV3(conn *gosnmp.GoSNMP, creds V3Credentials) error {
	if creds.Username == "" {
		return &V3ConfigError{Field: "username", Value: "", Reason: "required for SNMPv3"}
	}

	auth, err := authProtocol(creds.AuthProtocol)
	if err != nil {
		return err
	}
	priv, err := privProtocol(creds.PrivProtocol)
	if err != nil {
		return err
	}

	// A protocol with no passphrase cannot authenticate or encrypt anything.
	// Treating it as "off" would quietly downgrade the security level the
	// operator asked for, so it is refused instead.
	if auth != gosnmp.NoAuth && creds.AuthPassword == "" {
		return &V3ConfigError{
			Field: "auth_password", Value: "", Reason: "required with an authentication protocol",
		}
	}
	if priv != gosnmp.NoPriv && creds.PrivPassword == "" {
		return &V3ConfigError{
			Field: "priv_password", Value: "", Reason: "required with a privacy protocol",
		}
	}
	// Privacy without authentication is not a security level SNMPv3 has: the
	// levels are noAuthNoPriv, authNoPriv and authPriv.
	if priv != gosnmp.NoPriv && auth == gosnmp.NoAuth {
		return &V3ConfigError{
			Field:  "auth_protocol",
			Value:  "",
			Reason: "privacy requires authentication; SNMPv3 has no noAuthPriv level",
		}
	}

	conn.Version = gosnmp.Version3
	conn.SecurityModel = gosnmp.UserSecurityModel
	conn.SecurityParameters = &gosnmp.UsmSecurityParameters{
		UserName:                 creds.Username,
		AuthenticationProtocol:   auth,
		AuthenticationPassphrase: creds.AuthPassword,
		PrivacyProtocol:          priv,
		PrivacyPassphrase:        creds.PrivPassword,
	}
	conn.MsgFlags = securityLevel(auth, priv)
	return nil
}

// securityLevel is the flag set matching the protocols in force.
func securityLevel(auth gosnmp.SnmpV3AuthProtocol, priv gosnmp.SnmpV3PrivProtocol) gosnmp.SnmpV3MsgFlags {
	switch {
	case priv != gosnmp.NoPriv:
		return gosnmp.AuthPriv
	case auth != gosnmp.NoAuth:
		return gosnmp.AuthNoPriv
	default:
		return gosnmp.NoAuthNoPriv
	}
}

// authProtocol maps a name to gosnmp's constant. Empty means no authentication.
func authProtocol(name string) (gosnmp.SnmpV3AuthProtocol, error) {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "", "NONE":
		return gosnmp.NoAuth, nil
	case "MD5":
		return gosnmp.MD5, nil
	case "SHA":
		return gosnmp.SHA, nil
	case "SHA224":
		return gosnmp.SHA224, nil
	case "SHA256":
		return gosnmp.SHA256, nil
	case "SHA384":
		return gosnmp.SHA384, nil
	case "SHA512":
		return gosnmp.SHA512, nil
	default:
		return gosnmp.NoAuth, &V3ConfigError{
			Field: "auth_protocol", Value: name,
			Reason: "want SHA, SHA224, SHA256, SHA384, SHA512, MD5, or empty for none",
		}
	}
}

// privProtocol maps a name to gosnmp's constant. Empty means no privacy.
//
// "AES" is AES128; the wider key sizes are named explicitly because a device
// configured for AES256 and a poller sending AES128 authenticate and then fail
// to decrypt, which reads as a wrong passphrase.
func privProtocol(name string) (gosnmp.SnmpV3PrivProtocol, error) {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "", "NONE":
		return gosnmp.NoPriv, nil
	case "DES":
		return gosnmp.DES, nil
	case "AES", "AES128":
		return gosnmp.AES, nil
	case "AES192":
		return gosnmp.AES192, nil
	case "AES256":
		return gosnmp.AES256, nil
	case "AES192C":
		return gosnmp.AES192C, nil
	case "AES256C":
		return gosnmp.AES256C, nil
	default:
		return gosnmp.NoPriv, &V3ConfigError{
			Field: "priv_protocol", Value: name,
			Reason: "want AES, AES192, AES256, DES, or empty for none",
		}
	}
}

// V3ConfigError reports a v3 setting this cannot use.
type V3ConfigError struct {
	Field  string
	Value  string
	Reason string
}

func (e *V3ConfigError) Error() string {
	return "snmp: " + e.Field + " " + strconv.Quote(e.Value) + ": " + e.Reason
}
