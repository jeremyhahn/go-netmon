// Package cli implements the netmon command-line interface.
//
// Every command is a client of the go-netmon management API — the same API the
// web UI calls. Keeping one transport means the CLI and the GUI cannot drift
// into two half-featured surfaces.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// Options holds the settings shared by every command, bound to persistent flags.
type Options struct {
	// Server is the base URL of the management API.
	Server string

	// JSON switches output from tables to machine-readable JSON.
	JSON bool

	// Refresh, when positive, redraws the command's output on an interval
	// instead of printing once.
	Refresh float64

	// Timeout bounds a single API call.
	Timeout time.Duration
}

// Env carries everything a command needs to run: parsed options, an API client
// and the streams to write to. Commands take an *Env rather than reaching for
// globals so they stay testable.
type Env struct {
	Opts   *Options
	Out    io.Writer
	ErrOut io.Writer

	// In is where a command reads a file given as "-". A nil In reads the
	// process's standard input, so an Env built without one still works.
	In io.Reader

	client *client.Client

	// oidIndexes caches the per-device MIB index the monitor view resolves
	// metric names with, keyed by device ID. MIB definitions do not change
	// while the process runs, so a --refresh loop builds each index once.
	oidIndexes map[int64]*oidIndex
}

// in returns the stream a command reads "-" from.
func (e *Env) in() io.Reader {
	if e.In != nil {
		return e.In
	}
	return os.Stdin
}

// Client lazily constructs the API client, so commands that never call the
// server (help, completion) do not fail on a bad --server value.
func (e *Env) Client() (*client.Client, error) {
	if e.client != nil {
		return e.client, nil
	}
	c, err := client.New(e.Opts.Server, client.WithTimeout(e.Opts.Timeout))
	if err != nil {
		return nil, err
	}
	e.client = c
	return c, nil
}

// DefaultServer is used when neither --server nor NETMON_SERVER is set.
const DefaultServer = "http://localhost:8081"

// NewRootCommand builds the netmon command tree.
func NewRootCommand(version, commit, buildDate string) *cobra.Command {
	opts := &Options{}
	env := &Env{Opts: opts, Out: os.Stdout, ErrOut: os.Stderr, In: os.Stdin}
	info := newBuildInfo(version, commit, buildDate)

	root := &cobra.Command{
		Use:   "netmon",
		Short: "Manage and monitor SNMP devices",
		Long: "netmon manages and monitors SNMP devices through the go-netmon\n" +
			"management API.\n\n" +
			"Point it at a server with --server or NETMON_SERVER, for example\n" +
			"  netmon --server http://198.51.100.91:8081 device list\n\n" +
			"Read-only commands accept --refresh to redraw on an interval, and\n" +
			"--json to emit machine-readable output for scripting.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       info.String(),
	}

	// netmon ships its own completion command, restricted to the shells it is
	// tested against and documented in netmon's own terms.
	root.CompletionOptions.DisableDefaultCmd = true

	defaultServer := os.Getenv("NETMON_SERVER")
	if defaultServer == "" {
		defaultServer = DefaultServer
	}

	flags := root.PersistentFlags()
	flags.StringVar(&opts.Server, "server", defaultServer,
		"go-netmon management API URL (env NETMON_SERVER)")
	flags.BoolVar(&opts.JSON, "json", false,
		"emit JSON instead of formatted tables")
	flags.Float64Var(&opts.Refresh, "refresh", 0,
		"redraw every N seconds (e.g. 2, 0.5); 0 prints once")
	flags.DurationVar(&opts.Timeout, "timeout", client.DefaultTimeout,
		"timeout for a single API request")

	root.AddCommand(
		newDeviceCommand(env),
		newProfileCommand(env),
		newMIBCommand(env),
		newConfigCommand(env),
		newMetricsCommand(env),
		newMonitorCommand(env),
		newPollerCommand(env),
		newHealthCommand(env),
		newVersionCommand(env, info),
		newCompletionCommand(env),
	)

	return root
}

// mustMarkRequired marks flags as required on cmd. MarkFlagRequired only fails
// when the named flag was never declared, which is a wiring mistake in this
// package rather than anything a user can trigger, so it must not pass
// silently: the command would otherwise accept input it cannot serve.
func mustMarkRequired(cmd *cobra.Command, names ...string) {
	for _, name := range names {
		if err := cmd.MarkFlagRequired(name); err != nil {
			panic(fmt.Sprintf("cli: %s: cannot require undeclared flag %q: %v", cmd.Name(), name, err))
		}
	}
}

// Execute runs the CLI and returns a process exit code.
func Execute(version, commit, buildDate string) int {
	root := NewRootCommand(version, commit, buildDate)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 1
	}
	return 0
}

// contextWithSignals returns the context a refreshing view runs under, which is
// canceled by Ctrl-C. It is a variable so a test can substitute a context it
// controls and let the loop terminate.
var contextWithSignals = func() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}
