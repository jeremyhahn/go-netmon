// Command netmon is the CLI to go-netmon's management API.
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
