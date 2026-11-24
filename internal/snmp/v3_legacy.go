//go:build !fips

package snmp

import "github.com/gosnmp/gosnmp"

// HMAC-MD5-96, HMAC-SHA-96 and CBC-DES (RFC 3414 §6, §8), not FIPS 140-3 approved; this file is excluded from the fips build.
func init() {
	authProtocols["MD5"] = gosnmp.MD5
	authProtocols["SHA"] = gosnmp.SHA

	privProtocols["DES"] = gosnmp.DES
}
