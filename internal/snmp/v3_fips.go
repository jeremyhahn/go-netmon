//go:build fips

package snmp

// The counterpart to v3_legacy.go. This build does not provide MD5, SHA-1 or
// DES, and records why so that an operator whose device is configured for one
// is told what happened rather than that the setting is invalid.
//
// The distinction matters at three in the morning: "want SHA224, SHA256, ..."
// reads as a typo in the config file and sends someone looking for one. The
// message below says the protocol is real, is understood, and is not in this
// binary -- which is a different problem with a different fix (deploy the
// ordinary build, or reconfigure the device).
func init() {
	const authFix = "; configure the device for SHA224, SHA256, SHA384 or SHA512"
	const privFix = "; configure the device for AES, AES192 or AES256"

	omittedProtocols["MD5"] = "SNMPv3 HMAC-MD5-96 (RFC 3414 section 6) is not a FIPS 140-3 approved" +
		" algorithm and is absent from FIPS builds of go-netmon" + authFix
	omittedProtocols["SHA"] = "SNMPv3 \"SHA\" is HMAC-SHA-96, that is SHA-1 (RFC 3414 section 6), which is not a" +
		" FIPS 140-3 approved algorithm and is absent from FIPS builds of go-netmon" + authFix
	omittedProtocols["DES"] = "SNMPv3 CBC-DES (RFC 3414 section 8) is not a FIPS 140-3 approved" +
		" algorithm and is absent from FIPS builds of go-netmon" + privFix
}
