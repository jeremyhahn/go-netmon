package fipsprofile

import (
	"errors"
	"testing"
)

func TestCheckSNMP(t *testing.T) {
	tests := []struct {
		name    string
		version string
		auth    string
		priv    string
		refused string // field refused under the profile; empty means accepted
	}{
		{"v1", "1", "", "", "snmp_version"},
		{"v1 spelled v1", "v1", "", "", "snmp_version"},
		{"v2c", "2c", "", "", "snmp_version"},
		{"v2c spelled v2c", "V2C", "", "", "snmp_version"},
		{"v3 MD5 auth", "3", "MD5", "AES", "auth_protocol"},
		{"v3 MD5 lower case", "3", " md5 ", "", "auth_protocol"},
		{"v3 SHA-1 auth", "3", "SHA", "AES", "auth_protocol"},
		{"v3 DES priv", "3", "SHA256", "DES", "priv_protocol"},
		{"v3 unknown auth SHA3", "3", "SHA3", "AES", "auth_protocol"},
		{"v3 unknown auth SHA-256 spelling", "3", "SHA-256", "AES", "auth_protocol"},
		{"v3 unknown priv 3DES", "3", "SHA256", "3DES", "priv_protocol"},
		{"v3 unknown priv AES512", "3", "SHA256", "AES512", "priv_protocol"},
		{"v3 SHA256 AES", "3", "SHA256", "AES", ""},
		{"v3 SHA256 AES128", "3", "SHA256", "AES128", ""},
		{"v3 SHA384 AES192C", "3", "SHA384", "AES192C", ""},
		{"v3 SHA512 AES256C", "3", "SHA512", "AES256C", ""},
		{"v3 SHA512 AES256", "3", "SHA512", "AES256", ""},
		{"v3 SHA224 AES192", "v3", "SHA224", "AES192", ""},
		{"v3 authNoPriv", "3", "SHA384", "", "priv_protocol"},
		{"v3 authNoPriv NONE", "3", "SHA256", "NONE", "priv_protocol"},
		{"v3 noAuthNoPriv", "3", "", "", "auth_protocol"},
		{"v3 noAuthNoPriv NONE", "3", "none", "none", "auth_protocol"},
	}

	for _, tt := range tests {
		t.Run(tt.name+"/profile", func(t *testing.T) {
			defer Force(true)()
			err := CheckSNMP(tt.version, tt.auth, tt.priv)
			if tt.refused == "" {
				if err != nil {
					t.Fatalf("CheckSNMP() = %v, want accepted", err)
				}
				return
			}
			var refused *RefusedError
			if !errors.As(err, &refused) {
				t.Fatalf("CheckSNMP() = %v, want *RefusedError", err)
			}
			if refused.Field != tt.refused {
				t.Errorf("Field = %q, want %q", refused.Field, tt.refused)
			}
		})

		t.Run(tt.name+"/normal", func(t *testing.T) {
			defer Force(false)()
			if err := CheckSNMP(tt.version, tt.auth, tt.priv); err != nil {
				t.Fatalf("CheckSNMP() = %v, normal mode must accept everything", err)
			}
		})
	}
}

func TestForceRestoresPreviousState(t *testing.T) {
	restoreOn := Force(true)
	if !Active() {
		t.Fatal("Active() = false after Force(true)")
	}
	restoreOff := Force(false)
	if Active() {
		t.Fatal("Active() = true after Force(false)")
	}
	restoreOff()
	if !Active() {
		t.Fatal("Active() = false after restoring Force(true)")
	}
	restoreOn()
}
