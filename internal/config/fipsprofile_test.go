package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/fipsprofile"
)

func fipsTestDevice(version, auth, priv string) Device {
	d := Device{Name: "d", Address: "192.0.2.1", SNMPVersion: version}
	switch version {
	case "3":
		d.Username = "u"
		d.AuthProtocol = auth
		d.AuthPassword = "authpassword"
		d.PrivProtocol = priv
		d.PrivPassword = "privpassword"
	default:
		d.Community = "public"
	}
	return d
}

func TestDeviceValidateUnderTheFIPSProfile(t *testing.T) {
	tests := []struct {
		name    string
		device  Device
		refused string
	}{
		{"v1", fipsTestDevice("1", "", ""), "snmp_version"},
		{"v2c", fipsTestDevice("2c", "", ""), "snmp_version"},
		{"v3 MD5", fipsTestDevice("3", "MD5", "AES"), "auth_protocol"},
		{"v3 SHA-1", fipsTestDevice("3", "SHA", "AES"), "auth_protocol"},
		{"v3 DES", fipsTestDevice("3", "SHA256", "DES"), "priv_protocol"},
		{"v3 authNoPriv", fipsTestDevice("3", "SHA256", ""), "priv_protocol"},
		{"v3 noAuthNoPriv", fipsTestDevice("3", "", ""), "auth_protocol"},
		{"v3 unknown auth SHA3", fipsTestDevice("3", "SHA3", "AES"), "auth_protocol"},
		{"v3 unknown priv 3DES", fipsTestDevice("3", "SHA256", "3DES"), "priv_protocol"},
		{"v3 SHA256 AES", fipsTestDevice("3", "SHA256", "AES"), ""},
		{"v3 SHA512 AES256", fipsTestDevice("3", "SHA512", "AES256"), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name+"/profile", func(t *testing.T) {
			defer fipsprofile.Force(true)()
			err := tt.device.Validate()
			if tt.refused == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want accepted", err)
				}
				return
			}
			var refused *fipsprofile.RefusedError
			if !errors.As(err, &refused) {
				t.Fatalf("Validate() = %v, want *fipsprofile.RefusedError", err)
			}
			if refused.Field != tt.refused {
				t.Errorf("Field = %q, want %q", refused.Field, tt.refused)
			}
			// The wrapper must blame the refused key, not a valid snmp_version.
			var invalid *ValidationError
			if !errors.As(err, &invalid) {
				t.Fatalf("Validate() = %v, want *ValidationError", err)
			}
			if invalid.Field != tt.refused {
				t.Errorf("ValidationError.Field = %q, want %q", invalid.Field, tt.refused)
			}
		})

		t.Run(tt.name+"/normal", func(t *testing.T) {
			defer fipsprofile.Force(false)()
			if err := tt.device.Validate(); err != nil {
				t.Fatalf("Validate() = %v, normal mode must accept it", err)
			}
		})
	}
}

// A configuration file with a v2c target fails closed at load under the
// profile, and the typed error survives the configuration-level wrapper.
func TestLoadFromFileRefusesV2cUnderTheFIPSProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := `
global:
  default_poll_interval: 60
  max_workers: 10
  snmp_timeout: 10
  snmp_retries: 3
prometheus:
  remote_write_url: "http://prometheus:9090/api/v1/write"
  batch_size: 1000
  flush_interval: 10
api:
  listen_address: "127.0.0.1:18080"
metrics:
  listen_address: "0.0.0.0:9090"
devices:
  - name: "legacy"
    address: "192.0.2.1"
    snmp_version: "2c"
    community: "public"
logging:
  level: "info"
  format: "json"
  output: "stdout"
`
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	func() {
		defer fipsprofile.Force(true)()
		_, err := LoadFromFile(path)
		var refused *fipsprofile.RefusedError
		if !errors.As(err, &refused) {
			t.Fatalf("LoadFromFile() = %v, want *fipsprofile.RefusedError", err)
		}
	}()

	defer fipsprofile.Force(false)()
	if _, err := LoadFromFile(path); err != nil {
		t.Fatalf("LoadFromFile() = %v, normal mode must accept v2c", err)
	}
}
