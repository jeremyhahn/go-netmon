package integration

import (
	"os"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/fipsprofile"
)

// CI runs the suite under GODEBUG=fips140=only, which activates the runtime
// FIPS profile and refuses SNMPv1/v2c. These tests drive v2c agents, so the
// profile is pinned off.
func TestMain(m *testing.M) {
	fipsprofile.Force(false)
	os.Exit(m.Run())
}
