package api

import (
	"os"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/fipsprofile"
)

// CI runs the suite under GODEBUG=fips140=only, which activates the runtime
// FIPS profile and refuses SNMPv1/v2c. These tests exercise normal-mode
// behaviour with v2c devices, so the profile is pinned off.
func TestMain(m *testing.M) {
	fipsprofile.Force(false)
	os.Exit(m.Run())
}
