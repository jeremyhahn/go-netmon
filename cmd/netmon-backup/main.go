// Command netmon-backup backs up and restores a deployed go-netmon stack.
//
// It is a separate binary from netmon rather than a set of subcommands under it.
// netmon is a client of the management API: every one of its commands talks HTTP
// to a running stack, and its persistent flags (--server, --timeout) describe
// that conversation. This tool is the opposite kind of thing. It drives the
// container runtime and the local filesystem, it deliberately stops the stack,
// and the case it exists for is the one where nothing is running and the API
// cannot be reached at all. Folding it in would put destructive host-level
// commands behind flags that mean nothing to them, and would make the binary a
// disaster recovery depends on carry the whole API client with it.
//
// The command layer holds no logic. Every check that decides what gets
// overwritten lives in internal/backup, so there is no path to the destructive
// parts that goes around them.
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
