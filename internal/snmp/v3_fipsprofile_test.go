package snmp

import (
	"errors"
	"testing"

	"github.com/gosnmp/gosnmp"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/fipsprofile"
)

// The runtime FIPS profile (product ruling 7): refused under the profile with a
// typed error, unchanged otherwise. These run in both build configurations; in
// a fips-tagged build the legacy protocols are absent either way, so the
// "normal mode" rows for them are asserted only where the build has them.

func TestConfigureV3UnderTheFIPSProfile(t *testing.T) {
	tests := []struct {
		name    string
		auth    string
		priv    string
		refused string
	}{
		{"MD5 auth", "MD5", "AES", "auth_protocol"},
		{"SHA-1 auth", "SHA", "AES", "auth_protocol"},
		{"DES priv", "SHA256", "DES", "priv_protocol"},
		{"SHA256 AES", "SHA256", "AES", ""},
		{"SHA512 AES256", "SHA512", "AES256", ""},
		{"SHA224 AES192", "SHA224", "AES192", ""},
	}
	for _, tt := range tests {
		creds := V3Credentials{
			Username: "u", AuthProtocol: tt.auth, AuthPassword: "authpassword",
			PrivProtocol: tt.priv, PrivPassword: "privpassword",
		}

		t.Run(tt.name+"/profile", func(t *testing.T) {
			defer fipsprofile.Force(true)()
			err := ConfigureV3(&gosnmp.GoSNMP{}, creds)
			if tt.refused == "" {
				if err != nil {
					t.Fatalf("ConfigureV3() = %v, want accepted", err)
				}
				return
			}
			var refused *fipsprofile.RefusedError
			if !errors.As(err, &refused) {
				t.Fatalf("ConfigureV3() = %v, want *fipsprofile.RefusedError", err)
			}
			if refused.Field != tt.refused {
				t.Errorf("Field = %q, want %q", refused.Field, tt.refused)
			}
		})

		t.Run(tt.name+"/normal", func(t *testing.T) {
			defer fipsprofile.Force(false)()
			err := ConfigureV3(&gosnmp.GoSNMP{}, creds)
			var refused *fipsprofile.RefusedError
			if errors.As(err, &refused) {
				t.Fatalf("ConfigureV3() = %v, normal mode must not apply the profile", err)
			}
			// The approved rows must succeed in every build; the legacy rows
			// succeed only where the build registers them (not under -tags fips).
			if tt.refused == "" && err != nil {
				t.Fatalf("ConfigureV3() = %v, want accepted", err)
			}
		})
	}
}

func TestNewClientUnderTheFIPSProfile(t *testing.T) {
	tests := []struct {
		name    string
		device  config.Device
		refused bool
	}{
		{"v1", config.Device{SNMPVersion: "1", Community: "public"}, true},
		{"v2c", config.Device{SNMPVersion: "2c", Community: "public"}, true},
		{"v3 MD5", config.Device{SNMPVersion: "3", Username: "u", AuthProtocol: "MD5", AuthPassword: "authpassword"}, true},
		{"v3 SHA-1", config.Device{SNMPVersion: "3", Username: "u", AuthProtocol: "SHA", AuthPassword: "authpassword"}, true},
		{"v3 DES", config.Device{
			SNMPVersion: "3", Username: "u", AuthProtocol: "SHA256", AuthPassword: "authpassword",
			PrivProtocol: "DES", PrivPassword: "privpassword",
		}, true},
		{"v3 SHA256 AES", config.Device{
			SNMPVersion: "3", Username: "u", AuthProtocol: "SHA256", AuthPassword: "authpassword",
			PrivProtocol: "AES", PrivPassword: "privpassword",
		}, false},
	}
	for _, tt := range tests {
		dev := tt.device
		dev.Name = "d"
		dev.Address = "192.0.2.1:161"

		t.Run(tt.name+"/profile", func(t *testing.T) {
			defer fipsprofile.Force(true)()
			c, err := NewClient(&dev, 0, 0)
			if !tt.refused {
				if err != nil || c == nil {
					t.Fatalf("NewClient() = %v, %v, want a client", c, err)
				}
				return
			}
			var refused *fipsprofile.RefusedError
			if !errors.As(err, &refused) {
				t.Fatalf("NewClient() = %v, want *fipsprofile.RefusedError", err)
			}
			if c != nil {
				t.Error("NewClient() returned a client alongside a refusal")
			}
		})

		t.Run(tt.name+"/normal", func(t *testing.T) {
			defer fipsprofile.Force(false)()
			_, err := NewClient(&dev, 0, 0)
			var refused *fipsprofile.RefusedError
			if errors.As(err, &refused) {
				t.Fatalf("NewClient() = %v, normal mode must not apply the profile", err)
			}
			if !tt.refused && err != nil {
				t.Fatalf("NewClient() = %v, want accepted", err)
			}
			// v1 and v2c are accepted in normal mode in every build.
			if (tt.name == "v1" || tt.name == "v2c") && err != nil {
				t.Fatalf("NewClient() = %v, want accepted", err)
			}
		})
	}
}
