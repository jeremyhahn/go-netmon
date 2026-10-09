// Package fipsprofile decides, at runtime, whether the FIPS profile is in force
// and which SNMP settings it refuses.
//
// The enforcement model is FIPS-capable build plus runtime activation (company
// boundary ruling 1, handbook 04-security-model.md): the profile is on when the
// Go runtime is in FIPS 140-3 mode (GODEBUG=fips140=only, set by configuration
// or because the host kernel is in FIPS mode). Product ruling 7 is the policy:
// SNMPv1 and v2c, whose community strings travel in the clear, and the
// MD5/SHA-1/DES v3 protocols are legacy interop options. Normal mode permits
// them, because the product exists to monitor legacy gear; the FIPS profile
// refuses them with a typed error and permits only SNMPv3 with SHA-2
// authentication and AES privacy.
//
// This complements the fips build tag (docs/fips.md), which removes the v3
// legacy protocols from the binary. A binary built without the tag but run
// under fips140=only needs this check, because it still contains them.
package fipsprofile

import (
	"crypto/fips140"
	"strconv"
	"strings"
	"sync/atomic"
)

// override forces the profile on or off. Zero means follow the runtime.
var override atomic.Int32

const (
	forceOff int32 = 1
	forceOn  int32 = 2
)

// Active reports whether the FIPS profile is in force.
func Active() bool {
	switch override.Load() {
	case forceOn:
		return true
	case forceOff:
		return false
	}
	return fips140.Enabled()
}

// Force pins the profile on or off regardless of the runtime and returns a
// function that restores the previous state. It exists so tests in any package
// can exercise both modes without re-executing under GODEBUG.
func Force(active bool) (restore func()) {
	next := forceOff
	if active {
		next = forceOn
	}
	prev := override.Swap(next)
	return func() { override.Store(prev) }
}

// RefusedError reports a setting the FIPS profile does not permit. Field is the
// configuration key, Value what was configured, Reason why it is refused and
// what to do about it.
type RefusedError struct {
	Field  string
	Value  string
	Reason string
}

func (e *RefusedError) Error() string {
	return "fips profile: " + e.Field + " " + strconv.Quote(e.Value) + " is refused: " + e.Reason
}

// CheckVersion refuses SNMPv1 and SNMPv2c when the profile is active. Both the
// "2c" and "v2c" spellings are in use, so both are normalised.
func CheckVersion(version string) error {
	if !Active() {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(version)) {
	case "1", "v1":
		return &RefusedError{Field: "snmp_version", Value: version, Reason: "SNMPv1 sends its community string" +
			" in the clear and has no FIPS 140-3 approved protection; use SNMPv3 with SHA-2 authentication and AES privacy"}
	case "2c", "v2c":
		return &RefusedError{Field: "snmp_version", Value: version, Reason: "SNMPv2c sends its community string" +
			" in the clear and has no FIPS 140-3 approved protection; use SNMPv3 with SHA-2 authentication and AES privacy"}
	}
	return nil
}

// CheckAuthProtocol refuses the SNMPv3 authentication protocols FIPS 140-3 does
// not approve when the profile is active. "SHA" in SNMPv3 is HMAC-SHA-96, that
// is SHA-1 (RFC 3414 section 6).
func CheckAuthProtocol(name string) error {
	if !Active() {
		return nil
	}
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "MD5":
		return &RefusedError{Field: "auth_protocol", Value: name, Reason: "SNMPv3 HMAC-MD5-96 (RFC 3414 section 6)" +
			" is not a FIPS 140-3 approved algorithm; use SHA224, SHA256, SHA384 or SHA512"}
	case "SHA", "SHA1", "SHA-1":
		return &RefusedError{Field: "auth_protocol", Value: name, Reason: "SNMPv3 \"SHA\" is HMAC-SHA-96, that is SHA-1" +
			" (RFC 3414 section 6), which is not a FIPS 140-3 approved algorithm; use SHA224, SHA256, SHA384 or SHA512"}
	}
	return nil
}

// CheckPrivProtocol refuses the SNMPv3 privacy protocols FIPS 140-3 does not
// approve when the profile is active.
func CheckPrivProtocol(name string) error {
	if !Active() {
		return nil
	}
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "DES":
		return &RefusedError{Field: "priv_protocol", Value: name, Reason: "SNMPv3 CBC-DES (RFC 3414 section 8)" +
			" is not a FIPS 140-3 approved algorithm; use AES, AES192 or AES256"}
	}
	return nil
}

// CheckSNMP applies the whole SNMP policy to one target's settings: the
// version first, then, for SNMPv3, the authentication and privacy protocols.
func CheckSNMP(version, authProtocol, privProtocol string) error {
	if err := CheckVersion(version); err != nil {
		return err
	}
	if err := CheckAuthProtocol(authProtocol); err != nil {
		return err
	}
	return CheckPrivProtocol(privProtocol)
}
