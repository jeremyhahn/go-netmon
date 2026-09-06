//go:build !fips

package snmp

import "github.com/gosnmp/gosnmp"

// The SNMPv3 protocols RFC 3414 specifies that FIPS 140-3 does not approve:
// HMAC-MD5-96 and HMAC-SHA-96 for authentication (§6 -- "SHA" in SNMPv3 means
// SHA-1), and CBC-DES for privacy (§8).
//
// They are registered here, in a file the fips build tag excludes, rather than
// gated at runtime. A FIPS build does not contain this file, so it cannot
// select them at all; an ordinary build is unchanged and keeps every protocol.
// That is the company rule (configuration.md, owner 2026-08-18): a feature
// depending on a non-approved algorithm is compiled out of the FIPS build, not
// isolated behind an argument about which code paths reach it.
//
// This is deliberately NOT a deletion. The product exists to monitor legacy
// gear, a great deal of which speaks only these, and a deployment not subject
// to FIPS is entitled to them. The FIPS build is a constrained variant, not a
// replacement.
func init() {
	authProtocols["MD5"] = gosnmp.MD5
	authProtocols["SHA"] = gosnmp.SHA

	privProtocols["DES"] = gosnmp.DES
}
