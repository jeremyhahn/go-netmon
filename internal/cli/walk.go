package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// Walking a device's OID tree from the command line.
//
// This is the answer to "what does this box actually expose?", and it is asked
// constantly: before importing a MIB, because a module the device does not
// implement produces nothing; and after polling comes back empty, because a
// device answering NoSuchObject looks exactly like the poller not asking.
//
// The walk runs on the server, which already holds the device's credentials.
// That matters most for SNMPv3, where a user, two protocols and two passphrases
// have to agree, and re-entering them to troubleshoot is how they drift.

func newDeviceWalkCommand(env *Env) *cobra.Command {
	var (
		oid       string
		limit     int
		namedOnly bool
	)

	cmd := &cobra.Command{
		Use:     "walk <device> [oid]",
		Aliases: []string{"snmpwalk"},
		Short:   "Walk a device's OID tree",
		Long: "Walk a device's SNMP tree and print what it answers.\n\n" +
			"The OID may be given as an argument or with --oid; it defaults to\n" +
			"1.3.6.1, which is everything the device exposes. Object names are\n" +
			"filled in from the imported MIBs where they are known, so a walk is\n" +
			"useful before any MIB is imported.\n\n" +
			"A device that answers nothing under a subtree does not implement it.\n" +
			"That is a result, not a failure, and it is what decides whether a MIB\n" +
			"is worth importing.\n\n" +
			"Examples:\n" +
			"  netmon device walk bastion 1.3.6.1.4.1.21317\n" +
			"  netmon device walk core-01 --oid 1.3.6.1.2.1.2.2 --limit 2000\n" +
			"  netmon device walk bastion --named",
		Args:              cobra.RangeArgs(1, 2),
		ValidArgsFunction: env.completeNames(deviceNames),
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) == 2 {
				if oid != "" && oid != args[1] {
					return fmt.Errorf("the OID was given twice, as %q and --oid %q", args[1], oid)
				}
				oid = args[1]
			}

			return env.runWithRefresh(func(ctx context.Context, out io.Writer) error {
				api, err := env.Client()
				if err != nil {
					return err
				}
				id, err := api.ResolveDevice(ctx, args[0])
				if err != nil {
					return err
				}

				result, err := api.WalkDevice(ctx, id, oid, limit)
				if err != nil {
					return err
				}
				if namedOnly {
					result = onlyNamed(result)
				}

				if env.Opts.JSON {
					return writeJSON(out, result)
				}
				return renderWalk(out, result)
			})
		},
	}

	f := cmd.Flags()
	f.StringVar(&oid, "oid", "", "subtree to walk (default 1.3.6.1, the whole tree)")
	f.IntVar(&limit, "limit", 0, "stop after this many objects (0 for the server's default)")
	f.BoolVar(&namedOnly, "named", false,
		"show only objects an imported MIB defines, to see what is already understood")
	return cmd
}

// onlyNamed keeps the objects an imported MIB describes.
//
// The counts are left as the device reported them: a walk that answered 600
// objects of which 40 are named has to keep saying 600, or --named would read as
// the device exposing far less than it does.
func onlyNamed(result *client.SNMPWalkResponse) *client.SNMPWalkResponse {
	filtered := *result
	filtered.Varbinds = make([]client.SNMPWalkVarbind, 0, len(result.Varbinds))
	for _, varbind := range result.Varbinds {
		if varbind.Name != "" {
			filtered.Varbinds = append(filtered.Varbinds, varbind)
		}
	}
	return &filtered
}

// renderWalk prints a walk as a table.
func renderWalk(out io.Writer, result *client.SNMPWalkResponse) error {
	if !result.Success {
		return renderFailedWalk(out, result)
	}

	if len(result.Varbinds) == 0 {
		fmt.Fprintf(out, "%s answered nothing under %s.\n\n", walkSubject(result), result.Root)
		fmt.Fprintf(out, "The device does not implement anything in that subtree, so a MIB\n")
		fmt.Fprintf(out, "rooted there would produce no metrics.\n")
		return nil
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "OID\tNAME\tMODULE\tTYPE\tVALUE")
	for _, varbind := range result.Varbinds {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			varbind.OID, orDash(varbind.Name), orDash(varbind.Module),
			varbind.Type, oneLine(varbind.Value))
	}
	if err := w.Flush(); err != nil {
		return err
	}

	fmt.Fprintf(out, "\n%d objects under %s in %s", result.Count, result.Root, result.Duration)
	if shown := len(result.Varbinds); shown != result.Count {
		fmt.Fprintf(out, " (%d shown)", shown)
	}
	fmt.Fprintln(out)

	if result.Truncated {
		fmt.Fprintf(out, "Stopped at the limit; pass --limit to see more of %s.\n", result.Root)
	}
	return nil
}

// renderFailedWalk explains a walk that did not happen.
func renderFailedWalk(out io.Writer, result *client.SNMPWalkResponse) error {
	reason := result.Error
	if reason == "" {
		reason = "the walk failed for an unstated reason"
	}

	switch {
	case result.TimedOut:
		fmt.Fprintf(out, "%s did not answer: %s\n", walkSubject(result), reason)
	case result.Rejected:
		fmt.Fprintf(out, "The request was refused: %s\n", reason)
	default:
		fmt.Fprintf(out, "Walking %s failed: %s\n", walkSubject(result), reason)
	}
	return &WalkError{Device: result.Device, Root: result.Root, Reason: reason}
}

// walkSubject names the box a walk was about.
func walkSubject(result *client.SNMPWalkResponse) string {
	switch {
	case result.Device != "" && result.Address != "":
		return result.Device + " (" + result.Address + ")"
	case result.Device != "":
		return result.Device
	case result.Address != "":
		return result.Address
	default:
		return "the device"
	}
}

// WalkError reports a walk that did not complete, so the command exits non-zero
// while the explanation has already been printed.
type WalkError struct {
	Device string
	Root   string
	Reason string
}

func (e *WalkError) Error() string {
	subject := e.Device
	if subject == "" {
		subject = "device"
	}
	return "walk of " + subject + " under " + e.Root + " failed: " + e.Reason
}

// oneLine keeps a table row to one line. A walked value can be a multi-line
// sysDescr, which would otherwise break every column alignment after it.
func oneLine(value string) string {
	if !strings.ContainsAny(value, "\r\n") {
		return value
	}
	collapsed := strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(value)
	return strings.Join(strings.Fields(collapsed), " ")
}

// orDash renders an absent field as a dash rather than a hole in the table.
func orDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
