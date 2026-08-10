// Command netmon is the command-line interface to go-netmon.
//
// It manages devices, profiles and MIBs, and displays live metrics, through
// the same management API the web UI uses.
package main

import (
	"os"

	"github.com/jeremyhahn/go-netmon/internal/cli"
)

// Build metadata, set via -ldflags at build time.
var (
	version   = "dev"
	gitCommit = "unknown"
	buildDate = "unknown"
)

func main() {
	os.Exit(cli.Execute(version, gitCommit, buildDate))
}
