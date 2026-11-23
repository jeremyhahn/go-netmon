package config

import (
	"errors"
	"testing"
)

func TestValidateSealedTakesTheCommunityFromTheSeal(t *testing.T) {
	// A device whose credentials have been sealed off it has no community
	// field left; whether it has one is the sealer's to say.
	device := &Device{Name: "r1", Address: "198.51.100.101", SNMPVersion: "2c"}

	if err := device.ValidateSealed(true); err != nil {
		t.Errorf("sealed community: %v", err)
	}

	err := device.ValidateSealed(false)
	var verr *ValidationError
	if !errors.As(err, &verr) || verr.Field != "community" {
		t.Errorf("no community anywhere: got %v, want a community ValidationError", err)
	}

	if err := device.Validate(); err == nil {
		t.Error("Validate still reads the community field")
	}
}

func TestScrubCredentialsLeavesNoSecretInTheConfig(t *testing.T) {
	cfg := &Config{Devices: []Device{
		{Name: "r1", SNMPVersion: "2c", Community: "c0mm"},
		{Name: "r2", SNMPVersion: "3", Username: "u", AuthPassword: "a", PrivPassword: "p", AuthProtocol: "SHA"},
	}}

	cfg.ScrubCredentials()

	for _, d := range cfg.Devices {
		if d.Community != "" || d.AuthPassword != "" || d.PrivPassword != "" {
			t.Errorf("device %s still holds a credential: %+v", d.Name, d)
		}
	}
	if cfg.Devices[1].Username != "u" || cfg.Devices[1].AuthProtocol != "SHA" {
		t.Error("non-secret fields were scrubbed too")
	}
}
