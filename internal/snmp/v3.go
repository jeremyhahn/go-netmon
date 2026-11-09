package snmp

import (
	"sort"
	"strconv"
	"strings"

	"github.com/gosnmp/gosnmp"

	"github.com/jeremyhahn/go-netmon/internal/fipsprofile"
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
	if err := configureV3(conn, creds, creds.AuthPassword != "", creds.PrivPassword != ""); err != nil {
		return err
	}
	usm := conn.SecurityParameters.(*gosnmp.UsmSecurityParameters)
	usm.AuthenticationPassphrase = creds.AuthPassword
	usm.PrivacyPassphrase = creds.PrivPassword
	return nil
}

// configureV3 is ConfigureV3 without the passphrases: it validates the
// security level against which passphrases exist and leaves the connection
// holding none. A client whose passphrases are sealed installs them for the
// length of each request; see Client.call.
func configureV3(conn *gosnmp.GoSNMP, creds V3Credentials, hasAuth, hasPriv bool) error {
	if creds.Username == "" {
		return &V3ConfigError{Field: "username", Value: "", Reason: "required for SNMPv3"}
	}

	// The FIPS profile is a runtime decision, so an ordinary build run under
	// fips140=only still contains MD5, SHA-1 and DES and must refuse them here.
	if err := fipsprofile.CheckAuthProtocol(creds.AuthProtocol); err != nil {
		return err
	}
	if err := fipsprofile.CheckPrivProtocol(creds.PrivProtocol); err != nil {
		return err
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
	if auth != gosnmp.NoAuth && !hasAuth {
		return &V3ConfigError{
			Field: "auth_password", Value: "", Reason: "required with an authentication protocol",
		}
	}
	if priv != gosnmp.NoPriv && !hasPriv {
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
		UserName:               creds.Username,
		AuthenticationProtocol: auth,
		PrivacyProtocol:        priv,
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

// The protocol registries. Map-based rather than a switch so that the set a
// build offers is data, and so that the two build configurations differ by
// which entries exist rather than by a second copy of the lookup.
//
// These hold the FIPS 140-3 approved protocols, which every build has.
// SNMPv3's MD5 and SHA (which is SHA-1, RFC 3414 §6) and DES are added by
// v3_legacy.go, compiled only when the fips tag is absent; v3_fips.go instead
// records them as omitted, so a FIPS build can say what happened to them
// rather than calling the operator's setting invalid.
var (
	authProtocols = map[string]gosnmp.SnmpV3AuthProtocol{
		"":       gosnmp.NoAuth,
		"NONE":   gosnmp.NoAuth,
		"SHA224": gosnmp.SHA224,
		"SHA256": gosnmp.SHA256,
		"SHA384": gosnmp.SHA384,
		"SHA512": gosnmp.SHA512,
	}

	privProtocols = map[string]gosnmp.SnmpV3PrivProtocol{
		"":        gosnmp.NoPriv,
		"NONE":    gosnmp.NoPriv,
		"AES":     gosnmp.AES,
		"AES128":  gosnmp.AES,
		"AES192":  gosnmp.AES192,
		"AES256":  gosnmp.AES256,
		"AES192C": gosnmp.AES192C,
		"AES256C": gosnmp.AES256C,
	}

	// omittedProtocols names protocols this build knows about and does not
	// provide, mapped to the explanation the operator gets. Empty in an
	// ordinary build: nothing is omitted there.
	omittedProtocols = map[string]string{}
)

// acceptedNames lists a registry's keys, sorted, with the empty key rendered
// as the words that describe it. It is derived from the map rather than
// written out, so the error message cannot claim to accept a protocol the
// build does not have -- which is exactly what a hardcoded list would do in
// the FIPS build.
func acceptedNames[T comparable](m map[string]T) string {
	names := make([]string, 0, len(m))
	for k := range m {
		if k == "" || k == "NONE" {
			continue
		}
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ", ") + ", or empty for none"
}

// protocolError reports a protocol this build cannot use, distinguishing a
// name that is wrong from one that is merely absent here.
func protocolError[T comparable](field, name, key string, accepted map[string]T) error {
	if reason, ok := omittedProtocols[key]; ok {
		return &V3ConfigError{Field: field, Value: name, Reason: reason}
	}
	return &V3ConfigError{Field: field, Value: name, Reason: "want " + acceptedNames(accepted)}
}

// protocolKey normalises an operator-supplied protocol name.
func protocolKey(name string) string {
	return strings.ToUpper(strings.TrimSpace(name))
}

// authProtocol maps a name to gosnmp's constant. Empty means no authentication.
func authProtocol(name string) (gosnmp.SnmpV3AuthProtocol, error) {
	key := protocolKey(name)
	if p, ok := authProtocols[key]; ok {
		return p, nil
	}
	return gosnmp.NoAuth, protocolError("auth_protocol", name, key, authProtocols)
}

// privProtocol maps a name to gosnmp's constant. Empty means no privacy.
//
// "AES" is AES128; the wider key sizes are named explicitly because a device
// configured for AES256 and a poller sending AES128 authenticate and then fail
// to decrypt, which reads as a wrong passphrase.
func privProtocol(name string) (gosnmp.SnmpV3PrivProtocol, error) {
	key := protocolKey(name)
	if p, ok := privProtocols[key]; ok {
		return p, nil
	}
	return gosnmp.NoPriv, protocolError("priv_protocol", name, key, privProtocols)
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
