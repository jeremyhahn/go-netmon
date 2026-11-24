//go:build fips

package snmp

// The fips build omits MD5, SHA-1 and DES and says so when a device is configured for one.
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
