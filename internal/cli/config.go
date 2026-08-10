package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// ConfigCommandTimeout is how long a configuration command waits by default.
//
// It is far longer than the timeout every other command uses because importing
// a bundle re-parses every MIB module it carries, which for a full catalog is
// minutes rather than seconds. An explicit --timeout still wins: this is the
// default, not a floor.
const ConfigCommandTimeout = 15 * time.Minute

// TerminalOutputError reports an export that would have written a compressed
// archive to a terminal.
type TerminalOutputError struct{}

func (e *TerminalOutputError) Error() string {
	return "netmon: the export bundle is a compressed archive and would corrupt this " +
		"terminal; write it to a file with --output, pipe it somewhere, or use " +
		"--document to print the configuration document instead"
}

// ConfigFileError reports a configuration file that could not be read or written.
type ConfigFileError struct {
	Op   string
	Path string
	Err  error
}

func (e *ConfigFileError) Error() string {
	return "netmon: " + e.Op + " " + strconv.Quote(e.Path) + ": " + e.Err.Error()
}

func (e *ConfigFileError) Unwrap() error { return e.Err }

// newConfigCommand builds the "netmon config" subtree.
func newConfigCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Export and import the whole configuration",
		Long: "Move a go-netmon instance's configuration — its devices, profiles, MIB\n" +
			"assignments and every per-assignment filter — between instances, or keep\n" +
			"a reviewable copy of it.\n\n" +
			"An export is a self-contained bundle: one JSON document plus the MIB\n" +
			"source files the configuration references, so importing it onto an empty\n" +
			"instance needs nothing else.\n\n" +
			"An import replaces. Anything the file does not contain is deleted, so it\n" +
			"is a dry run unless you pass --yes.",
	}

	cmd.AddCommand(
		newConfigExportCommand(env),
		newConfigImportCommand(env),
	)
	return cmd
}

func newConfigExportCommand(env *Env) *cobra.Command {
	var (
		output     string
		document   bool
		referenced bool
	)

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Write this instance's configuration",
		Long: "Write the whole configuration to stdout, or to a file with --output.\n\n" +
			"By default the output is a bundle: a gzipped tar holding config.json and\n" +
			"the MIB source files the configuration references. That is what restores\n" +
			"onto a fresh instance without the MIBs being imported by hand first.\n\n" +
			"--document prints config.json alone. It is the artifact to read, diff and\n" +
			"attach to a change request; it cannot restore onto an instance that does\n" +
			"not already hold every module it names.\n\n" +
			"The document contains SNMP community strings in clear. Treat an export as\n" +
			"a credential.\n\n" +
			"Examples:\n" +
			"  netmon config export > netmon-config.tar.gz\n" +
			"  netmon config export --output netmon-config.tar.gz\n" +
			"  netmon config export --document | jq '.devices[].name'",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if output == "" && !document && writerIsTerminal(env.Out) {
				return &TerminalOutputError{}
			}

			ctx, cancel := context.WithTimeout(context.Background(), configTimeout(cmd, env.Opts))
			defer cancel()

			api, err := env.Client()
			if err != nil {
				return err
			}
			body, err := api.ExportConfigBundle(ctx, client.ConfigExportOptions{
				DocumentOnly:       document,
				ReferencedMIBsOnly: referenced,
			})
			if err != nil {
				return err
			}

			if output == "" {
				_, writeErr := env.Out.Write(body)
				return writeErr
			}

			// An export carries community strings, so it is written for its
			// owner only rather than with the process umask.
			if err := os.WriteFile(output, body, 0o600); err != nil {
				return &ConfigFileError{Op: "write", Path: output, Err: err}
			}
			return env.reportExport(output, len(body))
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "",
		"write to this file instead of stdout")
	cmd.Flags().BoolVar(&document, "document", false,
		"print config.json alone instead of a bundle")
	cmd.Flags().BoolVar(&referenced, "referenced-mibs", false,
		"carry only the MIB modules some device or profile assigns")
	return cmd
}

// reportExport confirms a file the export wrote.
func (e *Env) reportExport(path string, size int) error {
	if e.Opts.JSON {
		return writeJSON(e.Out, changeReport{Action: "exported", Kind: "config", Ref: path})
	}
	fmt.Fprintf(e.Out, "Wrote %s (%d bytes)\n", path, size)
	return nil
}

func newConfigImportCommand(env *Env) *cobra.Command {
	var yes bool

	cmd := &cobra.Command{
		Use:   "import <file>",
		Short: "Replace this instance's configuration",
		Long: "Read a configuration bundle or document and make this instance match it.\n\n" +
			"This replaces rather than merges: a device, profile or MIB assignment the\n" +
			"file does not contain is deleted. Nothing is changed unless you pass\n" +
			"--yes; without it the command prints exactly what it would add, change\n" +
			"and delete, and exits.\n\n" +
			"Applying saves the configuration it replaces first and prints where, so a\n" +
			"wrong import can be undone by importing that file back.\n\n" +
			"MIB modules are never deleted by an import. A bundle's modules are\n" +
			"imported or refreshed before any assignment refers to them; a module the\n" +
			"file needs but neither carries nor finds here fails the whole import and\n" +
			"names the module.\n\n" +
			"Read the file from stdin with -.\n\n" +
			"Examples:\n" +
			"  netmon config import netmon-config.tar.gz\n" +
			"  netmon config import netmon-config.tar.gz --yes\n" +
			"  netmon config export --server old | netmon config import - --server new",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			bundle, err := env.readConfigFile(args[0])
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(context.Background(), configTimeout(cmd, env.Opts))
			defer cancel()

			api, err := env.Client()
			if err != nil {
				return err
			}
			report, err := api.ImportConfigBundle(ctx, bundle, client.ConfigImportOptions{Apply: yes})
			if err != nil {
				return err
			}

			if env.Opts.JSON {
				return writeJSON(env.Out, report)
			}
			renderImportReport(env.Out, report)
			return nil
		},
	}

	cmd.Flags().BoolVar(&yes, "yes", false,
		"apply the import; without it nothing is changed")
	return cmd
}

