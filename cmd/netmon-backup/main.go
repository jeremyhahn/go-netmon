// Command netmon-backup backs up and restores a deployed go-netmon stack; the checks live in internal/backup.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/jeremyhahn/go-netmon/internal/backup"
)

// Build metadata, set via -ldflags at build time (the Makefile's
// VERSION_LDFLAGS).
var (
	version   = "dev"
	gitCommit = "unknown"
	buildDate = "unknown"
)

// versionString renders the build metadata as "netmon-backup --version" prints
// it, in the same form as "netmon --version".
func versionString() string {
	return version + " (commit " + gitCommit + ", built " + buildDate + ")"
}

// Exit codes. A dry run is deliberately non-zero so a scripted restore that
// forgot --apply cannot look like it succeeded.
const (
	exitOK        = 0
	exitError     = 1
	exitInterrupt = 130
)

func main() {
	// os.Exit skips deferred calls, so the signal context is released inside
	// run() before main decides the exit code. cmd/webui was restructured the
	// same way after the same finding.
	os.Exit(run())
}

// run executes the command and reports the process exit code, releasing the
// signal handler on every path.
func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := newRootCommand().ExecuteContext(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "netmon-backup: %v\n", err)
		return exitCodeFor(ctx, err)
	}
	return exitOK
}

// exitCodeFor distinguishes an interrupted run from a refused one, so that a
// caller can tell "you stopped me" from "I would not do that".
func exitCodeFor(ctx context.Context, err error) int {
	var dryRun *backup.DryRunError
	if errors.As(err, &dryRun) {
		return exitError
	}
	if ctx.Err() != nil {
		return exitInterrupt
	}
	return exitError
}