// readConfigFile reads the bundle or document to import, from a path or stdin.
func (e *Env) readConfigFile(path string) ([]byte, error) {
	if path == "-" {
		data, err := io.ReadAll(e.in())
		if err != nil {
			return nil, &ConfigFileError{Op: "read", Path: "stdin", Err: err}
		}
		return data, nil
	}

	// path is the file the operator named on the command line.
	data, err := os.ReadFile(path) // #nosec G304 -- the configuration file named on the command line
	if err != nil {
		return nil, &ConfigFileError{Op: "read", Path: path, Err: err}
	}
	return data, nil
}

// configTimeout returns how long a configuration command may take.
//
// An operator who set --timeout meant it. One who did not gets a deadline that
// fits an import of a few hundred MIB modules, rather than the default that
// suits a listing.
func configTimeout(cmd *cobra.Command, opts *Options) time.Duration {
	if flag := cmd.Root().PersistentFlags().Lookup("timeout"); flag != nil && flag.Changed {
		return opts.Timeout
	}
	return ConfigCommandTimeout
}

// writerIsTerminal reports whether writing binary output to w would land on a
// terminal. Anything that is not a character device — a file, a pipe, a test's
// buffer — is safe to write an archive to.
func writerIsTerminal(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// renderImportReport prints what an import did, or would do.
func renderImportReport(out io.Writer, report *client.ImportReport) {
	if report.Applied {
		fmt.Fprintln(out, "Configuration import — APPLIED")
	} else {
		fmt.Fprintln(out, "Configuration import — DRY RUN, nothing has been changed")
	}
	fmt.Fprintln(out)

	if !report.Changed && len(report.MIBs) == 0 {
		fmt.Fprintln(out, "This instance already matches the file. There is nothing to do.")
		return
	}

	renderMIBChanges(out, report.MIBs)
	renderRecordChanges(out, "Profiles", report.Profiles)
	renderRecordChanges(out, "Devices", report.Devices)

	fmt.Fprintln(out, summarizeCounts(report.Counts))
	if report.Applied {
		if report.SafetyExport != "" {
			fmt.Fprintf(out, "\nThe configuration this replaced was saved to %s\n", report.SafetyExport)
		}
		return
	}
	if report.Changed {
		fmt.Fprintln(out, "\nNothing has been changed. Re-run with --yes to apply.")
	}
}

// renderMIBChanges prints what happens to the MIB catalog. Modules are never
// deleted, so the list only ever grows or refreshes.
func renderMIBChanges(out io.Writer, changes []client.ConfigMIBChange) {
	if len(changes) == 0 {
		return
	}

	fmt.Fprintln(out, "MIB modules")
	table := newTable(out, "  ACTION", "MODULE", "SOURCE")
	for _, change := range changes {
		table.Row("  "+change.Action, change.Module, dash(change.File))
	}
	if err := table.Flush(); err != nil {
		return
	}
	fmt.Fprintln(out)
}

// renderRecordChanges prints one kind of record's changes, deletions first.
func renderRecordChanges(out io.Writer, heading string, changes []client.ConfigChange) {
	if len(changes) == 0 {
		return
	}

	fmt.Fprintln(out, heading)
	for _, change := range changes {
		fmt.Fprintf(out, "  %-7s %s%s\n", change.Action, change.Name, idSuffix(change.ID))
		if change.Detail != "" {
			fmt.Fprintf(out, "            %s\n", change.Detail)
		}
		for _, field := range change.Fields {
			fmt.Fprintf(out, "            %s: %s -> %s\n",
				field.Field, blank(field.From), blank(field.To))
		}
	}
	fmt.Fprintln(out)
}

// idSuffix names a record's stored ID when it has one.
func idSuffix(id int64) string {
	if id == 0 {
		return ""
	}
	return " (id " + strconv.FormatInt(id, 10) + ")"
}

// blank renders an empty side of a field change as something visible, so
// "removed" does not print as trailing whitespace.
func blank(value string) string {
	if value == "" {
		return "(none)"
	}
	return value
}

// summarizeCounts renders the one line an operator confirms an import from.
func summarizeCounts(counts client.ImportCounts) string {
	parts := []string{
		countPhrase(counts.DevicesDeleted, "device", "deleted"),
		countPhrase(counts.DevicesUpdated, "device", "updated"),
		countPhrase(counts.DevicesCreated, "device", "created"),
		countPhrase(counts.ProfilesDeleted, "profile", "deleted"),
		countPhrase(counts.ProfilesUpdated, "profile", "updated"),
		countPhrase(counts.ProfilesCreated, "profile", "created"),
		countPhrase(counts.MIBsImported, "MIB module", "imported"),
		countPhrase(counts.MIBsRefreshed, "MIB module", "refreshed"),
	}

	present := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			present = append(present, part)
		}
	}
	if len(present) == 0 {
		return "No changes."
	}
	return strings.Join(present, ", ") + "."
}

// countPhrase renders one non-zero tally, or nothing.
func countPhrase(count int, noun, verb string) string {
	if count == 0 {
		return ""
	}
	plural := noun
	if count != 1 {
		plural += "s"
	}
	return strconv.Itoa(count) + " " + plural + " " + verb
}
